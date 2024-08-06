package pkg

import (
	"os"
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
