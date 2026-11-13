package local

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPodSettingsCLI runs the Settings tab's commands against a stand-in CLI: only the fixed
// commands, their arguments, the Wi-Fi password on stdin (never argv), confirmations, offline.
func TestPodSettingsCLI(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "fake-benchpod")
	// Prints its arguments and whatever arrived on stdin.
	_ = os.WriteFile(cli, []byte("#!/bin/sh\necho \"argv: $*\"\nread -r line && echo \"stdin: $line\"\nexit 0\n"), 0o755)
	data := filepath.Join(dir, "data")
	_ = os.MkdirAll(data, 0o700)
	_ = os.WriteFile(filepath.Join(data, ".env"), []byte("BENCHPOD_CLI_COMMAND="+cli+"\n"), 0o600)
	app, err := NewApp(data, "test", filepath.Join(data, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	st := app.store.Load()
	st.Connection = "usb"
	_ = app.store.Save(st)
	ctx := t.Context()

	if r := app.runPodCLI(ctx, "rm", toolRequest{}); !r.Refused {
		t.Errorf("unknown command: %+v", r)
	}
	if r := app.runPodCLI(ctx, "show-network", toolRequest{}); r.IsError || !strings.Contains(r.Text, "argv: --connection usb show-network") {
		t.Errorf("show-network: %+v", r)
	}
	if r := app.runPodCLI(ctx, "lan-policy", toolRequest{Args: map[string]any{"set": "locked"}}); !r.Confirm {
		t.Errorf("lan-policy set without a yes: %+v", r)
	}
	if r := app.runPodCLI(ctx, "lan-policy", toolRequest{Args: map[string]any{"set": "wide-open"}, Confirmed: true}); !r.Refused {
		t.Errorf("a bad policy: %+v", r)
	}
	r := app.runPodCLI(ctx, "set-wifi", toolRequest{Args: map[string]any{"ssid": "lab net", "password": "s3cret"}, Confirmed: true})
	if r.IsError || !strings.Contains(r.Text, "argv: --connection usb set-wifi --ssid lab net --password-stdin") || !strings.Contains(r.Text, "stdin: (hidden)") {
		t.Errorf("set-wifi: %+v", r)
	}
	log, _ := os.ReadFile(filepath.Join(data, "actions.jsonl"))
	if strings.Contains(string(log), "s3cret") || !strings.Contains(string(log), `"command":"set-wifi"`) {
		t.Errorf("action log: %s", log)
	}
	st.Offline = true
	_ = app.store.Save(st)
	if r := app.runPodCLI(ctx, "deregister", toolRequest{Confirmed: true}); !r.Refused {
		t.Errorf("deregister offline: %+v", r)
	}
}
