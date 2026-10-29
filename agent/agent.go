package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Policy is what one host lets the model do. The zero value allows every tool, confirms none
// and applies the default caps.
type Policy struct {
	// Allow, when not empty, is the only tools the model sees. Deny is removed after Allow.
	Allow []string
	Deny  []string
	// Confirm names the tools that need the user's yes before each call (power, flash, raising
	// a voltage). A tool not in the list runs without asking.
	Confirm []string
	// Check runs before every call, after Confirm: return an error to refuse the call (a
	// voltage above the rail's ceiling, say). The error text goes back to the model.
	Check func(name string, args map[string]any) error
	// MaxToolCalls caps the tool calls of one run (default 25). When it is reached the model
	// gets one last turn without tools to tell the user where it stopped.
	MaxToolCalls int
	// MaxTurns caps the model calls of one run (default 30).
	MaxTurns int
	// MaxOutputTokens caps each model reply (0 = the provider's default).
	MaxOutputTokens int
	// MaxResultChars cuts each tool result the model reads (default 12000). The UI still gets
	// the whole structured result.
	MaxResultChars int
}

func (p Policy) allowed(name string) bool {
	if len(p.Allow) > 0 && !contains(p.Allow, name) {
		return false
	}
	return !contains(p.Deny, name)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func orDefault(v, d int) int {
	if v > 0 {
		return v
	}
	return d
}

// EventType names what an Event reports.
type EventType string

const (
	EventText       EventType = "text"        // Text: a piece of the assistant's reply
	EventToolCall   EventType = "tool_call"   // Call: the model asked for a tool
	EventToolResult EventType = "tool_result" // Call + Result + Millis
	EventUsage      EventType = "usage"       // Usage: the run's total so far
	EventLimit      EventType = "limit"       // Text: a cap stopped the run's tool calls
)

// Event is a step of a run, for the host to stream to its UI.
type Event struct {
	Type   EventType   `json:"type"`
	Text   string      `json:"text,omitempty"`
	Call   *ToolCall   `json:"call,omitempty"`
	Result *ToolResult `json:"result,omitempty"`
	Millis int64       `json:"ms,omitempty"`
	Usage  *Usage      `json:"usage,omitempty"`
	// Refused is set on a tool_result for a call that never ran (denied, not confirmed, failed
	// Check, bad arguments).
	Refused bool `json:"refused,omitempty"`
}

// Agent runs conversations: a model, the tools it may call and the host's policy.
type Agent struct {
	Model  Model
	Tools  Tools
	Policy Policy
	// System is the host's system prompt. The tool server's own instructions follow it.
	System string
	// Confirm asks the user about a call to a tool in Policy.Confirm. nil refuses them all.
	Confirm func(ctx context.Context, call ToolCall) (bool, error)
}

// ErrNoModelReply is returned when the model answers with neither text nor tool calls.
var ErrNoModelReply = errors.New("the model returned an empty reply")

// Run continues the conversation history (user and assistant turns, no system message) until
// the model answers without calling a tool. It returns the messages the run added, in order,
// which the host appends to its history; on an error they are the ones added before it, so a
// cancelled run still leaves a consistent conversation.
func (a *Agent) Run(ctx context.Context, history []Message, emit func(Event)) ([]Message, Usage, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	all, err := a.Tools.List(ctx)
	if err != nil {
		return nil, Usage{}, fmt.Errorf("list tools: %w", err)
	}
	var tools []ToolDef
	for _, t := range all {
		if a.Policy.allowed(t.Name) {
			tools = append(tools, t)
		}
	}

	system := strings.TrimSpace(a.System)
	if inst := strings.TrimSpace(a.Tools.Instructions()); inst != "" {
		system += "\n\n# Tool server instructions\n\n" + inst
	}
	msgs := make([]Message, 0, len(history)+8)
	if system != "" {
		msgs = append(msgs, Message{Role: RoleSystem, Content: system})
	}
	msgs = append(msgs, history...)
	start := len(msgs)
	added := func() []Message { return append([]Message(nil), msgs[start:]...) }

	maxCalls := orDefault(a.Policy.MaxToolCalls, 25)
	maxTurns := orDefault(a.Policy.MaxTurns, 30)
	maxChars := orDefault(a.Policy.MaxResultChars, 12000)
	var usage Usage
	calls := 0
	limited := false

	for turn := 0; ; turn++ {
		offerTools := tools
		if limited || turn == maxTurns-1 {
			offerTools = nil // the last turn: wrap up in words
		}
		reply, u, err := a.Model.Chat(ctx, ChatRequest{Messages: msgs, Tools: offerTools, MaxTokens: a.Policy.MaxOutputTokens},
			func(s string) { emit(Event{Type: EventText, Text: s}) })
		usage = usage.Add(u)
		emit(Event{Type: EventUsage, Usage: &usage})
		if err != nil {
			return added(), usage, err
		}
		if offerTools == nil {
			reply.ToolCalls = nil // a model that calls tools it was not offered gets no say
		}
		if reply.Content == "" && len(reply.ToolCalls) == 0 {
			return added(), usage, ErrNoModelReply
		}
		msgs = append(msgs, reply)
		if len(reply.ToolCalls) == 0 {
			return added(), usage, nil
		}

		for _, call := range reply.ToolCalls {
			c := call
			emit(Event{Type: EventToolCall, Call: &c})
			if calls >= maxCalls {
				if !limited {
					emit(Event{Type: EventLimit, Text: fmt.Sprintf("tool-call limit of %d reached for this request", maxCalls)})
				}
				limited = true
				res := ToolResult{Text: fmt.Sprintf("not run: the limit of %d tool calls for this request is reached; tell the user what you found and what is left", maxCalls), IsError: true}
				emit(Event{Type: EventToolResult, Call: &c, Result: &res, Refused: true})
				msgs = append(msgs, Message{Role: RoleTool, ToolCallID: c.ID, Content: res.Text})
				continue
			}
			calls++
			res, refused, ms := a.call(ctx, c)
			if ctx.Err() != nil {
				return added(), usage, ctx.Err()
			}
			emit(Event{Type: EventToolResult, Call: &c, Result: &res, Millis: ms, Refused: refused})
			msgs = append(msgs, Message{Role: RoleTool, ToolCallID: c.ID, Content: cut(res.Text, maxChars)})
		}
	}
}

// call runs one tool call through the policy. A refusal or a tool error is a result for the
// model to read, not an error of the run.
func (a *Agent) call(ctx context.Context, c ToolCall) (res ToolResult, refused bool, ms int64) {
	if !a.Policy.allowed(c.Name) {
		return ToolResult{Text: fmt.Sprintf("tool %q is not available here", c.Name), IsError: true}, true, 0
	}
	args := map[string]any{}
	if s := strings.TrimSpace(c.Arguments); s != "" {
		if err := json.Unmarshal([]byte(s), &args); err != nil {
			return ToolResult{Text: "the arguments are not a JSON object: " + err.Error(), IsError: true}, true, 0
		}
	}
	if contains(a.Policy.Confirm, c.Name) {
		ok := false
		if a.Confirm != nil {
			var err error
			ok, err = a.Confirm(ctx, c)
			if err != nil {
				return ToolResult{Text: "not run: " + err.Error(), IsError: true}, true, 0
			}
		}
		if !ok {
			return ToolResult{Text: "not run: the user declined this call", IsError: true}, true, 0
		}
	}
	if a.Policy.Check != nil {
		if err := a.Policy.Check(c.Name, args); err != nil {
			return ToolResult{Text: "refused: " + err.Error(), IsError: true}, true, 0
		}
	}
	t0 := time.Now()
	res, err := a.Tools.Call(ctx, c.Name, args)
	ms = time.Since(t0).Milliseconds()
	if err != nil {
		return ToolResult{Text: "tool error: " + err.Error(), IsError: true}, false, ms
	}
	return res, false, ms
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n… [cut: %d of %d characters shown]", n, len(s))
}
