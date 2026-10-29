package agent

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestRealMCPServer starts the real BenchPod MCP server and lists its tools. It does not touch a
// pod. Run with BENCHPOD_MCP_COMMAND=/path/to/embeddedci-mcp.
func TestRealMCPServer(t *testing.T) {
	bin := os.Getenv("BENCHPOD_MCP_COMMAND")
	if bin == "" {
		t.Skip("BENCHPOD_MCP_COMMAND not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tools, err := StartMCP(ctx, exec.Command(bin), "benchpod-desktop-test", "0")
	if err != nil {
		t.Fatal(err)
	}
	defer tools.Close()
	list, err := tools.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, d := range list {
		names[d.Name] = true
	}
	for _, want := range []string{"connect", "status", "uart_open", "uart_read", "capture_la", "dac_output"} {
		if !names[want] {
			t.Errorf("tool %s missing (have %d tools)", want, len(list))
		}
	}
	if tools.Instructions() == "" {
		t.Error("no server instructions")
	}
	t.Logf("%d tools, %d chars of instructions", len(list), len(tools.Instructions()))
}
