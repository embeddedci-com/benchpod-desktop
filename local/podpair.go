package local

import (
	"context"
	"encoding/json"
	"errors"
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
// bundled CLI's `lan-clients add` with --connection usb[:<name>] or embeddedci:<name>, or with the
// app's own LAN connection (an address or a name), where the CLI reads the pod's status and pairs
// over that pod's own USB link (usb.ip) when its cable is in this computer. The CLI,
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

// The short form `benchpod-a1b2c3` the pod advertises, with or without .local.
const podOwnNameRe = `benchpod-[0-9a-f]{6}`

var podOwnName = regexp.MustCompile(`^` + podOwnNameRe + `(\.local)?$`)

// A LAN connection the app may hand the CLI: host or IPv4[:port], no spaces, never a flag.
var lanConnPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}(:[0-9]{1,5})?$`)

// isLANConnection: conn is a network address or name (192.168.1.164, benchpod-a1b2c3.local),
// not usb, usb:<name>, embeddedci:<name> or a serial device.
func isLANConnection(conn string) bool {
	lower := strings.ToLower(strings.TrimSpace(conn))
	switch {
	case lower == "" || lower == "usb" || strings.HasPrefix(lower, "usb:") || strings.HasPrefix(lower, "embeddedci:"):
		return false
	case strings.HasPrefix(lower, "com") && strings.Trim(lower[3:], "0123456789") == "" && len(lower) > 3:
		return false // COM3
	}
	return lanConnPattern.MatchString(strings.TrimSpace(conn))
}

// podNameFromConnection is the pod's name when the connection names it: usb:<name>,
// embeddedci:<name>, benchpod-a1b2c3 or benchpod-a1b2c3.local. "" for an address, "usb" or the
// LAN default.
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
	if podOwnName.MatchString(strings.ToLower(name)) {
		name = strings.TrimSuffix(strings.ToLower(name), ".local")
	}
	if !podNamePattern.MatchString(name) {
		return ""
	}
	return name
}

// pairUSBConnection is the CLI connection to pair over the pod's USB-C cable for the app's
// connection conn: conn itself when it is a LAN address or name (the CLI pairs over that pod's own
// USB link, whatever else is on USB), usb:<name> for the pod the app knows, else usb (the one pod
// on USB; with several the CLI lists them).
func pairUSBConnection(conn, name string) string {
	if isLANConnection(conn) {
		return strings.TrimSpace(conn)
	}
	if name == "" {
		return "usb"
	}
	return "usb:" + name
}

// pairCloudConnection is the CLI connection to pair through embeddedci.com.
func pairCloudConnection(name string) string { return "embeddedci:" + name }

// validPairConnection: USB and embeddedci.com may pair, and the app's own LAN connection appConn
// (the CLI pairs over that pod's USB link; the pod refuses it over the LAN itself).
func validPairConnection(conn, appConn string) bool {
	switch {
	case isLANConnection(conn):
		return strings.EqualFold(conn, strings.TrimSpace(appConn))
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

// pairTarget is the connection of the pod to pair: the pod the app just reached that refused this
// computer (Find your pod, before the user chose "Use this pod"), else the app's own connection.
func (a *App) pairTarget(st Settings) string {
	a.mu.Lock()
	pod := a.pod
	a.mu.Unlock()
	if pod.Compat == compatNotPaired && strings.TrimSpace(pod.Connection) != "" {
		return pod.Connection
	}
	return st.Connection
}

// pairingOptions is how this computer can pair with the pod: over USB always, through
// embeddedci.com when the user is signed in and the account has pods.
func (a *App) pairingOptions(ctx context.Context) pairingOptions {
	st := a.store.Load()
	target := a.pairTarget(st)
	name := podNameFromConnection(target)
	out := pairingOptions{PodName: name, USB: pairUSBConnection(target, name), Cloud: []cloudPod{}, Offline: a.offline(st)}
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
	st := a.store.Load()
	target := a.pairTarget(st)
	if !validPairConnection(conn, target) {
		return pairResult{Text: "pair over usb, usb:<name>, embeddedci:<name> or the app's own connection"}
	}
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
		return pairResult{Text: pairErrorText(text), Pod: pod}
	}
	// Paired: connect again. A pod found by Find your pod (not yet the app's connection) is found
	// again at the same place; otherwise the MCP server's connect over the app's own connection.
	var cerr error
	if target != st.Connection {
		if f := a.findPod(ctx, target); !f.Connected && f.Error != "" {
			cerr = errors.New(f.Error)
		}
	} else {
		_, cerr = a.connectPod(ctx, true)
	}
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

// pairErrorText is the CLI's failure as the panel's error line: its error ("[benchpod] ..."
// lines) without the log prefix and the command's name, else everything it printed.
func pairErrorText(out string) string {
	var errs []string
	for _, l := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(l), "[benchpod] "); ok {
			errs = append(errs, strings.TrimSpace(rest))
		}
	}
	if len(errs) == 0 {
		return strings.TrimSpace(out)
	}
	text := strings.Join(errs, "\n")
	for _, p := range []string{"lan-clients add: ", "lan-clients: "} {
		text = strings.TrimPrefix(text, p)
	}
	if text != "" {
		text = strings.ToUpper(text[:1]) + text[1:]
	}
	return text
}

// mcpConnection is the connection the MCP server gets for the app's conn: a bare pod name
// (benchpod-a1b2c3) gets .local, which the SDK resolves over mDNS like any host; the SDK would
// otherwise ask the OS resolver for "benchpod-a1b2c3" and fail. Everything else as given.
func mcpConnection(conn string) string {
	c := strings.TrimSpace(conn)
	if podOwnName.MatchString(strings.ToLower(c)) && !strings.HasSuffix(strings.ToLower(c), ".local") {
		return c + ".local"
	}
	return c
}
