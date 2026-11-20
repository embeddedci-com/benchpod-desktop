package local

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMCPCompat reads the MCP server's refusals of a pod by their error class, as embeddedci-mcp
// 4 words them (behind the MCP library's prefix).
func TestMCPCompat(t *testing.T) {
	cases := []struct {
		text   string
		compat podCompat
		fw     string
	}{
		{"Error executing tool connect: FirmwareTooOldError: the pod runs firmware 3.10.13 (client API 1); this BenchPod MCP " +
			"server (embeddedci-mcp 4.0.0) needs firmware 4.0.0 or later. Tell the user to update", compatFirmwareTooOld, "3.10.13"},
		{"could not connect to the BenchPod: FirmwareTooOldError: the pod runs firmware 3.x (client API 1)", compatFirmwareTooOld, "3.x"},
		{"FirmwareTooOldError: no version here", compatFirmwareTooOld, ""},
		{"Error executing tool connect: ClientTooOldError: this BenchPod MCP server (embeddedci-mcp 4.0.0) is too old", compatAppTooOld, ""},
		{"Error executing tool connect: TransportError: no pod on USB", compatOK, ""},
		// Firmware 4.0.1 LAN pairing: embeddedci-mcp 4.0.1 raises PodNotPairedError (BP0081).
		{"Error executing tool connect: PodNotPairedError: connect: BP0081: This computer is not paired with the pod: " +
			"wiring_get needs pairing over USB or from the cloud; tell the user", compatNotPaired, ""},
		{"PodNotPairedError: BP0081: This computer is not paired with the pod", compatNotPaired, ""},
		// The code from a server whose SDK has no PodNotPairedError class yet.
		{"Error executing tool connect: PodLockedError: connect: BP0081: not paired", compatNotPaired, ""},
		// Another LAN refusal (the LAN policy) is not the unpaired computer.
		{"Error executing tool connect: PodLockedError: connect: BP0040: the LAN is locked", compatOK, ""},
		{"Error executing tool connect: PermissionError: the docs mention BP0081 and PodNotPairedError", compatOK, ""},
		// Only the class decides: a message that mentions it is not one.
		{"BP0009: the text says FirmwareTooOldError somewhere", compatOK, ""},
	}
	for _, c := range cases {
		got, fw := mcpCompat(c.text)
		if got != c.compat || fw != c.fw {
			t.Errorf("%q: %q %q, want %q %q", c.text, got, fw, c.compat, c.fw)
		}
	}
}

// TestCLICompat: the CLI's exit code decides; exit 3 (refused) is the unpaired computer only with
// the stable code BP0081 in its output.
func TestCLICompat(t *testing.T) {
	notPaired := "show-network: the pod refused: BP0081: This computer is not paired with the pod: wifi_status needs " +
		"pairing over USB or from the cloud; plug the pod into this computer's USB-C port and run `benchpod setup` once"
	cases := []struct {
		code int
		out  string
		want podCompat
	}{
		{cliExitRefused, notPaired, compatNotPaired},
		{cliExitRefused, "show-network: the pod refused: BP0040: the LAN is locked", compatOK},
		{cliExitRefused, "the pod refused: bad request", compatOK},
		// The code alone, on another exit code, is not the refusal.
		{cliExitUnreachable, notPaired, compatOK},
		{1, notPaired, compatOK},
		{cliExitUpdatePod, "", compatFirmwareTooOld},
		{cliExitUpdateCLI, "", compatAppTooOld},
		{0, "", compatOK},
	}
	for _, c := range cases {
		if got := cliCompat(c.code, c.out); got != c.want {
			t.Errorf("exit %d %q: %q, want %q", c.code, c.out, got, c.want)
		}
	}
}

// TestFirmwareTooOldOffersUpdate: a pod the MCP server refused for its firmware 3.x shows that
// firmware and the reason on the firmware page, which then offers the update.
func TestFirmwareTooOldOffersUpdate(t *testing.T) {
	data := t.TempDir()
	app, err := NewApp(data, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	st := app.store.Load()
	st.Offline = true // no GitHub lookup
	_ = app.store.Save(st)
	msg := "Error executing tool connect: FirmwareTooOldError: the pod runs firmware 3.10.13 (client API 1); update it"
	compat, fw := mcpCompat(msg)
	app.mu.Lock()
	app.pod = podState{Connection: "usb", Error: msg, Compat: compat, Firmware: fw}
	app.mu.Unlock()

	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/pod/firmware")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var info map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&info)
	if info["running"] != "3.10.13" || info["compat"] != "firmware_too_old" {
		t.Errorf("firmware info: %v", info)
	}
}

// TestPodCLIExitCodes: the bundled CLI's exit codes, not its wording, say what to do: 7 the pod
// needs firmware 4.0, 6 the app is too old; busy (4) retries once without the MCP session.
func TestPodCLIExitCodes(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "fake-benchpod")
	code := filepath.Join(dir, "code")
	// Exits with the code in the file "code".
	_ = os.WriteFile(cli, []byte("#!/bin/sh\necho \"argv: $*\"\nexit $(cat "+code+")\n"), 0o755)
	data := filepath.Join(dir, "data")
	_ = os.MkdirAll(data, 0o700)
	_ = os.WriteFile(filepath.Join(data, ".env"), []byte("BENCHPOD_CLI_COMMAND="+cli+"\n"), 0o600)
	app, err := NewApp(data, "test", filepath.Join(data, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	ctx := t.Context()

	for _, c := range []struct {
		exit string
		want string
	}{
		{"7", "needs pod firmware 4.0"},
		{"6", "update the BenchPod desktop app"},
		{"3", "argv: show-network"},
	} {
		_ = os.WriteFile(code, []byte(c.exit), 0o600)
		r := app.runPodCLI(ctx, "show-network", toolRequest{})
		if !r.IsError || !strings.Contains(r.Text, c.want) {
			t.Errorf("exit %s: %+v", c.exit, r)
		}
	}
	// Exit 3 with BP0081: this computer is not paired; the pod badge offers to pair.
	_ = os.WriteFile(code, []byte("3"), 0o600)
	_ = os.WriteFile(cli, []byte("#!/bin/sh\necho \"show-network: the pod refused: BP0081: This computer is not paired with the pod\"\nexit $(cat "+code+")\n"), 0o755)
	if r := app.runPodCLI(ctx, "show-network", toolRequest{}); !r.IsError || !strings.Contains(r.Text, "pod not paired") {
		t.Errorf("not paired: %+v", r)
	}
	if app.pod.Compat != compatNotPaired {
		t.Errorf("pod state: %+v", app.pod)
	}
	_ = os.WriteFile(cli, []byte("#!/bin/sh\necho \"argv: $*\"\nexit $(cat "+code+")\n"), 0o755)
	if r := app.runPodCLI(ctx, "sig-policy", toolRequest{Args: map[string]any{"set": "audit"}, Confirmed: true}); !r.Refused {
		t.Errorf("sig-policy set audit: %+v", r)
	}
	_ = os.WriteFile(code, []byte("0"), 0o600)
	if r := app.runPodCLI(ctx, "sig-policy", toolRequest{Args: map[string]any{"set": "required"}, Confirmed: true}); r.IsError || !strings.Contains(r.Text, "argv: sig-policy set required") {
		t.Errorf("sig-policy set required: %+v", r)
	}
	for _, c := range []struct {
		args map[string]any
		argv string
	}{
		{map[string]any{"enabled": false}, "argv: usb-network off"},
		{map[string]any{"enabled": true}, "argv: usb-network on"},
		{map[string]any{"set": "off"}, "argv: usb-network off"},
		{map[string]any{}, "argv: usb-network show"},
	} {
		if r := app.runPodCLI(ctx, "usb-network", toolRequest{Args: c.args, Confirmed: true}); r.IsError || !strings.Contains(r.Text, c.argv) {
			t.Errorf("usb-network %v: %+v", c.args, r)
		}
	}
	if r := app.runPodCLI(ctx, "usb-network", toolRequest{Args: map[string]any{"enabled": "yes"}, Confirmed: true}); !r.Refused {
		t.Errorf("usb-network enabled=yes: %+v", r)
	}
	if r := app.runPodCLI(ctx, "sig-policy", toolRequest{}); r.IsError || !strings.Contains(r.Text, "argv: sig-policy show") {
		t.Errorf("sig-policy show: %+v", r)
	}
}
