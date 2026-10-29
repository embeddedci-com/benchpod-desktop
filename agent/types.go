// Package agent is the BenchPod agent loop: a chat model that calls BenchPod tools.
//
// It is provider-neutral. The model is anything that speaks the OpenAI chat-completions API
// (OpenRouter, OpenAI, a local Ollama, llama.cpp or vLLM server), and the tools come from an
// MCP server, normally the BenchPod one (embeddedci-mcp) started over stdio. Nothing here knows
// a BenchPod command: a change to the MCP server reaches every host of this loop unchanged.
//
// The hosts are the desktop app (community mode, the user's own model), and the EmbeddedCI
// server (the website demo against a simulated pod, and the AI tab against a real one). Each
// host sets a Policy: which tools the model may see, which need the user's confirmation, and
// how many tool calls and turns one run may take. Limits live here, in code, never only in
// the prompt.
package agent

import "encoding/json"

// Role is who wrote a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one entry of the conversation, in the shape the chat-completions API uses.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
	// ToolCalls is what an assistant message asked to run.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID ties a tool message to the call it answers.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ToolCall is one tool the model asked to run. Arguments is the JSON object as the model wrote
// it, which may be invalid JSON: the loop reports that back to the model rather than failing.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolDef is a tool as the model sees it.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolResult is what a tool returned. Text goes to the model (cut to Policy.MaxResultChars);
// Structured is the tool's structured output, when it has one, for the UI to draw (a capture,
// a UART log) and never sent to the model a second time.
type ToolResult struct {
	Text       string          `json:"text"`
	Structured json.RawMessage `json:"structured,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
}

// Usage counts the tokens of one model call, when the provider reports them.
type Usage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	Cost             float64 `json:"cost,omitempty"` // OpenRouter reports the call's cost in USD
}

// Add sums two usages.
func (u Usage) Add(o Usage) Usage {
	return Usage{PromptTokens: u.PromptTokens + o.PromptTokens, CompletionTokens: u.CompletionTokens + o.CompletionTokens, Cost: u.Cost + o.Cost}
}
