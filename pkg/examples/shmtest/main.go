package main

import (
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	jumpboot "github.com/richinsley/jumpboot/pkg"
)

//go:embed main.py
var main_program string

func CreateSharedNumPyArray[T any](name string, shape []int) (*jumpboot.SharedMemory, int, error) {
	// Calculate total size
	size := 1
	for _, dim := range shape {
		size *= dim
	}

	// Add extra space for metadata (shape, dtype, and endianness flag)
	metadataSize := 4 + len(shape)*4 + 16 + 1 // 4 bytes for rank, 4 bytes per dimension, 16 bytes for dtype, 1 byte for endianness
	totalSize := metadataSize + size*int(unsafe.Sizeof(new(T)))

	// Create shared memory
	shm, err := jumpboot.CreateSharedMemory(name, totalSize)
	if err != nil {
		return nil, 0, err
	}

	// Get the byte slice for metadata
	metadataSlice := unsafe.Slice((*byte)(shm.GetPtr()), metadataSize)

	// Write metadata
	binary.LittleEndian.PutUint32(metadataSlice[:4], uint32(len(shape)))
	for i, dim := range shape {
		binary.LittleEndian.PutUint32(metadataSlice[4+i*4:8+i*4], uint32(dim))
	}
	dtype := jumpboot.GetDType[T]()
	copy(metadataSlice[4+len(shape)*4:20+len(shape)*4], []byte(dtype))

	// Write endianness flag
	metadataSlice[20+len(shape)*4] = 'L' // 'L' for little-endian

	return shm, totalSize, nil
}

func main() {
	// Specify the binary folder to place micromamba in
	cwd, _ := os.Getwd()
	rootDirectory := filepath.Join(cwd, "..", "environments")
	fmt.Println("Creating Jumpboot repo at: ", rootDirectory)

	progressFunc := func(message string, current, total int64) {
		if total > 0 {
			fmt.Printf("\r%s: %.2f%%\n", message, float64(current)/float64(total)*100)
		} else {
			fmt.Printf("\r%s: %d\n", message, current)
		}
	}

	// baseEnv, err := jumpboot.CreateEnvironmentFromSystem(progressFunc)
	version := "3.11"
	baseEnv, err := jumpboot.CreateEnvironmentMamba("myenv"+version, rootDirectory, version, "conda-forge", progressFunc)
	if err != nil {
		log.Fatalf("Error creating environment: %v", err)
	}
	fmt.Printf("Created environment: %s\n", baseEnv.Name)

	if baseEnv.IsNew {
		fmt.Println("Created a new environment")
	}

	// create a virtual environment from the system python
	venvOptions := jumpboot.VenvOptions{
		SystemSitePackages: true,
		Upgrade:            true,
		Prompt:             "my-venv",
		UpgradeDeps:        true,
		// Clear:              true,
	}
	env, err := jumpboot.CreateVenvEnvironment(baseEnv, filepath.Join(rootDirectory, "sysvenv"), venvOptions, progressFunc)
	if err != nil {
		log.Fatalf("Error creating venv environment: %v", err)
	}

	if env.IsNew {
		fmt.Println("Created a new venv environment")
		env.PipInstallPackages([]string{"numba", "numpy"}, "", "", false, nil)
	}

	// create a shared semaphore
	semaphore_name := "/MySemaphore"
	if runtime.GOOS == "windows" {
		semaphore_name = "MySemaphore"
	}
	sem, err := jumpboot.NewSemaphore(semaphore_name, 0)
	if err != nil {
		log.Fatalf("Failed to create semaphore: %v", err)
	}

	// Shared Numpy array
	numpy_name := "my_array"
	shape := []int{100, 100, 100}
	shm, nsize, err := CreateSharedNumPyArray[float32]("my_array", shape)
	if err != nil {
		log.Fatal(err)
	}
	defer shm.Close()

	// C:\Users\johnn\jumpboot\jumpboot\pkg\examples\environments\envs\myenv3.11\python.exe -m venv --system-site-packages --clear --upgrade --prompt my-venv --upgrade-deps C:\Users\johnn\jumpboot\jumpboot\pkg\examples\environments\sysvenv
	program := &jumpboot.PythonProgram{
		Name: "MyProgram",
		Path: cwd,
		Program: jumpboot.Module{
			Name:   "__main__",
			Path:   filepath.Join(cwd, "modules", "main.py"),
			Source: base64.StdEncoding.EncodeToString([]byte(main_program)),
		},
		Modules:  []jumpboot.Module{},
		Packages: []jumpboot.Package{},
		KVPairs:  map[string]interface{}{"SHARED_MEMORY_NAME": numpy_name, "SHARED_MEMORY_SIZE": nsize, "SEMAPHORE_NAME": semaphore_name},
	}

	// create a string map of env options to pass to the Python process
	envOptions := map[string]string{}

	pyProcess, _, err := env.NewPythonProcessFromProgram(program, envOptions, nil, false)
	if err != nil {
		panic(err)
	}

	// copy output from the Python script
	go func() {
		io.Copy(os.Stdout, pyProcess.Stdout)
	}()

	go func() {
		io.Copy(os.Stderr, pyProcess.Stderr)
	}()

	// wait for the semaphore to be released on the python side
	err = sem.Acquire()
	if err != nil {
		log.Fatalf("Failed to acquire semaphore: %v", err)
	}

	// do something with the shared memory

	// Release the semaphore so the Python process can exit
	sem.Release()

	// Wait for the Python process to finish
	pyProcess.Cmd.Wait()
}
