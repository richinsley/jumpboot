package jumpboot

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Runtime defines common operations for any language runtime environment.
// This interface allows code to work with different runtime types (Python, Node.js, etc.)
// in a uniform way.
type Runtime interface {
	// Name returns the environment identifier.
	Name() string

	// Path returns the base environment path.
	Path() string

	// BinPath returns the path to executables.
	BinPath() string

	// Freeze serializes the environment to a file for reproducibility.
	Freeze(filePath string) error
}

// BaseEnvironment contains common fields for any conda-managed environment.
// This is embedded in runtime-specific environment types like PythonEnvironment.
// It provides the foundation for container management independent of the runtime.
type BaseEnvironment struct {
	// EnvironmentName is the identifier for this environment (e.g., "myenv", "system").
	EnvironmentName string

	// RootDir is the root directory containing the environment and micromamba binary.
	RootDir string

	// EnvPath is the full path to the environment directory.
	EnvPath string

	// EnvBinPath is the path to the bin (or Scripts on Windows) directory.
	EnvBinPath string

	// EnvLibPath is the path to the lib directory within the environment.
	EnvLibPath string

	// MicromambaVersion is the version of micromamba, if applicable.
	MicromambaVersion Version

	// MicromambaPath is the full path to the micromamba executable.
	// Empty for system Python or venv environments.
	MicromambaPath string

	// IsNew indicates whether this environment was newly created (true)
	// or already existed (false).
	IsNew bool
}

// PythonEnvironment represents a Python environment with all necessary paths and version
// information. It can be created from micromamba, system Python, a virtual environment,
// or restored from a JSON specification file.
//
// The PythonEnvironment struct provides methods for running Python scripts, creating
// Python processes, and installing packages via pip or micromamba.
type PythonEnvironment struct {
	// BaseEnvironment contains container-agnostic fields.
	BaseEnvironment

	// PythonVersion is the detected Python version (e.g., 3.10.12).
	PythonVersion Version

	// PipVersion is the detected pip version.
	PipVersion Version

	// PythonPath is the full path to the Python executable.
	PythonPath string

	// PythonLibPath is the path to the Python shared library (libpython).
	// May be empty if the library is not found.
	PythonLibPath string

	// PipPath is the full path to the pip executable.
	PipPath string

	// PythonHeadersPath is the path to the Python development headers.
	PythonHeadersPath string

	// SitePackagesPath is the path to the site-packages directory.
	SitePackagesPath string
}

// Name returns the environment identifier.
// Implements the Runtime interface.
func (env *PythonEnvironment) Name() string {
	return env.EnvironmentName
}

// Path returns the base environment path.
// Implements the Runtime interface.
func (env *PythonEnvironment) Path() string {
	return env.EnvPath
}

// BinPath returns the path to executables.
// Implements the Runtime interface.
func (env *PythonEnvironment) BinPath() string {
	return env.EnvBinPath
}

// Freeze serializes the environment to a file for reproducibility.
// Implements the Runtime interface. This is an alias for FreezeToFile.
func (env *PythonEnvironment) Freeze(filePath string) error {
	return env.FreezeToFile(filePath)
}

// VenvOptions configures the creation of a Python virtual environment.
// These options correspond to the flags available in Python's venv module.
type VenvOptions struct {
	// SystemSitePackages gives access to the system site-packages directory.
	SystemSitePackages bool

	// Symlinks creates symlinks to Python files instead of copies (Unix default).
	Symlinks bool

	// Copies creates copies of Python files instead of symlinks (Windows default).
	Copies bool

	// Clear deletes the contents of the environment directory if it exists.
	Clear bool

	// Upgrade upgrades an existing environment to use the current Python version.
	Upgrade bool

	// WithoutPip skips pip installation in the virtual environment.
	WithoutPip bool

	// Prompt sets a custom prompt prefix for the virtual environment.
	Prompt string

	// UpgradeDeps upgrades pip and setuptools to the latest versions.
	UpgradeDeps bool
}

// PackageSpec represents a package with optional integrity verification.
// It provides a structured way to specify packages with version pinning
// and optional SHA256 checksums for security.
type PackageSpec struct {
	// Name is the package name.
	Name string `json:"name"`

	// Version is the package version.
	Version string `json:"version"`

	// Build is the build string (conda packages only).
	Build string `json:"build,omitempty"`

	// SHA256 is the optional SHA256 checksum for integrity verification.
	SHA256 string `json:"sha256,omitempty"`

	// Source indicates where the package came from ("conda" or "pip").
	Source string `json:"source,omitempty"`
}

// EnvironmentSpec represents a complete environment specification that can be
// serialized to JSON and used to recreate an identical environment.
// This is the format used by FreezeToFile and CreateEnvironmentFromJSONFile.
type EnvironmentSpec struct {
	// Name is the environment name.
	Name string `json:"name"`

	// Channels lists the conda channels used (e.g., "conda-forge", "defaults").
	Channels []string `json:"channels,omitempty"`

	// CondaPackages lists conda packages in "name=version=build" format.
	// Deprecated: Use Packages with Source="conda" instead.
	CondaPackages []string `json:"conda_packages,omitempty"`

	// PipPackages lists pip packages in "name==version" format.
	// Deprecated: Use Packages with Source="pip" instead.
	PipPackages []string `json:"pip_packages,omitempty"`

	// Packages is the unified package list with optional checksums.
	// This is the preferred format for new environment specs.
	Packages []PackageSpec `json:"packages,omitempty"`

	// PythonVersion specifies the Python version (e.g., "3.10").
	PythonVersion string `json:"python_version,omitempty"`

	// MicromambaVersion optionally specifies the micromamba version used.
	MicromambaVersion string `json:"micromamba_version,omitempty"`
}

// RestoreOptions configures how environments are restored from specification files.
type RestoreOptions struct {
	// VerifyChecksums enables SHA256 verification for packages that have checksums.
	VerifyChecksums bool

	// Strict fails the restore if any package with a checksum fails verification,
	// or if VerifyChecksums is true and a package lacks a checksum.
	Strict bool
}

// CreateEnvironmentOptions specifies feedback verbosity during environment creation.
type CreateEnvironmentOptions int

// ProgressCallback is called during long-running operations to report progress.
// The message describes the current operation, current is the progress value,
// and total is the expected total (-1 if unknown).
type ProgressCallback func(message string, current, total int64)

const (
	// Show progress bar
	ShowProgressBar CreateEnvironmentOptions = iota
	// Show progress bar and verbose output
	ShowProgressBarVerbose
	// Show verbose output
	ShowVerbose
	// Show nothing
	ShowNothing
)

// createCondaEnvironment ensures micromamba is available under rootDir/bin,
// then creates (or reuses) a conda environment named envName containing the
// given conda package specs, and returns a populated BaseEnvironment.
//
// It is runtime-agnostic: callers layer runtime-specific path and version
// inspection on top of the returned base. condaPackages are passed verbatim to
// `micromamba create` (e.g. "python=3.11" or "nodejs=20"); label is a short
// runtime name used only in progress messages (e.g. "Python", "Node.js").
func createCondaEnvironment(envName string, rootDir string, condaPackages []string, channel string, label string, progressCallback ProgressCallback) (*BaseEnvironment, error) {
	binDirectory := filepath.Join(rootDir, "bin")
	// Check if the specified root directory exists
	if _, err := os.Stat(binDirectory); os.IsNotExist(err) {
		// Ensure the target bin directory exists
		if err := os.MkdirAll(binDirectory, 0755); err != nil {
			return nil, fmt.Errorf("error creating directory: %v", err)
		}
	}

	// Check if the specified root directory is writable
	if !isDirWritable(rootDir) {
		return nil, fmt.Errorf("root directory is not writable: %s", rootDir)
	}

	// Detect platform and architecture
	platform := runtime.GOOS
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "64"
	case "arm64":
		if platform == "windows" {
			// As of now, there is not a separate arm64 download for Windows
			// We'll use the same download as for amd64
			arch = "64"
		}
	default:
		return nil, fmt.Errorf("unsupported architecture: %s", arch)
	}

	// Convert platform and arch to match micromamba naming
	var executableName string = "micromamba"
	if platform == "windows" {
		executableName += ".exe"
	}

	// Create the base environment object
	env := &BaseEnvironment{
		EnvironmentName: envName,
		RootDir:         rootDir,
		MicromambaPath:  filepath.Join(binDirectory, executableName),
	}

	// Check if binDirectory already has micromamba by getting its version
	mver, err := RunReadStdout(env.MicromambaPath, "micromamba", "--version")
	if err != nil {
		_, ok := err.(*fs.PathError)
		if ok {
			// download micromamba if it doesn't exist
			env.MicromambaPath, err = ExpectMicromamba(binDirectory, progressCallback)
			if err != nil {
				return nil, fmt.Errorf("error downloading micromamba: %v", err)
			}
			mver, err = RunReadStdout(env.MicromambaPath, "micromamba", "--version")
			if err != nil {
				return nil, fmt.Errorf("error running micromamba --version: %v", err)
			}
		} else {
			return nil, fmt.Errorf("error running micromamba --version: %v", err)
		}
	}

	env.MicromambaVersion, err = ParseVersion(mver)
	if err != nil {
		return nil, fmt.Errorf("error parsing micromamba version: %v", err)
	}

	// check if the environment exists
	envPath := filepath.Join(env.RootDir, "envs", env.EnvironmentName)
	if _, err := os.Stat(envPath); os.IsNotExist(err) {
		// this is a new environment
		env.IsNew = true

		// Create a new environment with micromamba
		cmdargs := []string{"--root-prefix", env.RootDir, "create", "-n", env.EnvironmentName, "-y"}
		cmdargs = append(cmdargs, condaPackages...)
		if channel != "" {
			cmdargs = append(cmdargs, "-c", channel)
		}

		createEnvCmd := exec.Command(env.MicromambaPath, cmdargs...)
		createEnvCmd.Env = append(os.Environ(), "MAMBA_ROOT_PREFIX="+env.RootDir)

		stdout, err := createEnvCmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		defer stdout.Close()

		if err := createEnvCmd.Start(); err != nil {
			return nil, err
		}

		scanner := bufio.NewScanner(stdout)
		lineCount := 0
		for scanner.Scan() {
			lineCount++
			if progressCallback != nil {
				progressCallback("Creating "+label+" environment...", int64(lineCount), -1)
			}
		}

		if err := createEnvCmd.Wait(); err != nil {
			return nil, fmt.Errorf("error creating environment: %v", err)
		}

		if progressCallback != nil {
			progressCallback(label+" environment created successfully", 100, 100)
		}
	}

	// Populate the runtime-agnostic paths.
	env.EnvPath = envPath
	env.EnvLibPath = filepath.Join(envPath, "lib")
	if platform == "windows" {
		// On Windows the environment root holds the executables directly.
		env.EnvBinPath = envPath
	} else {
		env.EnvBinPath = filepath.Join(envPath, "bin")
	}

	return env, nil
}

// CreateEnvironmentMamba creates a new Python environment using micromamba.
// If micromamba is not present in the rootDir/bin directory, it will be downloaded automatically.
//
// Parameters:
//   - envName: Name for the new environment (e.g., "myenv")
//   - rootDir: Root directory for micromamba and environments
//   - pythonVersion: Python version to install (e.g., "3.10"); defaults to "3.10" if empty
//   - channel: Conda channel to use (e.g., "conda-forge"); uses default if empty
//   - progressCallback: Optional callback for progress updates; may be nil
//
// The environment is created at rootDir/envs/envName. If the environment already exists,
// it is reused and IsNew will be false.
//
// Returns an error if the architecture is unsupported, the directory is not writable,
// or the requested Python version cannot be satisfied.
func CreateEnvironmentMamba(envName string, rootDir string, pythonVersion string, channel string, progressCallback ProgressCallback) (*PythonEnvironment, error) {
	if pythonVersion == "" {
		pythonVersion = "3.10"
	}

	requestedVersion, err := ParseVersion(pythonVersion)
	if err != nil {
		return nil, fmt.Errorf("error parsing requested python version: %v", err)
	}

	// Create (or reuse) the conda environment with the runtime-agnostic helper.
	base, err := createCondaEnvironment(envName, rootDir, []string{"python=" + pythonVersion}, channel, "Python", progressCallback)
	if err != nil {
		return nil, err
	}

	env := &PythonEnvironment{BaseEnvironment: *base}
	platform := runtime.GOOS

	// Construct the full paths to the Python and pip executables within the created environment
	if platform == "windows" {
		env.PythonPath = filepath.Join(env.EnvBinPath, "python.exe")
		env.PipPath = filepath.Join(env.EnvPath, "Scripts", "pip.exe")
	} else {
		env.PythonPath = filepath.Join(env.EnvBinPath, "python")
		env.PipPath = filepath.Join(env.EnvBinPath, "pip")
	}

	env.SitePackagesPath = filepath.Join(env.EnvPath, "lib", "python"+requestedVersion.MinorString(), "site-packages")

	// find the python lib path
	env.PythonLibPath = env.EnvLibPath
	if platform == "windows" {
		env.PythonLibPath = filepath.Join(env.EnvPath, "python"+requestedVersion.MinorStringCompact()+".dll")
	} else if platform == "darwin" {
		env.PythonLibPath = filepath.Join(env.EnvPath, "lib", "libpython"+requestedVersion.MinorString()+".dylib")
	} else {
		env.PythonLibPath = filepath.Join(env.EnvPath, "lib", "libpython"+requestedVersion.MinorString()+".so")
	}

	// find the python headers path
	env.PythonHeadersPath = filepath.Join(env.EnvPath, "include", "python"+requestedVersion.MinorString())

	// Check if the Python executable exists and get its version
	pver, err := RunReadStdout(env.PythonPath, "--version")
	if err != nil {
		return nil, fmt.Errorf("error running python --version: %v", err)
	}
	env.PythonVersion, err = ParsePythonVersion(pver)
	if err != nil {
		return nil, fmt.Errorf("error parsing Python version: %v", err)
	}
	// Check if the Python lib exists
	if _, err := os.Stat(env.PythonLibPath); os.IsNotExist(err) {
		env.PythonLibPath = ""
	}

	// Check if the pip executable exists and get its version
	pipver, err := RunReadStdout(env.PipPath, "--version")
	if err != nil {
		return nil, fmt.Errorf("error running pip --version: %v", err)
	}
	env.PipVersion, err = ParsePipVersion(pipver)
	if err != nil {
		return nil, fmt.Errorf("error parsing pip version: %v", err)
	}

	// ensure the python version is equal or greater than the requested version
	if env.PythonVersion.Compare(requestedVersion) < 0 {
		return nil, fmt.Errorf("requested python version %s is not available, found %s", requestedVersion.String(), env.PythonVersion.String())
	}

	return env, nil
}

// CreateEnvironmentFromExacutable creates a PythonEnvironment from an existing Python executable.
// This is useful when you have a specific Python installation you want to use.
//
// The function queries the Python executable to determine version information,
// site-packages path, pip location, and other environment details.
//
// Note: The function name contains a typo ("Exacutable") for backwards compatibility.
func CreateEnvironmentFromExacutable(pythonPath string) (*PythonEnvironment, error) {
	env := &PythonEnvironment{
		BaseEnvironment: BaseEnvironment{
			EnvironmentName: "system",
			RootDir:         "", // Will be set based on the system Python path
			IsNew:           false,
		},
	}

	env.PythonPath = pythonPath
	env.RootDir = filepath.Dir(filepath.Dir(pythonPath))

	// Get Python version
	versionCmd := exec.Command(pythonPath, "--version")
	versionOutput, err := versionCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("error getting Python version: %v", err)
	}

	versionStr := strings.TrimSpace(string(versionOutput))
	env.PythonVersion, err = ParsePythonVersion(versionStr)
	if err != nil {
		return nil, fmt.Errorf("error parsing Python version: %v", err)
	}

	// Get site-packages path
	sitePackagesCmd := exec.Command(pythonPath, "-c", "import site; print(site.getsitepackages()[0])")
	sitePackagesOutput, err := sitePackagesCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("error getting site-packages path: %v", err)
	}

	env.SitePackagesPath = strings.TrimSpace(string(sitePackagesOutput))

	// Get pip path
	pipCmd := "pip3"
	if runtime.GOOS == "windows" {
		pipCmd = "pip3.exe"
	}

	// try pip3 first
	env.PipPath, err = exec.LookPath(pipCmd)
	if err != nil {
		// try pip
		pipCmd = "pip"
		if runtime.GOOS == "windows" {
			pipCmd = "pip.exe"
		}
		env.PipPath, err = exec.LookPath(pipCmd)
		if err != nil {
			return nil, fmt.Errorf("pip not found: %v", err)
		}
	}

	// Get pip version
	pipVersionCmd := exec.Command(env.PipPath, "--version")
	pipVersionOutput, err := pipVersionCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("error getting pip version: %v", err)
	}

	pipVersionStr := strings.TrimSpace(string(pipVersionOutput))
	env.PipVersion, err = ParsePipVersion(pipVersionStr)
	if err != nil {
		return nil, fmt.Errorf("error parsing pip version: %v", err)
	}

	// Set other paths
	env.EnvPath = env.RootDir
	env.EnvBinPath = filepath.Dir(pythonPath)

	// Get Python lib path
	var libPathCmd string
	if runtime.GOOS == "windows" {
		libPathCmd = "import sys; print(sys.executable)"
	} else {
		libPathCmd = "import sysconfig; print(sysconfig.get_config_var('LIBDIR'))"
	}

	libPathCmdExec := exec.Command(pythonPath, "-c", libPathCmd)
	libPathOutput, err := libPathCmdExec.Output()
	if err != nil {
		return nil, fmt.Errorf("error getting Python lib path: %v", err)
	}

	env.PythonLibPath = strings.TrimSpace(string(libPathOutput))
	if runtime.GOOS != "windows" {
		env.PythonLibPath = filepath.Join(env.PythonLibPath, fmt.Sprintf("libpython%s.so", env.PythonVersion.MinorString()))
	}

	// Get Python headers path
	headersPathCmd := "import sysconfig; print(sysconfig.get_path('include'))"
	headersPathCmdExec := exec.Command(pythonPath, "-c", headersPathCmd)
	headersPathOutput, err := headersPathCmdExec.Output()
	if err != nil {
		return nil, fmt.Errorf("error getting Python headers path: %v", err)
	}

	env.PythonHeadersPath = strings.TrimSpace(string(headersPathOutput))

	// Set EnvLibPath
	env.EnvLibPath = filepath.Dir(env.PythonLibPath)

	// Micromamba is not applicable for system Python, so we'll set these to empty
	env.MicromambaPath = ""
	env.MicromambaVersion = Version{}

	return env, nil
}

// CreateEnvironmentFromSystem creates a PythonEnvironment using the system Python installation.
//
// On Unix systems, it searches for "python3" then "python" using exec.LookPath.
// On Windows, it first tries "py.exe" (Python launcher), then searches for "python"
// while filtering out the Microsoft Store placeholder executables.
//
// Returns an error if no Python installation is found.
func CreateEnvironmentFromSystem() (*PythonEnvironment, error) {
	pythonPath := ""
	if runtime.GOOS == "windows" {
		// windows is a gruesome OS, so we need to hunt for the correct python executable
		// microsoft has 'place holders' for python, so we must exclude them (AppData\Local\Microsoft\WindowsApps\python.exe)
		// check for py.exe (the python launcher).  We'll use exec.cmd with 'where'
		wcmd := exec.Command("where", "py")
		wout, err := wcmd.Output()
		if err != nil {
			return nil, fmt.Errorf("error running 'where py.exe': %v", err)
		}
		// we'll use the first path in the list
		pythonPath = strings.TrimSpace(string(wout))
		if pythonPath == "" {
			// ugh, we didn't find py.exe, so we'll use 'where python' and filter out the microsoft placeholder
			// we'll use the first path in the list
			wcmd = exec.Command("where", "python")
			wout, err = wcmd.Output()
			if err != nil {
				return nil, fmt.Errorf("error running 'where python': %v", err)
			}
			paths := strings.Split(string(wout), "\n")
			for _, p := range paths {
				p = strings.TrimSpace(p)
				if !strings.Contains(p, "Microsoft\\WindowsApps") {
					pythonPath = p
					break
				}
			}
		}
	} else {
		// for posix systems, we'll use exec.LookPath (see how easy that is Microsoft!?)
		var err error
		// look for explicit python3 first
		pythonPath, err = exec.LookPath("python3")
		if err != nil {
			// try "python"
			pythonPath, err = exec.LookPath("python")
			if err != nil {
				return nil, fmt.Errorf("python not found: %v", err)
			}
		}
	}

	return CreateEnvironmentFromExacutable(pythonPath)
}

// CreateVenvEnvironment creates a Python virtual environment using the venv module.
//
// Parameters:
//   - baseEnv: The base Python environment to create the venv from
//   - venvPath: Path where the virtual environment will be created
//   - options: Configuration options for the venv (see VenvOptions)
//   - progressCallback: Optional callback for progress updates; may be nil
//
// The virtual environment inherits from baseEnv but has its own site-packages.
// If the venv already exists and options.Clear is false, it may be upgraded
// or reused depending on options.Upgrade.
//
// Returns an error if baseEnv is nil or venv creation fails.
func CreateVenvEnvironment(baseEnv *PythonEnvironment, venvPath string, options VenvOptions, progressCallback ProgressCallback) (*PythonEnvironment, error) {
	if baseEnv == nil {
		return nil, fmt.Errorf("base environment is nil")
	}

	// Check if the environment already exists
	envExists := false
	if _, err := os.Stat(venvPath); err == nil {
		envExists = true
	}

	// Create a new PythonEnvironment object
	newEnv := &PythonEnvironment{
		BaseEnvironment: BaseEnvironment{
			EnvironmentName: filepath.Base(venvPath),
			RootDir:         venvPath,
			IsNew:           !envExists || options.Clear, // Set IsNew if the env doesn't exist or if clear is true
		},
	}

	// Prepare venv command arguments
	args := []string{"-m", "venv"}

	if options.SystemSitePackages {
		args = append(args, "--system-site-packages")
	}
	if options.Symlinks {
		args = append(args, "--symlinks")
	}
	if options.Copies {
		args = append(args, "--copies")
	}
	if options.Clear {
		args = append(args, "--clear")
	} else if options.Upgrade {
		args = append(args, "--upgrade")
	}
	if options.WithoutPip {
		args = append(args, "--without-pip")
	}
	if options.Prompt != "" {
		args = append(args, "--prompt", options.Prompt)
	}
	if options.UpgradeDeps {
		args = append(args, "--upgrade-deps")
	}

	args = append(args, venvPath)

	// Create or update the virtual environment
	var stderr bytes.Buffer
	venvCmd := exec.Command(baseEnv.PythonPath, args...)
	venvCmd.Stderr = &stderr // Capture stderr output
	if err := venvCmd.Run(); err != nil {
		// Include stderr in the error message
		return nil, fmt.Errorf("failed to create/update virtual environment: %v, stderr: %s", err, stderr.String())
	}

	if progressCallback != nil {
		if newEnv.IsNew {
			progressCallback("Created virtual environment", 20, 100)
		} else {
			progressCallback("Updated virtual environment", 20, 100)
		}
	}

	// Set paths based on the new virtual environment
	if runtime.GOOS == "windows" {
		newEnv.EnvBinPath = filepath.Join(venvPath, "Scripts")
		newEnv.PythonPath = filepath.Join(newEnv.EnvBinPath, "python.exe")
		newEnv.PipPath = filepath.Join(newEnv.EnvBinPath, "pip.exe")
	} else {
		newEnv.EnvBinPath = filepath.Join(venvPath, "bin")
		newEnv.PythonPath = filepath.Join(newEnv.EnvBinPath, "python")
		newEnv.PipPath = filepath.Join(newEnv.EnvBinPath, "pip")
	}

	// Get Python version
	versionCmd := exec.Command(newEnv.PythonPath, "--version")
	versionOutput, err := versionCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("error getting Python version: %v", err)
	}

	versionStr := strings.TrimSpace(string(versionOutput))
	newEnv.PythonVersion, err = ParsePythonVersion(versionStr)
	if err != nil {
		return nil, fmt.Errorf("error parsing Python version: %v", err)
	}

	if progressCallback != nil {
		progressCallback("Got Python version", 40, 100)
	}

	// Get site-packages path
	sitePackagesCmd := exec.Command(newEnv.PythonPath, "-c", "import site; print(site.getsitepackages()[0])")
	sitePackagesOutput, err := sitePackagesCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("error getting site-packages path: %v", err)
	}

	newEnv.SitePackagesPath = strings.TrimSpace(string(sitePackagesOutput))

	if progressCallback != nil {
		progressCallback("Got site-packages path", 60, 100)
	}

	// Get pip version (if pip is installed)
	if !options.WithoutPip {
		pipVersionCmd := exec.Command(newEnv.PipPath, "--version")
		pipVersionOutput, err := pipVersionCmd.Output()
		if err != nil {
			return nil, fmt.Errorf("error getting pip version: %v", err)
		}

		pipVersionStr := strings.TrimSpace(string(pipVersionOutput))
		newEnv.PipVersion, err = ParsePipVersion(pipVersionStr)
		if err != nil {
			return nil, fmt.Errorf("error parsing pip version: %v", err)
		}

		if progressCallback != nil {
			progressCallback("Got pip version", 80, 100)
		}
	}

	// Get Python lib path
	var libPathCmd string
	if runtime.GOOS == "windows" {
		libPathCmd = "import sys; print(sys.executable)"
	} else {
		libPathCmd = "import sysconfig; print(sysconfig.get_config_var('LIBDIR'))"
	}

	libPathCmdExec := exec.Command(newEnv.PythonPath, "-c", libPathCmd)
	libPathOutput, err := libPathCmdExec.Output()
	if err != nil {
		return nil, fmt.Errorf("error getting Python lib path: %v", err)
	}

	newEnv.PythonLibPath = strings.TrimSpace(string(libPathOutput))
	if runtime.GOOS != "windows" {
		newEnv.PythonLibPath = filepath.Join(newEnv.PythonLibPath, fmt.Sprintf("libpython%s.so", newEnv.PythonVersion.MinorString()))
	}

	// Get Python headers path
	headersPathCmd := "import sysconfig; print(sysconfig.get_path('include'))"
	headersPathCmdExec := exec.Command(newEnv.PythonPath, "-c", headersPathCmd)
	headersPathOutput, err := headersPathCmdExec.Output()
	if err != nil {
		return nil, fmt.Errorf("error getting Python headers path: %v", err)
	}

	newEnv.PythonHeadersPath = strings.TrimSpace(string(headersPathOutput))

	// Set EnvLibPath
	newEnv.EnvLibPath = filepath.Dir(newEnv.PythonLibPath)

	// Micromamba is not applicable for venv, so we'll set these to empty
	newEnv.MicromambaPath = ""
	newEnv.MicromambaVersion = Version{}

	if progressCallback != nil {
		progressCallback("Virtual environment setup complete", 100, 100)
	}

	return newEnv, nil
}

// FreezeToFile saves the environment specification to a JSON file.
//
// The output includes:
//   - Environment name and Python version
//   - Conda packages with versions and build strings (if micromamba environment)
//   - Pip packages with versions
//   - Conda channels used
//
// Packages installed via pip are not duplicated in the conda package list.
// The resulting JSON file can be used with CreateEnvironmentFromJSONFile to
// recreate an identical environment.
//
// File URLs in pip freeze output are cleaned to show only package names.
func (env *PythonEnvironment) FreezeToFile(filePath string) error {
	spec := EnvironmentSpec{
		Name:          env.EnvironmentName,
		CondaPackages: []string{},
		PipPackages:   []string{},
		PythonVersion: env.PythonVersion.MinorString(),
	}

	if env.MicromambaVersion.Major != -1 { //check if valid version
		spec.MicromambaVersion = env.MicromambaVersion.String()
	}

	// we'll need one or both of these
	if env.MicromambaPath == "" && env.PipPath == "" {
		return fmt.Errorf("no micromamba or pip path found")
	}

	// --- 1. Get pip packages (if pip is available) FIRST ---
	if env.PipPath != "" {
		pipCmd := exec.Command(env.PipPath, "freeze")
		pipOutput, pipErr := pipCmd.Output()
		if pipErr != nil {
			return fmt.Errorf("error running pip freeze: %v", pipErr)
		}

		// Clean up pip freeze output (remove file URLs).
		var cleanedPipOutput bytes.Buffer
		scanner := bufio.NewScanner(bytes.NewReader(pipOutput))
		fileURLRegex := regexp.MustCompile(`^(.+) @ file:///.+$`)

		for scanner.Scan() {
			line := scanner.Text()
			match := fileURLRegex.FindStringSubmatch(line)
			if len(match) > 1 {
				cleanedPipOutput.WriteString(match[1] + "\n")
			} else {
				cleanedPipOutput.WriteString(line + "\n")
			}
		}

		// Add cleaned pip packages to spec.PipPackages.
		scanner = bufio.NewScanner(bytes.NewReader(cleanedPipOutput.Bytes()))
		for scanner.Scan() {
			line := scanner.Text()
			// Split the line to handle comments
			parts := strings.SplitN(line, "#", 2)
			packageSpec := strings.TrimSpace(parts[0]) // Take only the part before the comment
			if packageSpec != "" {
				spec.PipPackages = append(spec.PipPackages, packageSpec)
			}
		}
	}
	// --- End of Pip Package Handling ---

	// 2. Get conda packages (if micromamba is available).
	if env.MicromambaPath != "" {
		cmd := exec.Command(env.MicromambaPath, "list", "-n", env.EnvironmentName, "--json")
		cmd.Env = append(os.Environ(), "MAMBA_ROOT_PREFIX="+env.RootDir)
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("error running micromamba list: %v - %s", err, string(output))
		}

		var packages []map[string]interface{}
		if err := json.Unmarshal(output, &packages); err != nil {
			return fmt.Errorf("error parsing micromamba list JSON output: %v", err)
		}

		// Create a set of pip package names for efficient duplicate checking.
		pipPackageNames := make(map[string]bool)
		for _, pkg := range spec.PipPackages {
			parts := strings.SplitN(pkg, "==", 2) // Split name and version
			if len(parts) > 0 {
				pipPackageNames[strings.ToLower(parts[0])] = true // Lowercase for case-insensitive comparison
			}
		}

		// Extract relevant information and add to spec.CondaPackages.
		for _, pkg := range packages {
			name, nameOk := pkg["name"].(string)
			version, versionOk := pkg["version"].(string)
			channel, channelOk := pkg["channel"].(string)
			if !nameOk || !versionOk {
				continue // Skip if name or version is missing
			}
			buildString, buildStringOk := pkg["build_string"].(string)

			// --- KEY CHANGE:  Check for Duplicates ---
			if _, ok := pipPackageNames[strings.ToLower(name)]; ok {
				continue // Skip this package if it's already in pipPackages
			}
			// --- End of Key Change ---

			var packageString string
			if buildStringOk {
				packageString = fmt.Sprintf("%s=%s=%s", name, version, buildString)
			} else {
				packageString = fmt.Sprintf("%s=%s", name, version)
			}
			spec.CondaPackages = append(spec.CondaPackages, packageString)

			if channelOk {
				found := false
				for _, c := range spec.Channels {
					if c == channel {
						found = true
						break
					}
				}
				if !found {
					spec.Channels = append(spec.Channels, channel)
				}
			}
		}
	}

	// 3. Marshal the EnvironmentSpec to JSON.
	jsonData, err := json.MarshalIndent(spec, "", "  ") // Use MarshalIndent for readability
	if err != nil {
		return fmt.Errorf("error marshaling environment spec to JSON: %v", err)
	}

	// 4. Write the JSON data to the file.
	if err := os.WriteFile(filePath, jsonData, 0644); err != nil {
		return fmt.Errorf("error writing JSON to file: %v", err)
	}

	return nil
}

// CreateEnvironmentFromJSONFile creates a new environment from a JSON specification file.
//
// The JSON file should match the EnvironmentSpec format, typically created by FreezeToFile.
// This function:
//  1. Creates a base micromamba environment with the specified Python version
//  2. Installs all conda packages from the spec
//  3. Installs all pip packages from the spec
//
// If no channels are specified in the JSON, "conda-forge" is used as the default.
// The environment is created at rootDir/envs/<name> where name comes from the spec.
func CreateEnvironmentFromJSONFile(filePath string, rootDir string, progressCallback ProgressCallback) (*PythonEnvironment, error) {
	// 1. Read the JSON file.
	jsonData, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("error reading JSON file: %v", err)
	}

	// 2. Unmarshal the JSON data into an EnvironmentSpec.
	var spec EnvironmentSpec
	if err := json.Unmarshal(jsonData, &spec); err != nil {
		return nil, fmt.Errorf("error unmarshaling JSON: %v", err)
	}

	// 3. Create the base environment (using the specified Python version, if any).
	env, err := CreateEnvironmentMamba(spec.Name, rootDir, spec.PythonVersion, "", progressCallback) // Pass empty string for channel initially.
	if err != nil {
		return nil, fmt.Errorf("error creating base environment: %v", err)
	}

	// Determine the channels to use. If not in the file, default to conda-forge
	channels := spec.Channels
	if len(channels) == 0 {
		channels = []string{"conda-forge"} // Default channel
	}

	// 4. Install conda packages.
	for _, pkg := range spec.CondaPackages {
		// Install using the specified channels
		var installErr error
		for _, channel := range channels {
			if err := env.MicromambaInstallPackage(pkg, channel); err == nil {
				installErr = nil // Success on at least one channel
				break            // Exit the inner loop (try next channel)
			} else {
				installErr = err // Keep track of the last error
			}
		}
		if installErr != nil {
			return nil, fmt.Errorf("error installing conda package %s: %v", pkg, installErr) // Report the final error
		}
		if progressCallback != nil {
			progressCallback(fmt.Sprintf("Installing conda package %s...", pkg), 50, 100)
		}
	}

	// 5. Install pip packages.
	if len(spec.PipPackages) > 0 {
		if err := env.PipInstallPackages(spec.PipPackages, "https://pypi.org/simple", "", true, progressCallback); err != nil {
			return nil, fmt.Errorf("error installing pip packages: %v", err)
		}
	}

	if progressCallback != nil {
		progressCallback("Finished creating environment from JSON file", 100, 100)
	}
	return env, nil
}

// CreateEnvironmentFromJSONFileWithOptions creates a new environment from a JSON
// specification file with additional security options for checksum verification.
//
// This function extends CreateEnvironmentFromJSONFile with optional SHA256 verification
// for packages that include checksums in their PackageSpec.
//
// Parameters:
//   - filePath: Path to the JSON specification file
//   - rootDir: Root directory where the environment will be created
//   - opts: RestoreOptions controlling checksum verification behavior
//   - progressCallback: Optional callback for progress updates; may be nil
//
// If opts.VerifyChecksums is true, packages with SHA256 checksums will be verified.
// If opts.Strict is also true, the function will fail if any package lacks a checksum.
func CreateEnvironmentFromJSONFileWithOptions(filePath string, rootDir string, opts RestoreOptions, progressCallback ProgressCallback) (*PythonEnvironment, error) {
	// 1. Read the JSON file.
	jsonData, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("error reading JSON file: %v", err)
	}

	// 2. Unmarshal the JSON data into an EnvironmentSpec.
	var spec EnvironmentSpec
	if err := json.Unmarshal(jsonData, &spec); err != nil {
		return nil, fmt.Errorf("error unmarshaling JSON: %v", err)
	}

	// 3. If Strict mode and VerifyChecksums enabled, check that all packages have checksums.
	if opts.Strict && opts.VerifyChecksums {
		for _, pkg := range spec.Packages {
			if pkg.SHA256 == "" {
				return nil, fmt.Errorf("strict mode: package %s lacks SHA256 checksum", pkg.Name)
			}
		}
	}

	// 4. Create the base environment.
	env, err := CreateEnvironmentMamba(spec.Name, rootDir, spec.PythonVersion, "", progressCallback)
	if err != nil {
		return nil, fmt.Errorf("error creating base environment: %v", err)
	}

	// Determine the channels to use.
	channels := spec.Channels
	if len(channels) == 0 {
		channels = []string{"conda-forge"}
	}

	// 5. Install packages from the unified Packages list if present.
	for _, pkg := range spec.Packages {
		if pkg.Source == "conda" {
			// Install conda package
			pkgSpec := pkg.Name + "=" + pkg.Version
			if pkg.Build != "" {
				pkgSpec += "=" + pkg.Build
			}
			var installErr error
			for _, channel := range channels {
				if err := env.MicromambaInstallPackage(pkgSpec, channel); err == nil {
					installErr = nil
					break
				} else {
					installErr = err
				}
			}
			if installErr != nil {
				return nil, fmt.Errorf("error installing conda package %s: %v", pkg.Name, installErr)
			}
		} else if pkg.Source == "pip" {
			// Install pip package
			pkgSpec := pkg.Name + "==" + pkg.Version
			if err := env.PipInstallPackage(pkgSpec, "https://pypi.org/simple", "", true, progressCallback); err != nil {
				return nil, fmt.Errorf("error installing pip package %s: %v", pkg.Name, err)
			}
		}

		if progressCallback != nil {
			progressCallback(fmt.Sprintf("Installing package %s...", pkg.Name), 50, 100)
		}
	}

	// 6. Fall back to legacy format if Packages list is empty.
	if len(spec.Packages) == 0 {
		// Install conda packages from legacy format.
		for _, pkg := range spec.CondaPackages {
			var installErr error
			for _, channel := range channels {
				if err := env.MicromambaInstallPackage(pkg, channel); err == nil {
					installErr = nil
					break
				} else {
					installErr = err
				}
			}
			if installErr != nil {
				return nil, fmt.Errorf("error installing conda package %s: %v", pkg, installErr)
			}
		}

		// Install pip packages from legacy format.
		if len(spec.PipPackages) > 0 {
			if err := env.PipInstallPackages(spec.PipPackages, "https://pypi.org/simple", "", true, progressCallback); err != nil {
				return nil, fmt.Errorf("error installing pip packages: %v", err)
			}
		}
	}

	if progressCallback != nil {
		progressCallback("Finished creating environment from JSON file", 100, 100)
	}
	return env, nil
}
