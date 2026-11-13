package local

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/embeddedci-com/benchpod-desktop/chat"
)

// The waveform library on this computer: recordings to play out of the DAC, kept as JSON files in
// the data folder's waveforms/ folder. No account: the web app's library lives on embeddedci.com.
//
//	GET    /api/waveforms                 -> {"waveforms": [meta...]}
//	GET    /api/waveforms/{id}            -> the waveform with its samples
//	POST   /api/waveforms                 {name, unit, sample_rate_hz, samples} -> meta
//	POST   /api/waveforms/from-capture    {name} -> meta: the last ADC capture
//	PATCH  /api/waveforms/{id}            {name} -> meta
//	DELETE /api/waveforms/{id}
//
// The bench controls play one with the replay tool and a local_waveform argument naming it:
// benchpod-local puts its samples in before the policy checks the call.

type waveform struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Unit         string    `json:"unit"` // V, or mA for a recording of the 4-20 mA input
	SampleRateHz float64   `json:"sample_rate_hz"`
	SampleCount  int       `json:"sample_count"`
	Min          float64   `json:"min"`
	Max          float64   `json:"max"`
	Created      time.Time `json:"created"`
	Samples      []float64 `json:"samples,omitempty"`
}

// maxWaveformSamples is the longest waveform the library takes (the pod's deep replay holds about
// this many).
const maxWaveformSamples = 4 << 20

type waveformStore struct {
	mu  sync.Mutex
	dir string
}

func (s *waveformStore) path(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\.`) {
		return "", errors.New("bad waveform id")
	}
	return filepath.Join(s.dir, id+".json"), nil
}

func (s *waveformStore) get(id string) (*waveform, error) {
	p, err := s.path(id)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no waveform %q in the library", id)
		}
		return nil, err
	}
	var w waveform
	if err := json.Unmarshal(b, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

func (s *waveformStore) put(w *waveform) error {
	p, err := s.path(w.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(w)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (s *waveformStore) list() ([]waveform, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	out := []waveform{}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() {
			continue
		}
		w, err := s.get(id)
		if err != nil {
			continue
		}
		w.Samples = nil
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// newWaveform checks and completes a waveform to store.
func newWaveform(name, unit string, rate float64, samples []float64) (*waveform, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return nil, errors.New("give the waveform a name")
	case len(samples) < 2:
		return nil, errors.New("a waveform needs at least 2 samples")
	case len(samples) > maxWaveformSamples:
		return nil, fmt.Errorf("%d samples is more than the %d the library takes", len(samples), maxWaveformSamples)
	case rate <= 0 || rate > 1e6:
		return nil, errors.New("sample_rate_hz is above 0 and at most 1 MHz")
	}
	if unit == "" {
		unit = "V"
	}
	if unit != "V" && unit != "mA" {
		return nil, errors.New("unit is V or mA")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	w := &waveform{ID: hex.EncodeToString(b), Name: name, Unit: unit, SampleRateHz: rate, SampleCount: len(samples),
		Min: samples[0], Max: samples[0], Created: time.Now().UTC(), Samples: samples}
	for _, v := range samples {
		w.Min, w.Max = min(w.Min, v), max(w.Max, v)
	}
	return w, nil
}

// expandLocalWaveform turns replay(local_waveform: id) into replay(volts: [...]) with the
// waveform's rate, so the policy's limits see the values that will play. A recording in mA plays
// as amps on the 4-20 mA output.
func (a *App) expandLocalWaveform(name string, args map[string]any) error {
	id, ok := args["local_waveform"].(string)
	if name != "replay" || !ok {
		return nil
	}
	delete(args, "local_waveform")
	w, err := a.waveforms.get(id)
	if err != nil {
		return err
	}
	scale := 1.0
	if w.Unit == "mA" {
		if p, _ := args["dac_path"].(string); p == "current_out" {
			scale = 0.001
		}
	}
	volts := make([]any, len(w.Samples))
	for i, v := range w.Samples {
		volts[i] = v * scale
	}
	args["volts"] = volts
	if _, ok := args["sample_rate_hz"]; !ok {
		args["sample_rate_hz"] = w.SampleRateHz
	}
	stuckLevel(args, w.Samples, scale)
	return nil
}

// stuckLevel gives a stuck fault without a level the code of the sample just before its window,
// so it freezes the output where the signal was (the web app's stuck). The pod's replay would hold
// full scale instead, and the limits check the level that will play.
func stuckLevel(args map[string]any, samples []float64, scale float64) {
	f, ok := args["fault"].(map[string]any)
	if !ok || f["type"] != "stuck" || f["level"] != nil || len(samples) == 0 {
		return
	}
	i := 0
	if s, ok := f["start"].(float64); ok && s >= 1 {
		i = min(int(s)-1, len(samples)-1)
	}
	path, _ := args["dac_path"].(string)
	lo, hi := outputSpan(path)
	v := samples[i] * scale
	if m, _ := args["mapping"].(string); m == "fit" {
		lo, hi = samples[0]*scale, samples[0]*scale
		for _, x := range samples {
			lo, hi = min(lo, x*scale), max(hi, x*scale)
		}
	}
	code := 32768.0
	if hi > lo {
		code = math.Round((v - lo) / (hi - lo) * 65535)
	}
	f["level"] = math.Max(0, math.Min(65535, code))
}

// outputSpan is the range a DAC output spans (nominal): volts, or amps on current_out.
func outputSpan(path string) (float64, float64) {
	switch path {
	case "5v":
		return 0, 5
	case "12v":
		return -12, 12
	case "current_out":
		return 0.004, 0.0201
	}
	return 0, 3.3
}

func (a *App) serveWaveforms(w http.ResponseWriter, r *http.Request) {
	list, err := a.waveforms.list()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	chat.WriteJSON(w, http.StatusOK, map[string]any{"waveforms": list})
}

func (a *App) serveWaveform(w http.ResponseWriter, r *http.Request) {
	wf, err := a.waveforms.get(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	chat.WriteJSON(w, http.StatusOK, wf)
}

func (a *App) serveCreateWaveform(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name         string    `json:"name"`
		Unit         string    `json:"unit"`
		SampleRateHz float64   `json:"sample_rate_hz"`
		Samples      []float64 `json:"samples"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<20)).Decode(&body); err != nil {
		http.Error(w, "bad waveform: "+err.Error(), http.StatusBadRequest)
		return
	}
	a.storeWaveform(w, body.Name, body.Unit, body.SampleRateHz, body.Samples)
}

// serveWaveformFromCapture saves the last ADC capture (the agent's or the capture view's).
func (a *App) serveWaveformFromCapture(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		http.Error(w, `send {"name": "..."}`, http.StatusBadRequest)
		return
	}
	t, err := a.mcp(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	text, err := t.ReadResource(r.Context(), "benchpod://capture/adc")
	if err != nil {
		http.Error(w, "the BenchPod tools cannot hand over captures: update embeddedci-mcp ("+err.Error()+")", http.StatusBadGateway)
		return
	}
	var cap struct {
		Seq          int       `json:"seq"`
		Unit         string    `json:"unit"`
		SampleRateHz float64   `json:"sample_rate_hz"`
		Samples      []float64 `json:"samples"`
	}
	if err := json.Unmarshal([]byte(text), &cap); err != nil || cap.Seq == 0 {
		http.Error(w, "no ADC capture yet: capture the analog input first", http.StatusConflict)
		return
	}
	a.storeWaveform(w, body.Name, cap.Unit, cap.SampleRateHz, cap.Samples)
}

func (a *App) storeWaveform(w http.ResponseWriter, name, unit string, rate float64, samples []float64) {
	wf, err := newWaveform(name, unit, rate, samples)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.waveforms.mu.Lock()
	err = a.waveforms.put(wf)
	a.waveforms.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	wf.Samples = nil
	chat.WriteJSON(w, http.StatusOK, wf)
}

func (a *App) serveRenameWaveform(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		http.Error(w, `send {"name": "..."}`, http.StatusBadRequest)
		return
	}
	a.waveforms.mu.Lock()
	defer a.waveforms.mu.Unlock()
	wf, err := a.waveforms.get(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	wf.Name = strings.TrimSpace(body.Name)
	if err := a.waveforms.put(wf); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	wf.Samples = nil
	chat.WriteJSON(w, http.StatusOK, wf)
}

func (a *App) serveDeleteWaveform(w http.ResponseWriter, r *http.Request) {
	p, err := a.waveforms.path(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.waveforms.mu.Lock()
	defer a.waveforms.mu.Unlock()
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
