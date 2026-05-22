package jumpboot

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// The WASM guest SDK source is embedded so it can be written into a temporary,
// self-contained build directory and compiled alongside the user's plugin.
// The files also exist as a real package (./wasmguest) for tooling.

//go:embed wasmguest/guest.go
var wasmGuestSource string

//go:embed wasmguest/msgpack.go
var wasmGuestMsgpackSource string

// WasmEnvironment provides the Go toolchain used to compile jumpboot WASM
// plugins to wasip1/wasm. Unlike PythonEnvironment / NodeEnvironment it does
// not manage a language runtime — the runtime is wazero, embedded in-process
// (see wasmproc.go) — it manages only the compiler.
type WasmEnvironment struct {
	// GoPath is the full path to the `go` executable used for compilation.
	GoPath string
}

// CreateWasmEnvironment returns a WasmEnvironment backed by the Go toolchain
// found on PATH. The toolchain must support the wasip1/wasm target (Go 1.21+).
func CreateWasmEnvironment() (*WasmEnvironment, error) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		return nil, fmt.Errorf("go toolchain not found on PATH: %w", err)
	}
	return &WasmEnvironment{GoPath: goPath}, nil
}

// CreateWasmEnvironmentMamba creates a conda environment containing a pinned
// Go toolchain (the conda-forge "go" package) and returns a WasmEnvironment
// using it. This gives reproducible plugin builds independent of any system Go.
//
// goVersion may be empty (conda picks a version) or a version spec such as
// "1.23". channel should normally be "conda-forge".
func CreateWasmEnvironmentMamba(envName string, rootDir string, goVersion string, channel string, progressCallback ProgressCallback) (*WasmEnvironment, error) {
	pkg := "go"
	if goVersion != "" {
		pkg = "go=" + goVersion
	}
	base, err := createCondaEnvironment(envName, rootDir, []string{pkg}, channel, "Go", progressCallback)
	if err != nil {
		return nil, err
	}
	goPath := filepath.Join(base.EnvBinPath, "go")
	if _, err := os.Stat(goPath); err != nil {
		return nil, fmt.Errorf("go executable not found in conda environment at %s: %w", goPath, err)
	}
	return &WasmEnvironment{GoPath: goPath}, nil
}

// WasmProgram defines a WASM plugin: the Go source of its main package. The
// plugin imports github.com/richinsley/jumpboot/wasmguest, registers handlers,
// and calls wasmguest.Serve (see the wasmguest package documentation).
type WasmProgram struct {
	// Name identifies the program (used as the module name and for logging).
	Name string

	// PluginSource is the complete Go source of the plugin's main.go.
	PluginSource string
}

// compilePlugin writes a self-contained build directory — a go.mod, the
// embedded wasmguest SDK, and the plugin's main.go — and compiles it to
// wasip1/wasm, returning the module bytes. The build directory is removed
// before returning. The build needs no network: the module is stdlib-only.
func (env *WasmEnvironment) compilePlugin(program *WasmProgram) ([]byte, error) {
	buildDir, err := os.MkdirTemp("", "jumpboot-wasm-build")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(buildDir)

	// A self-contained module. Its path matches jumpboot's so the plugin can
	// import "github.com/richinsley/jumpboot/wasmguest"; the local wasmguest
	// directory satisfies that import without any external dependency.
	gomod := "module github.com/richinsley/jumpboot\n\ngo 1.23\n"
	if err := os.WriteFile(filepath.Join(buildDir, "go.mod"), []byte(gomod), 0644); err != nil {
		return nil, err
	}

	guestDir := filepath.Join(buildDir, "wasmguest")
	if err := os.MkdirAll(guestDir, 0755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(guestDir, "guest.go"), []byte(wasmGuestSource), 0644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(guestDir, "msgpack.go"), []byte(wasmGuestMsgpackSource), 0644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(buildDir, "main.go"), []byte(program.PluginSource), 0644); err != nil {
		return nil, err
	}

	outPath := filepath.Join(buildDir, "plugin.wasm")
	cmd := exec.Command(env.GoPath, "build", "-o", outPath, ".")
	cmd.Dir = buildDir
	// GOWORK=off keeps a stray go.work above the temp dir from interfering.
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("wasm plugin build failed: %v\n%s", err, out)
	}

	return os.ReadFile(outPath)
}
