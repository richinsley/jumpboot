package jumpboot

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func testMicromambaPath(dir string) string {
	name := "micromamba"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name)
}

func useMicromambaServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	old := micromambaBaseURL
	micromambaBaseURL = server.URL
	t.Cleanup(func() { micromambaBaseURL = old })
}

func TestExpectMicromambaInterruptedDownloadLeavesNoBinary(t *testing.T) {
	useMicromambaServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("partial"))
	})
	dir := t.TempDir()
	if _, err := ExpectMicromamba(dir, nil); err == nil {
		t.Fatal("interrupted download succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("interrupted download left files: %v", entries)
	}
}

func TestExpectMicromambaInterruptedDownloadPreservesExisting(t *testing.T) {
	useMicromambaServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("partial"))
	})
	dir := t.TempDir()
	path := testMicromambaPath(dir)
	before := []byte("previous-working-binary")
	if err := os.WriteFile(path, before, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ExpectMicromamba(dir, nil); err == nil {
		t.Fatal("interrupted download succeeded")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("existing binary changed: got %q, want %q", after, before)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("interrupted download left files: %v", entries)
	}
}

func TestExpectMicromambaShorterDownloadReplacesExisting(t *testing.T) {
	want := []byte("new")
	useMicromambaServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(want)
	})
	dir := t.TempDir()
	path := testMicromambaPath(dir)
	if err := os.WriteFile(path, []byte("previous-working-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	gotPath, err := ExpectMicromamba(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != path {
		t.Fatalf("path = %q, want %q", gotPath, path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("replacement = %q, want %q", got, want)
	}
}
