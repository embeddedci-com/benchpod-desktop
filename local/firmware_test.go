package local

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestFirmwareUpload: a file the page uploads lands in the data folder under its base name, for
// flash to take; names that are not firmware or would leave the folder are refused.
func TestFirmwareUpload(t *testing.T) {
	app, err := NewApp(t.TempDir(), "test", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	up := func(name, body string) (*http.Response, firmwareFile) {
		resp, err := http.Post(srv.URL+"/api/firmware?name="+name, "application/octet-stream", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var f firmwareFile
		_ = json.NewDecoder(resp.Body).Decode(&f)
		return resp, f
	}
	resp, f := up("app.elf", "\x7fELF...")
	if resp.StatusCode != 200 || f.Size != 7 || !strings.HasPrefix(f.Path, app.firmwareDir()) {
		t.Fatalf("upload: %d %+v", resp.StatusCode, f)
	}
	if b, _ := os.ReadFile(f.Path); string(b) != "\x7fELF..." {
		t.Errorf("stored %q", b)
	}
	if resp, f := up("..%2F..%2Fsettings.elf", "x"); resp.StatusCode != 200 || !strings.HasPrefix(f.Path, app.firmwareDir()) {
		t.Errorf("a name with a path: %d %+v (want it kept inside the folder)", resp.StatusCode, f)
	}
	for _, bad := range []string{"notes.txt", ".hidden.elf", ""} {
		if resp, _ := up(bad, "x"); resp.StatusCode != 400 {
			t.Errorf("%q: %d, want 400", bad, resp.StatusCode)
		}
	}
	if resp, _ := up("empty.bin", ""); resp.StatusCode != 400 {
		t.Errorf("empty file: %d, want 400", resp.StatusCode)
	}
	var list struct{ Files []firmwareFile }
	r, _ := http.Get(srv.URL + "/api/firmware")
	_ = json.NewDecoder(r.Body).Decode(&list)
	r.Body.Close()
	if len(list.Files) != 2 {
		t.Errorf("list %+v", list.Files)
	}
}
