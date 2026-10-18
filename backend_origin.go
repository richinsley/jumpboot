package jumpboot

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const backendMarker = ".jumpboot-backend"
const maxBackendEvidence = 64 << 10

// DetectCondaBackend returns the backend that created prefix. BackendNone
// means the prefix is missing or empty, not an existing environment. New
// Jumpboot environments have a marker in conda-meta; for legacy ones the
// first creation command in conda-meta/history is used. Unknown or
// contradictory provenance is an error, never a reason to guess from the
// presence of a micromamba binary in the shared root.
func DetectCondaBackend(prefix string) (Backend, error) {
	entries, err := os.ReadDir(prefix)
	if errors.Is(err, fs.ErrNotExist) {
		return BackendNone, nil
	}
	if err != nil {
		return BackendNone, fmt.Errorf("inspect environment %s: %w", prefix, err)
	}
	if len(entries) == 0 {
		return BackendNone, nil
	}
	meta := filepath.Join(prefix, "conda-meta")
	marked, markerExists, err := readBackendMarker(filepath.Join(meta, backendMarker))
	if err != nil {
		return BackendNone, fmt.Errorf("environment %s: %w", prefix, err)
	}
	history, historyExists, err := readBackendHistory(filepath.Join(meta, "history"))
	if err != nil {
		return BackendNone, fmt.Errorf("environment %s: %w", prefix, err)
	}
	if markerExists && historyExists && history != BackendNone && marked != history {
		return BackendNone, fmt.Errorf("environment %s: backend marker %q conflicts with first history command %q", prefix, marked, history)
	}
	if markerExists {
		if !historyExists {
			return BackendNone, fmt.Errorf("environment %s: backend marker exists but conda-meta/history is missing", prefix)
		}
		return marked, nil
	}
	if history != BackendNone {
		return history, nil
	}
	// Goomba creates this marker before writing history and leaves it after
	// an interrupted install. Route it to Goomba, which reports incomplete
	// or busy under its normal transaction checks.
	if _, err := os.Lstat(filepath.Join(meta, ".goomba-incomplete")); err == nil {
		return BackendGoomba, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return BackendNone, fmt.Errorf("environment %s: inspect incomplete marker: %w", prefix, err)
	}
	return BackendNone, fmt.Errorf("environment %s: cannot determine creator from backend marker or first conda-meta/history command", prefix)
}

func readBackendMarker(path string) (Backend, bool, error) {
	data, exists, err := readRegularEvidence(path, false)
	if err != nil || !exists {
		return BackendNone, exists, err
	}
	switch strings.TrimSpace(string(data)) {
	case string(BackendGoomba):
		return BackendGoomba, true, nil
	case string(BackendMicromamba):
		return BackendMicromamba, true, nil
	default:
		return BackendNone, true, fmt.Errorf("unknown backend marker %s", path)
	}
}

func readBackendHistory(path string) (Backend, bool, error) {
	data, exists, err := readRegularEvidence(path, true)
	if err != nil || !exists {
		return BackendNone, exists, err
	}
	truncated := len(data) > maxBackendEvidence
	if truncated {
		// The first history command normally appears near the beginning;
		// later revisions may make the file arbitrarily large. Never parse
		// a command line cut at the evidence boundary.
		data = data[:maxBackendEvidence]
		if end := strings.LastIndexByte(string(data), '\n'); end >= 0 {
			data = data[:end+1]
		} else {
			return BackendNone, true, fmt.Errorf("first history command in %s exceeds %d bytes", path, maxBackendEvidence)
		}
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "# cmd:") {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "# cmd:")))
		if len(fields) == 0 {
			return BackendNone, true, fmt.Errorf("empty first history command in %s", path)
		}
		cmd := filepath.Base(strings.Trim(fields[0], `"'`))
		created := false
		for _, arg := range fields[1:] {
			if arg == "create" {
				created = true
				break
			}
		}
		if !created {
			return BackendNone, true, fmt.Errorf("first history command in %s is not a create command", path)
		}
		switch strings.ToLower(cmd) {
		case "goomba":
			return BackendGoomba, true, nil
		case "micromamba", "micromamba.exe":
			return BackendMicromamba, true, nil
		default:
			return BackendNone, true, fmt.Errorf("unrecognized first history command %q in %s", cmd, path)
		}
	}
	if err := scanner.Err(); err != nil {
		return BackendNone, true, fmt.Errorf("read history %s: %w", path, err)
	}
	if truncated {
		return BackendNone, true, fmt.Errorf("no first history command within %d bytes of %s", maxBackendEvidence, path)
	}
	return BackendNone, true, nil
}

func readRegularEvidence(path string, allowOverflow bool) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, true, fmt.Errorf("backend evidence %s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, true, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBackendEvidence+1))
	if err != nil {
		return nil, true, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > maxBackendEvidence && !allowOverflow {
		return nil, true, fmt.Errorf("backend evidence %s exceeds %d bytes", path, maxBackendEvidence)
	}
	return data, true, nil
}

func writeBackendMarker(prefix string, backend Backend) error {
	if backend != BackendGoomba && backend != BackendMicromamba {
		return fmt.Errorf("invalid backend marker %q", backend)
	}
	meta := filepath.Join(prefix, "conda-meta")
	f, err := os.CreateTemp(meta, ".jumpboot-backend-*")
	if err != nil {
		return fmt.Errorf("create backend marker: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err := io.WriteString(f, string(backend)+"\n"); err != nil {
		f.Close()
		return fmt.Errorf("write backend marker: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync backend marker: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close backend marker: %w", err)
	}
	if err := os.Rename(f.Name(), filepath.Join(meta, backendMarker)); err != nil {
		return fmt.Errorf("commit backend marker: %w", err)
	}
	return nil
}
