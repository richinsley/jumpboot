package jumpboot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// --------------------------------------------------------------------------------
// Helper Functions
// --------------------------------------------------------------------------------

// createTestDir creates a temporary directory for testing and returns its path.
func createTestDir(t *testing.T) string {
	t.Helper()
	testDir, err := os.MkdirTemp("", "jumpboot-test")
	if err != nil {
		t.Fatalf("Failed to create temporary directory: %v", err)
	}
	return testDir
}

// cleanupTestDir removes the given directory after the test.
func cleanupTestDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.RemoveAll(dir); err != nil {
		t.Errorf("Failed to remove temporary directory: %v", err)
	}
}

// mockProgressCallback is a dummy progress callback for testing.
func mockProgressCallback(message string, current, total int64) {
	// fmt.Printf("Progress: %s (%d/%d)\n", message, current, total) // Optional: Uncomment for debugging
}

// isValidEnvironment checks if the environment object has necessary paths set.
func isValidEnvironment(t *testing.T, env *Environment) {
	t.Helper()
	if env.PythonPath == "" {
		t.Error("PythonPath is empty")
	}
	if env.PipPath == "" {
		t.Error("PipPath is empty")
	}
	if env.SitePackagesPath == "" {
		t.Error("SitePackagesPath is empty")
	}
	if env.PythonLibPath == "" && runtime.GOOS != "windows" { //python lib path might not be available on windows
		t.Error("PythonLibPath is empty")
	}
	if env.PythonHeadersPath == "" {
		t.Error("PythonHeadersPath is empty")
	}
}

// --------------------------------------------------------------------------------
// Test Cases
// --------------------------------------------------------------------------------

func TestCreateEnvironmentMamba(t *testing.T) {
	testDir := createTestDir(t)
	defer cleanupTestDir(t, testDir)

	envName := "testenv"
	pythonVersion := "3.12"

	env, err := CreateEnvironmentMamba(envName, testDir, pythonVersion, "conda-forge", mockProgressCallback)
	if err != nil {
		t.Fatalf("CreateEnvironmentMamba failed: %v", err)
	}

	if env.Name != envName {
		t.Errorf("Expected environment name %s, got %s", envName, env.Name)
	}
	if env.RootDir != testDir {
		t.Errorf("Expected root directory %s, got %s", testDir, env.RootDir)
	}
	if env.MicromambaPath == "" {
		t.Error("MicromambaPath is empty")
	}

	isValidEnvironment(t, env)

	// Check if the environment directory was created
	if _, err := os.Stat(env.EnvPath); os.IsNotExist(err) {
		t.Errorf("Environment directory not created: %v", err)
	}

	// Test creating an environment with an existing name (should not fail, but should not be new)
	env2, err := CreateEnvironmentMamba(envName, testDir, pythonVersion, "conda-forge", mockProgressCallback)
	if err != nil {
		t.Fatalf("CreateEnvironmentMamba failed on second call: %v", err)
	}
	if env2.IsNew {
		t.Error("Second environment creation should not be marked as new")
	}

	// Test with invalid root directory (not writable)
	if runtime.GOOS != "windows" { //This will likely fail on windows since you must be admin
		notWritableDir := "/root/notwritable" // This directory typically exists and is not writable
		_, err = CreateEnvironmentMamba("invalid", notWritableDir, pythonVersion, "conda-forge", mockProgressCallback)
		if err == nil {
			t.Error("Expected error for non-writable root directory, but got none")
		}
	}
}

func TestCreateEnvironmentMamba_InvalidPythonVersion(t *testing.T) {
	testDir := createTestDir(t)
	defer cleanupTestDir(t, testDir)

	_, err := CreateEnvironmentMamba("testenv", testDir, "invalid-version", "conda-forge", mockProgressCallback)
	if err == nil {
		t.Error("Expected error for invalid Python version, but got none")
	}
}

func TestCreateEnvironmentFromSystem(t *testing.T) {
	env, err := CreateEnvironmentFromSystem()
	if err != nil {
		t.Fatalf("CreateEnvironmentFromSystem failed: %v", err)
	}

	isValidEnvironment(t, env)
	if env.Name != "system" {
		t.Errorf("Expected environment name 'system', got '%s'", env.Name)
	}
	if env.IsNew {
		t.Error("System environment should not be marked as new")
	}
}

func TestCreateEnvironmentFromExecutable_NotFound(t *testing.T) {
	_, err := CreateEnvironmentFromExacutable("/path/to/nonexistent/python")
	if err == nil {
		t.Error("Expected error for non-existent Python executable, but got none")
	}
}

func TestCreateVenvEnvironment(t *testing.T) {
	// First, create a base environment using micromamba.
	testDir := createTestDir(t)
	defer cleanupTestDir(t, testDir)

	baseEnv, err := CreateEnvironmentMamba("baseenv", testDir, "3.9", "conda-forge", mockProgressCallback)
	if err != nil {
		t.Fatalf("Failed to create base environment: %v", err)
	}

	// Now, create a venv on top of that.
	venvPath := filepath.Join(testDir, "testvenv")
	options := VenvOptions{} // Use default options.

	venvEnv, err := CreateVenvEnvironment(baseEnv, venvPath, options, mockProgressCallback)
	if err != nil {
		t.Fatalf("CreateVenvEnvironment failed: %v", err)
	}

	isValidEnvironment(t, venvEnv)

	if venvEnv.Name != "testvenv" {
		t.Errorf("Expected venv name 'testvenv', got '%s'", venvEnv.Name)
	}

	// Test with clear option
	clearOptions := VenvOptions{Clear: true}
	venvEnv2, err := CreateVenvEnvironment(baseEnv, venvPath, clearOptions, mockProgressCallback)
	if err != nil {
		t.Fatalf("CreateVenvEnvironment with Clear option failed: %v", err)
	}
	if venvEnv2.Name != "testvenv" {
		t.Errorf("Expected venv name 'testvenv', got '%s'", venvEnv2.Name)
	}

	// Test with no pip
	noPipOptions := VenvOptions{WithoutPip: true}
	_, err = CreateVenvEnvironment(baseEnv, filepath.Join(testDir, "testvenv_nopip"), noPipOptions, mockProgressCallback)
	if err != nil {
		t.Fatalf("CreateVenvEnvironment without pip failed: %v", err)
	}
	// Ensure that there is no pip executable in the new environment
	if _, err := os.Stat(filepath.Join(testDir, "testvenv_nopip", "bin", "pip")); err == nil {
		if runtime.GOOS != "windows" { //check for correct path on other OS
			t.Error("pip should not exist in the environment when WithoutPip is true")
		}
	}
	if _, err := os.Stat(filepath.Join(testDir, "testvenv_nopip", "Scripts", "pip.exe")); err == nil {
		if runtime.GOOS == "windows" { //check for correct path on windows
			t.Error("pip.exe should not exist in the environment when WithoutPip is true")
		}
	}

	// Test passing a nil base environment
	_, err = CreateVenvEnvironment(nil, venvPath, options, mockProgressCallback)
	if err == nil {
		t.Error("Expected error for nil base environment, but got none")
	}

	// Test error paths:
	// Using an invalid python executable to create venv should fail.
	_, err = CreateVenvEnvironment(&Environment{PythonPath: "/invalid/python"}, venvPath, options, nil)
	if err == nil {
		t.Error("Expected error when base environment has an invalid python path.")
	}
}

// mockHTTPServer creates a mock HTTP server that returns a specific status code.
func mockHTTPServer(statusCode int, t *testing.T) *httptest.Server {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(statusCode)
	})
	return httptest.NewServer(handler)
}

func TestExpectMicromamba_DownloadError(t *testing.T) {
	// Create a mock HTTP server that returns an error (e.g., 404 Not Found).
	server := mockHTTPServer(http.StatusNotFound, t)
	defer server.Close()

	// Override the download URL to point to our mock server.
	originalBaseURL := micromambaBaseURL // Store the original URL
	micromambaBaseURL = server.URL       // Set to the mock server

	// Restore the original URL *after* the test completes, even if it fails.
	defer func() { micromambaBaseURL = originalBaseURL }()

	// Create a temporary directory for the download.
	testDir := createTestDir(t)
	defer cleanupTestDir(t, testDir)

	_, err := ExpectMicromamba(testDir, func(message string, current, total int64) {})

	if err == nil {
		t.Error("Expected an error when downloading micromamba, but got nil")
	}

	// Add an assertion to check the error message
	if !strings.Contains(err.Error(), "unexpected status code: 404") {
		t.Errorf("Expected error message to contain 'unexpected status code: 404', but got: %v", err)
	}
}

func TestExpectMicromamba_FileCreationError(t *testing.T) {
	// Use a read-only directory to simulate a file creation error.
	readOnlyDir := "/tmp/readonly" // Use /tmp and create a subdirectory
	os.MkdirAll(readOnlyDir, 0555) // Create with read-only permissions (for non-Windows)
	defer os.RemoveAll(readOnlyDir)

	if runtime.GOOS != "windows" { //Windows doesn't have the same permissions, must be admin
		_, err := ExpectMicromamba(readOnlyDir, nil)
		if err == nil {
			t.Error("Expected error for file creation in read-only directory, but got none")
		}
	}
}

func TestRunReadStdout_CommandNotFound(t *testing.T) {
	_, err := RunReadStdout("nonexistent-command")
	if err == nil {
		t.Error("Expected error for non-existent command, but got none")
	}
}

func TestRunReadStdout_Success(t *testing.T) {
	//Use go version to test
	goversion, err := RunReadStdout("go", "version")
	if err != nil {
		t.Error("Expected no error, but got ", err)
	}
	if goversion == "" {
		t.Error("Expected output, but got empty string")
	}

	cmd := exec.Command("go", "version")
	expectedOutput, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if goversion != string(expectedOutput) {
		t.Errorf("Expected %s, got %s", string(expectedOutput), goversion)
	}
}

func InstallPipPackage(env *Environment, packageName string) error {
	return env.PipInstallPackage(packageName, "https://pypi.org/simple", "", true, mockProgressCallback) // Use mockProgressCallback
}

func InstallMicromambaPackage(env *Environment, packageName string) error {
	return env.MicromambaInstallPackage(packageName, "conda-forge") //conda-forge for consistency
}

func TestFreezeToFile_MicromambaAndPip(t *testing.T) {
	testDir := createTestDir(t)
	defer cleanupTestDir(t, testDir)

	envName := "testenv_freeze"
	pythonVersion := "3.9"

	env, err := CreateEnvironmentMamba(envName, testDir, pythonVersion, "conda-forge", mockProgressCallback)
	if err != nil {
		t.Fatalf("CreateEnvironmentMamba failed: %v", err)
	}

	// Install a package with pip *inside* the environment.
	if err := InstallPipPackage(env, "requests"); err != nil {
		t.Fatalf("Pip install failed: %v", err)
	}

	// Install a conda package.
	if err := InstallMicromambaPackage(env, "numpy"); err != nil {
		t.Fatalf("Micromamba install failed: %v", err)
	}

	tempFile := filepath.Join(testDir, "environment.json") // Use .json extension
	if err := env.FreezeToFile(tempFile); err != nil {
		t.Fatalf("FreezeToFile failed: %v", err)
	}

	if _, err := os.Stat(tempFile); os.IsNotExist(err) {
		t.Errorf("Environment file not created: %v", err)
	}

	// Read and unmarshal the JSON
	var spec EnvironmentSpec
	jsonData, err := os.ReadFile(tempFile)
	if err != nil {
		t.Fatalf("Error reading environment file: %v", err)
	}
	if err := json.Unmarshal(jsonData, &spec); err != nil {
		t.Fatalf("Error unmarshaling JSON: %v", err)
	}

	// Basic checks on the JSON content
	if spec.Name != envName {
		t.Errorf("Expected environment name %s, got %s", envName, spec.Name)
	}
	if spec.PythonVersion != pythonVersion {
		t.Errorf("Expected Python version %s, got %s", pythonVersion, spec.PythonVersion)
	}

	// Check for numpy (conda)
	foundNumpy := false
	for _, pkg := range spec.CondaPackages {
		if strings.HasPrefix(pkg, "numpy=") {
			foundNumpy = true
			break
		}
	}
	for _, pkg := range spec.PipPackages {
		if strings.HasPrefix(pkg, "numpy") {
			foundNumpy = true
			break
		}
	}

	if !foundNumpy {
		t.Error("numpy not found in conda packages")
	}

	// Check for requests (pip)
	foundRequests := false
	for _, pkg := range spec.PipPackages {
		if strings.HasPrefix(pkg, "requests==") {
			foundRequests = true
			break
		}
	}
	if !foundRequests {
		t.Error("requests not found in pip packages")
	}
}

func TestFreezeToFile_VenvOnly(t *testing.T) {
	testDir := createTestDir(t)
	defer cleanupTestDir(t, testDir)

	baseEnv, err := CreateEnvironmentMamba("baseenv", testDir, "3.9", "conda-forge", mockProgressCallback)
	if err != nil {
		t.Fatalf("Failed to create base environment: %v", err)
	}

	venvPath := filepath.Join(testDir, "testvenv")
	options := VenvOptions{}

	venvEnv, err := CreateVenvEnvironment(baseEnv, venvPath, options, mockProgressCallback)
	if err != nil {
		t.Fatalf("CreateVenvEnvironment failed: %v", err)
	}

	if err := InstallPipPackage(venvEnv, "requests"); err != nil {
		t.Fatalf("Pip install failed: %v", err)
	}

	tempFileVenv := filepath.Join(testDir, "environment_venv.json") // Use .json
	if err := venvEnv.FreezeToFile(tempFileVenv); err != nil {
		t.Fatalf("FreezeToFile should have succeeded: %v", err)
	}

	if _, err := os.Stat(tempFileVenv); os.IsNotExist(err) {
		t.Errorf("Environment file not created: %v", err)
	}

	// Read and unmarshal the JSON
	var spec EnvironmentSpec
	jsonData, err := os.ReadFile(tempFileVenv)
	if err != nil {
		t.Fatalf("Error reading environment file: %v", err)
	}
	if err := json.Unmarshal(jsonData, &spec); err != nil {
		t.Fatalf("Error unmarshaling JSON: %v", err)
	}

	// Check that there are no conda packages and requests is present in pip packages.
	if len(spec.CondaPackages) > 0 {
		t.Error("Conda packages should be empty for venv-only environment")
	}
	foundRequests := false
	for _, pkg := range spec.PipPackages {
		if strings.HasPrefix(pkg, "requests==") {
			foundRequests = true
			break
		}
	}
	if !foundRequests {
		t.Error("requests not found in pip packages")
	}
}

func TestFreezeToFile_NoMicromamba(t *testing.T) {
	testDir := createTestDir(t)
	defer cleanupTestDir(t, testDir)
	pythonVersion := "3.9"
	env, err := CreateEnvironmentMamba("test", testDir, pythonVersion, "conda-forge", mockProgressCallback)
	if err != nil {
		t.Fatalf("CreateEnvironmentMamba failed: %v", err)
	}

	// Install a package with pip *inside* the environment.
	if err := InstallPipPackage(env, "requests"); err != nil {
		t.Fatalf("Pip install failed: %v", err)
	}
	tempFile2 := filepath.Join(testDir, "environment2.json")

	env.MicromambaPath = "" // Simulate no micromamba
	err = env.FreezeToFile(tempFile2)
	if err != nil {
		t.Errorf("FreezeToFile should not fail, but got error: %v", err)
	}

	if _, err := os.Stat(tempFile2); os.IsNotExist(err) {
		t.Errorf("Requirements file not created: %v", err)
	}

	// Read and unmarshal
	var spec EnvironmentSpec
	jsonData, err := os.ReadFile(tempFile2)
	if err != nil {
		t.Fatalf("Error reading file: %v", err)
	}
	if err := json.Unmarshal(jsonData, &spec); err != nil {
		t.Fatalf("Error unmarshaling JSON: %v", err)
	}

	// Should have no conda packages, but should have pip packages
	if len(spec.CondaPackages) != 0 {
		t.Error("Conda packages should be empty")
	}
	// Check for requests (pip)
	foundRequests := false
	for _, pkg := range spec.PipPackages {
		if strings.HasPrefix(pkg, "requests==") {
			foundRequests = true
			break
		}
	}
	if !foundRequests {
		t.Error("requests not found in pip packages")
	}
}

func TestFreezeToFile_NeitherAvailable(t *testing.T) {
	testDir := createTestDir(t)
	defer cleanupTestDir(t, testDir)
	pythonVersion := "3.9"
	env, err := CreateEnvironmentMamba("test", testDir, pythonVersion, "conda-forge", mockProgressCallback)
	if err != nil {
		t.Fatalf("CreateEnvironmentMamba failed: %v", err)
	}

	tempFile2 := filepath.Join(testDir, "requirements2.json")

	env.MicromambaPath = "" // Simulate no micromamba
	env.PipPath = ""
	err = env.FreezeToFile(tempFile2)
	if err == nil {
		t.Errorf("expected error, but got none")
	}
}

func TestCreateEnvironmentFromJSONFile(t *testing.T) {
	testDir := createTestDir(t)
	defer cleanupTestDir(t, testDir)

	// 1. Create a source environment.
	sourceEnvName := "source_env"
	sourceEnv, err := CreateEnvironmentMamba(sourceEnvName, testDir, "3.9", "conda-forge", mockProgressCallback)
	if err != nil {
		t.Fatalf("Failed to create source environment: %v", err)
	}

	// Install packages (using conda for both, for simplicity and to avoid pip build issues).
	if err := sourceEnv.MicromambaInstallPackage("requests", "conda-forge"); err != nil {
		t.Fatalf("Micromamba install of requests failed: %v", err)
	}
	if err := sourceEnv.MicromambaInstallPackage("numpy=1.22", "conda-forge"); err != nil {
		t.Fatalf("Micromamba install of numpy failed: %v", err)
	}

	// Install a pip package to ensure FreezeToFile now records these correctly
	if err := InstallPipPackage(sourceEnv, "pendulum"); err != nil {
		t.Fatalf("Pip install failed: %v", err)
	}

	// 2. Freeze the source environment to a JSON file.
	jsonFile := filepath.Join(testDir, "environment.json")
	if err := sourceEnv.FreezeToFile(jsonFile); err != nil {
		t.Fatalf("FreezeToFile failed: %v", err)
	}

	// Read and print the frozen environment file
	frozenContent, err := os.ReadFile(jsonFile)
	if err != nil {
		t.Fatalf("Failed to read json file %v", err)
	}
	t.Logf("Frozen Environment Content:\n%s", string(frozenContent)) // Log the content for debugging

	// 3. Create a new environment from the JSON file.
	newEnv, err := CreateEnvironmentFromJSONFile(jsonFile, testDir, mockProgressCallback)
	if err != nil {
		t.Fatalf("CreateEnvironmentFromJSONFile failed: %v", err)
	}

	// 4. Verify the new environment.
	isValidEnvironment(t, newEnv)

	// Check if requests and numpy are installed and have the correct versions.
	requestsVersion, err := RunReadStdout(newEnv.PythonPath, "-c", "import requests; print(requests.__version__)")
	if err != nil {
		t.Errorf("requests not found in new environment: %v", err)
	}
	expectedRequestsVersion, err := RunReadStdout(sourceEnv.PythonPath, "-c", "import requests; print(requests.__version__)")
	if err != nil {
		t.Errorf("requests not found in source environment: %v", err)
	}
	if strings.TrimSpace(requestsVersion) != strings.TrimSpace(expectedRequestsVersion) {
		t.Errorf("Expected requests version %s, got %s", expectedRequestsVersion, requestsVersion)
	}

	numpyVersion, err := RunReadStdout(newEnv.PythonPath, "-c", "import numpy; print(numpy.__version__)")
	if err != nil {
		t.Errorf("numpy not found in new environment: %v", err)
	}
	expectedNumpyVersion, err := RunReadStdout(sourceEnv.PythonPath, "-c", "import numpy; print(numpy.__version__)")
	if err != nil {
		t.Errorf("numpy not found in source environment: %v", err)
	}
	if strings.TrimSpace(numpyVersion) != strings.TrimSpace(expectedNumpyVersion) {
		t.Errorf("Expected numpy version %s, got %s", expectedNumpyVersion, numpyVersion)
	}

	// Check pip package
	pendulumVersion, err := RunReadStdout(newEnv.PythonPath, "-c", "import pendulum; print(pendulum.__version__)")
	if err != nil {
		t.Errorf("pendulum not found in new environment: %v", err)
	}
	expectedPendulumVersion, err := RunReadStdout(sourceEnv.PythonPath, "-c", "import pendulum; print(pendulum.__version__)")
	if err != nil {
		t.Errorf("pendulum not found in source environment: %v", err)
	}
	if strings.TrimSpace(pendulumVersion) != strings.TrimSpace(expectedPendulumVersion) {
		t.Errorf("Expected pendulum version %s, got %s", expectedPendulumVersion, pendulumVersion)
	}

	// Test creating environment from a non-existent JSON file.
	_, err = CreateEnvironmentFromJSONFile("/nonexistent/file", testDir, nil)
	if err == nil {
		t.Error("Expected an error for non-existent JSON file, but got none")
	}

	// Test creating from bad json file
	badJsonFile := filepath.Join(testDir, "bad.json")
	if err := os.WriteFile(badJsonFile, []byte("invalid json"), 0644); err != nil {
		t.Fatalf("Failed to create bad json file: %v", err)
	}
	_, err = CreateEnvironmentFromJSONFile(badJsonFile, testDir, nil)
	if err == nil {
		t.Error("Expected an error for bad JSON file, but got none")
	}

	// Test with non-writable directory
	if runtime.GOOS != "windows" {
		nonWritableDir := "/root/notwritable"
		_, err = CreateEnvironmentFromJSONFile(jsonFile, nonWritableDir, mockProgressCallback) //Use valid json file
		if err == nil {
			t.Error("expected error when root is not writable, but got none")
		}
	}
}
