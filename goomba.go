package jumpboot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/richinsley/goomba"
	"github.com/richinsley/goomba/link"
	"github.com/richinsley/goomba/lock"
	"github.com/richinsley/goomba/record"
	"github.com/richinsley/goomba/solve"
	"github.com/richinsley/goomba/spec"
	"github.com/richinsley/goomba/virtual"
)

// Errors of the goomba backend. Test for them with errors.Is.
//
// The goomba functions also wrap goomba's own errors unchanged, so callers
// can branch on them: errors.As with *solve.Unsolvable for a request that
// has no solution (its Explanation is safe to print), and errors.Is with
// goomba.ErrPrefixLocked (a concurrent creation of the same environment),
// goomba.ErrPrefixNotEmpty, goomba.ErrUnsafePrefix, goomba.ErrInvalidOptions,
// goomba.ErrVirtual, fetch.ErrUnsafeCache and repodata.ErrUnsafeCache (a
// cache directory other users could change), context.DeadlineExceeded (the
// GoombaOptions.Timeout or the caller's deadline) and context.Canceled.
var (
	// ErrIncompleteEnvironment reports an environment directory that a
	// goomba installation left unfinished: the process doing it crashed or
	// was killed, so it could not roll back (goomba.CheckComplete reports
	// goomba.ErrIncomplete, which the error also wraps). jumpboot never
	// removes such a directory on its own: call RemoveIncompleteEnvironment,
	// then create the environment again. An installation that is still
	// running is reported as goomba.ErrPrefixLocked instead.
	ErrIncompleteEnvironment = errors.New("environment is incomplete")

	// ErrChecksumMismatch reports a conda package of a JSON specification
	// whose SHA256 differs from that of the archive the channels offer
	// for the same name, version and build.
	ErrChecksumMismatch = errors.New("checksum mismatch")

	// ErrEnvironmentNotIncomplete reports that RemoveIncompleteEnvironment
	// removed nothing because the environment is a finished one.
	ErrEnvironmentNotIncomplete = errors.New("environment is not incomplete")

	// ErrBackendUnsupported reports an operation that the environment's
	// backend cannot perform: installing a package into an existing
	// goomba environment with MicromambaInstallPackage, or any goomba
	// function on Windows.
	ErrBackendUnsupported = errors.New("operation not supported by the environment's backend")
)

// defaultGoombaTimeout bounds resolving plus installing when
// GoombaOptions.Timeout is zero.
const defaultGoombaTimeout = 10 * time.Minute

// goombaHostOS is the operating system the goomba functions check for
// support; tests replace it.
var goombaHostOS = runtime.GOOS

// checkGoombaHost refuses the operating systems goomba cannot create
// environments on yet: on Windows it cannot install the entry-point
// launchers that pip (which jumpboot always installs) ships.
func checkGoombaHost() error {
	if goombaHostOS == "windows" {
		return fmt.Errorf("%w: goomba cannot create environments on Windows yet (it does not install entry-point launchers such as pip.exe); use CreateEnvironmentMamba", ErrBackendUnsupported)
	}
	return nil
}

// GoombaOptions configures environment creation with goomba, a pure-Go
// conda resolver and installer that jumpboot calls as a library: no
// micromamba binary is downloaded or run. The zero value selects the same
// defaults as CreateEnvironmentMamba. The goomba functions run on Linux and
// macOS; on Windows they fail with ErrBackendUnsupported.
//
// goomba reads no environment variables and no configuration files
// (.condarc, MAMBA_ROOT_PREFIX, HTTPS_PROXY, CONDA_OVERRIDE_*, ...):
// everything comes from these options. They only matter when an
// environment is created: an existing environment is reused as it is, and
// its Specs, Channels, ChannelAlias and the other options are ignored.
//
// goomba refuses, where micromamba would go ahead:
//   - a root directory, or a directory above it, that other users can write
//     (goomba.ErrUnsafePrefix for the environment, fetch.ErrUnsafeCache or
//     repodata.ErrUnsafeCache for the caches); a group-writable directory of
//     the user's own private group is accepted;
//   - the "defaults" channel (list its channels explicitly), file:// and
//     http:// channels, channel URLs with credentials, channel URLs whose
//     host is an IPv6 literal or whose path is exactly "/t", and a channel
//     on a public host name that resolves to a private or loopback address
//     (name a mirror on the local network by its IPv4 address instead).
//
// A GoombaOptions value is only read, so one value may be shared by
// concurrent calls.
type GoombaOptions struct {
	// PythonVersion is the Python to install, as "3.11" or "3.11.9" (digits
	// and dots only); "" means "3.10", as for CreateEnvironmentMamba. It
	// becomes the match spec python=<PythonVersion>.
	PythonVersion string

	// Channels are channel names ("conda-forge") or https URLs, highest
	// priority first, with strict priority: every package comes from the
	// first channel that has its name. nil means ["conda-forge"].
	Channels []string

	// Specs are extra match specs ("numpy>=1.26"), solved together with
	// python and pip.
	Specs []string

	// ChannelAlias resolves channel names; "" means
	// https://conda.anaconda.org.
	ChannelAlias string

	// Timeout bounds resolving plus installing; 0 means 10 minutes. It
	// applies on top of the caller's context.
	Timeout time.Duration

	// Client performs every download (channel indexes and package
	// archives). nil selects goomba's default client, which uses no proxy:
	// to use the proxy of the environment, pass a client whose transport is
	// an *http.Transport with Proxy set to http.ProxyFromEnvironment.
	Client *http.Client

	// VirtualOverrides overrides detected virtual packages, as conda's
	// CONDA_OVERRIDE_<NAME> variables do: keys are names such as "cuda",
	// "glibc", "osx" or "archspec"; an empty value removes the package.
	VirtualOverrides map[string]string

	// NvidiaSMI is the absolute path of the nvidia-smi program that detects
	// __cuda; "" means goomba's built-in system locations (never PATH).
	NvidiaSMI string

	// NoPyc skips byte-compiling the noarch: python packages, which
	// micromamba and goomba do by default.
	NoPyc bool
}

// withDefaults returns a copy of o with the defaults filled in and its
// slices copied.
func (o GoombaOptions) withDefaults() GoombaOptions {
	if o.PythonVersion == "" {
		o.PythonVersion = "3.10"
	}
	if len(o.Channels) == 0 {
		o.Channels = []string{"conda-forge"}
	} else {
		o.Channels = append([]string(nil), o.Channels...)
	}
	o.Specs = append([]string(nil), o.Specs...)
	if o.Timeout == 0 {
		o.Timeout = defaultGoombaTimeout
	}
	return o
}

// pythonVersionRE is the form GoombaOptions.PythonVersion must have: it is
// written into a match spec, so nothing but digits and dots may pass.
var pythonVersionRE = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,2}$`)

// CreateEnvironmentGoomba creates a Python environment with goomba instead of
// micromamba. It takes the same arguments as CreateEnvironmentMamba and
// creates the same layout: the environment is rootDir/envs/envName, every
// path field is filled as CreateEnvironmentMamba fills it, and an existing
// environment of that name is reused (IsNew false), whichever backend made
// it. channel "" means conda-forge.
//
// It is CreateEnvironmentGoombaWithOptions with a background context and
// the default options.
func CreateEnvironmentGoomba(envName, rootDir, pythonVersion, channel string, progressCallback ProgressCallback) (*PythonEnvironment, error) {
	return CreateEnvironmentGoombaWithOptions(context.Background(), envName, rootDir, goombaOptionsFor(pythonVersion, channel), progressCallback)
}

// goombaOptionsFor returns the options of CreateEnvironmentGoomba.
func goombaOptionsFor(pythonVersion, channel string) GoombaOptions {
	opts := GoombaOptions{PythonVersion: pythonVersion}
	if channel != "" {
		opts.Channels = []string{channel}
	}
	return opts
}

// CreateEnvironmentGoombaWithOptions creates the Python environment
// rootDir/envs/envName with goomba, or reuses it if it exists.
//
// A new environment is solved for python=<PythonVersion>, opts.Specs and
// pip (unless a spec already names pip: goomba, unlike micromamba, does not
// add pip to python, and jumpboot needs it), then installed. Channel
// indexes are cached in rootDir/goomba/repodata and package archives in
// rootDir/goomba/pkgs, both created 0700 and never shared with
// micromamba's rootDir/pkgs; goomba refuses cache directories that other
// users could change. On any error goomba removes what it installed.
//
// What happens to an existing rootDir/envs/envName depends on what
// goomba.CheckComplete finds there:
//   - a finished environment, whichever backend made it, is reused as it
//     is, as CreateEnvironmentMamba does (opts.Specs, Channels and the like
//     are then ignored);
//   - one that another process is installing or removing is an error
//     wrapping goomba.ErrPrefixLocked (nothing is waited for);
//   - one whose goomba installation was interrupted is an
//     ErrIncompleteEnvironment error, also wrapping goomba.ErrIncomplete,
//     and nothing is removed: see RemoveIncompleteEnvironment;
//   - an empty directory is filled; any other directory that is not an
//     environment makes goomba refuse with goomba.ErrPrefixNotEmpty;
//   - other errors are returned wrapped, among them goomba.ErrUnsafePrefix
//     for an environment that does not belong to the current user (one
//     built as root and used by another account, for example) or that
//     other users can write: the goomba backend reuses only the current
//     user's own environments, where micromamba reuses any it can run.
//
// Either way, the Python and pip versions are checked as
// CreateEnvironmentMamba checks them. The environment's Backend is
// BackendGoomba and its MicromambaPath is empty.
//
// progressCallback, if not nil, receives the progress: "Loading channel
// index" (channels), "Solving environment", "Fetching packages" (bytes,
// total -1 when unknown), "Linking packages" (packages), "Compiling Python
// files" (files) and "Python environment created successfully". It is
// never called concurrently and not called once ctx is done; it must
// return quickly, because the installation waits for it (and the timeout
// keeps running).
//
// On Windows it fails with ErrBackendUnsupported before doing anything.
// It is safe for concurrent use with different environments.
func CreateEnvironmentGoombaWithOptions(ctx context.Context, envName, rootDir string, opts GoombaOptions, progressCallback ProgressCallback) (*PythonEnvironment, error) {
	if err := checkGoombaHost(); err != nil {
		return nil, err
	}
	j, err := newGoombaJob(ctx, envName, rootDir, opts, progressCallback)
	if err != nil {
		return nil, err
	}
	exists, err := environmentStatus(j.prefix)
	if err != nil {
		return nil, err
	}
	if !exists {
		j.env.IsNew = true
		if err := j.create(ctx, goombaSpecs(j.opts.PythonVersion, j.opts.Specs), nil); err != nil {
			return nil, fmt.Errorf("error creating environment: %w", err)
		}
		j.progress.say("Python environment created successfully", 100, 100)
	}
	setCondaEnvPaths(j.env, runtime.GOOS, j.requested)
	if err := checkCondaEnvironment(j.env, j.requested); err != nil {
		return nil, err
	}
	return j.env, nil
}

// CreateEnvironmentFromJSONFileGoomba creates an environment from a JSON
// specification file (see EnvironmentSpec and FreezeToFile) with goomba.
//
// goomba cannot install into an existing environment, so unlike
// CreateEnvironmentFromJSONFileWithOptions, which creates the base
// environment and then installs each conda package separately, it solves
// python, pip and every conda package of the specification in one solve
// and installs the result. The environment rootDir/envs/<spec name> must
// not exist yet, or be an empty directory (goomba.ErrPrefixNotEmpty); an
// interrupted or busy one is reported as CreateEnvironmentGoombaWithOptions
// reports it. When Packages is not empty, the conda packages come from
// Packages only, as for CreateEnvironmentFromJSONFileWithOptions; otherwise
// the legacy CondaPackages are used.
//
//   - Channels: the specification's, else gopts.Channels, else
//     conda-forge, in strict priority: every package comes from the first
//     channel listed that has its name. A specification with several
//     channels must list them highest priority first; FreezeToFile (with
//     either backend) does not record that order. The micromamba restore
//     tries each package on one channel after another; goomba does not.
//   - Python: the specification's PythonVersion, else the version of
//     python pinned in Packages, else gopts.PythonVersion.
//   - Packages with Source "conda" are pinned exactly (version equal, and
//     build equal when given), and the solution must hold exactly that
//     version and build string. Legacy CondaPackages are passed through as
//     match specs. gopts.Specs are added.
//   - Checksums: with ropts.VerifyChecksums, every conda package of
//     Packages that has a SHA256 must equal the solved archive's sha256
//     (ErrChecksumMismatch). With ropts.Strict as well, Packages must list
//     conda packages, each with a SHA256, and every package of the solution
//     must be one of them with a matching SHA256, so that everything conda
//     installs was verified. Strict covers the conda packages only: pip
//     entries need no SHA256 and are never checksum-verified. These checks
//     run after resolving and before anything is installed, so a failure
//     leaves no environment behind. (The micromamba restore only checks
//     that checksums are present, for pip entries as well, so its Strict
//     mode refuses a freeze that lists pip packages.)
//   - Pip packages are installed afterwards: the Packages with Source "pip"
//     one at a time (as CreateEnvironmentFromJSONFileWithOptions does),
//     then the legacy PipPackages lines that Packages does not name, in
//     order: the lines a freeze cannot pin as name==version (VCS, URL and
//     local installs, editable ones, ===). Without Packages, the legacy
//     PipPackages are installed. (CreateEnvironmentFromJSONFileWithOptions
//     ignores PipPackages whenever Packages is not empty, so it drops those
//     lines.) The pip step runs after ctx and gopts.Timeout, which cover
//     resolving and installing the conda packages, and ignores them.
//   - While the new environment is checked and the pip step runs, the
//     environment is locked as goomba locks it during an installation, so
//     that other callers see it as busy (goomba.ErrPrefixLocked) rather
//     than reusing an environment whose pip step may still fail. If the
//     lock cannot be taken, the restore goes on without it.
//   - If the check or the pip step fails, the environment is left in
//     place, with its conda packages installed; the error says so and
//     where. Remove the directory before retrying: a retry fails with
//     goomba.ErrPrefixNotEmpty.
//
// gopts otherwise applies as for CreateEnvironmentGoombaWithOptions. On
// Windows it fails with ErrBackendUnsupported before reading filePath.
func CreateEnvironmentFromJSONFileGoomba(ctx context.Context, filePath, rootDir string, ropts RestoreOptions, gopts GoombaOptions, progressCallback ProgressCallback) (*PythonEnvironment, error) {
	if err := checkGoombaHost(); err != nil {
		return nil, err
	}
	jsonData, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("error reading JSON file: %w", err)
	}
	var es EnvironmentSpec
	if err := json.Unmarshal(jsonData, &es); err != nil {
		return nil, fmt.Errorf("error unmarshaling JSON: %w", err)
	}
	if err := checkStrictSpec(&es, ropts); err != nil {
		return nil, err
	}
	condaSpecs, err := restoreCondaSpecs(&es)
	if err != nil {
		return nil, err
	}
	opts := gopts
	opts.PythonVersion = restorePythonVersion(&es, gopts.PythonVersion)
	if len(es.Channels) > 0 {
		opts.Channels = es.Channels
	}
	opts.Specs = append(append([]string(nil), gopts.Specs...), condaSpecs...)

	j, err := newGoombaJob(ctx, es.Name, rootDir, opts, progressCallback)
	if err != nil {
		return nil, err
	}
	// An existing environment is never reused (goomba refuses to install
	// into it), but an interrupted or busy one is reported as such.
	if _, err := environmentStatus(j.prefix); err != nil {
		return nil, err
	}
	j.env.IsNew = true
	verify := func(lf *lock.Lockfile) error { return verifyRestore(&es, lf, ropts) }
	if err := j.create(ctx, goombaSpecs(j.opts.PythonVersion, j.opts.Specs), verify); err != nil {
		var u *solve.Unsolvable
		if errors.As(err, &u) && len(j.opts.Channels) > 1 {
			return nil, fmt.Errorf("error creating environment: %w (the specification lists %d channels, which the goomba restore uses in strict priority in the order listed; FreezeToFile does not record that order, so check it)", err, len(j.opts.Channels))
		}
		return nil, fmt.Errorf("error creating environment: %w", err)
	}
	// From here to the end of the pip step, hold the lock goomba holds
	// while it installs, so that other callers see a busy environment
	// instead of reusing one whose pip step may still fail. Someone may
	// have taken it in the moment since goomba released it; then go on
	// without it.
	if restoreAfterInstallHook != nil {
		restoreAfterInstallHook(j.prefix)
	}
	if unlock, err := lockEnvDir(j.prefix); err == nil {
		defer unlock()
	}
	j.progress.say("Python environment created successfully", 100, 100)
	env := j.env
	setCondaEnvPaths(env, runtime.GOOS, j.requested)
	if err := checkCondaEnvironment(env, j.requested); err != nil {
		return nil, leftInPlace(j.prefix, "checking the new environment", err)
	}
	if err := restorePipPackages(env, &es, progressCallback); err != nil {
		return nil, leftInPlace(j.prefix, "the pip step", err)
	}
	if progressCallback != nil {
		progressCallback("Finished creating environment from JSON file", 100, 100)
	}
	return env, nil
}

// checkStrictSpec applies, before anything is resolved, the part of
// ropts.Strict (with VerifyChecksums) that needs no solution: es.Packages
// must list conda packages, each with a SHA256. Pip entries are exempt:
// their checksums are never verified.
func checkStrictSpec(es *EnvironmentSpec, ropts RestoreOptions) error {
	if !ropts.Strict || !ropts.VerifyChecksums {
		return nil
	}
	conda := 0
	for _, pkg := range es.Packages {
		if pkg.Source != "conda" {
			continue
		}
		conda++
		if pkg.SHA256 == "" {
			return fmt.Errorf("strict mode: package %q lacks SHA256 checksum", pkg.Name)
		}
	}
	if conda == 0 {
		return errors.New("strict mode: the specification lists no conda packages, so there are no checksums to verify")
	}
	return nil
}

// restorePythonVersion returns the Python version a restore of es asks
// for: es.PythonVersion, else the major.minor version of python pinned in
// es.Packages (those that the restore uses), else fallback.
func restorePythonVersion(es *EnvironmentSpec, fallback string) string {
	if es.PythonVersion != "" {
		return es.PythonVersion
	}
	for _, p := range es.Packages {
		if p.Source == "conda" && strings.EqualFold(p.Name, "python") {
			if v, err := ParseVersion(p.Version); err == nil && v.Minor >= 0 {
				return v.MinorString()
			}
		}
	}
	return fallback
}

// restoreAfterInstallHook, if set, runs in CreateEnvironmentFromJSONFileGoomba
// between goomba's installation and taking the environment's lock; tests
// use it to take the lock first.
var restoreAfterInstallHook func(prefix string)

// leftInPlace explains a restore that failed after the conda packages were
// installed: the environment stays where it is.
func leftInPlace(prefix, stage string, cause error) error {
	return fmt.Errorf("%w (the conda packages are installed, but %s failed; the environment is left at %s: remove it before retrying, or a retry fails with goomba.ErrPrefixNotEmpty)", cause, stage, prefix)
}

// restorePipPackages installs the pip packages of es into env. With
// Packages: its packages with Source "pip", one at a time, as
// CreateEnvironmentFromJSONFileWithOptions does; then the legacy
// PipPackages lines that none of them names, in order, which are the lines
// a freeze cannot pin as name==version. Without Packages: the legacy
// PipPackages.
func restorePipPackages(env *PythonEnvironment, es *EnvironmentSpec, progressCallback ProgressCallback) error {
	rest := es.PipPackages
	if len(es.Packages) > 0 {
		pinned := map[string]bool{}
		for _, pkg := range es.Packages {
			if pkg.Source != "pip" {
				continue
			}
			pinned[normalizePipName(pkg.Name)] = true
			if err := env.PipInstallPackage(pkg.Name+"=="+pkg.Version, "https://pypi.org/simple", "", true, progressCallback); err != nil {
				return fmt.Errorf("error installing pip package %s: %w", pkg.Name, err)
			}
			if progressCallback != nil {
				progressCallback(fmt.Sprintf("Installing package %s...", pkg.Name), 50, 100)
			}
		}
		rest = nil
		for _, line := range es.PipPackages {
			if name := pipLineName(line); name == "" || !pinned[name] {
				rest = append(rest, line)
			}
		}
	}
	if len(rest) > 0 {
		if err := env.PipInstallPackages(rest, "https://pypi.org/simple", "", true, progressCallback); err != nil {
			return fmt.Errorf("error installing pip packages: %w", err)
		}
	}
	return nil
}

// pipNameRE matches the project name at the start of a pip requirement.
var pipNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*`)

// pipLineName returns the normalised project name a pip freeze line starts
// with, or "" for a line that starts with an option (such as -e).
func pipLineName(line string) string {
	return normalizePipName(pipNameRE.FindString(strings.TrimSpace(line)))
}

// pipNameSepRE matches the runs of separators PEP 503 folds.
var pipNameSepRE = regexp.MustCompile(`[-_.]+`)

// normalizePipName normalises a Python project name as PEP 503 does.
func normalizePipName(name string) string {
	return pipNameSepRE.ReplaceAllString(strings.ToLower(name), "-")
}

// goombaJob is a goomba environment being created or reused.
type goombaJob struct {
	env       *PythonEnvironment // RootDir, EnvironmentName and EnvPath set
	requested Version            // the requested Python version
	opts      GoombaOptions      // with defaults
	absRoot   string             // rootDir made absolute
	prefix    string             // the environment's absolute path
	progress  *goombaProgress
}

// newGoombaJob checks the request and prepares rootDir as
// CreateEnvironmentMamba does (created if missing, must be writable).
func newGoombaJob(ctx context.Context, envName, rootDir string, opts GoombaOptions, cb ProgressCallback) (*goombaJob, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	opts = opts.withDefaults()
	if opts.Timeout < 0 {
		return nil, fmt.Errorf("negative GoombaOptions.Timeout %v", opts.Timeout)
	}
	if !pythonVersionRE.MatchString(opts.PythonVersion) {
		return nil, fmt.Errorf("error parsing requested python version: %q is not a version such as 3.11 or 3.11.9", opts.PythonVersion)
	}
	requested, err := ParseVersion(opts.PythonVersion)
	if err != nil {
		return nil, fmt.Errorf("error parsing requested python version: %v", err)
	}
	if err := checkEnvName(envName); err != nil {
		return nil, err
	}
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("error resolving root directory: %w", err)
	}
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return nil, fmt.Errorf("error creating directory: %w", err)
	}
	if !isDirWritable(rootDir) {
		return nil, fmt.Errorf("root directory is not writable: %s", rootDir)
	}
	env := &PythonEnvironment{
		BaseEnvironment: BaseEnvironment{
			EnvironmentName: envName,
			RootDir:         rootDir,
			EnvPath:         filepath.Join(rootDir, "envs", envName),
			// Major -1: no micromamba version to report.
			MicromambaVersion: Version{Major: -1, Minor: -1, Patch: -1},
			Backend:           BackendGoomba,
		},
	}
	return &goombaJob{
		env:       env,
		requested: requested,
		opts:      opts,
		absRoot:   absRoot,
		prefix:    filepath.Join(absRoot, "envs", envName),
		progress:  &goombaProgress{cb: cb, ctx: ctx},
	}, nil
}

// checkEnvName refuses environment names that are not one plain path
// element, so that rootDir/envs/NAME stays inside rootDir/envs.
func checkEnvName(name string) error {
	if name == "" || name == "." || !filepath.IsLocal(name) || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid environment name %q: it must be one path element", name)
	}
	return nil
}

// environmentStatus reports, with goomba.CheckComplete, whether prefix
// holds an environment to reuse (true), or nothing a creation must leave
// alone (false: it is missing, an empty directory, or not an environment;
// goomba refuses to install into a non-empty one with
// goomba.ErrPrefixNotEmpty). An environment another process is installing
// or removing is an error wrapping goomba.ErrPrefixLocked, an interrupted
// one an ErrIncompleteEnvironment error that also wraps
// goomba.ErrIncomplete; any other error (goomba.ErrUnsafePrefix, ENOTDIR,
// ...) is returned wrapped. Nothing is waited for or removed.
func environmentStatus(prefix string) (bool, error) {
	err := goomba.CheckComplete(prefix)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, goomba.ErrPrefixLocked):
		return false, fmt.Errorf("environment %s is busy: another process is installing or removing it: %w", prefix, err)
	case errors.Is(err, goomba.ErrIncomplete):
		return false, fmt.Errorf("%w: %s: a goomba installation into it was interrupted; remove it with RemoveIncompleteEnvironment and create it again: %w", ErrIncompleteEnvironment, prefix, err)
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case errors.Is(err, goomba.ErrUnsafePrefix):
		return false, fmt.Errorf("environment %s cannot be used by the goomba backend: it must belong to the current user, and neither it nor a directory above it may be writable by other users (micromamba has no such rule): %w", prefix, err)
	default:
		return false, fmt.Errorf("error checking environment %s: %w", prefix, err)
	}
}

// RemoveIncompleteEnvironment removes the environment rootDir/envs/envName
// when a goomba installation into it was interrupted, the state that the
// goomba functions report as ErrIncompleteEnvironment. The intended pattern
// is:
//
//	env, err := jumpboot.CreateEnvironmentGoomba(name, root, "3.11", "conda-forge", nil)
//	if errors.Is(err, jumpboot.ErrIncompleteEnvironment) {
//		if err = jumpboot.RemoveIncompleteEnvironment(name, root); err == nil {
//			env, err = jumpboot.CreateEnvironmentGoomba(name, root, "3.11", "conda-forge", nil)
//		}
//	}
//
// It removes nothing else: goomba.RemoveIncomplete checks, under the
// environment's lock, that the installation was interrupted. Its errors
// are returned wrapped:
//   - goomba.ErrPrefixLocked while another process installs or removes the
//     environment (or a goomba restore runs its pip step): call it again
//     later;
//   - an error wrapping fs.ErrNotExist when the directory is missing or
//     holds no environment;
//   - ErrEnvironmentNotIncomplete, wrapping goomba's own "not removed: it
//     holds a finished environment" error, for a finished environment;
//   - goomba.ErrUnsafePrefix for a directory other users could change.
//
// envName is checked as the create functions check it, and on Windows it
// fails with ErrBackendUnsupported, as they do.
func RemoveIncompleteEnvironment(envName, rootDir string) error {
	if err := checkGoombaHost(); err != nil {
		return err
	}
	if err := checkEnvName(envName); err != nil {
		return err
	}
	prefix := filepath.Join(rootDir, "envs", envName)
	if err := goomba.RemoveIncomplete(prefix); err != nil {
		// goomba's refusal of a finished environment wraps no sentinel:
		// recognise it by asking whether the environment is finished.
		if !errors.Is(err, goomba.ErrPrefixLocked) && !errors.Is(err, fs.ErrNotExist) && goomba.CheckComplete(prefix) == nil {
			return fmt.Errorf("%w: %s holds a finished environment: %w", ErrEnvironmentNotIncomplete, prefix, err)
		}
		return fmt.Errorf("error removing the incomplete environment %s: %w", prefix, err)
	}
	return nil
}

// create resolves specs and installs the result into the job's prefix
// within the job's timeout. verify, if not nil, checks the lockfile after
// resolving and before anything is installed.
func (j *goombaJob) create(ctx context.Context, specs []string, verify func(*lock.Lockfile) error) error {
	tctx, cancel := context.WithTimeout(ctx, j.opts.Timeout)
	defer cancel()
	j.progress.setContext(tctx)
	defer j.progress.setContext(ctx)
	err := j.lockAndInstall(tctx, specs, verify)
	if err != nil && errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return fmt.Errorf("timed out after %v (GoombaOptions.Timeout): %w", j.opts.Timeout, err)
	}
	return err
}

// lockAndInstall is goomba.Create done in its two steps, goomba.Lock and
// goomba.InstallLock, so that the lockfile can be verified and its archive
// sizes reported before the installation starts.
func (j *goombaJob) lockAndInstall(ctx context.Context, specs []string, verify func(*lock.Lockfile) error) error {
	pkgsDir := filepath.Join(j.absRoot, "goomba", "pkgs")
	repodataDir := filepath.Join(j.absRoot, "goomba", "repodata")
	for _, d := range []string{pkgsDir, repodataDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("error creating goomba cache directory: %w", err)
		}
	}
	inst := goomba.InstallOptions{
		PkgsDir:    pkgsDir,
		Client:     j.opts.Client,
		CompilePyc: !j.opts.NoPyc,
		Progress:   j.progress.installEvent,
	}
	lo := goomba.LockOptions{
		Specs:        specs,
		Channels:     j.opts.Channels,
		ChannelAlias: j.opts.ChannelAlias,
		RepodataDir:  repodataDir,
		Client:       j.opts.Client,
		Progress:     j.progress.lockEvent,
	}
	// Refuse what cannot work (an unsafe or non-empty prefix or cache, a
	// spec or channel that does not parse) before any detection or
	// network access, as goomba.Create does.
	if err := goomba.ValidateInstall(j.prefix, inst); err != nil {
		return err
	}
	if err := goomba.ValidateLock(lo); err != nil {
		return err
	}
	virt, err := virtual.Detect(ctx, virtual.Options{Overrides: j.opts.VirtualOverrides, NvidiaSMI: j.opts.NvidiaSMI})
	if err != nil {
		return fmt.Errorf("detect virtual packages: %w", err)
	}
	if virt == nil {
		virt = []*record.PackageRecord{}
	}
	lo.Virtual, inst.Virtual = virt, virt
	lf, err := goomba.Lock(ctx, lo)
	if err != nil {
		return err
	}
	if verify != nil {
		if err := verify(lf); err != nil {
			return err
		}
	}
	j.progress.setLockfile(lf)
	return goomba.InstallLock(ctx, j.prefix, lf, inst)
}

// goombaSpecs returns the match specs of a goomba environment:
// python=<pythonVersion>, then extra, then pip unless one of them names pip.
func goombaSpecs(pythonVersion string, extra []string) []string {
	specs := make([]string, 0, len(extra)+2)
	specs = append(specs, "python="+pythonVersion)
	specs = append(specs, extra...)
	if !specsName(specs, "pip") {
		specs = append(specs, "pip")
	}
	return specs
}

// specsName reports whether one of specs names exactly the package name.
// A spec that does not parse names nothing (goomba reports it later).
func specsName(specs []string, name string) bool {
	for _, s := range specs {
		ms, err := spec.ParseMatchSpec(s)
		if err == nil && ms.Name.IsExact() && ms.Name.String() == name {
			return true
		}
	}
	return false
}

// Characters a pinned conda package's fields may hold. Conda versions and
// builds hold no '-' (it separates them in "name-version-build"), and none
// of these characters is match spec syntax that could widen the pin.
var (
	condaNameRE    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	condaVersionRE = regexp.MustCompile(`^[A-Za-z0-9_.+!]+$`)
	condaBuildRE   = regexp.MustCompile(`^[A-Za-z0-9_.+]+$`)
)

// condaPinSpec returns a match spec that selects exactly version of name,
// and exactly build when build is not empty: "name==version=build". With
// "==", the version is an equality, not conda's "=version" prefix match.
// The fields are first checked against the characters conda allows, then
// the spec is parsed back to prove that it says exactly that.
func condaPinSpec(name, version, build string) (string, error) {
	bad := func(why string) error {
		return fmt.Errorf("conda package %q version %q build %q: %s", name, version, build, why)
	}
	if !condaNameRE.MatchString(name) || !condaVersionRE.MatchString(version) || (build != "" && !condaBuildRE.MatchString(build)) {
		return "", bad("not a conda package name, version and build")
	}
	s := name + "==" + version
	if build != "" {
		s += "=" + build
	}
	if err := checkExactPin(s, name, version, build); err != nil {
		return "", bad(err.Error())
	}
	return s, nil
}

// checkExactPin parses the match spec s and returns an error unless it
// selects exactly the package name, version equal to version and, when
// build is not empty, exactly build (any build otherwise), with no other
// condition.
func checkExactPin(s, name, version, build string) error {
	ms, err := spec.ParseMatchSpec(s)
	if err != nil {
		return err
	}
	want, err := spec.ParseVersionSpec("==" + version)
	if err != nil {
		return err
	}
	exact := ms.Name.IsExact() && ms.Name.String() == strings.ToLower(name) && ms.Version.Equal(want) &&
		ms.Channel == "" && ms.Subdir == "" && ms.Namespace == "" && ms.BuildNumber.IsAny() &&
		ms.MD5 == "" && ms.SHA256 == "" && ms.Fn == "" && ms.URL == "" && ms.License == "" &&
		ms.LicenseFamily == "" && ms.Features == "" && len(ms.TrackFeatures) == 0 && !ms.Optional
	if build == "" {
		exact = exact && ms.Build.IsAny()
	} else {
		exact = exact && ms.Build.IsExact() && ms.Build.String() == build
	}
	if !exact {
		return fmt.Errorf("match spec %q does not pin it exactly", s)
	}
	return nil
}

// restoreCondaSpecs returns the match specs for the conda packages of es:
// when es.Packages is not empty, an exact pin for each of its packages with
// Source "conda"; otherwise the legacy CondaPackages as given.
func restoreCondaSpecs(es *EnvironmentSpec) ([]string, error) {
	if len(es.Packages) == 0 {
		return append([]string(nil), es.CondaPackages...), nil
	}
	var out []string
	for _, p := range es.Packages {
		if p.Source != "conda" {
			continue
		}
		s, err := condaPinSpec(p.Name, p.Version, p.Build)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// verifyRestore checks the solved lockfile lf against the conda packages of
// es.Packages before anything is installed, as
// CreateEnvironmentFromJSONFileGoomba documents for ropts: every pinned
// package must have been solved to exactly its version and build string,
// with ropts.VerifyChecksums to its SHA256, and with ropts.Strict as well
// every solved package must be one of them.
func verifyRestore(es *EnvironmentSpec, lf *lock.Lockfile, ropts RestoreOptions) error {
	strict := ropts.Strict && ropts.VerifyChecksums
	solved := make(map[string]*lock.Package, len(lf.Packages))
	for i := range lf.Packages {
		solved[strings.ToLower(lf.Packages[i].Name)] = &lf.Packages[i]
	}
	listed := map[string]PackageSpec{}
	for _, p := range es.Packages {
		if p.Source != "conda" {
			continue
		}
		listed[strings.ToLower(p.Name)] = p
		got, ok := solved[strings.ToLower(p.Name)]
		if !ok {
			if strict {
				return fmt.Errorf("strict mode: conda package %q is not in the solution", p.Name)
			}
			continue
		}
		if got.Version != p.Version || (p.Build != "" && got.Build != p.Build) {
			return fmt.Errorf("conda package %q: the specification pins version %q build %q, the solution has %s-%s-%s",
				p.Name, p.Version, p.Build, got.Name, got.Version, got.Build)
		}
		if ropts.VerifyChecksums && p.SHA256 != "" && !strings.EqualFold(p.SHA256, got.SHA256) {
			return fmt.Errorf("%w: conda package %s-%s-%s: the specification has sha256 %q, the channel's archive has %s",
				ErrChecksumMismatch, got.Name, got.Version, got.Build, p.SHA256, got.SHA256)
		}
	}
	if strict {
		// lf.Packages is sorted by name, so the first offender is
		// deterministic.
		for _, got := range lf.Packages {
			if p, ok := listed[strings.ToLower(got.Name)]; !ok || p.SHA256 == "" {
				return fmt.Errorf("strict mode: %s-%s-%s is in the solution but not among the specification's conda packages with a checksum, so it cannot be verified",
					got.Name, got.Version, got.Build)
			}
		}
	}
	return nil
}

// pipPinRE matches a pip freeze line of the form name==version.
var pipPinRE = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)==([^\s=;@,#]+)$`)

// freezeCondaMeta adds the conda packages of the goomba environment env,
// read from its conda-meta records, to es, whose PipPackages are already
// filled. It fills both forms of EnvironmentSpec:
//   - the legacy CondaPackages ("name=version=build") as the micromamba
//     freeze fills them: packages that pip freeze also lists are left out;
//   - Channels: the channel of every conda package, in package name order
//     (the micromamba freeze leaves out the channels of the packages pip
//     lists);
//   - Packages: every conda package (Source "conda", with its sha256 when
//     the record has a valid one), then every pip package of the form
//     name==version (Source "pip").
func (env *PythonEnvironment) freezeCondaMeta(es *EnvironmentSpec) error {
	abs, err := filepath.Abs(env.EnvPath)
	if err != nil {
		return fmt.Errorf("error reading conda-meta of %s: %w", env.EnvPath, err)
	}
	// An interrupted environment, or one another process is installing or
	// removing, is refused.
	if _, err := environmentStatus(abs); err != nil {
		return err
	}
	// link.ReadCondaMeta reads only regular files of bounded size from a
	// safely resolved conda-meta, and, unlike lock.FromPrefix, accepts
	// records without an https URL or a sha256 (such as micromamba writes
	// for explicit or file:// installs).
	recs, err := link.ReadCondaMeta(abs)
	if err != nil {
		return fmt.Errorf("error reading conda-meta of %s: %w", env.EnvPath, err)
	}
	pipNames := map[string]bool{}
	for _, p := range es.PipPackages {
		name, _, _ := strings.Cut(p, "==")
		pipNames[strings.ToLower(name)] = true
	}
	for _, cm := range recs {
		r := cm.Record
		sha := strings.ToLower(r.SHA256)
		if !isLowerHex64(sha) {
			sha = ""
		}
		es.Packages = append(es.Packages, PackageSpec{Name: r.Name, Version: r.Version, Build: r.Build, SHA256: sha, Source: "conda"})
		// Every package's channel, even of one pip also lists: Packages
		// pins it, so a restore needs its channel.
		if c := shortChannelName(recordChannel(r)); c != "" && !slices.Contains(es.Channels, c) {
			es.Channels = append(es.Channels, c)
		}
		if !pipNames[strings.ToLower(r.Name)] {
			es.CondaPackages = append(es.CondaPackages, r.Name+"="+r.Version+"="+r.Build)
		}
	}
	for _, line := range es.PipPackages {
		if m := pipPinRE.FindStringSubmatch(line); m != nil {
			es.Packages = append(es.Packages, PackageSpec{Name: m[1], Version: m[2], Source: "pip"})
		}
	}
	return nil
}

// recordChannel returns the channel URL of a conda-meta record: its
// Channel, or else its URL without "/<subdir>/<file>".
func recordChannel(r *record.PackageRecord) string {
	if r.Channel != "" {
		return r.Channel
	}
	if r.Subdir == "" {
		return ""
	}
	i := strings.LastIndexByte(r.URL, '/')
	if i < 0 {
		return ""
	}
	base, _ := strings.CutSuffix(r.URL[:i], "/"+r.Subdir)
	if base == r.URL[:i] {
		return ""
	}
	return base
}

// isLowerHex64 reports whether s is 64 lowercase hex digits.
func isLowerHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// shortChannelName shortens a channel below the default channel alias to
// its name, as micromamba lists channels ("conda-forge").
func shortChannelName(ch string) string {
	if name, ok := strings.CutPrefix(ch, spec.DefaultChannelAlias+"/"); ok && name != "" {
		return name
	}
	return ch
}

// goombaProgress turns goomba's events into ProgressCallback calls, one at
// a time (goomba reports installation events from several goroutines), and
// none once its context is done.
type goombaProgress struct {
	mu  sync.Mutex
	cb  ProgressCallback
	ctx context.Context // nil means never done

	sizes      map[string]int64 // archive size by "name-version-build"
	bytesTotal int64            // sum of sizes, or -1 when one is unknown
	bytesDone  int64
	linked     int64
}

// setContext sets the context after which nothing is reported.
func (p *goombaProgress) setContext(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ctx = ctx
}

// say reports one message.
func (p *goombaProgress) say(message string, current, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.report(message, current, total)
}

// report calls the callback, if any and if the context is not done; p.mu
// is held.
func (p *goombaProgress) report(message string, current, total int64) {
	if p.cb != nil && (p.ctx == nil || p.ctx.Err() == nil) {
		p.cb(message, current, total)
	}
}

// setLockfile records the archive sizes of lf for the download progress.
func (p *goombaProgress) setLockfile(lf *lock.Lockfile) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sizes = make(map[string]int64, len(lf.Packages))
	p.bytesTotal, p.bytesDone, p.linked = 0, 0, 0
	for _, pkg := range lf.Packages {
		p.sizes[pkg.Name+"-"+pkg.Version+"-"+pkg.Build] = pkg.Size
		if pkg.Size <= 0 {
			p.bytesTotal = -1
		} else if p.bytesTotal >= 0 {
			p.bytesTotal += pkg.Size
		}
	}
}

// lockEvent reports the resolve's events.
func (p *goombaProgress) lockEvent(e goomba.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch e.Kind {
	case goomba.EventRepodata:
		p.report("Loading channel index", e.Done, e.Total)
	case goomba.EventSolving:
		p.report("Solving environment", 0, -1)
	case goomba.EventSolved:
		p.report("Solving environment", e.Done, e.Total)
	}
}

// installEvent reports the installation's events: downloads in bytes,
// then linking and byte-compilation.
func (p *goombaProgress) installEvent(e goomba.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch e.Kind {
	case goomba.EventFetched:
		p.bytesDone += max(p.sizes[e.Package], 0)
		p.report("Fetching packages", p.bytesDone, p.bytesTotal)
	case goomba.EventLinked:
		p.linked++
		p.report("Linking packages", p.linked, e.Total)
	case goomba.EventCompiling:
		p.report("Compiling Python files", 0, e.Total)
	case goomba.EventCompiled:
		p.report("Compiling Python files", e.Done, e.Total)
	}
}
