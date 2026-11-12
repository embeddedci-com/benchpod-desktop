package sensors

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const fakeSensor = `format: 1
kind: analog
name: ntc10k
label: "NTC 10k divider"
output: voltage
value: "1.65"
`

// tarball builds a GitHub-style source tarball: everything under benchpod-sensors-<v>/.
func tarball(t *testing.T, v string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "benchpod-sensors-" + v + "/", Typeflag: tar.TypeDir, Mode: 0o755})
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: "benchpod-sensors-" + v + "/" + name, Mode: 0o644,
			Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func release(t *testing.T, v string) []byte {
	idx := `{"format":1,"version":"` + v + `","entries":[` +
		`{"path":"sensors/analog/ntc10k.yaml","name":"ntc10k","kind":"analog","label":"NTC 10k divider"},` +
		`{"path":"sensors/analog/missing.yaml","name":"missing","kind":"analog","label":"x"}]}`
	return tarball(t, v, map[string]string{
		"library.json":               idx,
		"sensors/analog/ntc10k.yaml": fakeSensor,
		"README.md":                  "not a definition",
	})
}

type fakeGitHub struct {
	srv      *httptest.Server
	hits     atomic.Int32
	latest   string
	releases map[string][]byte
	delay    time.Duration
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{releases: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if f.delay > 0 {
			time.Sleep(f.delay)
		}
		if r.URL.Path == "/repos/embeddedci-com/benchpod-sensors/releases/latest" {
			if f.latest == "" {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "v" + f.latest})
			return
		}
		const prefix = "/embeddedci-com/benchpod-sensors/archive/refs/tags/v"
		if v, ok := strings.CutPrefix(r.URL.Path, prefix); ok {
			v = strings.TrimSuffix(v, ".tar.gz")
			if body, ok := f.releases[v]; ok {
				_, _ = w.Write(body)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) config(t *testing.T) Config {
	return Config{
		CacheDir:   t.TempDir(),
		ArchiveURL: f.srv.URL + "/%s/archive/refs/tags/v%s.tar.gz",
		LatestURL:  f.srv.URL + "/repos/%s/releases/latest",
		Timeout:    2 * time.Second,
	}
}

func TestEmbeddedCopyMatchesItsIndex(t *testing.T) {
	lib, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.Warnings) != 0 {
		t.Fatalf("embedded copy has warnings: %v", lib.Warnings)
	}
	sub, _ := fs.Sub(embeddedFS, "embedded")
	idx, _, err := BuildIndex(sub, lib.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(idx.Entries, lib.Entries) {
		t.Fatalf("library.json is stale; run go run ./sensors/gen\nindex: %+v\nfiles: %+v", lib.Entries, idx.Entries)
	}
	if _, src, ok := lib.Entry("bme280"); !ok || !bytes.Contains(src, []byte("name: bme280")) {
		t.Fatal("bme280 missing from the embedded copy")
	}
}

func TestResolvePinnedIsEmbeddedWithoutNetwork(t *testing.T) {
	gh := newFakeGitHub(t)
	s, err := New(gh.config(t))
	if err != nil {
		t.Fatal(err)
	}
	lib, res := s.Resolve(context.Background(), "")
	if res.Origin != OriginEmbedded || lib.Version != s.EmbeddedVersion() || len(res.Warnings) != 0 {
		t.Fatalf("res = %+v", res)
	}
	if gh.hits.Load() != 0 {
		t.Fatal("the pinned embedded version must not fetch")
	}
}

func TestResolveFetchesAndCaches(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.releases["0.2.0"] = release(t, "0.2.0")
	cfg := gh.config(t)
	s, _ := New(cfg)

	lib, res := s.Resolve(context.Background(), "v0.2.0")
	if res.Origin != OriginGitHub || res.Version != "0.2.0" {
		t.Fatalf("res = %+v", res)
	}
	if len(lib.Entries) != 1 || lib.Entries[0].Name != "ntc10k" {
		t.Fatalf("entries = %+v", lib.Entries)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "missing.yaml: file missing") {
		t.Fatalf("warnings = %v", res.Warnings)
	}
	_, res = s.Resolve(context.Background(), "0.2.0")
	if res.Origin != OriginCache || gh.hits.Load() != 1 {
		t.Fatalf("second resolve: %+v, %d hits", res, gh.hits.Load())
	}

	// A new process with the same cache directory, offline, serves it from disk.
	cfg.Offline = func() bool { return true }
	s2, _ := New(cfg)
	lib, res = s2.Resolve(context.Background(), "0.2.0")
	if res.Origin != OriginCache || lib.Version != "0.2.0" || gh.hits.Load() != 1 {
		t.Fatalf("disk cache: %+v, %d hits", res, gh.hits.Load())
	}
}

func TestResolveFallsBack(t *testing.T) {
	gh := newFakeGitHub(t)
	s, _ := New(gh.config(t))

	// No such tag: the embedded copy, with a warning; the failure is remembered.
	lib, res := s.Resolve(context.Background(), "9.9.9")
	if res.Origin != OriginEmbedded || lib.Version != s.EmbeddedVersion() || len(res.Warnings) < 2 ||
		!strings.Contains(res.Warnings[0], "no such sensor library release") {
		t.Fatalf("missing tag: %+v", res)
	}
	hits := gh.hits.Load()
	s.Resolve(context.Background(), "9.9.9")
	if gh.hits.Load() != hits {
		t.Fatal("a failed version must not be fetched again right away")
	}
	if err := s.CheckVersion(context.Background(), "9.9.9"); !errors.Is(err, ErrNoSuchVersion) {
		t.Fatalf("CheckVersion = %v", err)
	}

	// No release yet: latest falls back to the pinned version.
	_, res = s.Resolve(context.Background(), Latest)
	if res.Origin != OriginEmbedded || !strings.Contains(strings.Join(res.Warnings, " "), "latest") {
		t.Fatalf("latest without releases: %+v", res)
	}

	// A bad value.
	_, res = s.Resolve(context.Background(), "banana")
	if res.Origin != OriginEmbedded || len(res.Warnings) == 0 {
		t.Fatalf("bad value: %+v", res)
	}
}

func TestResolveTimeout(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.releases["0.3.0"] = release(t, "0.3.0")
	gh.delay = 300 * time.Millisecond
	cfg := gh.config(t)
	cfg.Timeout = 50 * time.Millisecond
	s, _ := New(cfg)
	_, res := s.Resolve(context.Background(), "0.3.0")
	if res.Origin != OriginEmbedded || len(res.Warnings) == 0 {
		t.Fatalf("timeout: %+v", res)
	}
}

func TestResolveLatest(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.latest = "0.4.0"
	gh.releases["0.4.0"] = release(t, "0.4.0")
	s, _ := New(gh.config(t))
	_, res := s.Resolve(context.Background(), Latest)
	if res.Version != "0.4.0" || res.Origin != OriginGitHub || res.Requested != Latest {
		t.Fatalf("latest: %+v", res)
	}
	if err := s.CheckVersion(context.Background(), Latest); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseVersionMismatch(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.releases["0.5.0"] = release(t, "0.6.0") // library.json says another version
	s, _ := New(gh.config(t))
	_, res := s.Resolve(context.Background(), "0.5.0")
	if res.Origin != OriginEmbedded || !strings.Contains(res.Warnings[0], "the tag is 0.5.0") {
		t.Fatalf("mismatch: %+v", res)
	}
}

func TestPinnedVersionFromConfig(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.releases["0.7.0"] = release(t, "0.7.0")
	cfg := gh.config(t)
	cfg.PinnedVersion = "v0.7.0"
	s, _ := New(cfg)
	if s.PinnedVersion() != "0.7.0" {
		t.Fatalf("pinned = %q", s.PinnedVersion())
	}
	// An override that does not exist falls back to the pinned version, not straight to embedded.
	_, res := s.Resolve(context.Background(), "8.8.8")
	if res.Version != "0.7.0" || res.Origin != OriginGitHub {
		t.Fatalf("fallback to pinned: %+v", res)
	}
}

func TestOfflineIsNotRememberedAsAFailure(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.releases["0.2.0"] = release(t, "0.2.0")
	off := true
	cfg := gh.config(t)
	cfg.Offline = func() bool { return off }
	s, _ := New(cfg)
	_, res := s.Resolve(context.Background(), "0.2.0")
	if res.Origin != OriginEmbedded || gh.hits.Load() != 0 {
		t.Fatalf("offline: %+v, %d hits", res, gh.hits.Load())
	}
	off = false
	_, res = s.Resolve(context.Background(), "0.2.0")
	if res.Origin != OriginGitHub || res.Version != "0.2.0" {
		t.Fatalf("back online: %+v", res)
	}
}

func TestLatestOfflineUsesNewestCached(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.latest = "0.10.0"
	gh.releases["0.9.0"] = release(t, "0.9.0")
	gh.releases["0.10.0"] = release(t, "0.10.0")
	cfg := gh.config(t)
	s, _ := New(cfg)
	s.Resolve(context.Background(), "0.9.0")
	s.Resolve(context.Background(), Latest)
	cfg.Offline = func() bool { return true }
	s2, _ := New(cfg)
	_, res := s2.Resolve(context.Background(), Latest)
	if res.Version != "0.10.0" || res.Origin != OriginCache || len(res.Warnings) == 0 {
		t.Fatalf("latest offline: %+v", res)
	}
}

// The Python SDK writes the same layout (<cache>/<version>/library.json and the files): a version
// it downloaded serves here without a fetch.
func TestReadsTheSDKCache(t *testing.T) {
	gh := newFakeGitHub(t)
	cfg := gh.config(t)
	dir := filepath.Join(cfg.CacheDir, "0.2.0", "sensors", "analog")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "ntc10k.yaml"), []byte(fakeSensor), 0o644)
	idx := `{"format": 1, "version": "0.2.0", "entries": [{"path": "sensors/analog/ntc10k.yaml", "name": "ntc10k", "kind": "analog", "label": "NTC 10k divider"}]}`
	_ = os.WriteFile(filepath.Join(cfg.CacheDir, "0.2.0", IndexFile), []byte(idx), 0o644)
	s, _ := New(cfg)
	lib, res := s.Resolve(context.Background(), "0.2.0")
	if res.Origin != OriginCache || len(lib.Entries) != 1 || gh.hits.Load() != 0 {
		t.Fatalf("sdk cache: %+v %+v, %d hits", res, lib.Entries, gh.hits.Load())
	}
}

func TestDefaultCacheDirIsShared(t *testing.T) {
	t.Setenv(CacheEnv, "")
	base, err := os.UserCacheDir()
	if err != nil {
		t.Skip(err)
	}
	if got := DefaultCacheDir(); got != filepath.Join(base, "embeddedci", "benchpod-sensors") {
		t.Fatalf("cache dir = %s", got)
	}
	t.Setenv(CacheEnv, "/x/y")
	if DefaultCacheDir() != "/x/y" {
		t.Fatal(CacheEnv + " must move the cache")
	}
}
