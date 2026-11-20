package farhelper

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
	phaseDeciding phase = iota
	phaseServing
	phaseAbsent
	phaseBroken
	phaseCount
)

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

// decisionState is copied into transition. No context, store, clock or remote
// call is reachable from the reducer; all external facts arrive in events.
type decisionState struct {
	conn          Connection
	haveConn      bool
	phase         phase
	reason        Reason
	action        Action
	mode          hostmode.Mode
	consent       consent.Answer
	consentKnown  bool
	rev           uint64
	installed     *Installed
	platform      *Platform
	found         bool
	everServed    bool
	frozen        bool
	attempt       int
	readFailures  int
	nextAttemptAt time.Duration
	client        *client.Client
	closed        bool
	working       bool
	workID        uint64
}
type eventKind uint8

const (
	eventObserve eventKind = iota
	eventInputs
	eventFreeze
	eventThaw
	eventRefresh
	eventLost
	eventResult
	eventTick
	eventClose
	eventCount
)

type stepKind uint8

const (
	stepProbe stepKind = iota
	stepFind
	stepInstall
	stepDial
)

type inputs struct {
	mode    hostmode.Mode
	consent consent.Answer
	known   bool
}
type event struct {
	kind        eventKind
	conn        Connection
	inputs      inputs
	interrupted bool
	peer        *client.Client
	result      stepResult
	now         time.Duration
}
type stepResult struct {
	kind                     stepKind
	generation, revision, id uint64
	probedPlatform           Platform
	build                    Installed
	found                    bool
	peer                     *client.Client
	err                      error
}
type effects struct {
	close  []*client.Client
	cancel bool
	start  bool
	step   stepKind
}

// transition is the only writer of decision fields. Effects describe work for
// the entry goroutine; even closing a stale dial result belongs to that owner.
func transition(s decisionState, ev event) (decisionState, effects) {
	fx := effects{}
	retire := func() {
		if s.client != nil {
			fx.close = append(fx.close, s.client)
			s.client = nil
		}
		if s.phase == phaseServing {
			s.phase = phaseDeciding
			s.reason = ""
			s.action = ActionNone
		}
	}
	interrupt := func() { fx.cancel = true; s.working = false; s.workID++; retire() }
	absent := func(reason Reason, action Action) {
		interrupt()
		s.phase = phaseAbsent
		s.reason = reason
		s.action = action
	}
	if s.closed {
		if ev.kind == eventResult && ev.result.peer != nil {
			fx.close = append(fx.close, ev.result.peer)
		}
		return s, fx
	}
	switch ev.kind {
	case eventObserve:
		if s.haveConn && ev.conn.Seq != 0 && s.conn.Seq != 0 && (ev.conn.Seq < s.conn.Seq || (ev.conn.Seq == s.conn.Seq && ev.conn.HostKeyFingerprint == s.conn.HostKeyFingerprint)) && !ev.interrupted {
			return s, fx
		}
		generation := s.haveConn && ev.conn.Generation != s.conn.Generation
		fingerprint := s.haveConn && ev.conn.HostKeyFingerprint != s.conn.HostKeyFingerprint
		cut := s.haveConn && s.conn.State == hostconn.Connected && ev.conn.State != hostconn.Connected
		if generation || fingerprint || cut || ev.interrupted {
			interrupt()
			s.attempt = 0
			s.nextAttemptAt = 0
		}
		if generation {
			s.readFailures = 0
			s.platform = nil
			s.found = false
		}
		if fingerprint {
			// A different authenticated host is a new decision, not a redial
			// of the prior host's build or terminal outcome.
			s.installed = nil
			s.everServed = false
			if s.phase == phaseAbsent && outcomeAbsent(s.reason) {
				s.phase = phaseDeciding
				s.reason = ""
				s.action = ActionNone
			}
			s.readFailures = 0
			s.rev++
			s.platform = nil
			s.found = false
		}
		s.conn = ev.conn
		s.haveConn = true
	case eventInputs:
		if s.mode != ev.inputs.mode || s.consent != ev.inputs.consent || s.consentKnown != ev.inputs.known {
			interrupt()
			s.rev++
			s.readFailures = 0
			s.attempt = 0
			s.nextAttemptAt = 0
			s.platform = nil
			s.found = false
			s.mode = ev.inputs.mode
			s.consent = ev.inputs.consent
			s.consentKnown = ev.inputs.known
			if s.phase == phaseAbsent {
				s.phase = phaseDeciding
				s.reason = ""
				s.action = ActionNone
			}
		}
		reason, action, allowed := inputVerdict(s.mode, s.consentKnown, s.consent)
		if !allowed {
			absent(reason, action)
		} else if s.phase == phaseAbsent && !outcomeAbsent(s.reason) {
			s.phase = phaseDeciding
			s.reason = ""
			s.action = ActionNone
		}
	case eventFreeze:
		interrupt()
		s.frozen = true
	case eventThaw:
		s.frozen = false
	case eventRefresh:
		if s.phase != phaseAbsent && s.installed != nil && !s.frozen && s.conn.State == hostconn.Connected {
			s.attempt = 0
			s.nextAttemptAt = 0
		}
	case eventLost:
		if ev.peer == s.client && s.client != nil {
			retire()
			if s.conn.State == hostconn.Connected {
				s.phase = phaseBroken
				s.reason = ReasonHelperLost
				s.action = ActionRefresh
				s.attempt = 0
				s.nextAttemptAt = ev.now + redialDelay(0)
			}
		}
	case eventResult:
		r := ev.result
		// All remote results meet one generation/revision/work fence here.
		if r.generation != s.conn.Generation || r.revision != s.rev || r.id != s.workID || !s.working || s.frozen || s.phase == phaseAbsent {
			if r.peer != nil {
				fx.close = append(fx.close, r.peer)
			}
			return s, fx
		}
		s.working = false
		if s.conn.State != hostconn.Connected {
			if r.peer != nil {
				fx.close = append(fx.close, r.peer)
			}
			s.phase = phaseDeciding
			s.reason = ""
			s.action = ActionNone
			return s, fx
		}
		if r.err != nil {
			switch {
			case errors.Is(r.err, context.Canceled):
				// A store broadcasts without a key. An unrelated notification
				// can cancel work without changing this entry's revision.
				s.nextAttemptAt = 0
			case r.kind == stepDial && s.everServed:
				s.phase = phaseBroken
				s.reason = ReasonHelperLost
				s.action = ActionRefresh
				s.attempt++
				s.nextAttemptAt = ev.now + redialDelay(s.attempt)
			case errors.Is(r.err, ErrExecForbidden):
				absent(ReasonExecForbidden, ActionNone)
			case errors.Is(r.err, ErrUnsupportedPlatform):
				absent(ReasonUnsupportedPlatform, ActionNone)
			case errors.Is(r.err, ErrVersionMismatch):
				absent(ReasonVersionMismatch, ActionNone)
			case r.kind == stepProbe || r.kind == stepFind:
				s.readFailures++
				s.attempt++
				if s.readFailures >= 3 {
					absent(ReasonInstallFailed, ActionNone)
				} else {
					s.platform = nil
					s.found = false
					s.nextAttemptAt = ev.now + redialDelay(s.attempt-1)
				}
			default:
				absent(ReasonInstallFailed, ActionNone)
			}
		} else {
			switch r.kind {
			case stepProbe:
				s.platform = &r.probedPlatform
			case stepFind:
				s.found = true
				if r.found {
					s.installed = &r.build
				}
			case stepInstall:
				s.installed = &r.build
				s.platform = nil
			case stepDial:
				retire()
				s.client = r.peer
				s.phase = phaseServing
				s.reason = ""
				s.action = ActionNone
				s.everServed = true
				s.attempt = 0
				s.nextAttemptAt = 0
			}
		}
	case eventClose:
		interrupt()
		s.closed = true
	case eventTick:
		if s.phase == phaseAbsent || s.closed || s.frozen {
			return s, fx
		}
		if s.haveConn && s.conn.State == hostconn.Connected && !s.frozen && !s.working && s.phase != phaseAbsent && s.phase != phaseServing && ev.now >= s.nextAttemptAt {
			if ev.result.err != nil {
				absent(ReasonExecForbidden, ActionNone)
				return s, fx
			}
			switch {
			case s.installed != nil:
				fx.step = stepDial
			case s.platform == nil:
				fx.step = stepProbe
			case !s.found:
				fx.step = stepFind
			case !s.conn.Slow && s.conn.Shells > 0:
				fx.step = stepInstall
			default:
				return s, fx
			}
			s.working = true
			s.workID++
			fx.start = true
		}
	}
	return s, fx
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

// entry owns all effects and the published snapshot. Its mutex is held only to
// copy state and signal waiters, never through a store lookup, callback or RPC.
type entry struct {
	r  *Registry
	id string
	mu sync.Mutex
	decisionState
	inboxMu         sync.Mutex
	inbox           entryInbox
	ingress         Connection
	interruptCancel context.CancelFunc
	wake            chan struct{}
	ready           chan struct{}
	active          int
	results         chan stepResult
	lastStatus      Status
	ctx             context.Context
	cancel          context.CancelFunc
	servingCtx      context.Context
	servingCancel   context.CancelFunc
}

func newEntry(r *Registry, c Connection) *entry {
	ctx, cancel := context.WithCancel(r.ctx)
	e := &entry{r: r, id: c.ID, ingress: c, wake: make(chan struct{}, 1), ready: make(chan struct{}), results: make(chan stepResult), ctx: ctx, cancel: cancel}
	e.decisionState, _ = transition(e.decisionState, event{kind: eventObserve, conn: c})
	return e
}
func (e *entry) start() { go e.run() }
func (e *entry) fingerprint() string {
	e.inboxMu.Lock()
	defer e.inboxMu.Unlock()
	return e.ingress.HostKeyFingerprint
}
func (e *entry) connectedLocked() bool { return e.haveConn && e.conn.State == hostconn.Connected }
func (e *entry) notify() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *entry) statusLocked() Status {
	if e.closed {
		if e.phase == phaseAbsent {
			return Status{State: StateUnavailable, Reason: e.reason, Action: e.action}
		}
		return Status{State: StateUnavailable, Reason: ReasonConnection, Action: ActionNone}
	}
	if e.frozen && e.phase != phaseAbsent {
		return Status{State: StateUnavailable, Reason: ReasonHelperLost, Action: ActionNone}
	}
	switch e.phase {
	case phaseAbsent:
		return Status{State: StateUnavailable, Reason: e.reason, Action: e.action}
	case phaseBroken:
		if e.connectedLocked() {
			return Status{State: StateUnavailable, Reason: ReasonHelperLost, Action: ActionRefresh}
		}
	case phaseServing:
		if e.connectedLocked() {
			return Status{State: StateServing, Action: ActionNone}
		}
	default:
		if e.connectedLocked() {
			return Status{State: StateStarting, Action: ActionNone}
		}
	}
	return Status{State: StatePaused, Reason: ReasonConnection, Action: ActionNone}
}

func (e *entry) accept(ev event) {
	ev.now = time.Duration(e.r.cfg.Clock.Now())
	e.mu.Lock()
	previous := e.client
	next, fx := transition(e.decisionState, ev)
	e.decisionState = next
	if previous != e.client || e.closed {
		if e.servingCancel != nil {
			e.servingCancel()
			e.servingCancel = nil
			e.servingCtx = nil
		}
		if e.client != nil {
			e.servingCtx, e.servingCancel = context.WithCancel(e.ctx)
			go e.watchClient(e.client, e.servingCtx)
		}
	}
	status := e.statusLocked()
	changed := status != e.lastStatus || ev.kind == eventClose
	if changed {
		e.lastStatus = status
		close(e.ready)
		e.ready = make(chan struct{})
	}
	e.mu.Unlock()
	if fx.cancel {
		e.inboxMu.Lock()
		if e.interruptCancel != nil {
			e.interruptCancel()
			e.interruptCancel = nil
		}
		e.inboxMu.Unlock()
	}
	for _, peer := range fx.close {
		_ = peer.Close()
	}
	if changed {
		e.r.publishEntry(e, status)
	}
	if fx.start {
		e.launch(fx.step, next)
	}
}

func (e *entry) checkInputs() event {
	e.mu.Lock()
	fp := e.conn.HostKeyFingerprint
	e.mu.Unlock()
	in := inputs{mode: hostmode.Auto}
	if e.r.cfg.Modes != nil {
		if m, ok := e.r.cfg.Modes.Lookup(fp); ok {
			in.mode = m
		}
	}
	if e.r.cfg.Consents != nil {
		in.consent, in.known = e.r.cfg.Consents.Lookup(fp)
	}
	return event{kind: eventInputs, inputs: in}
}

func (e *entry) run() {
	defer func() {
		e.cancel()
		for e.active > 0 {
			result := <-e.results
			e.active--
			if result.peer != nil {
				_ = result.peer.Close()
			}
		}
	}()
	e.accept(e.checkInputs())
	for {
		if e.drainInbox() {
			return
		}
		e.accept(e.checkInputs())
		e.r.decide(e)
		e.mu.Lock()
		wait := e.nextAttemptAt - time.Duration(e.r.cfg.Clock.Now())
		working := e.working
		e.mu.Unlock()
		if wait > 0 && !working {
			select {
			case <-e.ctx.Done():
				e.accept(event{kind: eventClose})
				return
			case <-e.wake:
			case result := <-e.results:
				if e.receiveResult(result) {
					return
				}
			case <-e.r.cfg.Clock.After(wait):
			}
			continue
		}
		select {
		case <-e.ctx.Done():
			e.accept(event{kind: eventClose})
			return
		case <-e.wake:
		case result := <-e.results:
			if e.receiveResult(result) {
				return
			}
		}
	}
}

func (e *entry) receiveResult(result stepResult) bool {
	e.active--
	if e.drainInbox() {
		if result.peer != nil {
			_ = result.peer.Close()
		}
		return true
	}
	e.accept(e.checkInputs())
	e.accept(event{kind: eventResult, result: result})
	return false
}

func (e *entry) advance() {
	ev := event{kind: eventTick}
	if e.r.cfg.Probe == nil || e.r.cfg.Install == nil || e.r.cfg.Dial == nil {
		ev.result.err = ErrExecForbidden
	}
	e.accept(ev)
}

func (e *entry) launch(kind stepKind, s decisionState) {
	e.active++
	ctx, cancel := context.WithCancel(e.ctx)
	e.inboxMu.Lock()
	e.interruptCancel = cancel
	if e.inbox.close || e.inbox.inputs != 0 || e.inbox.interrupted || e.inbox.freeze != 0 {
		cancel()
	}
	e.inboxMu.Unlock()
	target := Target{ConnectionID: e.id, Destination: s.conn.Destination, HostKeyFingerprint: s.conn.HostKeyFingerprint, Generation: s.conn.Generation}
	go func() {
		defer cancel()
		result := stepResult{kind: kind, generation: s.conn.Generation, revision: s.rev, id: s.workID}
		if ctx.Err() != nil {
			result.err = ctx.Err()
		} else {
			switch kind {
			case stepProbe:
				result.probedPlatform, result.err = e.r.cfg.Probe.Probe(ctx, target)
			case stepFind:
				result.build, result.found, result.err = e.r.cfg.Install.Find(ctx, target, *s.platform)
			case stepInstall:
				result.build, result.err = e.r.cfg.Install.Install(ctx, target, *s.platform)
			case stepDial:
				result.peer, result.err = e.r.cfg.Dial.Dial(ctx, target, *s.installed)
			}
		}
		e.results <- result
	}()
}

func (e *entry) watchClient(peer *client.Client, ctx context.Context) {
	select {
	case <-peer.Done():
		e.clientLost(peer)
	case <-ctx.Done():
	case <-e.ctx.Done():
	}
}
