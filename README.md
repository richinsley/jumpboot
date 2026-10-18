# Jumpboot

[![Go Reference](https://pkg.go.dev/badge/github.com/richinsley/jumpboot.svg)](https://pkg.go.dev/github.com/richinsley/jumpboot)
[![Go Report Card](https://goreportcard.com/badge/github.com/richinsley/jumpboot)](https://goreportcard.com/report/github.com/richinsley/jumpboot)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

Embed Python and its entire ecosystem in your Go binary. Ship a single executable that creates its own Python environment on first run.

```go
//go:embed sentiment.py
var sentimentCode string

func main() {
    // Creates isolated Python 3.11 environment with pip (first run only)
    env, _ := jumpboot.CreateEnvironmentMamba("mlenv", "./envs", "3.11", "conda-forge", nil)

    if env.IsNew {
        env.PipInstallPackages([]string{"transformers", "torch"}, "", "", false, nil)
    }

    // Embed Python code in your binary, call it like a function
    mod := jumpboot.NewModuleFromString("sentiment", "sentiment.py", sentimentCode)
    repl, _ := env.NewREPLPythonProcess(nil, nil, []jumpboot.Module{*mod}, nil)
    defer repl.Close()

    repl.Execute("import sentiment", true)
    result, _ := repl.Execute(`sentiment.analyze("I love this product!")`, true)
    fmt.Println(result)  // {"label": "POSITIVE", "score": 0.9998}
}
```

```python
# sentiment.py - embedded in Go binary
from transformers import pipeline
classifier = pipeline("sentiment-analysis")

def analyze(text):
    return classifier(text)[0]
```

## What This Gives You

**Single binary deployment.** Your Go app ships as one executable. On first run, it bootstraps a complete Python environment with any packages you need. Users don't install Python separately.

**Full Python ecosystem.** NumPy, PyTorch, TensorFlow, Hugging Face, OpenCV, pandas, scikit-learn—if it's on PyPI or conda-forge, you can use it.

**Multiple Python versions, simultaneously.** Run Python 3.9 for one task and Python 3.12 for another in the same application. Each environment is completely independent with its own packages.

```go
// Legacy code needs Python 3.8 with older numpy
legacyEnv, _ := jumpboot.CreateEnvironmentMamba("legacy", "./envs", "3.8", "conda-forge", nil)
legacyEnv.PipInstallPackage("numpy==1.21.0", "", "", false, nil)

// New ML pipeline needs Python 3.11 with latest packages
mlEnv, _ := jumpboot.CreateEnvironmentMamba("ml", "./envs", "3.11", "conda-forge", nil)
mlEnv.PipInstallPackages([]string{"numpy==2.0", "torch", "transformers"}, "", "", false, nil)

// Run both simultaneously
legacyRepl, _ := legacyEnv.NewREPLPythonProcess(nil, nil, nil, nil)
mlRepl, _ := mlEnv.NewREPLPythonProcess(nil, nil, nil, nil)
```

**Isolated environments.** No conflicts with system Python or other apps. Pin exact package versions per environment for reproducible builds.

**Three ways to run Python:**
- **REPL** - Interactive sessions, execute code strings, get results
- **QueueProcess** - Bidirectional RPC, call Python functions with structured data
- **Direct execution** - Run scripts, capture output

## Installation

```bash
go get github.com/richinsley/jumpboot
```

## Quick Examples

### Call Python Functions from Go

```go
env, _ := jumpboot.CreateEnvironmentMamba("myenv", "./envs", "3.11", "conda-forge", nil)
repl, _ := env.NewREPLPythonProcess(nil, nil, nil, nil)

// Execute any Python and get results
result, _ := repl.Execute("sum([1, 2, 3, 4, 5])", true)
fmt.Println(result)  // 15

// State persists between calls
repl.Execute("import numpy as np", true)
repl.Execute("data = np.random.randn(1000)", true)
result, _ = repl.Execute("np.mean(data)", true)
```

### Embed Entire Packages

```go
//go:embed mypackage/*
var myPackageFS embed.FS

func main() {
    // NewPackageFromFS(name, sourcePath, rootPath, fs)
    //   name:       Python import name (import mypackage)
    //   sourcePath: Path prefix for virtual __file__ paths
    //   rootPath:   Directory within embed.FS containing the package
    //   fs:         The embedded filesystem
    pkg, _ := jumpboot.NewPackageFromFS("mypackage", "mypackage", "mypackage", myPackageFS)

    program := &jumpboot.PythonProgram{
        Name:     "MyApp",
        Program:  mainModule,
        Packages: []jumpboot.Package{*pkg},
    }

    process, _, _ := env.NewPythonProcessFromProgram(program, nil, nil, false)
}
```

### Bidirectional RPC

```go
// Go calls Python
result, _ := queue.Call("process_image", 30, map[string]interface{}{
    "path": "/tmp/photo.jpg",
    "resize": []int{800, 600},
})

// Python calls Go
queue.RegisterHandler("log", func(data interface{}, id string) (interface{}, error) {
    fmt.Printf("[Python] %v\n", data)
    return "ok", nil
})
```

## How It Works

1. **First run**: Downloads [micromamba](https://mamba.readthedocs.io/en/latest/) (~5MB), creates a conda environment with your specified Python version, installs packages via pip/conda (or, with the goomba backend, creates the conda environment without downloading or running micromamba; see [Choosing a backend](#choosing-a-backend))
2. **Subsequent runs**: Environment exists, startup is fast
3. **Communication**: Go spawns Python subprocess, communicates via pipes (MessagePack serialization)
4. **Embedded code**: Python source is base64-encoded in the Go binary, loaded via custom import hooks

No CGO required for basic operation. Optional shared memory features use CGO on Unix.

**Building without CGO:** When you build with `CGO_ENABLED=0`, shared memory functions (`CreateSharedMemory`, `OpenSharedMemory`) will return `ErrSharedMemoryNotAvailable`. All other features work normally.

## Choosing a backend

Conda environments come from one of two backends. You choose one by the function you call; `env.Backend` tells which one an environment came from. Both create `rootDir/envs/NAME` and fill every path of `PythonEnvironment` the same way, so an environment created by one backend is reused by the other.

```go
// micromamba: downloads the micromamba binary on first use and runs it
env, err := jumpboot.CreateEnvironmentMamba("myenv", "./envs", "3.11", "conda-forge", nil)

// goomba: a pure-Go conda resolver and installer, compiled into your program
env, err := jumpboot.CreateEnvironmentGoomba("myenv", "./envs", "3.11", "conda-forge", nil)

// goomba with a context and options
env, err := jumpboot.CreateEnvironmentGoombaWithOptions(ctx, "myenv", "./envs", jumpboot.GoombaOptions{
    PythonVersion: "3.11",
    Specs:         []string{"numpy>=1.26"},
    Client:        &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}},
}, nil)
```

| | micromamba | goomba |
|---|---|---|
| Functions | `CreateEnvironmentMamba`, `CreateEnvironmentFromJSONFile[WithOptions]` | `CreateEnvironmentGoomba`, `CreateEnvironmentGoombaWithOptions`, `CreateEnvironmentFromJSONFileGoomba` |
| Platforms | Linux, macOS, Windows | Linux and macOS. On Windows every goomba function fails with `ErrBackendUnsupported` before doing anything (goomba cannot install entry-point launchers such as `pip.exe` yet) |
| Needs | A download of micromamba from GitHub into `rootDir/bin` on first use | No micromamba. goomba downloads the channel indexes and packages over HTTPS, may run `nvidia-smi` and `getconf` from fixed system locations to detect virtual packages, and runs the new environment's Python to byte-compile packages; jumpboot then runs python and pip to check the environment, as for micromamba |
| Configuration | `.condarc`, `MAMBA_*` and `CONDA_OVERRIDE_*` variables, proxies from the environment | No configuration files or environment variables: everything comes from `GoombaOptions` (`VirtualOverrides` replaces `CONDA_OVERRIDE_*`) |
| Proxies | `HTTPS_PROXY` and friends | Only through `GoombaOptions.Client` |
| Defaults | Python 3.10 | Python 3.10, conda-forge, 10 minute timeout plus your context |
| Reuse | An existing `rootDir/envs/NAME` is used as it is, whoever owns it | A finished environment (made by either backend) is used as it is, but only if it belongs to the current user: one owned by root or another account (an image that builds environments as root and runs as a user, say) or writable by others is refused with `goomba.ErrUnsafePrefix`. The options (specs, channels, ...) only matter when one is created. An environment another process is installing or removing (or restoring, until its pip step ends) is refused with `goomba.ErrPrefixLocked`, an interrupted one with `ErrIncompleteEnvironment`; an empty directory is filled, and any other non-empty directory refused (`goomba.ErrPrefixNotEmpty`) |
| Channels | Names or URLs | Names or https URLs, strict priority (highest first). Refused: the `defaults` multichannel (list its channels), `file://` and `http://` channels, URLs whose host is an IPv6 literal or whose path is exactly `/t`, and a mirror whose DNS name resolves to a private address (name it by its IPv4 address) |
| Root directory | Any writable directory | Must not be writable by other users, nor any directory above it (group-writable is fine for your own private group): goomba refuses it with `goomba.ErrUnsafePrefix` or `fetch.ErrUnsafeCache`/`repodata.ErrUnsafeCache` |
| Caches | `rootDir/pkgs` | `rootDir/goomba/pkgs` and `rootDir/goomba/repodata`, created 0700 |
| pip | Added to python by micromamba | Added by jumpboot as a `pip` spec unless a spec names pip |
| Install into an existing environment | `MicromambaInstallPackage` | Not supported (`ErrBackendUnsupported`): list the packages in `GoombaOptions.Specs`, or restore from JSON |
| JSON restore | Creates the base environment, then installs each conda package separately, trying each channel in turn; checks only that checksums are present (`Strict` therefore refuses a file that lists pip packages). `WithOptions` ignores `PipPackages` when `Packages` is set, dropping pip lines that are not `name==version` | Solves python, pip and every conda package together, pinned exactly, on the channels in strict priority in the order listed; verifies the checksums against the channel before installing anything, and with `Strict` refuses any solved conda package that has no checksum in the file (pip packages are exempt and never verified). Then checks the new environment and installs the pip packages, including the `PipPackages` lines `Packages` cannot pin, inside goomba's transaction: nobody can reuse the environment before pip has finished, and a pip failure or cancellation rolls everything back. `Timeout` covers resolving, installing and pip together: raise it for large pip installs |
| Freeze | `micromamba list` | The environment's `conda-meta` records; the file holds the legacy lists and `Packages` (with sha256). The goomba restore reads all of it; the micromamba restores read it as they read their own (see `FreezeToFile`) |
| Failure | Whatever micromamba leaves behind | Rolls back, also when the check of the new environment (python and pip versions) or a restore's pip step fails or is cancelled. After a crash or kill, the unfinished environment is refused with `ErrIncompleteEnvironment`; jumpboot never removes it by itself: call `RemoveIncompleteEnvironment`, then create it again |

Neither freeze records channel priority: `Channels` lists channels in the order the packages name them. Before restoring a file with several channels with goomba, put them highest priority first; when such a restore is unsolvable, the error says so.

goomba's errors are wrapped, so you can branch on them: `errors.As(err, &unsolvable)` with a `*solve.Unsolvable` for a request that has no solution (its `Explanation` is safe to print), and `errors.Is(err, goomba.ErrPrefixLocked)` for an environment another process is creating or removing (nothing waits; try again later).

Recovering from an interrupted installation:

```go
env, err := jumpboot.CreateEnvironmentGoomba("myenv", "./envs", "3.11", "conda-forge", nil)
if errors.Is(err, jumpboot.ErrIncompleteEnvironment) {
    // A previous run was killed while installing: remove what it left, then retry.
    if err = jumpboot.RemoveIncompleteEnvironment("myenv", "./envs"); err == nil {
        env, err = jumpboot.CreateEnvironmentGoomba("myenv", "./envs", "3.11", "conda-forge", nil)
    }
}
```

`RemoveIncompleteEnvironment` removes only an interrupted environment, under goomba's lock on it: a finished one is left alone (`ErrEnvironmentNotIncomplete`), and one another process is working on gives `goomba.ErrPrefixLocked`. See [examples/goomba](examples/goomba/).

## Examples

| Example | What it demonstrates |
|---------|---------------------|
| [repl](examples/repl/) | Interactive Python from Go |
| [goomba](examples/goomba/) | The REPL example on an environment created with goomba |
| [jsonqueueserver](examples/jsonqueueserver/) | Bidirectional RPC with MessagePack |
| [chromadb](examples/chromadb/) | Vector database for embeddings |
| [gradio](examples/gradio/) | Python web UI from Go |
| [mlx](examples/mlx/) | ML inference on Apple Silicon |
| [embedded_packages](examples/embedded_packages/) | Complex package structures |

## Documentation

| Document | Description |
|----------|-------------|
| [Quick Start](docs/QUICKSTART.md) | Get running in 5 minutes |
| [Environments](docs/ENVIRONMENTS.md) | Creating and managing Python environments |
| [Programs](docs/PROGRAMS.md) | Embedding modules and packages |
| [REPL](docs/REPL.md) | Interactive Python sessions |
| [Queue Process](docs/QUEUE.md) | Bidirectional RPC |
| [Architecture](docs/ARCHITECTURE.md) | Internal design |
| [AI Agents](docs/AGENTS.md) | Guide for AI coding assistants |

## What It Doesn't Do

- **Replace Python with Go** - This is for using Python libraries from Go, not avoiding Python
- **Require CGO** - Basic features work without CGO (`CGO_ENABLED=0`); shared memory is optional and requires CGO on Unix (disabled at compile time without it)
- **Provide Python C API bindings** - Communication is via subprocess pipes, not embedded interpreter

## Platform Support

| Platform | Status |
|----------|--------|
| macOS (amd64, arm64) | Supported |
| Linux (amd64, arm64) | Supported |
| Windows (amd64) | Supported |

## Acknowledgments

Jumpboot uses [micromamba](https://mamba.readthedocs.io/en/latest/) for fast, reliable conda environment management. Micromamba is a lightweight, standalone C++ implementation of the conda package manager developed by the [mamba](https://github.com/mamba-org/mamba) team. The alternative backend, [goomba](https://github.com/richinsley/goomba), is a pure-Go conda resolver and installer.

## License

MIT
