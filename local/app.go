package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/embeddedci-com/benchpod-desktop/agent"
	"github.com/embeddedci-com/benchpod-desktop/chat"
	"github.com/embeddedci-com/benchpod-desktop/policy"
	"github.com/embeddedci-com/benchpod-desktop/providers"
)

// SystemPrompt is the desktop agent's own prompt. The MCP server's instructions follow it.
const SystemPrompt = `You are the BenchPod agent in the BenchPod desktop app. You help an embedded engineer test
and debug the board (the DUT) wired to their BenchPod: power it, read its UART console, capture
logic and analog signals, drive voltages, emulate sensors, flash firmware.

- The app has already connected to the pod; never ask the user for a connection.
- Start with status and wiring when you do not know the bench yet.
- Say what you are about to change on the hardware before you change it. The app asks the user
  to confirm risky calls; a declined call is not an error, ask what they want instead.
- When a tool fails, read its error: it names the fix (a pin to release, a voltage to set).
- Answer briefly. Quote the log lines or the measured values your conclusion rests on.
- To use a UART console: uart_open once, then for each command uart_write it and uart_read
  with an until_regex that matches the end of the reply (the console's prompt, e.g. "\n> ").
  uart_read without one returns at once with whatever has arrived, often half a line or an
  older reply.`

// MCPState is the MCP server's lifecycle, as the UI shows it.
type MCPState string

const (
	MCPStopped    MCPState = "stopped"
	MCPInstalling MCPState = "installing"
	MCPStarting   MCPState = "starting"
	MCPReady      MCPState = "ready"
	MCPFailed     MCPState = "failed"
)

// App is the desktop backend.
type App struct {
	Version string
	store   *Store
	keys    *Keys

	// connMu serializes starting the MCP server and connecting, so two first calls do not
	// start two servers.
	connMu sync.Mutex

	mu       sync.Mutex
	tools    *agent.MCPTools
	mcpState MCPState
	mcpErr   string
	mcpLog   *ringLog
	pod      podState
	session  *chat.Session
	actions  *os.File
}

type podState struct {
	Connected  bool            `json:"connected"`
	Connection string          `json:"connection,omitempty"`
	Error      string          `json:"error,omitempty"`
	Status     json.RawMessage `json:"status,omitempty"`
}

// NewApp opens the data folder and the saved conversation.
func NewApp(dataDir, version string, envFiles ...string) (*App, error) {
	store, err := NewStore(dataDir)
	if err != nil {
		return nil, err
	}
	actions, err := os.OpenFile(filepath.Join(dataDir, "actions.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	a := &App{Version: version, store: store, keys: NewKeys(envFiles...), mcpState: MCPStopped, mcpLog: newRingLog(200), actions: actions}
	var history []agent.Message
	if b, err := os.ReadFile(a.conversationPath()); err == nil {
		_ = json.Unmarshal(b, &history)
	}
	a.session = chat.NewSession(a.buildAgent(), history)
	a.session.OnRun = func(added []agent.Message, u agent.Usage, err error) { a.saveConversation() }
	return a, nil
}

func (a *App) conversationPath() string { return filepath.Join(a.store.Dir(), "conversation.json") }

func (a *App) saveConversation() {
	msgs, _ := a.session.History()
	if b, err := json.Marshal(msgs); err == nil {
		_ = os.WriteFile(a.conversationPath(), b, 0o600)
	}
}

// buildAgent assembles the agent from the current settings.
func (a *App) buildAgent() *agent.Agent {
	st := a.store.Load()
	key, _ := a.keys.Get(st.Provider)
	model := &agent.OpenAICompatible{BaseURL: st.BaseURL, APIKey: key, Model: st.Model}
	if st.Provider == "openrouter" {
		model.Headers = map[string]string{"HTTP-Referer": "https://github.com/embeddedci-com/benchpod-desktop", "X-Title": "BenchPod Desktop"}
	}
	system := SystemPrompt
	if n := strings.TrimSpace(st.DUTNotes); n != "" {
		system += "\n\n# About this bench (from the user)\n\n" + n
	}
	return &agent.Agent{Model: model, Tools: &podTools{app: a}, Policy: policy.Desktop(st.Limits, st.ConfirmRisky), System: system}
}

// Close stops the MCP server.
func (a *App) Close() {
	a.mu.Lock()
	t := a.tools
	a.tools = nil
	a.mu.Unlock()
	if t != nil {
		_ = t.Close()
	}
	_ = a.actions.Close()
}

// startMCP (re)starts the MCP server with the current settings.
func (a *App) startMCP(ctx context.Context) (*agent.MCPTools, error) {
	a.mu.Lock()
	old := a.tools
	a.tools = nil
	a.pod = podState{}
	a.mcpState, a.mcpErr = MCPStarting, ""
	a.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	fail := func(err error) (*agent.MCPTools, error) {
		a.mu.Lock()
		a.mcpState, a.mcpErr = MCPFailed, err.Error()
		a.mu.Unlock()
		return nil, err
	}
	st := a.store.Load()
	name, args, err := mcpCommand(ctx, a.keys.Env, a.store.Dir(), false, a.mcpLog.Printf)
	if err != nil {
		a.mu.Lock()
		a.mcpState = MCPInstalling
		a.mu.Unlock()
		name, args, err = mcpCommand(ctx, a.keys.Env, a.store.Dir(), true, a.mcpLog.Printf)
		if err != nil {
			return fail(err)
		}
	}
	if st.Connection != "" {
		args = append(args, "--connection", st.Connection)
	}
	if st.LAVoltage == 1.8 || st.LAVoltage == 3.3 {
		args = append(args, "--la-voltage", strconv.FormatFloat(st.LAVoltage, 'f', 1, 64))
	}
	cmd := exec.Command(name, args...)
	cmd.Stderr = a.mcpLog
	cmd.Env = os.Environ()
	if key, _ := a.keys.Get("embeddedci"); key != "" {
		cmd.Env = append(cmd.Env, "BENCHPOD_API_KEY="+key) // cloud pods
	} else if v := a.keys.Env["BENCHPOD_API_KEY"]; v != "" {
		cmd.Env = append(cmd.Env, "BENCHPOD_API_KEY="+v)
	}
	a.mcpLog.Printf("starting %s %s", name, strings.Join(args, " "))
	// The first uvx run downloads Python and the package: give it time.
	sctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	t, err := agent.StartMCP(sctx, cmd, "benchpod-desktop", a.Version)
	if err != nil {
		return fail(err)
	}
	a.mu.Lock()
	a.tools, a.mcpState = t, MCPReady
	a.mu.Unlock()
	return t, nil
}

// connectPod makes sure the MCP server runs and is connected to the pod.
func (a *App) connectPod(ctx context.Context, restart bool) (*agent.MCPTools, error) {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	a.mu.Lock()
	t, connected := a.tools, a.pod.Connected
	a.mu.Unlock()
	if t != nil && connected && !restart {
		return t, nil
	}
	if t == nil || restart {
		var err error
		if t, err = a.startMCP(ctx); err != nil {
			return nil, err
		}
	}
	res, err := t.Call(ctx, "connect", map[string]any{})
	ps := podState{Connection: a.store.Load().Connection}
	switch {
	case err != nil:
		ps.Error = err.Error()
	case res.IsError:
		ps.Error = res.Text
	default:
		ps.Connected = true
		ps.Status = res.Structured
	}
	a.mu.Lock()
	a.pod = ps
	a.mu.Unlock()
	if !ps.Connected {
		return t, errors.New("could not connect to the BenchPod: " + ps.Error)
	}
	return t, nil
}

// podTools is the agent's view of the MCP server: it connects on first use and writes every
// call to the local action log.
type podTools struct{ app *App }

// List needs the MCP server, not the pod: the page lists the tools before anything is
// connected, and a pod that is not reachable is the first tool call's error to report.
func (p *podTools) List(ctx context.Context) ([]agent.ToolDef, error) {
	t, err := p.app.mcp(ctx)
	if err != nil {
		return nil, err
	}
	return t.List(ctx)
}

// mcp returns the running MCP server, starting it (without connecting) when there is none.
func (a *App) mcp(ctx context.Context) (*agent.MCPTools, error) {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	a.mu.Lock()
	t := a.tools
	a.mu.Unlock()
	if t != nil {
		return t, nil
	}
	return a.startMCP(ctx)
}

func (p *podTools) Call(ctx context.Context, name string, args map[string]any) (agent.ToolResult, error) {
	t, err := p.app.connectPod(ctx, false)
	if err != nil {
		return agent.ToolResult{Text: err.Error(), IsError: true}, nil
	}
	t0 := time.Now()
	res, err := t.Call(ctx, name, args)
	p.app.logAction(name, args, res, err, time.Since(t0))
	return res, err
}

func (p *podTools) Instructions() string {
	p.app.mu.Lock()
	t := p.app.tools
	p.app.mu.Unlock()
	if t == nil {
		return ""
	}
	return t.Instructions()
}

func (p *podTools) Close() error { return nil }

func (a *App) logAction(name string, args map[string]any, res agent.ToolResult, err error, d time.Duration) {
	entry := map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano), "tool": name, "args": args, "ms": d.Milliseconds(), "is_error": res.IsError}
	if err != nil {
		entry["error"] = err.Error()
	} else {
		entry["result"] = truncate(res.Text, 2000)
	}
	b, _ := json.Marshal(entry)
	a.mu.Lock()
	defer a.mu.Unlock()
	_, _ = a.actions.Write(append(b, '\n'))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---- HTTP API ----

// Handler serves /api/*.
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", a.serveState)
	mux.HandleFunc("PUT /api/settings", a.serveSettings)
	mux.HandleFunc("PUT /api/key", a.serveSetKey)
	mux.HandleFunc("DELETE /api/key", a.serveDeleteKey)
	mux.HandleFunc("GET /api/models", a.serveModels)
	mux.HandleFunc("POST /api/test-model", a.serveTestModel)
	mux.HandleFunc("POST /api/pod/connect", a.servePodConnect)
	mux.HandleFunc("GET /api/mcp/log", func(w http.ResponseWriter, r *http.Request) {
		chat.WriteJSON(w, http.StatusOK, map[string]any{"lines": a.mcpLog.Lines()})
	})
	a.session.Mount(mux, "/api/chat")
	return mux
}

func (a *App) serveState(w http.ResponseWriter, r *http.Request) {
	st := a.store.Load()
	_, src := a.keys.Get(st.Provider)
	a.mu.Lock()
	mcp := map[string]any{"state": a.mcpState, "error": a.mcpErr}
	pod := a.pod
	a.mu.Unlock()
	chat.WriteJSON(w, http.StatusOK, map[string]any{
		"version":       a.Version,
		"settings":      st,
		"key_source":    src,
		"presets":       providers.Presets,
		"recommended":   providers.Recommended,
		"model_warning": providers.SmallModelWarning(st.Model),
		"mcp":           mcp,
		"pod":           pod,
		"data_dir":      a.store.Dir(),
	})
}

func (a *App) serveSettings(w http.ResponseWriter, r *http.Request) {
	old := a.store.Load()
	st := old
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&st); err != nil {
		http.Error(w, "bad settings: "+err.Error(), http.StatusBadRequest)
		return
	}
	if st.LAVoltage != 0 && st.LAVoltage != 1.8 && st.LAVoltage != 3.3 {
		http.Error(w, "la_voltage is 1.8 or 3.3", http.StatusBadRequest)
		return
	}
	if err := a.store.Save(st); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.session.SetAgent(a.buildAgent())
	if st.Connection != old.Connection || st.LAVoltage != old.LAVoltage {
		// The MCP server takes the connection on its command line: restart it on next use.
		a.mu.Lock()
		t := a.tools
		a.tools, a.pod, a.mcpState = nil, podState{}, MCPStopped
		a.mu.Unlock()
		if t != nil {
			_ = t.Close()
		}
	}
	a.serveState(w, r)
}

func (a *App) serveSetKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
		Key      string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil || body.Provider == "" {
		http.Error(w, `send {"provider": "...", "key": "..."}`, http.StatusBadRequest)
		return
	}
	if err := a.keys.Set(body.Provider, body.Key); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.session.SetAgent(a.buildAgent())
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) serveDeleteKey(w http.ResponseWriter, r *http.Request) {
	if err := a.keys.Delete(r.URL.Query().Get("provider")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.session.SetAgent(a.buildAgent())
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) serveModels(w http.ResponseWriter, r *http.Request) {
	st := a.store.Load()
	key, _ := a.keys.Get(st.Provider)
	models, err := providers.List(r.Context(), st.BaseURL, key)
	if err != nil {
		http.Error(w, "could not list models: "+err.Error(), http.StatusBadGateway)
		return
	}
	chat.WriteJSON(w, http.StatusOK, map[string]any{"models": models})
}

// serveTestModel checks that the model answers and calls a tool, without the pod.
func (a *App) serveTestModel(w http.ResponseWriter, r *http.Request) {
	st := a.store.Load()
	key, _ := a.keys.Get(st.Provider)
	m := &agent.OpenAICompatible{BaseURL: st.BaseURL, APIKey: key, Model: st.Model}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	t0 := time.Now()
	msg, usage, err := m.Chat(ctx, agent.ChatRequest{
		Messages: []agent.Message{
			{Role: agent.RoleSystem, Content: "You are testing tool calling. Call the tool you are given; do not answer in text."},
			{Role: agent.RoleUser, Content: "Read the pod's LA voltage on channel LA3."},
		},
		Tools: []agent.ToolDef{{Name: "read_la_voltage", Description: "Read the voltage of one LA channel.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"channel":{"type":"integer","minimum":1,"maximum":14}},"required":["channel"]}`)}},
		MaxTokens: 200,
	}, nil)
	res := map[string]any{"ms": time.Since(t0).Milliseconds(), "usage": usage}
	switch {
	case err != nil:
		res["ok"], res["message"] = false, err.Error()
	case len(msg.ToolCalls) == 0:
		res["ok"], res["message"] = false, "the model answered but did not call the tool: it may not support tool calling"
	default:
		var args struct {
			Channel int `json:"channel"`
		}
		_ = json.Unmarshal([]byte(msg.ToolCalls[0].Arguments), &args)
		if msg.ToolCalls[0].Name != "read_la_voltage" || args.Channel != 3 {
			res["ok"], res["message"] = false, fmt.Sprintf("the model called %s(%s): wrong tool or arguments", msg.ToolCalls[0].Name, msg.ToolCalls[0].Arguments)
		} else {
			res["ok"], res["message"] = true, "the model calls tools correctly"
		}
	}
	chat.WriteJSON(w, http.StatusOK, res)
}

func (a *App) servePodConnect(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Minute)
	defer cancel()
	_, err := a.connectPod(ctx, true)
	a.mu.Lock()
	pod := a.pod
	a.mu.Unlock()
	if err != nil && pod.Error == "" {
		pod.Error = err.Error()
	}
	chat.WriteJSON(w, http.StatusOK, pod)
}

// ringLog keeps the MCP server's last stderr lines for the UI.
type ringLog struct {
	mu    sync.Mutex
	lines []string
	max   int
	part  string
}

func newRingLog(max int) *ringLog { return &ringLog{max: max} }

func (l *ringLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.part + string(p)
	parts := strings.Split(s, "\n")
	l.part = parts[len(parts)-1]
	for _, line := range parts[:len(parts)-1] {
		l.add(line)
	}
	return len(p), nil
}

func (l *ringLog) add(line string) {
	l.lines = append(l.lines, line)
	if len(l.lines) > l.max {
		l.lines = l.lines[len(l.lines)-l.max:]
	}
}

func (l *ringLog) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.add("benchpod-desktop: " + fmt.Sprintf(format, args...))
}

func (l *ringLog) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

var _ io.Writer = (*ringLog)(nil)
