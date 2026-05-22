package jumpboot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// NodeEnvironment represents a Node.js environment with all necessary paths and
// version information. It is created with micromamba by installing the
// conda-forge "nodejs" package, mirroring how PythonEnvironment is created.
//
// NodeEnvironment implements the Runtime interface and is the Node.js
// counterpart of PythonEnvironment in jumpboot's multi-language design (see
// docs/PROTOCOL.md and runtime_process.go).
type NodeEnvironment struct {
	// BaseEnvironment contains container-agnostic fields.
	BaseEnvironment

	// NodeVersion is the detected Node.js version (e.g., 20.11.1).
	NodeVersion Version

	// NpmVersion is the detected npm version.
	NpmVersion Version

	// NodePath is the full path to the node executable.
	NodePath string

	// NpmPath is the full path to the npm executable.
	NpmPath string

	// NodeModulesPath is the path to the environment's node_modules directory.
	NodeModulesPath string
}

// Name returns the environment identifier. Implements the Runtime interface.
func (env *NodeEnvironment) Name() string { return env.EnvironmentName }

// Path returns the base environment path. Implements the Runtime interface.
func (env *NodeEnvironment) Path() string { return env.EnvPath }

// BinPath returns the path to executables. Implements the Runtime interface.
func (env *NodeEnvironment) BinPath() string { return env.EnvBinPath }

// Freeze serializes the environment to a JSON file for reproducibility.
// Implements the Runtime interface.
func (env *NodeEnvironment) Freeze(filePath string) error {
	return env.FreezeToFile(filePath)
}

// Compile-time assertion that *NodeEnvironment satisfies the Runtime interface.
var _ Runtime = (*NodeEnvironment)(nil)

// NodeEnvironmentSpec is the JSON shape written by FreezeToFile and read back
// for environment reproducibility.
type NodeEnvironmentSpec struct {
	Name              string `json:"name"`
	NodeVersion       string `json:"node_version,omitempty"`
	NpmVersion        string `json:"npm_version,omitempty"`
	MicromambaVersion string `json:"micromamba_version,omitempty"`
}

// FreezeToFile writes a NodeEnvironmentSpec describing this environment to the
// given path as indented JSON.
func (env *NodeEnvironment) FreezeToFile(filePath string) error {
	spec := NodeEnvironmentSpec{
		Name:              env.EnvironmentName,
		NodeVersion:       env.NodeVersion.String(),
		NpmVersion:        env.NpmVersion.String(),
		MicromambaVersion: env.MicromambaVersion.String(),
	}
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling node environment spec: %v", err)
	}
	return os.WriteFile(filePath, data, 0644)
}

// CreateNodeEnvironmentMamba creates a new Node.js environment using micromamba.
// If micromamba is not present in rootDir/bin, it is downloaded automatically.
//
// Parameters:
//   - envName: Name for the new environment (e.g., "myenv")
//   - rootDir: Root directory for micromamba and environments
//   - nodeVersion: Node.js major or major.minor version (e.g., "20"); defaults
//     to "20" if empty. conda-forge does not publish every patch release, so
//     prefer a major (or major.minor) version here rather than an exact patch.
//   - channel: Conda channel to use; "conda-forge" is recommended since the
//     "nodejs" package lives there.
//   - progressCallback: Optional callback for progress updates; may be nil
//
// The environment is created at rootDir/envs/envName. If it already exists, it
// is reused and IsNew will be false.
func CreateNodeEnvironmentMamba(envName string, rootDir string, nodeVersion string, channel string, progressCallback ProgressCallback) (*NodeEnvironment, error) {
	if nodeVersion == "" {
		nodeVersion = "20"
	}

	// Create (or reuse) the conda environment with the runtime-agnostic helper.
	base, err := createCondaEnvironment(envName, rootDir, []string{"nodejs=" + nodeVersion}, channel, "Node.js", progressCallback)
	if err != nil {
		return nil, err
	}

	env := &NodeEnvironment{BaseEnvironment: *base}
	platform := runtime.GOOS

	// Construct the full paths to the node and npm executables.
	if platform == "windows" {
		env.NodePath = filepath.Join(env.EnvPath, "node.exe")
		env.NpmPath = filepath.Join(env.EnvPath, "npm.cmd")
		env.NodeModulesPath = filepath.Join(env.EnvPath, "node_modules")
	} else {
		env.NodePath = filepath.Join(env.EnvBinPath, "node")
		env.NpmPath = filepath.Join(env.EnvBinPath, "npm")
		env.NodeModulesPath = filepath.Join(env.EnvPath, "lib", "node_modules")
	}

	// Check that the node executable exists and get its version.
	nver, err := RunReadStdout(env.NodePath, "--version")
	if err != nil {
		return nil, fmt.Errorf("error running node --version: %v", err)
	}
	env.NodeVersion, err = ParseNodeVersion(nver)
	if err != nil {
		return nil, fmt.Errorf("error parsing Node.js version: %v", err)
	}

	// npm is optional for running embedded code, but record its version when present.
	if npmver, err := RunReadStdout(env.NpmPath, "--version"); err == nil {
		if v, err := ParseVersion(strings.TrimSpace(npmver)); err == nil {
			env.NpmVersion = v
		}
	}

	return env, nil
}

// ParseNodeVersion parses output from "node --version" (e.g., "v20.11.1").
// The leading "v" that Node prints is stripped before parsing.
func ParseNodeVersion(versionStr string) (Version, error) {
	trimmed := strings.TrimSpace(versionStr)
	trimmed = strings.TrimPrefix(trimmed, "v")
	return ParseVersion(trimmed)
}
