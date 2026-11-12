package local

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBenchControlsPolicy: the bench controls go through the agent's policy. connect is the app's
// own, a risky call waits for the user's yes, and the DAC limit refuses a call above it.
func TestBenchControlsPolicy(t *testing.T) {
	app, err := NewApp(t.TempDir(), "test", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	call := func(name string, body map[string]any) toolResponse {
		b, _ := json.Marshal(body)
		resp, err := http.Post(srv.URL+"/api/tool/"+name, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: HTTP %d", name, resp.StatusCode)
		}
		var r toolResponse
		_ = json.NewDecoder(resp.Body).Decode(&r)
		return r
	}
	if r := call("connect", map[string]any{}); !r.Refused {
		t.Errorf("connect: %+v, want refused", r)
	}
	if r := call("power_on", map[string]any{}); !r.Confirm {
		t.Errorf("power_on without a yes: %+v, want confirm", r)
	}
	r := call("dac_output", map[string]any{"args": map[string]any{"volts": 9.0}, "confirmed": true})
	if !r.Refused || !strings.Contains(r.Text, "limit") {
		t.Errorf("dac_output 9 V: %+v, want refused by the limit", r)
	}
}
