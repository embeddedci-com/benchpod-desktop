package local

import (
	"encoding/json"
	"testing"

	"github.com/embeddedci-com/benchpod-desktop/agent"
)

// TestConsoleLog: the agent's and the terminal's UART traffic land in one log, in order.
func TestConsoleLog(t *testing.T) {
	var l consoleLog
	res := func(v any) agent.ToolResult {
		b, _ := json.Marshal(v)
		return agent.ToolResult{Text: string(b), Structured: b}
	}
	l.record("manual", "uart_open", nil, res(map[string]any{"open": true, "rx": 4, "tx": 5, "baud": 115200}), nil)
	l.record("agent", "uart_write", map[string]any{"text": "help"}, res(map[string]any{"written": 5}), nil)
	l.record("manual", "uart_read", nil, res(map[string]any{"text": "commands: ...\n> "}), nil)
	l.record("agent", "status", nil, res(map[string]any{"text": "not a console"}), nil)
	entries, next, open, port := l.since(0)
	if len(entries) != 3 || next != 3 || !open || port != "RX LA4 · TX LA5 · 115200 baud" {
		t.Fatalf("since(0) = %+v next %d open %v port %q", entries, next, open, port)
	}
	if entries[1].Dir != "in" || entries[1].Text != "help" || entries[2].Source != "manual" {
		t.Errorf("entries %+v", entries)
	}
	if e, _, _, _ := l.since(2); len(e) != 1 || e[0].Seq != 3 {
		t.Errorf("since(2) = %+v", e)
	}
	l.record("manual", "uart_read", nil, agent.ToolResult{Text: "no UART session is open — call uart_open first", IsError: true}, nil)
	if _, _, open, _ := l.since(0); open {
		t.Error("a read with no session should mark the console closed")
	}
	l.clear()
	if e, next, _, _ := l.since(0); len(e) != 0 || next != 3 {
		t.Errorf("after clear: %+v next %d", e, next)
	}
}
