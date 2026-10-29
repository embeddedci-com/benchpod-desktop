// Package providers lists the model endpoints the app ships presets for, and the models we
// recommend for driving a BenchPod. Every one speaks the OpenAI chat-completions API, so a
// preset is only a base URL, whether it needs a key, and a default model.
package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Preset is a known endpoint.
type Preset struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	NeedKey bool   `json:"need_key"`
	// Local means nothing leaves the machine (or the user's own network).
	Local        bool   `json:"local"`
	DefaultModel string `json:"default_model"`
	KeyURL       string `json:"key_url,omitempty"` // where to get a key
	// KeyEnv is the environment variable (or .env entry) the key is read from in development.
	KeyEnv string `json:"key_env,omitempty"`
}

// Presets are offered on first run, in this order.
var Presets = []Preset{
	{ID: "openrouter", Name: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", NeedKey: true,
		DefaultModel: "anthropic/claude-sonnet-5.5", KeyURL: "https://openrouter.ai/settings/keys", KeyEnv: "OPENROUTER_API_KEY"},
	{ID: "anthropic", Name: "Anthropic", BaseURL: "https://api.anthropic.com/v1", NeedKey: true,
		DefaultModel: "claude-sonnet-5-5", KeyURL: "https://console.anthropic.com/settings/keys", KeyEnv: "ANTHROPIC_API_KEY"},
	{ID: "openai", Name: "OpenAI", BaseURL: "https://api.openai.com/v1", NeedKey: true,
		KeyURL: "https://platform.openai.com/api-keys", KeyEnv: "OPENAI_API_KEY"},
	{ID: "ollama", Name: "Ollama (local)", BaseURL: "http://localhost:11434/v1", Local: true},
	{ID: "lmstudio", Name: "LM Studio (local)", BaseURL: "http://localhost:1234/v1", Local: true},
	{ID: "custom", Name: "Other OpenAI-compatible server", Local: false},
}

// Get returns the preset with id.
func Get(id string) (Preset, bool) {
	for _, p := range Presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// Model is a model as a picker shows it.
type Model struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Note says what it is good for, or warns ("small: tool calling is unreliable").
	Note string `json:"note,omitempty"`
	// Prices in USD per million tokens, when known.
	InputPerM  float64 `json:"input_per_m,omitempty"`
	OutputPerM float64 `json:"output_per_m,omitempty"`
}

// Recommended are the OpenRouter models we have found to drive the BenchPod tools well,
// strongest first. Judge a model on real bench work, not benchmarks (design doc 5.2).
var Recommended = []Model{
	{ID: "anthropic/claude-sonnet-5.5", Name: "Claude Sonnet 5.5", Note: "recommended: the most reliable tool use", InputPerM: 2, OutputPerM: 10},
	{ID: "anthropic/claude-haiku-5.5", Name: "Claude Haiku 5.5", Note: "fast and cheap, good for routine checks", InputPerM: 0.1, OutputPerM: 0.5},
	{ID: "google/gemini-3.6-flash", Name: "Gemini 3.6 Flash", Note: "cheap; less tested on BenchPod work", InputPerM: 0.75, OutputPerM: 3.75},
	{ID: "qwen/qwen3.8-flash", Name: "Qwen 3.8 Flash", Note: "open weights; also runs locally", InputPerM: 0.15, OutputPerM: 0.47},
	{ID: "z-ai/glm-5.3", Name: "GLM 5.3", Note: "open weights", InputPerM: 0.07, OutputPerM: 1.4},
}

// SmallModelWarning returns a warning for a model that is probably too small to call tools
// well, or "".
func SmallModelWarning(model string) string {
	m := strings.ToLower(model)
	for _, tag := range []string{":1b", ":2b", ":3b", ":4b", ":7b", ":8b", "-1b", "-3b", "-4b", "-7b", "-8b", "mini", "nano", "tiny"} {
		if strings.Contains(m, tag) {
			return "small models often call tools wrongly or not at all; try a larger one if the agent misbehaves"
		}
	}
	return ""
}

// List asks an endpoint for its models (GET {base}/models). Local servers list what is pulled;
// OpenRouter lists hundreds, so only tool-capable ones are kept there.
func List(ctx context.Context, baseURL, apiKey string) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("GET /models: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID                  string   `json:"id"`
			Name                string   `json:"name"`
			SupportedParameters []string `json:"supported_parameters"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	var out []Model
	for _, d := range body.Data {
		if d.SupportedParameters != nil && !contains(d.SupportedParameters, "tools") {
			continue
		}
		name := d.Name
		if name == "" {
			name = d.ID
		}
		out = append(out, Model{ID: d.ID, Name: name, Note: SmallModelWarning(d.ID)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
