package local

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/embeddedci-com/benchpod-desktop/agent"
	"github.com/embeddedci-com/benchpod-desktop/chat"
)

// Finding the pod on first start, with no account and nothing typed: the MCP connect tool, tried
// over USB first (the pod's USB network adapter, else its console), then on the LAN over mDNS.
// No registration: unlike `benchpod setup`, nothing is claimed on embeddedci.com.
//
//	POST /api/pod/find  {"connection": ""}  -> {connected, connection, kind, version, ip, la_voltage, error, tried, choices}
//
// An empty connection tries usb, then the LAN; a given one is tried alone. The page then saves the
// connection and the I/O voltage in the settings. With several pods on USB or on the LAN, choices
// lists them, one connection each, for the page to offer as buttons.

type foundPod struct {
	Connected  bool     `json:"connected"`
	Connection string   `json:"connection"`
	Kind       string   `json:"kind,omitempty"`
	Version    string   `json:"version,omitempty"`
	Board      string   `json:"board,omitempty"` // the board revision, e.g. v3r2
	IP         string   `json:"ip,omitempty"`
	LAVoltage  *float64 `json:"la_voltage"`
	Error      string   `json:"error,omitempty"`
	Tried      []string `json:"tried"`
	// Compat: the pod answered but the app cannot drive it (firmware_too_old: firmware 3.x, which
	// the app offers to update; app_too_old; not_paired, which the app offers to pair). Version is
	// then the firmware it runs, when known.
	Compat podCompat `json:"compat,omitempty"`
	// Choices: several pods were found (on USB, on the LAN or both), so connect picked none. Each
	// one is a connection that finds that pod alone. A pod on both is listed once, over USB.
	Choices []podChoice `json:"choices,omitempty"`
}

// podChoice is one of several pods Find saw.
type podChoice struct {
	Name       string `json:"name"`       // benchpod-a1b2c3
	Connection string `json:"connection"` // usb:benchpod-a1b2c3, or benchpod-a1b2c3.local on the LAN
}

// mcpCaller is the one MCP call findPod makes (agent.MCPTools; a fake in the tests).
type mcpCaller interface {
	Call(ctx context.Context, name string, args map[string]any) (agent.ToolResult, error)
}

func (a *App) findPod(ctx context.Context, conn string) foundPod {
	conn = strings.TrimSpace(conn)
	out := foundPod{Tried: []string{}}
	if a.offline(a.store.Load()) {
		if err := offlineConnErr(conn); err != nil {
			out.Error = err.Error()
			return out
		}
	}
	tries := []string{conn}
	if conn == "" {
		tries = []string{"usb", ""} // "" = the one pod on the LAN (mDNS)
	}
	a.connMu.Lock()
	defer a.connMu.Unlock()
	t, err := a.startMCP(ctx)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	return a.tryConnect(ctx, t, tries)
}

// tryConnect calls connect with each connection in turn and stops at the first pod that answers.
func (a *App) tryConnect(ctx context.Context, t mcpCaller, tries []string) foundPod {
	out := foundPod{Tried: []string{}}
	var errs []string
	var choices []podChoice
	for _, c := range tries {
		label := c
		if label == "" {
			label = "your network"
		}
		out.Tried = append(out.Tried, label)
		args := map[string]any{}
		if c != "" {
			args["connection"] = mcpConnection(c)
		}
		res, err := t.Call(ctx, "connect", args)
		if err == nil && !res.IsError {
			var st struct {
				Kind      string   `json:"kind"`
				LAVoltage *float64 `json:"la_voltage"`
				Firmware  struct {
					Version  string `json:"version"`
					Board    string `json:"board"`
					BoardRev string `json:"board_rev"`
					IP       string `json:"ip"`
				} `json:"firmware"`
			}
			_ = json.Unmarshal(res.Structured, &st)
			out = foundPod{Connected: true, Connection: c, Kind: st.Kind, Version: st.Firmware.Version,
				Board: st.Firmware.BoardRev, IP: st.Firmware.IP,
				LAVoltage: st.LAVoltage, Tried: out.Tried}
			a.mu.Lock()
			a.pod = podState{Connected: true, Connection: c, Status: res.Structured}
			a.mu.Unlock()
			return out
		}
		msg := res.Text
		if err != nil {
			msg = err.Error()
		} else if compat, fw := mcpCompat(res.Text); compat != compatOK {
			// Found it, but the app cannot drive it: firmware 3.x (the page offers the update), or
			// this computer is not paired with it (the page offers to pair).
			out = foundPod{Connection: c, Version: fw, Compat: compat, Error: msg, Tried: out.Tried}
			a.mu.Lock()
			a.pod = podState{Connection: c, Error: msg, Compat: compat, Firmware: fw}
			a.mu.Unlock()
			return out
		}
		errs = append(errs, label+": "+msg)
		choices = addChoices(choices, severalPods(msg))
	}
	out.Error = strings.Join(errs, "\n")
	out.Choices = choices
	return out
}

// Neither the MCP server's connect error nor a tool gives the pods it saw as data (embeddedci-mcp
// 4.0.1: the error is text only, and there is no discover tool; `benchpod discover` prints a report,
// no JSON). So the names come from the error's text, with the pod-name pattern:
//
//	2 pods on USB (benchpod-30a9f1 on /dev/cu.usbmodem1, benchpod-c2f662 on /dev/cu.usbmodem2): pick one ...
//	no pod named benchpod-000000 on USB (found: benchpod-30a9f1 on /dev/cu.usbmodem1, ...)
//	2 BenchPods found ...:
//	  - BenchPod 30a9f1  10.237.134.169:8080  id=...
var (
	usbSeveral = regexp.MustCompile(`\d+ pods on USB \(([^)]*)\)|on USB \(found: ([^)]*)\)`)
	usbPodName = regexp.MustCompile(`\b` + podOwnNameRe + `\b`)
	lanSeveral = regexp.MustCompile(`\d+ BenchPods found`)
	lanPodLine = regexp.MustCompile(`(?m)^\s*-\s+BenchPod ([0-9a-f]{6})\b`)
)

// severalPods lists the pods a connect error says it saw, when it saw more than one or none of
// them was the one asked for. Nil otherwise.
func severalPods(msg string) []podChoice {
	var out []podChoice
	if m := usbSeveral.FindStringSubmatch(msg); m != nil {
		for _, n := range usbPodName.FindAllString(m[1]+" "+m[2], -1) {
			out = append(out, podChoice{Name: n, Connection: "usb:" + n})
		}
	}
	if lanSeveral.MatchString(msg) {
		for _, m := range lanPodLine.FindAllStringSubmatch(msg, -1) {
			n := "benchpod-" + m[1]
			out = append(out, podChoice{Name: n, Connection: n + ".local"})
		}
	}
	if len(out) < 2 && !strings.Contains(msg, "found: ") {
		return nil // one name is the example in the error, not a choice
	}
	return out
}

// addChoices adds the pods not yet listed: the first connection seen for a pod wins (USB is tried first).
func addChoices(list, more []podChoice) []podChoice {
	for _, c := range more {
		seen := false
		for _, l := range list {
			seen = seen || l.Name == c.Name
		}
		if !seen {
			list = append(list, c)
		}
	}
	return list
}

func (a *App) servePodFind(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Connection string `json:"connection"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body)
	// The first start may install the BenchPod tools: give it time.
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Minute)
	defer cancel()
	chat.WriteJSON(w, http.StatusOK, a.findPod(ctx, body.Connection))
}
