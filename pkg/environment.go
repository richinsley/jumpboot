package pkg

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type Environment struct {
	Name              string  // Name of the environment
	RootDir           string  // Root directory of the environment
	EnvPath           string  // Path to the environment
	EnvBinPath        string  // Path to the bin directory within the environment
	EnvLibPath        string  // Path to the lib directory within the environment
	PythonVersion     Version // Version of the Python environment
	MicromambaVersion Version // Version of the micromamba executable
	PipVersion        Version // Version of the pip executable
	MicromambaPath    string  // Path to the micromamba executable
	PythonPath        string  // Path to the Python executable within the environment
	PythonLibPath     string  // Path to the Python library within the environment
	PipPath           string  // Path to the pip executable within the environment
	PythonHeadersPath string  // Path to the Python headers within the environment
	SitePackagesPath  string  // Path to the site-packages directory within the environment
	IsNew             bool    // Whether the environment was newly created
}

type VenvOptions struct {
	SystemSitePackages bool
	Symlinks           bool
	Copies             bool
	Clear              bool
	Upgrade            bool
	WithoutPip         bool
	Prompt             string
	UpgradeDeps        bool
}

// user feedback options for CreateEnvironment
type CreateEnvironmentOptions int

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

func CreateEnvironment(envName string, rootDir string, pythonVersion string, channel string, progressCallback ProgressCallback) (*Environment, error) {
	if pythonVersion == "" {
		pythonVersion = "3.10"
	}

	requestedVersion, err := ParseVersion(pythonVersion)
	if err != nil {
		return nil, fmt.Errorf("error parsing requested python version: %v", err)
	}

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

	// Create the environment object
	env := &Environment{
		Name:           envName,
		RootDir:        rootDir,
		MicromambaPath: filepath.Join(binDirectory, executableName),
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
	envPath := filepath.Join(env.RootDir, "envs", env.Name)
	if _, err := os.Stat(envPath); os.IsNotExist(err) {
		// this is a new environment
		env.IsNew = true

		// Create a new Python environment with micromamba
		cmdargs := []string{"--root-prefix", env.RootDir, "create", "-n", env.Name, "python=" + pythonVersion, "-y"}
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
				progressCallback("Creating Python environment...", int64(lineCount), -1)
			}
		}

		if err := createEnvCmd.Wait(); err != nil {
			return nil, fmt.Errorf("error creating environment: %v", err)
		}

		if progressCallback != nil {
			progressCallback("Python environment created successfully", 100, 100)
		}
	}

	// Construct the full paths to the Python and pip executables within the created environment
	env.EnvPath = envPath
	if platform == "windows" {
		env.EnvBinPath = filepath.Join(env.RootDir, "envs", env.Name)
		env.PythonPath = filepath.Join(env.EnvBinPath, "python.exe")
		env.PipPath = filepath.Join(env.RootDir, "envs", env.Name, "Scripts", "pip.exe")
	} else {
		env.EnvBinPath = filepath.Join(env.RootDir, "envs", env.Name, "bin")
		env.PythonPath = filepath.Join(env.EnvBinPath, "python")
		env.PipPath = filepath.Join(env.EnvBinPath, "pip")
	}

	env.SitePackagesPath = filepath.Join(env.RootDir, "envs", env.Name, "lib", "python"+requestedVersion.MinorString(), "site-packages")

	// find the python lib path
	env.EnvLibPath = filepath.Join(env.RootDir, "envs", env.Name, "lib")
	env.PythonLibPath = env.EnvLibPath
	if platform == "windows" {
		env.PythonLibPath = filepath.Join(env.RootDir, "envs", env.Name, "python"+requestedVersion.MinorStringCompact()+".dll")
	} else if platform == "darwin" {
		env.PythonLibPath = filepath.Join(env.RootDir, "envs", env.Name, "lib", "libpython"+requestedVersion.MinorString()+".dylib")
	} else {
		env.PythonLibPath = filepath.Join(env.RootDir, "envs", env.Name, "lib", "libpython"+requestedVersion.MinorString()+".so")
	}

	// find the python headers path
	env.PythonHeadersPath = filepath.Join(env.RootDir, "envs", env.Name, "include", "python"+requestedVersion.MinorString())

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

func CreateEnvironmentFromSystem(progressCallback ProgressCallback) (*Environment, error) {
	env := &Environment{
		Name:    "system",
		RootDir: "", // Will be set based on the system Python path
		IsNew:   false,
	}

	// Find system Python
	pythonCmd := "python"
	if runtime.GOOS == "windows" {
		pythonCmd = "python.exe"
	}

	// Try to get Python path
	pythonPath, err := exec.LookPath(pythonCmd)
	if err != nil {
		return nil, fmt.Errorf("system Python not found: %v", err)
	}

	env.PythonPath = pythonPath
	env.RootDir = filepath.Dir(filepath.Dir(pythonPath))

	if progressCallback != nil {
		progressCallback("Found system Python", 10, 100)
	}

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

	if progressCallback != nil {
		progressCallback("Got Python version", 20, 100)
	}

	// Get site-packages path
	sitePackagesCmd := exec.Command(pythonPath, "-c", "import site; print(site.getsitepackages()[0])")
	sitePackagesOutput, err := sitePackagesCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("error getting site-packages path: %v", err)
	}

	env.SitePackagesPath = strings.TrimSpace(string(sitePackagesOutput))

	if progressCallback != nil {
		progressCallback("Got site-packages path", 30, 100)
	}

	// Get pip path
	pipCmd := "pip"
	if runtime.GOOS == "windows" {
		pipCmd = "pip.exe"
	}

	env.PipPath, err = exec.LookPath(pipCmd)
	if err != nil {
		return nil, fmt.Errorf("pip not found: %v", err)
	}

	if progressCallback != nil {
		progressCallback("Found pip", 40, 100)
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

	if progressCallback != nil {
		progressCallback("Got pip version", 50, 100)
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

	if progressCallback != nil {
		progressCallback("Got Python lib path", 60, 100)
	}

	// Get Python headers path
	headersPathCmd := "import sysconfig; print(sysconfig.get_path('include'))"
	headersPathCmdExec := exec.Command(pythonPath, "-c", headersPathCmd)
	headersPathOutput, err := headersPathCmdExec.Output()
	if err != nil {
		return nil, fmt.Errorf("error getting Python headers path: %v", err)
	}

	env.PythonHeadersPath = strings.TrimSpace(string(headersPathOutput))

	if progressCallback != nil {
		progressCallback("Got Python headers path", 70, 100)
	}

	// Set EnvLibPath
	env.EnvLibPath = filepath.Dir(env.PythonLibPath)

	// Micromamba is not applicable for system Python, so we'll set these to empty
	env.MicromambaPath = ""
	env.MicromambaVersion = Version{}

	if progressCallback != nil {
		progressCallback("Environment setup complete", 100, 100)
	}

	return env, nil
}

func CreateVenvEnvironment(baseEnv *Environment, venvPath string, options VenvOptions, progressCallback ProgressCallback) (*Environment, error) {
	if baseEnv == nil {
		return nil, fmt.Errorf("base environment is nil")
	}

	// Check if the environment already exists
	envExists := false
	if _, err := os.Stat(venvPath); err == nil {
		envExists = true
	}

	// Create a new Environment object
	newEnv := &Environment{
		Name:    filepath.Base(venvPath),
		RootDir: venvPath,
		IsNew:   !envExists || options.Clear, // Set IsNew if the env doesn't exist or if clear is true
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
	}
	if options.Upgrade {
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
	venvCmd := exec.Command(baseEnv.PythonPath, args...)
	if err := venvCmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to create/update virtual environment: %v", err)
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
