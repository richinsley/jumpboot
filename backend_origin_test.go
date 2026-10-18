package jumpboot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectCondaBackend(t *testing.T) {
	root := t.TempDir()
	prefix := filepath.Join(root, "envs", "legacy")
	if got, err := DetectCondaBackend(prefix); err != nil || got != BackendNone {
		t.Fatalf("missing prefix: backend %q, err %v", got, err)
	}
	meta := filepath.Join(prefix, "conda-meta")
	if err := os.MkdirAll(meta, 0o755); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(meta, "history")
	writeHistory := func(s string) {
		t.Helper()
		if err := os.WriteFile(history, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeHistory("==> 2026-10-18 <==\n# cmd: /home/calo/.poppi/bin/micromamba --root-prefix /home/calo/.poppi create -n base python=3.12\n# cmd: goomba create -p later\n")
	if got, err := DetectCondaBackend(prefix); err != nil || got != BackendMicromamba {
		t.Fatalf("legacy micromamba: backend %q, err %v", got, err)
	}
	writeHistory("==> 2026-10-18 <==\n# cmd: goomba create -p /env\n")
	if got, err := DetectCondaBackend(prefix); err != nil || got != BackendGoomba {
		t.Fatalf("legacy goomba: backend %q, err %v", got, err)
	}
	if err := writeBackendMarker(prefix, BackendGoomba); err != nil {
		t.Fatal(err)
	}
	if got, err := DetectCondaBackend(prefix); err != nil || got != BackendGoomba {
		t.Fatalf("marked goomba: backend %q, err %v", got, err)
	}
	writeHistory("# cmd: micromamba create -n legacy\n")
	if _, err := DetectCondaBackend(prefix); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting marker and history: %v", err)
	}
	if err := os.Remove(filepath.Join(meta, backendMarker)); err != nil {
		t.Fatal(err)
	}
	writeHistory("# cmd: conda create -n legacy\n")
	if _, err := DetectCondaBackend(prefix); err == nil || !strings.Contains(err.Error(), "unrecognized") {
		t.Fatalf("unknown creator: %v", err)
	}
}

func TestDetectCondaBackendInterrupted(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "envs", "half")
	meta := filepath.Join(prefix, "conda-meta")
	if err := os.MkdirAll(meta, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(meta, ".goomba-incomplete"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := DetectCondaBackend(prefix); err != nil || got != BackendGoomba {
		t.Fatalf("interrupted goomba: backend %q, err %v", got, err)
	}
}

func TestDetectCondaBackendLargeHistory(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "envs", "old")
	meta := filepath.Join(prefix, "conda-meta")
	if err := os.MkdirAll(meta, 0o755); err != nil {
		t.Fatal(err)
	}
	history := "# cmd: micromamba create -n old\n" + strings.Repeat("# later revision\n", 10000)
	if err := os.WriteFile(filepath.Join(meta, "history"), []byte(history), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := DetectCondaBackend(prefix); err != nil || got != BackendMicromamba {
		t.Fatalf("large legacy history: backend %q, err %v", got, err)
	}
}

func TestUnknownCreatorRefusesBothCreateAPIs(t *testing.T) {
	noMicromambaDownload(t)
	root := t.TempDir()
	fakeCondaEnv(t, root, "unknown", "3.11.9", "24.0")
	history := filepath.Join(root, "envs", "unknown", "conda-meta", "history")
	if err := os.WriteFile(history, []byte("# cmd: conda create -n unknown\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateEnvironmentMamba("unknown", root, "3.11", "", nil); err == nil || !strings.Contains(err.Error(), "unrecognized") {
		t.Fatalf("mamba API should refuse unknown creator: %v", err)
	}
	if _, err := CreateEnvironmentGoomba("unknown", root, "3.11", "", nil); err == nil || !strings.Contains(err.Error(), "unrecognized") {
		t.Fatalf("goomba API should refuse unknown creator: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "bin", "micromamba")); !os.IsNotExist(err) {
		t.Errorf("unknown creator caused a micromamba download: %v", err)
	}
}
