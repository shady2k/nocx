package shellintegration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// ProtocolVersion is the contract between the manifest, the launch carrier
// and the publisher. Changing the manifest schema, the generation layout or
// the carrier's expectations is a protocol bump with its own compatibility
// rule (design §4: changing launch itself is a protocol-version bump).
const ProtocolVersion = 1

// Fixed names inside ~/.nocx. Only these are ever written; nothing else on
// the host is created or modified, and no rc file is ever touched (N4).
const (
	manifestName   = "manifest.json" // the activation pointer, published by atomic rename
	launchName     = "launch"        // the stable 0700 carrier, installed once, never rewritten
	lockName       = "lock"          // atomic-mkdir lock directory
	lockNonceFile  = "nonce"         // the lock holder's identifying nonce
	tmpName        = "tmp"           // staging for unpublished generations and the manifest temp
	integrationDir = "integration"   // committed generations live here as v<version>/
	genPrefix      = "v"
)

// ourMarkers are the fixed names whose presence makes an existing ~/.nocx
// recognisably ours. Includes the legacy run/ staging dir and the retired
// VERSION marker so a nocx-pu4.6-era host is not mistaken for a foreign
// directory (design §4.1: an existing ~/.nocx that is not recognisably ours
// is never modified and never has its mode changed). "run" is a literal:
// the staging dir it names was deleted with the P7 hand-typed-ssh surface
// (ADR-0024 §1), but hosts that still carry it must stay recognisable.
var ourMarkers = []string{manifestName, launchName, lockName, tmpName, integrationDir, "run", versionFile}

// lockWaitBound is how long a publish waits for a contended lock before
// applying the stale rule. Package-level so tests can shorten it.
var lockWaitBound = 5 * time.Second

// lockPollInterval is the polling cadence while waiting for the lock.
var lockPollInterval = 25 * time.Millisecond

// Bundle is the descriptor both carriers hand to the publisher (AD-8: one
// owner of the behaviour, two carriers). It carries the generation files
// (data, 0600) and optionally the launch carrier (0700), which is installed
// only when absent — never rewritten by a generation publish.
type Bundle struct {
	Protocol int          // manifest contract version; must equal ProtocolVersion
	Version  string       // script version; safe name; names the generation dir v<version>
	Files    []BundleFile // fixed base filenames; "launch" is the carrier, everything else is generation data
}

// BundleFile is one file in a Bundle.
type BundleFile struct {
	Name string      // fixed base filename
	Mode os.FileMode // 0700 for the launch carrier, 0600 for generation data
	Data []byte
}

func (b Bundle) file(name string) (BundleFile, bool) {
	for _, f := range b.Files {
		if f.Name == name {
			return f, true
		}
	}
	return BundleFile{}, false
}

// PublishResult reports what a Publish call did.
type PublishResult struct {
	Published  bool   // a new generation and manifest were written
	Generation string // the active generation dir name (e.g. "v10")
	Version    string // the active script version
	Reason     string // when !Published: why nothing was written
}

// VerifyResult reports whether the committed manifest is fully present.
type VerifyResult struct {
	Installed  bool
	Generation string
	Version    string
	Protocol   int
}

// UninstallResult reports what an Uninstall call removed and what conflicted.
type UninstallResult struct {
	Removed   []string // root-relative paths removed
	Conflicts []string // manifest-owned paths the user modified, left in place
}

// SymlinkError reports a refusal to write through a symlink (design §4.1:
// no path in ~/.nocx is followed through a symlink — not the root, not a
// generation, not tmp, lock, manifest.json or launch).
type SymlinkError struct{ Path string }

func (e *SymlinkError) Error() string {
	return fmt.Sprintf("shellintegration: publisher: refusing to write through symlink %q", e.Path)
}

// ReadonlyError reports a home directory that refuses writes (design §4: a
// read-only $HOME publishes nothing and records no installed fact).
type ReadonlyError struct {
	Path string
	Err  error
}

func (e *ReadonlyError) Error() string {
	return fmt.Sprintf("shellintegration: publisher: %s is not writable: %v", e.Path, e.Err)
}

func (e *ReadonlyError) Unwrap() error { return e.Err }

// ForeignRootError reports an existing ~/.nocx that is not recognisably
// ours; it is never modified and never has its mode changed.
type ForeignRootError struct{ Path string }

func (e *ForeignRootError) Error() string {
	return fmt.Sprintf("shellintegration: publisher: %s exists and is not a nocx directory; refusing to modify it", e.Path)
}

// InvalidManifestError reports a manifest that fails validation; the whole
// manifest is invalid and nothing in it may be acted on.
type InvalidManifestError struct {
	Path   string
	Reason string
}

func (e *InvalidManifestError) Error() string {
	return fmt.Sprintf("shellintegration: publisher: invalid manifest %s: %s", e.Path, e.Reason)
}

// PublishError wraps a failed filesystem boundary operation, naming the op
// and the path so a carrier can classify it.
type PublishError struct {
	Op   string
	Path string
	Err  error
}

func (e *PublishError) Error() string {
	return fmt.Sprintf("shellintegration: publisher: %s %s: %v", e.Op, e.Path, e.Err)
}

func (e *PublishError) Unwrap() error { return e.Err }

// boundaryErr classifies a filesystem boundary failure: permission problems
// become ReadonlyError (the fail-open condition a carrier must recognise),
// everything else a PublishError naming the operation.
func boundaryErr(op, path string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS) {
		return &ReadonlyError{Path: path, Err: err}
	}
	return &PublishError{Op: op, Path: path, Err: err}
}

// Publisher publishes versioned immutable generations of the shell
// integration under a root directory (~/.nocx) through an FS seam. It has
// no knowledge of SSH, SFTP, launchers or the renderer.
type Publisher struct {
	log  log.Logger
	fs   FS
	root string
}

// NewPublisher returns a Publisher writing under root (the remote ~/.nocx)
// through fsys.
func NewPublisher(logger log.Logger, fsys FS, root string) *Publisher {
	return &Publisher{log: logger, fs: fsys, root: root}
}

// join joins path parts under the root. An absolute first part (a path the
// caller already built from p.root) is joined as-is instead of being
// doubled under the root again.
func (p *Publisher) join(parts ...string) string {
	if len(parts) > 0 && filepath.IsAbs(parts[0]) {
		return filepath.Join(parts...)
	}
	return filepath.Join(append([]string{p.root}, parts...)...)
}

// rel returns a root-relative path for reporting.
func (p *Publisher) rel(path string) string {
	r, err := filepath.Rel(p.root, path)
	if err != nil {
		return path
	}
	return r
}

// Publish makes bundle the active generation: it stages every file under
// tmp/<nonce>/, fsyncs it, renames it into integration/v<version>/, then
// renames the manifest into place — last, after every file it names exists
// and is fsynced — and finally bounds the footprint to at most two
// generations and no staging leftovers, all under the lock.
//
// Fail-open contract: any error leaves the previous activation untouched
// and the next attempt converges with no manual cleanup.
func (p *Publisher) Publish(bundle Bundle) (res PublishResult, err error) {
	if verr := validateBundle(bundle); verr != nil {
		// Nothing exists yet: the wrapped error is all the caller needs.
		return PublishResult{}, fmt.Errorf("shellintegration: publisher: invalid bundle: %w", verr)
	}
	created, err := p.prepareRoot()
	if err != nil {
		return PublishResult{}, err
	}
	// If this invocation created the root and then fails before writing
	// anything else, sweep the empty root back to the pre-state so a
	// crashed first publish leaves no ~/.nocx behind. The named err is how
	// this defer sees the failure: every error branch returns explicitly,
	// and an explicit return assigns the named result before the defer runs.
	defer func() {
		if err != nil && created {
			p.sweepEmptyRoot()
		}
	}()

	// ensureBaseDirs is the last point at which the root can still be
	// empty (its first mkdir), so its failure assigns the named err
	// directly: the sweep must see it.
	if err = p.ensureBaseDirs(); err != nil {
		return PublishResult{}, err
	}

	release, err := p.acquireLock()
	if err != nil {
		return PublishResult{}, err
	}
	defer release()

	// The version check is repeated after the lock is held (design §4):
	// between any earlier read and the lock acquisition another publisher
	// may have committed the version we are about to write.
	if installed, skip, checkErr := p.checkInstalled(bundle); checkErr != nil {
		return PublishResult{}, checkErr
	} else if skip {
		p.log.Info("shellintegration: publish skipped", "version", bundle.Version, "reason", installed.Reason)
		p.cleanupOrphans(installed.Generation)
		return installed, nil
	}

	// The launch carrier is a stable 0700 file installed before the first
	// activation and never rewritten as part of publishing a generation
	// (design §4).
	if f, ok := bundle.file(launchName); ok {
		if lerr := p.ensureLaunch(f); lerr != nil {
			return PublishResult{}, lerr
		}
	}

	nonce, err := p.stageGeneration(bundle)
	if err != nil {
		return PublishResult{}, err
	}
	if gerr := p.commitGeneration(bundle, nonce); gerr != nil {
		return PublishResult{}, gerr
	}
	if merr := p.commitManifest(bundle, nonce); merr != nil {
		return PublishResult{}, merr
	}

	res = PublishResult{Published: true, Generation: genDir(bundle.Version), Version: bundle.Version}
	p.cleanupOrphans(res.Generation)
	p.log.Info("shellintegration: published", "version", bundle.Version, "generation", res.Generation)
	return res, nil
}

// sweepEmptyRoot removes the root directory, but only when it is still
// empty: a root with any entry — a concurrent publisher's staging, a
// foreign file, a marker — is never removed. The caller decides ownership
// (only an invocation that created the root may sweep it). Best-effort:
// failure is logged, never fatal.
func (p *Publisher) sweepEmptyRoot() {
	names, err := p.fs.ReadDir(p.root)
	if err != nil || len(names) != 0 {
		return
	}
	if err := p.fs.Remove(p.root); err != nil {
		p.log.Debug("shellintegration: could not remove empty root after failed publish", "err", err)
	}
}

// Verify reports whether the committed manifest is fully present: every
// file it names exists with the recorded hash and mode, and no path is
// followed through a symlink. A matching version string alone never proves
// an installation; only the per-file proof does. A missing manifest is
// simply not installed (no error).
func (p *Publisher) Verify() (VerifyResult, error) {
	var res VerifyResult
	info, err := p.fs.Lstat(p.root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return res, nil
	case err != nil:
		return res, boundaryErr("lstat", p.root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return res, &SymlinkError{Path: p.root}
	}

	m, err := p.readManifest()
	if err != nil {
		return res, err
	}
	if m == nil {
		return res, nil
	}

	gen := p.join(integrationDir, m.Generation)
	ginfo, err := p.fs.Lstat(gen)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return res, nil
	case err != nil:
		return res, boundaryErr("lstat", gen, err)
	}
	if ginfo.Mode()&os.ModeSymlink != 0 {
		return res, &SymlinkError{Path: gen}
	}
	if !ginfo.IsDir() {
		return res, nil
	}

	for name, mf := range m.Files {
		ok, err := p.verifyFile(p.join(gen, name), mf)
		if err != nil {
			return res, err
		}
		if !ok {
			return res, nil // any missing or altered file means not installed
		}
	}
	return VerifyResult{Installed: true, Generation: m.Generation, Version: m.Version, Protocol: m.Protocol}, nil
}

// Uninstall removes only manifest-owned, unmodified files: it verifies each
// recorded hash, removes what matches, reports anything the user changed as
// a conflict, then removes the manifest itself. ~/.nocx is never removed
// recursively; launch, tmp and the root stay in place.
func (p *Publisher) Uninstall() (UninstallResult, error) {
	var res UninstallResult
	if _, err := p.prepareRoot(); err != nil {
		return res, err
	}
	release, err := p.acquireLock()
	if err != nil {
		return res, err
	}
	defer release()

	m, err := p.readManifest()
	if err != nil {
		return res, err
	}
	if m == nil {
		return res, nil // nothing installed
	}

	gen := p.join(integrationDir, m.Generation)
	for name, mf := range m.Files {
		path := p.join(gen, name)
		remove, conflict, err := p.classifyForUninstall(path, mf)
		if err != nil {
			return res, err
		}
		if conflict {
			res.Conflicts = append(res.Conflicts, p.rel(path))
			continue
		}
		if !remove {
			continue // already missing: nothing to remove, not a conflict
		}
		if err := p.fs.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return res, boundaryErr("remove", path, err)
		}
		res.Removed = append(res.Removed, p.rel(path))
	}

	// The generation dir is removed only if our removals emptied it; a
	// conflicted file keeps it in place (reported above).
	if err := p.fs.Remove(gen); err != nil && !errors.Is(err, fs.ErrNotExist) {
		p.log.Warn("shellintegration: uninstall left generation dir in place", "path", gen, "err", err)
	}

	if err := p.fs.Remove(p.join(manifestName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, boundaryErr("remove", p.join(manifestName), err)
	}
	res.Removed = append(res.Removed, manifestName)
	p.log.Info("shellintegration: uninstalled", "removed", len(res.Removed), "conflicts", len(res.Conflicts))
	return res, nil
}

// classifyForUninstall decides what to do with one manifest-owned file:
// remove it (unmodified), report a conflict (user changed it), or skip it
// (already missing).
func (p *Publisher) classifyForUninstall(path string, mf ManifestFile) (remove, conflict bool, err error) {
	info, err := p.fs.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, false, nil
	case err != nil:
		return false, false, boundaryErr("lstat", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, true, nil // replaced by a symlink or a non-file: user-modified
	}
	if info.Mode().Perm() != mustParseMode(mf.Mode) {
		return false, true, nil
	}
	data, err := p.fs.ReadFile(path)
	if err != nil {
		return false, false, boundaryErr("read", path, err)
	}
	if hashBytes(data) != mf.Hash {
		return false, true, nil
	}
	return true, false, nil
}

// prepareRoot ensures the root exists as a real directory and is
// recognisably ours. An existing root that is not ours is refused outright:
// it is never modified and never has its mode changed (design §4.1). The
// bool reports whether THIS call created the root — the caller uses it to
// decide whether a failed publish may sweep the empty root back.
func (p *Publisher) prepareRoot() (created bool, err error) {
	info, err := p.fs.Lstat(p.root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		mkErr := p.fs.Mkdir(p.root, 0o700)
		if mkErr != nil && !errors.Is(mkErr, fs.ErrExist) {
			// fs.ErrExist is a concurrent publisher winning the create
			// race; the root then belongs to the winner, not to us.
			return false, boundaryErr("mkdir", p.root, mkErr)
		}
		// No marker check applies to a root we just created (or that the
		// winner of the race created — an empty root cannot hold foreign
		// data, and accepting it is what lets a crashed first publish
		// converge with no manual cleanup).
		return mkErr == nil, nil
	case err != nil:
		return false, boundaryErr("lstat", p.root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, &SymlinkError{Path: p.root}
	}
	if !info.IsDir() {
		return false, &PublishError{Op: "lstat", Path: p.root, Err: fmt.Errorf("exists and is not a directory")}
	}
	names, err := p.fs.ReadDir(p.root)
	if err != nil {
		return false, boundaryErr("readdir", p.root, err)
	}
	// An empty root is accepted: it cannot hold foreign data, and a crashed
	// first publish leaves exactly this — a root created but never
	// populated. Refusing it would strand every later attempt (design §4:
	// convergence with no manual cleanup).
	if len(names) == 0 {
		return false, nil
	}
	for _, n := range names {
		for _, m := range ourMarkers {
			if n.Name() == m {
				return false, nil // recognisably ours
			}
		}
	}
	return false, &ForeignRootError{Path: p.root}
}

// ensureBaseDirs creates tmp/ and integration/ under the root if absent.
func (p *Publisher) ensureBaseDirs() error {
	for _, d := range []string{tmpName, integrationDir} {
		if err := p.ensureDir(p.join(d), 0o700); err != nil {
			return err
		}
	}
	return nil
}

// ensureDir creates dir with mode if absent; a symlink or a non-directory
// at that path is refused.
func (p *Publisher) ensureDir(dir string, mode os.FileMode) error {
	info, err := p.fs.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if mkErr := p.fs.Mkdir(dir, mode); mkErr != nil && !errors.Is(mkErr, fs.ErrExist) {
			return boundaryErr("mkdir", dir, mkErr)
		}
		info, err = p.fs.Lstat(dir)
		if err != nil {
			return boundaryErr("lstat", dir, err)
		}
	case err != nil:
		return boundaryErr("lstat", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return &SymlinkError{Path: dir}
	}
	if !info.IsDir() {
		return &PublishError{Op: "mkdir", Path: dir, Err: fmt.Errorf("exists and is not a directory")}
	}
	return nil
}

func (p *Publisher) mkdir(path string, mode os.FileMode) error {
	if err := p.fs.Mkdir(path, mode); err != nil {
		return boundaryErr("mkdir", path, err)
	}
	return nil
}

// acquireLock takes the lock, waiting up to lockWaitBound for a contender,
// then applying the stale rule, and returns an idempotent release func.
//
// Lock discipline. The lock is a directory created with mkdir(2) — atomic
// by construction — holding a single nonce file naming the holder's
// attempt. Every publisher creates its staging directory before taking the
// lock and removes it only through the generation rename, so the lock is
// held only for a short, bounded sequence of filesystem operations on one
// filesystem.
//
// Stale rule: a waiter polls for up to lockWaitBound; if the lock persists,
// it removes the lock directory and contends again (a second elapsed bound
// without acquisition fails). The rule trusts neither a remote PID (there
// may be none — the publisher can run on a different machine than the
// filesystem it writes through, so a PID file would be meaningless and
// PIDs recycle) nor the remote wall clock (mtimes are coarse, skewed or
// settable); the only time reference is the waiter's own bounded wait. This
// is safe because breaking a lock can never corrupt state: the manifest
// rename is the only activation pointer, publishes of the same version are
// byte-identical, and the post-lock version check makes a duplicate publish
// a no-op. The worst case of a broken lock is duplicate work, never lost or
// duplicated bytes.
func (p *Publisher) acquireLock() (func(), error) {
	lockDir := p.join(lockName)
	noncePath := p.join(lockName, lockNonceFile)
	deadline := time.Now().Add(lockWaitBound)
	brokeStale := false
	for {
		info, err := p.fs.Lstat(lockDir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			mkErr := p.fs.Mkdir(lockDir, 0o700)
			if mkErr == nil {
				// We hold it: write the identifying nonce.
				if werr := p.writeFile(noncePath, []byte(newNonce()), 0o600); werr != nil {
					_ = p.fs.Remove(noncePath)
					_ = p.fs.Remove(lockDir) // never held: give it back
					return nil, werr
				}
				p.log.Debug("shellintegration: lock acquired")
				return p.releaseLock(lockDir, noncePath), nil
			}
			if !errors.Is(mkErr, fs.ErrExist) {
				return nil, boundaryErr("lock", lockDir, mkErr)
			}
			// Someone else won the mkdir race: fall through and wait.
		case err != nil:
			return nil, boundaryErr("lstat", lockDir, err)
		default:
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, &SymlinkError{Path: lockDir}
			}
			if !info.IsDir() {
				return nil, &PublishError{Op: "lock", Path: lockDir, Err: fmt.Errorf("exists and is not a directory")}
			}
		}

		if time.Now().After(deadline) {
			if brokeStale {
				return nil, &PublishError{Op: "lock", Path: lockDir, Err: fmt.Errorf("still contended after breaking a stale lock")}
			}
			// Stale rule: see the comment above the function.
			_ = p.fs.Remove(noncePath)
			if err := p.fs.Remove(lockDir); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, boundaryErr("lock", lockDir, err)
			}
			brokeStale = true
			deadline = time.Now().Add(lockWaitBound)
			continue // retry acquisition immediately with a fresh bound
		}
		time.Sleep(lockPollInterval)
	}
}

// releaseLock returns a best-effort release: another publisher may already
// have broken the lock as stale, so absence is tolerated silently.
func (p *Publisher) releaseLock(lockDir, noncePath string) func() {
	return func() {
		if err := p.fs.Remove(noncePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			p.log.Debug("shellintegration: lock nonce removal failed", "err", err)
		}
		if err := p.fs.Remove(lockDir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			p.log.Debug("shellintegration: lock release failed", "err", err)
		}
	}
}

// checkInstalled applies the post-lock version check. It reports skip=true
// when nothing must be published: the installed manifest is newer (same
// protocol) — never downgraded; equality is not the comparison — or runs a
// protocol we do not understand, in which case it is left strictly alone.
// An invalid manifest names nothing, so nothing is active and we publish
// over it. A symlinked manifest is refused, never overwritten.
func (p *Publisher) checkInstalled(bundle Bundle) (PublishResult, bool, error) {
	m, err := p.readManifest()
	if err != nil {
		var invalid *InvalidManifestError
		if errors.As(err, &invalid) {
			p.log.Warn("shellintegration: existing manifest is invalid; publishing over it", "reason", invalid.Reason)
			return PublishResult{}, false, nil
		}
		return PublishResult{}, false, err
	}
	if m == nil {
		return PublishResult{}, false, nil
	}
	if m.Protocol > bundle.Protocol {
		return PublishResult{Generation: m.Generation, Version: m.Version, Reason: "incompatible-protocol"}, true, nil
	}
	if m.Protocol == bundle.Protocol && compareVersions(m.Version, bundle.Version) >= 0 {
		reason := "already-installed"
		if compareVersions(m.Version, bundle.Version) > 0 {
			reason = "newer-installed"
		}
		return PublishResult{Generation: m.Generation, Version: m.Version, Reason: reason}, true, nil
	}
	return PublishResult{}, false, nil
}

// readManifest returns the committed manifest, nil when absent. A symlink
// at manifest.json is an error (refuse, never write through it).
func (p *Publisher) readManifest() (*Manifest, error) {
	path := p.join(manifestName)
	info, err := p.fs.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, boundaryErr("lstat", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, &SymlinkError{Path: path}
	}
	data, err := p.fs.ReadFile(path)
	if err != nil {
		return nil, boundaryErr("read", path, err)
	}
	m, err := parseManifest(data)
	if err != nil {
		return nil, &InvalidManifestError{Path: path, Reason: err.Error()}
	}
	return m, nil
}

// ensureLaunch installs the launch carrier when absent; an existing launch
// is checked for symlinks but never rewritten.
func (p *Publisher) ensureLaunch(f BundleFile) error {
	path := p.join(launchName)
	info, err := p.fs.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return p.writeFile(path, f.Data, 0o700)
	case err != nil:
		return boundaryErr("lstat", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return &SymlinkError{Path: path}
	}
	if !info.Mode().IsRegular() {
		return &PublishError{Op: "launch", Path: path, Err: fmt.Errorf("exists and is not a regular file")}
	}
	p.log.Debug("shellintegration: launch carrier present, not rewritten")
	return nil
}

// stageGeneration writes every generation file under a fresh tmp/<nonce>/
// directory, fsyncing each file, the staging directory and tmp itself.
// It returns the nonce naming the staging directory.
func (p *Publisher) stageGeneration(bundle Bundle) (string, error) {
	for range 5 {
		nonce := newNonce()
		staging := p.join(tmpName, nonce)
		info, err := p.fs.Lstat(staging)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// available
		case err != nil:
			return "", boundaryErr("lstat", staging, err)
		default:
			if info.Mode()&os.ModeSymlink != 0 {
				return "", &SymlinkError{Path: staging}
			}
			continue // collision; try another nonce
		}
		if err := p.mkdir(staging, 0o700); err != nil {
			return "", err
		}
		for _, f := range bundle.Files {
			if f.Name == launchName {
				continue
			}
			if err := p.writeFile(p.join(tmpName, nonce, f.Name), f.Data, f.Mode); err != nil {
				return "", err
			}
		}
		if err := p.syncDir(staging); err != nil {
			return "", err
		}
		if err := p.syncDir(p.join(tmpName)); err != nil {
			return "", err
		}
		return nonce, nil
	}
	return "", &PublishError{Op: "mkdir", Path: p.join(tmpName), Err: fmt.Errorf("could not allocate a staging directory")}
}

// commitGeneration renames the staged directory into
// integration/v<version>/. A directory already present under that version
// is uncommitted garbage from an interrupted publish (the manifest does not
// name it, or the version check would have skipped us): it is removed first
// so the rename can succeed.
func (p *Publisher) commitGeneration(bundle Bundle, nonce string) error {
	gen := p.join(integrationDir, genDir(bundle.Version))
	info, err := p.fs.Lstat(gen)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return boundaryErr("lstat", gen, err)
	default:
		if info.Mode()&os.ModeSymlink != 0 {
			return &SymlinkError{Path: gen}
		}
		if err := p.removeTree(gen); err != nil {
			return err
		}
	}
	if err := p.fs.Rename(p.join(tmpName, nonce), gen); err != nil {
		return boundaryErr("rename", gen, err)
	}
	return p.syncDir(p.join(integrationDir))
}

// commitManifest writes the manifest to a unique temp file, fsyncs it and
// its directory, then renames it into place — last, after every file it
// names exists and is fsynced — and fsyncs the root after the rename
// (design §4: the manifest rename happens last; the manifest's directory is
// fsynced after it).
func (p *Publisher) commitManifest(bundle Bundle, nonce string) error {
	data, err := json.MarshalIndent(buildManifest(bundle), "", "  ")
	if err != nil {
		return fmt.Errorf("shellintegration: publisher: marshal manifest: %w", err)
	}
	data = append(data, '\n')

	tmp := p.join(tmpName, "manifest-"+nonce+".tmp")
	if werr := p.writeFile(tmp, data, 0o600); werr != nil {
		return werr
	}
	// From here every failure leaves this temp behind, and cleanupOrphans —
	// the sweep that bounds tmp/ — runs only on the success path in Publish.
	// The nonce is fresh per attempt, so a destination that refuses the
	// rename on every connection (an SFTP server without
	// posix-rename@openssh.com, whose refusal is deliberate: see
	// install_remote.go) would accumulate one dead manifest per connect,
	// forever. Sweeping the attempt's own temp is what makes a repeated
	// failure converge instead of grow.
	committed := false
	defer func() {
		if committed {
			return
		}
		if rerr := p.fs.Remove(tmp); rerr != nil {
			p.log.Debug("shellintegration: could not remove manifest temp after a failed commit",
				"path", tmp, "err", rerr)
		}
	}()

	if serr := p.syncDir(p.join(tmpName)); serr != nil {
		return serr
	}

	// Refuse to replace a manifest that is (or became) a symlink.
	manifestPath := p.join(manifestName)
	info, err := p.fs.Lstat(manifestPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return boundaryErr("lstat", manifestPath, err)
	default:
		if info.Mode()&os.ModeSymlink != 0 {
			return &SymlinkError{Path: manifestPath}
		}
	}

	if err := p.fs.Rename(tmp, manifestPath); err != nil {
		return boundaryErr("rename", manifestPath, err)
	}
	// The temp no longer exists under its own name — the rename consumed it,
	// and a Remove here would delete the manifest's predecessor path on a
	// carrier where rename is not atomic.
	committed = true
	if err := p.syncDir(p.root); err != nil {
		return boundaryErr("fsync", p.root, err)
	}
	return nil
}

// cleanupOrphans bounds the footprint: at most two generations (the active
// one and the newest other) and no tmp/ leftovers survive a publish.
// Failures are tolerated and logged: the manifest is already committed at
// this point, and the next publish retries the cleanup under the lock.
func (p *Publisher) cleanupOrphans(activeGen string) {
	// tmp/: everything here is a leftover — our staging dir and manifest
	// temp were renamed away — so every entry is an orphan.
	if entries, err := p.fs.ReadDir(p.join(tmpName)); err == nil {
		for _, e := range entries {
			child := p.join(tmpName, e.Name())
			if rerr := p.removeTree(child); rerr != nil {
				p.log.Warn("shellintegration: orphan cleanup failed", "path", child, "err", rerr)
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		p.log.Warn("shellintegration: orphan cleanup: cannot read tmp", "err", err)
	}

	// integration/: keep the active generation and the newest other.
	entries, err := p.fs.ReadDir(p.join(integrationDir))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			p.log.Warn("shellintegration: orphan cleanup: cannot read integration", "err", err)
		}
		return
	}
	keep := map[string]bool{activeGen: true}
	best := ""
	for _, e := range entries {
		if e.Name() == activeGen {
			continue
		}
		if best == "" || compareVersions(strings.TrimPrefix(e.Name(), genPrefix), strings.TrimPrefix(best, genPrefix)) > 0 {
			best = e.Name()
		}
	}
	if best != "" {
		keep[best] = true
	}
	for _, e := range entries {
		if keep[e.Name()] {
			continue
		}
		child := p.join(integrationDir, e.Name())
		if err := p.removeTree(child); err != nil {
			p.log.Warn("shellintegration: orphan generation cleanup failed", "path", child, "err", err)
		}
	}
}

// verifyFile reports whether path exists as a regular file with the
// recorded mode and hash. A symlink is an error, not merely "not
// installed": the manifest entry is invalid.
func (p *Publisher) verifyFile(path string, mf ManifestFile) (bool, error) {
	info, err := p.fs.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, boundaryErr("lstat", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, &SymlinkError{Path: path}
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	if info.Mode().Perm() != mustParseMode(mf.Mode) {
		return false, nil
	}
	data, err := p.fs.ReadFile(path)
	if err != nil {
		return false, boundaryErr("read", path, err)
	}
	if hashBytes(data) != mf.Hash {
		return false, nil
	}
	return true, nil
}

// writeFile writes data to path with mode as separate boundaries: lstat
// (symlink refusal), create, write, fsync, close. Every step is a
// fault-injectable point.
func (p *Publisher) writeFile(path string, data []byte, mode os.FileMode) error {
	info, err := p.fs.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return boundaryErr("lstat", path, err)
	default:
		if info.Mode()&os.ModeSymlink != 0 {
			return &SymlinkError{Path: path}
		}
	}
	f, err := p.fs.Create(path, mode)
	if err != nil {
		return boundaryErr("create", path, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return &PublishError{Op: "write", Path: path, Err: err}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return &PublishError{Op: "fsync", Path: path, Err: err}
	}
	if err := f.Close(); err != nil {
		return &PublishError{Op: "close", Path: path, Err: err}
	}
	return nil
}

func (p *Publisher) syncDir(path string) error {
	if err := p.fs.SyncDir(path); err != nil {
		return boundaryErr("fsync", path, err)
	}
	return nil
}

// removeTree removes a directory and its contents without ever following a
// symlink: a symlink entry is removed as the link itself, never traversed.
func (p *Publisher) removeTree(dir string) error {
	info, err := p.fs.Lstat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return boundaryErr("lstat", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		if rerr := p.fs.Remove(dir); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			return boundaryErr("remove", dir, rerr)
		}
		return nil
	}
	entries, err := p.fs.ReadDir(dir)
	if err != nil {
		return boundaryErr("readdir", dir, err)
	}
	for _, e := range entries {
		if err := p.removeTree(p.join(dir, e.Name())); err != nil {
			return err
		}
	}
	if err := p.fs.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return boundaryErr("remove", dir, err)
	}
	return nil
}

// buildManifest derives the manifest from a bundle.
func buildManifest(b Bundle) *Manifest {
	m := &Manifest{
		Protocol:   b.Protocol,
		Version:    b.Version,
		Generation: genDir(b.Version),
		Files:      map[string]ManifestFile{},
	}
	for _, f := range b.Files {
		if f.Name == launchName {
			continue
		}
		m.Files[f.Name] = ManifestFile{
			Hash: hashBytes(f.Data),
			Mode: fmt.Sprintf("%04o", f.Mode.Perm()),
			Size: int64(len(f.Data)),
		}
	}
	return m
}

// validateBundle rejects descriptors that could not be published safely:
// the version must be a safe name (it names the generation directory and
// the passport field), every filename must be a fixed base name, modes must
// be exactly 0600 for data and 0700 for the launch carrier, and the carrier
// must not be empty.
func validateBundle(b Bundle) error {
	if b.Protocol != ProtocolVersion {
		return fmt.Errorf("protocol %d is not supported (want %d)", b.Protocol, ProtocolVersion)
	}
	if !isSafeName(b.Version) {
		return fmt.Errorf("version %q is not a safe generation name", b.Version)
	}
	if len(b.Files) == 0 {
		return fmt.Errorf("bundle has no files")
	}
	seen := map[string]bool{}
	gens := 0
	for _, f := range b.Files {
		if f.Name == launchName {
			if f.Mode.Perm() != 0o700 {
				return fmt.Errorf("launch carrier mode must be 0700, got %04o", f.Mode.Perm())
			}
			if len(f.Data) == 0 {
				return fmt.Errorf("launch carrier is empty")
			}
		} else {
			if !isSafeName(f.Name) {
				return fmt.Errorf("file name %q is not a fixed base name", f.Name)
			}
			if f.Mode.Perm() != 0o600 {
				return fmt.Errorf("generation file %s mode must be 0600, got %04o", f.Name, f.Mode.Perm())
			}
			gens++
		}
		if seen[f.Name] {
			return fmt.Errorf("duplicate file %q", f.Name)
		}
		seen[f.Name] = true
	}
	if gens == 0 {
		return fmt.Errorf("bundle has no generation files (only the launch carrier)")
	}
	return nil
}

func genDir(version string) string { return genPrefix + version }

func mustParseMode(s string) os.FileMode {
	m, err := parseModeStr(s)
	if err != nil {
		panic("shellintegration: manifest mode failed validation: " + s)
	}
	return m
}

// newNonce returns a fresh random hex string naming a staging directory and
// identifying a lock holder. Entropy failure is unrecoverable in practice;
// a fixed fallback would make the lock forgeable.
func newNonce() string {
	id, ok := newSessionID()
	if !ok {
		panic("shellintegration: no entropy for publish nonce")
	}
	return id
}
