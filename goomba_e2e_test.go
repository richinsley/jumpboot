package jumpboot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/richinsley/goomba"
	"github.com/richinsley/goomba/solve"
	"github.com/richinsley/goomba/virtual"
)

// refChannel serves a conda channel over TLS from a loopback address, as
// goomba's own create differential does, built from the reference
// micromamba root $GOOMBA_REF_ROOT (only ever read): its index lists the
// conda-forge repodata records micromamba kept for the cached packages
// (pkgs/<dist>/info/repodata_record.json, for this platform and noarch),
// and the package archives are copied from $GOOMBA_REF_ROOT/pkgs into
// scratch when first requested. Solving against it can only choose
// packages the cache has, so the test needs no network.
type refChannel struct {
	srv   *httptest.Server
	url   string                               // <server>/conda-forge
	index map[string]map[string]map[string]any // subdir -> fn -> index entry
	files map[string]string                    // "<subdir>/<fn>" -> archive in the reference cache
	store string                               // scratch copies of the archives

	mu   sync.Mutex
	hits int
}

// indexKeys are the repodata fields copied from repodata_record.json.
var indexKeys = []string{"name", "version", "build", "build_number", "depends", "constrains", "subdir", "noarch",
	"sha256", "md5", "size", "timestamp", "license", "license_family", "track_features", "features", "python_site_packages_path"}

// startRefChannel builds the index from refRoot's package cache for
// platform and serves it; it skips the test when the cache holds no
// python for platform.
func startRefChannel(t *testing.T, refRoot, platform string) *refChannel {
	t.Helper()
	recs, err := filepath.Glob(filepath.Join(refRoot, "pkgs", "*", "info", "repodata_record.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := &refChannel{index: map[string]map[string]map[string]any{}, files: map[string]string{}, store: t.TempDir()}
	pythons := 0
	for _, p := range recs {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var rec map[string]any
		if err := json.Unmarshal(data, &rec); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		fn, _ := rec["fn"].(string)
		subdir, _ := rec["subdir"].(string)
		archive := filepath.Join(refRoot, "pkgs", fn)
		if fi, err := os.Stat(archive); err != nil || !fi.Mode().IsRegular() || fn != filepath.Base(fn) ||
			(subdir != platform && subdir != "noarch") {
			continue
		}
		entry := map[string]any{}
		for _, k := range indexKeys {
			if v, ok := rec[k]; ok && v != nil {
				entry[k] = v
			}
		}
		if c.index[subdir] == nil {
			c.index[subdir] = map[string]map[string]any{}
		}
		c.index[subdir][fn] = entry
		c.files[subdir+"/"+fn] = archive
		if rec["name"] == "python" {
			pythons++
		}
	}
	if pythons == 0 {
		t.Skipf("$GOOMBA_REF_ROOT/pkgs holds no python for %s", platform)
	}
	c.srv = httptest.NewTLSServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.srv.Close)
	c.url = c.srv.URL + "/conda-forge"
	return c
}

func (c *refChannel) serve(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.hits++
	c.mu.Unlock()
	rest, ok := strings.CutPrefix(r.URL.Path, "/conda-forge/")
	subdir, fn, ok2 := strings.Cut(rest, "/")
	if !ok || !ok2 || c.index[subdir] == nil && subdir != "noarch" {
		http.NotFound(w, r)
		return
	}
	if fn == "repodata.json" {
		conda, tarbz2 := map[string]any{}, map[string]any{}
		for name, e := range c.index[subdir] {
			if strings.HasSuffix(name, ".tar.bz2") {
				tarbz2[name] = e
			} else {
				conda[name] = e
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"info": map[string]any{"subdir": subdir}, "packages": tarbz2, "packages.conda": conda, "repodata_version": 1,
		})
		return
	}
	src, ok := c.files[subdir+"/"+fn]
	if !ok {
		http.NotFound(w, r) // the shard index and compressed index variants
		return
	}
	p, err := c.copyArchive(src, subdir, fn)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.ServeFile(w, r, p)
}

// copyArchive copies a reference archive into the store once.
func (c *refChannel) copyArchive(src, subdir, fn string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	dst := filepath.Join(c.store, subdir, fn)
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.Create(dst + ".part")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return dst, os.Rename(dst+".part", dst)
}

func (c *refChannel) requests() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits
}

// entry returns the index entry of the newest cached package called name.
func (c *refChannel) entry(t *testing.T, name string) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, sd := range c.index {
		for _, e := range sd {
			if e["name"] == name {
				found = append(found, e)
			}
		}
	}
	if len(found) == 0 {
		t.Skipf("$GOOMBA_REF_ROOT/pkgs holds no %s", name)
	}
	sort.Slice(found, func(i, j int) bool { return found[i]["version"].(string) > found[j]["version"].(string) })
	return found[0]
}

// pinned returns a conda PackageSpec of the index entry e.
func pinned(e map[string]any) PackageSpec {
	return PackageSpec{Name: e["name"].(string), Version: e["version"].(string), Build: e["build"].(string), SHA256: e["sha256"].(string), Source: "conda"}
}

// progressLog records progress callback calls.
type progressLog struct {
	mu    sync.Mutex
	calls []string
	last  map[string][2]int64
}

func (l *progressLog) callback(msg string, current, total int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = map[string][2]int64{}
	}
	if len(l.calls) == 0 || l.calls[len(l.calls)-1] != msg {
		l.calls = append(l.calls, msg)
	}
	l.last[msg] = [2]int64{current, total}
}

// TestGoombaEndToEndOffline creates python 3.11 with goomba from a channel
// served from $GOOMBA_REF_ROOT, reuses and freezes it, and restores JSON
// specifications with checksums.
func TestGoombaEndToEndOffline(t *testing.T) {
	refRoot := os.Getenv("GOOMBA_REF_ROOT")
	if refRoot == "" {
		t.Skip("set GOOMBA_REF_ROOT to a micromamba root holding envs/base-3.11 and its pkgs/ cache")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the reference package cache is for a Unix platform")
	}
	ch := startRefChannel(t, refRoot, virtual.HostPlatform())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	root := t.TempDir()
	opts := GoombaOptions{PythonVersion: "3.11", Channels: []string{ch.url}, Client: ch.srv.Client()}

	var progress progressLog
	start := time.Now()
	env, err := CreateEnvironmentGoombaWithOptions(ctx, "base-3.11", root, opts, progress.callback)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("created in %v; progress %q", time.Since(start), progress.calls)

	envPath := filepath.Join(root, "envs", "base-3.11")
	if env.Backend != BackendGoomba || !env.IsNew || env.MicromambaPath != "" || env.EnvPath != envPath {
		t.Errorf("env = %+v", env.BaseEnvironment)
	}
	if env.PythonVersion.Major != 3 || env.PythonVersion.Minor != 11 || env.PipVersion.Major <= 0 {
		t.Errorf("python %+v, pip %+v", env.PythonVersion, env.PipVersion)
	}
	site := filepath.Join(envPath, "lib", "python3.11", "site-packages")
	if env.SitePackagesPath != site {
		t.Errorf("SitePackagesPath = %q, want %q", env.SitePackagesPath, site)
	}
	if fi, err := os.Stat(filepath.Join(site, "pip")); err != nil || !fi.IsDir() {
		t.Errorf("pip is not in site-packages: %v", err)
	}
	lib := map[string]string{"linux": "libpython3.11.so", "darwin": "libpython3.11.dylib"}[runtime.GOOS]
	if want := filepath.Join(envPath, "lib", lib); env.PythonLibPath != want {
		t.Errorf("PythonLibPath = %q, want %q", env.PythonLibPath, want)
	}
	if _, err := os.Stat(env.PythonLibPath); err != nil {
		t.Errorf("PythonLibPath: %v", err)
	}
	out, err := RunReadStdout(env.PythonPath, "-c", "import sys, ssl, sqlite3, pip; print(sys.prefix)")
	if err != nil || strings.TrimSpace(out) != envPath {
		t.Errorf("python: %q, %v", out, err)
	}
	if _, err := os.Lstat(filepath.Join(envPath, "conda-meta", ".goomba-incomplete")); !os.IsNotExist(err) {
		t.Errorf("incomplete marker left: %v", err)
	}
	for _, d := range []string{"pkgs", "repodata"} {
		fi, err := os.Stat(filepath.Join(root, "goomba", d))
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("goomba/%s: %v %v", d, fi.Mode(), err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "pkgs")); !os.IsNotExist(err) {
		t.Errorf("micromamba's package cache was created: %v", err)
	}
	if f := progress.last["Fetching packages"]; f[0] <= 0 || f[0] != f[1] {
		t.Errorf("last download progress %v, want all bytes", f)
	}
	if l := progress.last["Linking packages"]; l[0] <= 0 || l[0] != l[1] {
		t.Errorf("last link progress %v", l)
	}
	for _, msg := range []string{"Loading channel index", "Solving environment", "Fetching packages", "Linking packages", "Python environment created successfully"} {
		if _, ok := progress.last[msg]; !ok {
			t.Errorf("no %q progress", msg)
		}
	}

	// Reuse: no request reaches the channel.
	before := ch.requests()
	again, err := CreateEnvironmentGoombaWithOptions(ctx, "base-3.11", root, opts, nil)
	if err != nil || again.IsNew || ch.requests() != before {
		t.Fatalf("reuse: IsNew %v, %d requests, %v", again != nil && again.IsNew, ch.requests()-before, err)
	}

	// Freeze: conda packages from conda-meta, with their checksums.
	frozen := filepath.Join(t.TempDir(), "env.json")
	if err := env.FreezeToFile(frozen); err != nil {
		t.Fatal(err)
	}
	es := readSpec(t, frozen)
	py := ch.entry(t, "python")
	if es.Name != "base-3.11" || es.PythonVersion != "3.11" || len(es.Channels) != 1 || es.Channels[0] != ch.url {
		t.Errorf("frozen %+v", es)
	}
	found := false
	for _, p := range es.Packages {
		if p.Name == "python" {
			found = true
			if p.Source != "conda" || !strings.HasPrefix(p.Version, "3.11.") || p.SHA256 == "" {
				t.Errorf("frozen python %+v", p)
			}
		}
	}
	if !found {
		t.Errorf("python not frozen: %+v", es.Packages)
	}

	// A JSON restore with exact pins and verified checksums.
	packaging := ch.entry(t, "packaging")
	var python311 map[string]any
	for _, sd := range ch.index {
		for _, e := range sd {
			if e["name"] == "python" && strings.HasPrefix(e["version"].(string), "3.11.") {
				python311 = e
			}
		}
	}
	if python311 == nil {
		t.Skipf("no python 3.11 in $GOOMBA_REF_ROOT/pkgs (newest: %v)", py["version"])
	}
	// The specifications have no python_version: the pinned python sets it
	// (without that, python=3.10 would conflict with the pin).
	restoreWith := func(ropts RestoreOptions, name string, pkgs ...PackageSpec) (*PythonEnvironment, error) {
		p := filepath.Join(t.TempDir(), name+".json")
		writeJSON(t, p, EnvironmentSpec{Name: name, Channels: []string{ch.url}, Packages: pkgs})
		return CreateEnvironmentFromJSONFileGoomba(ctx, p, root, ropts, GoombaOptions{Client: ch.srv.Client()}, nil)
	}
	restore := func(name string, pkgs ...PackageSpec) (*PythonEnvironment, error) {
		return restoreWith(RestoreOptions{VerifyChecksums: true}, name, pkgs...)
	}
	// Strict needs every solved package listed with its checksum: python's
	// dependencies are not, so nothing is installed.
	if _, err := restoreWith(RestoreOptions{VerifyChecksums: true, Strict: true}, "partial", pinned(python311), pinned(packaging)); err == nil ||
		!strings.Contains(err.Error(), "is in the solution but not among the specification's conda packages") {
		t.Errorf("strict restore of a partial list: err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "envs", "partial")); !os.IsNotExist(err) {
		t.Errorf("the refused strict restore left an environment (%v)", err)
	}
	restored, err := restore("restored", pinned(python311), pinned(packaging))
	if err != nil {
		t.Fatal(err)
	}
	out, err = RunReadStdout(restored.PythonPath, "-c", "import packaging, sys; print(packaging.__version__, sys.version.split()[0])")
	if want := packaging["version"].(string) + " " + python311["version"].(string); err != nil || strings.TrimSpace(out) != want {
		t.Errorf("restored: %q, want %q (%v)", out, want, err)
	}
	if restored.Backend != BackendGoomba || !restored.IsNew || restored.PipVersion.Major <= 0 {
		t.Errorf("restored %+v", restored.BaseEnvironment)
	}

	// The pins reached the solver: the history records them as the specs.
	hist, err := os.ReadFile(filepath.Join(restored.EnvPath, "conda-meta", "history"))
	if err != nil {
		t.Fatal(err)
	}
	for _, pin := range []string{
		"python==" + python311["version"].(string) + "=" + python311["build"].(string),
		"packaging==" + packaging["version"].(string) + "=" + packaging["build"].(string),
	} {
		if !strings.Contains(string(hist), "# update specs: ") || !strings.Contains(string(hist), `"`+pin+`"`) {
			t.Errorf("the history does not record the pin %q:\n%s", pin, hist)
		}
	}

	// A wrong checksum fails before anything is installed.
	bad := pinned(packaging)
	bad.SHA256 = python311["sha256"].(string)
	if _, err := restore("tampered", pinned(python311), bad); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("tampered: err = %v, want ErrChecksumMismatch", err)
	} else {
		t.Logf("tampered: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "envs", "tampered")); !os.IsNotExist(err) {
		t.Errorf("the tampered restore left %s (%v)", filepath.Join(root, "envs", "tampered"), err)
	}

	// A restore never installs into an existing environment.
	if _, err := restore("restored", pinned(python311)); !errors.Is(err, goomba.ErrPrefixNotEmpty) {
		t.Errorf("restore over an environment: err = %v, want goomba.ErrPrefixNotEmpty", err)
	}

	// An unsolvable request reaches the caller as *solve.Unsolvable.
	unsolvable := opts
	unsolvable.Specs = []string{"python=3.11", "nonexistent-package-for-jumpboot"}
	_, err = CreateEnvironmentGoombaWithOptions(ctx, "unsolvable", root, unsolvable, nil)
	var u *solve.Unsolvable
	if !errors.As(err, &u) {
		t.Fatalf("err = %v, want *solve.Unsolvable", err)
	}
	t.Logf("unsolvable:\n%s", u.Explanation)
	if _, err := os.Lstat(filepath.Join(root, "envs", "unsolvable")); !os.IsNotExist(err) {
		t.Errorf("the unsolvable request left an environment (%v)", err)
	}

	// A concurrent installation into the same environment.
	testPrefixLocked(t, ctx, root, ch, python311)

	// Freeze, then restore with Strict: everything installed is verified,
	// and the copy holds the same packages.
	es = readSpec(t, frozen)
	es.Name = "copy"
	copySpec := filepath.Join(t.TempDir(), "copy.json")
	writeJSON(t, copySpec, es)
	cp, err := CreateEnvironmentFromJSONFileGoomba(ctx, copySpec, root, RestoreOptions{VerifyChecksums: true, Strict: true}, GoombaOptions{Client: ch.srv.Client()}, nil)
	if err != nil {
		t.Fatalf("freeze -> strict restore: %v", err)
	}
	a, b := readCondaMetaPackages(t, env.EnvPath), readCondaMetaPackages(t, cp.EnvPath)
	if len(a) != len(b) {
		t.Errorf("the copy has %d packages, the original %d", len(b), len(a))
	}
	for n, p := range a {
		if q, ok := b[n]; !ok || p.Version != q.Version || p.Build != q.Build || p.SHA256 != q.SHA256 {
			t.Errorf("%s: original %+v, copy %+v", n, p, q)
		}
	}

	// A failing pip step removes the new environment, so a retry fails the
	// same way instead of finding the environment in its way.
	pipFail := filepath.Join(t.TempDir(), "pipfail.json")
	writeJSON(t, pipFail, EnvironmentSpec{Name: "pipfail", PythonVersion: "3.11", Channels: []string{ch.url},
		Packages: []PackageSpec{pinned(python311), {Name: "--jumpboot-no-such-option", Version: "1", Source: "pip"}}})
	for try := 1; try <= 2; try++ {
		_, err := CreateEnvironmentFromJSONFileGoomba(ctx, pipFail, root, RestoreOptions{}, GoombaOptions{Client: ch.srv.Client()}, nil)
		if err == nil || !strings.Contains(err.Error(), "error installing pip package") || !strings.Contains(err.Error(), "was removed") {
			t.Errorf("pip failure, try %d: err = %v", try, err)
		}
		if _, err := os.Lstat(filepath.Join(root, "envs", "pipfail")); !os.IsNotExist(err) {
			t.Errorf("pip failure, try %d: the environment was left (%v)", try, err)
		}
	}

	testChannelOrderHint(t, ctx, ch, python311)

	// The callback is not called once the Timeout has expired: one slow
	// call outlives it, and no other follows (the caches are warm, so
	// goomba still has many events to report).
	slow := opts
	slow.Timeout = 300 * time.Millisecond
	calls := 0
	_, err = CreateEnvironmentGoombaWithOptions(ctx, "slow", root, slow, func(msg string, current, total int64) {
		if msg == "Fetching packages" {
			calls++
			time.Sleep(400 * time.Millisecond)
		}
	})
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Errorf("slow callback: %d calls, err = %v; want 1 call and the deadline", calls, err)
	}
}

// testChannelOrderHint restores a specification whose two channels are
// listed lowest priority first: the high-priority channel has the pinned
// packaging, the other only a decoy of the same name. Strict priority
// takes packaging from the first channel listed, so the request is
// unsolvable, and the error says the channel order matters.
func testChannelOrderHint(t *testing.T, ctx context.Context, ref *refChannel, python map[string]any) {
	t.Helper()
	pk := ref.entry(t, "packaging")
	decoy := map[string]any{}
	for k, v := range pk {
		decoy[k] = v
	}
	decoy["version"], decoy["build"] = "0.0.1", "decoy_0"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
		if len(parts) != 3 || parts[2] != "repodata.json" || (parts[0] != "high" && parts[0] != "low") {
			if len(parts) == 3 {
				if src, ok := ref.files[parts[1]+"/"+parts[2]]; ok && parts[0] == "high" {
					if p, err := ref.copyArchive(src, parts[1], parts[2]); err == nil {
						http.ServeFile(w, r, p)
						return
					}
				}
			}
			http.NotFound(w, r)
			return
		}
		conda := map[string]any{}
		if parts[0] == "high" {
			for fn, e := range ref.index[parts[1]] {
				conda[fn] = e
			}
		} else if parts[1] == pk["subdir"] {
			conda["packaging-0.0.1-decoy_0.conda"] = decoy
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"info": map[string]any{"subdir": parts[1]}, "packages": map[string]any{}, "packages.conda": conda, "repodata_version": 1})
	}))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "order.json")
	writeJSON(t, p, EnvironmentSpec{Name: "order", PythonVersion: "3.11", Channels: []string{srv.URL + "/low", srv.URL + "/high"},
		Packages: []PackageSpec{pinned(python), pinned(pk)}})
	_, err := CreateEnvironmentFromJSONFileGoomba(ctx, p, t.TempDir(), RestoreOptions{}, GoombaOptions{Client: srv.Client()}, nil)
	var u *solve.Unsolvable
	if !errors.As(err, &u) || !strings.Contains(err.Error(), "lists 2 channels") {
		t.Errorf("channels in the wrong order: err = %v, want *solve.Unsolvable mentioning the channel order", err)
	}
}
