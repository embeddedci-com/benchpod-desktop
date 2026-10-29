package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// sseServer answers each chat completion with the next scripted stream and records the bodies.
func sseServer(t *testing.T, streams ...[]string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		bodies = append(bodies, body)
		if n >= len(streams) {
			http.Error(w, `{"error":{"message":"no more scripted replies"}}`, 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": OPENROUTER PROCESSING\n\n")
		for _, d := range streams[n] {
			fmt.Fprintf(w, "data: %s\n\n", d)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		n++
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

func TestStreamTextAndToolCalls(t *testing.T) {
	srv, bodies := sseServer(t, []string{
		`{"choices":[{"delta":{"content":"Let me "}}]}`,
		`{"choices":[{"delta":{"content":"check."}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"sta","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"tus","arguments":"{\"a\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}},{"index":1,"function":{"name":"wiring","arguments":"{}"}}]}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.001}}`,
	})
	m := &OpenAICompatible{BaseURL: srv.URL + "/v1", APIKey: "k", Model: "test/model", Extra: map[string]any{"provider": map[string]any{"data_collection": "deny"}}}
	var streamed strings.Builder
	msg, u, err := m.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}, Tools: []ToolDef{{Name: "status"}}}, func(s string) { streamed.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "Let me check." || streamed.String() != "Let me check." {
		t.Fatalf("content %q streamed %q", msg.Content, streamed.String())
	}
	if len(msg.ToolCalls) != 2 || msg.ToolCalls[0] != (ToolCall{ID: "c1", Name: "status", Arguments: `{"a":1}`}) || msg.ToolCalls[1].Name != "wiring" || msg.ToolCalls[1].ID == "" {
		t.Fatalf("tool calls %+v", msg.ToolCalls)
	}
	if u.PromptTokens != 10 || u.CompletionTokens != 5 || u.Cost != 0.001 {
		t.Fatalf("usage %+v", u)
	}
	b := (*bodies)[0]
	if b["model"] != "test/model" || b["stream"] != true || b["provider"] == nil || b["tools"] == nil {
		t.Fatalf("request body %v", b)
	}
}

func TestProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(402)
		fmt.Fprint(w, `{"error":{"message":"Insufficient credits"}}`)
	}))
	defer srv.Close()
	m := &OpenAICompatible{BaseURL: srv.URL, Model: "x"}
	_, _, err := m.Chat(context.Background(), ChatRequest{}, nil)
	var apiErr *APIError
	if err == nil || !errorsAs(err, &apiErr) || apiErr.Status != 402 || apiErr.Message != "Insufficient credits" {
		t.Fatalf("err %v", err)
	}
}

func errorsAs(err error, target **APIError) bool {
	e, ok := err.(*APIError)
	if ok {
		*target = e
	}
	return ok
}

// fakeMCP is an in-memory MCP server with a few BenchPod-like tools.
func fakeMCP(t *testing.T, calls *[]string) *MCPTools {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-benchpod", Version: "1"}, &mcp.ServerOptions{Instructions: "Call status first."})
	type statusOut struct {
		Firmware  string  `json:"firmware"`
		LAVoltage float64 `json:"la_voltage"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "status", Description: "Pod status"}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, statusOut, error) {
		*calls = append(*calls, "status")
		return nil, statusOut{Firmware: "3.9.4", LAVoltage: 3.3}, nil
	})
	type powerIn struct {
		On bool `json:"on"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "power_on", Description: "Power the DUT"}, func(ctx context.Context, req *mcp.CallToolRequest, in powerIn) (*mcp.CallToolResult, any, error) {
		*calls = append(*calls, "power_on")
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "powered"}}}, nil, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "connect", Description: "Connect"}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
		*calls = append(*calls, "connect")
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
	})
	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	tools, err := ConnectMCP(context.Background(), ct, "test", "0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tools.Close() })
	return tools
}

func toolCallChunk(id, name, args string) string {
	b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
		map[string]any{"index": 0, "id": id, "function": map[string]any{"name": name, "arguments": args}},
	}}}}})
	return string(b)
}

func TestRunCallsToolsAndPolicy(t *testing.T) {
	srv, bodies := sseServer(t,
		[]string{toolCallChunk("c1", "status", "{}")},
		[]string{toolCallChunk("c2", "connect", "{}")},  // hidden by Deny: refused, never run
		[]string{toolCallChunk("c3", "power_on", "{}")}, // needs confirmation: declined
		[]string{`{"choices":[{"delta":{"content":"Firmware 3.9.4."}}]}`},
	)
	var calls []string
	tools := fakeMCP(t, &calls)
	var asked []string
	a := &Agent{
		Model:   &OpenAICompatible{BaseURL: srv.URL + "/v1", Model: "m"},
		Tools:   tools,
		System:  "You drive a BenchPod.",
		Policy:  Policy{Deny: []string{"connect"}, Confirm: []string{"power_on"}},
		Confirm: func(ctx context.Context, c ToolCall) (bool, error) { asked = append(asked, c.Name); return false, nil },
	}
	var events []Event
	added, _, err := a.Run(context.Background(), []Message{{Role: RoleUser, Content: "what firmware?"}}, func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "status" {
		t.Fatalf("tools run: %v", calls)
	}
	if strings.Join(asked, ",") != "power_on" {
		t.Fatalf("confirmations: %v", asked)
	}
	// assistant(status) tool assistant(connect) tool assistant(power_on) tool assistant(text)
	if len(added) != 7 || added[6].Content != "Firmware 3.9.4." {
		t.Fatalf("added %d messages: %+v", len(added), added)
	}
	if !strings.Contains(added[1].Content, `"firmware":"3.9.4"`) {
		t.Fatalf("status result %q", added[1].Content)
	}
	if !strings.Contains(added[3].Content, "not available") || !strings.Contains(added[5].Content, "declined") {
		t.Fatalf("refusals %q / %q", added[3].Content, added[5].Content)
	}
	// The system prompt carries the server's own instructions; denied tools are not offered.
	first := (*bodies)[0]
	sys := first["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "You drive a BenchPod.") || !strings.Contains(sys, "Call status first.") {
		t.Fatalf("system prompt %q", sys)
	}
	for _, tl := range first["tools"].([]any) {
		if tl.(map[string]any)["function"].(map[string]any)["name"] == "connect" {
			t.Fatal("denied tool offered to the model")
		}
	}
	var structured bool
	for _, e := range events {
		if e.Type == EventToolResult && e.Call.Name == "status" && len(e.Result.Structured) > 0 {
			structured = true
		}
	}
	if !structured {
		t.Fatal("status result lost its structured content")
	}
}

func TestRunToolCallLimit(t *testing.T) {
	srv, bodies := sseServer(t,
		[]string{toolCallChunk("c1", "status", "{}")},
		[]string{toolCallChunk("c2", "status", "{}")},
		[]string{toolCallChunk("c3", "status", "{}")}, // over the limit: not run
		[]string{`{"choices":[{"delta":{"content":"Stopped after two calls."}}]}`},
	)
	var calls []string
	a := &Agent{Model: &OpenAICompatible{BaseURL: srv.URL + "/v1", Model: "m"}, Tools: fakeMCP(t, &calls), Policy: Policy{MaxToolCalls: 2}}
	var limit bool
	added, _, err := a.Run(context.Background(), []Message{{Role: RoleUser, Content: "loop"}}, func(e Event) {
		if e.Type == EventLimit {
			limit = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !limit {
		t.Fatalf("calls %v limit %v", calls, limit)
	}
	if last := (*bodies)[3]; last["tools"] != nil {
		t.Fatal("tools offered after the limit")
	}
	if added[len(added)-1].Content != "Stopped after two calls." {
		t.Fatalf("last %+v", added[len(added)-1])
	}
}

func TestRunBadArguments(t *testing.T) {
	srv, _ := sseServer(t,
		[]string{toolCallChunk("c1", "status", "{not json")},
		[]string{`{"choices":[{"delta":{"content":"sorry"}}]}`},
	)
	var calls []string
	a := &Agent{Model: &OpenAICompatible{BaseURL: srv.URL + "/v1", Model: "m"}, Tools: fakeMCP(t, &calls)}
	added, _, err := a.Run(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 || !strings.Contains(added[1].Content, "not a JSON object") {
		t.Fatalf("calls %v result %q", calls, added[1].Content)
	}
}

func TestPromptCacheBreakpoints(t *testing.T) {
	srv, bodies := sseServer(t, []string{`{"choices":[{"delta":{"content":"ok"}}]}`})
	m := &OpenAICompatible{BaseURL: srv.URL + "/v1", Model: "anthropic/x", PromptCache: true}
	_, _, err := m.Chat(context.Background(), ChatRequest{Messages: []Message{
		{Role: RoleSystem, Content: "sys"}, {Role: RoleUser, Content: "a"}, {Role: RoleAssistant, Content: "b"}, {Role: RoleUser, Content: "c"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	msgs := (*bodies)[0]["messages"].([]any)
	isParts := func(i int) bool { _, ok := msgs[i].(map[string]any)["content"].([]any); return ok }
	if !isParts(0) || isParts(1) || isParts(2) || !isParts(3) {
		t.Fatalf("breakpoints wrong: %v", msgs)
	}
}

func TestRetriesRateLimit(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"message":"Rate limit exceeded"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	old := Retries
	defer func() { Retries = old }()
	m := &OpenAICompatible{BaseURL: srv.URL, Model: "m"}
	start := time.Now()
	msg, _, err := m.Chat(context.Background(), ChatRequest{}, nil)
	if err != nil || msg.Content != "ok" || n != 3 {
		t.Fatalf("msg %+v err %v calls %d", msg, err, n)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("waited too long")
	}
	// A 400 is not retried.
	n = 0
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n++; w.WriteHeader(400) }))
	defer bad.Close()
	if _, _, err := (&OpenAICompatible{BaseURL: bad.URL}).Chat(context.Background(), ChatRequest{}, nil); err == nil || n != 1 {
		t.Fatalf("400 retried: calls %d err %v", n, err)
	}
}

func TestToolSummaries(t *testing.T) {
	defs := []ToolDef{{Name: "status", Description: "Pod status: firmware, voltage. More."}, {Name: "connect", Description: "Connect to a pod"}, {Name: "flash", Description: "Flash a file\nover SWD.\n\nLong text"}}
	got := Summaries(Policy{Deny: []string{"connect"}}.Allowed(defs))
	if len(got) != 2 || got[0].Summary != "Pod status: firmware, voltage." || got[1].Summary != "Flash a file over SWD." {
		t.Fatalf("%+v", got)
	}
}
