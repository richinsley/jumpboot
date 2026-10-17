package jumpboot

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// micromambaBaseURL is the base URL for downloading micromamba binaries.
// This can be overridden for testing or to use a custom mirror.
var micromambaBaseURL = "https://github.com/mamba-org/micromamba-releases/releases"

// ExpectMicromamba ensures micromamba is available in the specified folder.
// If not present, it downloads the appropriate binary for the current platform.
//
// Supported platforms:
//   - Linux: amd64, arm64 (aarch64)
//   - macOS: amd64, arm64
//   - Windows: amd64 (arm64 uses amd64 emulation)
//
// The binary is downloaded from GitHub releases and made executable on Unix systems.
// Returns the full path to the micromamba binary.
func ExpectMicromamba(binFolder string, progressCallback ProgressCallback) (string, error) {
	// Detect platform and architecture
	platform := runtime.GOOS
	arch := runtime.GOARCH

	// Convert platform and arch to match micromamba naming
	var executableName string = "micromamba"
	if platform == "darwin" {
		platform = "osx"
	} else if platform == "windows" {
		platform = "win"
	}

	switch arch {
	case "amd64":
		arch = "64"
	case "arm64":
		if platform == "win" {
			// As of now, there is not a separate arm64 download for Windows
			arch = "64"
		} else if platform == "linux" {
			arch = "aarch64"
		} else {
			arch = "arm64"
		}
	default:
		return "", fmt.Errorf("unsupported architecture: %s", arch)
	}

	// Construct the download URL
	var downloadURL string
	version := "2.2.0-0" // Use this to specify a version, or leave empty for latest
	if version == "" {
		// Use the variable here!
		downloadURL = fmt.Sprintf("%s/latest/download/%s-%s-%s", micromambaBaseURL, executableName, platform, arch)
	} else {
		// Use the variable here!
		downloadURL = fmt.Sprintf("%s/download/%s/%s-%s-%s", micromambaBaseURL, version, executableName, platform, arch)
	}

	// Ensure the target bin directory exists
	if err := os.MkdirAll(binFolder, 0755); err != nil {
		return "", fmt.Errorf("error creating directory: %v", err)
	}

	// Target binary path
	if platform == "win" {
		executableName += ".exe"
	}
	binpath := filepath.Join(binFolder, executableName)

	req, err := http.NewRequest("GET", downloadURL, nil)
	if err != nil {
		return "", fmt.Errorf("error creating request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("error downloading file: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Keep the installed binary untouched until the whole response has been
	// written. A failed download must never leave a partial executable behind.
	f, err := os.CreateTemp(binFolder, "."+executableName+"-download-*")
	if err != nil {
		return "", fmt.Errorf("error creating temporary file: %w", err)
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)
	defer f.Close()

	var written int64
	if progressCallback != nil {
		progressCallback("Downloading micromamba", 0, resp.ContentLength)
	}

	buf := make([]byte, 32*1024)
	for {
		nr, er := resp.Body.Read(buf)
		if nr > 0 {
			nw, ew := f.Write(buf[0:nr])
			if nw > 0 {
				written += int64(nw)
				if progressCallback != nil {
					progressCallback("Downloading micromamba", written, resp.ContentLength)
				}
			}
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = io.ErrShortWrite
				break
			}
		}
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
	}

	if err != nil {
		return "", fmt.Errorf("error downloading micromamba: %w", err)
	}

	// Change file permissions to make it executable (not applicable for Windows)
	if platform != "win" {
		if err := f.Chmod(0755); err != nil {
			return "", fmt.Errorf("error setting file permissions: %w", err)
		}
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("error syncing micromamba download: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("error closing micromamba download: %w", err)
	}
	if err := os.Rename(tmpPath, binpath); err != nil {
		return "", fmt.Errorf("error installing micromamba download: %w", err)
	}

	return binpath, nil
}

// MicromambaInstallPackage installs a conda package using micromamba.
//
// Parameters:
//   - packageToInstall: Package name or specifier (e.g., "numpy", "scipy=1.9")
//   - channel: Conda channel (e.g., "conda-forge"); empty uses default
//
// The installation is performed with --no-rc to avoid configuration conflicts
// and uses the environment's prefix directly.
func (env *PythonEnvironment) MicromambaInstallPackage(packageToInstall string, channel string) error {
	var installCmd *exec.Cmd
	if channel != "" {
		/*
			../../bin/micromamba install --no-rc -c conda-forge -y --prefix /Users/richardinsley/Projects/comfycli/jumpboot/tests/mlx/micromamba/envs/myenv3.10 mlx
		*/
		installCmd = exec.Command(env.MicromambaPath, "install", "--no-rc", "-c", channel, "--prefix", env.EnvPath, "-y", packageToInstall)
	} else {
		installCmd = exec.Command(env.MicromambaPath, "install", "--no-rc", "--prefix", env.EnvPath, "-y", packageToInstall)
	}

	installCmd.Stdout = os.Stdout
	installCmd.Stderr = os.Stderr
	if err := installCmd.Run(); err != nil {
		return fmt.Errorf("error installing package: %v", err)
	}
	return nil
}
