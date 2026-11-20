package local

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/embeddedci-com/benchpod-desktop/chat"
)

// Pairing this computer with the pod (firmware 4.0.1+). Over Ethernet and Wi-Fi a pod with LAN
// pairing on answers only status to a computer whose TLS client key is not on its list; the MCP
// server then refuses the connect with PodNotPairedError (BP0081), and the page shows the kind
// not_paired (podcompat.go). Pairing goes over USB or embeddedci.com, never over the LAN: the
// bundled CLI's `lan-clients add` with --connection usb[:<name>] or embeddedci:<name>. The CLI,
// the SDK and so the MCP server share one client key per user, so the MCP server's next connect
// is paired.
//
//	GET  /api/pod/pairing                       -> {pod_name, usb, cloud: [{name, online, connection}], cloud_pick, offline}
//	POST /api/pod/pair {"connection": "usb"}    -> {ok, text, pod}
//
// cloud lists the account's pods when the user is signed in to embeddedci.com (the MCP server's
// cloud_list_devices answers); cloud_pick is the pod the app knows by name, when it is among them.

const pairTimeout = 2 * time.Minute

// A pod's name: benchpod-a1b2c3, or the name it has on embeddedci.com (URL-safe).
var podNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// The short form `benchpod-a1b2c3` the pod advertises.
var podOwnName = regexp.MustCompile(`^benchpod-[0-9a-f]{6}$`)

// podNameFromConnection is the pod's name when the connection names it: usb:<name>,
// embeddedci:<name> or benchpod-a1b2c3. "" for an address, "usb" or the LAN default.
func podNameFromConnection(conn string) string {
	conn = strings.TrimSpace(conn)
	lower := strings.ToLower(conn)
	var name string
	switch {
	case strings.HasPrefix(lower, "usb:"):
		name = conn[len("usb:"):]
	case strings.HasPrefix(lower, "embeddedci:"):
		name = conn[len("embeddedci:"):]
	case podOwnName.MatchString(lower):
		name = lower
	}
	name = strings.TrimSpace(name)
	if !podNamePattern.MatchString(name) {
		return ""
	}
	return name
}

// pairUSBConnection is the CLI connection to pair over the pod's USB-C cable: usb:<name> for the
// pod the app knows, else usb (the one pod on USB; with several the CLI lists them).
func pairUSBConnection(name string) string {
	if name == "" {
		return "usb"
	}
	return "usb:" + name
}

// pairCloudConnection is the CLI connection to pair through embeddedci.com.
func pairCloudConnection(name string) string { return "embeddedci:" + name }

// validPairConnection: only USB and embeddedci.com may pair (the pod refuses it over the LAN).
func validPairConnection(conn string) bool {
	switch {
	case conn == "usb":
		return true
	case strings.HasPrefix(conn, "usb:"):
		return podNamePattern.MatchString(conn[len("usb:"):])
	case strings.HasPrefix(conn, "embeddedci:"):
		return podNamePattern.MatchString(conn[len("embeddedci:"):])
	}
	return false
}

// pairArgv is the bundled CLI's command line that pairs this computer with the pod over conn.
func pairArgv(conn string) []string {
	return []string{"--connection", conn, "lan-clients", "add"}
}

type cloudPod struct {
	Name       string `json:"name"`
	Online     bool   `json:"online"`
	Connection string `json:"connection"`
}

type pairingOptions struct {
	PodName   string     `json:"pod_name,omitempty"`
	USB       string     `json:"usb"`
	Cloud     []cloudPod `json:"cloud"`
	CloudPick string     `json:"cloud_pick,omitempty"`
	Offline   bool       `json:"offline"`
}

// pairingOptions is how this computer can pair with the pod: over USB always, through
// embeddedci.com when the user is signed in and the account has pods.
func (a *App) pairingOptions(ctx context.Context) pairingOptions {
	st := a.store.Load()
	name := podNameFromConnection(st.Connection)
	out := pairingOptions{PodName: name, USB: pairUSBConnection(name), Cloud: []cloudPod{}, Offline: a.offline(st)}
	if out.Offline {
		return out
	}
	t, err := a.mcp(ctx)
	if err != nil {
		return out
	}
	// Not signed in (no API key, no `benchpod login`): the tool fails and there is no cloud way.
	res, err := t.Call(ctx, "cloud_list_devices", map[string]any{})
	if err != nil || res.IsError {
		return out
	}
	var list struct {
		Devices []cloudPod `json:"devices"`
	}
	_ = json.Unmarshal(res.Structured, &list)
	for _, d := range list.Devices {
		if !podNamePattern.MatchString(d.Name) {
			continue
		}
		d.Connection = pairCloudConnection(d.Name)
		out.Cloud = append(out.Cloud, d)
		if name != "" && d.Name == name {
			out.CloudPick = d.Connection
		}
	}
	return out
}

func (a *App) servePodPairing(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	chat.WriteJSON(w, http.StatusOK, a.pairingOptions(ctx))
}

type pairResult struct {
	OK   bool     `json:"ok"`
	Text string   `json:"text"`
	Pod  podState `json:"pod"`
}

// pairPod runs `benchpod lan-clients add` over conn, then connects to the pod again.
func (a *App) pairPod(ctx context.Context, conn string) pairResult {
	conn = strings.TrimSpace(conn)
	if !validPairConnection(conn) {
		return pairResult{Text: "pair over usb, usb:<name> or embeddedci:<name>"}
	}
	st := a.store.Load()
	offline := a.offline(st)
	cloud := strings.HasPrefix(conn, "embeddedci:")
	if cloud && offline {
		return pairResult{Text: "offline mode: pairing through embeddedci.com needs the internet; pair over USB"}
	}
	cli, err := a.cliCommand()
	if err != nil {
		return pairResult{Text: err.Error()}
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pairTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, cli, pairArgv(conn)...)
	cmd.Env = append(os.Environ(), "BENCHPOD_NO_FIRMWARE_REPORT=1")
	if offline {
		// The same config folder as the MCP server's in offline mode: the same client key.
		cfg := filepath.Join(a.store.Dir(), "offline-config")
		_ = os.MkdirAll(cfg, 0o700)
		cmd.Env = append(cmd.Env, "XDG_CONFIG_HOME="+cfg)
	} else if key, ok := a.accountKey(); ok && cloud {
		cmd.Env = append(cmd.Env, "BENCHPOD_API_KEY="+key)
	}
	t0 := time.Now()
	raw, err := cmd.CombinedOutput()
	text := strings.TrimSpace(stripCLIDebug(string(raw)))
	if err != nil && cctx.Err() != nil {
		text = strings.TrimSpace(text + "\n(no answer within " + pairTimeout.String() + ")")
	}
	entry := map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano), "source": "manual", "tool": "pod_setting",
		"args": map[string]any{"command": "lan-clients add", "connection": conn}, "ms": time.Since(t0).Milliseconds(),
		"is_error": err != nil, "result": truncate(text, 2000)}
	b, _ := json.Marshal(entry)
	a.mu.Lock()
	_, _ = a.actions.Write(append(b, '\n'))
	a.mu.Unlock()
	if err != nil {
		a.mu.Lock()
		pod := a.pod
		a.mu.Unlock()
		return pairResult{Text: text, Pod: pod}
	}
	// Paired: connect again (the MCP server's connect, over the app's own connection).
	_, cerr := a.connectPod(ctx, true)
	a.mu.Lock()
	pod := a.pod
	a.mu.Unlock()
	if cerr != nil && pod.Error == "" {
		pod.Error = cerr.Error()
	}
	return pairResult{OK: true, Text: text, Pod: pod}
}

func (a *App) servePodPair(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Connection string `json:"connection"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		http.Error(w, `send {"connection": "usb"}`, http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Minute)
	defer cancel()
	chat.WriteJSON(w, http.StatusOK, a.pairPod(ctx, body.Connection))
}
