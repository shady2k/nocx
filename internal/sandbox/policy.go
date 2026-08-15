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

// Policy document bounds (design spec §5.6): reject policy above a fixed
// root count or serialized size.
const (
	maxRoots       = 256
	maxPolicyBytes = 64 * 1024
)

// BuildPolicy constructs the common filesystem policy for a canonical
// workspace. env supplies the inherited PATH; shellPath is the resolved
// shell executable; runtimeRoot is the per-session mode-0700 tree containing
// home/ and tmp/ (NewRuntimeRoot).
//
// Errors wrapping ErrInvalidWorkspace mean the workspace is unusable
// (-32602). Any other error is a setup failure (-32012).
func BuildPolicy(workspace, shellPath, runtimeRoot string, env []string) (*Policy, error) {
	canon, err := canonicalizeWorkspace(workspace)
	if err != nil {
		return nil, err
	}

	home := filepath.Join(runtimeRoot, "home")
	tmp := filepath.Join(runtimeRoot, "tmp")
	for _, d := range []string{home, tmp} {
		fi, e := os.Stat(d)
		if e != nil {
			return nil, NewSetupErrorf("runtime root %q: %v", d, e)
		}
		if !fi.IsDir() {
			return nil, NewSetupErrorf("runtime root %q is not a directory", d)
		}
	}

	// Writable roots, in the tooltip order the spec fixes (design spec §3.3):
	// workspace, optional Git common dir, ephemeral home/tmp.
	writable := []string{canon}
	if git, ok := gitCommonDir(canon); ok {
		writable = append(writable, git)
	}
	writable = append(writable, home, tmp)

	// Read-only roots: documented system set, canonical execution roots, and
	// absolute directories from inherited PATH. Missing optional roots are
	// skipped; permission and canonicalization errors are fatal.
	readonly := make([]string, 0, len(systemReadOnlyRoots())+16)
	for _, root := range systemReadOnlyRoots() {
		c, ok, e := canonicalOptionalDir(root)
		if e != nil {
			return nil, NewSetupErrorf("system root %q: %v", root, e)
		}
		if ok {
			readonly = append(readonly, c)
		}
	}

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
		return nil, err
	}
	return p, nil
}

// ValidatePolicy rejects policy documents that cannot be enforced: NUL or
// empty paths, relative or non-absolute paths, the same canonical path
// declared with conflicting permissions, and documents above the size or
// root-count bounds. It is the first check the Linux helper applies to the
// decoded FD payload.
func ValidatePolicy(p *Policy) error {
	seenRW := make(map[string]bool, len(p.WritableRoots)+len(p.WritableFiles)+len(p.WritableDirs))
	for _, roots := range [][]string{p.WritableRoots, p.WritableFiles, p.WritableDirs} {
		for _, root := range roots {
			if err := validatePolicyPath(root); err != nil {
				return err
			}
			seenRW[root] = true
		}
	}
	for _, roots := range [][]string{p.ReadOnlyRoots, p.ReadOnlyFiles} {
		for _, root := range roots {
			if err := validatePolicyPath(root); err != nil {
				return err
			}
			if seenRW[root] {
				return fmt.Errorf("sandbox: conflicting permissions for %q: read-write and read-only", root)
			}
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
	writable := make(map[string]bool, len(p.WritableRoots)+len(p.WritableFiles)+len(p.WritableDirs))
	for _, roots := range [][]string{p.WritableRoots, p.WritableFiles, p.WritableDirs} {
		for _, root := range roots {
			writable[root] = true
		}
	}
	p.ReadOnlyRoots = removeWritable(p.ReadOnlyRoots, writable)
	p.ReadOnlyFiles = removeWritable(p.ReadOnlyFiles, writable)
	return ValidatePolicy(p)
}

func dedupeKeepOrder(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func removeWritable(in []string, writable map[string]bool) []string {
	out := make([]string, 0, len(in))
	for _, root := range in {
		if !writable[root] {
			out = append(out, root)
		}
	}
	return dedupeKeepOrder(out)
}

func validatePolicyPath(p string) error {
	if p == "" {
		return errors.New("sandbox: empty path in policy")
	}
	if strings.ContainsRune(p, 0) {
		return errors.New("sandbox: NUL byte in policy path")
	}
	if !filepath.IsAbs(p) {
		return errors.New("sandbox: non-absolute path in policy: " + p)
	}
	return nil
}

// canonicalizeWorkspace applies Abs → EvalSymlinks → Stat and requires an
// existing absolute directory. Any failure is a workspace validation error
// (-32602), never a setup failure.
func canonicalizeWorkspace(workspace string) (string, error) {
	if workspace == "" {
		return "", NewValidationErrorf("workspace is empty")
	}
	if strings.ContainsRune(workspace, 0) {
		return "", NewValidationErrorf("workspace contains a NUL byte")
	}
	if !filepath.IsAbs(workspace) {
		return "", NewValidationErrorf("workspace must be absolute")
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", NewValidationErrorf("cannot resolve an absolute path")
	}
	canon, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", NewValidationErrorf("cannot resolve workspace symlinks")
	}
	fi, err := os.Stat(canon)
	if err != nil {
		return "", NewValidationErrorf("cannot stat workspace")
	}
	if !fi.IsDir() {
		return "", NewValidationErrorf("workspace is not a directory")
	}
	return canon, nil
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
