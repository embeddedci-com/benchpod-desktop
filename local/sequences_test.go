package local

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func seqApp(t *testing.T) (*App, func(method, path string, body any, out any) int) {
	t.Helper()
	keyring.MockInit() // never the real keychain
	app, err := NewApp(t.TempDir(), "test", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	return app, func(method, path string, body any, out any) int {
		var r io.Reader = bytes.NewReader(nil)
		if s, ok := body.(string); ok {
			r = strings.NewReader(s)
		} else if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, r)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			_ = json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}
}

// TestBusSequencesLocal: create, list, rename, replace the steps and delete, kept in one file.
func TestBusSequencesLocal(t *testing.T) {
	app, do := seqApp(t)
	steps := []map[string]any{{"tool": "i2c_open", "args": map[string]any{"hz": 400000}}, {"tool": "i2c_transfer", "args": map[string]any{"addr": 0x76, "write_hex": "d0", "read": 1}, "expect": map[string]any{"ack": true, "rx_hex": "58"}}}
	var s busSequence
	if c := do("POST", "/api/bus-sequences", map[string]any{"name": " chip id ", "version": 1, "steps": steps}, &s); c != 201 || s.ID == "" || s.Name != "chip id" || !s.Mine {
		t.Fatalf("create: %d %+v", c, s)
	}
	if c := do("POST", "/api/bus-sequences", map[string]any{"name": "CHIP ID", "steps": []any{}}, nil); c != 409 {
		t.Errorf("same name: %d, want 409", c)
	}
	for name, body := range map[string]string{
		"no name":      `{"steps":[]}`,
		"no steps":     `{"name":"x"}`,
		"step no tool": `{"name":"x","steps":[{"args":{}}]}`,
		"steps object": `{"name":"x","steps":{}}`,
		"bad json":     `{"name":`,
	} {
		if c := do("POST", "/api/bus-sequences", body, nil); c != 400 {
			t.Errorf("%s: %d, want 400", name, c)
		}
	}
	if c := do("PATCH", "/api/bus-sequences/"+s.ID, map[string]any{"name": "bme chip id"}, &s); c != 200 || s.Name != "bme chip id" || !strings.Contains(string(s.Steps), "i2c_transfer") {
		t.Fatalf("rename: %d %+v", c, s)
	}
	if c := do("PATCH", "/api/bus-sequences/"+s.ID, map[string]any{"steps": []any{map[string]any{"tool": "gpio_read", "args": map[string]any{}}}}, &s); c != 200 || !strings.Contains(string(s.Steps), "gpio_read") {
		t.Fatalf("replace steps: %d %+v", c, s)
	}
	var list struct{ Sequences []busSequence }
	do("GET", "/api/bus-sequences", nil, &list)
	if len(list.Sequences) != 1 || list.Sequences[0].Name != "bme chip id" {
		t.Fatalf("list: %+v", list)
	}
	b, err := os.ReadFile(filepath.Join(app.store.Dir(), sequencesFile))
	if err != nil || !strings.Contains(string(b), `"bme chip id"`) {
		t.Fatalf("the file: %v %s", err, b)
	}
	if c := do("DELETE", "/api/bus-sequences/"+s.ID, nil, nil); c != 204 {
		t.Errorf("delete: %d", c)
	}
	if c := do("DELETE", "/api/bus-sequences/"+s.ID, nil, nil); c != 404 {
		t.Errorf("delete again: %d, want 404", c)
	}
}

// TestBusSequencesAccount: without a key the account routes say so; with one they reach
// embeddedci.com's /api/benchpod/bus-sequences with the key.
func TestBusSequencesAccount(t *testing.T) {
	app, do := seqApp(t)
	var acct struct {
		SignedIn bool `json:"signed_in"`
	}
	if do("GET", "/api/account", nil, &acct); acct.SignedIn {
		t.Fatal("signed in without a key")
	}
	if c := do("GET", "/api/account/bus-sequences", nil, nil); c != 401 {
		t.Fatalf("account list without a key: %d, want 401", c)
	}

	var seen []string
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(b))
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
		}
		_, _ = w.Write([]byte(`{"id":"abc","name":"shared one","shared":true,"mine":true,"version":1,"steps":[]}`))
	}))
	defer cloud.Close()
	app.keys.Env["BENCHPOD_API_KEY"] = "k123"
	app.keys.Env["BENCHPOD_API_BASE"] = cloud.URL + "/"

	if do("GET", "/api/account", nil, &acct); !acct.SignedIn {
		t.Fatal("not signed in with BENCHPOD_API_KEY")
	}
	var s map[string]any
	if c := do("POST", "/api/account/bus-sequences", map[string]any{"name": "shared one", "shared": true, "steps": []any{}}, &s); c != 201 || s["id"] != "abc" {
		t.Fatalf("account create: %d %+v", c, s)
	}
	if c := do("PATCH", "/api/account/bus-sequences/abc", map[string]any{"shared": false}, nil); c != 200 {
		t.Fatalf("account update: %d", c)
	}
	want := []string{
		`POST /api/benchpod/bus-sequences ApiKey k123 {"name":"shared one","shared":true,"steps":[]}`,
		`PATCH /api/benchpod/bus-sequences/abc ApiKey k123 {"shared":false}`,
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls to embeddedci.com:\n%s\nwant\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
	}

	// Offline: no embeddedci.com, whatever key there is.
	app.keys.Env["BENCHPOD_OFFLINE"] = "1"
	if do("GET", "/api/account", nil, &acct); acct.SignedIn {
		t.Fatal("signed in while offline")
	}
}
