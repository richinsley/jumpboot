// Package jumpboot provides seamless Python environment management and Go-Python
// interoperability without requiring CGO for core operations.
//
// Jumpboot enables Go applications to create isolated Python environments, run Python
// code, and communicate bidirectionally with Python processes using pipes and optional
// shared memory. It supports Windows, macOS, and Linux platforms.
//
// # Architecture Overview
//
// Jumpboot uses a two-stage bootstrap mechanism to launch Python processes:
//
//  1. Primary Bootstrap: A minimal Python script that opens file descriptors and
//     executes the secondary bootstrap via exec().
//
//  2. Secondary Bootstrap: Initializes the custom module import system, loads
//     embedded packages/modules, and executes the main program.
//
// This design allows Go applications to embed Python code directly in the binary
// using go:embed and have it executed transparently by the Python subprocess.
//
// # Environment Management
//
// Jumpboot provides multiple ways to create Python environments:
//
//	// Create a new environment using micromamba (auto-downloads if needed)
//	env, err := jumpboot.CreateEnvironmentMamba("myenv", "/path/to/root", "3.10", "conda-forge", nil)
//
//	// The same environment with goomba, a pure-Go installer (no download)
//	env, err := jumpboot.CreateEnvironmentGoomba("myenv", "/path/to/root", "3.10", "conda-forge", nil)
//
//	// Use the system Python installation
//	env, err := jumpboot.CreateEnvironmentFromSystem()
//
//	// Create a virtual environment from an existing Python
//	env, err := jumpboot.CreateVenvEnvironment(baseEnv, "/path/to/venv", jumpboot.VenvOptions{}, nil)
//
//	// Restore an environment from a frozen JSON specification
//	env, err := jumpboot.CreateEnvironmentFromJSONFile("env.json", "/path/to/root", nil)
//
// # Choosing a Backend
//
// Conda environments come from one of two backends, chosen by the function
// called; PythonEnvironment.Backend says which one an environment came from.
// Both create rootDir/envs/NAME and fill every path field the same way, so an
// environment made by one backend is reused by the other.
//
// micromamba (CreateEnvironmentMamba, CreateEnvironmentFromJSONFile,
// CreateEnvironmentFromJSONFileWithOptions) downloads the micromamba binary
// from GitHub into rootDir/bin on first use and runs it, with the process
// environment: .condarc files, MAMBA_* and CONDA_OVERRIDE_* variables and
// proxy settings apply. Its package cache is rootDir/pkgs. It adds pip to
// python by default, and MicromambaInstallPackage installs into an
// existing environment.
//
// goomba (CreateEnvironmentGoomba, CreateEnvironmentGoombaWithOptions,
// CreateEnvironmentFromJSONFileGoomba) is a pure-Go conda resolver and
// installer compiled into the program, for Linux and macOS: on Windows the
// goomba functions fail with ErrBackendUnsupported before doing anything.
// No micromamba is downloaded or run: goomba downloads the channel indexes
// and packages over HTTPS, may run nvidia-smi and getconf from fixed system
// locations to detect virtual packages, and runs the new environment's
// Python to byte-compile noarch packages; jumpboot then runs python and pip
// to check the environment. goomba reads no configuration file and no
// environment variable; everything is set through GoombaOptions, which
// only matter when an environment is created:
//   - Defaults: Python 3.10, the conda-forge channel, a 10 minute
//     timeout (the context passed in applies too).
//   - Proxies: none, unless GoombaOptions.Client has one (for example a
//     transport with Proxy: http.ProxyFromEnvironment).
//   - Channels are names or https URLs with strict priority, highest
//     first. Refused: the "defaults" multichannel, file:// and http://
//     channels, URLs whose host is an IPv6 literal or whose path is exactly
//     "/t", and a channel on a public host name that resolves to a private
//     address (name a local mirror by its IPv4 address).
//   - The root directory and every directory above it must not be
//     writable by other users (goomba.ErrUnsafePrefix, fetch.ErrUnsafeCache,
//     repodata.ErrUnsafeCache), where micromamba accepts any writable root.
//   - Caches: rootDir/goomba/pkgs and rootDir/goomba/repodata, created
//     0700.
//   - pip: goomba does not add pip to python, so jumpboot adds a pip spec
//     unless one is given.
//   - No installs into an existing environment: MicromambaInstallPackage
//     fails with ErrBackendUnsupported; create the environment with the
//     packages in GoombaOptions.Specs, or restore it from JSON.
//   - JSON restore solves python, pip and all conda packages of the
//     specification together, pinned exactly, on the channels in strict
//     priority in the order listed (the micromamba restore installs them
//     one by one, trying each channel in turn), and verifies their
//     checksums against the channel before installing anything (the
//     micromamba restore only checks that they are present); with Strict,
//     every solved package must have a checksum in the specification (pip
//     packages are exempt and never verified). The check of the new
//     environment and the pip step run inside goomba's transaction, before
//     it commits: nobody can reuse the environment until pip has finished,
//     and a failure or cancellation rolls everything back. The Timeout
//     covers resolving, installing and pip together: raise it for large pip
//     installs. No freeze records channel priority, so list several
//     channels highest priority first.
//   - FreezeToFile reads the conda packages from conda-meta and writes both
//     the legacy lists and Packages with their sha256. The goomba restore
//     reads all of it; the micromamba restores do not (see FreezeToFile).
//   - Reuse: goomba.CheckComplete decides. A finished environment (made by
//     either backend) is reused as it is, but only if it belongs to the
//     current user (goomba.ErrUnsafePrefix otherwise, where micromamba
//     reuses any); one another process is installing or removing, or whose
//     restore is still running pip, is refused with goomba.ErrPrefixLocked,
//     without waiting; an empty directory is filled, any other directory
//     that is not an environment refused (goomba.ErrPrefixNotEmpty).
//   - A failed or cancelled creation is rolled back, also when the check
//     of the new environment (python and pip versions), which runs inside
//     goomba's transaction, fails. One whose process
//     crashed or was killed is refused with ErrIncompleteEnvironment;
//     jumpboot never removes it by itself. Call
//     RemoveIncompleteEnvironment, then create the environment again.
//
// goomba's errors are wrapped: errors.As finds *solve.Unsolvable for a
// request without a solution (its Explanation is safe to print), and
// errors.Is finds goomba.ErrPrefixLocked for a concurrent creation of the
// same environment.
//
// # Process Types
//
// Jumpboot offers three process types for different interaction patterns:
//
// REPLPythonProcess provides stateful, interactive Python execution where state
// persists between calls:
//
//	repl, err := env.NewREPLPythonProcess(nil, nil, nil, nil)
//	result, err := repl.Execute("x = 42", true)
//	result, err := repl.Execute("print(x * 2)", true)  // prints 84
//	repl.Close()
//
// QueueProcess provides bidirectional RPC-style communication using MessagePack:
//
//	queue, err := env.NewQueueProcess(program, nil, nil, nil)
//	result, err := queue.Call("my_python_method", 30, map[string]interface{}{"arg": "value"})
//	queue.Close()
//
// PythonExecProcess provides simple command execution with JSON communication:
//
//	exec, err := env.NewPythonExecProcess(nil, nil)
//	output, err := exec.Exec("print('hello')")
//	exec.Close()
//
// # Embedded Modules and Packages
//
// Python code can be embedded in Go binaries using go:embed:
//
//	//go:embed scripts/mymodule.py
//	var myModuleSource string
//
//	module := jumpboot.NewModuleFromString("mymodule", "scripts/mymodule.py", myModuleSource)
//
//	//go:embed packages/mypackage/*
//	var myPackageFS embed.FS
//
//	pkg, err := jumpboot.NewPackageFromFS("mypackage", "mypackage", "packages/mypackage", myPackageFS)
//
// These modules become importable in Python via a custom import system that intercepts
// imports before the standard library.
//
// # Shared Memory (Optional, requires CGO)
//
// For high-performance data sharing, jumpboot provides cross-platform shared memory:
//
//	// Create shared memory from Go
//	shm, err := jumpboot.CreateSharedMemory("my_shm", 1024*1024)
//	defer shm.Close()
//
//	// Write data
//	shm.Write([]byte("hello from Go"))
//
//	// Get typed slices for zero-copy access
//	floats := shm.GetFloat32Slice(0)
//
// Python can access the same memory using the jumpboot.SharedMemory class.
//
// # Semaphores (Optional, requires CGO)
//
// Named semaphores enable cross-process synchronization:
//
//	sem, err := jumpboot.CreateSemaphore("my_sem", 1)
//	defer sem.Close()
//
//	sem.Acquire()
//	// critical section
//	sem.Release()
//
// # Package Installation
//
// Environments support package installation via pip and conda/micromamba:
//
//	// Install pip packages
//	err := env.PipInstallPackages([]string{"numpy", "pandas"}, "", "", true, nil)
//
//	// Install from requirements file
//	err := env.PipInstallRequirements("requirements.txt", nil)
//
//	// Install conda packages (micromamba environments only)
//	err := env.MicromambaInstallPackage("scipy", "conda-forge")
//
// # Environment Freezing
//
// Environments can be frozen to JSON for reproducibility:
//
//	err := env.FreezeToFile("environment.json")
//
// The JSON specification includes conda packages, pip packages, channels, and
// Python version, allowing exact environment recreation.
//
// # Key-Value Data Passing
//
// Data can be passed from Go to Python at process startup via KVPairs:
//
//	program := &jumpboot.PythonProgram{
//	    // ...
//	    KVPairs: map[string]interface{}{
//	        "config_path": "/path/to/config",
//	        "debug_mode":  true,
//	    },
//	}
//
// In Python, these are accessible as attributes on the jumpboot module:
//
//	import jumpboot
//	print(jumpboot.config_path)  # /path/to/config
//	print(jumpboot.debug_mode)   # True
//
// # Debugging Support
//
// Python processes can be started with debugpy for remote debugging:
//
//	program := &jumpboot.PythonProgram{
//	    // ...
//	    DebugPort:    5678,
//	    BreakOnStart: true,
//	}
//
// The process will wait for a debugger to attach before executing the main program.
//
// # Platform Support
//
// Jumpboot supports:
//   - Linux (amd64, arm64)
//   - macOS (amd64, arm64/Apple Silicon)
//   - Windows (amd64)
//
// Shared memory and semaphores use platform-specific implementations:
//   - Linux/macOS: POSIX shared memory (shm_open/mmap) and semaphores
//   - Windows: Named file mappings and kernel synchronization objects
package jumpboot
