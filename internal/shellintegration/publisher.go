package shellintegration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
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

// Bounded remote work (design §7). Every number here is a bound this
// package ENFORCES; each was measured before it was written down, and
// REPORT-p3-measure.md holds the trace each was read off.
const (
	// maxPublishFSOps is N: the ceiling on FS-seam calls one publish
	// attempt issues for the shipped bundle (F = 3 generation files).
	//
	// N is defined AT THE FS SEAM — one call on the FS interface, plus one
	// each for Write, Sync and Close on the File a Create returns, because
	// publish_fs.go makes those three separate boundaries and a carrier
	// pays for each separately. It is NOT a syscall count (osFS.Mkdir is
	// mkdir+chmod, Create is open+chmod, SyncDir is open+fsync+close, so
	// the same attempt is ~101 local syscalls) and it is NOT an SFTP packet
	// count: extension negotiation, a SETSTAT per mode and WRITE splitting
	// make the carrier's packet count a separate, transport-specific
	// quantity.
	//
	// It is the exact measured maximum, not a round ceiling with slack:
	// slack is satisfied by any measurement below it, which is the
	// unfalsifiable acceptance criterion the repo's testing rules warn
	// about. The measured decomposition:
	//
	//	83  worst attempt inside the residue bounds below
	//	     = 63 residue-free worst (replacement + sweep + carrier reinstall)
	//	     +  9 removing one uncommitted generation at the target version
	//	     +  9 clearing one staging slot of three files
	//	     +  2 clearing that slot's manifest temp
	//	+ 5 lock probes at K = lockProbes
	//	+ 2 the stale break
	//	= 90
	//
	// publishFSOpBudget below is the general form, so a fourth generation
	// script raises the number to 101 loudly rather than being absorbed.
	maxPublishFSOps = 90

	// maxPublishBytes is B: the payload ceiling per publish. The shipped
	// bundle is 64,130 bytes after comment stripping, and a first-contact
	// publish issues 64,710 — bundle, a 548-byte manifest and a 32-byte lock
	// nonce — so B is 3.95x the measured maximum. B counts bytes WRITTEN; a
	// publish also reads (the manifest, and a Verify reads back the whole
	// active generation), so a verify-then-publish attempt moves ~120 KiB in
	// total and still fits B at 2.1x.
	//
	// The bundle was 56,916 when the ceiling was set: merging the carrier
	// with stage-1 grew the launch carrier by 7,214 bytes, because it now
	// emits the terminal bootstrap outcome and reads the capability from the
	// inherited descriptor. The measured maximum moved and the ratchet in
	// publisher_measure_test.go reported it; the call count did not move, so
	// maxPublishFSOps above is untouched.
	maxPublishBytes = 256 << 10

	// publishDeadline is T, the publish wall-clock. Expiring means: no new
	// remote operation is initiated, the attempt fails, and the shell still
	// starts. It is deliberately NOT a promise to destroy kernel I/O in an
	// uninterruptible state — a call already inside the carrier runs to its
	// own completion; closing the channel is the carrier's half.
	publishDeadline = 10 * time.Second

	// lockProbes is K: the number of times a contended lock is probed
	// before the stale rule applies. One probe is one Lstat, so a waiter
	// costs 5 FS calls where the retired 25 ms poll loop cost one per poll
	// — 200 for the common stale break that then acquires, and 400 for a
	// waiter that is re-contended and publishes nothing.
	lockProbes = 5

	// lockProbeBudget is the total wait those probes may add to T. It is
	// the sum of lockProbeSchedule; a test holds the two together.
	lockProbeBudget = 1550 * time.Millisecond

	// maxStagingSlots is bound 1 stated as a number: one attempt creates at
	// most one staging slot, and creates it only after the previous one is
	// gone.
	maxStagingSlots = 1

	// maxStagingSlotEntries is bound 1: one staging slot per destination.
	// tmp/ legitimately holds at most one staging directory and one
	// manifest temp, and both are cleared before a new slot is created.
	// Residue beyond that is left for the next attempt rather than
	// refused: tmp/ is also the sh publisher's staging area (see
	// launcher_publish.go), so a host can hold residue this process never
	// wrote, and refusing outright would strand it forever.
	maxStagingSlotEntries = 2

	// maxUncommittedPerAttempt is bound 2: at most one uncommitted
	// generation — the one at the target version — is removed per attempt.
	// More than one means residue is accumulating rather than converging,
	// and the attempt refuses instead of clearing it.
	maxUncommittedPerAttempt = 1

	// maxSweptPerAttempt is bound 3: at most one stale generation is swept
	// per attempt. The keep-two policy implies exactly this in steady
	// state — one generation falls out of the window per publish — and did
	// not enforce it: a directory holding nine generations swept seven in
	// one attempt.
	maxSweptPerAttempt = 1

	// maxResidueDepth and maxResidueEntries are bound 4: a generation or
	// staging directory is FLAT by construction — a directory of regular
	// files — so a tree deeper or wider than the layout can legitimately
	// produce is refused rather than traversed. Eight is wider than any
	// bundle we ship (three generation files) and narrow enough that the
	// refusal is reached before the traversal is worth doing.
	maxResidueDepth   = 1
	maxResidueEntries = 8

	// maxResidueInspected and maxResidueRemoved bound the two quantities
	// §7 asks to be bounded SEPARATELY. They are numerically equal and
	// conceptually distinct: a refused removal inspects without removing,
	// so the counters diverge exactly where the bound bites. The value is
	// the structural worst case — one staging slot, one uncommitted
	// generation and one swept generation, each of at most
	// maxResidueEntries files, plus one manifest temp.
	maxResidueInspected = 3*(maxResidueEntries+1) + 1
	maxResidueRemoved   = maxResidueInspected
)

// Named outcomes. §11 assertion 33 asks contention to produce a NAMED
// outcome rather than a session left in `starting`; these are the names,
// and they travel in PublishResult.Reason so a carrier can render them.
const (
	// ReasonContended: another publisher holds the destination, nothing
	// was written, and no verified generation exists to fall back on.
	ReasonContended = "publish-contended"
	// ReasonContendedExisting: contended, but a committed generation
	// verifies, so the session integrates on that one instead.
	ReasonContendedExisting = "publish-contended-existing-generation"
	// ReasonResidue: previous residue could not be cleared, so nothing was
	// written rather than a second staging slot being opened.
	ReasonResidue = "publish-residue"
	// ReasonTimeout: T expired; no further remote operation was initiated.
	ReasonTimeout = "publish-timeout"
)

// lockProbeSchedule is K probes at 50/100/200/400/800 ms — 1.55 s of
// waiting before the stale rule applies, against the 5 s bound and 25 ms
// cadence it replaces.
var lockProbeSchedule = [lockProbes]time.Duration{
	50 * time.Millisecond,
	100 * time.Millisecond,
	200 * time.Millisecond,
	400 * time.Millisecond,
	800 * time.Millisecond,
}

// publishFSOpBudget evaluates N for a bundle of f generation files. The
// formula was read off the measured trace, term by term:
//
//	N = 29 + b + m + l + 5·F + G + Σᵢ(3 + 2·kᵢ) + P
//
//	29        fixed skeleton: prepareRoot 2, acquireLock 7, staging 4,
//	          commitGeneration 3, commitManifest 9, cleanup readdirs 2,
//	          release 2
//	b         ensureBaseDirs: 6 when tmp/ and integration/ are absent, else 2
//	m         checkInstalled: 1 when no manifest, 2 when one is read
//	l         ensureLaunch: 1 when the carrier is present, 6 when written
//	5·F       per generation file: lstat, create, write, sync, close
//	G         removing the uncommitted generation at the target version
//	Σᵢ        one term per removed entry: a directory of k files costs
//	          3 + 2·k (lstat + readdir + k×(lstat+remove) + remove)
//	P         lock probes, plus 2 removes for the stale break
//
// evaluated at the worst attempt the five bounds still permit.
func publishFSOpBudget(f int) int {
	const (
		skeleton  = 29
		baseDirs  = 2 // tmp/ and integration/ already exist
		manifest  = 2 // an installed manifest is read
		carrier   = 6 // the launch carrier is absent and written
		tempEntry = 2 // clearing one manifest temp: lstat + remove
	)
	tree := 3 + 2*f // one flat directory of f files
	return skeleton + baseDirs + manifest + carrier + 5*f +
		tree + // the uncommitted generation at the target version
		tree + // the staging slot cleared before a new one is created
		tempEntry +
		tree + // one stale generation swept
		lockProbes + 2
}

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

// ContendedError reports that another publisher holds the destination and
// this attempt gave up without writing anything. It is the named outcome of
// §11 assertion 33: a session that cannot publish because of contention
// says so, rather than sitting in `starting` until a deadline elsewhere
// gives up on it.
type ContendedError struct {
	Path   string
	Reason string
}

func (e *ContendedError) Error() string {
	return fmt.Sprintf("shellintegration: publisher: %s: %s (%s)", e.Path, e.Reason, ReasonContended)
}

// ResidueError reports a refusal to write because previous residue could
// not be cleared, or because clearing it would cost more work than the
// layout can legitimately produce (design §7: one staging slot per
// destination, and no unbounded directory traversal). Refusing is the
// point: opening a second staging slot beside residue we cannot remove is
// how a bounded attempt becomes an unbounded total.
type ResidueError struct {
	Path   string
	Reason string
	Err    error
}

func (e *ResidueError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("shellintegration: publisher: %s: %s: %v (%s)", e.Path, e.Reason, e.Err, ReasonResidue)
	}
	return fmt.Sprintf("shellintegration: publisher: %s: %s (%s)", e.Path, e.Reason, ReasonResidue)
}

func (e *ResidueError) Unwrap() error { return e.Err }

// DeadlineError reports T expiring: the attempt initiated no further remote
// operation. The operation it names is the one that was refused, not one
// that was abandoned half-way — an operation already inside the carrier
// runs to its own completion, and closing the channel is the carrier's half
// of the contract (design §7).
type DeadlineError struct {
	Op     string
	Path   string
	Budget time.Duration
}

func (e *DeadlineError) Error() string {
	return fmt.Sprintf("shellintegration: publisher: publish deadline of %s expired before %s %s; no further remote operation was initiated (%s)",
		e.Budget, e.Op, e.Path, ReasonTimeout)
}

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

	// now and after are the clock seam. Every deadline in this package is
	// driven through them, so a test injects time instead of waiting for
	// it (design §11: no assertion may be satisfied by waiting on a
	// duration).
	now   func() time.Time
	after func(time.Duration) <-chan time.Time

	// at is the current attempt's budget: nil on the Publisher a caller
	// holds, non-nil on the clone attempt() returns. Every exported entry
	// point runs on such a clone, which is what makes "per attempt" a
	// property of the type rather than a convention.
	at *attemptState
}

// NewPublisher returns a Publisher writing under root (the remote ~/.nocx)
// through fsys.
func NewPublisher(logger log.Logger, fsys FS, root string) *Publisher {
	return &Publisher{log: logger, fs: fsys, root: root, now: time.Now, after: time.After}
}

// attemptState is one attempt's budget: the wall-clock deadline T, the
// FS-seam operation count N is measured against, and the residue counters
// that make the five bounds of §7 enforceable rather than lucky.
type attemptState struct {
	now      func() time.Time
	deadline time.Time

	ops       int // FS-seam calls issued; N is the ceiling
	inspected int // residue entries lstat'd
	removed   int // residue entries removed
	slots     int // staging slots created
	uncommit  int // uncommitted generations removed
	swept     int // stale generations swept
	probes    int // lock probes issued
	probeWait time.Duration
}

// begin admits one FS-seam operation, or refuses it because T expired. Past
// the deadline no NEW remote operation is initiated; an operation already
// in flight is not destroyed, and this is deliberately all the publisher
// promises (design §7).
func (a *attemptState) begin(op, path string) error {
	if a.now().After(a.deadline) {
		return &DeadlineError{Op: op, Path: path, Budget: publishDeadline}
	}
	a.ops++
	return nil
}

// attempt returns a Publisher clone bound to one attempt's budget: same
// log, same root, same seam, with every FS call routed through budgetFS so
// the deadline is enforced in ONE place rather than at each call site.
func (p *Publisher) attempt() *Publisher {
	now, after := p.now, p.after
	if now == nil {
		now = time.Now
	}
	if after == nil {
		after = time.After
	}
	at := &attemptState{now: now, deadline: now().Add(publishDeadline)}
	clone := *p
	clone.now, clone.after, clone.at = now, after, at
	clone.fs = budgetFS{FS: p.fs, at: at}
	return &clone
}

// budgetFS is the single owner of "no remote operation after T" and of the
// per-attempt operation count. Wrapping the seam is what makes both true of
// every call rather than of the calls somebody remembered to guard.
type budgetFS struct {
	FS
	at *attemptState
}

func (b budgetFS) Lstat(path string) (fs.FileInfo, error) {
	if err := b.at.begin("lstat", path); err != nil {
		return nil, err
	}
	return b.FS.Lstat(path)
}

func (b budgetFS) Mkdir(path string, mode os.FileMode) error {
	if err := b.at.begin("mkdir", path); err != nil {
		return err
	}
	return b.FS.Mkdir(path, mode)
}

func (b budgetFS) Create(path string, mode os.FileMode) (File, error) {
	if err := b.at.begin("create", path); err != nil {
		return nil, err
	}
	f, err := b.FS.Create(path, mode)
	if err != nil {
		return nil, err
	}
	return budgetFile{File: f, at: b.at, path: path}, nil
}

func (b budgetFS) SyncDir(path string) error {
	if err := b.at.begin("syncdir", path); err != nil {
		return err
	}
	return b.FS.SyncDir(path)
}

func (b budgetFS) Rename(src, dst string) error {
	if err := b.at.begin("rename", src); err != nil {
		return err
	}
	return b.FS.Rename(src, dst)
}

func (b budgetFS) Remove(path string) error {
	if err := b.at.begin("remove", path); err != nil {
		return err
	}
	return b.FS.Remove(path)
}

func (b budgetFS) ReadDir(path string) ([]fs.FileInfo, error) {
	if err := b.at.begin("readdir", path); err != nil {
		return nil, err
	}
	return b.FS.ReadDir(path)
}

func (b budgetFS) ReadFile(path string) ([]byte, error) {
	if err := b.at.begin("readfile", path); err != nil {
		return nil, err
	}
	return b.FS.ReadFile(path)
}

// budgetFile carries the budget across the write boundary: Write, Sync and
// Close are three separate remote operations, and T applies to each.
type budgetFile struct {
	File
	at   *attemptState
	path string
}

func (f budgetFile) Write(p []byte) (int, error) {
	if err := f.at.begin("write", f.path); err != nil {
		return 0, err
	}
	return f.File.Write(p)
}

func (f budgetFile) Sync() error {
	if err := f.at.begin("fsync", f.path); err != nil {
		return err
	}
	return f.File.Sync()
}

func (f budgetFile) Close() error {
	if err := f.at.begin("close", f.path); err != nil {
		return err
	}
	return f.File.Close()
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
// and is fsynced — all under the lock, and within the bounds of design §7:
// one staging slot, one uncommitted generation, one swept generation, no
// unbounded traversal, K lock probes and T of wall clock.
//
// Fail-open contract: any error leaves the previous activation untouched
// and the next attempt converges with no manual cleanup.
func (p *Publisher) Publish(bundle Bundle) (res PublishResult, err error) {
	if verr := validateBundle(bundle); verr != nil {
		// Nothing exists yet: the wrapped error is all the caller needs.
		return PublishResult{}, fmt.Errorf("shellintegration: publisher: invalid bundle: %w", verr)
	}
	// Local singleflight per resolved destination and content digest: a
	// hundred concurrent calls for one destination become ONE remote
	// publish, and a joined waiter issues no remote operation at all
	// (§11.29, §11.31). Singleflight is per process and is not the
	// boundary — a saved session and a typed session, or a second instance
	// of the application, race across processes, and the remote lock is
	// the only authoritative arbiter (§6.3).
	flight, leader := joinPublishFlight(p.fs, p.root, bundle)
	if !leader {
		return p.awaitPublishFlight(flight)
	}
	// The flight is finished from a defer, including when the attempt
	// panics: newNonce panics when the machine has no entropy, and a
	// leader that died without publishing its result would leave every
	// waiter for that destination blocked until T — turning one failure
	// into as many stalled sessions as happened to be joined.
	defer func() {
		if r := recover(); r != nil {
			flight.finish(PublishResult{}, fmt.Errorf("shellintegration: publisher: publish panicked: %v", r))
			panic(r)
		}
		if joined := flight.finish(res, err); joined > 0 {
			p.log.Debug("shellintegration: publish joined local waiters", "waiters", joined, "root", p.root)
		}
	}()
	res, err = p.attempt().publish(bundle)
	return res, err
}

// publish is one attempt, on the clone attempt() returned.
func (p *Publisher) publish(bundle Bundle) (res PublishResult, err error) {
	defer p.reportAttempt(bundle)
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
		return p.contendedFallback(err)
	}
	defer release()

	// The version check is repeated after the lock is held (design §4 and
	// §6.3): the lock is released between attempts, so holding it is not
	// by itself a guarantee of a single commit — between any earlier read
	// and this acquisition another publisher may have committed the
	// version we are about to write. This check is what makes at most one
	// commit happen per content digest.
	if installed, skip, checkErr := p.checkInstalled(bundle); checkErr != nil {
		return PublishResult{}, checkErr
	} else if skip {
		p.log.Info("shellintegration: publish skipped", "version", bundle.Version, "reason", installed.Reason)
		// Nothing will be staged, so the staging slot is cleared here
		// instead — best-effort, because there is nothing to refuse: an
		// attempt that writes nothing cannot open a second slot.
		if serr := p.clearStagingSlot(); serr != nil {
			p.log.Warn("shellintegration: staging residue not cleared", "err", serr)
		}
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

// contendedFallback turns a lost lock into a NAMED outcome. The loser of a
// contended publish cannot simply proceed: with nothing ever committed the
// far side would find no generation and nobody would move the session out
// of `starting` (design §6.3). So a verified committed generation is
// reported and the session integrates on that one; with nothing committed
// the failure is named publish-contended and the caller degrades knowing
// why. Any other error is passed through untouched.
func (p *Publisher) contendedFallback(err error) (PublishResult, error) {
	var ce *ContendedError
	if !errors.As(err, &ce) {
		return PublishResult{}, err
	}
	vr, verr := p.verify()
	if verr == nil && vr.Installed {
		p.log.Info("shellintegration: publish contended; the committed generation is used instead",
			"generation", vr.Generation, "version", vr.Version, "reason", ReasonContendedExisting)
		return PublishResult{Generation: vr.Generation, Version: vr.Version, Reason: ReasonContendedExisting}, nil
	}
	p.log.Warn("shellintegration: publish contended and nothing is committed",
		"root", p.root, "reason", ReasonContended)
	return PublishResult{Reason: ReasonContended}, err
}

// reportAttempt makes the attempt's cost visible in the product, not only
// in a test file. N is a ratchet the tests hold (§11.30), never a runtime
// abort — refusing a legitimate attempt at the ceiling would turn a
// measurement into an outage — but an attempt that outgrows its budget is a
// fact somebody reading the logs must be able to see.
func (p *Publisher) reportAttempt(bundle Bundle) {
	budget := publishFSOpBudget(generationFileCount(bundle))
	if p.at.ops > budget {
		p.log.Warn("shellintegration: publish exceeded its bounded remote work",
			"ops", p.at.ops, "budget", budget, "inspected", p.at.inspected,
			"removed", p.at.removed, "probes", p.at.probes)
		return
	}
	p.log.Debug("shellintegration: publish attempt cost",
		"ops", p.at.ops, "budget", budget, "inspected", p.at.inspected,
		"removed", p.at.removed, "probes", p.at.probes, "probeWait", p.at.probeWait)
}

// generationFileCount is F: the bundle's generation files, which is every
// file except the launch carrier.
func generationFileCount(b Bundle) int {
	n := 0
	for _, f := range b.Files {
		if f.Name != launchName {
			n++
		}
	}
	return n
}

// publishFlight is one in-flight publish. Waiters block on done and read
// the leader's result; they issue no FS call of their own.
type publishFlight struct {
	key  publishFlightKey
	done chan struct{}
	res  PublishResult
	err  error

	// waiters counts the callers joined to this flight; guarded by
	// publishFlights.mu. It is how many remote attempts singleflight did
	// not make, which is the whole point of it and belongs in the log.
	waiters int
}

// publishFlightKey identifies what may be joined: the seam VALUE, the root
// and the content digest. The root alone is not the destination — two hosts
// have the same ~/.nocx path, and joining them would report to one host a
// publish that only ever reached the other. The digest is part of the
// identity because §6.3 bounds commits per content digest: two different
// bundles must not be collapsed into one attempt.
type publishFlightKey struct {
	fs     FS
	root   string
	digest string
}

var publishFlights = struct {
	mu sync.Mutex
	m  map[publishFlightKey]*publishFlight
}{m: map[publishFlightKey]*publishFlight{}}

// joinPublishFlight registers this call as the leader for its destination,
// or returns the flight it must wait on. A seam whose dynamic type is not
// comparable cannot be a map key and therefore cannot be identified: such a
// caller leads its own attempt rather than being joined to somebody else's,
// which loses the optimisation and keeps the guarantee.
func joinPublishFlight(fsys FS, root string, bundle Bundle) (*publishFlight, bool) {
	if fsys == nil || !reflect.TypeOf(fsys).Comparable() {
		return nil, true
	}
	key := publishFlightKey{fs: fsys, root: root, digest: bundleDigest(bundle)}
	publishFlights.mu.Lock()
	defer publishFlights.mu.Unlock()
	if f, ok := publishFlights.m[key]; ok {
		f.waiters++
		return f, false
	}
	f := &publishFlight{key: key, done: make(chan struct{})}
	publishFlights.m[key] = f
	return f, true
}

// finish publishes the leader's result to its waiters and reports how many
// there were.
func (f *publishFlight) finish(res PublishResult, err error) int {
	if f == nil {
		return 0
	}
	publishFlights.mu.Lock()
	delete(publishFlights.m, f.key)
	waiters := f.waiters
	publishFlights.mu.Unlock()
	f.res, f.err = res, err
	close(f.done)
	return waiters
}

// awaitPublishFlight is the joined waiter. It performs NO remote operation:
// it either takes the leader's result or, if the leader outlives T,
// terminates as publish-contended — a named outcome rather than a session
// left in `starting`.
func (p *Publisher) awaitPublishFlight(f *publishFlight) (PublishResult, error) {
	after := p.after
	if after == nil {
		after = time.After
	}
	select {
	case <-f.done:
		return f.res, f.err
	case <-after(publishDeadline):
		return PublishResult{Reason: ReasonContended}, &ContendedError{
			Path:   p.root,
			Reason: "a local publish for this destination outlived the publish deadline",
		}
	}
}

// bundleDigest is the content identity of a bundle: the protocol, the
// version and every file's name, mode and content hash. Two calls with the
// same digest publish the same bytes, which is what makes joining them
// sound.
func bundleDigest(b Bundle) string {
	names := make([]string, 0, len(b.Files))
	byName := make(map[string]BundleFile, len(b.Files))
	for _, f := range b.Files {
		names = append(names, f.Name)
		byName[f.Name] = f
	}
	sort.Strings(names)
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d\x00%s", b.Protocol, b.Version)
	for _, n := range names {
		f := byName[n]
		fmt.Fprintf(&sb, "\x00%s\x00%04o\x00%s", n, f.Mode.Perm(), hashBytes(f.Data))
	}
	return hashBytes([]byte(sb.String()))
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
func (p *Publisher) Verify() (VerifyResult, error) { return p.attempt().verify() }

func (p *Publisher) verify() (VerifyResult, error) {
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
func (p *Publisher) Uninstall() (UninstallResult, error) { return p.attempt().uninstall() }

func (p *Publisher) uninstall() (UninstallResult, error) {
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

// acquireLock takes the lock with at most K = lockProbes probes on
// lockProbeSchedule — 50/100/200/400/800 ms, 1.55 s of waiting in total —
// then applies the stale rule once, and returns an idempotent release func.
//
// What this replaces. The retired loop polled every 25 ms for a 5 s bound
// and cost one FS call per poll: ~200 metadata operations for the common
// case (break a stale lock, then acquire) and ~400 for a waiter that is
// re-contended after the break and publishes nothing. Five probes and two
// removes cost what one of those polls did.
//
// Lock discipline. The lock is a directory created with mkdir(2) — atomic
// by construction — holding a single nonce file naming the holder's
// attempt. It is held for a short, bounded sequence of filesystem
// operations on one filesystem, and the wait is bounded statically: no
// remote sleep loop, and no remote work whose duration the remote host's
// state decides (design §7).
//
// Stale rule: after K probes the lock directory is removed and acquisition
// is attempted once more; still contended means a NAMED failure, not a
// second bound. The rule trusts neither a remote PID (there may be none —
// the publisher can run on a different machine than the filesystem it
// writes through, so a PID file would be meaningless and PIDs recycle) nor
// the remote wall clock (mtimes are coarse, skewed or settable); the only
// time reference is the waiter's own bounded wait. This is safe because
// breaking a lock can never corrupt state: the manifest rename is the only
// activation pointer, publishes of the same version are byte-identical, and
// the post-lock version check makes a duplicate publish a no-op. The worst
// case of a broken lock is duplicate work, never lost or duplicated bytes.
func (p *Publisher) acquireLock() (func(), error) {
	lockDir := p.join(lockName)
	noncePath := p.join(lockName, lockNonceFile)
	for probe := range lockProbes {
		p.at.probes++
		release, taken, err := p.tryLock(lockDir, noncePath)
		if err != nil {
			return nil, err
		}
		if taken {
			return release, nil
		}
		p.at.probeWait += lockProbeSchedule[probe]
		<-p.after(lockProbeSchedule[probe])
	}

	// The bound elapsed with the lock still held: break it once.
	_ = p.fs.Remove(noncePath)
	if err := p.fs.Remove(lockDir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, boundaryErr("lock", lockDir, err)
	}
	release, taken, err := p.tryLock(lockDir, noncePath)
	if err != nil {
		return nil, err
	}
	if taken {
		return release, nil
	}
	return nil, &ContendedError{Path: lockDir, Reason: "still contended after breaking a stale lock"}
}

// tryLock makes one acquisition attempt: one Lstat, and — when the lock is
// absent — the Mkdir that is the acquisition itself plus the nonce naming
// the holder. taken=false with a nil error means somebody else holds it.
func (p *Publisher) tryLock(lockDir, noncePath string) (func(), bool, error) {
	info, err := p.fs.Lstat(lockDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		mkErr := p.fs.Mkdir(lockDir, 0o700)
		if mkErr == nil {
			// We hold it: write the identifying nonce.
			if werr := p.writeFile(noncePath, []byte(newNonce()), 0o600); werr != nil {
				_ = p.fs.Remove(noncePath)
				_ = p.fs.Remove(lockDir) // never held: give it back
				return nil, false, werr
			}
			p.log.Debug("shellintegration: lock acquired")
			return p.releaseLock(lockDir, noncePath), true, nil
		}
		if !errors.Is(mkErr, fs.ErrExist) {
			return nil, false, boundaryErr("lock", lockDir, mkErr)
		}
		return nil, false, nil // someone else won the mkdir race
	case err != nil:
		return nil, false, boundaryErr("lstat", lockDir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, false, &SymlinkError{Path: lockDir}
	}
	if !info.IsDir() {
		return nil, false, &PublishError{Op: "lock", Path: lockDir, Err: fmt.Errorf("exists and is not a directory")}
	}
	return nil, false, nil
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
//
// Bound 1, one staging slot per destination: the previous slot is cleared
// BEFORE a new one is created, and residue that cannot be cleared refuses
// the write rather than opening a second slot beside it. Without this a
// failure before commit left a staging directory until some future
// successful publish swept it, and repeated failures left several — each
// attempt bounded, the total not.
//
// The name stays a fresh nonce rather than becoming a fixed one. A fixed
// slot name would let a publisher whose lock was broken as stale write into
// the directory another publisher is about to rename into place; a nonce
// keeps concurrent writers in separate directories and makes the rename the
// only publication event.
func (p *Publisher) stageGeneration(bundle Bundle) (string, error) {
	if err := p.clearStagingSlot(); err != nil {
		return "", err
	}
	if p.at.slots >= maxStagingSlots {
		return "", &ResidueError{
			Path:   p.join(tmpName),
			Reason: fmt.Sprintf("one attempt may create at most %d staging slot", maxStagingSlots),
		}
	}
	p.at.slots++
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

// clearStagingSlot enforces bound 1. It removes at most one staging slot's
// worth of residue — one staging directory and one manifest temp — so the
// work an attempt does here is bounded whatever tmp/ holds. Residue beyond
// that is left for the next attempt and reported rather than refused: tmp/
// is also the sh publisher's staging area (launcher_publish.go), so a host
// can carry residue this process never wrote, and refusing outright would
// strand it forever. What DOES refuse is residue that cannot be removed —
// then nothing is written, because a second slot beside an uncleanable
// first is exactly the unbounded total §7 forbids.
func (p *Publisher) clearStagingSlot() error {
	dir := p.join(tmpName)
	entries, err := p.fs.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return boundaryErr("readdir", dir, err)
	}
	// ReadDir order is the carrier's business (os.ReadDir sorts, SFTP need
	// not), and which entries a bounded clear takes must not depend on it.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for i, e := range entries {
		if i == maxStagingSlotEntries {
			p.log.Warn("shellintegration: staging residue beyond one slot left for the next attempt",
				"tmp", dir, "remaining", len(entries)-i)
			break
		}
		child := p.join(tmpName, e.Name())
		if rerr := p.removeTree(child); rerr != nil {
			return &ResidueError{Path: child, Reason: "previous staging residue could not be cleared", Err: rerr}
		}
	}
	return nil
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
		// Bound 2: the ONE uncommitted generation an attempt may remove is
		// the one at the target version. A second would mean residue is
		// accumulating rather than converging, and the attempt refuses
		// instead of clearing it.
		if p.at.uncommit >= maxUncommittedPerAttempt {
			return &ResidueError{
				Path:   gen,
				Reason: fmt.Sprintf("an attempt removes at most %d uncommitted generation", maxUncommittedPerAttempt),
			}
		}
		p.at.uncommit++
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
	// From here every failure leaves this temp behind, and the sweep that
	// bounds tmp/ — clearStagingSlot — runs at the START of the NEXT
	// attempt. The nonce is fresh per attempt, so a destination that
	// refuses the rename on every connection (an SFTP server without
	// posix-rename@openssh.com, whose refusal is deliberate: see
	// install_remote.go) would otherwise hold a dead manifest until then,
	// and two of them if the next attempt failed earlier still. Removing
	// the attempt's own temp here is what keeps its residue to one slot
	// rather than leaving the bound to be re-established later.
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

// cleanupOrphans bounds the generation footprint: the active generation and
// the newest other survive, and AT MOST ONE stale generation is swept per
// attempt (bound 3). The keep-two policy implies exactly that in steady
// state — one generation falls out of the window per publish — and did not
// enforce it: a directory holding nine generations swept seven in a single
// attempt, under the lock. Bounded this way a directory that somehow holds
// nine converges one per attempt instead, and every attempt including a
// no-op sweeps, so convergence does not wait for the next version bump.
//
// tmp/ is NOT swept here: clearStagingSlot owns that bound, and it runs
// before a new slot is created rather than after the manifest is committed.
// One owner per behaviour — a second sweep here would be a second answer to
// "how much staging residue may exist".
//
// Failures are tolerated and logged: the manifest is already committed at
// this point, and the next publish retries the cleanup under the lock.
func (p *Publisher) cleanupOrphans(activeGen string) {
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

	// The oldest removable generation goes first, so repeated attempts
	// converge in a defined order rather than in ReadDir order.
	var removable []string
	for _, e := range entries {
		if !keep[e.Name()] {
			removable = append(removable, e.Name())
		}
	}
	sort.Slice(removable, func(i, j int) bool {
		return compareVersions(strings.TrimPrefix(removable[i], genPrefix), strings.TrimPrefix(removable[j], genPrefix)) < 0
	})
	for i, name := range removable {
		if i == maxSweptPerAttempt {
			p.log.Warn("shellintegration: stale generations beyond this attempt's sweep left for the next one",
				"remaining", len(removable)-i)
			break
		}
		child := p.join(integrationDir, name)
		if err := p.removeTree(child); err != nil {
			p.log.Warn("shellintegration: orphan generation cleanup failed", "path", child, "err", err)
			break
		}
		p.at.swept++
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
//
// Bound 4: bounded in depth and in breadth. A generation or staging
// directory is FLAT by construction — a directory of regular files — so a
// tree deeper or wider than the layout can legitimately produce is REFUSED
// rather than traversed. Before this, a directory tree planted under tmp/
// or integration/ (by a previous protocol, by a user, by another program)
// was walked to whatever depth it had, on the publish path, under the lock.
//
// Inspected and removed entries are counted separately against the
// attempt's budget, as §7 asks. They are numerically equal in the good case
// and diverge exactly where the bound bites: a refusal inspects an entry
// and removes nothing.
func (p *Publisher) removeTree(dir string) error { return p.removeTreeAt(dir, maxResidueDepth) }

func (p *Publisher) removeTreeAt(dir string, depth int) error {
	if p.at.inspected >= maxResidueInspected {
		return &ResidueError{
			Path:   dir,
			Reason: fmt.Sprintf("an attempt inspects at most %d residue entries", maxResidueInspected),
		}
	}
	p.at.inspected++
	info, err := p.fs.Lstat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return boundaryErr("lstat", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return p.removeEntry(dir)
	}
	if depth <= 0 {
		return &ResidueError{
			Path:   dir,
			Reason: fmt.Sprintf("a directory nested deeper than the %d level the layout produces", maxResidueDepth),
		}
	}
	entries, err := p.fs.ReadDir(dir)
	if err != nil {
		return boundaryErr("readdir", dir, err)
	}
	if len(entries) > maxResidueEntries {
		return &ResidueError{
			Path:   dir,
			Reason: fmt.Sprintf("holds %d entries; the layout produces at most %d", len(entries), maxResidueEntries),
		}
	}
	for _, e := range entries {
		if err := p.removeTreeAt(p.join(dir, e.Name()), depth-1); err != nil {
			return err
		}
	}
	return p.removeEntry(dir)
}

// removeEntry removes one residue entry against the attempt's removal
// budget. Absence is tolerated: another writer converging on the same
// residue is not an error.
func (p *Publisher) removeEntry(path string) error {
	if p.at.removed >= maxResidueRemoved {
		return &ResidueError{
			Path:   path,
			Reason: fmt.Sprintf("an attempt removes at most %d residue entries", maxResidueRemoved),
		}
	}
	p.at.removed++
	if err := p.fs.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return boundaryErr("remove", path, err)
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
