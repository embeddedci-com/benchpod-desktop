package local

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// MCPSpec is the BenchPod MCP server the app runs: one exact embeddedci-mcp release per app
// release (bump it with each release, to the version the Claude and Codex plugins pin), so a new
// PyPI upload never reaches the app without a release of the app.
//
// RELEASE PIN (firmware 4.0.1, LAN pairing): 4.0.1 must be on PyPI before an app with it ships; until
// then the app cannot install its tools. benchpod-cli v0.10.2 in release.yml goes with it.
const MCPSpec = "embeddedci-mcp==4.0.1"

// The private uv (step 4 below) is this uv release, from its GitHub release archive, checked
// against the SHA-256 in uvArchives (the release's .sha256 files) before anything is unpacked.
// Bump the version and every hash together.
const uvVersion = "0.13.6"

var uvReleaseBase = "https://github.com/astral-sh/uv/releases/download"

type uvArchive struct {
	target string // the release asset is uv-<target>.tar.gz, or .zip on Windows
	sha256 string
}

// uvArchives is keyed by GOOS/GOARCH. Linux uses the static musl build, which runs on any
// distribution.
var uvArchives = map[string]uvArchive{
	"darwin/arm64":  {"aarch64-apple-darwin", "60fbd5c8a1ed681845eea28d9d622fcde03c51e91a43465b64a1e17db1ed3472"},
	"darwin/amd64":  {"x86_64-apple-darwin", "63ba17fdda55ad1a230d4b37fc6017ffa10c23688c83818bd3bfdf914c652a02"},
	"linux/amd64":   {"x86_64-unknown-linux-musl", "5d61f26b6c7213779eda90edc6971ea6b0b796bfb8d49a574c8ad69b59838548"},
	"linux/arm64":   {"aarch64-unknown-linux-musl", "819692e404d23e5aae6bc44c0e260f1f5492fd1910ab4693b2c650f926dbe800"},
	"windows/amd64": {"x86_64-pc-windows-msvc", "a2a8820d5eea32f08ef467ef3b7f66883a9b076d5737a2fc2fbb5f458d1bb50f"},
	"windows/arm64": {"aarch64-pc-windows-msvc", "c2426c0c8e28b53d8c440e47a18d96ad4db51e159866c0e81435b196d7f0081c"},
}

// mcpCommand finds a way to start the MCP server, in this order:
//
//  1. BENCHPOD_MCP_COMMAND: a command line (development: a checkout's venv)
//  2. embeddedci-mcp on PATH (pip/pipx install)
//  3. uvx on PATH
//  4. a private uv in the data folder, installed on first use (no sudo, profile untouched)
//
// install says whether step 4 may download uv now. offline never downloads: uv runs from its
// cache only (--offline), and with no uv the error says how to install the tools by hand.
func mcpCommand(ctx context.Context, env map[string]string, dataDir string, install, offline bool, logf func(string, ...any)) (string, []string, error) {
	uvArgs := []string{"--from", MCPSpec, "embeddedci-mcp"}
	if offline {
		uvArgs = append([]string{"--offline"}, uvArgs...)
	}
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
		return p, uvArgs, nil
	}
	uvDir := filepath.Join(dataDir, "uv")
	uvx := filepath.Join(uvDir, exe("uvx"))
	if _, err := os.Stat(uvx); err == nil {
		return uvx, uvArgs, nil
	}
	if offline {
		return "", nil, errors.New("offline mode: the BenchPod tools are not installed; install embeddedci-mcp (pip install embeddedci-mcp, from your package mirror) so it is on PATH")
	}
	if !install {
		return "", nil, errors.New("the BenchPod tools are not installed yet")
	}
	logf("uv not found: installing a private copy in %s", uvDir)
	if err := installUV(ctx, uvDir); err != nil {
		return "", nil, fmt.Errorf("could not install uv (%v); install it yourself: https://docs.astral.sh/uv/getting-started/installation/", err)
	}
	return uvx, uvArgs, nil
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

// installUV downloads the pinned uv release archive for this machine, checks its SHA-256 and
// unpacks uv and uvx into dir (no PATH or profile changes), the same way the Claude and Codex
// plugins do.
func installUV(ctx context.Context, dir string) error {
	return installUVFor(ctx, dir, runtime.GOOS+"/"+runtime.GOARCH)
}

func installUVFor(ctx context.Context, dir, platform string) error {
	a, ok := uvArchives[platform]
	if !ok {
		return fmt.Errorf("no uv download for %s", platform)
	}
	windows := strings.HasPrefix(platform, "windows/")
	ext := ".tar.gz"
	if windows {
		ext = ".zip"
	}
	url := uvReleaseBase + "/" + uvVersion + "/uv-" + a.target + ext

	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	// The archives are about 20 MB; anything far larger is not the file we pinned.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 200<<20))
	if err != nil {
		return fmt.Errorf("%s: %v", url, err)
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != a.sha256 {
		return fmt.Errorf("%s has SHA-256 %s, expected %s; not installing it", url, got, a.sha256)
	}

	tmp := dir + ".tmp." + strconv.Itoa(os.Getpid())
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	names := map[string]bool{"uv": true, "uvx": true, "uvw": true}
	if windows {
		names = map[string]bool{"uv.exe": true, "uvx.exe": true, "uvw.exe": true}
		err = unpackZip(body, tmp, names)
	} else {
		err = unpackTarGz(body, tmp, names)
	}
	uvxName := "uvx"
	if windows {
		uvxName = "uvx.exe"
	}
	if err == nil {
		if _, serr := os.Stat(filepath.Join(tmp, uvxName)); serr != nil {
			err = errors.New("the uv archive has no " + uvxName)
		}
	}
	if err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.RemoveAll(tmp) // another instance finished first; either copy works
	}
	return nil
}

// unpackTarGz writes the regular files of a .tar.gz whose base name is in names into dir, by
// base name only (the archives hold uv-<target>/uv and uv-<target>/uvx).
func unpackTarGz(data []byte, dir string, names map[string]bool) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || !names[path.Base(h.Name)] {
			continue
		}
		if err := writeExe(filepath.Join(dir, path.Base(h.Name)), tr); err != nil {
			return err
		}
	}
}

func unpackZip(data []byte, dir string, names map[string]bool) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !names[path.Base(f.Name)] {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeExe(filepath.Join(dir, path.Base(f.Name)), rc)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeExe(p string, r io.Reader) error {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
