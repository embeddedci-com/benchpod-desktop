package local

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/embeddedci-com/benchpod-desktop/agent"
	"github.com/embeddedci-com/benchpod-desktop/chat"
)

// The board's console, as one log: everything that crossed the UART through the MCP tools,
// whether the agent or the bench terminal made the call. A uart_read hands out each byte once,
// so the terminal and the agent would otherwise each see only part of it.
//
//	GET  /api/console?after=<seq>  -> {"entries": [{seq, dir, text, source}], "next", "open", "port"}
//	POST /api/console/clear
//
// dir is "out" (from the board), "in" (typed to it) or "note" (a session opened or closed).

type consoleEntry struct {
	Seq    int64  `json:"seq"`
	Dir    string `json:"dir"`
	Text   string `json:"text"`
	Source string `json:"source"`
}

type consoleLog struct {
	mu      sync.Mutex
	entries []consoleEntry
	seq     int64
	size    int
	open    bool
	port    string
}

// consoleMaxBytes caps the log; the oldest entries go first.
const consoleMaxBytes = 2 << 20

func (l *consoleLog) add(dir, text, source string) {
	if text == "" {
		return
	}
	l.seq++
	l.entries = append(l.entries, consoleEntry{Seq: l.seq, Dir: dir, Text: text, Source: source})
	l.size += len(text)
	for l.size > consoleMaxBytes && len(l.entries) > 1 {
		l.size -= len(l.entries[0].Text)
		l.entries = l.entries[1:]
	}
}

// record notes one tool call's console traffic. Calls that are not about the UART are ignored.
func (l *consoleLog) record(source, tool string, args map[string]any, res agent.ToolResult, err error) {
	if err != nil {
		return
	}
	var st struct {
		Text string `json:"text"`
		Rx   int    `json:"rx"`
		Tx   int    `json:"tx"`
		Baud int    `json:"baud"`
	}
	if len(res.Structured) > 0 {
		_ = json.Unmarshal(res.Structured, &st)
	} else {
		_ = json.Unmarshal([]byte(res.Text), &st)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if res.IsError {
		// The session is gone (closed, or the pod reconnected): the terminal offers to open it.
		if tool == "uart_read" || tool == "uart_write" {
			if strings.Contains(strings.ToLower(res.Text), "no uart session") {
				l.open = false
			}
		}
		return
	}
	switch tool {
	case "uart_open":
		l.open = true
		l.port = fmt.Sprintf("RX LA%d · TX LA%d · %d baud", st.Rx, st.Tx, st.Baud)
		l.add("note", "UART open: "+l.port, source)
	case "uart_close":
		if l.open {
			l.add("note", "UART closed", source)
		}
		l.open = false
	case "uart_write":
		t, _ := args["text"].(string)
		l.add("in", t, source)
	case "uart_read", "capture_uart", "power_cycle_and_capture":
		l.add("out", st.Text, source)
	}
}

func (l *consoleLog) since(after int64) (entries []consoleEntry, next int64, open bool, port string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if e.Seq > after {
			entries = append(entries, e)
		}
	}
	return entries, l.seq, l.open, l.port
}

func (l *consoleLog) clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries, l.size = nil, 0
}

// reset forgets the session state when the MCP server restarts (its session went with it).
func (l *consoleLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open {
		l.add("note", "UART closed", "app")
	}
	l.open, l.port = false, ""
}

func (a *App) serveConsole(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	entries, next, open, port := a.console.since(after)
	if entries == nil {
		entries = []consoleEntry{}
	}
	chat.WriteJSON(w, http.StatusOK, map[string]any{"entries": entries, "next": next, "open": open, "port": port})
}
