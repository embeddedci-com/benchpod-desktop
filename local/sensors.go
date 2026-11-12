package local

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/embeddedci-com/benchpod-desktop/chat"
	"github.com/embeddedci-com/benchpod-desktop/sensors"
)

// User-defined sensors on this computer: the user's definitions and stimuli as files in the data
// folder's sensors/ folder, laid out like the download zip, each file as the user wrote it:
//
//	sensors/sensors/<kind>/<name>.yaml (or .json)    a sensor definition
//	sensors/stimuli/<name>.yaml (or .json)           a stimulus
//
// The routes have the shape of the EmbeddedCI web app's (/api/benchpod/sensor-definitions and
// /api/benchpod/sensor-library), so the page's SensorStore is a thin layer over either. The id of
// a definition is its name (names are unique across kinds); a save that renames it answers the
// new id.
//
//	GET    /api/sensor-definitions                   ?kind= -> {definitions, kinds, full_validation}
//	POST   /api/sensor-definitions                   {"source"} -> 201 definition; 409 name_taken
//	POST   /api/sensor-definitions/upload            raw body or multipart "file"; ?replace=1
//	GET    /api/sensor-definitions/download          ?kind=, ?format=json|yaml -> zip
//	GET    /api/sensor-definitions/{id}              definition with source
//	PUT    /api/sensor-definitions/{id}              {"source"} -> definition (may rename)
//	DELETE /api/sensor-definitions/{id}
//	POST   /api/sensor-definitions/{id}/duplicate    optional {"name"} -> 201
//	GET    /api/sensor-definitions/{id}/download     ?format= -> the file
//	GET    /api/sensor-library                       ?kind=, ?version= -> listing
//	GET    /api/sensor-library/entries/{name}        ?version= -> entry with source
//	GET    /api/sensor-library/entries/{name}/download
//	GET    /api/sensor-library/settings              {override, pinned_version, embedded_version, repo, can_change}
//	PUT    /api/sensor-library/settings              {"version": "" | "latest" | "X.Y.Z"}
//	POST   /api/sensors/command                      {"command": {"cmd": "sensor_...", ...}} -> {"reply"}
//
// The files are checked here as the web app's server checks them (header, size, top-level keys);
// the page checks the rest with the pod's own engine (WebAssembly) before it saves.

const (
	sensorErrInvalid   = "invalid_definition"
	sensorErrNameTaken = "name_taken"
	sensorErrNotFound  = "not_found"
)

type sensorStore struct {
	mu  sync.Mutex
	dir string
}

// storedSensor is one definition as the API returns it; Source and Definition are left out of
// the list.
type storedSensor struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Kind         string          `json:"kind"`
	Label        string          `json:"label"`
	SourceFormat string          `json:"source_format"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
	Source       *string         `json:"source,omitempty"`
	Definition   json.RawMessage `json:"definition,omitempty"`

	path string
	def  *sensors.Definition
}

func sensorExt(format string) string {
	if format == sensors.SourceJSON {
		return "json"
	}
	return "yaml"
}

// sensorRel is where a definition goes, relative to the store (and in a zip).
func sensorRel(kind, name, format string) string {
	if kind == sensors.KindStimulus {
		return "stimuli/" + name + "." + sensorExt(format)
	}
	return "sensors/" + kind + "/" + name + "." + sensorExt(format)
}

// all reads every definition in the store. Files that do not parse are skipped.
func (s *sensorStore) all() ([]*storedSensor, error) {
	var out []*storedSensor
	for _, top := range []string{"sensors", "stimuli"} {
		root := filepath.Join(s.dir, top)
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			ext := strings.ToLower(filepath.Ext(p))
			if d.IsDir() || (ext != ".yaml" && ext != ".yml" && ext != ".json") {
				return nil
			}
			if d := s.read(p); d != nil {
				out = append(out, d)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *sensorStore) read(p string) *storedSensor {
	src, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	def, err := sensors.Parse(src)
	if err != nil {
		return nil
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil
	}
	at := st.ModTime().UTC().Format(time.RFC3339)
	text := string(src)
	return &storedSensor{ID: def.Name, Name: def.Name, Kind: def.Kind, Label: def.Label, SourceFormat: def.SourceFormat,
		CreatedAt: at, UpdatedAt: at, Source: &text, Definition: def.JSON, path: p, def: def}
}

func (s *sensorStore) find(id string) (*storedSensor, error) {
	if !sensors.NameRe.MatchString(id) {
		return nil, nil
	}
	all, err := s.all()
	if err != nil {
		return nil, err
	}
	for _, d := range all {
		if d.ID == id {
			return d, nil
		}
	}
	return nil, nil
}

// errNameTaken: another definition has the name.
type errNameTaken struct{ name string }

func (e errNameTaken) Error() string {
	return fmt.Sprintf("a definition named %q already exists; rename it in the file (name:)", e.name)
}

var errSensorNotFound = errors.New("definition not found")

// save writes def as a new definition (id "") or over the definition id. It refuses a name
// another definition has.
func (s *sensorStore) save(def *sensors.Definition, id string) (*storedSensor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var old *storedSensor
	if id != "" {
		var err error
		if old, err = s.find(id); err != nil {
			return nil, err
		} else if old == nil {
			return nil, errSensorNotFound
		}
	}
	if def.Name != id {
		taken, err := s.find(def.Name)
		if err != nil {
			return nil, err
		}
		if taken != nil {
			return nil, errNameTaken{def.Name}
		}
	}
	p := filepath.Join(s.dir, filepath.FromSlash(sensorRel(def.Kind, def.Name, def.SourceFormat)))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, def.Source, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, p); err != nil {
		return nil, err
	}
	if old != nil && old.path != p {
		_ = os.Remove(old.path)
	}
	d := s.read(p)
	if d == nil {
		return nil, errors.New("the saved file does not read back")
	}
	if old != nil {
		d.CreatedAt = old.CreatedAt
	}
	return d, nil
}

func (s *sensorStore) remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.find(id)
	if err != nil {
		return err
	}
	if d == nil {
		return errSensorNotFound
	}
	return os.Remove(d.path)
}

// sensorFile renders a definition for download: the source as written, or JSON or YAML.
func sensorFile(def *sensors.Definition, format string) ([]byte, string, error) {
	switch format {
	case "", def.SourceFormat:
		return def.Source, sensorExt(def.SourceFormat), nil
	case sensors.SourceJSON:
		return sensors.ToJSON(def), "json", nil
	default:
		b, err := sensors.ToYAML(def)
		return b, "yaml", err
	}
}

// ---- HTTP ----

func (a *App) mountSensors(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sensor-definitions", a.serveSensorList)
	mux.HandleFunc("POST /api/sensor-definitions", a.serveSensorCreate)
	mux.HandleFunc("POST /api/sensor-definitions/upload", a.serveSensorUpload)
	mux.HandleFunc("GET /api/sensor-definitions/download", a.serveSensorZip)
	mux.HandleFunc("GET /api/sensor-definitions/{id}", a.serveSensorGet)
	mux.HandleFunc("PUT /api/sensor-definitions/{id}", a.serveSensorUpdate)
	mux.HandleFunc("DELETE /api/sensor-definitions/{id}", a.serveSensorDelete)
	mux.HandleFunc("POST /api/sensor-definitions/{id}/duplicate", a.serveSensorDuplicate)
	mux.HandleFunc("GET /api/sensor-definitions/{id}/download", a.serveSensorDownload)
	mux.HandleFunc("GET /api/sensor-library", a.serveSensorLibrary)
	mux.HandleFunc("GET /api/sensor-library/entries/{name}", a.serveSensorLibraryEntry)
	mux.HandleFunc("GET /api/sensor-library/entries/{name}/download", a.serveSensorLibraryDownload)
	mux.HandleFunc("GET /api/sensor-library/settings", a.serveSensorLibrarySettings)
	mux.HandleFunc("PUT /api/sensor-library/settings", a.serveSetSensorLibrary)
	mux.HandleFunc("POST /api/sensors/command", a.serveSensorCommand)
}

func sensorError(w http.ResponseWriter, status int, code, msg string) {
	body := map[string]string{"error": msg}
	if code != "" {
		body["code"] = code
	}
	chat.WriteJSON(w, status, body)
}

// writeSensorErr answers a store error with its status and code.
func writeSensorErr(w http.ResponseWriter, err error) {
	var taken errNameTaken
	var bad *sensors.Error
	switch {
	case errors.As(err, &taken):
		sensorError(w, http.StatusConflict, sensorErrNameTaken, err.Error())
	case errors.Is(err, errSensorNotFound):
		sensorError(w, http.StatusNotFound, sensorErrNotFound, err.Error())
	case errors.As(err, &bad):
		sensorError(w, http.StatusBadRequest, sensorErrInvalid, err.Error())
	default:
		sensorError(w, http.StatusInternalServerError, "", err.Error())
	}
}

// readSensorBody reads {"source": "..."} or {"definition": {...}}.
func readSensorBody(w http.ResponseWriter, r *http.Request) (*sensors.Definition, error) {
	var body struct {
		Source     *string         `json:"source"`
		Definition json.RawMessage `json:"definition"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*sensors.MaxSourceBytes+4096)).Decode(&body); err != nil {
		return nil, &sensors.Error{Msg: `send {"source": "<yaml or json text>"}`}
	}
	var src []byte
	switch {
	case body.Source != nil:
		src = []byte(*body.Source)
	case len(body.Definition) > 0:
		var buf bytes.Buffer
		if err := json.Indent(&buf, body.Definition, "", "  "); err != nil {
			return nil, &sensors.Error{Msg: "definition: " + err.Error()}
		}
		src = append(buf.Bytes(), '\n')
	default:
		return nil, &sensors.Error{Msg: `send {"source": "<yaml or json text>"}`}
	}
	return sensors.Parse(src)
}

func summary(d *storedSensor) *storedSensor {
	c := *d
	c.Source, c.Definition = nil, nil
	return &c
}

func (a *App) serveSensorList(w http.ResponseWriter, r *http.Request) {
	all, err := a.sensorDefs.all()
	if err != nil {
		writeSensorErr(w, err)
		return
	}
	kind := r.URL.Query().Get("kind")
	out := []*storedSensor{}
	for _, d := range all {
		if kind == "" || d.Kind == kind {
			out = append(out, summary(d))
		}
	}
	chat.WriteJSON(w, http.StatusOK, map[string]any{"definitions": out, "kinds": sensors.Kinds, "full_validation": false})
}

func (a *App) serveSensorGet(w http.ResponseWriter, r *http.Request) {
	d, err := a.sensorDefs.find(r.PathValue("id"))
	if err == nil && d == nil {
		err = errSensorNotFound
	}
	if err != nil {
		writeSensorErr(w, err)
		return
	}
	chat.WriteJSON(w, http.StatusOK, d)
}

func (a *App) serveSensorCreate(w http.ResponseWriter, r *http.Request) {
	def, err := readSensorBody(w, r)
	if err == nil {
		var d *storedSensor
		if d, err = a.sensorDefs.save(def, ""); err == nil {
			chat.WriteJSON(w, http.StatusCreated, d)
			return
		}
	}
	writeSensorErr(w, err)
}

func (a *App) serveSensorUpdate(w http.ResponseWriter, r *http.Request) {
	def, err := readSensorBody(w, r)
	if err == nil {
		var d *storedSensor
		if d, err = a.sensorDefs.save(def, r.PathValue("id")); err == nil {
			chat.WriteJSON(w, http.StatusOK, d)
			return
		}
	}
	writeSensorErr(w, err)
}

func (a *App) serveSensorDelete(w http.ResponseWriter, r *http.Request) {
	if err := a.sensorDefs.remove(r.PathValue("id")); err != nil {
		writeSensorErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) serveSensorDuplicate(w http.ResponseWriter, r *http.Request) {
	d, err := a.sensorDefs.find(r.PathValue("id"))
	if err == nil && d == nil {
		err = errSensorNotFound
	}
	if err != nil {
		writeSensorErr(w, err)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if raw, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096)); len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			sensorError(w, http.StatusBadRequest, "", "invalid JSON body")
			return
		}
	}
	names := []string{strings.TrimSpace(body.Name)}
	if names[0] == "" {
		names = copyNames(d.Name)
	}
	for _, name := range names {
		text, err := sensors.Rename(d.def, name)
		if err != nil {
			writeSensorErr(w, err)
			return
		}
		def, err := sensors.Parse(text)
		if err != nil {
			writeSensorErr(w, err)
			return
		}
		created, err := a.sensorDefs.save(def, "")
		var taken errNameTaken
		if errors.As(err, &taken) && len(names) > 1 {
			continue
		}
		if err != nil {
			writeSensorErr(w, err)
			return
		}
		chat.WriteJSON(w, http.StatusCreated, created)
		return
	}
	sensorError(w, http.StatusConflict, sensorErrNameTaken, `no free name for the copy; pass one in the body: {"name": "..."}`)
}

// copyNames lists the default names of a copy: base_copy, base_copy2 ... base_copy20, each within
// 32 characters.
func copyNames(base string) []string {
	var out []string
	for i := 1; i <= 20; i++ {
		suffix := "_copy"
		if i > 1 {
			suffix += fmt.Sprint(i)
		}
		b := base
		if len(b)+len(suffix) > 32 {
			b = b[:32-len(suffix)]
		}
		out = append(out, b+suffix)
	}
	return out
}

func (a *App) serveSensorUpload(w http.ResponseWriter, r *http.Request) {
	text, err := readUploadedFile(w, r)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			sensorError(w, http.StatusRequestEntityTooLarge, sensorErrInvalid, fmt.Sprintf("a definition may be at most %d bytes", sensors.MaxSourceBytes))
			return
		}
		sensorError(w, http.StatusBadRequest, "", err.Error())
		return
	}
	def, err := sensors.Parse(text)
	if err != nil {
		writeSensorErr(w, err)
		return
	}
	d, err := a.sensorDefs.save(def, "")
	var taken errNameTaken
	if errors.As(err, &taken) {
		if !isTruthy(r.URL.Query().Get("replace")) {
			sensorError(w, http.StatusConflict, sensorErrNameTaken, err.Error()+" or upload again with replace to overwrite it")
			return
		}
		if d, err = a.sensorDefs.save(def, def.Name); err == nil {
			chat.WriteJSON(w, http.StatusOK, map[string]any{"replaced": true, "definition": d})
			return
		}
	}
	if err != nil {
		writeSensorErr(w, err)
		return
	}
	chat.WriteJSON(w, http.StatusCreated, map[string]any{"replaced": false, "definition": d})
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// readUploadedFile returns the uploaded file: a multipart part ("file", or the first), or the
// raw body.
func readUploadedFile(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	const max = sensors.MaxSourceBytes + 16<<10
	body := http.MaxBytesReader(w, r.Body, max)
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "multipart/form-data" {
		return io.ReadAll(body)
	}
	r.Body = body
	if err := r.ParseMultipartForm(max); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, err
		}
		return nil, errors.New("invalid multipart body")
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		for _, fh := range r.MultipartForm.File {
			files = fh
			break
		}
	}
	if len(files) != 1 {
		return nil, errors.New("upload one file (multipart field \"file\")")
	}
	f, err := files[0].Open()
	if err != nil {
		return nil, errors.New("cannot read the uploaded file")
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max))
}

func downloadFormat(w http.ResponseWriter, r *http.Request) (string, bool) {
	f := strings.ToLower(r.URL.Query().Get("format"))
	if f == "yml" {
		f = sensors.SourceYAML
	}
	if f != "" && f != sensors.SourceYAML && f != sensors.SourceJSON {
		sensorError(w, http.StatusBadRequest, "", "format is json or yaml")
		return "", false
	}
	return f, true
}

func sendFile(w http.ResponseWriter, name string, data []byte, ext string) {
	ct := "application/yaml"
	if ext == "json" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.%s"`, name, ext))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (a *App) serveSensorDownload(w http.ResponseWriter, r *http.Request) {
	format, ok := downloadFormat(w, r)
	if !ok {
		return
	}
	d, err := a.sensorDefs.find(r.PathValue("id"))
	if err == nil && d == nil {
		err = errSensorNotFound
	}
	if err != nil {
		writeSensorErr(w, err)
		return
	}
	data, ext, err := sensorFile(d.def, format)
	if err != nil {
		writeSensorErr(w, err)
		return
	}
	sendFile(w, d.Name, data, ext)
}

func (a *App) serveSensorZip(w http.ResponseWriter, r *http.Request) {
	format, ok := downloadFormat(w, r)
	if !ok {
		return
	}
	all, err := a.sensorDefs.all()
	if err != nil {
		writeSensorErr(w, err)
		return
	}
	kind := r.URL.Query().Get("kind")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, d := range all {
		if kind != "" && d.Kind != kind {
			continue
		}
		data, ext, err := sensorFile(d.def, format)
		if err != nil {
			writeSensorErr(w, err)
			return
		}
		f, err := zw.Create(sensorRel(d.Kind, d.Name, map[string]string{"json": sensors.SourceJSON, "yaml": sensors.SourceYAML}[ext]))
		if err != nil {
			writeSensorErr(w, err)
			return
		}
		_, _ = f.Write(data)
	}
	if err := zw.Close(); err != nil {
		writeSensorErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="sensor-definitions.zip"`)
	_, _ = w.Write(buf.Bytes())
}

// ---- the library ----

// sensorLibrary resolves the library version asked for: ?version=, else the setting.
func (a *App) sensorLibrary(r *http.Request) (*sensors.Library, sensors.Resolved) {
	v := r.URL.Query().Get("version")
	if v == "" {
		v = a.store.Load().SensorLibrary
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	return a.sensorLib.Resolve(ctx, v)
}

func (a *App) serveSensorLibrary(w http.ResponseWriter, r *http.Request) {
	lib, res := a.sensorLibrary(r)
	kind := r.URL.Query().Get("kind")
	entries := []sensors.Entry{}
	for _, e := range lib.Entries {
		if kind == "" || e.Kind == kind {
			entries = append(entries, e)
		}
	}
	chat.WriteJSON(w, http.StatusOK, map[string]any{
		"requested": res.Requested, "version": res.Version, "origin": res.Origin,
		"pinned_version": a.sensorLib.PinnedVersion(), "embedded_version": a.sensorLib.EmbeddedVersion(),
		"warnings": res.Warnings, "entries": entries,
	})
}

func (a *App) libraryEntry(w http.ResponseWriter, r *http.Request) (sensors.Entry, *sensors.Definition, sensors.Resolved, bool) {
	lib, res := a.sensorLibrary(r)
	e, src, ok := lib.Entry(r.PathValue("name"))
	if !ok {
		sensorError(w, http.StatusNotFound, sensorErrNotFound, "no such library entry in version "+res.Version)
		return e, nil, res, false
	}
	def, err := sensors.Parse(src)
	if err != nil {
		writeSensorErr(w, err)
		return e, nil, res, false
	}
	return e, def, res, true
}

func (a *App) serveSensorLibraryEntry(w http.ResponseWriter, r *http.Request) {
	e, def, res, ok := a.libraryEntry(w, r)
	if !ok {
		return
	}
	chat.WriteJSON(w, http.StatusOK, map[string]any{
		"name": e.Name, "kind": e.Kind, "label": e.Label, "path": e.Path,
		"version": res.Version, "origin": res.Origin, "warnings": res.Warnings,
		"source_format": def.SourceFormat, "source": string(def.Source), "definition": json.RawMessage(def.JSON),
	})
}

func (a *App) serveSensorLibraryDownload(w http.ResponseWriter, r *http.Request) {
	format, ok := downloadFormat(w, r)
	if !ok {
		return
	}
	e, def, _, ok := a.libraryEntry(w, r)
	if !ok {
		return
	}
	data, ext, err := sensorFile(def, format)
	if err != nil {
		writeSensorErr(w, err)
		return
	}
	sendFile(w, e.Name, data, ext)
}

func (a *App) librarySettings(warnings []string) map[string]any {
	if warnings == nil {
		warnings = []string{}
	}
	return map[string]any{
		"override": a.store.Load().SensorLibrary, "pinned_version": a.sensorLib.PinnedVersion(),
		"embedded_version": a.sensorLib.EmbeddedVersion(), "repo": a.sensorLib.Repo(),
		"can_change": true, "warnings": warnings,
	}
}

func (a *App) serveSensorLibrarySettings(w http.ResponseWriter, r *http.Request) {
	chat.WriteJSON(w, http.StatusOK, a.librarySettings(nil))
}

// serveSetSensorLibrary keeps the library version in the settings. A version GitHub says does not
// exist is refused; one that cannot be checked (offline, no network) is kept with a warning, and
// the library falls back until it can be fetched.
func (a *App) serveSetSensorLibrary(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		sensorError(w, http.StatusBadRequest, "", `send {"version": "" | "latest" | "X.Y.Z"}`)
		return
	}
	v, ok := sensors.NormalizeVersion(body.Version)
	if !ok {
		sensorError(w, http.StatusBadRequest, "invalid", `the library version is "" (the default), latest or X.Y.Z`)
		return
	}
	var warnings []string
	if v != "" && v != a.sensorLib.EmbeddedVersion() {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		err := a.sensorLib.CheckVersion(ctx, v)
		cancel()
		switch {
		case errors.Is(err, sensors.ErrNoSuchVersion):
			sensorError(w, http.StatusBadRequest, "invalid", err.Error())
			return
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("could not check sensor library %s (%v); it is used once it can be downloaded", v, err))
		}
	}
	st := a.store.Load()
	st.SensorLibrary = v
	if err := a.store.Save(st); err != nil {
		sensorError(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	chat.WriteJSON(w, http.StatusOK, a.librarySettings(warnings))
}

// ---- the pod ----

// serveSensorCommand sends one sensor command to the pod (the MCP server's raw command tool), for
// the page's SensorPod. Only sensor_* commands pass: anything else goes through the tools and the
// policy. Starting an emulated sensor is what the user's Start click asks for, so it does not ask
// again; every command goes to the action log (a chunk upload without its data).
func (a *App) serveSensorCommand(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Command map[string]any `json:"command"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil || body.Command == nil {
		sensorError(w, http.StatusBadRequest, "", `send {"command": {"cmd": "sensor_...", ...}}`)
		return
	}
	cmd, _ := body.Command["cmd"].(string)
	if !strings.HasPrefix(cmd, "sensor_") || strings.ContainsAny(cmd, " \t\n") {
		sensorError(w, http.StatusBadRequest, "", "only sensor_* commands go through here")
		return
	}
	if !a.policy(a.store.Load()).Allows("command") {
		sensorError(w, http.StatusForbidden, "", "pod commands are not available here")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	t, err := a.connectPod(ctx, false)
	if err != nil {
		sensorError(w, http.StatusServiceUnavailable, "", err.Error())
		return
	}
	args := map[string]any{"request": body.Command}
	t0 := time.Now()
	res, err := t.Call(ctx, "command", args)
	a.logAction("manual", cmd, sensorLogArgs(body.Command), res, err, time.Since(t0))
	switch {
	case err != nil:
		sensorError(w, http.StatusBadGateway, "", "tool error: "+err.Error())
	case res.IsError:
		sensorError(w, http.StatusBadGateway, "", strings.TrimSpace(res.Text))
	default:
		var out struct {
			Reply json.RawMessage `json:"reply"`
		}
		_ = json.Unmarshal(res.Structured, &out)
		if len(out.Reply) == 0 || string(out.Reply) == "null" {
			out.Reply = json.RawMessage("{}")
		}
		chat.WriteJSON(w, http.StatusOK, out)
	}
}

// sensorLogArgs is a command for the action log: an upload chunk's data as its length.
func sensorLogArgs(c map[string]any) map[string]any {
	out := make(map[string]any, len(c))
	for k, v := range c {
		if s, ok := v.(string); ok && k == "data_b64" {
			out[k] = fmt.Sprintf("[%d base64 chars]", len(s))
			continue
		}
		out[k] = v
	}
	return out
}
