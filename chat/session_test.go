package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/embeddedci-com/benchpod-desktop/agent"
)

// scripted is a Model that returns its replies in order.
type scripted struct{ replies []agent.Message }

func (m *scripted) Chat(ctx context.Context, req agent.ChatRequest, onText func(string)) (agent.Message, agent.Usage, error) {
	r := m.replies[0]
	m.replies = m.replies[1:]
	if r.Content != "" {
		onText(r.Content)
	}
	return r, agent.Usage{PromptTokens: 1}, nil
}

type tools struct{ ran []string }

func (t *tools) List(ctx context.Context) ([]agent.ToolDef, error) {
	return []agent.ToolDef{{Name: "power_on"}}, nil
}
func (t *tools) Call(ctx context.Context, name string, args map[string]any) (agent.ToolResult, error) {
	t.ran = append(t.ran, name)
	return agent.ToolResult{Text: "on"}, nil
}
func (t *tools) Instructions() string { return "" }
func (t *tools) Close() error         { return nil }

func TestSendWithConfirmation(t *testing.T) {
	tl := &tools{}
	a := &agent.Agent{
		Model: &scripted{replies: []agent.Message{
			{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "c1", Name: "power_on", Arguments: "{}"}}},
			{Role: agent.RoleAssistant, Content: "Powered."},
		}},
		Tools:  tl,
		Policy: agent.Policy{Confirm: []string{"power_on"}},
	}
	s := NewSession(a, nil)
	var runs int
	s.OnRun = func(added []agent.Message, u agent.Usage, err error) { runs++ }
	mux := http.NewServeMux()
	s.Mount(mux, "/api/chat")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/chat/send", "application/json", strings.NewReader(`{"text":"power it"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var types []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := strings.TrimPrefix(sc.Text(), "data: ")
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		types = append(types, string(e.Type))
		if e.Type == EventConfirm {
			// A second request while the first waits: the running conversation refuses it.
			busy, _ := http.Post(srv.URL+"/api/chat/send", "application/json", strings.NewReader(`{"text":"again"}`))
			if busy.StatusCode != http.StatusConflict {
				t.Errorf("concurrent send: %d", busy.StatusCode)
			}
			busy.Body.Close()
			ok, _ := http.Post(srv.URL+"/api/chat/confirm", "application/json", strings.NewReader(`{"id":"c1","ok":true}`))
			if ok.StatusCode != http.StatusNoContent {
				t.Errorf("confirm: %d", ok.StatusCode)
			}
			ok.Body.Close()
		}
	}
	got := strings.Join(types, ",")
	want := "usage,tool_call,confirm,tool_result,text,usage,done"
	if got != want {
		t.Fatalf("events %s, want %s", got, want)
	}
	if strings.Join(tl.ran, ",") != "power_on" || runs != 1 {
		t.Fatalf("ran %v runs %d", tl.ran, runs)
	}
	msgs, running := s.History()
	if running || len(msgs) != 4 || msgs[0].Content != "power it" || msgs[3].Content != "Powered." {
		t.Fatalf("history %+v running %v", msgs, running)
	}
}

func TestConfirmTimeoutRefuses(t *testing.T) {
	tl := &tools{}
	a := &agent.Agent{
		Model: &scripted{replies: []agent.Message{
			{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "c1", Name: "power_on"}}},
			{Role: agent.RoleAssistant, Content: "Not powered."},
		}},
		Tools:  tl,
		Policy: agent.Policy{Confirm: []string{"power_on"}},
	}
	s := NewSession(a, nil)
	s.ConfirmTimeout = 50 * time.Millisecond
	if err := s.Send(context.Background(), "power it", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if len(tl.ran) != 0 {
		t.Fatal("ran without an answer")
	}
}

func TestWindowStartsAtUser(t *testing.T) {
	h := []agent.Message{
		{Role: agent.RoleUser, Content: "1"}, {Role: agent.RoleAssistant}, {Role: agent.RoleTool},
		{Role: agent.RoleAssistant, Content: "a"}, {Role: agent.RoleUser, Content: "2"}, {Role: agent.RoleAssistant, Content: "b"},
	}
	w := Window(h, 4)
	if len(w) != 2 || w[0].Content != "2" {
		t.Fatalf("window %+v", w)
	}
	if len(Window(h, 0)) != 6 {
		t.Fatal("0 = all")
	}
}
