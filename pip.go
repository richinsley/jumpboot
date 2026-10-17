package jumpboot

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os/exec"
)

// PipInstallPackages installs one or more Python packages using pip.
//
// Parameters:
//   - packages: Package names/specifiers (e.g., "numpy", "pandas>=1.0")
//   - index_url: Custom PyPI index URL; empty string uses default
//   - extra_index_url: Additional package index; empty string means none
//   - no_cache: If true, disables pip's cache (useful for CI/CD)
//   - progressCallback: Optional progress callback; may be nil
//
// Returns an error if pip fails, including stderr output for debugging.
func (env *PythonEnvironment) PipInstallPackages(packages []string, index_url string, extra_index_url string, no_cache bool, progressCallback ProgressCallback) error {
	args := []string{
		"install",
		"--no-warn-script-location",
	}

	if no_cache {
		args = append(args, "--no-cache-dir")
	}

	args = append(args, packages...)
	if index_url != "" {
		args = append(args, "--index-url", index_url)
	}
	if extra_index_url != "" {
		args = append(args, "--extra-index-url", extra_index_url)
	}

	installCmd := exec.Command(env.PipPath, args...)

	// Read stdout as pip writes it; scanning a bytes.Buffer concurrently with
	// os/exec writing to it races and can miss progress lines.
	stdout, err := installCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("error creating pip stdout pipe: %w", err)
	}
	defer stdout.Close()
	var stderrBuf bytes.Buffer
	installCmd.Stderr = &stderrBuf

	if err := installCmd.Start(); err != nil {
		return fmt.Errorf("error starting pip install: %v", err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 32*1024), 1024*1024)
	lineCount := int64(0)
	for scanner.Scan() {
		lineCount++
		if progressCallback != nil {
			bardesc := "Installing pip packages..."
			if len(packages) == 1 {
				bardesc = fmt.Sprintf("Installing pip package %s...", packages[0])
			}
			progressCallback(bardesc, lineCount, -1)
		}
	}
	scanErr := scanner.Err()
	if scanErr != nil {
		// Keep draining so pip cannot block on a full pipe before Wait reaps it.
		_, _ = io.Copy(io.Discard, stdout)
	}

	// Get the error (if any) *and* the stderr output.
	if err := installCmd.Wait(); err != nil {
		return fmt.Errorf("error installing package: %v, stderr: %s", err, stderrBuf.String())
	}
	if scanErr != nil {
		return fmt.Errorf("error reading pip install output: %w", scanErr)
	}

	if progressCallback != nil {
		progressCallback("Pip packages installed successfully", 100, 100)
	}

	return nil
}

// PipInstallRequirements installs packages from a requirements.txt file.
// The file should contain one package specifier per line in pip format.
func (env *PythonEnvironment) PipInstallRequirements(requirementsPath string, progressCallback ProgressCallback) error {
	installCmd := exec.Command(env.PipPath, "install", "--no-warn-script-location", "-r", requirementsPath)

	stdout, err := installCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("error creating stdout pipe: %v", err)
	}
	defer stdout.Close()

	if err := installCmd.Start(); err != nil {
		return fmt.Errorf("error starting pip install: %v", err)
	}

	scanner := bufio.NewScanner(stdout)
	lineCount := int64(0)
	for scanner.Scan() {
		lineCount++
		if progressCallback != nil {
			progressCallback("Installing pip requirements...", lineCount, -1)
		}
	}

	if err := installCmd.Wait(); err != nil {
		return fmt.Errorf("error installing requirements: %v", err)
	}

	if progressCallback != nil {
		progressCallback("Pip requirements installed successfully", 100, 100)
	}

	return nil
}

// PipInstallPackage installs a single Python package using pip.
// This is a convenience wrapper around PipInstallPackages for single packages.
func (env *PythonEnvironment) PipInstallPackage(packageToInstall string, index_url string, extra_index_url string, no_cache bool, progressCallback ProgressCallback) error {
	packages := []string{
		packageToInstall,
	}
	return env.PipInstallPackages(packages, index_url, extra_index_url, no_cache, progressCallback)
}
