package jumpboot

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
)

func (env *Environment) PipInstallPackages(packages []string, index_url string, extra_index_url string, no_cache bool, progressCallback ProgressCallback) error {
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

	// Capture both stdout AND stderr
	var stdoutBuf, stderrBuf bytes.Buffer
	installCmd.Stdout = &stdoutBuf
	installCmd.Stderr = &stderrBuf

	if err := installCmd.Start(); err != nil {
		return fmt.Errorf("error starting pip install: %v", err)
	}

	scanner := bufio.NewScanner(&stdoutBuf)
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

	// Get the error (if any) *and* the stderr output.
	if err := installCmd.Wait(); err != nil {
		return fmt.Errorf("error installing package: %v, stderr: %s", err, stderrBuf.String())
	}

	if progressCallback != nil {
		progressCallback("Pip packages installed successfully", 100, 100)
	}

	return nil
}

func (env *Environment) PipInstallRequirements(requirementsPath string, progressCallback ProgressCallback) error {
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

func (env *Environment) PipInstallPackage(packageToInstall string, index_url string, extra_index_url string, no_cache bool, progressCallback ProgressCallback) error {
	packages := []string{
		packageToInstall,
	}
	return env.PipInstallPackages(packages, index_url, extra_index_url, no_cache, progressCallback)
}
