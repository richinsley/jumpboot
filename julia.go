package jumpboot

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// juliaSDKSource is the embedded Julia runtime SDK (a single self-contained
// .jl file). It is concatenated ahead of a plugin and run with `julia -e`.
//
//go:embed packages/jumpboot-julia/jumpboot.jl
var juliaSDKSource string

// JuliaEnvironment represents a Julia environment created with micromamba by
// installing the conda-forge "julia" package. It is the Julia counterpart of
// PythonEnvironment / NodeEnvironment.
type JuliaEnvironment struct {
	// BaseEnvironment contains container-agnostic fields.
	BaseEnvironment

	// JuliaVersion is the detected Julia version (e.g., 1.10.4).
	JuliaVersion Version

	// JuliaPath is the full path to the julia executable.
	JuliaPath string
}

// Name returns the environment identifier. Implements the Runtime interface.
func (env *JuliaEnvironment) Name() string { return env.EnvironmentName }

// Path returns the base environment path. Implements the Runtime interface.
func (env *JuliaEnvironment) Path() string { return env.EnvPath }

// BinPath returns the path to executables. Implements the Runtime interface.
func (env *JuliaEnvironment) BinPath() string { return env.EnvBinPath }

// Freeze records the environment's identity and Julia version as JSON.
// Implements the Runtime interface.
func (env *JuliaEnvironment) Freeze(filePath string) error {
	spec := fmt.Sprintf("{\n  \"name\": %q,\n  \"julia_version\": %q,\n  \"micromamba_version\": %q\n}\n",
		env.EnvironmentName, env.JuliaVersion.String(), env.MicromambaVersion.String())
	return os.WriteFile(filePath, []byte(spec), 0644)
}

// Compile-time assertion that *JuliaEnvironment satisfies the Runtime interface.
var _ Runtime = (*JuliaEnvironment)(nil)

// JuliaProgram defines a Julia plugin: the Julia source of its program. The
// source runs in the same scope as the embedded jumpboot Julia SDK, so it can
// call register(...) and serve() directly without any import.
type JuliaProgram struct {
	// Name identifies the program (used for logging).
	Name string

	// PluginSource is the Julia source of the plugin.
	PluginSource string
}

// CreateJuliaEnvironmentMamba creates a new Julia environment using micromamba.
// If micromamba is not present in rootDir/bin, it is downloaded automatically.
//
// juliaVersion may be empty (conda picks a version) or a version spec such as
// "1.10". channel should normally be "conda-forge".
func CreateJuliaEnvironmentMamba(envName string, rootDir string, juliaVersion string, channel string, progressCallback ProgressCallback) (*JuliaEnvironment, error) {
	pkg := "julia"
	if juliaVersion != "" {
		pkg = "julia=" + juliaVersion
	}
	base, err := createCondaEnvironment(envName, rootDir, []string{pkg}, channel, "Julia", progressCallback)
	if err != nil {
		return nil, err
	}

	env := &JuliaEnvironment{BaseEnvironment: *base}
	if runtime.GOOS == "windows" {
		env.JuliaPath = filepath.Join(env.EnvBinPath, "julia.exe")
	} else {
		env.JuliaPath = filepath.Join(env.EnvBinPath, "julia")
	}

	jver, err := RunReadStdout(env.JuliaPath, "--version")
	if err != nil {
		return nil, fmt.Errorf("error running julia --version: %v", err)
	}
	env.JuliaVersion, err = ParseJuliaVersion(jver)
	if err != nil {
		return nil, fmt.Errorf("error parsing Julia version: %v", err)
	}
	return env, nil
}

// ParseJuliaVersion parses output from "julia --version" (e.g.,
// "julia version 1.10.4").
func ParseJuliaVersion(versionStr string) (Version, error) {
	fields := strings.Fields(versionStr)
	if len(fields) < 3 || fields[0] != "julia" || fields[1] != "version" {
		return Version{}, fmt.Errorf("invalid julia version string: %q", strings.TrimSpace(versionStr))
	}
	return ParseVersion(fields[2])
}

// NewQueueProcess starts a Julia process with bidirectional RPC communication
// and returns a QueueProcess driving it. It is the Julia counterpart of
// PythonEnvironment.NewQueueProcess and reuses the language-neutral
// newQueueProcess core.
//
// The plugin source is concatenated after the embedded Julia SDK and executed
// with `julia -e`; the queue protocol travels over the process's stdin/stdout
// (see stdioProcess). The returned QueueProcess has a nil PythonProcess field.
func (env *JuliaEnvironment) NewQueueProcess(program *JuliaProgram, serviceStruct interface{}) (*QueueProcess, error) {
	source := juliaSDKSource + "\n" + program.PluginSource
	cmd := exec.Command(env.JuliaPath, "--startup-file=no", "--quiet", "-e", source)
	cmd.Env = os.Environ()

	sp, err := newStdioProcess(cmd)
	if err != nil {
		return nil, err
	}
	return newQueueProcess(sp, serviceStruct)
}
