package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Model is a chat model that can call tools.
type Model interface {
	// Chat sends the conversation and the tools, streams text through onText as it arrives,
	// and returns the assistant's whole message.
	Chat(ctx context.Context, req ChatRequest, onText func(string)) (Message, Usage, error)
}

// ChatRequest is one model call.
type ChatRequest struct {
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int // 0 = the provider's default
}

// OpenAICompatible is a Model behind the OpenAI chat-completions API. The same client serves
// OpenRouter, OpenAI, Ollama, llama.cpp and vLLM: switching is a change of BaseURL and Model.
type OpenAICompatible struct {
	// BaseURL ends before /chat/completions, e.g. https://openrouter.ai/api/v1 or
	// http://localhost:11434/v1.
	BaseURL string
	APIKey  string // empty for a local server that takes none
	Model   string
	// Headers are sent on every request (OpenRouter's HTTP-Referer and X-Title, say).
	Headers map[string]string
	// Extra is merged into the request body, for provider options the loop does not model
	// (OpenRouter's "provider": {"data_collection": "deny"}, say).
	Extra map[string]any
	// PromptCache marks cache breakpoints (cache_control) on the system prompt and on the
	// newest message, for Anthropic models behind OpenRouter: the tools and the system prompt,
	// most of every request, are then read from the cache at a tenth of the price. Leave it off
	// for providers that reject the field.
	PromptCache bool
	Client      *http.Client
}

// APIError is an error the provider returned, with its HTTP status (0 for one sent mid-stream).
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Status == 0 {
		return "model: " + e.Message
	}
	return fmt.Sprintf("model: HTTP %d: %s", e.Status, e.Message)
}

type wireToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireMessage struct {
	Role Role `json:"role"`
	// Content is a string, nil, or content parts (to carry cache_control).
	Content    any            `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type contentPart struct {
	Type         string         `json:"type"`
	Text         string         `json:"text"`
	CacheControl map[string]any `json:"cache_control,omitempty"`
}

func cachedText(s string) []contentPart {
	return []contentPart{{Type: "text", Text: s, CacheControl: map[string]any{"type": "ephemeral"}}}
}

func toWire(msgs []Message, cache bool) []wireMessage {
	out := make([]wireMessage, 0, len(msgs))
	for i, m := range msgs {
		content := m.Content
		w := wireMessage{Role: m.Role, Content: content, ToolCallID: m.ToolCallID}
		// Two breakpoints: the system prompt (tools + system, the same every call) and the
		// newest message (so the next turn of a tool loop reads the whole conversation back).
		if cache && content != "" && (m.Role == RoleSystem || (i == len(msgs)-1 && m.Role != RoleAssistant)) {
			w.Content = cachedText(content)
		}
		for i, tc := range m.ToolCalls {
			wc := wireToolCall{Index: i, ID: tc.ID, Type: "function"}
			wc.Function.Name, wc.Function.Arguments = tc.Name, tc.Arguments
			w.ToolCalls = append(w.ToolCalls, wc)
		}
		// An assistant message that only calls tools has null content in the API's own replies;
		// some servers (older vLLM) reject an empty string there.
		if m.Role == RoleAssistant && content == "" && len(m.ToolCalls) > 0 {
			w.Content = nil
		}
		out = append(out, w)
	}
	return out
}

func (c *OpenAICompatible) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	// No overall timeout: a long answer streams for minutes. The context bounds the call.
	return &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 2 * time.Minute, Proxy: http.ProxyFromEnvironment}}
}

// Retries is how often a call refused for rate limits or overload (HTTP 429, 503) is tried again,
// waiting for the provider's Retry-After or a growing backoff. Nothing has streamed by then, so
// a retry is invisible to the user apart from the wait.
var Retries = 3

func (c *OpenAICompatible) post(ctx context.Context, body []byte) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		hreq.Header.Set("Content-Type", "application/json")
		hreq.Header.Set("Accept", "text/event-stream")
		if c.APIKey != "" {
			hreq.Header.Set("Authorization", "Bearer "+c.APIKey)
		}
		for k, v := range c.Headers {
			hreq.Header.Set(k, v)
		}
		resp, err := c.client().Do(hreq)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode/100 == 2 {
			return resp, nil
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		apiErr := &APIError{Status: resp.StatusCode, Message: errorMessage(raw)}
		if (resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusServiceUnavailable) || attempt >= Retries {
			return nil, apiErr
		}
		wait := time.Duration(1<<attempt) * 2 * time.Second // 2, 4, 8 s
		if ra, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && ra > 0 {
			wait = time.Duration(min(ra, 30)) * time.Second
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, apiErr
		}
	}
}

// Chat implements Model with a streamed chat completion.
func (c *OpenAICompatible) Chat(ctx context.Context, req ChatRequest, onText func(string)) (Message, Usage, error) {
	body := map[string]any{}
	for k, v := range c.Extra {
		body[k] = v
	}
	body["model"] = c.Model
	body["messages"] = toWire(req.Messages, c.PromptCache)
	body["stream"] = true
	body["stream_options"] = map[string]any{"include_usage": true}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			params := t.Parameters
			if len(params) == 0 {
				params = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": params,
			}})
		}
		body["tools"] = tools
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return Message{}, Usage{}, err
	}
	resp, err := c.post(ctx, buf)
	if err != nil {
		return Message{}, Usage{}, err
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		// A server that ignores stream=true answers with one JSON body.
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if err != nil {
			return Message{}, Usage{}, err
		}
		return parseWhole(raw, onText)
	}
	return readStream(resp.Body, onText)
}

type chunk struct {
	Choices []struct {
		Delta struct {
			Content   string         `json:"content"`
			ToolCalls []wireToolCall `json:"tool_calls"`
		} `json:"delta"`
		Message *struct {
			Content   string         `json:"content"`
			ToolCalls []wireToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
		Cost             float64 `json:"cost"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func readStream(r io.Reader, onText func(string)) (Message, Usage, error) {
	msg := Message{Role: RoleAssistant}
	var usage Usage
	var text strings.Builder
	calls := map[int]*ToolCall{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // comments (": OPENROUTER PROCESSING"), event names, blank separators
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var ch chunk
		if json.Unmarshal([]byte(data), &ch) != nil {
			continue
		}
		if ch.Error != nil {
			return Message{}, usage, &APIError{Message: ch.Error.Message}
		}
		if ch.Usage != nil {
			usage = Usage{PromptTokens: ch.Usage.PromptTokens, CompletionTokens: ch.Usage.CompletionTokens, Cost: ch.Usage.Cost}
		}
		for _, choice := range ch.Choices {
			if choice.Delta.Content != "" {
				text.WriteString(choice.Delta.Content)
				if onText != nil {
					onText(choice.Delta.Content)
				}
			}
			for _, d := range choice.Delta.ToolCalls {
				tc := calls[d.Index]
				if tc == nil {
					tc = &ToolCall{}
					calls[d.Index] = tc
				}
				if d.ID != "" {
					tc.ID = d.ID
				}
				tc.Name += d.Function.Name
				tc.Arguments += d.Function.Arguments
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Message{}, usage, err
	}
	msg.Content = text.String()
	msg.ToolCalls = sortedCalls(calls)
	return msg, usage, nil
}

func parseWhole(raw []byte, onText func(string)) (Message, Usage, error) {
	var ch chunk
	if err := json.Unmarshal(raw, &ch); err != nil {
		return Message{}, Usage{}, fmt.Errorf("model: unreadable reply: %w", err)
	}
	if ch.Error != nil {
		return Message{}, Usage{}, &APIError{Message: ch.Error.Message}
	}
	msg := Message{Role: RoleAssistant}
	var usage Usage
	if ch.Usage != nil {
		usage = Usage{PromptTokens: ch.Usage.PromptTokens, CompletionTokens: ch.Usage.CompletionTokens, Cost: ch.Usage.Cost}
	}
	if len(ch.Choices) == 0 || ch.Choices[0].Message == nil {
		return msg, usage, errors.New("model: reply has no message")
	}
	m := ch.Choices[0].Message
	msg.Content = m.Content
	if onText != nil && m.Content != "" {
		onText(m.Content)
	}
	calls := map[int]*ToolCall{}
	for i, c := range m.ToolCalls {
		calls[i] = &ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments}
	}
	msg.ToolCalls = sortedCalls(calls)
	return msg, usage, nil
}

func sortedCalls(calls map[int]*ToolCall) []ToolCall {
	idx := make([]int, 0, len(calls))
	for i := range calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	var out []ToolCall
	for n, i := range idx {
		tc := *calls[i]
		if tc.Name == "" {
			continue
		}
		if tc.ID == "" {
			// Some local servers send no ids; the API needs one to tie the result to the call.
			tc.ID = fmt.Sprintf("call_%d", n)
		}
		out = append(out, tc)
	}
	return out
}

// errorMessage pulls the human part out of a provider's error body.
func errorMessage(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &e) == nil {
		if e.Error.Message != "" {
			return e.Error.Message
		}
		if e.Message != "" {
			return e.Message
		}
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
