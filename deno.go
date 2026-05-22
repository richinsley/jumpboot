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

// denoSDKSource is the embedded Deno runtime SDK (a single self-contained JS
// file). It is concatenated ahead of a plugin and run with `deno eval`.
//
//go:embed packages/jumpboot-deno/jumpboot.js
var denoSDKSource string

// DenoEnvironment represents a Deno environment created with micromamba by
// installing the conda-forge "deno" package. It is the Deno counterpart of
// PythonEnvironment / NodeEnvironment.
type DenoEnvironment struct {
	// BaseEnvironment contains container-agnostic fields.
	BaseEnvironment

	// DenoVersion is the detected Deno version (e.g., 2.1.4).
	DenoVersion Version

	// DenoPath is the full path to the deno executable.
	DenoPath string
}

// Name returns the environment identifier. Implements the Runtime interface.
func (env *DenoEnvironment) Name() string { return env.EnvironmentName }

// Path returns the base environment path. Implements the Runtime interface.
func (env *DenoEnvironment) Path() string { return env.EnvPath }

// BinPath returns the path to executables. Implements the Runtime interface.
func (env *DenoEnvironment) BinPath() string { return env.EnvBinPath }

// Freeze records the environment's identity and Deno version as JSON.
// Implements the Runtime interface.
func (env *DenoEnvironment) Freeze(filePath string) error {
	spec := fmt.Sprintf("{\n  \"name\": %q,\n  \"deno_version\": %q,\n  \"micromamba_version\": %q\n}\n",
		env.EnvironmentName, env.DenoVersion.String(), env.MicromambaVersion.String())
	return os.WriteFile(filePath, []byte(spec), 0644)
}

// Compile-time assertion that *DenoEnvironment satisfies the Runtime interface.
var _ Runtime = (*DenoEnvironment)(nil)

// DenoProgram defines a Deno plugin: the JavaScript source of its program.
// The source runs in the same scope as the embedded jumpboot Deno SDK, so it
// can subclass MessagePackQueueServer directly without any import.
type DenoProgram struct {
	// Name identifies the program (used for logging).
	Name string

	// PluginSource is the JavaScript source of the plugin.
	PluginSource string
}

// CreateDenoEnvironmentMamba creates a new Deno environment using micromamba.
// If micromamba is not present in rootDir/bin, it is downloaded automatically.
//
// denoVersion may be empty (conda picks a version) or a version spec such as
// "2". channel should normally be "conda-forge".
func CreateDenoEnvironmentMamba(envName string, rootDir string, denoVersion string, channel string, progressCallback ProgressCallback) (*DenoEnvironment, error) {
	pkg := "deno"
	if denoVersion != "" {
		pkg = "deno=" + denoVersion
	}
	base, err := createCondaEnvironment(envName, rootDir, []string{pkg}, channel, "Deno", progressCallback)
	if err != nil {
		return nil, err
	}

	env := &DenoEnvironment{BaseEnvironment: *base}
	if runtime.GOOS == "windows" {
		env.DenoPath = filepath.Join(env.EnvPath, "deno.exe")
	} else {
		env.DenoPath = filepath.Join(env.EnvBinPath, "deno")
	}

	dver, err := RunReadStdout(env.DenoPath, "--version")
	if err != nil {
		return nil, fmt.Errorf("error running deno --version: %v", err)
	}
	env.DenoVersion, err = ParseDenoVersion(dver)
	if err != nil {
		return nil, fmt.Errorf("error parsing Deno version: %v", err)
	}
	return env, nil
}

// ParseDenoVersion parses output from "deno --version". The first line has the
// form "deno X.Y.Z (...)"; later lines (v8, typescript) are ignored.
func ParseDenoVersion(versionStr string) (Version, error) {
	firstLine := versionStr
	if i := strings.IndexByte(versionStr, '\n'); i >= 0 {
		firstLine = versionStr[:i]
	}
	fields := strings.Fields(firstLine)
	if len(fields) < 2 || fields[0] != "deno" {
		return Version{}, fmt.Errorf("invalid deno version string: %q", firstLine)
	}
	return ParseVersion(fields[1])
}

// NewQueueProcess starts a Deno process with bidirectional RPC communication
// and returns a QueueProcess driving it. It is the Deno counterpart of
// PythonEnvironment.NewQueueProcess and reuses the language-neutral
// newQueueProcess core.
//
// The plugin source is concatenated after the embedded Deno SDK and executed
// with `deno eval`; the queue protocol travels over the process's stdin/stdout
// (see stdioProcess). The returned QueueProcess has a nil PythonProcess field.
func (env *DenoEnvironment) NewQueueProcess(program *DenoProgram, serviceStruct interface{}) (*QueueProcess, error) {
	source := denoSDKSource + "\n" + program.PluginSource
	cmd := exec.Command(env.DenoPath, "eval", source)
	cmd.Env = os.Environ()

	sp, err := newStdioProcess(cmd)
	if err != nil {
		return nil, err
	}
	return newQueueProcess(sp, serviceStruct)
}
