package farhelper

// External inputs are coalesced by an entry-owned inbox. Its loop handles
// lifecycle events independently of a remote step that ignores cancellation.
// Step state mutations are retained here until the transition-table rebuild.

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostmode"
)

type phase uint8

const (
	// phaseDeciding is a decision in progress, or one waiting for its inputs:
	// C9's slow grade and first shell, a transport pause, or a retry timer.
	phaseDeciding phase = iota
	phaseServing
	phaseAbsent
	phaseBroken
)

// The redial schedule: 1 s, doubling, capped at 30 s, with no limit while the
// transport is connected (ADR-727g63tr decision 2's bounded backoff). It is one
// policy in one place so a change is one number, and the clock is injected so a
// test advances it rather than waiting.
const (
	firstRedial = time.Second
	maxRedial   = 30 * time.Second
)

func redialDelay(attempt int) time.Duration {
	delay := firstRedial
	for i := 0; i < attempt && delay < maxRedial; i++ {
		delay *= 2
	}
	if delay > maxRedial {
		delay = maxRedial
	}
	return delay
}

type entry struct {
	r  *Registry
	id string

	inboxMu         sync.Mutex
	interruptCancel context.CancelFunc
	inbox           entryInbox
	ingress         Connection
	mu              sync.Mutex
	// conn is the newest observation, and haveConn says one arrived.
	conn     Connection
	haveConn bool

	// phase, reason and action are the internal state and the reason it
	// carries. Status is their fold; a surface never reads these fields.
	phase  phase
	reason Reason
	action Action

	// mode and consent are the inputs the current revision was decided from.
	// The revision moves only when one of them changes, which is what makes a
	// change of mode during an install cancel the work and drop its answer.
	mode         hostmode.Mode
	consent      consent.Answer
	consentKnown bool
	rev          uint64

	// installed is the completed install, kept across transport pauses: a
	// return redials it, never re-probes or reinstalls it. A revision change
	// does not clear it either — a mode that leaves helper and returns finds
	// the build still there, and the decision is still a dial.
	installed *Installed
	// everServed distinguishes a helper that was serving and is gone
	// (helper-lost, refresh) from one that never answered (install-failed, no
	// button).
	everServed bool
	// platform is a successful probe's answer for the generation and revision
	// in probedGen/probedRev. Holding it is what keeps "decides once per
	// generation and revision" true while C9's gate is shut: the probe is
	// allowed to run, the install waits, and the wait does not probe again.
	platform  *Platform
	probedGen uint64
	probedRev uint64
	// frozen is D25's window: clients are closed and no decision may dial until
	// the uninstall finishes.
	frozen bool

	// attempt and nextAttemptAt are the redial schedule. nextAttemptAt is a
	// reading of the injected clock; zero means "now".
	attempt       int
	nextAttemptAt time.Duration

	client        *client.Client
	servingCtx    context.Context
	servingCancel context.CancelFunc

	// workCancel cancels the step in flight, and is nil when none is.
	workCancel context.CancelFunc

	wake  chan struct{}
	ready chan struct{}

	// lastStatus is what was last published, so an observation that changes
	// nothing costs no frame.
	lastStatus Status

	ctx    context.Context
	cancel context.CancelFunc
	closed bool
}

func newEntry(r *Registry, c Connection) *entry {
	ctx, cancel := context.WithCancel(r.ctx)
	e := &entry{
		r:        r,
		ingress:  c,
		id:       c.ID,
		conn:     c,
		haveConn: true,
		phase:    phaseDeciding,
		action:   ActionNone,
		wake:     make(chan struct{}, 1),
		ready:    make(chan struct{}),
		ctx:      ctx,
		cancel:   cancel,
	}
	return e
}

func (e *entry) start() { go e.run() }

func (e *entry) fingerprint() string {
	e.inboxMu.Lock()
	defer e.inboxMu.Unlock()
	return e.ingress.HostKeyFingerprint
}

func (e *entry) targetLocked() Target {
	return Target{
		ConnectionID:       e.id,
		Destination:        e.conn.Destination,
		HostKeyFingerprint: e.conn.HostKeyFingerprint,
		Generation:         e.conn.Generation,
	}
}

func (e *entry) connectedLocked() bool {
	return e.haveConn && e.conn.State == hostconn.Connected
}

// readInputsLocked reads the two stores and records what the decision would be
// based on. Called with the mutex held (at construction) or through
// inputsChanged (outside it).
func (e *entry) readInputsLocked() {
	if e.r.cfg.Modes != nil {
		mode, ok := e.r.cfg.Modes.Lookup(e.conn.HostKeyFingerprint)
		if !ok {
			mode = hostmode.Auto
		}
		e.mode = mode
	} else {
		e.mode = hostmode.Auto
	}
	if e.r.cfg.Consents != nil {
		e.consent, e.consentKnown = e.r.cfg.Consents.Lookup(e.conn.HostKeyFingerprint)
	} else {
		e.consent, e.consentKnown = "", false
	}
}

// apply folds one observation in. It wakes the loop always; it interrupts the
// step in flight only for the two facts a result is fenced by (a new generation
// or leaving the connected state) and for the first fingerprint, because every
// step reads the record the fingerprint keys.
func (e *entry) handleApply(c Connection) {
	e.mu.Lock()
	if c.Seq != 0 && e.conn.Seq != 0 && c.Seq <= e.conn.Seq && c.HostKeyFingerprint == e.conn.HostKeyFingerprint {
		e.mu.Unlock()
		return
	}
	previous := e.conn
	wasConnected := e.connectedLocked()
	generationChanged := e.haveConn && c.Generation != previous.Generation
	fingerprintChanged := e.haveConn && c.HostKeyFingerprint != previous.HostKeyFingerprint
	e.conn = c
	e.haveConn = true
	if generationChanged {
		// A new link: the old client rode the old one, and the redial schedule
		// starts over rather than waiting for a backoff earned by a different
		// connection.
		e.attempt = 0
		e.nextAttemptAt = 0
	}
	if wasConnected && c.State != hostconn.Connected {
		// The return is a fresh attempt, not a continuation of the schedule
		// the cut interrupted.
		e.attempt = 0
		e.nextAttemptAt = 0
	}
	if fingerprintChanged {
		e.rev++
		e.readInputsLocked()
	}
	interrupt := generationChanged || fingerprintChanged || (wasConnected && c.State != hostconn.Connected)
	var closeClient *client.Client
	if interrupt {
		if e.workCancel != nil {
			e.workCancel()
		}
		closeClient = e.detachClientLocked()
		if e.phase != phaseAbsent && e.phase != phaseDeciding {
			e.phase = phaseDeciding
			e.reason = ""
			e.action = ActionNone
		}
	}
	_, changed := e.publishLocked()
	e.mu.Unlock()
	if closeClient != nil {
		e.closePeer(closeClient)
	}
	if changed {
		e.requestPublication()
	}
	e.notify()
}

// inputsChanged is the stores' notification. It reads the two records and moves
// the revision only when the answer about THIS machine changed.
func (e *entry) handleInputsChanged() {
	e.mu.Lock()
	fingerprint := e.conn.HostKeyFingerprint
	e.mu.Unlock()
	mode := hostmode.Auto
	if e.r.cfg.Modes != nil {
		if recorded, ok := e.r.cfg.Modes.Lookup(fingerprint); ok {
			mode = recorded
		}
	}
	var answer consent.Answer
	known := false
	if e.r.cfg.Consents != nil {
		answer, known = e.r.cfg.Consents.Lookup(fingerprint)
	}

	e.mu.Lock()
	if mode == e.mode && known == e.consentKnown && answer == e.consent {
		e.mu.Unlock()
		return
	}
	e.rev++
	e.mode, e.consent, e.consentKnown = mode, answer, known
	// A revision is a new decision: an outcome that was absent for a reason
	// (install failed, platform) is decided again.
	if e.phase == phaseAbsent && outcomeAbsent(e.reason) {
		e.phase = phaseDeciding
		e.reason = ""
		e.action = ActionNone
	}
	// AND THE NEW ANSWER IS APPLIED AT ONCE. The loop may be parked inside a
	// remote step that ignores cancellation, and a surface must not go on
	// reading "starting" for a host that was just set to raw.
	var closeClient *client.Client
	if reason, action, allowed := inputVerdict(mode, known, answer); !allowed {
		if e.phase != phaseAbsent || e.reason != reason || e.action != action {
			e.phase = phaseAbsent
			e.reason = reason
			e.action = action
			closeClient = e.detachClientLocked()
		}
	} else if e.phase == phaseAbsent && !outcomeAbsent(e.reason) {
		// The record answers "helper" again (raw -> helper, denied -> granted).
		// An input-derived absence is not a verdict, it is the last read.
		e.phase = phaseDeciding
		e.reason = ""
		e.action = ActionNone
	}
	if e.workCancel != nil {
		e.workCancel()
	}
	_, changed := e.publishLocked()
	e.mu.Unlock()
	if closeClient != nil {
		e.closePeer(closeClient)
	}
	if changed {
		e.requestPublication()
	}
	e.notify()
}

// checkInputs re-reads the two records in the middle of a step and reports
// whether they still match the revision the step began under. It is the fence
// before every remote effect and again before publishing a hello.
func (e *entry) checkInputs(rev uint64) bool {
	e.mu.Lock()
	fingerprint := e.conn.HostKeyFingerprint
	e.mu.Unlock()
	mode := hostmode.Auto
	if e.r.cfg.Modes != nil {
		if recorded, ok := e.r.cfg.Modes.Lookup(fingerprint); ok {
			mode = recorded
		}
	}
	var answer consent.Answer
	known := false
	if e.r.cfg.Consents != nil {
		answer, known = e.r.cfg.Consents.Lookup(fingerprint)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if mode != e.mode || known != e.consentKnown || answer != e.consent {
		e.rev++
		e.mode, e.consent, e.consentKnown = mode, answer, known
		if e.phase == phaseAbsent && outcomeAbsent(e.reason) {
			e.phase = phaseDeciding
			e.reason = ""
			e.action = ActionNone
		}
		return false
	}
	return e.rev == rev
}

func (e *entry) stillCurrent(generation, rev uint64) bool {
	e.inboxMu.Lock()
	pending := e.inbox.close || e.inbox.freeze != 0
	if c := e.inbox.observation; c != nil {
		pending = pending || c.Generation != generation || c.State != hostconn.Connected
	}
	e.inboxMu.Unlock()
	if pending {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ctx.Err() == nil &&
		e.conn.Generation == generation &&
		e.rev == rev &&
		e.connectedLocked() &&
		e.phase != phaseAbsent &&
		!e.frozen
}

func (e *entry) run() {
	e.handleInputsChanged()
	done := make(chan struct{}, 1)
	working := false
	for {
		if e.drainInbox() {
			return
		}
		e.reconcileInputs()
		act, wait := e.plan()
		if act && !working {
			working = true
			go func() { e.r.decide(e); done <- struct{}{} }()
		}
		if wait > 0 && !working {
			select {
			case <-e.ctx.Done():
				e.finishClose()
				return
			case <-e.wake:
			case <-done:
				working = false
			case <-e.r.cfg.Clock.After(wait):
			}
		} else {
			select {
			case <-e.ctx.Done():
				e.finishClose()
				return
			case <-e.wake:
			case <-done:
				working = false
			}
		}
	}
}

// reconcileInputs applies the decision the RECORDS answer, whatever the
// transport is doing: a host set to raw is unavailable through a pause and
// after it, not merely while connected. Outcome-derived absence is left alone;
// only a new revision clears it.
func (e *entry) reconcileInputs() {
	e.mu.Lock()
	if !e.haveConn || e.closed {
		e.mu.Unlock()
		return
	}
	if e.phase == phaseAbsent && outcomeAbsent(e.reason) {
		e.mu.Unlock()
		return
	}
	reason, action, allowed := inputVerdict(e.mode, e.consentKnown, e.consent)
	var closeClient *client.Client
	if allowed {
		if e.phase == phaseAbsent {
			e.phase = phaseDeciding
			e.reason = ""
			e.action = ActionNone
		}
	} else if e.phase != phaseAbsent || e.reason != reason || e.action != action {
		e.phase = phaseAbsent
		e.reason = reason
		e.action = action
		if e.workCancel != nil {
			e.workCancel()
		}
		closeClient = e.detachClientLocked()
	}
	_, changed := e.publishLocked()
	e.mu.Unlock()
	if closeClient != nil {
		e.closePeer(closeClient)
	}
	if changed {
		e.requestPublication()
	}
}

func inputVerdict(mode hostmode.Mode, consentKnown bool, answer consent.Answer) (Reason, Action, bool) {
	switch mode {
	case hostmode.Raw:
		return ReasonModeRaw, ActionHostSettings, false
	case hostmode.Script:
		return ReasonModeScript, ActionHostSettings, false
	case hostmode.Helper:
		// The one mode that may carry the helper; consent decides below.
	case hostmode.Auto:
		// Auto delegates mode choice to the recorded consent, matching
		// consent.forMachine: a granted answer enables the helper.
		if !consentKnown {
			return ReasonConsentPending, ActionNone, false
		}
		if answer != consent.Granted {
			return ReasonConsentDenied, ActionNone, false
		}
		return "", ActionNone, true
	default:
		// An unrecognised record fails closed, as hostmode.Mode's own gate
		// does: nothing is written to a host whose answer nobody understands.
		return ReasonModeRaw, ActionHostSettings, false
	}
	if !consentKnown {
		return ReasonConsentPending, ActionNone, false
	}
	if answer != consent.Granted {
		return ReasonConsentDenied, ActionNone, false
	}
	return "", ActionNone, true
}

func outcomeAbsent(reason Reason) bool {
	switch reason {
	case ReasonUnsupportedPlatform, ReasonInstallFailed, ReasonExecForbidden, ReasonVersionMismatch:
		return true
	}
	return false
}

// plan answers whether a step may run now, or how long to wait for the retry
// timer. Zero duration with act=false means "wait for a wake".
func (e *entry) plan() (bool, time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || !e.haveConn || e.phase == phaseAbsent {
		return false, 0
	}
	if e.frozen {
		return false, 0
	}
	if !e.connectedLocked() || e.phase == phaseServing {
		return false, 0
	}
	now := time.Duration(e.r.cfg.Clock.Now())
	if e.nextAttemptAt != 0 && now < e.nextAttemptAt {
		return false, e.nextAttemptAt - now
	}
	if e.installed == nil {
		// The probe is not gated by C9; only the install is. A probe already
		// answered for this generation and revision is reused, so a shut gate
		// costs a wait rather than a second remote command.
		if e.platform != nil && e.probedGen == e.conn.Generation && e.probedRev == e.rev {
			if e.conn.Slow || e.conn.Shells <= 0 {
				return false, 0
			}
			return true, 0
		}
		return true, 0
	}
	return true, 0
}

func (e *entry) advance() {
	if e.r.cfg.Probe == nil || e.r.cfg.Install == nil || e.r.cfg.Dial == nil {
		e.setAbsent(ReasonExecForbidden, ActionNone)
		return
	}
	e.mu.Lock()
	generation := e.conn.Generation
	rev := e.rev
	installed := e.installed
	platform := e.platform
	probed := platform != nil && e.probedGen == generation && e.probedRev == rev
	e.mu.Unlock()
	if installed == nil {
		if probed {
			e.installOnly(generation, rev, *platform)
			return
		}
		e.probeAndInstall(generation, rev)
		return
	}
	e.dial(generation, rev, *installed)
}

// probeAndInstall is one decision's remote half. The install is the second
// remote effect and C9's gate is re-checked immediately before it, because the
// probe is allowed to run while the transport is still grading itself slow but
// the write is not.
func (e *entry) probeAndInstall(generation, rev uint64) {
	workCtx, done := e.beginWork()
	defer done()
	target := e.target()
	platform, err := e.r.cfg.Probe.Probe(workCtx, target)
	if err != nil {
		e.finishProbeError(generation, rev, err)
		return
	}
	if !e.checkInputs(rev) || !e.stillCurrent(generation, rev) {
		return
	}
	installed, found, err := e.r.cfg.Install.Find(workCtx, target, platform)
	if err != nil {
		e.finishReadError(generation, rev)
		return
	}
	if !e.checkInputs(rev) || !e.stillCurrent(generation, rev) {
		return
	}
	if found {
		e.mu.Lock()
		if e.phase != phaseAbsent && !e.frozen && e.conn.Generation == generation && e.rev == rev && e.connectedLocked() {
			e.platform = &platform
			e.probedGen = generation
			e.probedRev = rev
			e.installed = &installed
		}
		e.mu.Unlock()
		e.notify()
		return
	}
	e.mu.Lock()
	if e.phase != phaseAbsent && !e.frozen && e.conn.Generation == generation && e.rev == rev && e.connectedLocked() {
		e.platform = &platform
		e.probedGen = generation
		e.probedRev = rev
	}
	e.mu.Unlock()
	e.installOnly(generation, rev, platform)
}

// installOnly is C9's gated half: the probe answered, and the write waits for a
// sound transport and a shell.
func (e *entry) installOnly(generation, rev uint64, platform Platform) {
	if !e.gateOpen() {
		return
	}
	workCtx, done := e.beginWork()
	defer done()
	target := e.target()
	if !e.checkInputs(rev) || !e.stillCurrent(generation, rev) {
		return
	}
	installed, err := e.r.cfg.Install.Install(workCtx, target, platform)
	if err != nil {
		e.finishInstallError(generation, rev, err)
		return
	}
	// THE LATE ANSWER IS DROPPED BY THE REVISION, not stored and later
	// re-dialled: a mode that changed while the install ran is a new decision,
	// and believing this result would make the old one stand.
	if !e.stillCurrent(generation, rev) {
		return
	}
	e.mu.Lock()
	if e.phase != phaseAbsent {
		e.installed = &installed
		e.platform = nil
	}
	e.mu.Unlock()
	e.notify()
}

// dial is the step every subsequent generation takes: the build is known, so
// this only opens a lane and completes the hello. It is also the redial, which
// is why Helper-lost carries refresh and never re-runs the decision.
func (e *entry) dial(generation, rev uint64, installed Installed) {
	workCtx, done := e.beginWork()
	defer done()
	target := e.target()
	peer, err := e.r.cfg.Dial.Dial(workCtx, target, installed)
	if err != nil {
		e.finishDialError(generation, rev, err)
		return
	}
	if !e.checkInputs(rev) || !e.stillCurrent(generation, rev) {
		e.closePeer(peer)
		return
	}
	e.serve(generation, rev, peer)
}

func (e *entry) serve(generation, rev uint64, peer *client.Client) {
	e.mu.Lock()
	if e.closed || e.ctx.Err() != nil || e.conn.Generation != generation ||
		e.rev != rev || !e.connectedLocked() || e.phase == phaseAbsent || e.frozen {
		e.mu.Unlock()
		e.closePeer(peer)
		return
	}
	old := e.detachClientLocked()
	servingCtx, cancel := context.WithCancel(e.ctx)
	e.client = peer
	e.servingCtx = servingCtx
	e.servingCancel = cancel
	e.phase = phaseServing
	e.reason = ""
	e.action = ActionNone
	e.everServed = true
	e.attempt = 0
	e.nextAttemptAt = 0
	_, changed := e.publishLocked()
	e.mu.Unlock()
	if old != nil {
		e.closePeer(old)
	}
	if changed {
		e.requestPublication()
	}
	go e.watchClient(peer, servingCtx)
	e.notify()
}

// watchClient reports a lost daemon. The serving context ends when this side
// deliberately leaves serving, and that arm is what keeps a close we caused
// from being reported as helper-lost.
func (e *entry) watchClient(peer *client.Client, servingCtx context.Context) {
	select {
	case <-peer.Done():
		e.clientLost(peer)
	case <-servingCtx.Done():
	case <-e.ctx.Done():
	}
}

func (e *entry) handleClientLost(peer *client.Client) {
	e.mu.Lock()
	if e.client != peer {
		e.mu.Unlock()
		return
	}
	e.client = nil
	if e.servingCancel != nil {
		e.servingCancel()
		e.servingCancel = nil
	}
	if e.phase == phaseServing {
		if e.connectedLocked() {
			e.phase = phaseBroken
			e.reason = ReasonHelperLost
			e.action = ActionRefresh
			e.attempt = 0
			e.nextAttemptAt = time.Duration(e.r.cfg.Clock.Now()) + redialDelay(0)
		} else {
			e.phase = phaseDeciding
			e.reason = ""
			e.action = ActionNone
		}
	}
	_, changed := e.publishLocked()
	e.mu.Unlock()
	if changed {
		e.requestPublication()
	}
	e.notify()
}

func (e *entry) finishReadError(generation, rev uint64) {
	if !e.stillCurrent(generation, rev) {
		return
	}
	e.mu.Lock()
	if e.ctx.Err() == nil && e.conn.Generation == generation && e.rev == rev && e.connectedLocked() {
		e.phase = phaseDeciding
		e.nextAttemptAt = time.Duration(e.r.cfg.Clock.Now()) + redialDelay(e.attempt)
		e.attempt++
	}
	e.mu.Unlock()
	e.notify()
}

func (e *entry) finishProbeError(generation, rev uint64, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	if !e.stillCurrent(generation, rev) {
		return
	}
	switch {
	case errors.Is(err, ErrUnsupportedPlatform):
		e.setAbsent(ReasonUnsupportedPlatform, ActionNone)
	case errors.Is(err, ErrExecForbidden):
		e.setAbsent(ReasonExecForbidden, ActionNone)
	default:
		e.setAbsent(ReasonInstallFailed, ActionNone)
	}
}

func (e *entry) finishInstallError(generation, rev uint64, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	if !e.stillCurrent(generation, rev) {
		return
	}
	switch {
	case errors.Is(err, ErrUnsupportedPlatform):
		e.setAbsent(ReasonUnsupportedPlatform, ActionNone)
	case errors.Is(err, ErrExecForbidden):
		e.setAbsent(ReasonExecForbidden, ActionNone)
	default:
		e.setAbsent(ReasonInstallFailed, ActionNone)
	}
}

func (e *entry) finishDialError(generation, rev uint64, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	e.mu.Lock()
	current := e.ctx.Err() == nil && e.conn.Generation == generation && e.rev == rev
	connected := e.connectedLocked()
	served := e.everServed
	e.mu.Unlock()
	if !current {
		return
	}
	if served {
		e.markBroken()
		return
	}
	switch {
	case errors.Is(err, ErrVersionMismatch):
		e.setAbsent(ReasonVersionMismatch, ActionNone)
	case errors.Is(err, ErrExecForbidden):
		e.setAbsent(ReasonExecForbidden, ActionNone)
	case !connected:
		// The transport left while this dial was in flight: the axis is
		// Paused, not broken. A redial belongs to the return.
		e.setPhase(phaseDeciding)
	default:
		if served {
			e.markBroken()
			return
		}
		e.setAbsent(ReasonInstallFailed, ActionNone)
	}
}

// markBroken schedules the next redial and publishes helper-lost with refresh.
func (e *entry) markBroken() {
	e.mu.Lock()
	if e.closed || e.phase == phaseAbsent || e.frozen {
		e.mu.Unlock()
		return
	}
	e.phase = phaseBroken
	e.reason = ReasonHelperLost
	e.action = ActionRefresh
	e.attempt++
	e.nextAttemptAt = time.Duration(e.r.cfg.Clock.Now()) + redialDelay(e.attempt)
	_, changed := e.publishLocked()
	e.mu.Unlock()
	if changed {
		e.requestPublication()
	}
	e.notify()
}

func (e *entry) setAbsent(reason Reason, action Action) {
	e.mu.Lock()
	if e.phase == phaseAbsent && e.reason == reason && e.action == action {
		e.mu.Unlock()
		return
	}
	e.phase = phaseAbsent
	e.reason = reason
	e.action = action
	if e.workCancel != nil {
		e.workCancel()
	}
	closeClient := e.detachClientLocked()
	_, changed := e.publishLocked()
	e.mu.Unlock()
	if closeClient != nil {
		e.closePeer(closeClient)
	}
	if changed {
		e.requestPublication()
	}
	e.notify()
}

func (e *entry) setPhase(p phase) {
	e.mu.Lock()
	if e.closed || e.phase == phaseAbsent || e.phase == p {
		e.mu.Unlock()
		return
	}
	e.phase = p
	e.reason = ""
	e.action = ActionNone
	_, changed := e.publishLocked()
	e.mu.Unlock()
	if changed {
		e.requestPublication()
	}
	e.notify()
}

// refresh redials now: it resets the schedule and wakes the loop without
// touching installed. An absent axis, or one with no build, is left alone —
// refresh never installs.
func (e *entry) handleRefresh() {
	e.mu.Lock()
	if e.closed || e.phase == phaseAbsent || e.installed == nil || e.frozen || !e.connectedLocked() {
		e.mu.Unlock()
		return
	}
	e.attempt = 0
	e.nextAttemptAt = time.Duration(e.r.cfg.Clock.Now())
	e.mu.Unlock()
	e.notify()
}

// freeze is D25's window: close the client and refuse to dial until thaw. The
// axis reads helper-lost without a button, because the only action that would
// help is the uninstall that is already running.
func (e *entry) handleFreeze() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.frozen = true
	if e.workCancel != nil {
		e.workCancel()
	}
	closeClient := e.detachClientLocked()
	if e.phase != phaseAbsent {
		e.phase = phaseDeciding
		e.reason = ""
		e.action = ActionNone
	}
	_, changed := e.publishLocked()
	e.mu.Unlock()
	if closeClient != nil {
		e.closePeer(closeClient)
	}
	if changed {
		e.requestPublication()
	}
	e.notify()
}

// thaw releases freeze and re-reads the records. An uninstall that revoked
// consent therefore answers consent-pending, and installs nothing.
func (e *entry) handleThaw() {
	e.mu.Lock()
	e.frozen = false
	_, changed := e.publishLocked()
	e.mu.Unlock()
	if changed {
		e.requestPublication()
	}
	e.handleInputsChanged()
	e.notify()
}

// detachClientLocked takes the client away from the entry and cancels the
// serving lifetime. It answers the client for the caller to close outside the
// lock. Called with the mutex held.
func (e *entry) detachClientLocked() *client.Client {
	peer := e.client
	e.client = nil
	if e.servingCancel != nil {
		e.servingCancel()
		e.servingCancel = nil
		e.servingCtx = nil
	}
	if peer != nil && e.phase == phaseServing {
		e.phase = phaseDeciding
		e.reason = ""
		e.action = ActionNone
	}
	return peer
}

// publishLocked records the current status, tells Acquire's waiters when it
// changed, and answers the status and whether it changed. Called with the mutex
// held; the caller publishes outside it.
func (e *entry) publishLocked() (Status, bool) {
	status := e.statusLocked()
	if status == e.lastStatus {
		return status, false
	}
	e.lastStatus = status
	close(e.ready)
	e.ready = make(chan struct{})
	return status, true
}

func (e *entry) statusLocked() Status {
	if e.frozen && e.phase != phaseAbsent {
		return Status{State: StateUnavailable, Reason: ReasonHelperLost, Action: ActionNone}
	}
	switch e.phase {
	case phaseServing:
		if e.connectedLocked() {
			return Status{State: StateServing, Action: ActionNone}
		}
		return Status{State: StatePaused, Reason: ReasonConnection, Action: ActionNone}
	case phaseAbsent:
		return Status{State: StateUnavailable, Reason: e.reason, Action: e.action}
	case phaseBroken:
		if e.connectedLocked() {
			return Status{State: StateUnavailable, Reason: ReasonHelperLost, Action: ActionRefresh}
		}
		return Status{State: StatePaused, Reason: ReasonConnection, Action: ActionNone}
	default:
		if e.connectedLocked() {
			return Status{State: StateStarting, Action: ActionNone}
		}
		return Status{State: StatePaused, Reason: ReasonConnection, Action: ActionNone}
	}
}

func (e *entry) notify() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *entry) target() Target {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.targetLocked()
}

func (e *entry) gateOpen() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.connectedLocked() && !e.conn.Slow && e.conn.Shells > 0 && !e.frozen
}

func (e *entry) beginWork() (context.Context, func()) {
	ctx, cancel := context.WithCancel(e.ctx)
	e.inboxMu.Lock()
	e.interruptCancel = cancel
	if e.inbox.close || e.inbox.inputs != 0 || e.inbox.interrupted || e.inbox.freeze != 0 {
		cancel()
	}
	e.inboxMu.Unlock()
	e.mu.Lock()
	e.workCancel = cancel
	e.mu.Unlock()
	return ctx, func() {
		cancel()
		e.inboxMu.Lock()
		e.interruptCancel = nil
		e.inboxMu.Unlock()
		e.mu.Lock()
		e.workCancel = nil
		e.mu.Unlock()
	}
}

// close ends the entry: no more decisions, no client, and Acquire's waiters
// released. It answers the last status so the registry can publish a final
// frame.
func (e *entry) finishClose() (Status, bool) {
	e.mu.Lock()
	if e.closed {
		status := e.statusLocked()
		e.mu.Unlock()
		return status, false
	}
	e.closed = true
	if e.workCancel != nil {
		e.workCancel()
	}
	peer := e.detachClientLocked()
	// The axis is gone with the connection. An absent reason is still the
	// truth the last decision reached, so it is kept; a serving or starting
	// one is reported as the connection it lost.
	status := Status{State: StateUnavailable, Reason: ReasonConnection, Action: ActionNone}
	changed := true
	if e.phase == phaseAbsent {
		status = Status{State: StateUnavailable, Reason: e.reason, Action: e.action}
	}
	e.lastStatus = status
	close(e.ready)
	e.ready = make(chan struct{})
	e.cancel()
	e.mu.Unlock()
	if peer != nil {
		_ = peer.Close()
	}
	return status, changed
}
