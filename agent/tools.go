package agent

import (
	"context"
	"os/exec"
	"strings"
)

// ToolInfo is a tool as a person reads it in a list: its name and the first sentence of its
// description.
type ToolInfo struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

// Allowed returns the tools of defs the policy lets the model see, in their order.
func (p Policy) Allowed(defs []ToolDef) []ToolDef {
	var out []ToolDef
	for _, d := range defs {
		if p.allowed(d.Name) {
			out = append(out, d)
		}
	}
	return out
}

// Summaries turns tool definitions into the list a UI shows.
func Summaries(defs []ToolDef) []ToolInfo {
	out := make([]ToolInfo, 0, len(defs))
	for _, d := range defs {
		out = append(out, ToolInfo{Name: d.Name, Summary: FirstSentence(d.Description)})
	}
	return out
}

// FirstSentence is the start of a tool description up to its first sentence end.
func FirstSentence(s string) string {
	// Descriptions wrap across lines; a paragraph break ends the summary.
	if i := strings.Index(s, "\n\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.Join(strings.Fields(s), " ")
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '.' && s[i+1] == ' ' {
			return s[:i+1]
		}
	}
	if len(s) > 180 {
		s = s[:177] + "…"
	}
	return s
}

// ListMCPTools starts an MCP server only to read its tool list, then stops it. Listing needs no
// connection to a pod, so a host can show what the agent can do before anyone has chatted.
func ListMCPTools(ctx context.Context, cmd *exec.Cmd, clientName string) ([]ToolDef, error) {
	t, err := StartMCP(ctx, cmd, clientName, "list")
	if err != nil {
		return nil, err
	}
	defer t.Close()
	return t.List(ctx)
}
