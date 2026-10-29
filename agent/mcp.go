package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools is where the model's tools come from.
type Tools interface {
	List(ctx context.Context) ([]ToolDef, error)
	Call(ctx context.Context, name string, args map[string]any) (ToolResult, error)
	// Instructions is the tool server's own guide for the model (MCP initialize instructions),
	// or "". The loop puts it in the system prompt, so the guide stays with the server.
	Instructions() string
	Close() error
}

// MCPTools is a Tools backed by an MCP client session.
type MCPTools struct {
	session *mcp.ClientSession
}

// StartMCP launches an MCP server over stdio (cmd's stdin and stdout) and opens a session.
// cmd is not started yet; its Stderr may be set to capture the server's log.
func StartMCP(ctx context.Context, cmd *exec.Cmd, clientName, clientVersion string) (*MCPTools, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: clientVersion}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: 3 * time.Second}, nil)
	if err != nil {
		return nil, fmt.Errorf("start MCP server %s: %w", cmd.Path, err)
	}
	return &MCPTools{session: session}, nil
}

// ConnectMCP opens a session over any MCP transport (an in-memory one in tests, streamable HTTP).
func ConnectMCP(ctx context.Context, t mcp.Transport, clientName, clientVersion string) (*MCPTools, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: clientVersion}, nil)
	session, err := client.Connect(ctx, t, nil)
	if err != nil {
		return nil, err
	}
	return &MCPTools{session: session}, nil
}

func (m *MCPTools) Instructions() string {
	if r := m.session.InitializeResult(); r != nil {
		return r.Instructions
	}
	return ""
}

func (m *MCPTools) List(ctx context.Context) ([]ToolDef, error) {
	var out []ToolDef
	var cursor string
	for {
		res, err := m.session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, t := range res.Tools {
			schema, err := json.Marshal(t.InputSchema)
			if err != nil || string(schema) == "null" {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			out = append(out, ToolDef{Name: t.Name, Description: t.Description, Parameters: schema})
		}
		if res.NextCursor == "" {
			return out, nil
		}
		cursor = res.NextCursor
	}
}

func (m *MCPTools) Call(ctx context.Context, name string, args map[string]any) (ToolResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	res, err := m.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return ToolResult{}, err
	}
	return toolResult(res), nil
}

func toolResult(res *mcp.CallToolResult) ToolResult {
	var parts []string
	for _, c := range res.Content {
		switch c := c.(type) {
		case *mcp.TextContent:
			parts = append(parts, c.Text)
		case *mcp.ImageContent:
			parts = append(parts, fmt.Sprintf("[image %s, %d bytes]", c.MIMEType, len(c.Data)))
		default:
			if b, err := json.Marshal(c); err == nil {
				parts = append(parts, string(b))
			}
		}
	}
	out := ToolResult{Text: strings.Join(parts, "\n"), IsError: res.IsError}
	if res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			out.Structured = b
			if out.Text == "" {
				out.Text = string(b)
			}
		}
	}
	return out
}

func (m *MCPTools) Close() error {
	err := m.session.Close()
	// The server exiting on stdin EOF is the normal end of a stdio session.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil
	}
	return err
}
