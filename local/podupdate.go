package local

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/embeddedci-com/benchpod-desktop/chat"
)

// Updating the pod's own firmware. The work is the benchpod CLI's `flash-self --network`: it stages
// the image in the pod (ota_begin/ota_data), has the pod check its SHA-256 and signature, lets the
// pod write it and restart, waits for it to come back, then installs the release's blobs (FPGA
// images, the Wi-Fi bridge). The desktop app bundles the CLI next to benchpod-local; a CLI on PATH
// does too. One implementation, the one `benchpod flash-self` users run.
//
//	GET    /api/pod/firmware                -> {running, gateware, latest, latest_error, cli, files, offline}
//	POST   /api/pod/firmware/files?name=    raw bytes: a release file for an update without internet
//	DELETE /api/pod/firmware/files          forget them
//	POST   /api/pod/firmware/update         {"confirmed": true, "version": "latest" | "v3.10.2"} or
//	                                        {"confirmed": true, "files": true}
//	                                        -> text/event-stream: {"type":"line","text"} ... {"type":"done","ok","error"}

const firmwareRepo = "embeddedci-com/benchpod-firmware"

// podImage is the pod firmware's file name in a release.
const podImage = "bench_pod_stm32.bin"

// podUpdateTimeout bounds one update: the upload, the pod's flash write and restart, the blobs.
const podUpdateTimeout = 20 * time.Minute

type podUpdater struct {
	mu      sync.Mutex
	running bool
}

// cliCommand finds the benchpod CLI: BENCHPOD_CLI_COMMAND, then the copy bundled next to this
// program, then PATH and the folders installers use.
func (a *App) cliCommand() (string, error) {
	if v := strings.TrimSpace(a.keys.Env["BENCHPOD_CLI_COMMAND"]); v != "" {
		return v, nil
	}
	if self, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(self), exe("benchpod"))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	home, _ := os.UserHomeDir()
	extra := []string{"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(home, ".local", "bin"), filepath.Join(home, "go", "bin")}
	if runtime.GOOS == "windows" {
		extra = append(extra, filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "benchpod"))
	}
	if p, err := lookPath("benchpod", extra); err == nil {
		return p, nil
	}
	return "", errors.New("the benchpod CLI is not installed: the desktop app bundles it; on a source build, install it (brew install embeddedci-com/tap/benchpod) or set BENCHPOD_CLI_COMMAND")
}

func (a *App) podFirmwareDir() string { return filepath.Join(a.store.Dir(), "pod-firmware") }

func (a *App) podFirmwareFiles() []string {
	entries, _ := os.ReadDir(a.podFirmwareDir())
	var out []string
	for _, e := range entries {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// latestRelease asks GitHub for the newest pod firmware release. Nothing about the user is sent.
func latestRelease(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+firmwareRepo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return "", err
	}
	return rel.TagName, nil
}

// runningFirmware is the pod's firmware and gateware version from the last status.
func (a *App) runningFirmware() (version string, gateware int) {
	a.mu.Lock()
	raw := a.pod.Status
	a.mu.Unlock()
	var st struct {
		Firmware struct {
			Version  string `json:"version"`
			Gateware int    `json:"gateware"`
		} `json:"firmware"`
	}
	_ = json.Unmarshal(raw, &st)
	return st.Firmware.Version, st.Firmware.Gateware
}

func (a *App) servePodFirmware(w http.ResponseWriter, r *http.Request) {
	st := a.store.Load()
	offline := a.offline(st)
	running, gw := a.runningFirmware()
	out := map[string]any{"running": running, "gateware": gw, "offline": offline, "files": a.podFirmwareFiles()}
	if p, err := a.cliCommand(); err == nil {
		out["cli"] = p
	} else {
		out["cli_error"] = err.Error()
	}
	if !offline {
		if tag, err := latestRelease(r.Context()); err == nil {
			out["latest"] = strings.TrimPrefix(tag, "v")
		} else {
			out["latest_error"] = err.Error()
		}
	}
	chat.WriteJSON(w, http.StatusOK, out)
}

func (a *App) servePodFirmwareFile(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(strings.TrimSpace(r.URL.Query().Get("name")))
	ok := name == podImage || name == podImage+".sig" || name == "blobs-manifest.json" ||
		(strings.HasPrefix(name, "blob-") && (strings.HasSuffix(name, ".bin") || strings.HasSuffix(name, ".bin.sig")))
	if !ok {
		http.Error(w, "take the pod release's files: "+podImage+", "+podImage+".sig, blobs-manifest.json and the blob-*.bin files with their .sig", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(a.podFirmwareDir(), 0o700); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmp, err := os.CreateTemp(a.podFirmwareDir(), ".upload-*")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, http.MaxBytesReader(w, r.Body, 16<<20))
	_ = tmp.Close()
	if err != nil || n == 0 {
		http.Error(w, fmt.Sprintf("upload of %s failed (%v, %d bytes)", name, err, n), http.StatusBadRequest)
		return
	}
	if err := os.Rename(tmp.Name(), filepath.Join(a.podFirmwareDir(), name)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	chat.WriteJSON(w, http.StatusOK, map[string]any{"files": a.podFirmwareFiles()})
}

func (a *App) serveClearPodFirmwareFiles(w http.ResponseWriter, r *http.Request) {
	_ = os.RemoveAll(a.podFirmwareDir())
	w.WriteHeader(http.StatusNoContent)
}

// podUpdateArgs builds the flash-self command line.
func (a *App) podUpdateArgs(version string, files bool) ([]string, error) {
	st := a.store.Load()
	args := []string{"flash-self", "--network"}
	if c := strings.TrimSpace(st.Connection); c != "" {
		if strings.HasPrefix(strings.ToLower(c), "embeddedci:") {
			return nil, errors.New("a pod on embeddedci.com updates from the web app; connect it over USB or your network here")
		}
		args = append(args, "--connection", c)
	}
	switch {
	case files:
		img := filepath.Join(a.podFirmwareDir(), podImage)
		if _, err := os.Stat(img); err != nil {
			return nil, errors.New("add the release's " + podImage + " first")
		}
		args = append(args, img)
		if _, err := os.Stat(filepath.Join(a.podFirmwareDir(), "blobs-manifest.json")); err == nil {
			args = append(args, "--blobs-dir", a.podFirmwareDir())
		} else {
			args = append(args, "--skip-blobs")
		}
	case a.offline(st):
		return nil, errors.New("offline mode: the app does not download firmware; add the release's files instead")
	default:
		v := strings.TrimSpace(version)
		if v != "" && v != "latest" && !strings.HasPrefix(v, "v") {
			v = "v" + v
		}
		if v != "" && v != "latest" {
			args = append(args, "--firmware-version", v)
		}
	}
	return args, nil
}

func (a *App) servePodFirmwareUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Confirmed bool   `json:"confirmed"`
		Version   string `json:"version"`
		Files     bool   `json:"files"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil || !body.Confirmed {
		http.Error(w, `send {"confirmed": true, "version": "latest"} after the user said yes`, http.StatusBadRequest)
		return
	}
	cli, err := a.cliCommand()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	args, err := a.podUpdateArgs(body.Version, body.Files)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.updater.mu.Lock()
	if a.updater.running {
		a.updater.mu.Unlock()
		http.Error(w, "an update is already running", http.StatusConflict)
		return
	}
	a.updater.running = true
	a.updater.mu.Unlock()
	defer func() {
		a.updater.mu.Lock()
		a.updater.running = false
		a.updater.mu.Unlock()
	}()

	sse, ok := chat.NewSSE(w)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}
	send := func(v map[string]any) { sse.SendData(v) }
	before, _ := a.runningFirmware()

	// The pod restarts: let go of it first, and connect again on the next call.
	a.releasePod()

	send(map[string]any{"type": "line", "text": "benchpod " + strings.Join(args, " ")})
	// An update is never cut off half-way by a closed page: it runs to its end.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), podUpdateTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Env = os.Environ()
	if a.offline(a.store.Load()) {
		cfg := filepath.Join(a.store.Dir(), "offline-config")
		_ = os.MkdirAll(cfg, 0o700)
		cmd.Env = append(cmd.Env, "XDG_CONFIG_HOME="+cfg, "BENCHPOD_NO_FIRMWARE_REPORT=1")
	}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	t0 := time.Now()
	if err := cmd.Start(); err != nil {
		send(map[string]any{"type": "done", "ok": false, "error": err.Error()})
		return
	}
	go func() { _ = cmd.Wait(); _ = pw.Close() }()
	var tail []string
	sc := bufio.NewScanner(pr)
	for sc.Scan() {
		line := sc.Text()
		tail = append(tail, line)
		if len(tail) > 40 {
			tail = tail[1:]
		}
		send(map[string]any{"type": "line", "text": line})
	}
	ok = cmd.ProcessState != nil && cmd.ProcessState.Success()
	errText := ""
	if !ok {
		errText = "the update did not finish: read the lines above"
		if ctx.Err() != nil {
			errText = "the update took longer than " + podUpdateTimeout.String()
		}
	}
	a.logPodUpdate(args, before, ok, strings.Join(tail, "\n"), time.Since(t0))
	if ok {
		// Connect again, so the page shows the version the pod now runs.
		send(map[string]any{"type": "line", "text": "connecting to the pod again..."})
		cctx, ccancel := context.WithTimeout(ctx, 2*time.Minute)
		if _, err := a.connectPod(cctx, false); err != nil {
			send(map[string]any{"type": "line", "text": err.Error()})
		}
		ccancel()
	}
	send(map[string]any{"type": "done", "ok": ok, "error": errText})
}

func (a *App) logPodUpdate(args []string, before string, ok bool, tail string, d time.Duration) {
	entry := map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano), "source": "manual", "tool": "pod_firmware_update",
		"args": map[string]any{"cli": args, "running_before": before}, "ms": d.Milliseconds(), "is_error": !ok, "result": truncate(tail, 2000)}
	b, _ := json.Marshal(entry)
	a.mu.Lock()
	defer a.mu.Unlock()
	_, _ = a.actions.Write(append(b, '\n'))
}
