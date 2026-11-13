// The public BenchPod sensor library (github.com/embeddedci-com/benchpod-sensors), for the
// Sensors view.
//
// Contract with the library repository: every release is a tag vX.Y.Z, and the tag's source
// tarball (https://github.com/<repo>/archive/refs/tags/vX.Y.Z.tar.gz) has library.json at its
// root:
//
//	{"format":1,"version":"X.Y.Z","entries":[{"path":"sensors/i2c/bme280.yaml","name":"bme280",
//	  "kind":"i2c","label":"..."}]}
//
// The app pins a version and carries a copy of it (sensors/embedded, refreshed with
// go run ./sensors/gen), so the library works offline. The user may pick "latest" or another
// release in Settings. A version other than the embedded one is fetched on demand and cached on
// disk per version, in the folder the BenchPod Python SDK and CLI use
// (<user cache>/embeddedci/benchpod-sensors/<version>). When the fetch fails (offline, no such
// tag, timeout, broken archive) the service falls back to the cache, the pinned version and then
// the embedded copy, with a warning the response carries.
package sensors

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed all:embedded
var embeddedFS embed.FS

// IndexFile is the library index at the root of a release.
const IndexFile = "library.json"

// Latest is the version value that follows the newest release.
const Latest = "latest"

// Where a library came from.
const (
	OriginEmbedded = "embedded" // baked into this build
	OriginCache    = "cache"    // fetched earlier, from the disk or memory cache
	OriginGitHub   = "github"   // fetched from GitHub for this request
)

// Limits on a fetched release.
const (
	maxArchiveBytes = 20 << 20 // decompressed bytes read from a release tarball
	maxIndexBytes   = 1 << 20
	maxEntries      = 2000
)

// ErrNoSuchVersion is returned when the library repository has no such release tag.
var ErrNoSuchVersion = errors.New("no such sensor library release")

var versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// NormalizeVersion returns v without a leading "v" and reports whether it is "latest" or
// X.Y.Z. "" is valid (the pinned version).
func NormalizeVersion(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" || v == Latest {
		return v, true
	}
	v = strings.TrimPrefix(v, "v")
	return v, versionRe.MatchString(v)
}

// Entry is one definition in the library index.
type Entry struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// Index is library.json.
type Index struct {
	Format  int     `json:"format"`
	Version string  `json:"version"`
	Entries []Entry `json:"entries"`
}

// Library is one version of the library: the index and each entry's source text.
type Library struct {
	Index
	Files map[string][]byte // entry path -> source
	// Warnings are entries left out because they did not check out.
	Warnings []string
}

// Entry returns the entry with that name.
func (l *Library) Entry(name string) (Entry, []byte, bool) {
	for _, e := range l.Entries {
		if e.Name == name {
			return e, l.Files[e.Path], true
		}
	}
	return Entry{}, nil, false
}

// Config configures a Service.
type Config struct {
	// Repo is the library's GitHub repository, owner/name.
	Repo string
	// PinnedVersion is the default version ("" = the embedded copy's).
	PinnedVersion string
	// CacheDir holds one directory per fetched version.
	CacheDir string
	// ArchiveURL is the tarball URL, with %s for the repo and %s for the version (no "v").
	ArchiveURL string
	// LatestURL is the GitHub "latest release" API URL, with %s for the repo.
	LatestURL string
	// Timeout bounds one fetch.
	Timeout time.Duration
	// Offline says whether fetching is off (the app's offline mode); only the embedded copy and
	// the disk cache serve then. nil: always online.
	Offline func() bool
	// HTTPClient is used for fetches (nil: a client with Timeout).
	HTTPClient *http.Client
}

// DefaultRepo is the public library repository.
const DefaultRepo = "embeddedci-com/benchpod-sensors"

// CacheEnv moves the download cache, as for the Python SDK (BENCHPOD_SENSOR_CACHE).
const CacheEnv = "BENCHPOD_SENSOR_CACHE"

// DefaultCacheDir is where downloaded versions live: $BENCHPOD_SENSOR_CACHE, else
// <user cache>/embeddedci/benchpod-sensors, the folder the Python SDK and the CLI share.
func DefaultCacheDir() string {
	if d := strings.TrimSpace(os.Getenv(CacheEnv)); d != "" {
		return d
	}
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "embeddedci", "benchpod-sensors")
}

// errOffline: fetching is turned off (offline mode). Not remembered as a failure, so turning
// offline mode off takes effect at once.
var errOffline = errors.New("offline mode: no downloads")

// Service resolves and caches library versions. Safe for concurrent use.
type Service struct {
	cfg      Config
	embedded *Library

	mu       sync.Mutex
	mem      map[string]*Library
	failed   map[string]failure
	latest   string
	latestAt time.Time
	fetching map[string]*sync.WaitGroup
}

type failure struct {
	err error
	at  time.Time
}

// How long a failed fetch is remembered (so a missing tag does not cost a request per page view)
// and how long "latest" stays resolved.
const (
	failureTTL = 5 * time.Minute
	latestTTL  = time.Hour
)

// New builds a Service. It fails only when the embedded copy is broken (a build problem).
func New(cfg Config) (*Service, error) {
	emb, err := LoadEmbedded()
	if err != nil {
		return nil, fmt.Errorf("embedded sensor library: %w", err)
	}
	if cfg.Repo == "" {
		cfg.Repo = DefaultRepo
	}
	if v, ok := NormalizeVersion(cfg.PinnedVersion); ok && v != "" && v != Latest {
		cfg.PinnedVersion = v
	} else {
		if cfg.PinnedVersion != "" {
			log.Printf("sensor library: pinned version %q is not X.Y.Z; using the embedded %s", cfg.PinnedVersion, emb.Version)
		}
		cfg.PinnedVersion = emb.Version
	}
	if cfg.ArchiveURL == "" {
		cfg.ArchiveURL = "https://github.com/%s/archive/refs/tags/v%s.tar.gz"
	}
	if cfg.LatestURL == "" {
		cfg.LatestURL = "https://api.github.com/repos/%s/releases/latest"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: cfg.Timeout}
	}
	if cfg.CacheDir == "" {
		cfg.CacheDir = DefaultCacheDir()
	}
	return &Service{cfg: cfg, embedded: emb, mem: map[string]*Library{}, failed: map[string]failure{},
		fetching: map[string]*sync.WaitGroup{}}, nil
}

// offline says whether fetching is off now.
func (s *Service) offline() bool { return s.cfg.Offline != nil && s.cfg.Offline() }

// PinnedVersion is the app's default version.
func (s *Service) PinnedVersion() string { return s.cfg.PinnedVersion }

// Repo is the library repository, owner/name.
func (s *Service) Repo() string { return s.cfg.Repo }

// EmbeddedVersion is the version of the copy baked into this build.
func (s *Service) EmbeddedVersion() string { return s.embedded.Version }

// Resolved says which library a Resolve call returned and why.
type Resolved struct {
	// Requested is what was asked for: "" (the pinned version), "latest" or X.Y.Z.
	Requested string `json:"requested"`
	// Version and Origin are what is served.
	Version string `json:"version"`
	Origin  string `json:"origin"`
	// Warnings explain a fallback (and entries a release left out).
	Warnings []string `json:"warnings"`
}

// Resolve returns the library for requested ("" = pinned, "latest", or X.Y.Z), falling back to
// the pinned version and then to the embedded copy. It always returns a library.
func (s *Service) Resolve(ctx context.Context, requested string) (*Library, Resolved) {
	res := Resolved{Requested: requested, Warnings: []string{}}
	want, ok := NormalizeVersion(requested)
	if !ok {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%q is not a library version (latest or X.Y.Z); using the pinned version %s", requested, s.cfg.PinnedVersion))
		want = ""
	}
	if want == Latest {
		v, err := s.latestVersion(ctx)
		switch {
		case err == nil:
			want = v
		case s.newestCached() != "":
			// Offline, or GitHub cannot be asked: the newest release fetched before.
			want = s.newestCached()
			res.Warnings = append(res.Warnings, fmt.Sprintf("could not find the latest sensor library release (%v); using the newest one downloaded before, %s", err, want))
		default:
			res.Warnings = append(res.Warnings, fmt.Sprintf("could not find the latest sensor library release (%v); using %s", err, s.cfg.PinnedVersion))
			want = ""
		}
	}
	if want == "" {
		want = s.cfg.PinnedVersion
	}
	tries := []string{want}
	if want != s.cfg.PinnedVersion {
		tries = append(tries, s.cfg.PinnedVersion)
	}
	for _, v := range tries {
		lib, origin, err := s.get(ctx, v)
		if err == nil {
			res.Version, res.Origin = lib.Version, origin
			if v != want {
				res.Warnings = append(res.Warnings, fmt.Sprintf("using the pinned sensor library %s instead", v))
			}
			res.Warnings = append(res.Warnings, lib.Warnings...)
			return lib, res
		}
		res.Warnings = append(res.Warnings, fmt.Sprintf("sensor library %s is not available (%v)", v, err))
	}
	res.Version, res.Origin = s.embedded.Version, OriginEmbedded
	res.Warnings = append(res.Warnings, "using the copy built into the app, "+s.embedded.Version)
	return s.embedded, res
}

// newestCached is the highest version in the disk cache, or "".
func (s *Service) newestCached() string {
	ents, err := os.ReadDir(s.cfg.CacheDir)
	if err != nil {
		return ""
	}
	best := ""
	for _, e := range ents {
		if !e.IsDir() || !versionRe.MatchString(e.Name()) {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.cfg.CacheDir, e.Name(), IndexFile)); err != nil {
			continue
		}
		if best == "" || versionLess(best, e.Name()) {
			best = e.Name()
		}
	}
	return best
}

// versionLess compares two X.Y.Z versions.
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return false
}

// CheckVersion reports whether version (X.Y.Z or latest) can be served: nil when it can,
// ErrNoSuchVersion when the repository has no such tag, another error when GitHub could not be
// asked (offline, timeout).
func (s *Service) CheckVersion(ctx context.Context, version string) error {
	v, ok := NormalizeVersion(version)
	if !ok || v == "" {
		return fmt.Errorf("%q is not a library version (latest or X.Y.Z)", version)
	}
	if v == Latest {
		_, err := s.latestVersion(ctx)
		return err
	}
	_, _, err := s.get(ctx, v)
	return err
}

func (s *Service) get(ctx context.Context, v string) (*Library, string, error) {
	if v == s.embedded.Version {
		return s.embedded, OriginEmbedded, nil
	}
	for {
		s.mu.Lock()
		if lib, ok := s.mem[v]; ok {
			s.mu.Unlock()
			return lib, OriginCache, nil
		}
		if f, ok := s.failed[v]; ok && time.Since(f.at) < failureTTL {
			s.mu.Unlock()
			return nil, "", f.err
		}
		if wg, ok := s.fetching[v]; ok {
			// Another request fetches it: wait and look again.
			s.mu.Unlock()
			wg.Wait()
			continue
		}
		wg := &sync.WaitGroup{}
		wg.Add(1)
		s.fetching[v] = wg
		s.mu.Unlock()

		lib, origin, err := s.load(ctx, v)
		s.mu.Lock()
		delete(s.fetching, v)
		if err == nil {
			s.mem[v] = lib
			delete(s.failed, v)
		} else if !errors.Is(err, context.Canceled) && !errors.Is(err, errOffline) {
			s.failed[v] = failure{err: err, at: time.Now()}
		}
		s.mu.Unlock()
		wg.Done()
		return lib, origin, err
	}
}

// load reads a version from the disk cache, else fetches it.
func (s *Service) load(ctx context.Context, v string) (*Library, string, error) {
	dir := filepath.Join(s.cfg.CacheDir, v)
	if _, err := os.Stat(filepath.Join(dir, IndexFile)); err == nil {
		lib, err := LoadFS(os.DirFS(dir), v)
		if err == nil {
			return lib, OriginCache, nil
		}
		log.Printf("sensor library: cached %s is broken, fetching again: %v", v, err)
		_ = os.RemoveAll(dir)
	}
	if s.offline() {
		return nil, "", errOffline
	}
	lib, err := s.fetch(ctx, v)
	if err != nil {
		return nil, "", err
	}
	if err := s.writeCache(lib); err != nil {
		log.Printf("sensor library: caching %s: %v", v, err)
	}
	return lib, OriginGitHub, nil
}

func (s *Service) fetch(ctx context.Context, v string) (*Library, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	url := fmt.Sprintf(s.cfg.ArchiveURL, s.cfg.Repo, v)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: tag v%s", ErrNoSuchVersion, v)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	files, err := readTarball(resp.Body)
	if err != nil {
		return nil, err
	}
	return fromFiles(files, v)
}

// readTarball returns the regular files of a GitHub source tarball, with the top directory
// (<repo>-<version>/) stripped.
func readTarball(r io.Reader) (map[string][]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(io.LimitReader(gz, maxArchiveBytes))
	files := map[string][]byte{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := h.Name
		if i := strings.IndexByte(name, '/'); i >= 0 {
			name = name[i+1:]
		} else {
			continue
		}
		name, ok := cleanPath(name)
		if !ok {
			continue
		}
		limit := int64(MaxSourceBytes)
		if name == IndexFile {
			limit = maxIndexBytes
		} else if !isDefinitionFile(name) {
			continue
		}
		if h.Size > limit {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, limit+1))
		if err != nil {
			return nil, fmt.Errorf("archive: %w", err)
		}
		files[name] = data
	}
	return files, nil
}

func isDefinitionFile(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	return ext == ".yaml" || ext == ".yml" || ext == ".json"
}

// cleanPath accepts a relative, slash-separated path inside the archive.
func cleanPath(p string) (string, bool) {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return "", false
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}

// fromFiles builds a Library from library.json and the entry files. want is the expected version
// ("" accepts any).
func fromFiles(files map[string][]byte, want string) (*Library, error) {
	raw, ok := files[IndexFile]
	if !ok {
		return nil, errors.New(IndexFile + " is missing at the root of the release")
	}
	var idx Index
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("%s: %w", IndexFile, err)
	}
	if idx.Format != 1 {
		return nil, fmt.Errorf("%s: format is %d; this app reads format 1", IndexFile, idx.Format)
	}
	if want != "" && strings.TrimPrefix(idx.Version, "v") != want {
		return nil, fmt.Errorf("%s: version is %q, the tag is %s", IndexFile, idx.Version, want)
	}
	idx.Version = strings.TrimPrefix(idx.Version, "v")
	if len(idx.Entries) > maxEntries {
		return nil, fmt.Errorf("%s: %d entries; at most %d", IndexFile, len(idx.Entries), maxEntries)
	}
	lib := &Library{Index: Index{Format: idx.Format, Version: idx.Version, Entries: []Entry{}},
		Files: map[string][]byte{}}
	seen := map[string]bool{}
	for _, e := range idx.Entries {
		p, ok := cleanPath(e.Path)
		if !ok {
			lib.Warnings = append(lib.Warnings, fmt.Sprintf("library entry %q: bad path", e.Path))
			continue
		}
		data, ok := files[p]
		if !ok {
			lib.Warnings = append(lib.Warnings, fmt.Sprintf("library entry %s: file missing", p))
			continue
		}
		def, err := Parse(data)
		if err != nil {
			lib.Warnings = append(lib.Warnings, fmt.Sprintf("library entry %s: %v", p, err))
			continue
		}
		if def.Name != e.Name || def.Kind != e.Kind {
			lib.Warnings = append(lib.Warnings, fmt.Sprintf("library entry %s: the index says %s/%s, the file %s/%s", p, e.Kind, e.Name, def.Kind, def.Name))
			continue
		}
		if seen[def.Name] {
			lib.Warnings = append(lib.Warnings, fmt.Sprintf("library entry %s: name %s is used twice", p, def.Name))
			continue
		}
		seen[def.Name] = true
		e.Path = p
		if e.Label == "" {
			e.Label = def.Label
		}
		lib.Entries = append(lib.Entries, e)
		lib.Files[p] = data
	}
	for _, w := range lib.Warnings {
		log.Printf("sensor library %s: %s", lib.Version, w)
	}
	return lib, nil
}

// LoadFS reads a library from a directory laid out like a release (library.json at the root).
func LoadFS(fsys fs.FS, want string) (*Library, error) {
	files := map[string][]byte{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if p != IndexFile && !isDefinitionFile(p) {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		files[p] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return fromFiles(files, want)
}

// LoadEmbedded reads the copy baked into this build.
func LoadEmbedded() (*Library, error) {
	sub, err := fs.Sub(embeddedFS, "embedded")
	if err != nil {
		return nil, err
	}
	return LoadFS(sub, "")
}

func (s *Service) writeCache(lib *Library) error {
	if err := os.MkdirAll(s.cfg.CacheDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(s.cfg.CacheDir, "."+lib.Version+".tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	idx, err := json.MarshalIndent(lib.Index, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, IndexFile), idx, 0o644); err != nil {
		return err
	}
	for p, data := range lib.Files {
		dst := filepath.Join(tmp, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}
	final := filepath.Join(s.cfg.CacheDir, lib.Version)
	if err := os.Rename(tmp, final); err != nil {
		if _, statErr := os.Stat(filepath.Join(final, IndexFile)); statErr == nil {
			return nil // another process cached it first
		}
		return err
	}
	return nil
}

// latestVersion asks GitHub for the newest release (cached for latestTTL).
func (s *Service) latestVersion(ctx context.Context) (string, error) {
	s.mu.Lock()
	if s.latest != "" && time.Since(s.latestAt) < latestTTL {
		v := s.latest
		s.mu.Unlock()
		return v, nil
	}
	if f, ok := s.failed[Latest]; ok && time.Since(f.at) < failureTTL {
		s.mu.Unlock()
		return "", f.err
	}
	s.mu.Unlock()
	v, err := s.fetchLatest(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, errOffline) {
			s.failed[Latest] = failure{err: err, at: time.Now()}
		}
		return "", err
	}
	delete(s.failed, Latest)
	s.latest, s.latestAt = v, time.Now()
	return v, nil
}

func (s *Service) fetchLatest(ctx context.Context) (string, error) {
	if s.offline() {
		return "", errOffline
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(s.cfg.LatestURL, s.cfg.Repo), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("%w: the repository has no release yet", ErrNoSuchVersion)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub answered HTTP %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	v, ok := NormalizeVersion(body.TagName)
	if !ok || v == "" || v == Latest {
		return "", fmt.Errorf("the latest release tag %q is not vX.Y.Z", body.TagName)
	}
	return v, nil
}

// BuildIndex scans a library checkout (sensors/ and stimuli/) and returns its index, sorted by
// path. The generator (gen/) and the embedded-copy test use it.
func BuildIndex(fsys fs.FS, version string) (Index, map[string][]byte, error) {
	idx := Index{Format: 1, Version: version, Entries: []Entry{}}
	files := map[string][]byte{}
	for _, root := range []string{"sensors", "stimuli"} {
		if _, err := fs.Stat(fsys, root); err != nil {
			continue
		}
		err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !isDefinitionFile(p) {
				return err
			}
			data, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			def, err := Parse(data)
			if err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			idx.Entries = append(idx.Entries, Entry{Path: p, Name: def.Name, Kind: def.Kind, Label: def.Label})
			files[p] = data
			return nil
		})
		if err != nil {
			return idx, nil, err
		}
	}
	sort.Slice(idx.Entries, func(i, j int) bool { return idx.Entries[i].Path < idx.Entries[j].Path })
	return idx, files, nil
}
