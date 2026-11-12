package local

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// The bench's wiring profile lives on this computer: wiring.json in the data folder. The MCP server
// starts with BENCHPOD_WIRING pointing at it, so every tool reads it (the console's channels, the
// flash target, the capture view's names), with or without an embeddedci.com account. Every
// set_wiring that succeeds, the wiring editor's or the agent's, is written back to it.

func (a *App) wiringPath() string { return filepath.Join(a.store.Dir(), "wiring.json") }

// wiringEnv is the BENCHPOD_WIRING setting for the MCP server: the saved profile, unless the
// environment already names one (development, tests).
func (a *App) wiringEnv() (string, bool) {
	if os.Getenv("BENCHPOD_WIRING") != "" {
		return "", false
	}
	if _, err := os.Stat(a.wiringPath()); err != nil {
		return "", false
	}
	return a.wiringPath(), true
}

// saveWiring keeps the profile a set_wiring call applied.
func (a *App) saveWiring(structured json.RawMessage) {
	var res struct {
		Profile map[string]any `json:"profile"`
	}
	if json.Unmarshal(structured, &res) != nil || len(res.Profile) == 0 {
		return
	}
	b, err := json.MarshalIndent(res.Profile, "", "  ")
	if err != nil {
		return
	}
	tmp := a.wiringPath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, a.wiringPath())
	}
}
