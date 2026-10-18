package jumpboot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/richinsley/goomba"
	"github.com/richinsley/goomba/lock"
	"github.com/richinsley/goomba/record"
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
	if !errors.Is(err, ErrIncompleteEnvironment) {
		t.Fatalf("err = %v, want ErrIncompleteEnvironment", err)
	}
	specFile := filepath.Join(t.TempDir(), "env.json")
	writeJSON(t, specFile, EnvironmentSpec{Name: "half", PythonVersion: "3.11"})
	if _, err := CreateEnvironmentFromJSONFileGoomba(context.Background(), specFile, root, RestoreOptions{}, GoombaOptions{}, nil); !errors.Is(err, ErrIncompleteEnvironment) {
		t.Fatalf("restore: err = %v, want ErrIncompleteEnvironment", err)
	}
	env := &PythonEnvironment{BaseEnvironment: BaseEnvironment{EnvironmentName: "half", RootDir: root, EnvPath: filepath.Join(root, "envs", "half"), Backend: BackendGoomba}}
	if err := env.FreezeToFile(filepath.Join(t.TempDir(), "out.json")); !errors.Is(err, ErrIncompleteEnvironment) {
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
}

func TestRestoreCondaSpecs(t *testing.T) {
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
	want := []string{"numpy==1.26.4=py311h64a7726_0", "packaging==26.0", "scipy=1.11.4=py311h64a7726_0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("specs = %q, want %q", got, want)
	}
	es.Packages = append(es.Packages, PackageSpec{Name: "evil", Version: ">=0", Source: "conda"})
	if _, err := restoreCondaSpecs(es); err == nil {
		t.Error("a version range was accepted as a pin")
	}
}

func TestVerifyRestoreChecksums(t *testing.T) {
	sum := func(c byte) string { return strings.Repeat(string(c), 64) }
	lf := &lock.Lockfile{Packages: []lock.Package{
		{Name: "numpy", Version: "1.26.4", Build: "py311_0", SHA256: sum('a')},
		{Name: "python", Version: "3.11.15", Build: "h_0", SHA256: sum('b')},
	}}
	conda := func(name, sha string) PackageSpec {
		return PackageSpec{Name: name, Version: "1", Source: "conda", SHA256: sha}
	}
	verify := RestoreOptions{VerifyChecksums: true}
	strict := RestoreOptions{VerifyChecksums: true, Strict: true}
	for _, tc := range []struct {
		name     string
		pkgs     []PackageSpec
		opts     RestoreOptions
		mismatch bool // the error wraps ErrChecksumMismatch
		fail     bool
	}{
		{"all match", []PackageSpec{conda("numpy", sum('a')), conda("python", sum('b'))}, strict, false, false},
		{"hex case", []PackageSpec{conda("numpy", strings.ToUpper(sum('a')))}, strict, false, false},
		{"name case", []PackageSpec{conda("NumPy", sum('a'))}, strict, false, false},
		{"mismatch", []PackageSpec{conda("numpy", sum('c'))}, verify, true, true},
		{"mismatch strict", []PackageSpec{conda("python", sum('a'))}, strict, true, true},
		{"not verifying", []PackageSpec{conda("numpy", sum('c'))}, RestoreOptions{}, false, false},
		{"strict without verify", []PackageSpec{conda("numpy", sum('c'))}, RestoreOptions{Strict: true}, false, false},
		{"no checksum", []PackageSpec{conda("numpy", "")}, verify, false, false},
		{"not solved", []PackageSpec{conda("scipy", sum('a'))}, verify, false, false},
		{"not solved strict", []PackageSpec{conda("scipy", sum('a'))}, strict, false, true},
		{"pip ignored", []PackageSpec{{Name: "numpy", Version: "1", Source: "pip", SHA256: sum('c')}}, strict, false, false},
	} {
		err := verifyRestoreChecksums(&EnvironmentSpec{Packages: tc.pkgs}, lf, tc.opts)
		if (err != nil) != tc.fail || errors.Is(err, ErrChecksumMismatch) != tc.mismatch {
			t.Errorf("%s: err = %v", tc.name, err)
		}
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

func TestFreezeGoombaCondaMeta(t *testing.T) {
	root := t.TempDir()
	fakeCondaEnv(t, root, "base-3.11", "3.11.15", "26.0.1")
	envPath := filepath.Join(root, "envs", "base-3.11")
	pySum, pipSum := strings.Repeat("ab", 32), strings.Repeat("cd", 32)
	for _, r := range []map[string]any{
		{
			"name": "python", "version": "3.11.15", "build": "hd63d673_0_cpython", "build_number": 0,
			"subdir": "linux-64", "sha256": pySum, "depends": []string{"libzlib >=1.3.1,<2.0a0"},
			"url":            "https://conda.anaconda.org/conda-forge/linux-64/python-3.11.15-hd63d673_0_cpython.conda",
			"requested_spec": "python=3.11", "files": []string{"bin/python3.11"},
		},
		{
			"name": "pip", "version": "26.0.1", "build": "pyh8b19718_0", "build_number": 0,
			"subdir": "noarch", "noarch": "python", "sha256": pipSum,
			"url": "https://conda.anaconda.org/conda-forge/noarch/pip-26.0.1-pyh8b19718_0.conda",
		},
	} {
		writeJSON(t, filepath.Join(envPath, "conda-meta", r["name"].(string)+"-"+r["version"].(string)+"-"+r["build"].(string)+".json"), r)
	}
	// pip freeze lists a pip package and, as for conda-forge builds, a conda
	// package by file URL; neither removes a conda package.
	pipScript := "#!/bin/sh\n" +
		"if [ \"$1\" = --version ]; then echo 'pip 26.0.1 from x (python 3.11)'; exit 0; fi\n" +
		"printf '%s\\n' 'requests==2.31.0' 'pip @ file:///home/conda/feedstock_root/build_artifacts/pip_1/work'\n"
	if err := os.WriteFile(filepath.Join(envPath, "bin", "pip"), []byte(pipScript), 0o755); err != nil {
		t.Fatal(err)
	}

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
		Channels:      []string{"conda-forge"},
		Packages: []PackageSpec{
			{Name: "pip", Version: "26.0.1", Build: "pyh8b19718_0", SHA256: pipSum, Source: "conda"},
			{Name: "python", Version: "3.11.15", Build: "hd63d673_0_cpython", SHA256: pySum, Source: "conda"},
		},
		PipPackages: []string{"requests==2.31.0", "pip"},
	}
	if !reflect.DeepEqual(es, want) {
		t.Errorf("frozen:\n got %+v\nwant %+v", es, want)
	}

	// Without pip, the conda packages are still frozen.
	env.PipPath = ""
	if err := env.FreezeToFile(out); err != nil {
		t.Fatal(err)
	}
	es = readSpec(t, out)
	if len(es.Packages) != 2 || len(es.PipPackages) != 0 {
		t.Errorf("frozen without pip: %+v", es)
	}

	// The frozen pins are what a goomba restore would solve for.
	specs, err := restoreCondaSpecs(&es)
	if err != nil || !reflect.DeepEqual(specs, []string{"pip==26.0.1=pyh8b19718_0", "python==3.11.15=hd63d673_0_cpython"}) {
		t.Errorf("restore specs = %q, %v", specs, err)
	}
}

func TestGoombaRestoreStrictNeedsChecksums(t *testing.T) {
	specFile := filepath.Join(t.TempDir(), "env.json")
	writeJSON(t, specFile, EnvironmentSpec{
		Name: "strict", PythonVersion: "3.11",
		Packages: []PackageSpec{{Name: "numpy", Version: "1.26.4", Source: "conda"}},
	})
	root := filepath.Join(t.TempDir(), "root")
	_, err := CreateEnvironmentFromJSONFileGoomba(context.Background(), specFile, root, RestoreOptions{VerifyChecksums: true, Strict: true}, GoombaOptions{}, nil)
	if err == nil || !strings.Contains(err.Error(), "lacks SHA256 checksum") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Error("the root directory was created")
	}
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
