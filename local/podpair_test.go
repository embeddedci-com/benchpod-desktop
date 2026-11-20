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
	if got, want := pairArgv(pairUSBConnection("benchpod-30a9f1")), []string{"--connection", "usb:benchpod-30a9f1", "lan-clients", "add"}; !reflect.DeepEqual(got, want) {
		t.Errorf("usb: %v, want %v", got, want)
	}
	if got, want := pairArgv(pairUSBConnection("")), []string{"--connection", "usb", "lan-clients", "add"}; !reflect.DeepEqual(got, want) {
		t.Errorf("usb, no name: %v, want %v", got, want)
	}
	if got, want := pairArgv(pairCloudConnection("benchpod-30a9f1")), []string{"--connection", "embeddedci:benchpod-30a9f1", "lan-clients", "add"}; !reflect.DeepEqual(got, want) {
		t.Errorf("cloud: %v, want %v", got, want)
	}
	for conn, ok := range map[string]bool{"usb": true, "usb:benchpod-30a9f1": true, "embeddedci:benchpod-30a9f1": true,
		"192.168.1.150": false, "": false, "usb:": false, "embeddedci:": false, "embeddedci:a b": false, "usb:--help": false} {
		if validPairConnection(conn) != ok {
			t.Errorf("validPairConnection(%q) != %v", conn, ok)
		}
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
