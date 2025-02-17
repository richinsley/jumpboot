package jumpboot

import (
	"bufio"
	"os"
	"os/exec"
)

func isDirWritable(path string) bool {
	// Attempt to create a temporary file in the specified directory.
	tmpFile, err := os.CreateTemp(path, "test-*")
	if err != nil {
		// If an error occurs, the directory is not writable.
		return false
	}
	fileName := tmpFile.Name()

	// Clean up: close and remove the temporary file.
	tmpFile.Close()
	os.Remove(fileName)

	// If the temporary file was created successfully, the directory is writable.
	return true
}

// RunReadStdout is a general function to run a binary and return the standard output.
// This can be used to run any binary, not just Python scripts.
// RunReadStdout blocks until the child process exits.
func RunReadStdout(binPath string, args ...string) (string, error) {
	retv := ""
	cmd := exec.Command(binPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	defer stdout.Close()

	// continue to read the output until there is no more
	// or an error occurs
	if err := cmd.Start(); err != nil {
		return "", err
	}
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		retv += scanner.Text() + "\n"
	}
	return retv, nil
}
