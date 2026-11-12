package local

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/embeddedci-com/benchpod-desktop/chat"
)

// Firmware files to flash from the bench controls. The page cannot hand a path on this computer
// to the flash tool, so it uploads the file here first: it lands in the data folder's firmware/
// folder and flash takes that path. The newest few are kept to flash again.
//
//	POST /api/firmware?name=<file name>   body: the file   -> {name, path, size, modified}
//	GET  /api/firmware                                     -> {"files": [...]} newest first

// firmwareMaxBytes caps one upload; firmwareKeep is how many files stay.
const (
	firmwareMaxBytes = 64 << 20
	firmwareKeep     = 10
)

type firmwareFile struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

func (a *App) firmwareDir() string { return filepath.Join(a.store.Dir(), "firmware") }

// firmwareName keeps a file's base name, with nothing that could leave the folder.
func firmwareName(name string) (string, error) {
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), `\`, "/"))
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return "", errors.New("give the file's name: ?name=app.elf")
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".elf", ".hex", ".bin", ".axf", ".out", ".srec", ".s19":
	default:
		return "", errors.New("flash takes an .elf, .hex or .bin file (also .axf, .out, .srec)")
	}
	return name, nil
}

func (a *App) listFirmware() []firmwareFile {
	entries, _ := os.ReadDir(a.firmwareDir())
	out := []firmwareFile{}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, firmwareFile{Name: e.Name(), Path: filepath.Join(a.firmwareDir(), e.Name()), Size: info.Size(), Modified: info.ModTime().UTC()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out
}

func (a *App) serveFirmwareList(w http.ResponseWriter, r *http.Request) {
	chat.WriteJSON(w, http.StatusOK, map[string]any{"files": a.listFirmware()})
}

func (a *App) serveFirmwareUpload(w http.ResponseWriter, r *http.Request) {
	name, err := firmwareName(r.URL.Query().Get("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(a.firmwareDir(), 0o700); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmp, err := os.CreateTemp(a.firmwareDir(), ".upload-*")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, http.MaxBytesReader(w, r.Body, firmwareMaxBytes))
	_ = tmp.Close()
	if err != nil {
		http.Error(w, "upload failed (64 MB at most): "+err.Error(), http.StatusBadRequest)
		return
	}
	if n == 0 {
		http.Error(w, "the file is empty", http.StatusBadRequest)
		return
	}
	dst := filepath.Join(a.firmwareDir(), name)
	if err := os.Rename(tmp.Name(), dst); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now()
	_ = os.Chtimes(dst, now, now)
	files := a.listFirmware()
	for _, f := range files[min(len(files), firmwareKeep):] {
		_ = os.Remove(f.Path)
	}
	chat.WriteJSON(w, http.StatusOK, firmwareFile{Name: name, Path: dst, Size: n, Modified: now.UTC()})
}
