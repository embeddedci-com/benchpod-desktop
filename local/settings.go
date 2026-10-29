// Package local is the desktop app's backend: its settings, the model key, the BenchPod MCP
// server it starts, and the HTTP API the window talks to.
package local

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/embeddedci-com/benchpod-desktop/policy"
	"github.com/embeddedci-com/benchpod-desktop/providers"
	"github.com/zalando/go-keyring"
)

// Settings are what the user picks in the app. The model key is not here: it lives in the OS
// keychain (or, in development, the environment / a .env file).
type Settings struct {
	Provider string `json:"provider"` // a providers.Preset id
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`

	// Connection is the pod: host[:port], "usb", "usb:<name>" or "embeddedci:<device>".
	// Empty finds the one pod on the LAN.
	Connection string  `json:"connection"`
	LAVoltage  float64 `json:"la_voltage,omitempty"` // 1.8 or 3.3; 0 = keep the pod's

	// ConfirmRisky asks before power, flash and output changes.
	ConfirmRisky bool          `json:"confirm_risky"`
	Limits       policy.Limits `json:"limits"`

	// DUTNotes is what the agent should know about the board: pinout, expected rails,
	// firmware notes. It goes into the system prompt.
	DUTNotes string `json:"dut_notes"`
}

// DefaultSettings is the first-run state: OpenRouter preselected, confirmations on.
func DefaultSettings() Settings {
	p, _ := providers.Get("openrouter")
	return Settings{Provider: p.ID, BaseURL: p.BaseURL, Model: p.DefaultModel, ConfirmRisky: true,
		Limits: policy.Limits{MaxDACVolts: 5, MaxCurrentOut: 0.024}}
}

// Store keeps the settings in the data folder.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore uses dir (created if missing).
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Dir is the data folder.
func (s *Store) Dir() string { return s.dir }

func (s *Store) path() string { return filepath.Join(s.dir, "settings.json") }

// Load returns the saved settings, or the defaults.
func (s *Store) Load() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := DefaultSettings()
	if b, err := os.ReadFile(s.path()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

// Save writes the settings.
func (s *Store) Save(st Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}

// ---- the model key ----

const keyringService = "com.embeddedci.benchpod-desktop"

// KeySource says where the key in use came from.
type KeySource string

const (
	KeyNone     KeySource = ""
	KeyKeychain KeySource = "keychain"
	KeyEnv      KeySource = "env" // environment or .env: development
)

// Keys reads and writes the model key per provider.
type Keys struct {
	// Env is the environment plus any .env file, read once at start.
	Env map[string]string
}

// NewKeys reads the environment and the .env files given (missing ones are skipped).
func NewKeys(envFiles ...string) *Keys {
	env := map[string]string{}
	for _, f := range envFiles {
		for k, v := range readDotenv(f) {
			env[k] = v
		}
	}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v // the real environment wins over a .env file
		}
	}
	return &Keys{Env: env}
}

// Get returns the key for a provider: the keychain first, then BENCHPOD_MODEL_API_KEY, then
// the preset's own variable (OPENROUTER_API_KEY, say).
func (k *Keys) Get(provider string) (string, KeySource) {
	if v, err := keyring.Get(keyringService, provider); err == nil && v != "" {
		return v, KeyKeychain
	}
	if v := k.Env["BENCHPOD_MODEL_API_KEY"]; v != "" {
		return v, KeyEnv
	}
	if p, ok := providers.Get(provider); ok && p.KeyEnv != "" {
		if v := k.Env[p.KeyEnv]; v != "" {
			return v, KeyEnv
		}
	}
	return "", KeyNone
}

// Set stores the key in the OS keychain. There is no plain-text fallback: on a system without
// a keychain the key goes in the environment or a .env file instead.
func (k *Keys) Set(provider, key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("empty key")
	}
	if err := keyring.Set(keyringService, provider, strings.TrimSpace(key)); err != nil {
		return errors.New("could not store the key in the system keychain (" + err.Error() + "); set BENCHPOD_MODEL_API_KEY in a .env file instead")
	}
	return nil
}

// Delete removes the stored key.
func (k *Keys) Delete(provider string) error {
	err := keyring.Delete(keyringService, provider)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// readDotenv parses KEY=VALUE lines, with optional quotes and export prefixes.
func readDotenv(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		out[strings.TrimSpace(k)] = v
	}
	return out
}
