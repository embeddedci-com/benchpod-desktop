package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// MCPSpec is the BenchPod MCP server the app runs: the same embeddedci-mcp the Claude and
// Codex plugins run, so a change to it reaches the app with no change here.
const MCPSpec = "embeddedci-mcp>=2.13,<3"

// mcpCommand finds a way to start the MCP server, in this order:
//
//  1. BENCHPOD_MCP_COMMAND: a command line (development: a checkout's venv)
//  2. embeddedci-mcp on PATH (pip/pipx install)
//  3. uvx on PATH
//  4. a private uv in the data folder, installed on first use (no sudo, profile untouched)
//
// install says whether step 4 may download uv now.
func mcpCommand(ctx context.Context, env map[string]string, dataDir string, install bool, logf func(string, ...any)) (string, []string, error) {
	if v := strings.TrimSpace(env["BENCHPOD_MCP_COMMAND"]); v != "" {
		f := strings.Fields(v)
		return f[0], f[1:], nil
	}
	// An app started from the Dock or Start menu gets a minimal PATH without the folders the
	// uv installer, pipx and Homebrew use.
	home, _ := os.UserHomeDir()
	extra := []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, ".cargo", "bin"), "/opt/homebrew/bin", "/usr/local/bin"}
	if p, err := lookPath("embeddedci-mcp", extra); err == nil {
		return p, nil, nil
	}
	if p, err := lookPath("uvx", extra); err == nil {
		return p, []string{"--from", MCPSpec, "embeddedci-mcp"}, nil
	}
	uvDir := filepath.Join(dataDir, "uv")
	uvx := filepath.Join(uvDir, exe("uvx"))
	if _, err := os.Stat(uvx); err == nil {
		return uvx, []string{"--from", MCPSpec, "embeddedci-mcp"}, nil
	}
	if !install {
		return "", nil, errors.New("the BenchPod tools are not installed yet")
	}
	logf("uv not found: installing a private copy in %s", uvDir)
	if err := installUV(ctx, uvDir); err != nil {
		return "", nil, fmt.Errorf("could not install uv (%v); install it yourself: https://docs.astral.sh/uv/getting-started/installation/", err)
	}
	return uvx, []string{"--from", MCPSpec, "embeddedci-mcp"}, nil
}

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func lookPath(name string, extra []string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, d := range extra {
		p := filepath.Join(d, exe(name))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", exec.ErrNotFound
}

// installUV runs uv's official installer into dir (UV_UNMANAGED_INSTALL: no PATH or profile
// changes), the same way the Claude and Codex plugins do.
func installUV(ctx context.Context, dir string) error {
	tmp := dir + ".tmp." + strconv.Itoa(os.Getpid())
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-ExecutionPolicy", "ByPass", "-Command", "irm https://astral.sh/uv/install.ps1 | iex")
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", "curl -LsSf https://astral.sh/uv/install.sh | sh")
	}
	cmd.Env = append(os.Environ(), "UV_UNMANAGED_INSTALL="+tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("%v: %s", err, lastLine(string(out)))
	}
	if _, err := os.Stat(filepath.Join(tmp, exe("uvx"))); err != nil {
		_ = os.RemoveAll(tmp)
		return errors.New("the installer did not produce uvx")
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.RemoveAll(tmp) // another instance finished first; either copy works
	}
	return nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
