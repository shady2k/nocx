package sandbox

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Policy is the validated, canonical-path document both backends enforce.
// It is JSON-serializable for the Linux helper FD handshake. Every path is
// absolute and symlink-resolved; the backend never re-resolves anything.
type Policy struct {
	Workspace     string   `json:"workspace"`
	WritableRoots []string `json:"writableRoots"`
	ReadOnlyRoots []string `json:"readOnlyRoots"`
	WritableFiles []string `json:"writableFiles"`
	WritableDirs  []string `json:"writableDirs"`
	ReadOnlyFiles []string `json:"readOnlyFiles"`
	Shell         string   `json:"shell"`
	Home          string   `json:"home"`
	Tmp           string   `json:"tmp"`
}

// Policy and request bounds (design spec §5.6, §6.1): reject policy above a
// fixed root count or serialized size, and each per-tab path list above the
// fixed entry count.
const (
	maxRoots       = 256
	maxPolicyBytes = 64 * 1024
	maxUserPaths   = 32
)

// BuildPolicy constructs the common filesystem policy for one sandboxed tab
// (design spec §6). env supplies the inherited PATH; shellPath is the
// resolved shell executable; runtimeRoot is the per-session mode-0700 tree
// containing home/ and tmp/ (NewRuntimeRoot).
//
// Errors wrapping ErrInvalidPermissions mean a request parameter (workspace,
// addition, or removal) is unusable (-32602). A malformed or vanished
// persisted global, or any other failure, is a setup failure (-32012).
func BuildPolicy(req Request, shellPath, runtimeRoot string, env []string) (*Policy, error) {
	canon, err := canonicalizeWorkspace(req.Workspace)
	if err != nil {
		return nil, err
	}

	home, err := canonicalExistingDir(filepath.Join(runtimeRoot, "home"))
	if err != nil {
		return nil, NewSetupErrorf("runtime home: %v", err)
	}
	tmp, err := canonicalExistingDir(filepath.Join(runtimeRoot, "tmp"))
	if err != nil {
		return nil, NewSetupErrorf("runtime tmp: %v", err)
	}

	// Canonical system read-only roots seed both the read-only set and the
	// protected-ancestor check. Missing roots are skipped; permission and
	// canonicalization errors are fatal.
	sysRoots, err := canonicalSystemRoots()
	if err != nil {
		return nil, err
	}

	// Bounded per-list (design spec §3.1 maxEntries, §5.1): overflow is a
	// request error for deltas and backend-state error for the baselines.
	// Checked before canonicalization so oversized lists never resolve a
	// single path.
	if len(req.GlobalWritable) > maxUserPaths {
		return nil, NewSetupErrorf("global writable list exceeds %d entries", maxUserPaths)
	}
	if len(req.GlobalReadOnly) > maxUserPaths {
		return nil, NewSetupErrorf("global read-only list exceeds %d entries", maxUserPaths)
	}
	if len(req.AddWritable) > maxUserPaths {
		return nil, NewValidationErrorf("writable addition list exceeds %d entries", maxUserPaths)
	}
	if len(req.RemoveWritable) > maxUserPaths {
		return nil, NewValidationErrorf("writable removal list exceeds %d entries", maxUserPaths)
	}
	if len(req.AddReadOnly) > maxUserPaths {
		return nil, NewValidationErrorf("read-only addition list exceeds %d entries", maxUserPaths)
	}
	if len(req.RemoveReadOnly) > maxUserPaths {
		return nil, NewValidationErrorf("read-only removal list exceeds %d entries", maxUserPaths)
	}

	// Canonical persisted baselines (setup failures) and per-tab deltas
	// (request validation failures).
	globalWritable, err := canonicalPaths(req.GlobalWritable, setupPathErr)
	if err != nil {
		return nil, err
	}
	globalReadOnly, err := canonicalPaths(req.GlobalReadOnly, setupPathErr)
	if err != nil {
		return nil, err
	}
	addWritable, err := canonicalPaths(req.AddWritable, requestPathErr)
	if err != nil {
		return nil, err
	}
	removeWritable, err := canonicalPaths(req.RemoveWritable, requestPathErr)
	if err != nil {
		return nil, err
	}
	addReadOnly, err := canonicalPaths(req.AddReadOnly, requestPathErr)
	if err != nil {
		return nil, err
	}
	removeReadOnly, err := canonicalPaths(req.RemoveReadOnly, requestPathErr)
	if err != nil {
		return nil, err
	}

	// A user writable root, including the mandatory workspace, equal to or an
	// ancestor of a documented system read-only root would erase the
	// read-only execution floor. Workspace/additions are request validation;
	// persisted globals are backend state.
	if writableRootIsProtected(canon, sysRoots) {
		return nil, NewValidationErrorf("workspace conflicts with a read-only system root")
	}
	for _, g := range globalWritable {
		if writableRootIsProtected(g, sysRoots) {
			return nil, NewSetupErrorf("global writable path conflicts with a read-only system root")
		}
	}
	for _, a := range addWritable {
		if writableRootIsProtected(a, sysRoots) {
			return nil, NewValidationErrorf("writable addition conflicts with a read-only system root")
		}
	}

	// Class-scoped removal validation against the effective grant. A removal
	// must match the same class's baseline via filesystem identity (exact
	// directory, not lexical string); a mandatory writable root or a
	// same-class add/remove collision is invalid.
	gitRoot, hasGit := gitCommonDir(canon)

	// pathInSet reports whether path is filesystem-equivalent to any entry
	// in set (all canonical).
	pathInSet := func(path string, set []string) bool {
		for _, s := range set {
			if sameDir(path, s) {
				return true
			}
		}
		return false
	}

	for _, r := range removeWritable {
		if pathInSet(r, addWritable) {
			return nil, NewValidationErrorf("same writable path added and removed")
		}
		if r == canon || (hasGit && r == gitRoot) || r == home || r == tmp {
			return nil, NewValidationErrorf("cannot remove a mandatory root")
		}
		if !pathInSet(r, globalWritable) {
			return nil, NewValidationErrorf("writable removal does not match an allowed path")
		}
	}

	for _, r := range removeReadOnly {
		if pathInSet(r, addReadOnly) {
			return nil, NewValidationErrorf("same read-only path added and removed")
		}
		if !pathInSet(r, globalReadOnly) {
			return nil, NewValidationErrorf("read-only removal does not match an allowed path")
		}
	}

	// Writable roots in the fixed order the spec fixes (design spec §6.2):
	// workspace, optional Git common dir, global writable minus exact
	// removals, writable additions, ephemeral home/tmp.
	writable := []string{canon}
	if hasGit {
		writable = append(writable, gitRoot)
	}
	for _, g := range globalWritable {
		if !pathInSet(g, removeWritable) {
			writable = append(writable, g)
		}
	}
	writable = append(writable, addWritable...)
	writable = append(writable, home, tmp)

	// Cross-class containment (design spec §6, binding invariant 6): a
	// requested read-only root must not equal or descend from an effective
	// writable root — the native additive policy cannot honor the read-only
	// grant there. A writable child under a read-only ancestor remains the
	// one allowed exception. Provenance decides the failure class: a delta on
	// either side is a request error (-32602); a baseline-only conflict is a
	// setup failure (-32012). This runs before normalize so a requested
	// conflict is never silently resolved writable-wins.
	for _, g := range globalReadOnly {
		if pathInSet(g, removeReadOnly) {
			continue
		}
		if w, ok := conflictingWritable(g, writable); ok {
			if pathInSet(w, addWritable) {
				return nil, NewValidationErrorf("persisted read-only path conflicts with a requested writable path")
			}
			return nil, NewSetupErrorf("persisted read-only path conflicts with a writable path")
		}
	}
	for _, a := range addReadOnly {
		if _, ok := conflictingWritable(a, writable); ok {
			return nil, NewValidationErrorf("read-only path conflicts with a writable path")
		}
	}

	// Read-only roots: the canonical system set, user read-only baseline minus
	// exact removals, read-only additions, absolute directories from inherited
	// PATH, and execution roots. Missing optional roots are skipped;
	// permission and canonicalization errors are fatal.
	readonly := make([]string, 0, len(sysRoots)+len(globalReadOnly)+len(addReadOnly)+16)
	readonly = append(readonly, sysRoots...)
	for _, g := range globalReadOnly {
		if !pathInSet(g, removeReadOnly) {
			readonly = append(readonly, g)
		}
	}
	readonly = append(readonly, addReadOnly...)

	shellCanon, err := filepath.EvalSymlinks(shellPath)
	if err != nil {
		return nil, NewSetupErrorf("shell %q: %v", shellPath, err)
	}
	if fi, statErr := os.Stat(shellCanon); statErr != nil || !fi.Mode().IsRegular() {
		return nil, NewSetupErrorf("shell %q is not a regular file", shellCanon)
	}

	for _, dir := range pathEntries(env) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue // relative PATH entries resolve against the child's cwd — skip
		}
		c, ok, e := canonicalOptionalDir(dir)
		if e != nil {
			return nil, NewSetupErrorf("PATH dir %q: %v", dir, e)
		}
		if ok {
			readonly = append(readonly, c)
		}
	}
	executionRoots, err := runtimeReadOnlyRoots(shellCanon, env)
	if err != nil {
		return nil, NewSetupErrorf("runtime roots: %v", err)
	}
	readonly = append(readonly, executionRoots...)

	deviceFiles, deviceDirs, err := writableDevicePaths()
	if err != nil {
		return nil, NewSetupErrorf("device paths: %v", err)
	}

	p := &Policy{
		Workspace:     canon,
		WritableRoots: writable,
		ReadOnlyRoots: readonly,
		WritableFiles: deviceFiles,
		WritableDirs:  deviceDirs,
		ReadOnlyFiles: []string{shellCanon},
		Shell:         shellCanon,
		Home:          home,
		Tmp:           tmp,
	}
	if err := p.normalize(); err != nil {
		return nil, NewSetupErrorf("policy: %v", err)
	}
	return p, nil
}

// ValidatePolicy rejects policy documents that cannot be enforced: NUL or
// empty paths, relative or non-absolute paths, the same canonical path
// declared with conflicting permissions, and documents above the size or
// root-count bounds. It is the first check the Linux helper applies to the
// decoded FD payload.
func ValidatePolicy(p *Policy) error {
	seenRW := make([]string, 0, len(p.WritableRoots)+len(p.WritableFiles)+len(p.WritableDirs))
	for _, roots := range [][]string{p.WritableRoots, p.WritableFiles, p.WritableDirs} {
		for _, root := range roots {
			if err := validatePolicyPath(root); err != nil {
				return err
			}
			seenRW = append(seenRW, root)
		}
	}
	for _, roots := range [][]string{p.ReadOnlyRoots, p.ReadOnlyFiles} {
		for _, root := range roots {
			if err := validatePolicyPath(root); err != nil {
				return err
			}
			for _, rw := range seenRW {
				if sameDir(root, rw) {
					return fmt.Errorf("sandbox: conflicting permissions: read-write and read-only")
				}
			}
		}
	}
	// A read-only root or file must not sit inside a writable directory root:
	// the writable grant would subsume the read-only one (binding invariant 6).
	writableDirs := append(append([]string(nil), p.WritableRoots...), p.WritableDirs...)
	for _, root := range append(append([]string(nil), p.ReadOnlyRoots...), p.ReadOnlyFiles...) {
		if _, ok := conflictingWritable(root, writableDirs); ok {
			return fmt.Errorf("sandbox: read-only path conflicts with a writable root")
		}
	}
	for _, field := range []string{p.Workspace, p.Shell, p.Home, p.Tmp} {
		if err := validatePolicyPath(field); err != nil {
			return err
		}
	}
	if len(p.WritableRoots)+len(p.WritableFiles)+len(p.WritableDirs)+len(p.ReadOnlyRoots)+len(p.ReadOnlyFiles) > maxRoots {
		return fmt.Errorf("sandbox: policy exceeds %d roots", maxRoots)
	}
	if _, err := p.Bytes(); err != nil {
		return err
	}
	return nil
}

// Bytes serializes the policy for the helper FD handshake, enforcing the
// serialized-size bound.
func (p *Policy) Bytes() ([]byte, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("sandbox: serialize policy: %w", err)
	}
	if len(b) > maxPolicyBytes {
		return nil, fmt.Errorf("sandbox: policy exceeds %d bytes", maxPolicyBytes)
	}
	return b, nil
}

func (p *Policy) normalize() error {
	p.WritableRoots = dedupeKeepOrder(p.WritableRoots)
	p.WritableFiles = dedupeKeepOrder(p.WritableFiles)
	p.WritableDirs = dedupeKeepOrder(p.WritableDirs)
	writableDirs := append(append([]string(nil), p.WritableRoots...), p.WritableDirs...)
	p.ReadOnlyRoots = removeUnderWritable(p.ReadOnlyRoots, writableDirs)
	p.ReadOnlyFiles = removeUnderWritable(p.ReadOnlyFiles, writableDirs)
	return ValidatePolicy(p)
}

func dedupeKeepOrder(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		dup := false
		for _, seen := range out {
			if sameDir(s, seen) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, s)
		}
	}
	return out
}

// removeUnderWritable drops read-only entries that an effective writable
// directory root already subsumes (equal or descendant). Backend-derived
// read-only roots (PATH directories, loader roots) resolve this way so the
// writable grant stays authoritative; a user-requested read-only root never
// reaches here — BuildPolicy rejects it before construction.
func removeUnderWritable(in []string, writable []string) []string {
	out := make([]string, 0, len(in))
	for _, root := range in {
		if _, ok := conflictingWritable(root, writable); ok {
			continue
		}
		out = append(out, root)
	}
	return dedupeKeepOrder(out)
}

// conflictingWritable returns the first writable directory root that path
// equals or descends from, or ok=false. Component-aware via pathWithin.
func conflictingWritable(path string, writable []string) (string, bool) {
	for _, w := range writable {
		if pathWithin(w, path) {
			return w, true
		}
	}
	return "", false
}

// pathWithin reports whether path equals root or is a descendant of root,
// component-aware. It uses a cheap lexical fast path (filepath.Rel) and falls
// back to filesystem identity (os.SameFile) to catch case-insensitive and
// normalization aliases that lexical comparison would miss.
func pathWithin(root, path string) bool {
	// Lexical fast path: exact string match or component-aware descendant.
	rel, err := filepath.Rel(root, path)
	if err == nil {
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
			return true
		}
	}
	// Filesystem identity fallback: walk path parents; if any ancestor (or
	// path itself) is the same file as root, it is within. Fail closed on
	// stat errors — only the lexical fast path widens permissions.
	rootInfo, err := os.Stat(root)
	if err != nil {
		return false
	}
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Stat(p)
		if err != nil {
			return false
		}
		if os.SameFile(rootInfo, info) {
			return true
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
	}
	return false
}

// sameDir reports whether two canonical paths refer to the same directory,
// falling back to os.SameFile for case-insensitive/case-normalizing
// filesystems where lexical comparison is not identity. Fails closed: a stat
// failure that would let the caller widen permissions returns false, so the
// caller conservatively treats the paths as distinct.
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	fiA, errA := os.Stat(a)
	fiB, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return false
	}
	return os.SameFile(fiA, fiB)
}

func validatePolicyPath(p string) error {
	if p == "" {
		return errors.New("sandbox: empty path in policy")
	}
	if strings.ContainsRune(p, 0) {
		return errors.New("sandbox: NUL byte in policy path")
	}
	if !filepath.IsAbs(p) {
		return errors.New("sandbox: non-absolute path in policy")
	}
	return nil
}

// canonicalizeWorkspace resolves the workspace through the single
// existing-directory pipeline and classifies any failure as a request
// validation error (-32602), never a setup failure.
func canonicalizeWorkspace(workspace string) (string, error) {
	canon, err := canonicalExistingDir(workspace)
	if err != nil {
		return "", NewValidationErrorf("workspace: %v", err)
	}
	return canon, nil
}

// canonicalExistingDir implements the single existing-directory pipeline for
// user-supplied paths (design spec §6.1): non-empty → no NUL/control →
// absolute → Abs → EvalSymlinks → Stat(dir). It returns a path-free error;
// the caller owns classifying it as request validation (-32602) or
// persisted-global failure (-32012).
func canonicalExistingDir(p string) (string, error) {
	if p == "" {
		return "", errors.New("path is empty")
	}
	for _, r := range p {
		if r == 0 {
			return "", errors.New("path contains a NUL byte")
		}
		if unicode.IsControl(r) {
			return "", errors.New("path contains a control character")
		}
	}
	if !filepath.IsAbs(p) {
		return "", errors.New("path is not absolute")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", errors.New("cannot resolve an absolute path")
	}
	canon, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", errors.New("cannot resolve to an existing directory")
	}
	fi, err := os.Stat(canon)
	if err != nil {
		return "", errors.New("cannot resolve to an existing directory")
	}
	if !fi.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return canon, nil
}

// canonicalPaths resolves each user-supplied path through the single
// existing-directory pipeline and dedupes canonical first-wins. classify maps
// a resolution failure to the right error class: request validation for
// additions/removals, setup failure for the persisted global baseline.
func canonicalPaths(paths []string, classify func(error) error) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		canon, err := canonicalExistingDir(p)
		if err != nil {
			return nil, classify(err)
		}
		out = append(out, canon)
	}
	return dedupeKeepOrder(out), nil
}

func requestPathErr(err error) error { return NewValidationErrorf("invalid path: %v", err) }
func setupPathErr(err error) error   { return NewSetupErrorf("invalid global path: %v", err) }

// canonicalSystemRoots resolves the documented read-only set to canonical
// existing directories (missing roots skipped; permission/canonicalization
// errors fatal). The same slice seeds the read-only roots and the
// protected-ancestor check.
func canonicalSystemRoots() ([]string, error) {
	roots := make([]string, 0, len(systemReadOnlyRoots()))
	for _, root := range systemReadOnlyRoots() {
		c, ok, e := canonicalOptionalDir(root)
		if e != nil {
			return nil, NewSetupErrorf("system root %q: %v", root, e)
		}
		if ok {
			roots = append(roots, c)
		}
	}
	return roots, nil
}

// writableRootIsProtected reports whether a canonical writable candidate is
// equal to or an ancestor of any canonical system read-only root.
func writableRootIsProtected(candidate string, systemRoots []string) bool {
	for _, root := range systemRoots {
		if pathWithin(candidate, root) {
			return true
		}
	}
	return false
}

// canonicalOptionalDir resolves an optional root. Missing roots (ENOENT /
// ENOTDIR) are skipped with ok=false; permission and other errors are fatal.
func canonicalOptionalDir(p string) (string, bool, error) {
	if p == "" || strings.ContainsRune(p, 0) {
		return "", false, nil
	}
	canon, err := filepath.EvalSymlinks(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	fi, err := os.Stat(canon)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	if !fi.IsDir() {
		return "", false, nil
	}
	return canon, true, nil
}

const gitPointerMaxBytes = 4 << 10

// gitCommonDir accepts only Git's linked-worktree layout:
//
//	<common>/.git/worktrees/<name>/.gitdir -> <workspace>/.git
//
// The reciprocal pointer is the authority check. A writable workspace can
// contain an attacker-authored .git file; trusting that file alone would let
// the next sandbox launch grant any existing directory read-write.
func gitCommonDir(workspace string) (string, bool) {
	gitLine, ok := readBoundedRegularFile(workspace, ".git")
	if !ok {
		return "", false
	}
	firstLine := strings.TrimSpace(strings.SplitN(gitLine, "\n", 2)[0])
	target, ok := strings.CutPrefix(firstLine, "gitdir:")
	if !ok {
		return "", false
	}
	target = strings.TrimSpace(target)
	if target == "" || strings.ContainsRune(target, 0) {
		return "", false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(workspace, target)
	}
	canonTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", false
	}
	targetInfo, err := os.Stat(canonTarget) // #nosec G703 -- target is canonicalized and accepted only after reciprocal Git metadata validation below
	if err != nil || !targetInfo.IsDir() {
		return "", false
	}

	worktreesDir := filepath.Dir(canonTarget)
	if filepath.Base(worktreesDir) != "worktrees" {
		return "", false
	}
	common := filepath.Dir(worktreesDir)
	if filepath.Base(common) != ".git" {
		return "", false
	}
	commonInfo, err := os.Stat(common) // #nosec G703 -- common is derived from the canonical, shape-checked target
	if err != nil || !commonInfo.IsDir() {
		return "", false
	}

	backlink, ok := readBoundedRegularFile(canonTarget, "gitdir")
	if !ok {
		return "", false
	}
	backlink = strings.TrimSpace(strings.SplitN(backlink, "\n", 2)[0])
	if backlink == "" || strings.ContainsRune(backlink, 0) {
		return "", false
	}
	if !filepath.IsAbs(backlink) {
		backlink = filepath.Join(canonTarget, backlink)
	}
	canonBacklink, err := filepath.EvalSymlinks(backlink)
	if err != nil {
		return "", false
	}
	canonDotGit, err := filepath.EvalSymlinks(filepath.Join(workspace, ".git"))
	if err != nil || canonBacklink != canonDotGit {
		return "", false
	}
	return common, true
}

// readBoundedRegularFile reads fixed Git metadata names without following a
// symlink outside root and without allowing attacker-controlled file size to
// allocate unbounded memory.
func readBoundedRegularFile(rootPath, name string) (string, bool) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return "", false
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > gitPointerMaxBytes {
		return "", false
	}
	file, err := root.Open(name)
	if err != nil {
		return "", false
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, gitPointerMaxBytes+1))
	if err != nil || len(data) > gitPointerMaxBytes {
		return "", false
	}
	return string(data), true
}

// pathEntries returns the absolute entries of the last PATH in env.
func pathEntries(env []string) []string {
	path := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	if path == "" {
		return nil
	}
	return strings.Split(path, string(os.PathListSeparator))
}

// NewRuntimeRoot creates a fresh mode-0700 per-session runtime tree under
// cacheDir/sandbox-sessions/<random>/ containing home/ and tmp/ (design
// spec §5.2).
func NewRuntimeRoot(cacheDir string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("sandbox: runtime root entropy: %w", err)
	}
	root := filepath.Join(cacheDir, "sandbox-sessions", hex.EncodeToString(b[:]))
	for _, d := range []string{root, filepath.Join(root, "home"), filepath.Join(root, "tmp")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return "", fmt.Errorf("sandbox: runtime root %q: %w", d, err)
		}
	}
	return root, nil
}

// sandboxEnv redirects HOME and the XDG/temp variables into the ephemeral
// runtime tree and marks the session (design spec §5.3). The remaining
// environment is retained.
func sandboxEnv(env []string, home, tmp string) []string {
	deltas := map[string]string{
		"HOME":            home,
		"XDG_DATA_HOME":   filepath.Join(home, ".local", "share"),
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
		"XDG_STATE_HOME":  filepath.Join(home, ".local", "state"),
		"TMPDIR":          tmp,
		"TMP":             tmp,
		"TEMP":            tmp,
		"NOCX_SANDBOX":    "filesystem",
	}
	out := make([]string, 0, len(env)+len(deltas))
	seen := make(map[string]bool, len(deltas))
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			out = append(out, kv)
			continue
		}
		if v, hit := deltas[key]; hit {
			out = append(out, key+"="+v)
			seen[key] = true
			continue
		}
		out = append(out, kv)
	}
	for key, v := range deltas {
		if !seen[key] {
			out = append(out, key+"="+v)
		}
	}
	return out
}

// RemoveRuntimeRoot best-effort deletes a per-session runtime tree. Deletion
// is not secure erase (design spec §5.3).
func RemoveRuntimeRoot(root string) {
	if root == "" {
		return
	}
	_ = os.RemoveAll(root)
}
