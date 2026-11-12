package local

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPodFirmwareUpdate runs the update against a stand-in CLI: the command line it gets (the
// pod's connection, the release files, the blobs), the streamed lines, and the refusals.
func TestPodFirmwareUpdate(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "fake-benchpod")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\necho \"args: $*\"\necho 'flash-self: success, the pod runs firmware 3.10.2'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "data")
	_ = os.MkdirAll(data, 0o700)
	_ = os.WriteFile(filepath.Join(data, ".env"), []byte("BENCHPOD_CLI_COMMAND="+cli+"\n"), 0o600)
	app, err := NewApp(data, "test", filepath.Join(data, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	st := app.store.Load()
	st.Connection, st.Offline = "usb:benchpod-test", true
	_ = app.store.Save(st)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	update := func(body string) (int, string) {
		resp, err := http.Post(srv.URL+"/api/pod/firmware/update", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if c, _ := update(`{"version":"latest"}`); c != 400 {
		t.Errorf("without confirmed: %d, want 400", c)
	}
	if c, b := update(`{"confirmed":true,"version":"latest"}`); c != 400 || !strings.Contains(b, "offline") {
		t.Errorf("offline download: %d %s", c, b)
	}
	if c, b := update(`{"confirmed":true,"files":true}`); c != 400 || !strings.Contains(b, podImage) {
		t.Errorf("files without the image: %d %s", c, b)
	}
	put := func(name, body string) int {
		resp, err := http.Post(srv.URL+"/api/pod/firmware/files?name="+name, "application/octet-stream", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := put("notes.txt", "x"); c != 400 {
		t.Errorf("a non-release file: %d", c)
	}
	for _, n := range []string{podImage, podImage + ".sig", "blobs-manifest.json", "blob-gw0.bin"} {
		if c := put(n, "data"); c != 200 {
			t.Fatalf("%s: %d", n, c)
		}
	}
	c, b := update(`{"confirmed":true,"files":true}`)
	if c != 200 {
		t.Fatalf("update: %d %s", c, b)
	}
	var done map[string]any
	var lines []string
	for _, frame := range strings.Split(b, "\n\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(frame, "data: ")), &ev) != nil {
			continue
		}
		if ev["type"] == "done" {
			done = ev
		} else {
			lines = append(lines, ev["text"].(string))
		}
	}
	all := strings.Join(lines, "\n")
	if done == nil || done["ok"] != true {
		t.Fatalf("done %v, lines:\n%s", done, all)
	}
	for _, want := range []string{"flash-self --network --connection usb:benchpod-test", filepath.Join(app.podFirmwareDir(), podImage), "--blobs-dir", "3.10.2"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	log, _ := os.ReadFile(filepath.Join(data, "actions.jsonl"))
	if !strings.Contains(string(log), `"tool":"pod_firmware_update"`) {
		t.Errorf("the update is not in the action log: %s", log)
	}
}
