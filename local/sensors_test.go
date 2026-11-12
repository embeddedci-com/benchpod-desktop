package local

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/embeddedci-com/benchpod-desktop/sensors"
)

const ntcYAML = `format: 1
kind: analog
name: ntc10k   # a comment that stays
label: "NTC 10k divider"
output: voltage
value: "1.65"
`

type sensorClient struct {
	t   *testing.T
	srv *httptest.Server
}

func (c sensorClient) do(method, path string, body any, out any) (int, http.Header, []byte) {
	c.t.Helper()
	var r io.Reader = http.NoBody
	ct := "application/json"
	switch b := body.(type) {
	case nil:
	case []byte:
		r, ct = bytes.NewReader(b), "application/yaml"
	default:
		js, _ := json.Marshal(b)
		r = bytes.NewReader(js)
	}
	req, _ := http.NewRequest(method, c.srv.URL+path, r)
	req.Header.Set("Content-Type", ct)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil {
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode, resp.Header, raw
}

func newSensorApp(t *testing.T) (*App, sensorClient, string) {
	t.Setenv(sensors.CacheEnv, t.TempDir())
	dir := t.TempDir()
	app, err := NewApp(dir, "test", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	return app, sensorClient{t, srv}, dir
}

type defJSON struct {
	ID, Name, Kind, Label, Source string
	SourceFormat                  string          `json:"source_format"`
	Definition                    json.RawMessage `json:"definition"`
	Code                          string          `json:"code"`
	Error                         string          `json:"error"`
}

// TestSensorDefinitionRoutes: create, list, get, rename, duplicate, upload, download, zip, delete,
// with the files on disk as the user wrote them.
func TestSensorDefinitionRoutes(t *testing.T) {
	_, c, dir := newSensorApp(t)

	var d defJSON
	if st, _, raw := c.do("POST", "/api/sensor-definitions", map[string]string{"source": ntcYAML}, &d); st != 201 || d.ID != "ntc10k" || d.Kind != "analog" {
		t.Fatalf("create: %d %s", st, raw)
	}
	onDisk := filepath.Join(dir, "sensors", "sensors", "analog", "ntc10k.yaml")
	if b, err := os.ReadFile(onDisk); err != nil || string(b) != ntcYAML {
		t.Fatalf("file on disk: %v %q", err, b)
	}
	if st, _, _ := c.do("POST", "/api/sensor-definitions", map[string]string{"source": ntcYAML}, &d); st != 409 || d.Code != "name_taken" {
		t.Fatalf("same name: %d %+v", st, d)
	}
	if st, _, _ := c.do("POST", "/api/sensor-definitions", map[string]string{"source": "format: 1\nkind: nope\nname: x\n"}, &d); st != 400 || d.Code != "invalid_definition" {
		t.Fatalf("bad kind: %d %+v", st, d)
	}

	var list struct {
		Definitions []defJSON `json:"definitions"`
	}
	c.do("GET", "/api/sensor-definitions?kind=analog", nil, &list)
	if len(list.Definitions) != 1 || list.Definitions[0].Source != "" {
		t.Fatalf("list: %+v", list)
	}
	c.do("GET", "/api/sensor-definitions?kind=i2c", nil, &list)
	if len(list.Definitions) != 0 {
		t.Fatalf("kind filter: %+v", list)
	}

	// Rename through a save: the file moves, the id follows the name.
	renamed := strings.Replace(ntcYAML, "name: ntc10k", "name: ntc_b", 1)
	if st, _, raw := c.do("PUT", "/api/sensor-definitions/ntc10k", map[string]string{"source": renamed}, &d); st != 200 || d.ID != "ntc_b" {
		t.Fatalf("rename: %d %s", st, raw)
	}
	if _, err := os.Stat(onDisk); !os.IsNotExist(err) {
		t.Fatal("the old file must go on a rename")
	}
	if st, _, _ := c.do("GET", "/api/sensor-definitions/ntc10k", nil, &d); st != 404 || d.Code != "not_found" {
		t.Fatalf("old id: %d", st)
	}
	if st, _, _ := c.do("PUT", "/api/sensor-definitions/missing", map[string]string{"source": ntcYAML}, &d); st != 404 {
		t.Fatalf("put missing: %d", st)
	}

	// Duplicate: name_copy, the YAML text and its comment kept.
	if st, _, raw := c.do("POST", "/api/sensor-definitions/ntc_b/duplicate", nil, &d); st != 201 || d.Name != "ntc_b_copy" || !strings.Contains(d.Source, "# a comment that stays") {
		t.Fatalf("duplicate: %d %s", st, raw)
	}
	if st, _, _ := c.do("POST", "/api/sensor-definitions/ntc_b/duplicate", nil, &d); st != 201 || d.Name != "ntc_b_copy2" {
		t.Fatalf("duplicate 2: %d %+v", st, d)
	}

	// Upload a JSON stimulus as the raw body, then again: 409, then replace.
	stim := []byte(`{"format": 1, "kind": "stimulus", "name": "warmup", "rate_hz": 10, "signals": []}`)
	var up struct {
		Replaced   bool    `json:"replaced"`
		Definition defJSON `json:"definition"`
		Code       string  `json:"code"`
	}
	if st, _, raw := c.do("POST", "/api/sensor-definitions/upload", stim, &up); st != 201 || up.Replaced || up.Definition.SourceFormat != "json" {
		t.Fatalf("upload: %d %s", st, raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "sensors", "stimuli", "warmup.json")); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := c.do("POST", "/api/sensor-definitions/upload", stim, &up); st != 409 || up.Code != "name_taken" {
		t.Fatalf("upload again: %d", st)
	}
	if st, _, _ := c.do("POST", "/api/sensor-definitions/upload?replace=1", stim, &up); st != 200 || !up.Replaced {
		t.Fatalf("replace: %d", st)
	}
	// Multipart, as a browser form sends it.
	var mp bytes.Buffer
	mw := multipart.NewWriter(&mp)
	fw, _ := mw.CreateFormFile("file", "other.yaml")
	_, _ = fw.Write([]byte(strings.Replace(ntcYAML, "name: ntc10k", "name: other", 1)))
	_ = mw.Close()
	req, _ := http.NewRequest("POST", c.srv.URL+"/api/sensor-definitions/upload", &mp)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 201 {
		t.Fatalf("multipart: %v %v", err, resp.StatusCode)
	}

	// Downloads: as written, or converted.
	st, h, body := c.do("GET", "/api/sensor-definitions/ntc_b/download", nil, nil)
	if st != 200 || string(body) != renamed || !strings.Contains(h.Get("Content-Disposition"), `filename="ntc_b.yaml"`) {
		t.Fatalf("download: %d %s %s", st, h, body)
	}
	_, h, body = c.do("GET", "/api/sensor-definitions/warmup/download?format=yaml", nil, nil)
	if !strings.Contains(h.Get("Content-Disposition"), "warmup.yaml") || !strings.Contains(string(body), "kind: stimulus") {
		t.Fatalf("json as yaml: %s", body)
	}
	_, _, body = c.do("GET", "/api/sensor-definitions/download", nil, nil)
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if strings.Join(names, " ") != "sensors/analog/ntc_b.yaml sensors/analog/ntc_b_copy.yaml sensors/analog/ntc_b_copy2.yaml sensors/analog/other.yaml stimuli/warmup.json" {
		t.Fatalf("zip: %v", names)
	}

	if st, _, _ := c.do("DELETE", "/api/sensor-definitions/ntc_b_copy", nil, nil); st != 204 {
		t.Fatalf("delete: %d", st)
	}
	if st, _, _ := c.do("DELETE", "/api/sensor-definitions/ntc_b_copy", nil, nil); st != 404 {
		t.Fatalf("delete again: %d", st)
	}
	if st, _, _ := c.do("GET", "/api/sensor-definitions/..%2Fsettings", nil, nil); st != 404 {
		t.Fatalf("path id: %d", st)
	}
}

// fakeRelease is a GitHub source tarball of a library release.
func fakeRelease(t *testing.T, v string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := map[string]string{
		"library.json":               `{"format":1,"version":"` + v + `","entries":[{"path":"sensors/analog/ntc10k.yaml","name":"ntc10k","kind":"analog","label":"NTC 10k divider"}]}`,
		"sensors/analog/ntc10k.yaml": ntcYAML,
	}
	for name, body := range files {
		_ = tw.WriteHeader(&tar.Header{Name: "benchpod-sensors-" + v + "/" + name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// TestSensorLibraryRoutes: the embedded library with no network, a version from a fake GitHub kept
// in the settings, a missing tag refused, and offline mode falling back with a warning.
func TestSensorLibraryRoutes(t *testing.T) {
	app, c, _ := newSensorApp(t)
	hits := 0
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/embeddedci-com/benchpod-sensors/archive/refs/tags/v0.2.0.tar.gz" {
			_, _ = w.Write(fakeRelease(t, "0.2.0"))
			return
		}
		http.NotFound(w, r)
	}))
	defer gh.Close()
	cache := t.TempDir()
	lib, err := sensors.New(sensors.Config{CacheDir: cache, Timeout: 2 * time.Second,
		ArchiveURL: gh.URL + "/%s/archive/refs/tags/v%s.tar.gz", LatestURL: gh.URL + "/repos/%s/releases/latest",
		Offline: func() bool { return app.offline(app.store.Load()) }})
	if err != nil {
		t.Fatal(err)
	}
	app.sensorLib = lib

	type listing struct {
		Version  string   `json:"version"`
		Origin   string   `json:"origin"`
		Warnings []string `json:"warnings"`
		Entries  []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	var l listing
	c.do("GET", "/api/sensor-library", nil, &l)
	if l.Version != "0.1.0" || l.Origin != "embedded" || len(l.Entries) != 5 || hits != 0 {
		t.Fatalf("embedded: %+v, %d hits", l, hits)
	}
	var e struct {
		Name, Source, Version string
		SourceFormat          string `json:"source_format"`
	}
	if st, _, _ := c.do("GET", "/api/sensor-library/entries/bme280", nil, &e); st != 200 || e.Name != "bme280" || !strings.Contains(e.Source, "kind: i2c") {
		t.Fatalf("entry: %d %+v", st, e)
	}
	if st, _, _ := c.do("GET", "/api/sensor-library/entries/nope", nil, nil); st != 404 {
		t.Fatalf("missing entry: %d", st)
	}
	_, h, _ := c.do("GET", "/api/sensor-library/entries/sht4x/download?format=json", nil, nil)
	if !strings.Contains(h.Get("Content-Disposition"), "sht4x.json") {
		t.Fatalf("library download: %s", h)
	}

	var s struct {
		Override  string   `json:"override"`
		CanChange bool     `json:"can_change"`
		Warnings  []string `json:"warnings"`
		Error     string   `json:"error"`
	}
	if st, _, _ := c.do("PUT", "/api/sensor-library/settings", map[string]string{"version": "9.9.9"}, &s); st != 400 {
		t.Fatalf("missing tag: %d %+v", st, s)
	}
	if st, _, _ := c.do("PUT", "/api/sensor-library/settings", map[string]string{"version": "banana"}, &s); st != 400 {
		t.Fatalf("bad value: %d", st)
	}
	if st, _, _ := c.do("PUT", "/api/sensor-library/settings", map[string]string{"version": "v0.2.0"}, &s); st != 200 || s.Override != "0.2.0" || !s.CanChange {
		t.Fatalf("set: %d %+v", st, s)
	}
	if app.store.Load().SensorLibrary != "0.2.0" {
		t.Fatal("the version must be kept in the settings")
	}
	c.do("GET", "/api/sensor-library", nil, &l)
	if l.Version != "0.2.0" || len(l.Entries) != 1 {
		t.Fatalf("0.2.0: %+v", l)
	}
	if _, err := os.Stat(filepath.Join(cache, "0.2.0", "library.json")); err != nil {
		t.Fatal("the fetched version must be cached per version")
	}

	// Offline: a version not in the cache falls back to the embedded copy, with a warning.
	st := app.store.Load()
	st.Offline, st.SensorLibrary = true, "0.3.0"
	_ = app.store.Save(st)
	before := hits
	c.do("GET", "/api/sensor-library", nil, &l)
	if l.Version != "0.1.0" || len(l.Warnings) == 0 || hits != before {
		t.Fatalf("offline: %+v, %d hits", l, hits-before)
	}
	// ... and one in the cache still serves.
	c.do("GET", "/api/sensor-library?version=0.2.0", nil, &l)
	if l.Version != "0.2.0" || hits != before {
		t.Fatalf("offline cache: %+v", l)
	}
}

func TestSensorCommandOnlySensorCommands(t *testing.T) {
	_, c, _ := newSensorApp(t)
	var e struct{ Error string }
	if st, _, _ := c.do("POST", "/api/sensors/command", map[string]any{"command": map[string]any{"cmd": "power_on"}}, &e); st != 400 || !strings.Contains(e.Error, "sensor_") {
		t.Fatalf("power_on: %d %+v", st, e)
	}
	if st, _, _ := c.do("POST", "/api/sensors/command", map[string]any{}, &e); st != 400 {
		t.Fatalf("no command: %d", st)
	}
	got := sensorLogArgs(map[string]any{"cmd": "sensor_define", "data_b64": "QUJD", "offset": 0})
	if got["data_b64"] != "[4 base64 chars]" || got["offset"] != 0 {
		t.Fatalf("log args: %v", got)
	}
}

func TestSaveFileNeverOverwrites(t *testing.T) {
	app, c, _ := newSensorApp(t)
	app.saveDir = t.TempDir()
	var out struct{ Path string }
	for i, want := range []string{"bme280.yaml", "bme280 (2).yaml"} {
		if st, _, raw := c.do("POST", "/api/files/save?name=bme280.yaml", []byte("v"+string(rune('1'+i))), &out); st != 200 || filepath.Base(out.Path) != want {
			t.Fatalf("save %d: %d %s", i, st, raw)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(app.saveDir, "bme280.yaml")); string(b) != "v1" {
		t.Fatalf("first file changed: %q", b)
	}
	for _, bad := range []string{"../x.yaml", ".hidden", "", "a%2Fb.yaml"} {
		if st, _, _ := c.do("POST", "/api/files/save?name="+bad, []byte("x"), nil); st != 400 {
			t.Fatalf("name %q: %d", bad, st)
		}
	}
}
