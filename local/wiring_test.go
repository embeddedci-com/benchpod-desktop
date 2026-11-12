package local

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/embeddedci-com/benchpod-desktop/agent"
)

// TestWiringKeptOnThisComputer: a set_wiring that ran is written to the data folder and handed to
// the next MCP server; offline, it never asks to store it on embeddedci.com.
func TestWiringKeptOnThisComputer(t *testing.T) {
	t.Setenv("BENCHPOD_WIRING", "")
	app, err := NewApp(t.TempDir(), "test", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if _, ok := app.wiringEnv(); ok {
		t.Fatal("no profile saved yet, but wiringEnv names one")
	}
	res := agent.ToolResult{Structured: json.RawMessage(`{"source":"dict","profile":{"version":1,"uart_rx":3,"uart_tx":4}}`)}
	app.afterCall("manual", "set_wiring", map[string]any{}, res, nil)
	p, ok := app.wiringEnv()
	if !ok {
		t.Fatal("set_wiring did not keep the profile")
	}
	b, _ := os.ReadFile(p)
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	if got["uart_rx"] != 3.0 {
		t.Errorf("saved profile %s", b)
	}
	app.afterCall("manual", "set_wiring", map[string]any{}, agent.ToolResult{IsError: true, Text: "invalid wiring"}, nil)
	if b2, _ := os.ReadFile(p); string(b2) != string(b) {
		t.Error("a refused set_wiring changed the saved profile")
	}

	st := app.store.Load()
	st.Offline = true
	_ = app.store.Save(st)
	args := map[string]any{"profile": map[string]any{}, "save": true}
	app.prepareCall("set_wiring", args)
	if _, ok := args["save"]; ok {
		t.Error("offline set_wiring still asks to store the profile on embeddedci.com")
	}
}
