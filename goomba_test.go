package jumpboot

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/richinsley/goomba"
	"github.com/richinsley/goomba/fetch"
	"github.com/richinsley/goomba/lock"
	"github.com/richinsley/goomba/record"
	"github.com/richinsley/goomba/repodata"
	"github.com/richinsley/goomba/spec"
)

// writeScript writes an executable shell script that prints out.
func writeScript(t *testing.T, path, out string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '"+out+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// fakeCondaEnv lays out a conda environment root/envs/name whose python and
// pip are scripts printing the given versions, with a libpython library,
// as setCondaEnvPaths expects it on this OS. It is POSIX-only.
func fakeCondaEnv(t *testing.T, root, name, python, pip string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake environments are POSIX-only")
	}
	v, err := ParseVersion(python)
	if err != nil {
		t.Fatal(err)
	}
	env := &PythonEnvironment{BaseEnvironment: BaseEnvironment{RootDir: root, EnvironmentName: name}}
	setCondaEnvPaths(env, runtime.GOOS, v)
	writeScript(t, env.PythonPath, "Python "+python)
	writeScript(t, env.PipPath, "pip "+pip+" from "+env.SitePackagesPath+"/pip (python "+v.MinorString()+")")
	if err := os.MkdirAll(filepath.Dir(env.PythonLibPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.PythonLibPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(env.EnvPath, "conda-meta"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A history makes it a finished environment for goomba.CheckComplete.
	if err := os.WriteFile(filepath.Join(env.EnvPath, "conda-meta", "history"), []byte("==> 2026-10-17 00:00:00 <==\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// noMicromambaDownload points the micromamba download at a server that
// refuses it, for the rest of the test: a fake micromamba that fails to
// run must never lead CreateEnvironmentMamba to download the real one.
func noMicromambaDownload(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected micromamba download: %s", r.URL)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	orig := micromambaBaseURL
	micromambaBaseURL = srv.URL
	t.Cleanup(func() { micromambaBaseURL = orig })
}

func TestCondaEnvPathsPerGOOS(t *testing.T) {
	root := filepath.Join("some", "root")
	e := func(parts ...string) string {
		return filepath.Join(append([]string{root, "envs", "base-3.11"}, parts...)...)
	}
	for goos, want := range map[string]PythonEnvironment{
		"linux": {
			BaseEnvironment:   BaseEnvironment{EnvPath: e(), EnvBinPath: e("bin"), EnvLibPath: e("lib")},
			PythonPath:        e("bin", "python"),
			PipPath:           e("bin", "pip"),
			SitePackagesPath:  e("lib", "python3.11", "site-packages"),
			PythonLibPath:     e("lib", "libpython3.11.so"),
			PythonHeadersPath: e("include", "python3.11"),
		},
		"darwin": {
			BaseEnvironment:   BaseEnvironment{EnvPath: e(), EnvBinPath: e("bin"), EnvLibPath: e("lib")},
			PythonPath:        e("bin", "python"),
			PipPath:           e("bin", "pip"),
			SitePackagesPath:  e("lib", "python3.11", "site-packages"),
			PythonLibPath:     e("lib", "libpython3.11.dylib"),
			PythonHeadersPath: e("include", "python3.11"),
		},
		"windows": {
			BaseEnvironment:   BaseEnvironment{EnvPath: e(), EnvBinPath: e(), EnvLibPath: e("lib")},
			PythonPath:        e("python.exe"),
			PipPath:           e("Scripts", "pip.exe"),
			SitePackagesPath:  e("lib", "python3.11", "site-packages"),
			PythonLibPath:     e("python311.dll"),
			PythonHeadersPath: e("include", "python3.11"),
		},
	} {
		got := PythonEnvironment{BaseEnvironment: BaseEnvironment{RootDir: root, EnvironmentName: "base-3.11"}}
		setCondaEnvPaths(&got, goos, Version{Major: 3, Minor: 11, Patch: -1})
		want.RootDir, want.EnvironmentName = root, "base-3.11"
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", goos, got, want)
		}
	}
}

// TestGoombaFillsPathsAsMamba reuses one environment through both backends
// (offline: a fake micromamba and a fake environment) and compares every
// field.
func TestGoombaFillsPathsAsMamba(t *testing.T) {
	noMicromambaDownload(t)
	root := t.TempDir()
	fakeCondaEnv(t, root, "base-3.11", "3.11.9", "24.0")
	mm := filepath.Join(root, "bin", "micromamba")
	writeScript(t, mm, "2.2.0")
	if _, err := RunReadStdout(mm, "--version"); err != nil {
		t.Fatalf("the fake micromamba does not run (%v); not calling CreateEnvironmentMamba", err)
	}

	m, err := CreateEnvironmentMamba("base-3.11", root, "3.11", "conda-forge", nil)
	if err != nil {
		t.Fatal(err)
	}
	g, err := CreateEnvironmentGoomba("base-3.11", root, "3.11", "conda-forge", nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Backend != BackendMicromamba || g.Backend != BackendGoomba {
		t.Errorf("Backend = %q and %q, want %q and %q", m.Backend, g.Backend, BackendMicromamba, BackendGoomba)
	}
	if m.MicromambaPath != mm || g.MicromambaPath != "" {
		t.Errorf("MicromambaPath = %q and %q", m.MicromambaPath, g.MicromambaPath)
	}
	if m.IsNew || g.IsNew {
		t.Errorf("IsNew = %v and %v for an existing environment", m.IsNew, g.IsNew)
	}
	if g.PythonVersion != (Version{3, 11, 9}) || g.PipVersion != (Version{24, 0, -1}) {
		t.Errorf("versions = %+v, %+v", g.PythonVersion, g.PipVersion)
	}
	// Everything but the backend's own fields is the same.
	m.Backend, m.MicromambaPath, m.MicromambaVersion = g.Backend, g.MicromambaPath, g.MicromambaVersion
	if !reflect.DeepEqual(m, g) {
		t.Errorf("environments differ:\nmicromamba %+v\ngoomba     %+v", m, g)
	}
	if g.PythonLibPath == "" || g.SitePackagesPath != filepath.Join(root, "envs", "base-3.11", "lib", "python3.11", "site-packages") {
		t.Errorf("PythonLibPath %q, SitePackagesPath %q", g.PythonLibPath, g.SitePackagesPath)
	}
	// Reusing creates no goomba caches.
	if _, err := os.Stat(filepath.Join(root, "goomba")); !os.IsNotExist(err) {
		t.Errorf("reuse created %s/goomba (%v)", root, err)
	}
}

func TestBackendValues(t *testing.T) {
	for b, want := range map[Backend]string{BackendNone: "", BackendMicromamba: "micromamba", BackendGoomba: "goomba"} {
		if string(b) != want {
			t.Errorf("backend %q, want %q", b, want)
		}
	}
	var env PythonEnvironment
	if env.Backend != BackendNone {
		t.Errorf("zero environment has backend %q", env.Backend)
	}
}

func TestGoombaReuseChecksVersion(t *testing.T) {
	root := t.TempDir()
	fakeCondaEnv(t, root, "old", "3.9.18", "23.3")
	// The existing environment is reused, and checked like
	// CreateEnvironmentMamba checks it.
	_, err := CreateEnvironmentGoomba("old", root, "3.11", "", nil)
	if err == nil || !strings.Contains(err.Error(), "requested python version 3.11 is not available, found 3.9.18") {
		t.Fatalf("err = %v", err)
	}
	env, err := CreateEnvironmentGoomba("old", root, "3.9", "", nil)
	if err != nil || env.IsNew || env.PythonVersion != (Version{3, 9, 18}) {
		t.Fatalf("env = %+v, err = %v", env, err)
	}
}

func TestGoombaRefusesIncompleteEnvironment(t *testing.T) {
	root := t.TempDir()
	fakeCondaEnv(t, root, "half", "3.11.9", "24.0")
	marker := filepath.Join(root, "envs", "half", "conda-meta", ".goomba-incomplete")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := CreateEnvironmentGoomba("half", root, "3.11", "", nil)
	if !errors.Is(err, ErrIncompleteEnvironment) || !errors.Is(err, goomba.ErrIncomplete) || !strings.Contains(err.Error(), "RemoveIncompleteEnvironment") {
		t.Fatalf("err = %v, want ErrIncompleteEnvironment and goomba.ErrIncomplete", err)
	}
	specFile := filepath.Join(t.TempDir(), "env.json")
	writeJSON(t, specFile, EnvironmentSpec{Name: "half", PythonVersion: "3.11"})
	if _, err := CreateEnvironmentFromJSONFileGoomba(context.Background(), specFile, root, RestoreOptions{}, GoombaOptions{}, nil); !errors.Is(err, ErrIncompleteEnvironment) {
		t.Fatalf("restore: err = %v, want ErrIncompleteEnvironment", err)
	}
	env := &PythonEnvironment{BaseEnvironment: BaseEnvironment{EnvironmentName: "half", RootDir: root, EnvPath: filepath.Join(root, "envs", "half"), Backend: BackendGoomba}}
	if err := env.FreezeToFile(filepath.Join(t.TempDir(), "out.json")); !errors.Is(err, ErrIncompleteEnvironment) || !errors.Is(err, goomba.ErrIncomplete) {
		t.Fatalf("freeze: err = %v, want ErrIncompleteEnvironment", err)
	}
	// Nothing was removed.
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "envs", "half", "bin", "python")); err != nil {
		t.Fatalf("environment: %v", err)
	}
}

func TestGoombaOptionDefaults(t *testing.T) {
	d := GoombaOptions{}.withDefaults()
	if d.PythonVersion != "3.10" || !reflect.DeepEqual(d.Channels, []string{"conda-forge"}) || d.Timeout != 10*time.Minute {
		t.Errorf("defaults = %+v", d)
	}
	if d.ChannelAlias != "" || d.Client != nil || d.NoPyc || d.NvidiaSMI != "" || d.VirtualOverrides != nil || len(d.Specs) != 0 {
		t.Errorf("defaults = %+v", d)
	}
	in := GoombaOptions{PythonVersion: "3.12", Channels: []string{"a", "b"}, Specs: []string{"numpy"}, Timeout: time.Second}
	o := in.withDefaults()
	if o.PythonVersion != "3.12" || !reflect.DeepEqual(o.Channels, []string{"a", "b"}) || o.Timeout != time.Second {
		t.Errorf("explicit options changed: %+v", o)
	}
	o.Channels[0], o.Specs[0] = "x", "x"
	if in.Channels[0] != "a" || in.Specs[0] != "numpy" {
		t.Error("withDefaults shares the caller's slices")
	}
	// CreateEnvironmentGoomba's arguments.
	if o := goombaOptionsFor("", ""); !reflect.DeepEqual(o, GoombaOptions{}) {
		t.Errorf("goombaOptionsFor(\"\", \"\") = %+v", o)
	}
	if o := goombaOptionsFor("3.11", "bioconda"); o.PythonVersion != "3.11" || !reflect.DeepEqual(o.Channels, []string{"bioconda"}) {
		t.Errorf("goombaOptionsFor = %+v", o)
	}
}

func TestGoombaRefusesBadRequests(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, env string
		opts      GoombaOptions
		want      string
	}{
		{"python version", "e", GoombaOptions{PythonVersion: "invalid-version"}, "error parsing requested python version"},
		{"python spec injection", "e", GoombaOptions{PythonVersion: "3.11 [build=x]"}, "error parsing requested python version"},
		{"python glob", "e", GoombaOptions{PythonVersion: "3.*"}, "error parsing requested python version"},
		{"empty name", "", GoombaOptions{}, "invalid environment name"},
		{"dot name", ".", GoombaOptions{}, "invalid environment name"},
		{"parent name", "..", GoombaOptions{}, "invalid environment name"},
		{"nested name", "a/b", GoombaOptions{}, "invalid environment name"},
		{"escaping name", "../x", GoombaOptions{}, "invalid environment name"},
		{"negative timeout", "e", GoombaOptions{Timeout: -time.Second}, "negative GoombaOptions.Timeout"},
	} {
		root := filepath.Join(t.TempDir(), "root")
		_, err := CreateEnvironmentGoombaWithOptions(ctx, tc.env, root, tc.opts, nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Errorf("%s: the root directory was created", tc.name)
		}
	}
	// A cancelled context stops before anything is done.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	root := filepath.Join(t.TempDir(), "root")
	if _, err := CreateEnvironmentGoombaWithOptions(cctx, "e", root, GoombaOptions{}, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: err = %v", err)
	}
}

func TestGoombaSpecsPipRule(t *testing.T) {
	for _, tc := range []struct {
		python string
		extra  []string
		want   []string
	}{
		{"3.11", nil, []string{"python=3.11", "pip"}},
		{"3.12", []string{"numpy>=1.26"}, []string{"python=3.12", "numpy>=1.26", "pip"}},
		{"3.11", []string{"pip=24"}, []string{"python=3.11", "pip=24"}},
		{"3.11", []string{"pip"}, []string{"python=3.11", "pip"}},
		{"3.11", []string{"PIP >=23"}, []string{"python=3.11", "PIP >=23"}},
		{"3.11", []string{"conda-forge::pip"}, []string{"python=3.11", "conda-forge::pip"}},
		{"3.11", []string{"pip[version='>=24']"}, []string{"python=3.11", "pip[version='>=24']"}},
		{"3.11", []string{"https://conda.anaconda.org/conda-forge/noarch/pip-26.0.1-pyh8b19718_0.conda"},
			[]string{"python=3.11", "https://conda.anaconda.org/conda-forge/noarch/pip-26.0.1-pyh8b19718_0.conda"}},
		// Not pip itself: pip is added.
		{"3.11", []string{"pip-tools"}, []string{"python=3.11", "pip-tools", "pip"}},
		{"3.11", []string{"pi*"}, []string{"python=3.11", "pi*", "pip"}},
		{"3.11", []string{"pipx"}, []string{"python=3.11", "pipx", "pip"}},
	} {
		if got := goombaSpecs(tc.python, tc.extra); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("goombaSpecs(%q, %q) = %q, want %q", tc.python, tc.extra, got, tc.want)
		}
	}
}

// TestCondaPinSpecIsExact proves with goomba's own parser that the pins
// of a JSON restore are equalities, unlike conda's "=version" prefix
// match.
func TestCondaPinSpecIsExact(t *testing.T) {
	rec := func(version, build string) *record.PackageRecord {
		return &record.PackageRecord{Name: "numpy", Version: version, Build: build, Subdir: "linux-64"}
	}
	matches := func(s string, r *record.PackageRecord) bool {
		t.Helper()
		ms, err := spec.ParseMatchSpec(s)
		if err != nil {
			t.Fatalf("ParseMatchSpec(%q): %v", s, err)
		}
		return ms.Matches(r)
	}

	pin, err := condaPinSpec("numpy", "1.26", "py311h64a7726_0")
	if err != nil {
		t.Fatal(err)
	}
	if pin != "numpy==1.26=py311h64a7726_0" {
		t.Errorf("pin = %q", pin)
	}
	if !matches(pin, rec("1.26", "py311h64a7726_0")) {
		t.Error("the pin does not match its own package")
	}
	for _, r := range []*record.PackageRecord{rec("1.26.4", "py311h64a7726_0"), rec("1.26.0.1", "py311h64a7726_0"), rec("1.26", "py311h64a7726_1"), rec("1.27", "py311h64a7726_0")} {
		if matches(pin, r) {
			t.Errorf("pin %q matches %s", pin, r)
		}
	}
	// conda's fuzzy form would have matched a later patch release.
	if !matches("numpy=1.26", rec("1.26.4", "py311h64a7726_0")) {
		t.Error("numpy=1.26 should be the prefix match this pin avoids")
	}

	// Without a build: exactly the version, any build.
	pin, err = condaPinSpec("numpy", "1.26", "")
	if err != nil || pin != "numpy==1.26" {
		t.Fatalf("pin = %q, %v", pin, err)
	}
	if !matches(pin, rec("1.26", "anything_0")) || matches(pin, rec("1.26.4", "anything_0")) {
		t.Errorf("pin %q is not exact", pin)
	}

	// Real versions and builds pin exactly too.
	for _, p := range [][3]string{
		{"python", "3.11.15", "hd63d673_0_cpython"},
		{"tzdata", "2025c", "hc9c84f9_1"},
		{"_openmp_mutex", "4.5", "20_gnu"},
		{"ld_impl_linux-64", "2.45.1", "default_hbd61a6d_102"},
		{"libjpeg-turbo", "3.0.0", "hd590300_1"},
		{"pytorch", "2.3.1", "cuda120_py311h2cbd1b9_300"},
		{"icu", "1!73.2", "h59595ed_0"},
		{"openssl", "3.6.1+local", "h35e630c_1"},
	} {
		s, err := condaPinSpec(p[0], p[1], p[2])
		if err != nil {
			t.Errorf("%v: %v", p, err)
			continue
		}
		r := &record.PackageRecord{Name: p[0], Version: p[1], Build: p[2]}
		if !matches(s, r) {
			t.Errorf("%q does not match %v", s, p)
		}
	}

	// Anything that is not a plain name, version and build is refused, so
	// a specification cannot widen its pin.
	for _, p := range [][3]string{
		{"numpy", "1.26,>=0", ""},
		{"numpy", "1.26|2.0", ""},
		{"numpy", ">=1.26", ""},
		{"numpy", "1.26.*", ""},
		{"numpy", "1.26 *", ""},
		{"numpy", "", ""},
		{"numpy", "1.26", "py*"},
		{"numpy", "1.26", "a b"},
		{"numpy", "1.26", "[build=x]"},
		{"numpy", "1.26", "py311-0"},
		{"conda-forge::numpy", "1.26", ""},
		{"numpy[version='>=0']", "1.26", ""},
		{"", "1.26", ""},
		{"numpy", "1.26=x", ""},
		{"numpy", "1.26#x", ""},
	} {
		if s, err := condaPinSpec(p[0], p[1], p[2]); err == nil {
			t.Errorf("condaPinSpec(%q) = %q, want an error", p, s)
		}
	}

	// The parse-back proof decides on its own. condaPinSpec's character
	// check refuses everything above first, so checkExactPin is also given
	// specs that parse but are not an exact pin of the package asked for.
	for _, tc := range []struct {
		s, name, version, build string
		exact                   bool
	}{
		{"numpy==1.26=b_0", "numpy", "1.26", "b_0", true},
		{"numpy==1.26", "numpy", "1.26", "", true},
		{"NumPy==1.26=b_0", "NumPy", "1.26", "b_0", true},
		{"numpy=1.26=b_0", "numpy", "1.26", "b_0", false}, // conda's prefix match
		{"numpy=1.26", "numpy", "1.26", "", false},
		{"numpy>=1.26=b_0", "numpy", "1.26", "b_0", false},
		{"numpy==1.27=b_0", "numpy", "1.26", "b_0", false},
		{"numpy==1.26", "numpy", "1.26", "b_0", false},     // no build
		{"numpy==1.26=b_0", "numpy", "1.26", "", false},    // a build nobody asked for
		{"numpy==1.26=b_*", "numpy", "1.26", "b_*", false}, // a glob
		{"numpy==1.26=b_0", "numpy", "1.26", "b_1", false},
		{"scipy==1.26=b_0", "numpy", "1.26", "b_0", false},
		{"nump*==1.26=b_0", "nump*", "1.26", "b_0", false},
		{"conda-forge::numpy==1.26=b_0", "numpy", "1.26", "b_0", false},
		{"conda-forge/linux-64::numpy==1.26=b_0", "numpy", "1.26", "b_0", false},
		{"numpy==1.26=b_0[build_number=3]", "numpy", "1.26", "b_0", false},
		{"numpy==1.26=b_0[md5=" + strings.Repeat("a", 32) + "]", "numpy", "1.26", "b_0", false},
		{"numpy==1.26=b_0[license=MIT]", "numpy", "1.26", "b_0", false},
	} {
		err := checkExactPin(tc.s, tc.name, tc.version, tc.build)
		switch {
		case tc.exact && err != nil:
			t.Errorf("checkExactPin(%q) = %v, want an exact pin", tc.s, err)
		case !tc.exact && (err == nil || !strings.Contains(err.Error(), "does not pin it exactly")):
			t.Errorf("checkExactPin(%q, %q, %q, %q) = %v, want \"does not pin it exactly\"", tc.s, tc.name, tc.version, tc.build, err)
		}
	}
}

func TestRestoreCondaSpecs(t *testing.T) {
	// With Packages, only Packages: legacy CondaPackages are ignored, as
	// CreateEnvironmentFromJSONFileWithOptions ignores them.
	es := &EnvironmentSpec{
		Packages: []PackageSpec{
			{Name: "numpy", Version: "1.26.4", Build: "py311h64a7726_0", Source: "conda"},
			{Name: "requests", Version: "2.31.0", Source: "pip"},
			{Name: "packaging", Version: "26.0", Source: "conda"},
			{Name: "ignored", Version: "1"},
		},
		CondaPackages: []string{"scipy=1.11.4=py311h64a7726_0"},
	}
	got, err := restoreCondaSpecs(es)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"numpy==1.26.4=py311h64a7726_0", "packaging==26.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("specs = %q, want %q", got, want)
	}
	// Without Packages, the legacy CondaPackages pass through as given.
	legacy := &EnvironmentSpec{CondaPackages: []string{"scipy=1.11.4=py311h64a7726_0", "zlib"}}
	if got, err := restoreCondaSpecs(legacy); err != nil || !reflect.DeepEqual(got, legacy.CondaPackages) {
		t.Errorf("legacy specs = %q, %v", got, err)
	}
	es.Packages = append(es.Packages, PackageSpec{Name: "evil", Version: ">=0", Source: "conda"})
	if _, err := restoreCondaSpecs(es); err == nil {
		t.Error("a version range was accepted as a pin")
	}
}

func TestVerifyRestore(t *testing.T) {
	sum := func(c byte) string { return strings.Repeat(string(c), 64) }
	lf := &lock.Lockfile{Packages: []lock.Package{
		{Name: "libzlib", Version: "1.3.1", Build: "h_2", SHA256: sum('d')},
		{Name: "numpy", Version: "1.26.0", Build: "py311_0", SHA256: sum('a')},
		{Name: "python", Version: "3.11.15", Build: "h_0", SHA256: sum('b')},
	}}
	pkg := func(name, version, build, sha string) PackageSpec {
		return PackageSpec{Name: name, Version: version, Build: build, Source: "conda", SHA256: sha}
	}
	numpy, python, zlib := pkg("numpy", "1.26.0", "py311_0", sum('a')), pkg("python", "3.11.15", "h_0", sum('b')), pkg("libzlib", "1.3.1", "h_2", sum('d'))
	verify := RestoreOptions{VerifyChecksums: true}
	strict := RestoreOptions{VerifyChecksums: true, Strict: true}
	for _, tc := range []struct {
		name     string
		pkgs     []PackageSpec
		opts     RestoreOptions
		mismatch bool   // the error wraps ErrChecksumMismatch
		fail     string // a part of the error, or "" for none
	}{
		{"everything listed", []PackageSpec{numpy, python, zlib}, strict, false, ""},
		{"hex case", []PackageSpec{pkg("numpy", "1.26.0", "py311_0", strings.ToUpper(sum('a')))}, verify, false, ""},
		{"name case", []PackageSpec{pkg("NumPy", "1.26.0", "py311_0", sum('a'))}, verify, false, ""},
		{"name case mismatch", []PackageSpec{pkg("NumPy", "1.26.0", "py311_0", sum('c'))}, verify, true, "checksum mismatch"},
		{"no build pinned", []PackageSpec{pkg("numpy", "1.26.0", "", sum('a'))}, verify, false, ""},
		{"mismatch", []PackageSpec{pkg("numpy", "1.26.0", "py311_0", sum('c'))}, verify, true, "checksum mismatch"},
		{"mismatch strict", []PackageSpec{zlib, numpy, pkg("python", "3.11.15", "h_0", sum('a'))}, strict, true, "checksum mismatch"},
		{"not verifying", []PackageSpec{pkg("numpy", "1.26.0", "py311_0", sum('c'))}, RestoreOptions{}, false, ""},
		{"strict without verify", []PackageSpec{pkg("numpy", "1.26.0", "py311_0", sum('c'))}, RestoreOptions{Strict: true}, false, ""},
		{"no checksum", []PackageSpec{pkg("numpy", "1.26.0", "py311_0", "")}, verify, false, ""},
		{"not solved", []PackageSpec{pkg("scipy", "1", "0", sum('a'))}, verify, false, ""},
		{"not solved strict", []PackageSpec{numpy, python, zlib, pkg("scipy", "1", "0", sum('a'))}, strict, false, `conda package "scipy" is not in the solution`},
		{"pip ignored", []PackageSpec{numpy, python, zlib, {Name: "numpy", Version: "1", Source: "pip", SHA256: sum('c')}}, strict, false, ""},
		// Versions and builds compare as strings, whatever the options:
		// conda's version equality pads zeros and ignores case.
		{"padded version", []PackageSpec{pkg("numpy", "1.26", "py311_0", "")}, RestoreOptions{}, false, `pins version "1.26"`},
		{"other build", []PackageSpec{pkg("python", "3.11.15", "h_1", "")}, RestoreOptions{}, false, `build "h_1"`},
		// Strict: every solved package must be listed with a checksum.
		{"unlisted dependency", []PackageSpec{numpy, python}, strict, false, "libzlib-1.3.1-h_2 is in the solution"},
		{"listed without checksum", []PackageSpec{numpy, python, pkg("libzlib", "1.3.1", "h_2", "")}, strict, false, "libzlib-1.3.1-h_2 is in the solution"},
		{"no packages", nil, strict, false, "libzlib-1.3.1-h_2 is in the solution"},
	} {
		err := verifyRestore(&EnvironmentSpec{Packages: tc.pkgs}, lf, tc.opts)
		if (err != nil) != (tc.fail != "") || (err != nil && !strings.Contains(err.Error(), tc.fail)) || errors.Is(err, ErrChecksumMismatch) != tc.mismatch {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.fail)
		}
	}
	// The case that made Strict meaningful: an unchecked legacy package.
	es := &EnvironmentSpec{
		Packages:      []PackageSpec{pkg("python", "3.11.9", "h_0", sum('a'))},
		CondaPackages: []string{"evilpkg"},
	}
	if specs, _ := restoreCondaSpecs(es); !reflect.DeepEqual(specs, []string{"python==3.11.9=h_0"}) {
		t.Errorf("legacy CondaPackages reach the solve beside Packages: %q", specs)
	}
	evil := &lock.Lockfile{Packages: []lock.Package{
		{Name: "evilpkg", Version: "6.6.6", Build: "0", SHA256: sum('e')},
		{Name: "python", Version: "3.11.9", Build: "h_0", SHA256: sum('a')},
	}}
	if err := verifyRestore(es, evil, strict); err == nil || !strings.Contains(err.Error(), "evilpkg-6.6.6-0") {
		t.Errorf("strict accepted an unverified package: %v", err)
	}
}

func TestMicromambaInstallPackageOnGoombaEnvironment(t *testing.T) {
	env := &PythonEnvironment{BaseEnvironment: BaseEnvironment{EnvPath: t.TempDir(), Backend: BackendGoomba}}
	err := env.MicromambaInstallPackage("numpy", "conda-forge")
	if !errors.Is(err, ErrBackendUnsupported) || !strings.Contains(err.Error(), "CreateEnvironmentFromJSONFileGoomba") {
		t.Fatalf("err = %v", err)
	}
}

// writeJSON writes v as JSON to path.
func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readSpec(t *testing.T, path string) EnvironmentSpec {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var es EnvironmentSpec
	if err := json.Unmarshal(data, &es); err != nil {
		t.Fatal(err)
	}
	return es
}

// fakeGoombaFreezeEnv lays out a fake environment root/envs/name with
// conda-meta records and a pip that answers freeze with pipFreeze.
func fakeGoombaFreezeEnv(t *testing.T, root, name string, recs []map[string]any, pipFreeze ...string) string {
	t.Helper()
	fakeCondaEnv(t, root, name, "3.11.15", "26.0.1")
	envPath := filepath.Join(root, "envs", name)
	for _, r := range recs {
		writeJSON(t, filepath.Join(envPath, "conda-meta", r["name"].(string)+"-"+r["version"].(string)+"-"+r["build"].(string)+".json"), r)
	}
	pipScript := "#!/bin/sh\n" +
		"if [ \"$1\" = --version ]; then echo 'pip 26.0.1 from x (python 3.11)'; exit 0; fi\n" +
		"printf '%s\\n'"
	for _, l := range pipFreeze {
		pipScript += " '" + l + "'"
	}
	if err := os.WriteFile(filepath.Join(envPath, "bin", "pip"), []byte(pipScript+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return envPath
}

// condaMetaRecord returns a conda-meta record of a package from a channel
// URL, with the given sha256.
func condaMetaRecord(channel, subdir, name, version, build, sha string) map[string]any {
	return map[string]any{
		"name": name, "version": version, "build": build, "build_number": 0, "subdir": subdir,
		"sha256": sha, "depends": []string{}, "files": []string{},
		"fn": name + "-" + version + "-" + build + ".conda", "channel": channel,
		"url": channel + "/" + subdir + "/" + name + "-" + version + "-" + build + ".conda",
	}
}

// withoutKeys returns m without the keys.
func withoutKeys(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		delete(m, k)
	}
	return m
}

const cf = "https://conda.anaconda.org/conda-forge"

func TestFreezeGoombaCondaMeta(t *testing.T) {
	root := t.TempDir()
	pySum, pipSum, pkSum, barSum := strings.Repeat("ab", 32), strings.Repeat("cd", 32), strings.Repeat("ef", 32), strings.Repeat("01", 32)
	fakeGoombaFreezeEnv(t, root, "base-3.11", []map[string]any{
		condaMetaRecord(cf, "linux-64", "python", "3.11.15", "hd63d673_0_cpython", pySum),
		condaMetaRecord(cf, "noarch", "pip", "26.0.1", "pyh8b19718_0", pipSum),
		condaMetaRecord(cf, "noarch", "packaging", "26.0", "pyhcf101f3_0", pkSum),
		// A record from another channel, with an upper-case digest and
		// neither fn nor channel.
		withoutKeys(condaMetaRecord("https://example.org/chan", "linux-64", "bar", "2", "h_0", strings.ToUpper(barSum)), "fn", "channel"),
		// Records lock.FromPrefix refuses: a file:// URL, no sha256.
		condaMetaRecord("file:///srv/chan", "linux-64", "foo", "1", "0", strings.Repeat("23", 32)),
		condaMetaRecord(cf, "linux-64", "baz", "3", "0", ""),
		condaMetaRecord(cf, "linux-64", "qux", "4", "0", "not-a-digest"),
	},
		// pip freeze: a pip package, conda packages (one by file URL), an
		// editable install and a mixed-case name.
		"requests==2.31.0", "packaging==26.0", "pip @ file:///home/conda/feedstock_root/build_artifacts/pip_1/work",
		"-e git+https://example.org/x.git#egg=x", "Foo_Bar==1.0")

	env, err := CreateEnvironmentGoomba("base-3.11", root, "3.11", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "env.json")
	if err := env.FreezeToFile(out); err != nil {
		t.Fatal(err)
	}
	es := readSpec(t, out)
	want := EnvironmentSpec{
		Name:          "base-3.11",
		PythonVersion: "3.11",
		// Legacy fields, as the micromamba freeze fills them: what pip
		// lists (packaging, pip) is left out, and so are its channels.
		CondaPackages: []string{"bar=2=h_0", "baz=3=0", "foo=1=0", "python=3.11.15=hd63d673_0_cpython", "qux=4=0"},
		Channels:      []string{"https://example.org/chan", "conda-forge", "file:///srv/chan"},
		// The freeze drops what follows '#' in a pip line, as it always has.
		PipPackages: []string{"requests==2.31.0", "packaging==26.0", "pip", "-e git+https://example.org/x.git", "Foo_Bar==1.0"},
		// Packages: every conda package, with its digest when it has one,
		// and every name==version pip package.
		Packages: []PackageSpec{
			{Name: "bar", Version: "2", Build: "h_0", SHA256: barSum, Source: "conda"},
			{Name: "baz", Version: "3", Build: "0", Source: "conda"},
			{Name: "foo", Version: "1", Build: "0", SHA256: strings.Repeat("23", 32), Source: "conda"},
			{Name: "packaging", Version: "26.0", Build: "pyhcf101f3_0", SHA256: pkSum, Source: "conda"},
			{Name: "pip", Version: "26.0.1", Build: "pyh8b19718_0", SHA256: pipSum, Source: "conda"},
			{Name: "python", Version: "3.11.15", Build: "hd63d673_0_cpython", SHA256: pySum, Source: "conda"},
			{Name: "qux", Version: "4", Build: "0", Source: "conda"},
			{Name: "requests", Version: "2.31.0", Source: "pip"},
			{Name: "packaging", Version: "26.0", Source: "pip"},
			{Name: "Foo_Bar", Version: "1.0", Source: "pip"},
		},
	}
	if !reflect.DeepEqual(es, want) {
		t.Errorf("frozen:\n got %+v\nwant %+v", es, want)
	}

	// Without pip, every conda package is in both forms.
	env.PipPath = ""
	if err := env.FreezeToFile(out); err != nil {
		t.Fatal(err)
	}
	es = readSpec(t, out)
	if len(es.Packages) != 7 || len(es.CondaPackages) != 7 || len(es.PipPackages) != 0 {
		t.Errorf("frozen without pip: %+v", es)
	}
}

// TestGoombaFreezeThroughAllRestores restores a goomba freeze with the
// three restores (the micromamba ones with a logging fake micromamba and
// pip) and checks that each one installs every conda and pip package.
func TestGoombaFreezeThroughAllRestores(t *testing.T) {
	noMicromambaDownload(t)
	src := t.TempDir()
	recs := []map[string]any{
		condaMetaRecord(cf, "linux-64", "python", "3.11.15", "hd63d673_0_cpython", strings.Repeat("ab", 32)),
		condaMetaRecord(cf, "linux-64", "numpy", "2.1.0", "py311_0", strings.Repeat("cd", 32)),
		condaMetaRecord(cf, "noarch", "packaging", "26.0", "pyhcf101f3_0", strings.Repeat("ef", 32)),
	}
	fakeGoombaFreezeEnv(t, src, "e", recs, "requests==2.31.0", "packaging==26.0")
	env, err := CreateEnvironmentGoomba("e", src, "3.11", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	frozen := filepath.Join(t.TempDir(), "env.json")
	if err := env.FreezeToFile(frozen); err != nil {
		t.Fatal(err)
	}

	// restoreLog restores frozen with restore into a fake environment
	// whose micromamba and pip log their calls, and returns the calls.
	restoreLog := func(restore func(root string) error) string {
		root := t.TempDir()
		fakeGoombaFreezeEnv(t, root, "e", nil)
		logf := filepath.Join(root, "calls.log")
		writeShell := func(p, body string) {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		writeShell(filepath.Join(root, "bin", "micromamba"), "case \"$*\" in *--version*) echo 2.2.0; exit 0;; esac\necho \"micromamba $*\" >> "+logf+"\n")
		writeShell(filepath.Join(root, "envs", "e", "bin", "pip"), "if [ \"$1\" = --version ]; then echo 'pip 26.0.1 from x (python 3.11)'; exit 0; fi\necho \"pip $*\" >> "+logf+"\n")
		if out, err := RunReadStdout(filepath.Join(root, "bin", "micromamba"), "--version"); err != nil || strings.TrimSpace(out) != "2.2.0" {
			t.Fatalf("the fake micromamba does not run (%q, %v)", out, err)
		}
		if err := restore(root); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(logf)
		return string(data)
	}
	wantCalls := func(name, calls string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(calls, w) {
				t.Errorf("%s restore: no %q in\n%s", name, w, calls)
			}
		}
	}

	legacy := restoreLog(func(root string) error {
		_, err := CreateEnvironmentFromJSONFile(frozen, root, nil)
		return err
	})
	wantCalls("legacy", legacy, " numpy=2.1.0=py311_0", " python=3.11.15=hd63d673_0_cpython", "requests==2.31.0", "packaging==26.0")

	withOptions := restoreLog(func(root string) error {
		_, err := CreateEnvironmentFromJSONFileWithOptions(frozen, root, RestoreOptions{}, nil)
		return err
	})
	wantCalls("WithOptions", withOptions, " numpy=2.1.0=py311_0", " python=3.11.15=hd63d673_0_cpython", " packaging=26.0=pyhcf101f3_0", "requests==2.31.0", "packaging==26.0")

	// The goomba restore: every conda package pinned in the one solve, then
	// the pip packages.
	es := readSpec(t, frozen)
	specs, err := restoreCondaSpecs(&es)
	if want := []string{"numpy==2.1.0=py311_0", "packaging==26.0=pyhcf101f3_0", "python==3.11.15=hd63d673_0_cpython"}; err != nil || !reflect.DeepEqual(specs, want) {
		t.Errorf("goomba restore specs = %q, %v; want %q", specs, err, want)
	}
	pipCalls := restoreLog(func(root string) error {
		e := &PythonEnvironment{PipPath: filepath.Join(root, "envs", "e", "bin", "pip")}
		return restorePipPackages(e, &es, nil)
	})
	wantCalls("goomba", pipCalls, "requests==2.31.0", "packaging==26.0")
}

func TestGoombaRestoreStrictNeedsChecksums(t *testing.T) {
	strict := RestoreOptions{VerifyChecksums: true, Strict: true}
	for _, tc := range []struct {
		name string
		es   EnvironmentSpec
		want string
	}{
		{"missing checksum", EnvironmentSpec{Name: "strict", PythonVersion: "3.11",
			Packages: []PackageSpec{{Name: "numpy", Version: "1.26.4", Source: "conda"}}}, "lacks SHA256 checksum"},
		{"pip package without checksum", EnvironmentSpec{Name: "strict", PythonVersion: "3.11",
			Packages: []PackageSpec{{Name: "numpy", Version: "1.26.4", Source: "conda", SHA256: strings.Repeat("a", 64)}, {Name: "requests", Version: "2.31.0", Source: "pip"}}}, "lacks SHA256 checksum"},
		{"legacy lists only", EnvironmentSpec{Name: "strict", PythonVersion: "3.11",
			CondaPackages: []string{"numpy=1.26.4=py311_0"}}, "lists no packages"},
	} {
		specFile := filepath.Join(t.TempDir(), "env.json")
		writeJSON(t, specFile, tc.es)
		root := filepath.Join(t.TempDir(), "root")
		_, err := CreateEnvironmentFromJSONFileGoomba(context.Background(), specFile, root, strict, GoombaOptions{}, nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Errorf("%s: the root directory was created", tc.name)
		}
	}
	root := filepath.Join(t.TempDir(), "root")
	for _, bad := range []string{filepath.Join(t.TempDir(), "missing.json"), writeFile(t, "not json")} {
		if _, err := CreateEnvironmentFromJSONFileGoomba(context.Background(), bad, root, RestoreOptions{}, GoombaOptions{}, nil); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

// writeFile writes data to a new temporary file and returns its path.
func writeFile(t *testing.T, data string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGoombaProgress(t *testing.T) {
	type call struct {
		msg            string
		current, total int64
	}
	var calls []call
	p := &goombaProgress{cb: func(msg string, current, total int64) { calls = append(calls, call{msg, current, total}) }}
	p.lockEvent(goomba.Event{Kind: goomba.EventRepodata, Channel: "https://conda.anaconda.org/conda-forge", Done: 1, Total: 1})
	p.lockEvent(goomba.Event{Kind: goomba.EventSolving, Prefilter: "3.11", Done: 1, Total: 2})
	p.lockEvent(goomba.Event{Kind: goomba.EventSolved, Done: 2, Total: 2})
	p.setLockfile(&lock.Lockfile{Packages: []lock.Package{
		{Name: "a", Version: "1", Build: "0", Size: 100},
		{Name: "b", Version: "2", Build: "0", Size: 50},
	}})
	p.installEvent(goomba.Event{Kind: goomba.EventFetched, Package: "b-2-0", Done: 1, Total: 2})
	p.installEvent(goomba.Event{Kind: goomba.EventFetched, Package: "a-1-0", Done: 2, Total: 2})
	// Linked events may arrive out of order; the count is the jumpboot's.
	p.installEvent(goomba.Event{Kind: goomba.EventLinked, Package: "a-1-0", Done: 2, Total: 2})
	p.installEvent(goomba.Event{Kind: goomba.EventLinked, Package: "b-2-0", Done: 1, Total: 2})
	p.installEvent(goomba.Event{Kind: goomba.EventCompiling, Total: 10})
	p.installEvent(goomba.Event{Kind: goomba.EventCompiled, Done: 9, Total: 10})
	p.installEvent(goomba.Event{Kind: goomba.EventDone, Done: 2, Total: 2}) // not reported
	want := []call{
		{"Loading channel index", 1, 1},
		{"Solving environment", 0, -1},
		{"Solving environment", 2, 2},
		{"Fetching packages", 50, 150},
		{"Fetching packages", 150, 150},
		{"Linking packages", 1, 2},
		{"Linking packages", 2, 2},
		{"Compiling Python files", 0, 10},
		{"Compiling Python files", 9, 10},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls:\n got %v\nwant %v", calls, want)
	}

	// An unknown size makes the total unknown.
	calls = nil
	p.setLockfile(&lock.Lockfile{Packages: []lock.Package{{Name: "a", Version: "1", Build: "0", Size: 100}, {Name: "b", Version: "2", Build: "0"}}})
	p.installEvent(goomba.Event{Kind: goomba.EventFetched, Package: "a-1-0", Done: 1, Total: 2})
	p.installEvent(goomba.Event{Kind: goomba.EventFetched, Package: "b-2-0", Done: 2, Total: 2})
	if want := []call{{"Fetching packages", 100, -1}, {"Fetching packages", 100, -1}}; !reflect.DeepEqual(calls, want) {
		t.Errorf("calls with an unknown size: %v", calls)
	}

	// A nil callback is fine.
	(&goombaProgress{}).installEvent(goomba.Event{Kind: goomba.EventCompiling, Total: 1})
}

func TestGoombaUnsafeCaches(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("goomba checks cache ownership on Unix only")
	}
	// Nothing listens on port 1 of the loopback address: if goomba got as
	// far as the network, the error would not be the cache's.
	opts := GoombaOptions{PythonVersion: "3.11", Channels: []string{"https://127.0.0.1:1/none"}, Timeout: time.Minute}
	for _, tc := range []struct {
		dir  string
		want error
	}{
		{"pkgs", fetch.ErrUnsafeCache},
		{"repodata", repodata.ErrUnsafeCache},
	} {
		root := t.TempDir()
		for _, d := range []string{"pkgs", "repodata"} {
			if err := os.MkdirAll(filepath.Join(root, "goomba", d), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chmod(filepath.Join(root, "goomba", tc.dir), 0o777); err != nil {
			t.Fatal(err)
		}
		_, err := CreateEnvironmentGoombaWithOptions(context.Background(), "e", root, opts, nil)
		if !errors.Is(err, tc.want) {
			t.Errorf("goomba/%s writable by others: err = %v, want %v", tc.dir, err, tc.want)
		}
		if _, err := os.Lstat(filepath.Join(root, "envs", "e")); !os.IsNotExist(err) {
			t.Errorf("goomba/%s: an environment was left (%v)", tc.dir, err)
		}
	}
}

func TestGoombaRefusesWindows(t *testing.T) {
	orig := goombaHostOS
	goombaHostOS = "windows"
	t.Cleanup(func() { goombaHostOS = orig })
	root := filepath.Join(t.TempDir(), "root")
	ctx := context.Background()
	_, err1 := CreateEnvironmentGoomba("e", root, "3.11", "", nil)
	_, err2 := CreateEnvironmentGoombaWithOptions(ctx, "e", root, GoombaOptions{}, nil)
	// The JSON file does not exist: it must not even be read.
	_, err3 := CreateEnvironmentFromJSONFileGoomba(ctx, filepath.Join(root, "missing.json"), root, RestoreOptions{}, GoombaOptions{}, nil)
	for i, err := range []error{err1, err2, err3} {
		if !errors.Is(err, ErrBackendUnsupported) || !strings.Contains(err.Error(), "Windows") {
			t.Errorf("call %d: err = %v, want ErrBackendUnsupported", i+1, err)
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Error("the root directory was created")
	}
}

// hangingChannel serves a TLS channel whose every request hangs until the
// client gives up.
func hangingChannel(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Minute):
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGoombaTimeout(t *testing.T) {
	srv := hangingChannel(t)
	opts := GoombaOptions{PythonVersion: "3.11", Channels: []string{srv.URL + "/hang"}, Client: srv.Client(), Timeout: 300 * time.Millisecond}
	start := time.Now()
	_, err := CreateEnvironmentGoombaWithOptions(context.Background(), "e", t.TempDir(), opts, nil)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out after 300ms (GoombaOptions.Timeout)") {
		t.Errorf("Timeout: err = %v", err)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("Timeout of 300ms took %v", d)
	}
	// The caller's deadline is not blamed on GoombaOptions.Timeout.
	opts.Timeout = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = CreateEnvironmentGoombaWithOptions(ctx, "e", t.TempDir(), opts, nil)
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "GoombaOptions.Timeout") {
		t.Errorf("caller deadline: err = %v", err)
	}
}

func TestGoombaProgressSerialised(t *testing.T) {
	var inFlight, overlaps, calls atomic.Int64
	p := &goombaProgress{cb: func(string, int64, int64) {
		if inFlight.Add(1) > 1 {
			overlaps.Add(1)
		}
		calls.Add(1)
		time.Sleep(50 * time.Microsecond)
		inFlight.Add(-1)
	}}
	p.setLockfile(&lock.Lockfile{})
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				p.installEvent(goomba.Event{Kind: goomba.EventLinked, Total: 320})
				p.installEvent(goomba.Event{Kind: goomba.EventFetched, Package: "x", Total: 320})
			}
		}()
	}
	wg.Wait()
	if overlaps.Load() != 0 || calls.Load() != 640 || p.linked != 320 {
		t.Errorf("%d overlapping calls of %d, %d linked", overlaps.Load(), calls.Load(), p.linked)
	}

	// Nothing is reported once the context is done.
	calls.Store(0)
	ctx, cancel := context.WithCancel(context.Background())
	p.setContext(ctx)
	p.installEvent(goomba.Event{Kind: goomba.EventLinked, Total: 1})
	cancel()
	p.installEvent(goomba.Event{Kind: goomba.EventLinked, Total: 1})
	p.lockEvent(goomba.Event{Kind: goomba.EventSolving})
	p.say("Python environment created successfully", 100, 100)
	if calls.Load() != 1 {
		t.Errorf("%d calls, want 1 before the context was done", calls.Load())
	}
}

func TestRestorePythonVersion(t *testing.T) {
	py := func(v string) PackageSpec {
		return PackageSpec{Name: "python", Version: v, Build: "h_0", Source: "conda"}
	}
	for _, tc := range []struct {
		es       EnvironmentSpec
		fallback string
		want     string
	}{
		{EnvironmentSpec{PythonVersion: "3.12", Packages: []PackageSpec{py("3.11.9")}}, "3.10", "3.12"},
		{EnvironmentSpec{Packages: []PackageSpec{py("3.11.9")}}, "", "3.11"},
		{EnvironmentSpec{Packages: []PackageSpec{py("3.11.9")}}, "3.10", "3.11"},
		{EnvironmentSpec{Packages: []PackageSpec{{Name: "Python", Version: "3.13.0rc1", Source: "conda"}}}, "", "3.13"},
		{EnvironmentSpec{Packages: []PackageSpec{{Name: "python", Version: "3.11.9", Source: "pip"}}}, "3.10", "3.10"},
		{EnvironmentSpec{Packages: []PackageSpec{py("3")}}, "3.9", "3.9"},
		{EnvironmentSpec{CondaPackages: []string{"python=3.11"}}, "", ""},
	} {
		if got := restorePythonVersion(&tc.es, tc.fallback); got != tc.want {
			t.Errorf("restorePythonVersion(%+v, %q) = %q, want %q", tc.es, tc.fallback, got, tc.want)
		}
	}
	// What the solve then asks for: no python=3.10 beside the pin.
	es := &EnvironmentSpec{Packages: []PackageSpec{py("3.11.9")}}
	specs, _ := restoreCondaSpecs(es)
	opts := GoombaOptions{PythonVersion: restorePythonVersion(es, "")}.withDefaults()
	if got := goombaSpecs(opts.PythonVersion, specs); !reflect.DeepEqual(got, []string{"python=3.11", "python==3.11.9=h_0", "pip"}) {
		t.Errorf("specs = %q", got)
	}
}

// loggingPip returns an environment whose pip appends its arguments to a
// log, and the log's path.
func loggingPip(t *testing.T) (*PythonEnvironment, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake pip is POSIX-only")
	}
	dir := t.TempDir()
	logf := filepath.Join(dir, "pip.log")
	pip := filepath.Join(dir, "pip")
	if err := os.WriteFile(pip, []byte("#!/bin/sh\necho \"$*\" >> "+logf+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &PythonEnvironment{PipPath: pip}, logf
}

func TestRestorePipPackages(t *testing.T) {
	read := func(p string) string { data, _ := os.ReadFile(p); return string(data) }
	// With Packages: its pip packages one at a time; legacy PipPackages are
	// ignored, as CreateEnvironmentFromJSONFileWithOptions ignores them.
	env, logf := loggingPip(t)
	es := &EnvironmentSpec{
		Packages: []PackageSpec{
			{Name: "numpy", Version: "1.26.4", Source: "conda"},
			{Name: "requests", Version: "2.31.0", Source: "pip"},
			{Name: "idna", Version: "3.7", Source: "pip"},
		},
		PipPackages: []string{"legacy==1.0"},
	}
	if err := restorePipPackages(env, es, nil); err != nil {
		t.Fatal(err)
	}
	got := read(logf)
	if strings.Count(got, "install ") != 2 || !strings.Contains(got, " requests==2.31.0 ") || !strings.Contains(got, " idna==3.7 ") ||
		strings.Contains(got, "legacy") || strings.Contains(got, "numpy") {
		t.Errorf("pip calls with Packages:\n%s", got)
	}
	// Without Packages: the legacy list, in one call.
	env, logf = loggingPip(t)
	if err := restorePipPackages(env, &EnvironmentSpec{PipPackages: []string{"a==1", "b==2"}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := read(logf); strings.Count(got, "install ") != 1 || !strings.Contains(got, " a==1 b==2 ") {
		t.Errorf("pip calls with legacy PipPackages:\n%s", got)
	}
}

func TestRemoveCreatedEnv(t *testing.T) {
	cause := errors.New("pip failed")
	newEnv := func() (string, os.FileInfo) {
		p := filepath.Join(t.TempDir(), "envs", "e")
		if err := os.MkdirAll(filepath.Join(p, "conda-meta"), 0o755); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		return p, fi
	}

	p, fi := newEnv()
	err := removeCreatedEnv(p, fi, cause)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "was removed") {
		t.Errorf("removed: err = %v", err)
	}
	if _, serr := os.Lstat(p); !os.IsNotExist(serr) {
		t.Errorf("the environment is still there (%v)", serr)
	}

	// A directory that replaced the one created is not removed.
	p, fi = newEnv()
	if err := os.Rename(p, p+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	err = removeCreatedEnv(p, fi, cause)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "left behind at "+p) || !strings.Contains(err.Error(), "replaced") {
		t.Errorf("replaced: err = %v", err)
	}
	if _, serr := os.Lstat(p); serr != nil {
		t.Errorf("the replacement was removed (%v)", serr)
	}

	// A removal that fails says where the environment was left.
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		p, fi = newEnv()
		locked := filepath.Join(p, "locked")
		if err := os.MkdirAll(filepath.Join(locked, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o755) })
		err = removeCreatedEnv(p, fi, cause)
		if !errors.Is(err, cause) || !strings.Contains(err.Error(), "left behind at "+p) || !strings.Contains(err.Error(), "removing it failed") {
			t.Errorf("removal failed: err = %v", err)
		}
	}

	// One that is gone already.
	p, fi = newEnv()
	if err := os.RemoveAll(p); err != nil {
		t.Fatal(err)
	}
	if err := removeCreatedEnv(p, fi, cause); !errors.Is(err, cause) || !strings.Contains(err.Error(), "no longer exists") {
		t.Errorf("gone: err = %v", err)
	}
}

// TestGoombaEnvironmentStatus checks what the goomba functions do with an
// existing rootDir/envs/NAME that is not a finished environment. Nothing
// listens on the channel's port: a request that got as far as the network
// fails there.
func TestGoombaEnvironmentStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the goomba functions refuse Windows")
	}
	opts := GoombaOptions{PythonVersion: "3.11", Channels: []string{"https://127.0.0.1:1/none"}, Timeout: time.Minute}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, env string)
		check func(error) bool
		want  string
	}{
		{"not an environment", func(t *testing.T, env string) {
			mkdir(t, env)
			writeFileAt(t, filepath.Join(env, "data.txt"))
		}, func(err error) bool { return errors.Is(err, goomba.ErrPrefixNotEmpty) }, "goomba.ErrPrefixNotEmpty"},
		{"conda-meta without history", func(t *testing.T, env string) {
			mkdir(t, filepath.Join(env, "conda-meta"))
			writeFileAt(t, filepath.Join(env, "conda-meta", "x-1-0.json"))
		}, func(err error) bool { return errors.Is(err, goomba.ErrPrefixNotEmpty) }, "goomba.ErrPrefixNotEmpty"},
		{"only an empty conda-meta", func(t *testing.T, env string) {
			mkdir(t, filepath.Join(env, "conda-meta"))
		}, func(err error) bool {
			return errors.Is(err, ErrIncompleteEnvironment) && errors.Is(err, goomba.ErrIncomplete)
		}, "ErrIncompleteEnvironment"},
		{"writable by others", func(t *testing.T, env string) {
			fakeCondaEnv(t, filepath.Dir(filepath.Dir(env)), filepath.Base(env), "3.11.9", "24.0")
			if err := os.Chmod(env, 0o777); err != nil {
				t.Fatal(err)
			}
		}, func(err error) bool { return errors.Is(err, goomba.ErrUnsafePrefix) }, "goomba.ErrUnsafePrefix"},
		{"a file", func(t *testing.T, env string) {
			mkdir(t, filepath.Dir(env))
			writeFileAt(t, env)
		}, func(err error) bool { return err != nil && strings.Contains(err.Error(), "error checking environment") }, "an error checking the environment"},
		{"an empty directory", func(t *testing.T, env string) {
			mkdir(t, env)
		}, func(err error) bool {
			// Created, not reused: the request reaches the (missing) channel.
			return err != nil && strings.Contains(err.Error(), "127.0.0.1:1") && !errors.Is(err, goomba.ErrPrefixNotEmpty)
		}, "a creation reaching the channel"},
	} {
		root := t.TempDir()
		env := filepath.Join(root, "envs", "e")
		tc.setup(t, env)
		_, err := CreateEnvironmentGoombaWithOptions(context.Background(), "e", root, opts, nil)
		if !tc.check(err) {
			t.Errorf("%s: err = %v, want %s", tc.name, err, tc.want)
		}
		if tc.name == "an empty directory" {
			if names, err := os.ReadDir(env); err != nil || len(names) != 0 {
				t.Errorf("%s: the directory holds %v (%v) after the failed creation", tc.name, names, err)
			}
		}
	}
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFileAt(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// interruptedEnv lays out root/envs/name as a goomba installation killed
// halfway leaves it: some files and the incomplete marker, no lock held.
func interruptedEnv(t *testing.T, root, name string) string {
	t.Helper()
	env := filepath.Join(root, "envs", name)
	mkdir(t, filepath.Join(env, "conda-meta"))
	mkdir(t, filepath.Join(env, "lib", "python3.11"))
	writeFileAt(t, filepath.Join(env, "lib", "python3.11", "os.py"))
	writeFileAt(t, filepath.Join(env, "conda-meta", ".goomba-incomplete"))
	return env
}

func TestRemoveIncompleteEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("goomba cannot remove an interrupted prefix without flock")
	}
	root := t.TempDir()
	env := interruptedEnv(t, root, "half")
	if err := RemoveIncompleteEnvironment("half", root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(env); !os.IsNotExist(err) {
		t.Errorf("the interrupted environment is still there (%v)", err)
	}
	// Nothing left to remove.
	if err := RemoveIncompleteEnvironment("half", root); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("again: err = %v, want fs.ErrNotExist", err)
	}
	// A finished environment is not removed.
	fakeCondaEnv(t, root, "done", "3.11.9", "24.0")
	if err := RemoveIncompleteEnvironment("done", root); err == nil {
		t.Error("a finished environment was removed")
	}
	if _, err := os.Stat(filepath.Join(root, "envs", "done", "bin", "python")); err != nil {
		t.Errorf("the finished environment was touched: %v", err)
	}
	for _, name := range []string{"", "..", "../half", "a/b"} {
		if err := RemoveIncompleteEnvironment(name, root); err == nil || !strings.Contains(err.Error(), "invalid environment name") {
			t.Errorf("name %q: err = %v", name, err)
		}
	}
	orig := goombaHostOS
	goombaHostOS = "windows"
	defer func() { goombaHostOS = orig }()
	interruptedEnv(t, root, "win")
	if err := RemoveIncompleteEnvironment("win", root); !errors.Is(err, ErrBackendUnsupported) {
		t.Errorf("windows: err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "envs", "win", "conda-meta", ".goomba-incomplete")); err != nil {
		t.Errorf("windows: the environment was touched: %v", err)
	}
}
