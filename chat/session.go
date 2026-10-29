// Package chat is one conversation with the BenchPod agent, served over HTTP.
//
// Every host speaks the same wire protocol, so one chat UI (benchpod-ui) works against all of
// them: the desktop app's local server, the website demo and the AI tab of the EmbeddedCI
// server. A host owns the Session (one per conversation) and wraps the handlers with its own
// sign-in, rate limits and quotas.
//
// The protocol:
//
//	POST {base}/send     {"text": "..."}       -> text/event-stream of Events, ends with done or error
//	POST {base}/confirm  {"id": "<call id>", "ok": true}
//	POST {base}/stop
//	GET  {base}/history                         -> {"messages": [...], "running": bool}
//
// Each SSE frame is `data: <Event JSON>`. Event types are the agent's (text, tool_call,
// tool_result, usage, limit) plus confirm (a call waits for the user), done and error.
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/embeddedci-com/benchpod-desktop/agent"
)

// Event is one SSE frame.
type Event struct {
	agent.Event
	// Error is set on type "error".
	Error string `json:"error,omitempty"`
	// Data carries a host's own event (the demo's quota, say).
	Data any `json:"data,omitempty"`
}

const (
	EventConfirm agent.EventType = "confirm" // Call: waiting for POST confirm
	EventDone    agent.EventType = "done"    // Usage: the run finished
	EventError   agent.EventType = "error"   // Error
)

// ErrBusy is returned by Send while a run is in progress.
var ErrBusy = errors.New("the agent is still answering the previous message")

// Session is one conversation. It is safe for concurrent use; one run at a time.
type Session struct {
	// Agent is the model, tools and policy. Its Confirm is set by the session. Change it
	// with SetAgent once the session is in use.
	Agent *agent.Agent
	// ConfirmTimeout is how long a call waits for the user's answer (default 5 minutes).
	ConfirmTimeout time.Duration
	// MaxHistory, when set, sends the model only the newest messages of a long conversation
	// (cut at a user message, so no tool result loses its call). The whole history is kept.
	MaxHistory int
	// OnRun is called after each run with the messages it added and its usage, for a host
	// that persists conversations or meters tokens. err is the run's error, if any.
	OnRun func(added []agent.Message, usage agent.Usage, err error)

	mu       sync.Mutex
	history  []agent.Message
	cancel   context.CancelFunc
	confirms map[string]chan bool
}

// NewSession starts a conversation, optionally with an earlier history.
func NewSession(a *agent.Agent, history []agent.Message) *Session {
	return &Session{Agent: a, history: append([]agent.Message(nil), history...), confirms: map[string]chan bool{}}
}

// SetAgent replaces the model, tools or policy; the run in progress keeps the old ones.
func (s *Session) SetAgent(a *agent.Agent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Agent = a
}

// History returns the conversation so far and whether a run is in progress.
func (s *Session) History() ([]agent.Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agent.Message(nil), s.history...), s.cancel != nil
}

// Send runs the agent on a new user message, emitting events until it finishes.
func (s *Session) Send(ctx context.Context, text string, emit func(Event)) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("empty message")
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return ErrBusy
	}
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	history := append(append([]agent.Message(nil), s.history...), agent.Message{Role: agent.RoleUser, Content: text})
	a := *s.Agent
	window := Window(history, s.MaxHistory)
	s.mu.Unlock()

	a.Confirm = func(ctx context.Context, call agent.ToolCall) (bool, error) { return s.waitConfirm(ctx, call, emit) }
	added, usage, err := a.Run(ctx, window, func(e agent.Event) { emit(Event{Event: e}) })
	cancel()

	s.mu.Lock()
	// The user's message stays in the history even when the run failed, with whatever the run
	// added before it stopped, so the conversation reads the way it happened.
	s.history = append(history, added...)
	s.cancel = nil
	s.mu.Unlock()
	if s.OnRun != nil {
		s.OnRun(append([]agent.Message{{Role: agent.RoleUser, Content: text}}, added...), usage, err)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			err = errors.New("stopped")
		}
		emit(Event{Event: agent.Event{Type: EventError, Usage: &usage}, Error: err.Error()})
		return err
	}
	emit(Event{Event: agent.Event{Type: EventDone, Usage: &usage}})
	return nil
}

func (s *Session) waitConfirm(ctx context.Context, call agent.ToolCall, emit func(Event)) (bool, error) {
	ch := make(chan bool, 1)
	s.mu.Lock()
	s.confirms[call.ID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.confirms, call.ID)
		s.mu.Unlock()
	}()
	c := call
	emit(Event{Event: agent.Event{Type: EventConfirm, Call: &c}})
	timeout := s.ConfirmTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	select {
	case ok := <-ch:
		return ok, nil
	case <-time.After(timeout):
		return false, errors.New("no answer from the user")
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// Answer delivers the user's answer to a pending confirmation. It reports whether one was
// waiting.
func (s *Session) Answer(callID string, ok bool) bool {
	s.mu.Lock()
	ch := s.confirms[callID]
	s.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- ok:
	default:
	}
	return true
}

// Stop cancels the run in progress, if any.
func (s *Session) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

// Reset clears the conversation (not while a run is in progress).
func (s *Session) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return ErrBusy
	}
	s.history = nil
	return nil
}

// ServeSend handles POST send: it streams the run as server-sent events. The run belongs to the
// request: the client closing the stream stops it.
func (s *Session) ServeSend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" {
		http.Error(w, "send {\"text\": \"...\"}", http.StatusBadRequest)
		return
	}
	if _, running := s.History(); running {
		http.Error(w, ErrBusy.Error(), http.StatusConflict)
		return
	}
	sse, ok := NewSSE(w)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	_ = s.Send(r.Context(), body.Text, sse.Send)
}

// ServeConfirm handles POST confirm.
func (s *Session) ServeConfirm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
		OK bool   `json:"ok"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil || body.ID == "" {
		http.Error(w, "send {\"id\": \"...\", \"ok\": true}", http.StatusBadRequest)
		return
	}
	if !s.Answer(body.ID, body.OK) {
		http.Error(w, "no call is waiting with that id", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ServeHistory handles GET history.
func (s *Session) ServeHistory(w http.ResponseWriter, r *http.Request) {
	msgs, running := s.History()
	WriteJSON(w, http.StatusOK, map[string]any{"messages": Visible(msgs), "running": running})
}

// Mount registers the protocol's handlers under prefix (e.g. "/api/chat") on mux.
func (s *Session) Mount(mux *http.ServeMux, prefix string) {
	mux.HandleFunc("POST "+prefix+"/send", s.ServeSend)
	mux.HandleFunc("POST "+prefix+"/confirm", s.ServeConfirm)
	mux.HandleFunc("POST "+prefix+"/stop", func(w http.ResponseWriter, r *http.Request) { s.Stop(); w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET "+prefix+"/history", s.ServeHistory)
	mux.HandleFunc("POST "+prefix+"/reset", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Reset(); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// Window returns the newest messages of history, at most max (0 = all), starting at a user
// message.
func Window(history []agent.Message, max int) []agent.Message {
	if max <= 0 || len(history) <= max {
		return history
	}
	for i := len(history) - max; i < len(history); i++ {
		if history[i].Role == agent.RoleUser {
			return history[i:]
		}
	}
	return history[len(history)-1:]
}

// Visible is the history as a UI shows it: system messages left out.
func Visible(msgs []agent.Message) []agent.Message {
	out := make([]agent.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role != agent.RoleSystem {
			out = append(out, m)
		}
	}
	return out
}

// SSE writes server-sent events.
type SSE struct {
	w  http.ResponseWriter
	f  http.Flusher
	mu sync.Mutex
}

// NewSSE starts an event stream on w.
func NewSSE(w http.ResponseWriter) (*SSE, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // proxies (Caddy, nginx) must not hold the stream back
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &SSE{w: w, f: f}, true
}

// Send writes one event. Errors (a client gone) are ignored: the request context ends the run.
func (s *SSE) Send(e Event) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.w, "data: %s\n\n", b)
	s.f.Flush()
}

// WriteJSON writes v as a JSON response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
