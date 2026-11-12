package local

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWaveformLibrary: store, list, rename and delete a waveform, and play one through the
// policy: the limit sees its samples.
func TestWaveformLibrary(t *testing.T) {
	app, err := NewApp(t.TempDir(), "test", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	do := func(method, path string, body any, out any) int {
		var r *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		} else {
			r = bytes.NewReader(nil)
		}
		req, _ := http.NewRequest(method, srv.URL+path, r)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			_ = json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}
	var wf waveform
	if c := do("POST", "/api/waveforms", map[string]any{"name": "ramp", "sample_rate_hz": 1000, "samples": []float64{0, 1, 2, 7}}, &wf); c != 200 || wf.ID == "" || wf.Max != 7 {
		t.Fatalf("create: %d %+v", c, wf)
	}
	if c := do("POST", "/api/waveforms", map[string]any{"name": "", "sample_rate_hz": 1000, "samples": []float64{0, 1}}, nil); c != 400 {
		t.Errorf("nameless waveform: %d, want 400", c)
	}
	var list struct{ Waveforms []waveform }
	do("GET", "/api/waveforms", nil, &list)
	if len(list.Waveforms) != 1 || list.Waveforms[0].Samples != nil {
		t.Fatalf("list %+v", list)
	}
	if c := do("PATCH", "/api/waveforms/"+wf.ID, map[string]any{"name": "ramp 2"}, &wf); c != 200 || wf.Name != "ramp 2" {
		t.Errorf("rename: %d %+v", c, wf)
	}
	// 7 V is above the default 5 V DAC limit: refused before anything runs.
	var res toolResponse
	do("POST", "/api/tool/replay", map[string]any{"args": map[string]any{"local_waveform": wf.ID}, "confirmed": true}, &res)
	if !res.Refused || !strings.Contains(res.Text, "limit") {
		t.Errorf("replay of a 7 V waveform: %+v, want refused by the limit", res)
	}
	do("POST", "/api/tool/replay", map[string]any{"args": map[string]any{"local_waveform": "nope"}, "confirmed": true}, &res)
	if !res.Refused {
		t.Errorf("replay of a missing waveform: %+v", res)
	}
	if c := do("DELETE", "/api/waveforms/"+wf.ID, nil, nil); c != 204 {
		t.Errorf("delete: %d", c)
	}
	if c := do("GET", "/api/waveforms/"+wf.ID, nil, nil); c != 404 {
		t.Errorf("get after delete: %d", c)
	}
	if c := do("GET", "/api/waveforms/..%2Fsettings", nil, nil); c == 200 {
		t.Errorf("path traversal answered 200")
	}
}
