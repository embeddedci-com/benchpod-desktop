package local

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOfflineSettings: offline mode refuses hosted providers and cloud pods, and allows a model
// on the user's own network.
func TestOfflineSettings(t *testing.T) {
	t.Setenv("BENCHPOD_OFFLINE", "1")
	app, err := NewApp(t.TempDir(), "test", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	put := func(s map[string]any) int {
		b, _ := json.Marshal(s)
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings", bytes.NewReader(b))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := put(map[string]any{"provider": "openrouter", "base_url": "https://openrouter.ai/api/v1"}); c != 400 {
		t.Errorf("openrouter offline: %d, want 400", c)
	}
	if c := put(map[string]any{"provider": "custom", "base_url": "https://api.openai.com/v1"}); c != 400 {
		t.Errorf("custom pointing at OpenAI offline: %d, want 400", c)
	}
	if c := put(map[string]any{"provider": "custom", "base_url": "http://llm.lab.internal:8000/v1", "connection": "embeddedci:pod"}); c != 400 {
		t.Errorf("cloud pod offline: %d, want 400", c)
	}
	if c := put(map[string]any{"provider": "custom", "base_url": "http://llm.lab.internal:8000/v1", "connection": "192.168.1.50"}); c != 200 {
		t.Errorf("team server + LAN pod offline: %d, want 200", c)
	}
	resp, err := http.Get(srv.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st struct {
		Offline       bool `json:"offline"`
		OfflineForced bool `json:"offline_forced"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&st)
	if !st.Offline || !st.OfflineForced {
		t.Errorf("state offline=%v forced=%v, want both true", st.Offline, st.OfflineForced)
	}
}

func TestDefaultIsLocalModel(t *testing.T) {
	if p := DefaultSettings().Provider; p != "ollama" {
		t.Errorf("default provider %q, want ollama", p)
	}
}
