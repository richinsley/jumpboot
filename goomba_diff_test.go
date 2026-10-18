package jumpboot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// unsetenv removes the environment variable key for the rest of the test.
func unsetenv(t *testing.T, key string) {
	t.Helper()
	old, ok := os.LookupEnv(key)
	if !ok {
		return
	}
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Setenv(key, old) })
}

// condaMetaPackage is what the differential compares of a conda-meta record.
type condaMetaPackage struct {
	Version string `json:"version"`
	Build   string `json:"build"`
	SHA256  string `json:"sha256"`
	Channel string `json:"channel"`
	Subdir  string `json:"subdir"`
}

// readCondaMetaPackages reads the records in prefix/conda-meta by name.
func readCondaMetaPackages(t *testing.T, prefix string) map[string]condaMetaPackage {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(prefix, "conda-meta", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]condaMetaPackage{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var r struct {
			Name string `json:"name"`
			condaMetaPackage
		}
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		out[r.Name] = r.condaMetaPackage
	}
	return out
}

// TestGoombaDifferentialMicromamba creates base-3.12 from the live
// conda-forge channel with each backend, under separate scratch roots,
// and compares the environments: the PythonEnvironment fields (paths
// equal modulo the root), the python and pip versions, and the conda
// packages recorded in conda-meta. micromamba is $GOOMBA_MICROMAMBA,
// placed in the scratch root's bin/ so that nothing is downloaded from
// GitHub, and runs with a scratch HOME, an explicit MAMBA_ROOT_PREFIX and
// no other CONDA_* or MAMBA_* variable of the developer's shell. Neither
// backend gets a pin: both must agree on current conda-forge.
func TestGoombaDifferentialMicromamba(t *testing.T) {
	mm := os.Getenv("GOOMBA_MICROMAMBA")
	if mm == "" || os.Getenv("GOOMBA_LIVE") != "1" {
		t.Skip("set GOOMBA_MICROMAMBA and GOOMBA_LIVE=1")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the micromamba placement below is for Unix")
	}
	noMicromambaDownload(t)
	mm, err := filepath.Abs(mm)
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	mRoot, gRoot, home := filepath.Join(scratch, "micromamba"), filepath.Join(scratch, "goomba"), filepath.Join(scratch, "home")
	for _, d := range []string{filepath.Join(mRoot, "bin"), gRoot, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(mm, filepath.Join(mRoot, "bin", "micromamba")); err != nil {
		t.Fatal(err)
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CONDA_") || strings.HasPrefix(k, "MAMBA_") {
			unsetenv(t, k)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("MAMBA_ROOT_PREFIX", mRoot)

	start := time.Now()
	m, err := CreateEnvironmentMamba("base-3.12", mRoot, "3.12", "conda-forge", nil)
	if err != nil {
		t.Fatalf("micromamba: %v", err)
	}
	mTime := time.Since(start)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	start = time.Now()
	g, err := CreateEnvironmentGoombaWithOptions(ctx, "base-3.12", gRoot, GoombaOptions{PythonVersion: "3.12", Channels: []string{"conda-forge"}}, nil)
	if err != nil {
		t.Fatalf("goomba: %v", err)
	}
	gTime := time.Since(start)
	t.Logf("micromamba %v (%s), goomba %v", mTime.Round(time.Millisecond), m.MicromambaVersion.String(), gTime.Round(time.Millisecond))

	// The PythonEnvironment fields.
	if m.Backend != BackendMicromamba || g.Backend != BackendGoomba || m.MicromambaPath == "" || g.MicromambaPath != "" || !m.IsNew || !g.IsNew {
		t.Errorf("backend fields: micromamba %+v, goomba %+v", m.BaseEnvironment, g.BaseEnvironment)
	}
	rel := func(root, p string) string {
		if p == "" {
			return ""
		}
		r, err := filepath.Rel(root, p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, f := range []struct {
		name string
		m, g string
	}{
		{"RootDir", m.RootDir, g.RootDir},
		{"EnvPath", m.EnvPath, g.EnvPath},
		{"EnvBinPath", m.EnvBinPath, g.EnvBinPath},
		{"EnvLibPath", m.EnvLibPath, g.EnvLibPath},
		{"PythonPath", m.PythonPath, g.PythonPath},
		{"PythonLibPath", m.PythonLibPath, g.PythonLibPath},
		{"PipPath", m.PipPath, g.PipPath},
		{"PythonHeadersPath", m.PythonHeadersPath, g.PythonHeadersPath},
		{"SitePackagesPath", m.SitePackagesPath, g.SitePackagesPath},
	} {
		if a, b := rel(mRoot, f.m), rel(gRoot, f.g); a != b || (f.name == "PythonLibPath" && a == "") {
			t.Errorf("%s: micromamba %q, goomba %q", f.name, a, b)
		}
	}
	if m.EnvironmentName != g.EnvironmentName || m.PythonVersion != g.PythonVersion || m.PipVersion != g.PipVersion {
		t.Errorf("name/python/pip: micromamba %s %s %s, goomba %s %s %s", m.EnvironmentName, m.PythonVersion.String(), m.PipVersion.String(),
			g.EnvironmentName, g.PythonVersion.String(), g.PipVersion.String())
	}
	t.Logf("python %s, pip %s", g.PythonVersion.String(), g.PipVersion.String())

	// The conda packages.
	mp, gp := readCondaMetaPackages(t, m.EnvPath), readCondaMetaPackages(t, g.EnvPath)
	names := map[string]bool{}
	for n := range mp {
		names[n] = true
	}
	for n := range gp {
		names[n] = true
	}
	var sorted []string
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	same := 0
	for _, n := range sorted {
		a, aok := mp[n]
		b, bok := gp[n]
		switch {
		case !aok:
			t.Errorf("%s %s %s: goomba only", n, b.Version, b.Build)
		case !bok:
			t.Errorf("%s %s %s: micromamba only", n, a.Version, a.Build)
		case a.Version != b.Version || a.Build != b.Build:
			t.Errorf("%s: micromamba %s %s, goomba %s %s", n, a.Version, a.Build, b.Version, b.Build)
		case a.SHA256 != b.SHA256 || a.Subdir != b.Subdir:
			t.Errorf("%s %s %s: micromamba sha256 %s subdir %s, goomba %s %s", n, a.Version, a.Build, a.SHA256, a.Subdir, b.SHA256, b.Subdir)
		default:
			same++
			t.Logf("  %s %s %s (channel: micromamba %q, goomba %q)", n, a.Version, a.Build, a.Channel, b.Channel)
		}
	}
	t.Logf("%d of %d packages identical (name, version, build, sha256, subdir)", same, len(sorted))
}
