package local

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/embeddedci-com/benchpod-sim/benchsim"
)

// TestFindPodAndBenchControlsOnSimulatedPod: the first start without an account (find the pod,
// keep its I/O voltage) and the bench controls, against benchsim and the real MCP server.
// Run with BENCHPOD_MCP_COMMAND=/path/to/embeddedci-mcp.
func TestFindPodAndBenchControlsOnSimulatedPod(t *testing.T) {
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
	_ = os.WriteFile(wiring, []byte(benchsim.WiringProfileJSON), 0o600)
	t.Setenv("BENCHPOD_TLS", "off")
	t.Setenv("BENCHPOD_WIRING", wiring)

	app, err := NewApp(t.TempDir(), "test", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	post := func(path string, body any, out any) {
		b, _ := json.Marshal(body)
		resp, err := (&http.Client{Timeout: 3 * time.Minute}).Post(srv.URL+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: HTTP %d", path, resp.StatusCode)
		}
		_ = json.NewDecoder(resp.Body).Decode(out)
	}

	var found foundPod
	post("/api/pod/find", map[string]any{"connection": ln.Addr().String()}, &found)
	if !found.Connected || found.Version == "" {
		t.Fatalf("find: %+v", found)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if f := app.findPod(ctx, "127.0.0.1:1"); f.Connected || f.Error == "" {
		t.Errorf("a port with no pod: %+v", f)
	}
	post("/api/pod/find", map[string]any{"connection": ln.Addr().String()}, &found)

	var r toolResponse
	post("/api/tool/power_on", map[string]any{"args": map[string]any{}}, &r)
	if !r.Confirm {
		t.Fatalf("power_on without a yes: %+v", r)
	}
	post("/api/tool/power_on", map[string]any{"args": map[string]any{}, "confirmed": true}, &r)
	if r.IsError {
		t.Fatalf("power_on: %s", r.Text)
	}
	post("/api/tool/power_status", map[string]any{"args": map[string]any{}, "poll": true}, &r)
	var ps struct {
		Internal struct {
			Enabled bool `json:"enabled"`
		} `json:"internal"`
	}
	_ = json.Unmarshal(r.Structured, &ps)
	if !ps.Internal.Enabled {
		t.Errorf("power_status after power_on: %s", r.Text)
	}
	post("/api/tool/uart_open", map[string]any{"args": map[string]any{}}, &r)
	if r.IsError {
		t.Fatalf("uart_open: %s", r.Text)
	}
	if _, _, open, _ := app.console.since(0); !open {
		t.Error("the console log does not know the UART is open")
	}
}
