package transport

// The files.* control plane (fm-w8): six JSON-RPC methods backed by
// internal/filesystem, plus the files.changed notification.
//
// Two guards, and they are the point of this file:
//
//  1. files.open is authorised by connState (D15) — a connection can open
//     a filesystem only for a session it has opened or reattached to. The
//     global session registry is never the answer: resolving a sessionId
//     through it would let any authenticated socket that learned another
//     connection's session id open that session's filesystem.
//  2. Every later call re-checks, in exactly one place: Registry.Acquire
//     re-checks that the binding's session is in the REQUESTING
//     connection's connState, and takes the use-guard that keeps the
//     binding alive for the call. A handler cannot forget a check it
//     never performs.
//
// The change notification's addressing is the interesting half: the
// destination is resolved at emit time — the binding's session's current
// subscriber (sessionRx.subscriber), never the connection that opened the
// binding, which is destroyed on a WebSocket drop. With no subscriber,
// changes accumulate as a set of dirty paths and are delivered once on
// re-attach (spec §5.2).
//
// Until the watching wave lands (design §6 step 5: fsnotify locally,
// polling over SFTP, both provider-side), the change signal is a
// transport-side digest-poll loop: files.watch installs the provider's
// watch set (whose mode the response reports, degrading honestly to
// polling when the provider refuses), and the loop re-lists each watched
// path, comparing the listing digest — the same comparison the SFTP
// watcher will perform. internal/filesystem is read-only for this wave and
// exposes no watch-event seam, so this loop is the only change signal the
// transport can produce; it is what the files.changed tests drive.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport/control"
)

// ── files.* ingress bounds and validators (the per-field sweep) ───────────
//
// Every files.* path is a provider path, and the provider owns path syntax:
// "paths absolute and cleaned by the provider's rules" (internal/filesystem,
// spec §5.2), enforced per call by each provider's private checkPath
// (ErrInvalidPath → -32602). The predicate below is the transport's copy of
// that documented contract — absolute, clean, bounded — moving the refusal
// before the handler while the provider stays the owner of the rule and
// still enforces it on every call. This is deliberately NOT the git
// pathspec rule (ws_git.go): a git.* path is repository-relative.
const (
	// maxWatchPaths bounds the watch set files.watch may carry. The set is
	// the expanded tree — one directory per expand gesture — and the handler
	// lists each path synchronously for the baseline (filesBaseline), so the
	// count is both a product ceiling and a per-call work bound.
	maxWatchPaths = 512

	// maxFileNameRunes bounds the save dialog's suggested file name: an OS
	// file name component is limited to 255 bytes, and the dialog owns the
	// final path.
	maxFileNameRunes = 255
)

// validateFSPath checks one files.* path against the provider path contract.
func validateFSPath(path, what string) string {
	if path == "" {
		return what + " is required"
	}
	if !filepath.IsAbs(path) {
		return what + " must be an absolute path"
	}
	if filepath.Clean(path) != path {
		return what + " must be a clean path"
	}
	if utf8.RuneCountInString(path) > maxPathRunes {
		return fmt.Sprintf("%s exceeds %d characters", what, maxPathRunes)
	}
	return ""
}

// validateFilesOpenRaw checks files.open: the session the requesting
// connection must own, and the optional D2 rootPath override.
func validateFilesOpenRaw(raw json.RawMessage) string {
	var p filesOpenParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if !isLowerHex(p.SessionID, 32) {
		return "sessionId is required and must be the 32-hex id the backend minted"
	}
	// rootPath is the optional D2 override (the verified OSC 7 cwd). The
	// provider interprets it by its own rules and FALLS BACK to Root() when
	// it is absent or unusable (spec §5.1), so only the wire-cost bound is
	// checked here: an unusable root is a designed fallback, never an error.
	if utf8.RuneCountInString(p.RootPath) > maxPathRunes {
		return fmt.Sprintf("rootPath exceeds %d characters", maxPathRunes)
	}
	return ""
}

// validateFilesListRaw checks files.list: the binding, the directory, and
// the page the provider itself rules on (ErrInvalidPage → -32602).
func validateFilesListRaw(raw json.RawMessage) string {
	var p filesListParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if !isLowerHex(p.BindingID, 32) {
		return "bindingId is required and must be the 32-hex id the backend minted"
	}
	if msg := validateFSPath(p.Path, "path"); msg != "" {
		return msg
	}
	// The provider's own page rule, moved earlier. A large limit is
	// saturating-safe in the provider (it can only cut at total) and stays
	// accepted — only an impossible page is refused.
	if p.Offset < 0 {
		return "offset must not be negative"
	}
	if p.Limit < 1 {
		return "limit must be at least 1"
	}
	return ""
}

// validateFilesReadRaw checks files.read: the binding, the file, and the
// byte bound. maxBytes <= 0 is the legitimate "server default": the
// provider's documented rule clamps anything <= 0 or above its 2 MiB ceiling
// to the ceiling ("the parameter can only lower the 2 MiB ceiling"), so a
// negative value — which no read can mean — is the only shape refused.
func validateFilesReadRaw(raw json.RawMessage) string {
	var p filesReadParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if !isLowerHex(p.BindingID, 32) {
		return "bindingId is required and must be the 32-hex id the backend minted"
	}
	if msg := validateFSPath(p.Path, "path"); msg != "" {
		return msg
	}
	if p.MaxBytes < 0 {
		return "maxBytes must not be negative"
	}
	return ""
}

// validateFilesWatchRaw checks files.watch: the binding and the watch set.
// An empty set is the deliberate "no watches" (the loop stops) and stays
// accepted; a non-empty set is bounded by count and per-path shape.
// validateFilesVisibleRaw checks files.visible: the binding, and that the
// flag was actually sent.
func validateFilesVisibleRaw(raw json.RawMessage) string {
	var p filesVisibleParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if !isLowerHex(p.BindingID, 32) {
		return "bindingId is required and must be the 32-hex id the backend minted"
	}
	if p.Visible == nil {
		return "visible is required"
	}
	return ""
}

func validateFilesWatchRaw(raw json.RawMessage) string {
	var p filesWatchParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if !isLowerHex(p.BindingID, 32) {
		return "bindingId is required and must be the 32-hex id the backend minted"
	}
	if len(p.Paths) > maxWatchPaths {
		return fmt.Sprintf("paths exceeds %d entries", maxWatchPaths)
	}
	for _, path := range p.Paths {
		if msg := validateFSPath(path, "a watch path"); msg != "" {
			return msg
		}
	}
	return ""
}

// validateFilesCloseRaw checks files.close: the binding id.
func validateFilesCloseRaw(raw json.RawMessage) string {
	var p filesCloseParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if !isLowerHex(p.BindingID, 32) {
		return "bindingId is required and must be the 32-hex id the backend minted"
	}
	return ""
}

// validateFilesRevealRaw checks files.reveal: the binding and the path the
// revealer hands to the OS file manager — a provider path, so the same
// absolute+clean contract applies.
func validateFilesRevealRaw(raw json.RawMessage) string {
	var p filesRevealParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if !isLowerHex(p.BindingID, 32) {
		return "bindingId is required and must be the 32-hex id the backend minted"
	}
	if msg := validateFSPath(p.Path, "path"); msg != "" {
		return msg
	}
	return ""
}

// FilesystemProviderFactory builds the provider files.open registers for a
// resolved session. rootPath is the optional D2 override — the verified
// OSC 7 cwd — and is empty when the caller omitted it; the provider
// interprets it by its own path rules and falls back to Root() when it is
// absent or unusable. Declared on the transport side so the dependency
// stays filesystem ← transport; wired at the composition root (AD-8), the
// way internal/discovery declares its consumer interfaces. When nil,
// files.open answers an error.
type FilesystemProviderFactory func(sess session.Session, rootPath string) (filesystem.Provider, error)

// FilesRevealer shows a path in the OS file manager (files.reveal). The
// Wails runtime seam is a later wave (design §6 step 6); the interface is
// declared now so the handler exists, the local-only guard is enforced,
// and an unwired revealer answers -32601 instead of silently doing
// nothing.
type FilesRevealer interface {
	Reveal(path string) error
}

// filesBinding is the transport's bookkeeping for one binding it issued.
// The filesystem package deliberately exposes neither a binding's session
// nor its endpoint attestation, so the transport records what it itself
// handed to Register at files.open: the session the binding belongs to
// (notification addressing, spec §5.2) and the endpoint attestation
// (files.reveal's local-only guard, D4). endpointID is empty for local
// bindings, which is what makes files.reveal a local-only capability.
type filesBinding struct {
	sessionID  session.ID
	endpointID string
	watcher    *filesWatcher // nil until files.watch
}

// defaultFilesPollInterval is the base unit for the transport-side
// digest-poll cadence while provider-side watchers are absent. It is 500 ms
// because the panel's responsive end is 2x (1 s), matching Ports' 1 s
// settle/debounce while staying below Git's 5 s poll; the ladder backs off to
// 10 s like Ports' periodic 10 s sample rather than adding a third cadence.
//
// A completed terminal command is a legitimate supplementary HINT, as it is
// for Ports, but D5 rejects command-end as the sole refresh source. The
// existing files.changed seam can consume that hint; terminal command-end
// wiring remains intentionally absent.
const defaultFilesPollInterval = 500 * time.Millisecond

const (
	filesPollResponsiveMultiplier = 2
	filesPollMaxIdleStep          = 4
	filesPollMaxFailureStep       = 4
)

// The delayed edge is derived from the scheduler's production envelope:
// routine idle polling tops out at 20×500 ms = 10 s, while the failure ladder
// tops out at 120×500 ms = 60 s. A visible panel is delayed only when no
// eligible path has been successfully observed across that whole envelope.
// Tests shorten this window through WSServer.filesRefreshStateThreshold.
const defaultFilesRefreshStateThreshold = 60 * time.Second

// A dispatch is one bounded listing of one watched path. maxWatchPaths=512
// bounds the watched set; the watcher owns exactly one timer for the earliest
// dueAt and breaks ties round-robin, never creating one timer or goroutine per
// watched directory. At the 512-path ceiling, exact freshness, bounded load,
// and full enumeration cannot all hold, so a scheduler that falls behind
// prefers stale-but-responsive over continuous load. Making that degradation
// filesPollEntry is the independent schedule and observation for one watched
// path. The pointer is the path's identity: a dispatch captures it and its
// generation so a remove-and-readd cannot let an old completion mutate the
// replacement.
type filesPollEntry struct {
	rev                       string
	lastSuccessfulObservation time.Time
	idleStep                  int
	failureStep               int
	dueAt                     time.Time
	generation                uint64
}

type filesPollPathResult uint8

const (
	filesPollPathOK filesPollPathResult = iota
	filesPollPathChanged
	filesPollPathFailed
	filesPollPathStopped
)

type filesRefreshState string

const (
	filesRefreshStateOK      filesRefreshState = "ok"
	filesRefreshStateDelayed filesRefreshState = "delayed"
)

// filesPollWait chooses a path's next delay. The idle and failure ladders are
// deliberately independent: one unreachable directory must not slow healthy
// paths, and one noisy directory must not reset quiet ones.
func filesPollWait(base time.Duration, idleStep, failureStep int, jitter *rand.Rand) time.Duration {
	if idleStep > filesPollMaxIdleStep {
		idleStep = filesPollMaxIdleStep
	}
	if failureStep > filesPollMaxFailureStep {
		failureStep = filesPollMaxFailureStep
	}
	multiplier := filesPollResponsiveMultiplier
	if failureStep > 0 {
		multiplier = 20
		switch failureStep {
		case 2:
			multiplier = 40
		case 3:
			multiplier = 80
		case 4:
			multiplier = 120
		}
	} else {
		switch idleStep {
		case 2:
			multiplier = 4
		case 3:
			multiplier = 10
		case 4:
			multiplier = 20
		}
	}
	wait := time.Duration(multiplier) * base
	if jitter != nil {
		span := wait / 10
		if span > 0 {
			wait += time.Duration(jitter.Int64N(int64(2*span+1))) - span
		}
	}
	return wait
}

func newFilesPollEntry(rev string, generation uint64, base time.Duration, jitter *rand.Rand) *filesPollEntry {
	now := time.Now()
	return &filesPollEntry{
		rev:                       rev,
		lastSuccessfulObservation: now,
		dueAt:                     now.Add(filesPollWait(base, 0, 0, jitter)),
		generation:                generation,
	}
}

type filesPollSelection struct {
	path       string
	entry      *filesPollEntry
	generation uint64
}

// filesWatcher is the transport-side watch state of one binding: the paths
// under observation and their independent schedules, the dirty set, the
// panel's visibility, and the poll loop that detects change. It holds the
// handle the owning connection's files.watch acquired — the authorisation
// happened there, once — and releases it on stop, before the binding is
// closed, so the use-guard always drains. It never references a *wsConn: the
// notification destination is resolved at emit time from the session's
// current subscriber, which is what survives an AD-9 reconnect.
type filesWatcher struct {
	bindingID string
	sessionID session.ID

	mu             sync.Mutex
	paths          map[string]*filesPollEntry
	pathOrder      []string
	cursor         int
	nextGeneration uint64
	catchUp        map[string]*filesPollEntry
	dirty          map[string]string
	handle         filesystem.Handle
	release        func()
	// visible is whether the panel showing this tree is on screen (D5 §5.5:
	// polling is "paused while visible() is false"). True at creation, since
	// files.watch is sent by a panel that is rendering; the client sends
	// every transition after that.
	visible      bool
	pollBase     time.Duration
	refreshState filesRefreshState
	// jitter is non-nil only for remote bindings. Local tests leave jitter nil,
	// making the ladder deterministic; remote waits vary by up to ±10%.
	jitter *rand.Rand

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
	// wake carries an immediate dispatch request. A becoming-visible edge
	// fills catchUp first; an upload marks only its destination due. Both
	// cases remain interruptible because one wake dispatches one path.
	wake chan struct{}
}

func filesPollJitterForEndpoint(endpointID string) *rand.Rand {
	if endpointID == "" {
		return nil
	}
	return rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())) //nolint:gosec // jitter only spaces remote polling; it cannot grant access, alter watched paths, or affect authorization
}

// markVisibleCatchUp records the exact paths present at a visibility edge.
// Paths added later are ordinary due work; paths removed and re-added are new
// identities and cannot inherit this catch-up obligation.
func (w *filesWatcher) markVisibleCatchUpLocked(now time.Time) {
	if w.catchUp == nil {
		w.catchUp = make(map[string]*filesPollEntry)
	}
	for path, entry := range w.paths {
		entry.dueAt = now
		entry.lastSuccessfulObservation = now
		w.catchUp[path] = entry
	}
}

// setVisible records the panel's visibility and, on a hidden→visible edge,
// asks the loop for one immediate dispatch. The edge is what is tested, not
// the value: re-sending "visible" while already visible must not turn the
// panel into a second cadence.
func (w *filesWatcher) setVisible(visible bool) {
	w.mu.Lock()
	was := w.visible
	w.visible = visible
	if visible && !was {
		w.markVisibleCatchUpLocked(time.Now())
	}
	w.mu.Unlock()
	if !visible || was {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default: // a catch-up dispatch is already owed; one is enough
	}
}

// observeSuccessful records the exact fact produced by a foreground
// files.list. It resets only that path, and only if the path identity selected
// before the call is still current; a replacement cannot inherit stale data.
func (w *filesWatcher) observeSuccessful(
	path string,
	expected *filesPollEntry,
	generation uint64,
	rev string,
	base time.Duration,
) {
	if expected == nil {
		return
	}
	w.mu.Lock()
	entry, ok := w.paths[path]
	if !ok || entry != expected || entry.generation != generation {
		w.mu.Unlock()
		return
	}
	delete(w.catchUp, path)
	now := time.Now()
	entry.rev = rev
	entry.lastSuccessfulObservation = now
	entry.idleStep = 0
	entry.failureStep = 0
	entry.dueAt = now.Add(filesPollWait(base, 0, 0, w.jitter))
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// requestPollNow marks exactly one path due. The wake is only a prompt to
// recompute the single watcher timer; the durable target is the path entry.
// Hidden watchers retain the due state and spend it on their next catch-up.
func (w *filesWatcher) requestPollNow(path string) {
	w.mu.Lock()
	entry := w.paths[path]
	if entry != nil {
		entry.dueAt = time.Now()
	}
	w.mu.Unlock()
	if entry == nil {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *filesWatcher) nextDueAt() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	var due time.Time
	for _, entry := range w.paths {
		if due.IsZero() || entry.dueAt.Before(due) {
			due = entry.dueAt
		}
	}
	return due
}

// takeDue selects one path only. Catch-up paths are selected round-robin;
// ordinary paths choose the earliest due time with the same tie breaker.
func (w *filesWatcher) takeDue(now time.Time) *filesPollSelection {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pathOrder) == 0 {
		return nil
	}
	if w.cursor >= len(w.pathOrder) {
		w.cursor = 0
	}
	if len(w.catchUp) > 0 {
		for i := range w.pathOrder {
			index := (w.cursor + i) % len(w.pathOrder)
			path := w.pathOrder[index]
			entry, ok := w.paths[path]
			if !ok || w.catchUp[path] != entry {
				continue
			}
			delete(w.catchUp, path)
			w.cursor = (index + 1) % len(w.pathOrder)
			return &filesPollSelection{path: path, entry: entry, generation: entry.generation}
		}
		for path, entry := range w.catchUp {
			if w.paths[path] != entry {
				delete(w.catchUp, path)
			}
		}
	}
	var earliest time.Time
	for _, path := range w.pathOrder {
		entry := w.paths[path]
		if entry == nil || entry.dueAt.After(now) {
			continue
		}
		if earliest.IsZero() || entry.dueAt.Before(earliest) {
			earliest = entry.dueAt
		}
	}
	if earliest.IsZero() {
		return nil
	}
	for i := range w.pathOrder {
		index := (w.cursor + i) % len(w.pathOrder)
		path := w.pathOrder[index]
		entry := w.paths[path]
		if entry != nil && entry.dueAt.Equal(earliest) {
			w.cursor = (index + 1) % len(w.pathOrder)
			return &filesPollSelection{path: path, entry: entry, generation: entry.generation}
		}
	}
	return nil
}

func (w *filesWatcher) isVisible() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.visible
}

// refreshStateAt derives the binding-level freshness state from the latest
// successful observation of any eligible path. Hidden time is excluded by
// the visibility gate; the visible edge resets each path's observation window.
func (w *filesWatcher) refreshStateAtLocked(now time.Time, threshold time.Duration) filesRefreshState {
	if !w.visible || len(w.paths) == 0 {
		return filesRefreshStateOK
	}
	var latest time.Time
	for _, entry := range w.paths {
		if entry.lastSuccessfulObservation.After(latest) {
			latest = entry.lastSuccessfulObservation
		}
	}
	if latest.IsZero() || now.Before(latest.Add(threshold)) {
		return filesRefreshStateOK
	}
	return filesRefreshStateDelayed
}

func (w *filesWatcher) refreshStateDeadline(threshold time.Duration) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.visible || len(w.paths) == 0 || w.refreshState == filesRefreshStateDelayed {
		return time.Time{}
	}
	var latest time.Time
	for _, entry := range w.paths {
		if entry.lastSuccessfulObservation.After(latest) {
			latest = entry.lastSuccessfulObservation
		}
	}
	if latest.IsZero() {
		return time.Time{}
	}
	return latest.Add(threshold)
}

// updateRefreshState applies an edge only. The caller emits the returned state
// outside the lock so a blocked socket cannot hold scheduler bookkeeping.
func (w *filesWatcher) updateRefreshState(now time.Time, threshold time.Duration) (filesRefreshState, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	want := w.refreshStateAtLocked(now, threshold)
	if w.refreshState == want {
		return want, false
	}
	w.refreshState = want
	return want, true
}

// filesMachine is the transport-owned files.* surface the handlers reach
// beyond their capability operations: the binding bookkeeping the filesystem
// package deliberately exposes nowhere (a binding's session and endpoint
// attestation), the watcher machinery files.watch drives, and the reveal
// guard's inputs. WSServer implements it; a handler is constructed with the
// interface, so it can reach exactly these operations and nothing else on
// the server. This is transport-owned state, not a store — no capability
// gates it (migration map, files.* rows).
type filesMachine interface {
	// recordFilesBinding adds the bookkeeping for a binding minted by
	// files.open: the session it belongs to (notification addressing,
	// spec §5.2) and the endpoint attestation (files.reveal's local-only
	// guard, D4).
	recordFilesBinding(bid string, sid session.ID, endpointID string)
	// filesBindingOf returns the bookkeeping for a binding id, or nil. The
	// session and endpoint fields are written once at creation and never
	// change, so the returned pointer is safe to read; the watcher slot is
	// only mutated under the bookkeeping lock (withFilesBinding).
	filesBindingOf(bid string) *filesBinding
	// removeFilesBinding drops the bookkeeping for a binding and returns it
	// (nil when unknown) — files.close's bookkeeping half; the registry
	// close is the handler's capability service.
	removeFilesBinding(bid string) *filesBinding
	// dropFilesBinding removes the bookkeeping and closes the binding in
	// the registry: the files.open error paths, where the id never reached
	// the client and the provider must not leak.
	dropFilesBinding(bid string, sid session.ID)
	// withFilesBinding runs f under the bookkeeping lock with the binding's
	// bookkeeping entry; it reports false when the binding is gone. The
	// closure runs with the lock held, so watcher installs and clears are
	// atomic against files.close and filesSessionClosed, exactly like the
	// inline handlers they replaced.
	withFilesBinding(bid string, f func(b *filesBinding)) bool
	// filesPollLoop runs the transport-side digest-poll change detector for
	// one binding (see the poll section below). files.watch starts it; the
	// loop keeps the handle that call acquired.
	filesPollLoop(w *filesWatcher)
	// filesPollBaseInterval returns the current transport poll base so
	// foreground observations can re-arm their path at the responsive rung.
	filesPollBaseInterval() time.Duration
	// stopFilesWatcher stops a binding's poll loop and releases its handle.
	stopFilesWatcher(w *filesWatcher)
	// filesBaseline takes the watch baseline synchronously: one listing per
	// path in the new set, inside files.watch, before the response.
	filesBaseline(h filesystem.Handle, paths []string, known map[string]string) map[string]string
	// cancelBindingTransfers cancels every running transfer of one binding
	// and waits, bounded, for them to unwind — files.close's half of D8.
	// It never waits for an upload: the bound expires and the close goes on.
	cancelBindingTransfers(bid string)
}

// ── wire shapes (contracts/files.*.schema.json) ──────────────────────────

type filesOpenParams struct {
	SessionID string `json:"sessionId"`
	RootPath  string `json:"rootPath,omitempty"`
}

type filesOpenResult struct {
	BindingID       string          `json:"bindingId"`
	EndpointID      *string         `json:"endpointId"` // null for a local binding, never absent
	Root            filesRootResult `json:"root"`
	RevealAvailable bool            `json:"revealAvailable"`
}

type filesRootResult struct {
	Path           string `json:"path"`
	Display        string `json:"display"`
	Inferred       bool   `json:"inferred"`
	InferredReason string `json:"inferredReason"`
}

type filesListParams struct {
	BindingID string `json:"bindingId"`
	Path      string `json:"path"`
	Offset    int    `json:"offset"`
	Limit     int    `json:"limit"`
}

// filesListEntry is one row of a listing. linkTarget/linkKind are present
// only for symlinks — omitempty, never null — and kind closes at the
// schema's enum: an entry whose metadata could not be read collapses to
// "other" (wireKind), never to a plausible size it never had.
type filesListEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	LinkTarget string `json:"linkTarget,omitempty"`
	LinkKind   string `json:"linkKind,omitempty"`
	Size       int64  `json:"size"`
	ModTime    string `json:"modTime"`
	Mode       uint32 `json:"mode"`
}

// The listing result is a discriminated union (D14) — three closed
// branches, one per outcome, never one object with everything optional.
type filesListOK struct {
	State     string           `json:"state"` // "ok"
	Path      string           `json:"path"`
	Canonical string           `json:"canonical"`
	Entries   []filesListEntry `json:"entries"` // never null: [] for an empty directory
	Offset    int              `json:"offset"`
	Total     int              `json:"total"`

	HasMore bool   `json:"hasMore"`
	Rev     string `json:"rev"`
}

type filesListTooLarge struct {
	State         string `json:"state"` // "tooLarge"
	ObservedCount int    `json:"observedCount"`
	Limit         int    `json:"limit"`
}

type filesListTimedOut struct {
	State   string `json:"state"`   // "timedOut"
	Timeout int64  `json:"timeout"` // milliseconds
}

type filesReadParams struct {
	BindingID string `json:"bindingId"`
	Path      string `json:"path"`
	MaxBytes  int64  `json:"maxBytes"`
}

type filesReadResult struct {
	Path      string `json:"path"`
	Canonical string `json:"canonical"`
	Text      string `json:"text"`
	Size      int64  `json:"size"`
	ModTime   string `json:"modTime"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
	Lossy     bool   `json:"lossy"`
	Changed   bool   `json:"changed"`
}

type filesWatchParams struct {
	BindingID string   `json:"bindingId"`
	Paths     []string `json:"paths"`
}

type filesWatchResult struct {
	Mode           string `json:"mode"` // "watching" | "polling"
	DegradedReason string `json:"degradedReason,omitempty"`
}

type filesCloseParams struct {
	BindingID string `json:"bindingId"`
}

// filesVisibleParams is the panel's on-screen signal for one binding. The
// flag is required rather than defaulted: a caller that omits it means
// nothing in particular, and a visibility gate that guesses is one that
// silently stops refreshing a panel somebody is looking at.
type filesVisibleParams struct {
	BindingID string `json:"bindingId"`
	Visible   *bool  `json:"visible"`
}

type filesRevealParams struct {
	BindingID string `json:"bindingId"`
	Path      string `json:"path"`
}

type filesChangedParams struct {
	BindingID string `json:"bindingId"`
	Path      string `json:"path"`
	Rev       string `json:"rev,omitempty"` // absent when nothing has been re-listed
}

type filesRefreshStateChangedParams struct {
	BindingID string `json:"bindingId"`
	State     string `json:"state"`
}

// ── handlers (constructed types) ───────────────────────────────────────────

// filesOpenHandlers answers files.open. It holds the FilesystemOpenOperation
// (session, filesystem gates — resolve, register and root all run inside the
// callback), the provider-factory seam and the transport bookkeeping seam;
// never the *WSServer. state arrives per call from the build closure: the
// session-ownership check is connState's (D15), and Acquire re-checks it.
type filesOpenHandlers struct {
	op      capability.FilesystemOpenOperation // nil → filesystem not wired
	factory capability.ProviderFactory         // nil → no provider factory wired
	machine filesMachine
	// revealAvailable is the composition fact carried on every open result:
	// whether this build has a file-manager revealer wired. Set once from
	// s.revealer != nil at registration; the renderer reads it to decide
	// whether the "Show in Finder" action exists at all (nocx-ngf3u).
	revealAvailable bool
	r               Responder
}

// handleOpen resolves a session the requesting connection owns and registers
// a provider for it, minting the binding every later files.* call carries.
// sessionId appears exactly once on the wire — here (D1) — and the
// authorisation is connState's, not the global registry's (D15): the same
// gate handleResize applies, and a connection that learned another
// connection's session id gets a refusal, not a filesystem.
func (h filesOpenHandlers) handleOpen(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "files not available"})
		return
	}
	var params filesOpenParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.SessionID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: sessionId required"})
		return
	}
	sid := session.ID(params.SessionID)
	if !state.has(sid) {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown sessionId"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.FilesystemOpenService) error {
		sess, err := svc.Get(sid)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown sessionId"})
			return nil
		}
		if h.factory == nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "files.open not available (no provider factory wired)"})
			return nil
		}
		// OpenBinding builds the provider through the wired factory and
		// registers it, returning the minted binding id and the endpoint
		// attestation (spec §5.1): local providers carry none — empty is
		// what makes files.reveal local-only (D4) — and the composition
		// root's remote providers attest their destination through the
		// optional seam. A provider that cannot attest registers with an
		// empty id; the frontend keys its viewers on this value, so an
		// empty remote id collapses into the local namespace and is
		// reported loudly by the factory's tests rather than silently here.
		bid, endpointID, err := svc.OpenBinding(ctx, sess, params.RootPath)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return nil
		}
		h.machine.recordFilesBinding(bid, sid, endpointID)

		// The root rides the open result — the panel needs a starting
		// directory before any files.list. The handle is held only for this
		// call: the use-guard covers the root acquisition, and release drops
		// it (spec §5.1: hold it for the call, defer the release).
		handle, release, err := svc.Acquire(bid, state)
		if err != nil {
			h.machine.dropFilesBinding(bid, sid)
			_ = h.r.TryError(req.ID, RPCError{Code: filesErrorCode(err), Message: err.Error()})
			return nil
		}
		root, err := handle.Root(ctx)
		release()
		if err != nil {
			h.machine.dropFilesBinding(bid, sid)
			_ = h.r.TryError(req.ID, RPCError{Code: filesErrorCode(err), Message: err.Error()})
			return nil
		}

		var ep *string
		if endpointID != "" {
			ep = &endpointID
		}
		_ = h.r.TryResult(req.ID, mustMarshal(filesOpenResult{
			BindingID:       bid,
			EndpointID:      ep,
			RevealAvailable: h.revealAvailable,
			Root: filesRootResult{
				Path:           root.Path,
				Display:        root.Display,
				Inferred:       root.Inferred,
				InferredReason: root.InferredReason,
			},
		}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// filesBindingHandlers answers the per-binding methods after files.open:
// files.list, files.read, files.watch, files.close and files.reveal. It
// holds the FilesystemBindingOperation (filesystem gate), the transport
// bookkeeping seam and the revealer; never the *WSServer. The binding id is
// validated per call by Acquire — bindings close at any moment, so a
// construction-time check would be a lie.
type filesBindingHandlers struct {
	op       capability.FilesystemBindingOperation // nil → filesystem not wired
	machine  filesMachine
	revealer FilesRevealer // nil → files.reveal answers -32601
	r        Responder
}

// handleList lists one page of one directory. The three D14 outcomes are
// RESULT states, never errors: tooLarge and timedOut are refusals a user can
// reason about, and the discriminated union is the contract.
func (h filesBindingHandlers) handleList(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "files not available"})
		return
	}
	var params filesListParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.BindingID == "" || params.Path == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: bindingId and path required"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.FilesystemBindingService) error {
		// Authorisation: Acquire re-checks that THIS connection owns the
		// binding's session (D15), and takes the use-guard that keeps the
		// binding alive for the call.
		handle, release, err := svc.Acquire(params.BindingID, state)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: filesErrorCode(err), Message: err.Error()})
			return nil
		}
		defer release()
		var watcher *filesWatcher
		var watchedEntry *filesPollEntry
		var watchedGeneration uint64
		h.machine.withFilesBinding(params.BindingID, func(b *filesBinding) {
			watcher = b.watcher
			if watcher == nil {
				return
			}
			watcher.mu.Lock()
			watchedEntry = watcher.paths[params.Path]
			if watchedEntry != nil {
				watchedGeneration = watchedEntry.generation
			}
			watcher.mu.Unlock()
		})
		listing, err := handle.List(ctx, params.Path, filesystem.Page{Offset: params.Offset, Limit: params.Limit})
		if err != nil {
			var tooLarge *filesystem.ErrTooLarge
			var timedOut *filesystem.ErrTimedOut
			switch {
			case errors.As(err, &tooLarge):
				_ = h.r.TryResult(req.ID, mustMarshal(filesListTooLarge{
					State:         "tooLarge",
					ObservedCount: tooLarge.ObservedCount,
					Limit:         tooLarge.Limit,
				}))
			case errors.As(err, &timedOut):
				_ = h.r.TryResult(req.ID, mustMarshal(filesListTimedOut{
					State:   "timedOut",
					Timeout: timedOut.Timeout.Milliseconds(),
				}))
			default:
				_ = h.r.TryError(req.ID, RPCError{Code: filesErrorCode(err), Message: err.Error()})
			}
			return nil
		}
		if watcher != nil {
			watcher.observeSuccessful(
				params.Path,
				watchedEntry,
				watchedGeneration,
				listing.Rev,
				h.machine.filesPollBaseInterval(),
			)
		}
		entries := make([]filesListEntry, 0, len(listing.Entries))
		for _, e := range listing.Entries {
			entries = append(entries, filesListEntry{
				Name:       e.Name,
				Path:       e.Path,
				Kind:       wireKind(e.Kind),
				LinkTarget: e.LinkTarget,
				LinkKind:   wireKind(e.LinkKind),
				Size:       e.Size,
				ModTime:    wireTime(e.ModTime),
				Mode:       e.Mode,
			})
		}
		_ = h.r.TryResult(req.ID, mustMarshal(filesListOK{
			State:     "ok",
			Path:      listing.Path,
			Canonical: listing.Canonical,
			Entries:   entries,
			Offset:    listing.Offset,
			Total:     listing.Total,
			HasMore:   listing.HasMore,
			Rev:       listing.Rev,
		}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleRead reads one file, bounded and streamed (spec §5.1): at most
// min(requested, 2 MiB) plus one byte, so the memory guard holds for a 40 GB
// file. Canonical is the identity the viewer's singletonKey is built from.
func (h filesBindingHandlers) handleRead(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "files not available"})
		return
	}
	var params filesReadParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.BindingID == "" || params.Path == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: bindingId and path required"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.FilesystemBindingService) error {
		handle, release, err := svc.Acquire(params.BindingID, state)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: filesErrorCode(err), Message: err.Error()})
			return nil
		}
		defer release()
		content, err := handle.Read(ctx, params.Path, params.MaxBytes)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: filesErrorCode(err), Message: err.Error()})
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(filesReadResult{
			Path:      content.Path,
			Canonical: content.Canonical,
			Text:      content.Text,
			Size:      content.Size,
			ModTime:   wireTime(content.ModTime),
			Truncated: content.Truncated,
			Binary:    content.Binary,
			Lossy:     content.Lossy,
			Changed:   content.Changed,
		}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleWatch replaces the binding's watch set (spec §5.2): the client sends
// the set it currently wants and the backend diffs, so collapsing a
// directory cannot leak a watch. The provider-side set is swapped atomically
// by the registry; when the provider refuses (the watching wave has not
// landed), the transport degrades to its own digest-poll loop and reports
// the degradation — the persistent "Polling" badge (spec §5.5) — rather than
// a silent lie. The watch baseline is taken synchronously inside the
// handler, before the response (filesBaseline): from the instant the call
// returns every change is delivered, and changes before it are not replayed
// — inotify semantics.
func (h filesBindingHandlers) handleWatch(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "files not available"})
		return
	}
	var params filesWatchParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.BindingID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: bindingId and paths required"})
		return
	}
	// The binding must be one this transport issued: the notification
	// addressing needs the session it belongs to, and filesystem exposes
	// none of that.
	b := h.machine.filesBindingOf(params.BindingID)
	if b == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown bindingId"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.FilesystemBindingService) error {
		// Authorisation: Acquire re-checks that THIS connection owns the
		// binding's session (D15) — one map lookup, and it is what holds if
		// an id ever reaches a log, a screenshot or a crash report.
		handle, release, err := svc.Acquire(params.BindingID, state)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: filesErrorCode(err), Message: err.Error()})
			return nil
		}
		mode, err := handle.Watch(ctx, params.Paths)
		if err != nil {
			var unavail *filesystem.ErrWatchUnavailable
			if !errors.As(err, &unavail) {
				release()
				_ = h.r.TryError(req.ID, RPCError{Code: filesErrorCode(err), Message: err.Error()})
				return nil
			}
			// No reason: ErrWatchUnavailable says the watching wave (nocx-rkk9)
			// has not landed, which is a build-time fact and not a degrade.
			// Polling is the designed and only mode today, it delivers the
			// change signal the user asked for, and the mechanism under it is
			// not their business. A reason here lights the §5.5 badge for every
			// user on every local binding forever — a warning that is always on
			// warns about nothing and teaches the user to ignore the next one.
			// When Live watching exists, a host that cannot have it will carry a
			// reason from the provider and the badge lights for the first time
			// meaningfully.
			mode = filesystem.WatchMode{Kind: filesystem.WatchPolling}
		}
		if len(params.Paths) == 0 {
			// The client wants no watches: the provider set is already empty
			// (the swap above), so stop the loop and drop the guard.
			release()
			var w *filesWatcher
			h.machine.withFilesBinding(params.BindingID, func(b *filesBinding) {
				w = b.watcher
				b.watcher = nil
			})
			if w != nil {
				h.machine.stopFilesWatcher(w)
			}
			_ = h.r.TryResult(req.ID, mustMarshal(filesWatchResultOf(mode)))
			return nil
		}

		// Take the baseline synchronously, before the response: from the
		// instant files.watch returns, every change is delivered; changes
		// before it are not (inotify semantics). The old first-poll-tick
		// baseline left a 500 ms window where a change was folded into the
		// baseline and never announced. The listing runs on this call's
		// fresh handle, whose guard is held until the branch below releases
		// it (filesBaseline documents the cost shape).
		// What the watcher is ALREADY comparing against. A path in here is
		// not re-listed (filesBaseline says why), so this call enumerates
		// only what the set gained.
		known := make(map[string]string)
		h.machine.withFilesBinding(params.BindingID, func(b *filesBinding) {
			if b.watcher == nil {
				return
			}
			b.watcher.mu.Lock()
			for p, entry := range b.watcher.paths {
				known[p] = entry.rev
			}
			b.watcher.mu.Unlock()
		})
		added := h.machine.filesBaseline(handle, params.Paths, known)

		installed := h.machine.withFilesBinding(params.BindingID, func(b *filesBinding) {
			if b.watcher == nil {
				jitter := filesPollJitterForEndpoint(b.endpointID)
				base := h.machine.filesPollBaseInterval()
				paths := make(map[string]*filesPollEntry, len(added))
				for p, rev := range added {
					paths[p] = newFilesPollEntry(rev, 1, base, jitter)
				}
				w := &filesWatcher{
					bindingID:      params.BindingID,
					sessionID:      b.sessionID,
					paths:          paths,
					pathOrder:      append([]string(nil), params.Paths...),
					catchUp:        make(map[string]*filesPollEntry),
					dirty:          make(map[string]string),
					visible:        true,
					pollBase:       base,
					refreshState:   filesRefreshStateOK,
					nextGeneration: 1,
					stop:           make(chan struct{}),
					done:           make(chan struct{}),
					wake:           make(chan struct{}, 1),
					jitter:         jitter,
				}
				w.handle = handle
				w.release = release
				b.watcher = w
				// Start under the bookkeeping lock so files.close cannot
				// observe a registered watcher whose loop is not running.
				go h.machine.filesPollLoop(w)
				return
			}

			b.watcher.mu.Lock()
			next := make(map[string]*filesPollEntry, len(params.Paths))
			nextGeneration := b.watcher.nextGeneration
			for _, p := range params.Paths {
				if entry, ok := b.watcher.paths[p]; ok {
					next[p] = entry
					continue
				}
				nextGeneration++
				rev := added[p]
				next[p] = newFilesPollEntry(
					rev,
					nextGeneration,
					h.machine.filesPollBaseInterval(),
					b.watcher.jitter,
				)
			}
			b.watcher.nextGeneration = nextGeneration
			b.watcher.paths = next
			b.watcher.pathOrder = append(b.watcher.pathOrder[:0], params.Paths...)
			for p, entry := range b.watcher.catchUp {
				if next[p] != entry {
					delete(b.watcher.catchUp, p)
				}
			}
			b.watcher.mu.Unlock()
			select {
			case b.watcher.wake <- struct{}{}:
			default:
			}
			// The loop keeps its original handle; this fresh guard belongs
			// only to the synchronous baseline above.
			release()
		})
		if !installed {
			// Raced with files.close; the binding is gone.
			release()
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown bindingId"})
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(filesWatchResultOf(mode)))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleClose closes the binding: its provider released, its watches torn
// down. Ownership is re-checked like every call (D15) — a binding is closed
// by the connection that owns its session, not by whoever knows its id. The
// watcher stops first so the use-guard drains before Close's teardown.
// handleVisible records whether the panel showing this binding's tree is on
// screen. While it is not, the poll loop issues no listing at all; a
// hidden→visible edge polls once immediately (D5 §5.5).
//
// It does NOT take the filesystem operation, and that is the decision worth
// stating. Every other binding method runs through h.op.Run for the D15
// ownership check, but that composes the filesystem gate — capacity one,
// queue eight, a one-second wait — against listings bounded at ten seconds
// locally and thirty over SFTP. A visibility signal put behind that gate is
// refusable by exactly the contention it exists to reduce: the busier the
// backend, the more likely the "I am hidden now" never lands, and the poll
// it was meant to stop goes on running. So the flag flip is answered from
// the transport's own bookkeeping.
//
// The authorisation is the same property, asked of the record the transport
// already holds rather than of the registry: this connection must own the
// binding's session. That is not a second answer to the question — the
// watcher's own comment records that "the authorisation happened there,
// once", in files.watch, which acquired the handle the loop still holds.
// This is a flag on that already-authorised object, and it touches no
// filesystem.
func (h filesBindingHandlers) handleVisible(state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "files not available"})
		return
	}
	var params filesVisibleParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.BindingID == "" || params.Visible == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: bindingId and visible required"})
		return
	}
	b := h.machine.filesBindingOf(params.BindingID)
	if b == nil || !state.Owns(b.sessionID) {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown bindingId"})
		return
	}
	// A binding with no watcher has nothing to pause: files.watch has not
	// been sent yet, and the watcher it creates starts visible. Answering OK
	// rather than erroring keeps the client free to send the signal in
	// whatever order its own effects run.
	h.machine.withFilesBinding(params.BindingID, func(b *filesBinding) {
		if b.watcher == nil {
			return
		}
		b.watcher.setVisible(*params.Visible)
	})
	_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
}

func (h filesBindingHandlers) handleClose(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "files not available"})
		return
	}
	var params filesCloseParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.BindingID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: bindingId required"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.FilesystemBindingService) error {
		_, release, err := svc.Acquire(params.BindingID, state)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: filesErrorCode(err), Message: err.Error()})
			return nil
		}
		release()

		b := h.machine.removeFilesBinding(params.BindingID)
		if b != nil && b.watcher != nil {
			h.machine.stopFilesWatcher(b.watcher)
		}
		// Cancel this binding's running uploads BEFORE the registry close
		// (upload design D8). Not because the close would otherwise block —
		// a transfer runs on a detached sink and holds no use-guard for
		// Binding.close to drain — but because a transfer whose binding has
		// gone should be told so, and told so before its lease is closed
		// underneath it. The wait after the cancel is bounded and the close
		// proceeds either way.
		h.machine.cancelBindingTransfers(params.BindingID)
		if err := svc.Close(params.BindingID); err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: filesErrorCode(err), Message: err.Error()})
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleReveal shows a path in the OS file manager. Local bindings only: the
// backend refuses a remote binding rather than silently doing nothing,
// because a UI-only guard is one bug away from being no guard (spec §5.2).
// Without a wired revealer the method answers -32601 — the Wails runtime
// seam is a later wave, and a reveal that did nothing would be a silent lie.
func (h filesBindingHandlers) handleReveal(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "files not available"})
		return
	}
	var params filesRevealParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.BindingID == "" || params.Path == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: bindingId and path required"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.FilesystemBindingService) error {
		_, release, err := svc.Acquire(params.BindingID, state)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: filesErrorCode(err), Message: err.Error()})
			return nil
		}
		release()

		b := h.machine.filesBindingOf(params.BindingID)
		if b == nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown bindingId"})
			return nil
		}
		if b.endpointID != "" {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "files.reveal: remote bindings cannot be revealed"})
			return nil
		}
		if h.revealer == nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "files.reveal not available"})
			return nil
		}
		if err := h.revealer.Reveal(params.Path); err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// filesSpecs declares the files.* control methods. Every method needs the
// connection's connState — files.open for the session-ownership check (D15)
// and the binding methods as the Acquire caller — so each is registered with
// reg and runs on the ordinary lane. The FilesystemOpenOperation and
// FilesystemBindingOperation are built here from the wired stores
// (composition root for this domain); when no filesystem registry is wired,
// every handler answers -32601 "files not available".
func (s *WSServer) filesSpecs(lane control.Admission, sessionGate, fsGate control.Admission) []methodSpec {
	var openOp capability.FilesystemOpenOperation
	var bindingOp capability.FilesystemBindingOperation
	var factory capability.ProviderFactory
	if s.filesys != nil {
		// The transport-side and capability-side factory types have the
		// same shape; this converts the wired one (AD-8, the dependency
		// stays filesystem ← capability). nil converts to nil, preserving
		// the old "no provider factory wired" answer.
		factory = capability.ProviderFactory(s.filesProviderFor)
		openOp = capability.NewFilesystemOpenOperation(sessionGate, fsGate, lane, s.registry, factory, s.filesys)
		bindingOp = capability.NewFilesystemBindingOperation(fsGate, lane, s.filesys)
	}
	openSub := s.operationQueue("files-open")
	bindingSub := s.operationQueue("files")
	specs := []methodSpec{
		reg(openSub, "files.open", params(validateFilesOpenRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := filesOpenHandlers{op: openOp, factory: factory, machine: s, revealAvailable: s.revealer != nil, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleOpen(ctx, state, req) }
		}),
		reg(bindingSub, "files.list", params(validateFilesListRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := filesBindingHandlers{op: bindingOp, machine: s, revealer: s.revealer, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleList(ctx, state, req) }
		}),
		reg(bindingSub, "files.read", params(validateFilesReadRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := filesBindingHandlers{op: bindingOp, machine: s, revealer: s.revealer, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleRead(ctx, state, req) }
		}),
		reg(bindingSub, "files.visible", params(validateFilesVisibleRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := filesBindingHandlers{op: bindingOp, machine: s, revealer: s.revealer, r: r}
			return func(_ context.Context, req jsonrpcRequest) { h.handleVisible(state, req) }
		}),
		reg(bindingSub, "files.watch", params(validateFilesWatchRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := filesBindingHandlers{
				op:       bindingOp,
				machine:  s,
				revealer: s.revealer,
				r:        r,
			}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleWatch(ctx, state, req) }
		}),
		reg(bindingSub, "files.close", params(validateFilesCloseRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := filesBindingHandlers{op: bindingOp, machine: s, revealer: s.revealer, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleClose(ctx, state, req) }
		}),
		reg(bindingSub, "files.reveal", params(validateFilesRevealRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := filesBindingHandlers{op: bindingOp, machine: s, revealer: s.revealer, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleReveal(ctx, state, req) }
		}),
		reg(bindingSub, "files.upload", params(validateFilesUploadRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := uploadHandlers{op: bindingOp, machine: s, sources: s.sources, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleUpload(ctx, state, req) }
		}),
		reg(bindingSub, "files.uploadCancel", params(validateFilesUploadCancelRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := uploadHandlers{op: bindingOp, machine: s, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleUploadCancel(ctx, state, req) }
		}),
	}
	// The download half is declared in ws_download.go and appended here,
	// on the SAME bounded submission, so both directions of the binding
	// domain queue against one bound rather than two.
	return append(specs, s.filesDownloadSpecs(bindingOp, bindingSub)...)
}

// ── notification delivery ─────────────────────────────────────────────────

// emitFilesChanged announces one dirty path for a binding. The destination
// is resolved at emit time (spec §5.2) — the binding's session's CURRENT
// subscriber, never the connection that opened the binding, which is
// destroyed on a WebSocket drop: this is what survives an AD-9 reconnect.
// With no subscriber — or a subscriber whose socket is gone (the write
// fails) — the path accumulates in the dirty set, delivered once on the
// next attach. A set, never a queue: a burst that meant one change
// replays as one.
func (s *WSServer) emitFilesChanged(w *filesWatcher, path, rev string) {
	rx := s.getRx(w.sessionID)
	if rx == nil {
		return // the session is gone; the binding is dying with it
	}
	wconn, _ := rx.getSubscriber()
	if wconn == nil {
		w.mu.Lock()
		w.dirty[path] = rev
		w.mu.Unlock()
		return
	}
	if err := wconn.TryNotify("files.changed", mustMarshal(filesChangedParams{
		BindingID: w.bindingID,
		Path:      path,
		Rev:       rev,
	})); err != nil {
		// The subscriber's socket is going down; the change must not be
		// lost with it. Keep the path dirty for the next attach.
		w.mu.Lock()
		w.dirty[path] = rev
		w.mu.Unlock()
		return
	}
	w.mu.Lock()
	delete(w.dirty, path)
	w.mu.Unlock()
}

func (s *WSServer) emitFilesRefreshState(w *filesWatcher, state filesRefreshState) {
	rx := s.getRx(w.sessionID)
	if rx == nil {
		return
	}
	wconn, _ := rx.getSubscriber()
	if wconn == nil {
		return
	}
	_ = wconn.TryNotify("files.refreshStateChanged", mustMarshal(filesRefreshStateChangedParams{
		BindingID: w.bindingID,
		State:     string(state),
	}))
}

// flushFilesChanged delivers the dirty paths a session's bindings
// accumulated while no connection was attached, to the connection that
// just attached. One notification per dirty path, then clear — the
// accumulation was a set, so a burst that meant one change delivers once
// (spec §5.2). Called from handleAttach after setSubscriber, so the
// notifications resolve to the new connection.
func (s *WSServer) flushFilesChanged(sid session.ID, wconn Responder) {
	s.filesMu.Lock()
	ids := make([]string, 0, len(s.filesBySession[sid]))
	for id := range s.filesBySession[sid] {
		ids = append(ids, id)
	}
	watchers := make([]*filesWatcher, 0, len(ids))
	for _, id := range ids {
		if b := s.filesBindings[id]; b != nil && b.watcher != nil {
			watchers = append(watchers, b.watcher)
		}
	}
	s.filesMu.Unlock()
	for _, w := range watchers {
		w.mu.Lock()
		dirty := make(map[string]string, len(w.dirty))
		for p, rev := range w.dirty {
			dirty[p] = rev
		}
		w.mu.Unlock()
		for p, rev := range dirty {
			if err := wconn.TryNotify("files.changed", mustMarshal(filesChangedParams{
				BindingID: w.bindingID,
				Path:      p,
				Rev:       rev,
			})); err != nil {
				return // the socket is dying; whatever remains stays dirty
			}
			w.mu.Lock()
			delete(w.dirty, p)
			w.mu.Unlock()
		}
		w.mu.Lock()
		state := w.refreshState
		w.mu.Unlock()
		if state == "" {
			state = filesRefreshStateOK
		}
		if err := wconn.TryNotify("files.refreshStateChanged", mustMarshal(filesRefreshStateChangedParams{
			BindingID: w.bindingID,
			State:     string(state),
		})); err != nil {
			return
		}
	}
}

// ── the digest-poll watcher ───────────────────────────────────────────────

// filesPollLoop is the transport-side change detector for one binding: each
// dispatch re-lists one selected path and compares its digest — the same
// comparison the SFTP watcher will perform provider-side when the watching
// wave lands (design §6 step 5). Until then this loop is the only change
// signal, and it is what files.changed delivers.
func (s *WSServer) filesPollBaseInterval() time.Duration {
	interval := s.filesPollInterval
	if interval <= 0 {
		return defaultFilesPollInterval
	}
	return interval
}

func (s *WSServer) filesPollLoop(w *filesWatcher) {
	defer close(w.done)
	base := s.filesPollBaseInterval()
	threshold := s.filesRefreshStateThreshold
	if threshold <= 0 {
		threshold = defaultFilesRefreshStateThreshold
	}
	w.mu.Lock()
	w.pollBase = base
	w.mu.Unlock()
	timer := time.NewTimer(base)
	defer timer.Stop()
	resetTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		wait := time.Hour
		if w.isVisible() {
			wait = base
			if due := w.nextDueAt(); !due.IsZero() {
				wait = time.Until(due)
				if wait < 0 {
					wait = 0
				}
			}
			if deadline := w.refreshStateDeadline(threshold); !deadline.IsZero() {
				stateWait := time.Until(deadline)
				if stateWait < wait {
					wait = stateWait
				}
				if wait < 0 {
					wait = 0
				}
			}
		}
		timer.Reset(wait)
	}
	updateState := func() {
		state, changed := w.updateRefreshState(time.Now(), threshold)
		if changed {
			s.emitFilesRefreshState(w, state)
		}
	}
	runPoll := func() bool {
		result := s.filesPollTick(w)
		if result == filesPollPathStopped {
			return false
		}
		updateState()
		resetTimer()
		return true
	}
	resetTimer()
	for {
		select {
		case <-w.stop:
			return
		case <-w.wake:
			if w.isVisible() && !runPoll() {
				return
			}
		case <-timer.C:
			if !w.isVisible() {
				resetTimer()
				continue
			}
			if !runPoll() {
				return
			}
		}
	}
}

// filesBaseline takes the watch baseline synchronously: one listing per
// ADDED path in the new set, inside files.watch, before the response. This
// is the inotify install point — from the instant the call returns, every
// change is delivered, and changes before it are not replayed (the
// first-poll-tick baseline this replaces left a 500 ms blind spot). A path
// whose listing fails is recorded as "" — unbaselined — and the first poll
// that lists it successfully ANNOUNCES rather than folding the change into
// a baseline nobody saw: the safe direction (a false positive re-lists the
// directory; a false negative hides a change forever).
//
// `known` is what the binding's watcher is already comparing against, and a
// path in it is SKIPPED rather than re-listed. That is not only the cheap
// answer, it is the correct one, and the two arrived together (nocx-8hdia.5):
//
//   - Correctness. filesPollPath announces when the fresh rev differs from
//     w.paths[path]. Re-baselining an already-watched path moves that
//     comparison point to the CURRENT rev, so a change that landed after the
//     last tick and before this call is folded into a baseline nobody saw and
//     is never announced. Since files.watch REPLACES the set and the client
//     resends the whole set on every expand and collapse, expanding one
//     folder could silently swallow a pending change in another — the exact
//     failure the synchronous baseline exists to prevent, reintroduced
//     through the replacement path.
//   - Cost. Every resend re-enumerated every already-watched directory, and
//     each enumeration is a full readdir with an lstat per entry, a sort and
//     a digest (internal/filesystem/{local,sftp}: the whole directory is
//     enumerated before the page is sliced, so Limit does not bound it).
//     That was charged to a click, and it grew with the number of folders
//     the person had open.
//
// A path whose previous baseline is "" is retained too, not retried: "" means
// the first successful poll ANNOUNCES, and re-listing it here would establish
// a silent baseline instead — the unsafe direction.
//
// Sequential, one listing per added path, and deliberately so: each listing
// is bounded by the D14 caps (entry cap, size cap, the provider's list
// timeout) and, over SFTP, by the operation lane. Additions are one directory
// per expand gesture, so this is normally a single listing; a bounded-
// concurrent baseline would add machinery the poll loop itself does not have
// for a case the product does not reach.
func (s *WSServer) filesBaseline(
	h filesystem.Handle,
	paths []string,
	known map[string]string,
) map[string]string {
	base := make(map[string]string, len(paths))
	for _, p := range paths {
		if _, ok := known[p]; ok {
			continue // already being compared against; see above
		}
		// The poll loop is binding-owned, never request-scoped: it runs for
		// the life of the watch (owner: the files binding, bounded by its
		// session per spec §5.1, never by a WebSocket), so it keeps a
		// background context. The closing event is the binding teardown —
		// stopFilesWatcher, filesSessionClosed or filesBindingClosed.
		listing, err := h.List(context.Background(), p, filesystem.Page{Offset: 0, Limit: 1})
		if err != nil {
			s.log.Debug("files watch baseline failed", "path", p, "error", err)
			base[p] = "" // unbaselined: the first successful poll announces
			continue
		}
		base[p] = listing.Rev
	}
	return base
}

// filesPollTick performs exactly one bounded dispatch: one selected path,
// never the entire watch set. It returns the path outcome so the loop can
// stop when the binding has gone away.
func (s *WSServer) filesPollTick(w *filesWatcher) filesPollPathResult {
	w.mu.Lock()
	h := w.handle
	w.mu.Unlock()
	if h == nil {
		return filesPollPathOK
	}
	selection := w.takeDue(time.Now())
	if selection == nil {
		return filesPollPathOK
	}
	return s.filesPollPath(w, h, selection)
}

// filesPollPath re-lists one selected watched path and updates only that
// path's ladder. The pointer and generation check prevents a stale completion
// from mutating a remove-and-readded path.
func (s *WSServer) filesPollPath(
	w *filesWatcher,
	h filesystem.Handle,
	selection *filesPollSelection,
) filesPollPathResult {
	if selection == nil || selection.entry == nil {
		return filesPollPathOK
	}
	path := selection.path
	listing, err := h.List(context.Background(), path, filesystem.Page{Offset: 0, Limit: 1})
	if err != nil {
		var released *filesystem.ErrHandleReleased
		var notFound *filesystem.ErrNotFound
		switch {
		case errors.As(err, &released):
			s.log.Debug("files watcher: binding closed", "binding_id", w.bindingID, "error", err)
			go s.filesBindingClosed(w.bindingID)
			return filesPollPathStopped
		case errors.As(err, &notFound):
			w.mu.Lock()
			current := w.paths[path]
			valid := current == selection.entry && current.generation == selection.generation
			if valid {
				delete(w.paths, path)
				delete(w.catchUp, path)
				for i, watched := range w.pathOrder {
					if watched == path {
						w.pathOrder = append(w.pathOrder[:i], w.pathOrder[i+1:]...)
						if w.cursor >= len(w.pathOrder) {
							w.cursor = 0
						}
						break
					}
				}
			}
			w.mu.Unlock()
			if !valid {
				return filesPollPathOK
			}
			s.emitFilesChanged(w, path, "")
			return filesPollPathChanged
		default:
			w.mu.Lock()
			if current := w.paths[path]; current == selection.entry &&
				current.generation == selection.generation {
				current.failureStep++
				if current.failureStep > filesPollMaxFailureStep {
					current.failureStep = filesPollMaxFailureStep
				}
				current.dueAt = time.Now().Add(filesPollWait(
					w.pollBase, current.idleStep, current.failureStep, w.jitter,
				))
			}
			w.mu.Unlock()
			s.log.Debug("files poll error", "binding_id", w.bindingID, "path", path, "error", err)
			return filesPollPathFailed
		}
	}

	w.mu.Lock()
	current := w.paths[path]
	if current == nil || current != selection.entry || current.generation != selection.generation {
		w.mu.Unlock()
		return filesPollPathOK
	}
	changed := current.rev == "" || listing.Rev != current.rev
	current.rev = listing.Rev
	current.lastSuccessfulObservation = time.Now()
	current.failureStep = 0
	if changed {
		current.idleStep = 0
	} else {
		current.idleStep++
		if current.idleStep > filesPollMaxIdleStep {
			current.idleStep = filesPollMaxIdleStep
		}
	}
	current.dueAt = time.Now().Add(filesPollWait(
		w.pollBase, current.idleStep, current.failureStep, w.jitter,
	))
	w.mu.Unlock()
	if changed {
		s.emitFilesChanged(w, path, listing.Rev)
		return filesPollPathChanged
	}
	return filesPollPathOK
}

// ── lifecycle ─────────────────────────────────────────────────────────────

// stopFilesWatcher stops a binding's poll loop and releases its handle.
// The release runs WITHOUT waiting for the loop to exit, and that is safe
// by the filesystem package's own contract (binding.go): each Handle
// method takes its own per-call use-guard, a released handle only refuses
// future begin() calls (ErrHandleReleased) while in-flight calls keep
// their guards, and Close waits for the guards, not for the loop. So the
// loop's next List after the release errors and stops it, and the registry
// close never sees a guard held by the watcher. Waiting instead would let
// a notification write hold a close path hostage forever. The enqueue is
// non-blocking (the outbound pump owns the socket), and a subscriber that
// stopped reading while its socket stays open cannot hang the poll loop —
// the frame is dropped and the path stays dirty for the next attach. w.done
// closes when the loop exits; nothing waits on it.
func (s *WSServer) stopFilesWatcher(w *filesWatcher) {
	w.stopOnce.Do(func() { close(w.stop) })
	w.mu.Lock()
	if w.release != nil {
		w.release()
		w.release = nil
	}
	w.mu.Unlock()
}

// filesSessionClosed tears down every binding of a session — closing the
// terminal closes its bindings (spec §5.1): a binding is bounded by its
// session, never by a WebSocket. The watchers stop first (their handles
// release, so the use-guards drain), then the registry closes the
// bindings. Fire-and-forget: the session itself is already dying, and
// nothing awaits this. Idempotent: a session that reaches this twice
// cleans up once.
func (s *WSServer) filesSessionClosed(sid session.ID) {
	s.filesMu.Lock()
	ids := make([]string, 0, len(s.filesBySession[sid]))
	for id := range s.filesBySession[sid] {
		ids = append(ids, id)
	}
	delete(s.filesBySession, sid)
	watchers := make([]*filesWatcher, 0, len(ids))
	for _, id := range ids {
		b := s.filesBindings[id]
		delete(s.filesBindings, id)
		if b != nil && b.watcher != nil {
			watchers = append(watchers, b.watcher)
		}
	}
	s.filesMu.Unlock()
	for _, w := range watchers {
		s.stopFilesWatcher(w)
	}
	// Same ordering, same reason as files.close (D8): the cancel tells a
	// transfer its binding is going before the lease under its sink is
	// closed. CloseSession does not wait for it either way.
	s.cancelSessionTransfers(sid)
	if s.filesys != nil {
		s.filesys.CloseSession(sid)
	}
}

// filesBindingClosed removes a binding's bookkeeping and stops its watcher
// — the defensive cleanup when the poll loop discovers the binding was
// closed underneath it (the transport's own close paths stop first, so
// this is a safety net, not the common path).
func (s *WSServer) filesBindingClosed(bid string) {
	s.filesMu.Lock()
	b := s.filesBindings[bid]
	delete(s.filesBindings, bid)
	if b != nil {
		if set := s.filesBySession[b.sessionID]; set != nil {
			delete(set, bid)
			if len(set) == 0 {
				delete(s.filesBySession, b.sessionID)
			}
		}
	}
	s.filesMu.Unlock()
	if b != nil && b.watcher != nil {
		s.stopFilesWatcher(b.watcher)
	}
}

// dropFilesBinding removes a binding from the transport's bookkeeping and
// closes it in the registry. Used on the files.open error paths, where the
// id never reached the client and the provider must not leak.
func (s *WSServer) dropFilesBinding(bid string, sid session.ID) {
	s.filesMu.Lock()
	delete(s.filesBindings, bid)
	if set := s.filesBySession[sid]; set != nil {
		delete(set, bid)
		if len(set) == 0 {
			delete(s.filesBySession, sid)
		}
	}
	s.filesMu.Unlock()
	_ = s.filesys.Close(bid)
}

// recordFilesBinding adds the transport bookkeeping for a binding minted by
// files.open: the session it belongs to (notification addressing, spec §5.2)
// and the endpoint attestation (files.reveal's local-only guard, D4).
func (s *WSServer) recordFilesBinding(bid string, sid session.ID, endpointID string) {
	s.filesMu.Lock()
	s.filesBindings[bid] = &filesBinding{sessionID: sid, endpointID: endpointID}
	set := s.filesBySession[sid]
	if set == nil {
		set = make(map[string]struct{})
		s.filesBySession[sid] = set
	}
	set[bid] = struct{}{}
	s.filesMu.Unlock()
}

// filesBindingOf returns the bookkeeping for a binding id, or nil. The
// session and endpoint fields are written once at creation and never change,
// so the returned pointer is safe to read; the watcher slot is only mutated
// under the bookkeeping lock (withFilesBinding).
func (s *WSServer) filesBindingOf(bid string) *filesBinding {
	s.filesMu.Lock()
	defer s.filesMu.Unlock()
	return s.filesBindings[bid]
}

// removeFilesBinding drops a binding from the transport bookkeeping and
// returns it (nil when unknown) — files.close's bookkeeping half. The
// registry close is the handler's capability service.
func (s *WSServer) removeFilesBinding(bid string) *filesBinding {
	s.filesMu.Lock()
	defer s.filesMu.Unlock()
	b := s.filesBindings[bid]
	delete(s.filesBindings, bid)
	if b != nil {
		if set := s.filesBySession[b.sessionID]; set != nil {
			delete(set, bid)
			if len(set) == 0 {
				delete(s.filesBySession, b.sessionID)
			}
		}
	}
	return b
}

// withFilesBinding runs f under the bookkeeping lock with the binding's
// bookkeeping entry; it reports false when the binding is gone. Watcher
// installs and clears happen inside f, so they are atomic against
// files.close and filesSessionClosed, exactly like the inline handlers they
// replaced.
func (s *WSServer) withFilesBinding(bid string, f func(b *filesBinding)) bool {
	s.filesMu.Lock()
	defer s.filesMu.Unlock()
	b := s.filesBindings[bid]
	if b == nil {
		return false
	}
	f(b)
	return true
}

// ── wire mapping helpers ──────────────────────────────────────────────────

// filesErrorCode maps filesystem domain errors to JSON-RPC codes: the
// request-shaped refusals — a binding the caller cannot use, a path the
// provider rejects, a row that cannot be opened — are invalid-params;
// everything else is internal. The message always carries the domain
// error's own words (permission denied, no such file or directory), which
// is what the panel surfaces.
func filesErrorCode(err error) int {
	switch err.(type) {
	case *filesystem.ErrUnknownBinding, *filesystem.ErrNotOwned,
		*filesystem.ErrInvalidPath, *filesystem.ErrInvalidPage,
		*filesystem.ErrNotFound, *filesystem.ErrNotDir,
		*filesystem.ErrNotRegular, *filesystem.ErrPermission,
		// A binding with no sink cannot be uploaded to, and one with no
		// source cannot be downloaded from (rule R1, in each direction).
		// Both belong with the request-shaped refusals and not with
		// -32603: the caller named a binding that cannot do this, which is
		// a property of what they asked for, not of the server going
		// wrong.
		*filesystem.ErrUploadUnsupported,
		*filesystem.ErrDownloadUnsupported:
		return -32602
	default:
		return -32603
	}
}

// wireKind maps a provider Kind onto the wire enum
// (contracts/files.list.schema.json). The schema closes at
// regular|dir|symlink|other — there is no "unreadable" — and an entry
// whose metadata could not be read must not be rendered as empty plausible
// data nor as a broken link: its open/expand table row is exactly
// "other"'s (do neither), so it collapses there rather than failing the
// contract or lying with a size it never had.
func wireKind(k filesystem.Kind) string {
	if k == filesystem.KindUnreadable {
		return "other"
	}
	return string(k)
}

// wireTime renders a modification time in the schema's ISO-8601 UTC shape.
func wireTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// filesWatchResultOf maps a provider WatchMode onto the wire shape: the
// schema's mode enum spells "watching" where the provider says "live".
func filesWatchResultOf(m filesystem.WatchMode) filesWatchResult {
	mode := "polling"
	if m.Kind == filesystem.WatchLive {
		mode = "watching"
	}
	return filesWatchResult{Mode: mode, DegradedReason: m.DegradedReason}
}
