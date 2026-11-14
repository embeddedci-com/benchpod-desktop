package local

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestMCPSpecIsExact(t *testing.T) {
	if !regexp.MustCompile(`^embeddedci-mcp==\d+\.\d+\.\d+$`).MatchString(MCPSpec) {
		t.Fatalf("MCPSpec %q is not one exact release (embeddedci-mcp==X.Y.Z)", MCPSpec)
	}
}

func TestUVArchivesCoverReleasePlatforms(t *testing.T) {
	hexRe := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, p := range []string{"darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64"} {
		a, ok := uvArchives[p]
		if !ok {
			t.Errorf("no uv archive for %s", p)
			continue
		}
		if !hexRe.MatchString(a.sha256) {
			t.Errorf("%s: bad SHA-256 %q", p, a.sha256)
		}
	}
}

func tarGz(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func zipOf(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

// serveUV points the installer at a local server holding archive for platform, with the given
// pinned hash, and restores the real table afterwards.
func serveUV(t *testing.T, platform, ext string, archive []byte, pinned string) {
	t.Helper()
	target := "test-target"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+uvVersion+"/uv-"+target+ext {
			http.NotFound(w, r)
			return
		}
		w.Write(archive)
	}))
	t.Cleanup(srv.Close)
	oldBase, oldArchives := uvReleaseBase, uvArchives
	uvReleaseBase = srv.URL
	uvArchives = map[string]uvArchive{platform: {target, pinned}}
	t.Cleanup(func() { uvReleaseBase, uvArchives = oldBase, oldArchives })
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestInstallUVUnpacksVerifiedTarGz(t *testing.T) {
	archive := tarGz(t, map[string]string{
		"uv-test-target/uv":       "uv-binary",
		"uv-test-target/uvx":      "uvx-binary",
		"uv-test-target/README":   "skipped",
		"../../escape/uvx-shadow": "skipped",
	})
	serveUV(t, "linux/amd64", ".tar.gz", archive, sum(archive))
	dir := filepath.Join(t.TempDir(), "uv")
	if err := installUVFor(context.Background(), dir, "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "uvx"))
	if err != nil || string(got) != "uvx-binary" {
		t.Fatalf("uvx = %q, %v", got, err)
	}
	if st, _ := os.Stat(filepath.Join(dir, "uvx")); st.Mode()&0o100 == 0 {
		t.Fatalf("uvx is not executable: %v", st.Mode())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("unpacked %d files, want uv and uvx only", len(entries))
	}
}

func TestInstallUVUnpacksVerifiedZip(t *testing.T) {
	archive := zipOf(t, map[string]string{"uv.exe": "uv", "uvx.exe": "uvx", "uvw.exe": "uvw"})
	serveUV(t, "windows/amd64", ".zip", archive, sum(archive))
	dir := filepath.Join(t.TempDir(), "uv")
	if err := installUVFor(context.Background(), dir, "windows/amd64"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "uvx.exe")); err != nil {
		t.Fatal(err)
	}
}

func TestInstallUVRefusesWrongHash(t *testing.T) {
	archive := tarGz(t, map[string]string{"uv-test-target/uvx": "tampered"})
	serveUV(t, "darwin/arm64", ".tar.gz", archive, strings.Repeat("0", 64))
	dir := filepath.Join(t.TempDir(), "uv")
	err := installUVFor(context.Background(), dir, "darwin/arm64")
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("err = %v, want a SHA-256 mismatch", err)
	}
	if _, serr := os.Stat(dir); !os.IsNotExist(serr) {
		t.Fatalf("%s exists after a refused install", dir)
	}
}

func TestInstallUVUnknownPlatform(t *testing.T) {
	if err := installUVFor(context.Background(), t.TempDir(), "plan9/386"); err == nil {
		t.Fatal("want an error for a platform with no pinned uv")
	}
}
