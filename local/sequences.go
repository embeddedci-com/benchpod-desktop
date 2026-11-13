package local

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/embeddedci-com/benchpod-desktop/chat"
)

// Saved bus console sequences on this computer: log rows of the bus dashboard kept as the tool
// calls they were, in one JSON file in the data folder. No account needed. The routes have the
// shape of the EmbeddedCI server's /api/benchpod/bus-sequences, so the page's SequenceStore is the
// same thin layer over either:
//
//	GET    /api/bus-sequences          -> {"sequences": [...]}
//	POST   /api/bus-sequences          {name, steps, version} -> 201 sequence; 409 when the name is taken
//	PATCH  /api/bus-sequences/{id}     any of {name, steps, version} -> sequence
//	DELETE /api/bus-sequences/{id}
//
// Signed in to embeddedci.com (an EmbeddedCI API key in the keychain or BENCHPOD_API_KEY, not
// offline), /api/account/bus-sequences[/{id}] passes the same calls to the account's sequences, so
// the page can share one in the organization:
//
//	GET    /api/account                -> {"signed_in": bool}

const (
	sequencesFile     = "bus-sequences.json"
	maxSequenceBody   = 256 << 10
	maxSequenceSteps  = 1000
	maxSequenceName   = 128
	accountAPITimeout = 20 * time.Second
)

type busSequence struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Version   int             `json:"version"`
	Steps     json.RawMessage `json:"steps"`
	Mine      bool            `json:"mine"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type sequenceStore struct {
	mu   sync.Mutex
	path string
}

func (s *sequenceStore) load() ([]busSequence, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []busSequence{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Sequences []busSequence `json:"sequences"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if f.Sequences == nil {
		f.Sequences = []busSequence{}
	}
	return f.Sequences, nil
}

func (s *sequenceStore) save(list []busSequence) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(map[string]any{"version": 1, "sequences": list}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// sequenceBody is a create or update; nil fields are left as they are.
type sequenceBody struct {
	Name    *string         `json:"name"`
	Version *int            `json:"version"`
	Steps   json.RawMessage `json:"steps"`
}

func (b *sequenceBody) check() error {
	if b.Name != nil {
		n := strings.TrimSpace(*b.Name)
		if n == "" || len([]rune(n)) > maxSequenceName {
			return errors.New("name must be 1-128 characters")
		}
		b.Name = &n
	}
	if b.Version != nil && *b.Version < 1 {
		return errors.New("version must be 1 or more")
	}
	if b.Steps != nil {
		var steps []map[string]json.RawMessage
		t := bytes.TrimSpace(b.Steps)
		if len(t) == 0 || t[0] != '[' || json.Unmarshal(t, &steps) != nil {
			return errors.New("steps must be a list of objects")
		}
		if len(steps) > maxSequenceSteps {
			return errors.New("a sequence has at most 1000 steps")
		}
		for _, st := range steps {
			var tool string
			if st == nil || json.Unmarshal(st["tool"], &tool) != nil || strings.TrimSpace(tool) == "" {
				return errors.New(`each step needs a "tool"`)
			}
		}
		b.Steps = t
	}
	return nil
}

func readSequenceBody(w http.ResponseWriter, r *http.Request) (*sequenceBody, bool) {
	var b sequenceBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSequenceBody)).Decode(&b); err != nil {
		jsonError(w, http.StatusBadRequest, "bad sequence: "+err.Error())
		return nil, false
	}
	if err := b.check(); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return &b, true
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	chat.WriteJSON(w, code, map[string]string{"error": msg})
}

func nameTaken(list []busSequence, name, except string) bool {
	for _, s := range list {
		if s.ID != except && strings.EqualFold(s.Name, name) {
			return true
		}
	}
	return false
}

func (a *App) mountSequences(mux *http.ServeMux) {
	a.sequences.path = filepath.Join(a.store.Dir(), sequencesFile)
	mux.HandleFunc("GET /api/bus-sequences", a.serveSequences)
	mux.HandleFunc("POST /api/bus-sequences", a.serveCreateSequence)
	mux.HandleFunc("PATCH /api/bus-sequences/{id}", a.serveUpdateSequence)
	mux.HandleFunc("PUT /api/bus-sequences/{id}", a.serveUpdateSequence)
	mux.HandleFunc("DELETE /api/bus-sequences/{id}", a.serveDeleteSequence)
	mux.HandleFunc("GET /api/account", func(w http.ResponseWriter, r *http.Request) {
		_, ok := a.accountKey()
		chat.WriteJSON(w, http.StatusOK, map[string]any{"signed_in": ok})
	})
	mux.HandleFunc("/api/account/bus-sequences", a.serveAccountSequences)
	mux.HandleFunc("/api/account/bus-sequences/{id}", a.serveAccountSequences)
}

func (a *App) serveSequences(w http.ResponseWriter, r *http.Request) {
	a.sequences.mu.Lock()
	defer a.sequences.mu.Unlock()
	list, err := a.sequences.load()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not read "+sequencesFile+": "+err.Error())
		return
	}
	chat.WriteJSON(w, http.StatusOK, map[string]any{"sequences": list})
}

func (a *App) serveCreateSequence(w http.ResponseWriter, r *http.Request) {
	b, ok := readSequenceBody(w, r)
	if !ok {
		return
	}
	if b.Name == nil || b.Steps == nil {
		jsonError(w, http.StatusBadRequest, "a sequence needs a name and steps")
		return
	}
	a.sequences.mu.Lock()
	defer a.sequences.mu.Unlock()
	list, err := a.sequences.load()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if nameTaken(list, *b.Name, "") {
		jsonError(w, http.StatusConflict, "you already have a sequence with that name")
		return
	}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	now := time.Now().UTC()
	s := busSequence{ID: hex.EncodeToString(id), Name: *b.Name, Version: 1, Steps: b.Steps, Mine: true, CreatedAt: now, UpdatedAt: now}
	if b.Version != nil {
		s.Version = *b.Version
	}
	if err := a.sequences.save(append(list, s)); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	chat.WriteJSON(w, http.StatusCreated, s)
}

func (a *App) serveUpdateSequence(w http.ResponseWriter, r *http.Request) {
	b, ok := readSequenceBody(w, r)
	if !ok {
		return
	}
	a.sequences.mu.Lock()
	defer a.sequences.mu.Unlock()
	list, err := a.sequences.load()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := r.PathValue("id")
	for i := range list {
		if list[i].ID != id {
			continue
		}
		if b.Name != nil {
			if nameTaken(list, *b.Name, id) {
				jsonError(w, http.StatusConflict, "you already have a sequence with that name")
				return
			}
			list[i].Name = *b.Name
		}
		if b.Version != nil {
			list[i].Version = *b.Version
		}
		if b.Steps != nil {
			list[i].Steps = b.Steps
		}
		list[i].UpdatedAt = time.Now().UTC()
		if err := a.sequences.save(list); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		chat.WriteJSON(w, http.StatusOK, list[i])
		return
	}
	jsonError(w, http.StatusNotFound, "no such sequence")
}

func (a *App) serveDeleteSequence(w http.ResponseWriter, r *http.Request) {
	a.sequences.mu.Lock()
	defer a.sequences.mu.Unlock()
	list, err := a.sequences.load()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := r.PathValue("id")
	for i := range list {
		if list[i].ID == id {
			if err := a.sequences.save(append(list[:i], list[i+1:]...)); err != nil {
				jsonError(w, http.StatusInternalServerError, err.Error())
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	jsonError(w, http.StatusNotFound, "no such sequence")
}

// accountKey is the EmbeddedCI API key the desktop uses for embeddedci.com, if any and not offline.
func (a *App) accountKey() (string, bool) {
	if a.offline(a.store.Load()) {
		return "", false
	}
	if key, _ := a.keys.Get("embeddedci"); key != "" {
		return key, true
	}
	if v := strings.TrimSpace(a.keys.Env["BENCHPOD_API_KEY"]); v != "" {
		return v, true
	}
	return "", false
}

// apiBase is embeddedci.com, or BENCHPOD_API_BASE (as the SDK and the CLI).
func (a *App) apiBase() string {
	if v := strings.TrimRight(strings.TrimSpace(a.keys.Env["BENCHPOD_API_BASE"]), "/"); v != "" {
		return v
	}
	return "https://www.embeddedci.com"
}

// serveAccountSequences passes a sequences call to the signed-in account on embeddedci.com.
func (a *App) serveAccountSequences(w http.ResponseWriter, r *http.Request) {
	key, ok := a.accountKey()
	if !ok {
		jsonError(w, http.StatusUnauthorized, "not signed in to embeddedci.com: add an EmbeddedCI API key to share sequences")
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
	default:
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	target := a.apiBase() + "/api/benchpod/bus-sequences"
	if id := r.PathValue("id"); id != "" {
		target += "/" + url.PathEscape(id)
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSequenceBody))
	if err != nil {
		jsonError(w, http.StatusRequestEntityTooLarge, "the sequence is too large (256 KB at most)")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), accountAPITimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, target, bytes.NewReader(body))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Authorization", "ApiKey "+key)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.httpClient().Do(req)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "embeddedci.com did not answer: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 4<<20))
}

func (a *App) httpClient() *http.Client {
	if a.accountClient != nil {
		return a.accountClient
	}
	return http.DefaultClient
}
