package local

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/embeddedci-com/benchpod-desktop/chat"
)

// Finding the pod on first start, with no account and nothing typed: the MCP connect tool, tried
// over USB first (the pod's USB network adapter, else its console), then on the LAN over mDNS.
// No registration: unlike `benchpod setup`, nothing is claimed on embeddedci.com.
//
//	POST /api/pod/find  {"connection": ""}  -> {connected, connection, kind, version, ip, la_voltage, error, tried}
//
// An empty connection tries usb, then the LAN; a given one is tried alone. The page then saves the
// connection and the I/O voltage in the settings.

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
	var errs []string
	for _, c := range tries {
		label := c
		if label == "" {
			label = "your network"
		}
		out.Tried = append(out.Tried, label)
		args := map[string]any{}
		if c != "" {
			args["connection"] = c
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
		}
		errs = append(errs, label+": "+msg)
	}
	out.Error = strings.Join(errs, "\n")
	return out
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
