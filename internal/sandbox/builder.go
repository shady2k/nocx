package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

const (
	LinuxBaselineVersion = 9
	MacOSBaselineVersion = 1
	maxPATHEntries       = 16384
	maxELFString         = 4096
	maxELFTags           = 256
	maxELFBytes          = 64 << 20
	maxELFNodes          = 65536
	maxELFDepth          = 64
)

type BuildRequest struct {
	WorkspaceID                         string
	Cwd, HostHome, Shell, Runner        string
	StandardRevision, WorkspaceRevision uint64
	Profile, Delta                      ProfileRoots
	ProfileProvenance                   Provenance
	ReservedRoots                       []string
	RuntimeBase                         string
}

type BuildError struct {
	Code  string `json:"code"`
	Field string `json:"field"`
	Index int    `json:"index"`
	cause error  `json:"-"`
}

func (e *BuildError) Error() string { return "sandbox: prepare failed (" + e.Code + ")" }
func (e *BuildError) Unwrap() error { return e.cause }
func buildErr(code, field string, index int, cause error) error {
	return &BuildError{Code: code, Field: field, Index: index, cause: cause}
}

type Prepared struct {
	Policy            Policy
	Digest            string
	Runtime           RuntimePaths
	Roots             []*os.File
	Workspace         *os.File
	ownedRuntime      bool
	mu                sync.Mutex
	descriptorsClosed bool
	runtimeCleaned    bool
}

// Close releases pinned descriptors, but deliberately leaves runtime alive.
func (p *Prepared) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeDescriptorsLocked()
}

func (p *Prepared) closeDescriptorsLocked() error {
	if p.descriptorsClosed {
		return nil
	}
	var first error
	for _, f := range p.Roots {
		if f != nil {
			if err := f.Close(); err != nil && first == nil {
				first = err
			}
		}
	}
	p.Roots = nil
	if p.Workspace != nil {
		if err := p.Workspace.Close(); err != nil && first == nil {
			first = err
		}
		p.Workspace = nil
	}
	p.descriptorsClosed = true
	return first
}

// Cleanup is only safe after the helper has confirmed the process absent.
func (p *Prepared) Cleanup() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.closeDescriptorsLocked(); err != nil {
		return err
	}
	if !p.ownedRuntime || p.Runtime.Root == "" || p.runtimeCleaned {
		return nil
	}
	if err := os.RemoveAll(p.Runtime.Root); err != nil {
		return err
	}
	p.runtimeCleaned = true
	return nil
}

// Environment constructs the launch environment without inheriting host HOME/XDG/TMP values.
func (p *Prepared) Environment(hostEnv []string) []string {
	out := make([]string, 0, len(hostEnv)+9)
	for _, item := range hostEnv {
		key, _, ok := strings.Cut(item, "=")
		if ok {
			switch key {
			case "HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "TMPDIR", "TMP", "TEMP", "NOCX_SANDBOX":
				continue
			}
		}
		out = append(out, item)
	}
	out = append(out, "HOME="+p.Runtime.Home, "XDG_CONFIG_HOME="+p.Runtime.Config, "XDG_DATA_HOME="+p.Runtime.Data, "XDG_CACHE_HOME="+p.Runtime.Cache, "XDG_STATE_HOME="+p.Runtime.State, "TMPDIR="+p.Runtime.Temp, "TMP="+p.Runtime.Temp, "TEMP="+p.Runtime.Temp, "NOCX_SANDBOX=filesystem")
	return out
}

type buildRoot struct {
	path       string
	access     Access
	kind       RootKind
	provenance Provenance
	identity   FileIdentity
	pin        *os.File
}

func newBuildRoot(path string, access Access, kind RootKind, provenance Provenance) (buildRoot, error) {
	id, e := identity(path)
	if e != nil {
		return buildRoot{}, e
	}
	pin, e := openPinned(path, kind)
	if e != nil {
		return buildRoot{}, e
	}
	if e = verifyPinned(pin, id, kind); e != nil {
		_ = pin.Close()
		return buildRoot{}, e
	}
	return buildRoot{path: path, access: access, kind: kind, provenance: provenance, identity: id, pin: pin}, nil
}

func Prepare(req BuildRequest) (*Prepared, error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return nil, buildErr("unsupported_backend", "backend", 0, nil)
	}
	if len(req.ReservedRoots) > 128 {
		return nil, buildErr("too_many_reserved_roots", "reservedRoots", 128, nil)
	}
	if len(req.Profile.ReadOnlyDirs) > MaxProfileRoots || len(req.Profile.ReadWriteDirs) > MaxProfileRoots || len(req.Delta.ReadOnlyDirs) > MaxProfileRoots || len(req.Delta.ReadWriteDirs) > MaxProfileRoots {
		return nil, buildErr("too_many_roots", "profile", 0, nil)
	}
	cwd, err := canonicalDir(req.Cwd)
	if err != nil {
		return nil, buildErr("invalid_workspace", "cwd", 0, err)
	}
	home, err := canonicalDir(req.HostHome)
	if err != nil {
		return nil, buildErr("invalid_home", "home", 0, err)
	}
	pathEnv := os.Getenv("PATH")
	if !filepath.IsAbs(req.Shell) {
		if strings.ContainsRune(req.Shell, os.PathSeparator) {
			return nil, buildErr("invalid_shell", "shell", 0, nil)
		}
		resolved, e := resolveExecutable(req.Shell, pathEnv)
		if e != nil {
			return nil, buildErr("shell_unavailable", "shell", 0, e)
		}
		req.Shell = resolved
	}
	for _, artifact := range [...]struct{ field, path string }{{"shell", req.Shell}, {"runner", req.Runner}} {
		field, path := artifact.field, artifact.path
		cp, e := canonicalExisting(path)
		if e != nil {
			return nil, buildErr("invalid_artifact", field, 0, e)
		}
		st, e := os.Stat(cp)
		if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o111 == 0 {
			return nil, buildErr("invalid_artifact", field, 0, e)
		}
		if field == "shell" {
			req.Shell = cp
		} else {
			req.Runner = cp
		}
	}
	roots := make([]buildRoot, 0, 64)
	pinsTransferred := false
	defer func() {
		if !pinsTransferred {
			for _, root := range roots {
				if root.pin != nil {
					_ = root.pin.Close()
				}
			}
		}
	}()
	if e := backendAvailable(); e != nil {
		return nil, buildErr("backend_unsupported", "backend", 0, e)
	}
	if req.ProfileProvenance != StandardRoot && req.ProfileProvenance != WorkspaceProfileRoot {
		return nil, buildErr("invalid_provenance", "profile", 0, nil)
	}
	backend := LinuxLandlock
	if runtime.GOOS == "darwin" {
		backend = MacOSSeatbelt
	}
	addRoot := func(path string, a Access, k RootKind, provenance Provenance, field string, index int) error {
		root, e := newBuildRoot(path, a, k, provenance)
		if e != nil {
			return buildErr("root_pin_failed", field, index, e)
		}
		roots = append(roots, root)
		return nil
	}
	addDir := func(path string, a Access, p Provenance, field string, index int) error {
		cp, e := expandCanonical(path, home, cwd)
		if e != nil {
			return buildErr("invalid_root", field, index, e)
		}
		return addRoot(cp, a, DirectoryRoot, p, field, index)
	}
	if e := addRoot(cwd, ReadWrite, DirectoryRoot, WorkspaceRoot, "cwd", 0); e != nil {
		return nil, e
	}
	for i, v := range req.Profile.ReadOnlyDirs {
		if e := addDir(v, ReadOnly, req.ProfileProvenance, "profile.readOnlyDirs", i); e != nil {
			return nil, e
		}
	}
	for i, v := range req.Profile.ReadWriteDirs {
		if e := addDir(v, ReadWrite, req.ProfileProvenance, "profile.readWriteDirs", i); e != nil {
			return nil, e
		}
	}
	for i, v := range req.Delta.ReadOnlyDirs {
		if e := addDir(v, ReadOnly, LaunchDeltaRoot, "delta.readOnlyDirs", i); e != nil {
			return nil, e
		}
	}
	for i, v := range req.Delta.ReadWriteDirs {
		if e := addDir(v, ReadWrite, LaunchDeltaRoot, "delta.readWriteDirs", i); e != nil {
			return nil, e
		}
	}
	for _, path := range baseline(runtime.GOOS) {
		if _, e := os.Lstat(path); e == nil {
			cp, canonicalErr := filepath.EvalSymlinks(path)
			if canonicalErr != nil {
				return nil, buildErr("invalid_baseline", "baseline", 0, canonicalErr)
			}
			if rootErr := addRoot(filepath.Clean(cp), ReadOnly, DirectoryRoot, SystemRoot, "baseline", 0); rootErr != nil {
				return nil, rootErr
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return nil, buildErr("baseline_unavailable", "baseline", 0, e)
		}
	}
	for _, path := range []string{"/dev/null", "/dev/zero", "/dev/random", "/dev/urandom", "/dev/tty"} {
		if _, e := os.Stat(path); e == nil {
			cp, e := filepath.EvalSymlinks(path)
			if e != nil {
				return nil, buildErr("invalid_device", "baseline", 0, e)
			}
			if e := addRoot(cp, ReadWrite, DeviceRoot, WritableDeviceRoot, "baseline", 0); e != nil {
				return nil, e
			}
		}
	}
	if e := addRoot(req.Shell, ReadOnly, ArtifactRoot, TrustedArtifactRoot, "shell", 0); e != nil {
		return nil, e
	}
	if e := addRoot(req.Runner, ReadOnly, ArtifactRoot, TrustedArtifactRoot, "runner", 0); e != nil {
		return nil, e
	}
	deps, e := discoverDependencies(req.Shell, pathEnv)
	if e != nil {
		return nil, buildErr("dependency_discovery_failed", "dependencies", 0, e)
	}
	runnerDeps, e := discoverDependencies(req.Runner, pathEnv)
	if e != nil {
		return nil, buildErr("dependency_discovery_failed", "dependencies", 0, e)
	}
	deps = append(deps, runnerDeps...)
	for _, p := range deps {
		if p == req.Shell || p == req.Runner {
			continue
		}
		covered := false
		for _, r := range roots {
			if r.provenance == SystemRoot && r.access == ReadOnly && contained(r.path, p) {
				covered = true
				break
			}
		}
		if !covered {
			if len(roots) >= MaxEffectiveRoots {
				return nil, buildErr("too_many_effective_roots", "roots", MaxEffectiveRoots, nil)
			}
			if rootErr := addRoot(p, ReadOnly, ArtifactRoot, DependencyRoot, "dependencies", 0); rootErr != nil {
				return nil, rootErr
			}
		}
	}
	gitRoots, e := linkedGitRoots(cwd)
	if e != nil {
		return nil, buildErr("invalid_worktree_metadata", "git", 0, e)
	}
	for _, p := range gitRoots {
		if rootErr := addRoot(p, ReadWrite, DirectoryRoot, GitRoot, "git", 0); rootErr != nil {
			return nil, rootErr
		}
	}
	runtimePaths, err := createRuntime(req.RuntimeBase)
	if err != nil {
		return nil, buildErr("runtime_unavailable", "runtime", 0, err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(runtimePaths.Root)
		}
	}()
	if rootErr := addRoot(runtimePaths.Root, ReadWrite, DirectoryRoot, RuntimeRoot, "runtime", 0); rootErr != nil {
		return nil, rootErr
	}
	for i, path := range req.ReservedRoots {
		cp, canonicalErr := canonicalReserved(path)
		if canonicalErr != nil {
			return nil, buildErr("invalid_reserved_root", "reservedRoots", i, canonicalErr)
		}
		for _, r := range roots {
			if !overlaps(r.path, cp) {
				continue
			}
			if r.provenance == RuntimeRoot && contained(cp, r.path) {
				continue
			}
			if r.provenance == TrustedArtifactRoot && r.access == ReadOnly && contained(cp, r.path) {
				continue
			}
			return nil, buildErr("reserved_root_conflict", "roots", 0, nil)
		}
	}
	// A trusted binary can never be writable, including through a broad ancestor.
	for _, r := range roots {
		if r.access == ReadWrite && (contained(r.path, req.Shell) || contained(r.path, req.Runner)) {
			return nil, buildErr("trusted_artifact_writable", "roots", 0, nil)
		}
	}
	if conflictErr := validateRootConflicts(roots); conflictErr != nil {
		return nil, conflictErr
	}
	roots = coalesceRoots(roots)
	if len(roots) > MaxEffectiveRoots {
		return nil, buildErr("too_many_effective_roots", "roots", MaxEffectiveRoots, nil)
	}
	sort.Slice(roots, func(i, j int) bool {
		a, b := roots[i], roots[j]
		if a.path != b.path {
			return a.path < b.path
		}
		if a.access != b.access {
			return a.access < b.access
		}
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		if a.provenance != b.provenance {
			return a.provenance < b.provenance
		}
		if a.identity.Device != b.identity.Device {
			return a.identity.Device < b.identity.Device
		}
		return a.identity.Inode < b.identity.Inode
	})
	policyRoots := make([]Root, 0, len(roots))
	var workspaceRoot buildRoot
	for _, r := range roots {
		policyRoots = append(policyRoots, Root{Path: r.path, Access: r.access, Kind: r.kind, Provenance: r.provenance, Identity: r.identity})
		if r.path == cwd && r.provenance == WorkspaceRoot {
			workspaceRoot = r
		}
	}
	if workspaceRoot.pin == nil {
		return nil, buildErr("workspace_identity_missing", "cwd", 0, nil)
	}
	policy := Policy{Version: PolicyVersion, Backend: backend, BackendVersion: backendVersion(runtime.GOOS), WorkspaceID: req.WorkspaceID, WorkspaceRoot: cwd, StandardRevision: req.StandardRevision, WorkspaceRevision: req.WorkspaceRevision, Shell: req.Shell, Runner: req.Runner, Runtime: runtimePaths, Roots: policyRoots}
	p := &Prepared{Policy: policy, Runtime: runtimePaths, ownedRuntime: true}
	p.Workspace, e = dupPinned(workspaceRoot.pin)
	if e != nil {
		return nil, buildErr("workspace_pin_failed", "cwd", 0, e)
	}
	if e = verifyPinned(p.Workspace, workspaceRoot.identity, DirectoryRoot); e != nil {
		_ = p.Close()
		return nil, buildErr("workspace_identity_changed", "cwd", 0, e)
	}
	_, digest, e := EncodePolicy(policy)
	if e != nil {
		_ = p.Close()
		return nil, buildErr("policy_invalid", "policy", 0, e)
	}
	p.Digest = digest
	p.Roots = make([]*os.File, 0, len(roots))
	for _, root := range roots {
		p.Roots = append(p.Roots, root.pin)
	}
	pinsTransferred = true
	if e := createProjections(p, home, cwd, req.Profile, req.Delta); e != nil {
		_ = p.Cleanup()
		return nil, e
	}
	ok = true
	return p, nil
}

func baseline(goos string) []string {
	if goos == "linux" {
		return []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc", "/dev", "/proc", "/sys", "/nix/store"}
	}
	return []string{"/usr", "/bin", "/sbin", "/System/Library", "/System/Volumes/Preboot/Cryptexes", "/Library/Developer/CommandLineTools", "/etc", "/dev", "/private/etc", "/private/var/db"}
}

func backendVersion(goos string) int {
	if goos == "linux" {
		return LinuxBaselineVersion
	}
	return MacOSBaselineVersion
}

func canonicalDir(path string) (string, error) {
	if path == "" || strings.ContainsAny(path, "\x00\r\n") {
		return "", fmt.Errorf("invalid path")
	}
	p, e := canonicalActual(path)
	if e != nil {
		return "", e
	}
	p, e = filepath.Abs(p)
	if e != nil {
		return "", e
	}
	st, e := os.Stat(p)
	if e != nil || !st.IsDir() {
		return "", fmt.Errorf("not directory")
	}
	return filepath.Clean(p), nil
}

func canonicalExisting(path string) (string, error) {
	if path == "" || strings.ContainsAny(path, "\x00\r\n") {
		return "", fmt.Errorf("invalid path")
	}
	return canonicalActual(path)
}

// Reserved application directories remain protected before their first creation.
// Resolve the nearest existing ancestor without treating a broken symlink as absent.
func canonicalReserved(path string) (string, error) {
	if !filepath.IsAbs(path) || len(path) > MaxPathBytes || strings.ContainsAny(path, "\x00\r\n") {
		return "", fmt.Errorf("invalid reserved path")
	}
	current := filepath.Clean(path)
	suffix := ""
	for {
		resolved, err := canonicalActual(current)
		if err == nil {
			return filepath.Join(resolved, suffix), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if _, statErr := os.Lstat(current); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = filepath.Join(filepath.Base(current), suffix)
		current = parent
	}
}

func expandCanonical(path, home, cwd string) (string, error) {
	if path == "" || strings.ContainsAny(path, "\x00\r\n") || len(path) > MaxPathBytes {
		return "", fmt.Errorf("invalid path")
	}
	if path == "~" {
		path = home
	} else if strings.HasPrefix(path, "~/") {
		path = filepath.Join(home, path[2:])
	} else if strings.HasPrefix(path, "~") {
		return "", fmt.Errorf("invalid home shorthand")
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	p, e := canonicalDir(path)
	if e != nil {
		return "", e
	}
	return p, nil
}

// Inputs are canonical absolute paths; equality and component boundaries matter.
func contained(parent, child string) bool {
	if parent == string(os.PathSeparator) {
		return filepath.IsAbs(child)
	}
	return child == parent || strings.HasPrefix(child, parent+string(os.PathSeparator))
}
func overlaps(a, b string) bool { return contained(a, b) || contained(b, a) }
func validateRootConflicts(rs []buildRoot) error {
	readWrite := make(map[string]int)
	for i, r := range rs {
		if r.access == ReadWrite {
			if _, ok := readWrite[r.path]; !ok {
				readWrite[r.path] = i
			}
		}
	}
	for i, r := range rs {
		if r.access != ReadOnly {
			continue
		}
		for parent := r.path; ; parent = filepath.Dir(parent) {
			if _, ok := readWrite[parent]; ok {
				return buildErr("root_conflict", "roots", i, nil)
			}
			if filepath.Dir(parent) == parent {
				break
			}
		}
	}
	return nil
}

func coalesceRoots(rs []buildRoot) []buildRoot {
	sort.Slice(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.access != b.access {
			return a.access < b.access
		}
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		if a.provenance != b.provenance {
			return a.provenance < b.provenance
		}
		return a.path < b.path
	})
	type key struct {
		access     Access
		kind       RootKind
		provenance Provenance
		path       string
	}
	seen := make(map[key]struct{}, len(rs))
	out := make([]buildRoot, 0, len(rs))
	for _, r := range rs {
		skip := false
		for parent := r.path; ; parent = filepath.Dir(parent) {
			if _, ok := seen[key{r.access, r.kind, r.provenance, parent}]; ok {
				skip = true
				break
			}
			if filepath.Dir(parent) == parent {
				break
			}
		}
		if skip {
			if r.pin != nil {
				_ = r.pin.Close()
			}
			continue
		}
		out = append(out, r)
		seen[key{r.access, r.kind, r.provenance, r.path}] = struct{}{}
	}
	return out
}
