package local

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/embeddedci-com/benchpod-desktop/chat"
)

// Saving a file for the page. The desktop window has no Tauri APIs and the system web views do
// not all handle a download link, so the page hands a file it wants to give the user (a sensor
// definition, a zip of them) to benchpod-local, which writes it into the Downloads folder:
//
//	POST /api/files/save?name=<file name>   body: the bytes -> {"path"}
//
// An existing file is never overwritten: "name (2).yaml", "name (3).yaml", ...

const maxSavedFile = 16 << 20

// downloadsDir is the user's Downloads folder (created if missing), else the home folder.
func downloadsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return home, nil
	}
	return d, nil
}

// safeFileName keeps a plain file name: no folders, no hidden or empty names.
func safeFileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\:`) || strings.HasPrefix(name, ".") || len(name) > 200 {
		return "", errors.New("bad file name")
	}
	return name, nil
}

// saveUnique writes data as dir/name, or name (2), (3), ... when that exists.
func saveUnique(dir, name string, data []byte) (string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; i <= 1000; i++ {
		p := filepath.Join(dir, name)
		if i > 1 {
			p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.Write(data)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = os.Remove(p)
			return "", werr
		}
		return p, nil
	}
	return "", errors.New("no free file name")
}

func (a *App) serveSaveFile(w http.ResponseWriter, r *http.Request) {
	name, err := safeFileName(r.URL.Query().Get("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSavedFile))
	if err != nil {
		http.Error(w, "the file is too large", http.StatusRequestEntityTooLarge)
		return
	}
	dir := a.saveDir
	if dir == "" {
		if dir, err = downloadsDir(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	p, err := saveUnique(dir, name, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	chat.WriteJSON(w, http.StatusOK, map[string]string{"path": p})
}
