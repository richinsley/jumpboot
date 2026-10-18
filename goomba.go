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
	"strings"
	"sync"
	"time"

	"github.com/richinsley/goomba"
	"github.com/richinsley/goomba/lock"
	"github.com/richinsley/goomba/record"
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
	// ErrIncompleteEnvironment reports an environment directory that holds
	// conda-meta/.goomba-incomplete: a goomba installation into it was
	// interrupted without rolling back (the process crashed or was
	// killed), or is still running. jumpboot removes nothing; once no
	// installation is running, remove the directory and create the
	// environment again.
	ErrIncompleteEnvironment = errors.New("environment is incomplete")

	// ErrChecksumMismatch reports a conda package of a JSON specification
	// whose SHA256 differs from that of the archive the channels offer
	// for the same name, version and build.
	ErrChecksumMismatch = errors.New("checksum mismatch")

	// ErrBackendUnsupported reports an operation that the environment's
	// backend cannot perform, such as installing a package into an
	// existing goomba environment with MicromambaInstallPackage.
	ErrBackendUnsupported = errors.New("operation not supported by the environment's backend")
)

// defaultGoombaTimeout bounds resolving plus installing when
// GoombaOptions.Timeout is zero.
const defaultGoombaTimeout = 10 * time.Minute

// goombaIncompleteMarker is the file goomba keeps in conda-meta while it
// installs into a prefix.
const goombaIncompleteMarker = ".goomba-incomplete"

// GoombaOptions configures environment creation with goomba, a pure-Go
// conda resolver and installer that jumpboot calls as a library (no
// micromamba binary is downloaded or run). The zero value selects the same
// defaults as CreateEnvironmentMamba.
//
// goomba reads no environment variables and no configuration files
// (.condarc, MAMBA_ROOT_PREFIX, HTTPS_PROXY, CONDA_OVERRIDE_*, ...):
// everything comes from these options. A GoombaOptions value is only read,
// so one value may be shared by concurrent calls.
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
	// goomba refuses a channel URL on a public host whose name resolves to
	// a private or loopback address; a mirror on the local network must be
	// named by its address literal.
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
// An existing rootDir/envs/envName is reused, as CreateEnvironmentMamba
// does, unless it holds conda-meta/.goomba-incomplete: then the error wraps
// ErrIncompleteEnvironment and nothing is removed.
//
// Either way, the Python and pip versions are checked as
// CreateEnvironmentMamba checks them. The environment's Backend is
// BackendGoomba and its MicromambaPath is empty. progressCallback, if not
// nil, receives the progress: "Loading channel index" (channels),
// "Solving environment", "Fetching packages" (bytes, total -1 when
// unknown), "Linking packages" (packages) and "Compiling Python files"
// (files). It is never called concurrently.
//
// It is safe for concurrent use with different environments.
func CreateEnvironmentGoombaWithOptions(ctx context.Context, envName, rootDir string, opts GoombaOptions, progressCallback ProgressCallback) (*PythonEnvironment, error) {
	j, err := newGoombaJob(ctx, envName, rootDir, opts, progressCallback)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(j.env.EnvPath); os.IsNotExist(err) {
		j.env.IsNew = true
		if err := j.create(ctx, goombaSpecs(j.opts.PythonVersion, j.opts.Specs), nil); err != nil {
			return nil, fmt.Errorf("error creating environment: %w", err)
		}
		j.progress.report("Python environment created successfully", 100, 100)
	} else if err := j.checkComplete(); err != nil {
		return nil, err
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
// not exist yet (goomba.ErrPrefixNotEmpty).
//
//   - Channels: the specification's, else gopts.Channels, else
//     conda-forge, all in strict priority. The micromamba restore tries
//     each package on one channel after another; goomba does not.
//   - Python: the specification's PythonVersion, else gopts.PythonVersion.
//   - Packages with Source "conda" are pinned exactly: version equal, and
//     build equal when given. Legacy CondaPackages are passed through as
//     match specs. gopts.Specs are added.
//   - Checksums: with ropts.VerifyChecksums, every conda package of
//     Packages that has a SHA256 must equal the solved archive's sha256
//     (ErrChecksumMismatch); with ropts.Strict as well, every entry of
//     Packages needs a SHA256 and every conda package must be in the
//     solution. These checks run after resolving and before anything is
//     installed, so a failure leaves no environment behind. (The
//     micromamba restore only checks that checksums are present.)
//   - Pip packages are installed afterwards as
//     CreateEnvironmentFromJSONFileWithOptions installs them: the Packages
//     with Source "pip" one at a time, then the legacy PipPackages. Unlike
//     it, both lists are used when both are present, so that the output of
//     FreezeToFile for a goomba environment (conda Packages, legacy
//     PipPackages) restores completely. Pip checksums are not verified.
//
// gopts otherwise applies as for CreateEnvironmentGoombaWithOptions.
func CreateEnvironmentFromJSONFileGoomba(ctx context.Context, filePath, rootDir string, ropts RestoreOptions, gopts GoombaOptions, progressCallback ProgressCallback) (*PythonEnvironment, error) {
	jsonData, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("error reading JSON file: %w", err)
	}
	var es EnvironmentSpec
	if err := json.Unmarshal(jsonData, &es); err != nil {
		return nil, fmt.Errorf("error unmarshaling JSON: %w", err)
	}
	if ropts.Strict && ropts.VerifyChecksums {
		for _, pkg := range es.Packages {
			if pkg.SHA256 == "" {
				return nil, fmt.Errorf("strict mode: package %q lacks SHA256 checksum", pkg.Name)
			}
		}
	}
	condaSpecs, err := restoreCondaSpecs(&es)
	if err != nil {
		return nil, err
	}
	opts := gopts
	if es.PythonVersion != "" {
		opts.PythonVersion = es.PythonVersion
	}
	if len(es.Channels) > 0 {
		opts.Channels = es.Channels
	}
	opts.Specs = append(append([]string(nil), gopts.Specs...), condaSpecs...)

	j, err := newGoombaJob(ctx, es.Name, rootDir, opts, progressCallback)
	if err != nil {
		return nil, err
	}
	if err := j.checkComplete(); err != nil {
		return nil, err
	}
	j.env.IsNew = true
	verify := func(lf *lock.Lockfile) error { return verifyRestoreChecksums(&es, lf, ropts) }
	if err := j.create(ctx, goombaSpecs(j.opts.PythonVersion, j.opts.Specs), verify); err != nil {
		return nil, fmt.Errorf("error creating environment: %w", err)
	}
	j.progress.report("Python environment created successfully", 100, 100)
	env := j.env
	setCondaEnvPaths(env, runtime.GOOS, j.requested)
	if err := checkCondaEnvironment(env, j.requested); err != nil {
		return nil, err
	}

	for _, pkg := range es.Packages {
		if pkg.Source != "pip" {
			continue
		}
		if err := env.PipInstallPackage(pkg.Name+"=="+pkg.Version, "https://pypi.org/simple", "", true, progressCallback); err != nil {
			return nil, fmt.Errorf("error installing pip package %s: %w", pkg.Name, err)
		}
		if progressCallback != nil {
			progressCallback(fmt.Sprintf("Installing package %s...", pkg.Name), 50, 100)
		}
	}
	if len(es.PipPackages) > 0 {
		if err := env.PipInstallPackages(es.PipPackages, "https://pypi.org/simple", "", true, progressCallback); err != nil {
			return nil, fmt.Errorf("error installing pip packages: %w", err)
		}
	}
	if progressCallback != nil {
		progressCallback("Finished creating environment from JSON file", 100, 100)
	}
	return env, nil
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
		progress:  &goombaProgress{cb: cb},
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

// checkComplete returns an ErrIncompleteEnvironment error when the
// environment holds goomba's incomplete marker.
func (j *goombaJob) checkComplete() error {
	return checkGoombaComplete(j.env.EnvPath)
}

// checkGoombaComplete returns an ErrIncompleteEnvironment error when the
// environment envPath holds goomba's incomplete marker.
func checkGoombaComplete(envPath string) error {
	_, err := os.Lstat(filepath.Join(envPath, "conda-meta", goombaIncompleteMarker))
	switch {
	case err == nil:
		return fmt.Errorf("%w: %s: a goomba installation into it was interrupted or is still running; once none is running, remove the directory and create the environment again", ErrIncompleteEnvironment, envPath)
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("error checking environment %s: %w", envPath, err)
	}
}

// create resolves specs and installs the result into the job's prefix
// within the job's timeout. verify, if not nil, checks the lockfile after
// resolving and before anything is installed.
func (j *goombaJob) create(ctx context.Context, specs []string, verify func(*lock.Lockfile) error) error {
	tctx, cancel := context.WithTimeout(ctx, j.opts.Timeout)
	defer cancel()
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
// The result is parsed back to prove that it says exactly that.
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
	ms, err := spec.ParseMatchSpec(s)
	if err != nil {
		return "", bad(err.Error())
	}
	want, err := spec.ParseVersionSpec("==" + version)
	if err != nil {
		return "", bad(err.Error())
	}
	exact := ms.Name.IsExact() && ms.Name.String() == strings.ToLower(name) && ms.Version.Equal(want) &&
		ms.Channel == "" && ms.Subdir == "" && ms.BuildNumber.IsAny()
	if build == "" {
		exact = exact && ms.Build.IsAny()
	} else {
		exact = exact && ms.Build.IsExact() && ms.Build.String() == build
	}
	if !exact {
		return "", bad(fmt.Sprintf("match spec %q does not pin it exactly", s))
	}
	return s, nil
}

// restoreCondaSpecs returns the match specs for the conda packages of es:
// an exact pin for each package of Packages with Source "conda", then the
// legacy CondaPackages as given.
func restoreCondaSpecs(es *EnvironmentSpec) ([]string, error) {
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
	return append(out, es.CondaPackages...), nil
}

// verifyRestoreChecksums checks the solved lockfile lf against the conda
// packages of es's Packages, as CreateEnvironmentFromJSONFileGoomba
// documents for ropts.
func verifyRestoreChecksums(es *EnvironmentSpec, lf *lock.Lockfile, ropts RestoreOptions) error {
	if !ropts.VerifyChecksums {
		return nil
	}
	solved := make(map[string]*lock.Package, len(lf.Packages))
	for i := range lf.Packages {
		solved[lf.Packages[i].Name] = &lf.Packages[i]
	}
	for _, p := range es.Packages {
		if p.Source != "conda" {
			continue
		}
		got, ok := solved[strings.ToLower(p.Name)]
		if !ok {
			if ropts.Strict {
				return fmt.Errorf("strict mode: conda package %q is not in the solution", p.Name)
			}
			continue
		}
		if p.SHA256 == "" {
			continue
		}
		if !strings.EqualFold(p.SHA256, got.SHA256) {
			return fmt.Errorf("%w: conda package %s-%s-%s: the specification has sha256 %q, the channel's archive has %s",
				ErrChecksumMismatch, got.Name, got.Version, got.Build, p.SHA256, got.SHA256)
		}
	}
	return nil
}

// freezeCondaMeta adds the conda packages of the goomba environment env,
// read from its conda-meta records, to es: one Packages entry with Source
// "conda", version, build and sha256 per package, and the channels.
func (env *PythonEnvironment) freezeCondaMeta(es *EnvironmentSpec) error {
	if err := checkGoombaComplete(env.EnvPath); err != nil {
		return err
	}
	lf, err := lock.FromPrefix(env.EnvPath, "")
	if err != nil {
		return fmt.Errorf("error reading conda-meta of %s: %w", env.EnvPath, err)
	}
	for _, p := range lf.Packages {
		es.Packages = append(es.Packages, PackageSpec{
			Name:    p.Name,
			Version: p.Version,
			Build:   p.Build,
			SHA256:  p.SHA256,
			Source:  "conda",
		})
	}
	for _, c := range lf.Channels {
		c = shortChannelName(c)
		found := false
		for _, have := range es.Channels {
			if have == c {
				found = true
				break
			}
		}
		if !found {
			es.Channels = append(es.Channels, c)
		}
	}
	return nil
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
// a time (goomba reports installation events from several goroutines).
type goombaProgress struct {
	mu sync.Mutex
	cb ProgressCallback

	sizes      map[string]int64 // archive size by "name-version-build"
	bytesTotal int64            // sum of sizes, or -1 when one is unknown
	bytesDone  int64
	linked     int64
}

// report calls the callback, if any.
func (p *goombaProgress) report(message string, current, total int64) {
	if p.cb != nil {
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
