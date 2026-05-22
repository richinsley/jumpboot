package jumpboot

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// The Node.js test environment is created once per test run and cached. It is
// placed at a fixed path under the OS temp directory so repeated `go test`
// runs reuse the same micromamba environment instead of recreating it.
var (
	testNodeEnvOnce sync.Once
	testNodeEnv     *NodeEnvironment
	testNodeEnvErr  error
)

// getTestNodeEnv creates (or reuses) a micromamba-managed Node.js environment
// for tests. The first call is slow — it may download micromamba and the
// nodejs package — but the environment persists between runs. Tests that need
// Node are skipped (not failed) if the environment cannot be created, e.g.
// when the machine is offline.
func getTestNodeEnv(t *testing.T) *NodeEnvironment {
	t.Helper()
	testNodeEnvOnce.Do(func() {
		root := filepath.Join(os.TempDir(), "jumpboot-node-testenv")
		if err := os.MkdirAll(root, 0755); err != nil {
			testNodeEnvErr = err
			return
		}
		testNodeEnv, testNodeEnvErr = CreateNodeEnvironmentMamba("nodeenv", root, "20", "conda-forge", nil)
	})
	if testNodeEnvErr != nil {
		t.Skipf("could not create Node.js test environment: %v", testNodeEnvErr)
	}
	return testNodeEnv
}

func TestCreateNodeEnvironmentMamba(t *testing.T) {
	env := getTestNodeEnv(t)

	if env.NodeVersion.Major < 1 {
		t.Errorf("expected a valid Node.js version, got %q", env.NodeVersion.String())
	}
	if env.EnvironmentName != "nodeenv" {
		t.Errorf("expected environment name 'nodeenv', got %q", env.EnvironmentName)
	}
	if _, err := os.Stat(env.NodePath); err != nil {
		t.Errorf("node executable not found at %s: %v", env.NodePath, err)
	}

	// A second creation call against the same root must reuse the environment.
	env2, err := CreateNodeEnvironmentMamba("nodeenv", env.RootDir, "20", "conda-forge", nil)
	if err != nil {
		t.Fatalf("second CreateNodeEnvironmentMamba call failed: %v", err)
	}
	if env2.IsNew {
		t.Error("expected IsNew to be false when reusing an existing environment")
	}
}

func TestNodeEnvironment_RuntimeInterface(t *testing.T) {
	// Compile-time assertion var _ Runtime = (*NodeEnvironment)(nil) lives in
	// node.go; this test exercises the accessors at runtime.
	env := &NodeEnvironment{
		BaseEnvironment: BaseEnvironment{
			EnvironmentName: "demo",
			EnvPath:         "/tmp/demo",
			EnvBinPath:      "/tmp/demo/bin",
		},
	}
	var rt Runtime = env
	if rt.Name() != "demo" || rt.Path() != "/tmp/demo" || rt.BinPath() != "/tmp/demo/bin" {
		t.Errorf("Runtime accessors returned unexpected values: %q %q %q",
			rt.Name(), rt.Path(), rt.BinPath())
	}
}
