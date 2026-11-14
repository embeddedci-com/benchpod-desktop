package local

import (
	"testing"

	"github.com/zalando/go-keyring"
)

// A hosted provider's key goes only to that provider's host over https.
func TestModelKeyStaysWithItsProvider(t *testing.T) {
	keyring.MockInit() // never the real keychain
	a := &App{keys: &Keys{Env: map[string]string{"BENCHPOD_MODEL_API_KEY": "sk-test"}}}
	cases := []struct {
		provider, base string
		want           string
	}{
		{"openai", "https://api.openai.com/v1", "sk-test"},
		{"openai", "http://api.openai.com/v1", ""},
		{"openai", "https://evil.example/v1", ""},
		{"openrouter", "https://openrouter.ai/api/v1", "sk-test"},
		{"openrouter", "https://192.168.1.5:8000/v1", ""},
		{"custom", "http://192.168.1.5:8000/v1", "sk-test"},
		{"ollama", "http://localhost:11434/v1", "sk-test"},
	}
	for _, c := range cases {
		if got := a.modelKey(Settings{Provider: c.provider, BaseURL: c.base}); got != c.want {
			t.Errorf("%s at %s: key %q, want %q", c.provider, c.base, got, c.want)
		}
	}
}
