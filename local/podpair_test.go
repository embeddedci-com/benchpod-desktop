package local

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestPairCommandLine: the bundled CLI pairs this computer over USB (the pod the app knows by
// name, else the one on USB) or through embeddedci.com, never over the LAN.
func TestPairCommandLine(t *testing.T) {
	for _, c := range []struct {
		conn, name string
	}{
		{"usb:benchpod-30a9f1", "benchpod-30a9f1"},
		{"USB:benchpod-30a9f1", "benchpod-30a9f1"},
		{"embeddedci:lab-pod-1", "lab-pod-1"},
		{"benchpod-30a9f1", "benchpod-30a9f1"},
		{"benchpod-c2f662.local", "benchpod-c2f662"},
		{"BenchPod-C2F662.local", "benchpod-c2f662"},
		{"usb:benchpod-c2f662.local", "benchpod-c2f662"},
		{"pod.local", ""},
		{"192.168.1.150", ""},
		{"192.168.1.150:8443", ""},
		{"usb", ""},
		{"", ""},
		{"usb:bad name", ""},
	} {
		if got := podNameFromConnection(c.conn); got != c.name {
			t.Errorf("podNameFromConnection(%q) = %q, want %q", c.conn, got, c.name)
		}
	}
	if got, want := pairArgv(pairUSBConnection("usb:benchpod-30a9f1", "benchpod-30a9f1")), []string{"--connection", "usb:benchpod-30a9f1", "lan-clients", "add"}; !reflect.DeepEqual(got, want) {
		t.Errorf("usb: %v, want %v", got, want)
	}
	if got, want := pairArgv(pairUSBConnection("usb", "")), []string{"--connection", "usb", "lan-clients", "add"}; !reflect.DeepEqual(got, want) {
		t.Errorf("usb, no name: %v, want %v", got, want)
	}
	if got, want := pairArgv(pairCloudConnection("benchpod-30a9f1")), []string{"--connection", "embeddedci:benchpod-30a9f1", "lan-clients", "add"}; !reflect.DeepEqual(got, want) {
		t.Errorf("cloud: %v, want %v", got, want)
	}
	// The bug from 0.2.2: two pods on USB, the app on the LAN. Pair hands the CLI the app's own LAN
	// connection, so it pairs over that pod's USB link instead of asking which pod on USB.
	for _, c := range []string{"192.168.1.164", "192.168.1.164:8443", "benchpod-c2f662.local", "benchpod-c2f662", "lab-pod.example.com"} {
		if got, want := pairArgv(pairUSBConnection(c, podNameFromConnection(c))), []string{"--connection", c, "lan-clients", "add"}; !reflect.DeepEqual(got, want) {
			t.Errorf("LAN %s: %v, want %v", c, got, want)
		}
	}
	for _, c := range []string{"usb", "usb:benchpod-30a9f1", "embeddedci:x", "/dev/cu.usbmodem1", "COM3", "", "-h", "a b"} {
		if isLANConnection(c) {
			t.Errorf("isLANConnection(%q)", c)
		}
	}
	for conn, ok := range map[string]bool{"usb": true, "usb:benchpod-30a9f1": true, "embeddedci:benchpod-30a9f1": true,
		"192.168.1.164": true, "192.168.1.150": false, "": false, "usb:": false, "embeddedci:": false, "embeddedci:a b": false,
		"usb:--help": false, "--help": false} {
		if validPairConnection(conn, "192.168.1.164") != ok {
			t.Errorf("validPairConnection(%q) != %v", conn, ok)
		}
	}
	for in, want := range map[string]string{"benchpod-c2f662": "benchpod-c2f662.local", "benchpod-c2f662.local": "benchpod-c2f662.local",
		"192.168.1.164": "192.168.1.164", "usb:benchpod-c2f662": "usb:benchpod-c2f662"} {
		if got := mcpConnection(in); got != want {
			t.Errorf("mcpConnection(%q) = %q, want %q", in, got, want)
		}
	}
}

// The CLI's refusal is the panel's error line, without "[benchpod]" and the command's name.
func TestPairErrorText(t *testing.T) {
	out := "benchpod-c2f662 did not answer on the LAN over mDNS; looking for it on USB (usb:benchpod-c2f662).\n" +
		"[benchpod] lan-clients add: this computer is not paired with the pod at 192.168.1.164:8080, and the pod is not plugged in."
	if got, want := pairErrorText(out), "This computer is not paired with the pod at 192.168.1.164:8080, and the pod is not plugged in."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := pairErrorText("lan-clients: the pod refused"); got != "lan-clients: the pod refused" {
		t.Errorf("no prefix: %q", got)
	}
}

// TestPairRunsCLI: Pair runs `lan-clients add` with the bundled CLI over the connection the page
// picked, and refuses the LAN and, offline, the cloud.
func TestPairRunsCLI(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "fake-benchpod")
	argvFile := filepath.Join(dir, "argv")
	// Pairing fails (exit 3, as an org member without the admin role would): no reconnect.
	_ = os.WriteFile(cli, []byte("#!/bin/sh\necho \"$*\" > "+argvFile+"\necho \"lan-clients: the pod refused\"\nexit 3\n"), 0o755)
	data := filepath.Join(dir, "data")
	_ = os.MkdirAll(data, 0o700)
	_ = os.WriteFile(filepath.Join(data, ".env"), []byte("BENCHPOD_CLI_COMMAND="+cli+"\n"), 0o600)
	app, err := NewApp(data, "test", filepath.Join(data, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	pair := func(conn string) pairResult {
		resp, err := http.Post(srv.URL+"/api/pod/pair", "application/json", strings.NewReader(`{"connection":"`+conn+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var r pairResult
		_ = json.NewDecoder(resp.Body).Decode(&r)
		return r
	}
	if r := pair("usb:benchpod-30a9f1"); r.OK || !strings.Contains(r.Text, "the pod refused") {
		t.Errorf("usb: %+v", r)
	}
	if b, _ := os.ReadFile(argvFile); strings.TrimSpace(string(b)) != "--connection usb:benchpod-30a9f1 lan-clients add" {
		t.Errorf("argv: %q", b)
	}
	_ = os.Remove(argvFile)
	if r := pair("192.168.1.150"); r.OK || !strings.Contains(r.Text, "usb") {
		t.Errorf("LAN: %+v", r)
	}
	if _, err := os.Stat(argvFile); err == nil {
		t.Error("the CLI ran for a LAN address that is not the app's connection")
	}
	// A pod Find your pod reached that refused this computer (not the app's connection yet): pair
	// over that pod's own address, so the CLI pairs it through its USB link.
	app.mu.Lock()
	app.pod = podState{Connection: "192.168.1.170", Compat: compatNotPaired}
	app.mu.Unlock()
	if o := app.pairingOptions(t.Context()); o.USB != "192.168.1.170" {
		t.Errorf("pairing options for the found pod: %+v", o)
	}
	// pairingOptions started the (fake) MCP server, which clears the pod state; a real Find runs
	// after the server is up, so set it again.
	app.mu.Lock()
	app.pod = podState{Connection: "192.168.1.170", Compat: compatNotPaired}
	app.mu.Unlock()
	_ = pair("192.168.1.170")
	if b, _ := os.ReadFile(argvFile); strings.TrimSpace(string(b)) != "--connection 192.168.1.170 lan-clients add" {
		t.Errorf("found pod argv: %q", b)
	}
	_ = os.Remove(argvFile)
	app.mu.Lock()
	app.pod = podState{}
	app.mu.Unlock()
	// The app's own LAN connection goes to the CLI as it is.
	st0 := app.store.Load()
	st0.Connection = "192.168.1.164"
	_ = app.store.Save(st0)
	if o := app.pairingOptions(t.Context()); o.USB != "192.168.1.164" {
		t.Errorf("pairing options on the LAN: %+v", o)
	}
	_ = pair("192.168.1.164")
	if b, _ := os.ReadFile(argvFile); strings.TrimSpace(string(b)) != "--connection 192.168.1.164 lan-clients add" {
		t.Errorf("LAN argv: %q", b)
	}
	_ = os.Remove(argvFile)
	st := app.store.Load()
	st.Offline = true
	_ = app.store.Save(st)
	if r := pair("embeddedci:benchpod-30a9f1"); r.OK || !strings.Contains(r.Text, "offline") {
		t.Errorf("offline cloud: %+v", r)
	}
	if _, err := os.Stat(argvFile); err == nil {
		t.Error("the CLI ran for a refused connection")
	}

	// Offline: no cloud way, USB with the name from the connection.
	st.Connection = "usb:benchpod-30a9f1"
	_ = app.store.Save(st)
	o := app.pairingOptions(t.Context())
	if o.USB != "usb:benchpod-30a9f1" || len(o.Cloud) != 0 || !o.Offline {
		t.Errorf("pairing options: %+v", o)
	}
}
