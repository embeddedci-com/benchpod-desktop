package local

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/embeddedci-com/benchpod-desktop/chat"
)

// The bus sessions the MCP server holds open (I2C master, UART, CAN), for a page that loads
// again (a reload, the bench panel mounting anew) while they stay open:
//
//	GET /api/bus/sessions -> {"i2c": {"open": true, "sda": 2, "scl": 1, "hz": 100000, "pullups": "pod"},
//	                          "uart": {"open": false}, "can": {"open": false}}
//
// Each open bus carries the fields of the result of the call that opened it (i2c_open, uart_open,
// can_open), the agent's or a bench control's. The status tool's session flags have the last word
// when the pod is connected: a bus it reports closed is closed whatever opened it, and one it
// reports open that this app did not see open is {"open": true} with no details.

type busSessions struct {
	mu   sync.Mutex
	open map[string]json.RawMessage // bus -> the open call's structured result
}

var busOpen = map[string]string{"i2c_open": "i2c", "uart_open": "uart", "can_open": "can"}
var busClose = map[string]string{"i2c_close": "i2c", "uart_close": "uart", "can_close": "can"}

// observe follows one call that ran.
func (b *busSessions) observe(name string, structured json.RawMessage, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if bus, isOpen := busOpen[name]; isOpen {
		if b.open == nil {
			b.open = map[string]json.RawMessage{}
		}
		// A failed open may have closed the session it replaced (i2c_open does).
		delete(b.open, bus)
		if ok {
			if len(structured) == 0 || string(structured) == "null" {
				structured = json.RawMessage(`{"open":true}`)
			}
			b.open[bus] = structured
		}
		return
	}
	if bus, isClose := busClose[name]; isClose && ok {
		delete(b.open, bus)
	}
}

// reset forgets every session: the MCP server restarted or connected again.
func (b *busSessions) reset() {
	b.mu.Lock()
	b.open = nil
	b.mu.Unlock()
}

// state is every bus with its open flag; flags (from status), when not nil, override.
func (b *busSessions) state(flags map[string]bool) map[string]json.RawMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string]json.RawMessage{}
	for _, bus := range []string{"i2c", "uart", "can"} {
		raw, have := b.open[bus]
		if f, known := flags[bus]; known {
			if !f {
				delete(b.open, bus)
				have = false
			} else if !have {
				raw, have = json.RawMessage(`{"open":true}`), true
			}
		}
		if !have {
			raw = json.RawMessage(`{"open":false}`)
		}
		out[bus] = raw
	}
	return out
}

func (a *App) serveBusSessions(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	t, connected := a.tools, a.pod.Connected
	a.mu.Unlock()
	var flags map[string]bool
	if t != nil && connected {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if res, err := t.Call(ctx, "status", map[string]any{}); err == nil && !res.IsError {
			var st struct {
				Session *struct {
					I2C  bool `json:"i2c_open"`
					UART bool `json:"uart_open"`
					CAN  bool `json:"can_open"`
				} `json:"session"`
			}
			if json.Unmarshal(res.Structured, &st) == nil && st.Session != nil {
				flags = map[string]bool{"i2c": st.Session.I2C, "uart": st.Session.UART, "can": st.Session.CAN}
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	chat.WriteJSON(w, http.StatusOK, a.buses.state(flags))
}
