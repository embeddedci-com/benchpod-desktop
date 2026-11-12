package local

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/embeddedci-com/benchpod-desktop/chat"
	"github.com/embeddedci-com/benchpod-desktop/policy"
)

// The bench controls beside the chat (power, console, captures, outputs) call the BenchPod MCP
// tools by hand, through the same MCP session as the agent and the same policy:
//
//	POST /api/tool/{name}  {"args": {...}, "confirmed": false, "poll": false}
//	  -> {"text", "structured", "is_error", "refused", "confirm", "ms"}
//
// A tool the policy does not allow is refused. A risky tool (power, outputs, flash) answers
// confirm=true without running until the page sends it again with confirmed=true, after the
// user said yes; with confirmations off in Settings it runs at once. The limits (DAC volts,
// loop current) refuse a call above them whoever makes it. poll marks a read-only call the
// page repeats (a live current reading): it is not written to the action log.

type toolRequest struct {
	Args      map[string]any `json:"args"`
	Confirmed bool           `json:"confirmed"`
	Poll      bool           `json:"poll"`
}

type toolResponse struct {
	Text       string          `json:"text"`
	Structured json.RawMessage `json:"structured,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	// Refused: the call never ran (not allowed here, above a limit).
	Refused bool `json:"refused,omitempty"`
	// Confirm: the call waits for the user's yes; send it again with confirmed=true.
	Confirm bool  `json:"confirm,omitempty"`
	Millis  int64 `json:"ms,omitempty"`
}

// callManual runs one bench-control call through the policy.
func (a *App) callManual(ctx context.Context, name string, req toolRequest) toolResponse {
	if req.Args == nil {
		req.Args = map[string]any{}
	}
	pol := a.policy(a.store.Load())
	if !pol.Allows(name) {
		return toolResponse{Text: "tool " + name + " is not available here", IsError: true, Refused: true}
	}
	if pol.NeedsConfirm(name) && !req.Confirmed {
		return toolResponse{Confirm: true}
	}
	if pol.Check != nil {
		if err := pol.Check(name, req.Args); err != nil {
			return toolResponse{Text: "refused: " + err.Error(), IsError: true, Refused: true}
		}
	}
	t, err := a.connectPod(ctx, false)
	if err != nil {
		return toolResponse{Text: err.Error(), IsError: true}
	}
	t0 := time.Now()
	res, err := t.Call(ctx, name, req.Args)
	d := time.Since(t0)
	if !req.Poll || !slices.Contains(policy.ReadOnly, name) {
		a.logAction("manual", name, req.Args, res, err, d)
	}
	a.console.record("manual", name, req.Args, res, err)
	if err != nil {
		return toolResponse{Text: "tool error: " + err.Error(), IsError: true, Millis: d.Milliseconds()}
	}
	return toolResponse{Text: res.Text, Structured: res.Structured, IsError: res.IsError, Millis: d.Milliseconds()}
}

func (a *App) serveTool(w http.ResponseWriter, r *http.Request) {
	var req toolRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
		http.Error(w, `send {"args": {...}}`, http.StatusBadRequest)
		return
	}
	// A capture or a flash can take minutes; the page gives up first if it wants to.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	chat.WriteJSON(w, http.StatusOK, a.callManual(ctx, r.PathValue("name"), req))
}
