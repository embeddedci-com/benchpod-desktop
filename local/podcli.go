package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/embeddedci-com/benchpod-desktop/chat"
)

// The pod's own settings that no MCP tool covers: Wi-Fi, LAN access, the USB network adapter, the
// signature policy (firmware 4.0: required only), deregistering. The
// bundled benchpod CLI (0.10) does them: the Wi-Fi and network commands and sig-policy as JSON over
// the pod's USB network adapter, lan-policy and usb-network over its USB console. The Settings tab
// runs it, only for this fixed list of commands, with the same
// request and answer as the tool endpoint (bench.go), confirmations included:
//
//	POST /api/pod/cli/{command}  {"args": {...}, "confirmed": false}
//	  -> {"text", "is_error", "refused", "confirm", "ms"}

type podCLICommand struct {
	// argv builds the CLI arguments (after the global ones) from the page's args.
	argv func(args map[string]any) ([]string, error)
	// changes: it changes the pod, so it asks first (with confirmations on) and is logged.
	changes func(args map[string]any) bool
	// stdin is fed to the CLI (a Wi-Fi password, never on its command line).
	stdin func(args map[string]any) string
	// cloud: it talks to embeddedci.com (with the user's `benchpod login`), so offline refuses it.
	cloud   bool
	timeout time.Duration
}

var wifiSSID = regexp.MustCompile(`^[^\x00-\x1f]{1,32}$`)

func str(args map[string]any, k string) string { s, _ := args[k].(string); return strings.TrimSpace(s) }

func oneOf(v string, allowed ...string) error {
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return fmt.Errorf("%q is not one of %s", v, strings.Join(allowed, ", "))
}

func always(map[string]any) bool { return true }
func never(map[string]any) bool  { return false }

// setting reads with no "set", changes with one.
func setting(name string, values ...string) podCLICommand {
	return podCLICommand{
		argv: func(args map[string]any) ([]string, error) {
			v := str(args, "set")
			if v == "" {
				return []string{name, "show"}, nil
			}
			if len(values) == 0 {
				return nil, fmt.Errorf("%s cannot be changed: firmware 4.0 always requires signed images (BP0074)", name)
			}
			if err := oneOf(v, values...); err != nil {
				return nil, err
			}
			return []string{name, "set", v}, nil
		},
		changes: func(args map[string]any) bool { return str(args, "set") != "" },
		timeout: time.Minute,
	}
}

var podCLICommands = map[string]podCLICommand{
	"show-network": {argv: func(map[string]any) ([]string, error) { return []string{"show-network"}, nil }, changes: never, timeout: time.Minute},
	"scan-wifi":    {argv: func(map[string]any) ([]string, error) { return []string{"scan-wifi"}, nil }, changes: never, timeout: 2 * time.Minute},
	"set-wifi": {
		argv: func(args map[string]any) ([]string, error) {
			ssid, _ := args["ssid"].(string)
			if !wifiSSID.MatchString(ssid) {
				return nil, errors.New("give the network name (SSID), 1 to 32 characters")
			}
			return []string{"set-wifi", "--ssid", ssid, "--password-stdin"}, nil
		},
		changes: always,
		stdin:   func(args map[string]any) string { p, _ := args["password"].(string); return p + "\n" },
		timeout: 5 * time.Minute, // may first install the pod's Wi-Fi chip image
	},
	"clear-wifi": {argv: func(map[string]any) ([]string, error) { return []string{"clear-wifi"}, nil }, changes: always, timeout: time.Minute},
	"lan-policy": setting("lan-policy", "open", "locked", "off"),
	// Firmware 4.0 always requires signatures (BP0074): "required" is the only setting left.
	"sig-policy": setting("sig-policy", "required"),
	"usb-network": {
		argv: func(args map[string]any) ([]string, error) {
			v, err := usbNetworkSet(args)
			if err != nil || v == "" {
				return []string{"usb-network", "show"}, err
			}
			return []string{"usb-network", v}, nil
		},
		changes: func(args map[string]any) bool { v, err := usbNetworkSet(args); return err == nil && v != "" },
		timeout: time.Minute,
	},
	"deregister": {argv: func(map[string]any) ([]string, error) { return []string{"deregister"}, nil }, changes: always, cloud: true, timeout: 2 * time.Minute},
}

// usbNetworkSet is the usb-network change the page asks for: {"enabled": true|false} (the
// firmware 4.0 form, as the MCP tool's), or {"set": "on"|"off"}; "" reads the setting.
func usbNetworkSet(args map[string]any) (string, error) {
	if e, ok := args["enabled"]; ok && e != nil {
		b, ok := e.(bool)
		if !ok {
			return "", errors.New("enabled is true or false")
		}
		if b {
			return "on", nil
		}
		return "off", nil
	}
	v := str(args, "set")
	if v == "" {
		return "", nil
	}
	return v, oneOf(v, "on", "off")
}

// podCLILogArgs hides the Wi-Fi password from the action log.
func podCLILogArgs(args map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range args {
		if k == "password" {
			v = "(hidden)"
		}
		out[k] = v
	}
	return out
}

func (a *App) runPodCLI(ctx context.Context, name string, req toolRequest) toolResponse {
	c, ok := podCLICommands[name]
	if !ok {
		return toolResponse{Text: "not a pod setting the app runs: " + name, IsError: true, Refused: true}
	}
	if req.Args == nil {
		req.Args = map[string]any{}
	}
	st := a.store.Load()
	offline := a.offline(st)
	if c.cloud && offline {
		return toolResponse{Text: "offline mode: this needs embeddedci.com", IsError: true, Refused: true}
	}
	argv, err := c.argv(req.Args)
	if err != nil {
		return toolResponse{Text: err.Error(), IsError: true, Refused: true}
	}
	changes := c.changes(req.Args)
	if changes && st.ConfirmRisky && !req.Confirmed {
		return toolResponse{Confirm: true}
	}
	cli, err := a.cliCommand()
	if err != nil {
		return toolResponse{Text: err.Error(), IsError: true}
	}
	if conn := strings.TrimSpace(st.Connection); conn != "" {
		argv = append([]string{"--connection", conn}, argv...)
	}
	// The CLI talks to the pod over its USB console. When the MCP server holds that console (no
	// USB network), and before any change, let go of the pod: the next tool call connects again.
	if changes || a.podKind() == "serial" {
		a.releasePod()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout)
	defer cancel()
	run := func() ([]byte, error) {
		cmd := exec.CommandContext(ctx, cli, argv...)
		cmd.Env = append(os.Environ(), "BENCHPOD_NO_FIRMWARE_REPORT=1")
		if offline {
			cfg := filepath.Join(a.store.Dir(), "offline-config")
			_ = os.MkdirAll(cfg, 0o700)
			cmd.Env = append(cmd.Env, "XDG_CONFIG_HOME="+cfg)
		}
		if c.stdin != nil {
			cmd.Stdin = strings.NewReader(c.stdin(req.Args))
		}
		return cmd.CombinedOutput()
	}
	t0 := time.Now()
	out, err := run()
	// Busy (exit 4): the MCP server's session holds the pod (its lease on the USB network or the
	// LAN). Let go of it and try once more.
	if cliExitCode(err) == cliExitBusy && a.podConnected() {
		a.releasePod()
		out, err = run()
	}
	d := time.Since(t0)
	text := strings.TrimSpace(stripCLIDebug(string(out)))
	if p, _ := req.Args["password"].(string); p != "" {
		text = strings.ReplaceAll(text, p, "(hidden)") // never shown or logged, whatever the CLI prints
	}
	res := toolResponse{Text: text, Millis: d.Milliseconds()}
	if err != nil {
		res.IsError = true
		if ctx.Err() != nil {
			res.Text = strings.TrimSpace(text + "\n(no answer within " + c.timeout.String() + ")")
		}
		// Exit 7: the pod runs firmware 3.x; exit 6: its firmware is newer than the bundled CLI;
		// exit 3 with BP0081: this computer is not paired with the pod.
		compat := cliCompat(cliExitCode(err), text)
		if h := compatHint(compat); h != "" {
			res.Text = strings.TrimSpace(res.Text + "\n" + h)
		}
		if compat == compatNotPaired {
			// The header's pod badge then offers to pair (podpair.go).
			a.mu.Lock()
			if !a.pod.Connected {
				a.pod.Compat, a.pod.Error = compatNotPaired, text
			}
			a.mu.Unlock()
		}
	}
	if changes {
		entry := map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano), "source": "manual", "tool": "pod_setting",
			"args": map[string]any{"command": name, "args": podCLILogArgs(req.Args)}, "ms": d.Milliseconds(), "is_error": res.IsError, "result": truncate(text, 2000)}
		b, _ := json.Marshal(entry)
		a.mu.Lock()
		_, _ = a.actions.Write(append(b, '\n'))
		a.mu.Unlock()
	}
	return res
}

// stripCLIDebug drops the CLI's "[benchpod] [serial] ..." probe lines, which say how it found the
// console, not what the pod answered.
func stripCLIDebug(s string) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "[benchpod] [serial]") {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}

// podKind is how the MCP server reached the pod (usb_net, serial, tcp...), "" when not connected.
func (a *App) podKind() string {
	a.mu.Lock()
	raw := a.pod.Status
	a.mu.Unlock()
	var st struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(raw, &st)
	return st.Kind
}

func (a *App) podConnected() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tools != nil && a.pod.Connected
}

// releasePod stops the MCP server, so another program can talk to the pod.
func (a *App) releasePod() {
	a.connMu.Lock()
	a.mu.Lock()
	t := a.tools
	a.tools, a.pod = nil, podState{}
	a.setMCPState(MCPStopped, a.mcpErr)
	a.mu.Unlock()
	a.console.reset()
	a.buses.reset()
	if t != nil {
		_ = t.Close()
	}
	a.connMu.Unlock()
}

func (a *App) servePodCLI(w http.ResponseWriter, r *http.Request) {
	var req toolRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, `send {"args": {...}}`, http.StatusBadRequest)
		return
	}
	chat.WriteJSON(w, http.StatusOK, a.runPodCLI(r.Context(), r.PathValue("name"), req))
}
