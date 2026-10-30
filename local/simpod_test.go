package local

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/embeddedci-com/benchpod-sim/benchsim"
)

// TestFirstRunOnSimulatedPod is the app as a user meets it: choose a model and a pod in the
// settings, ask, allow the hardware call, read the boot log. The pod is benchsim, the MCP server
// is the real one and the model is scripted. Run with BENCHPOD_MCP_COMMAND=/path/to/embeddedci-mcp.
func TestFirstRunOnSimulatedPod(t *testing.T) {
	if os.Getenv("BENCHPOD_MCP_COMMAND") == "" {
		t.Skip("BENCHPOD_MCP_COMMAND not set")
	}
	sim := benchsim.New(benchsim.WithLaVoltage(3300))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = sim.Serve(ln) }()
	t.Cleanup(func() { _ = sim.Close() })
	wiring := filepath.Join(t.TempDir(), "wiring.json")
	if err := os.WriteFile(wiring, []byte(benchsim.WiringProfileJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BENCHPOD_TLS", "off")
	t.Setenv("BENCHPOD_WIRING", wiring)

	var toolResult atomic.Value
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		var delta map[string]any
		last := body.Messages[len(body.Messages)-1]
		if last.Role == "tool" {
			toolResult.Store(fmt.Sprint(last.Content))
			delta = map[string]any{"content": "It booted."}
		} else {
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "c1", "function": map[string]any{
				"name": "power_cycle_and_capture", "arguments": `{"until_regex":"APP_OK","duration":10}`}}}}
		}
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
	}))
	defer model.Close()

	app, err := NewApp(t.TempDir(), "test", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	settings, _ := json.Marshal(map[string]any{"provider": "custom", "base_url": model.URL, "model": "scripted",
		"connection": ln.Addr().String(), "confirm_risky": true})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings", bytes.NewReader(settings))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("settings: %v %v", err, resp)
	}
	resp.Body.Close()

	send := strings.NewReader(`{"text":"Power-cycle the board and show me its boot log."}`)
	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err = client.Post(srv.URL+"/api/chat/send", "application/json", send)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var events []string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var e struct {
			Type  string `json:"type"`
			Error string `json:"error"`
			Call  struct {
				ID string `json:"id"`
			} `json:"call"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		events = append(events, e.Type)
		switch e.Type {
		case "confirm":
			r, err := http.Post(srv.URL+"/api/chat/confirm", "application/json",
				strings.NewReader(fmt.Sprintf(`{"id":%q,"ok":true}`, e.Call.ID)))
			if err != nil || r.StatusCode != http.StatusNoContent {
				t.Fatalf("confirm: %v %v", err, r)
			}
			r.Body.Close()
		case "error":
			t.Fatalf("the run failed: %s (events %v)", e.Error, events)
		}
	}
	got := strings.Join(events, ",")
	if !strings.Contains(got, "confirm") || !strings.Contains(got, "done") {
		t.Fatalf("events: %s", got)
	}
	log, _ := toolResult.Load().(string)
	if !strings.Contains(log, "PROCESS-NODE") || !strings.Contains(log, "APP_OK") {
		t.Fatalf("the model did not get the boot log: %.400s", log)
	}
}
