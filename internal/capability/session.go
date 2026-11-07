package capability

import (
	"context"
	"fmt"
	"time"

	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport/control"
)

// SessionService is the session domain surface. It is what a SessionOperation
// hands its callback. Read policy: reads participate in the session gate —
// one registry, and a close must not interleave an open of the same id.
type SessionService interface {
	// Get resolves one session by id. Errors for an unknown id.
	Get(id session.ID) (session.Session, error)
	// Close tears down one session.
	Close(id session.ID) error
	// List returns every live session (sessions.status, attach addressing).
	List() []session.Session
	// LastUsedForProfiles answers persisted last-used timestamps
	// (sessions.status). An unwired tracker answers an empty map.
	LastUsedForProfiles(profileIDs []string) (map[string]time.Time, error)
}

// SessionOperation is the typed operation for the session domain. Its gate
// is [session]. The operation is scoped by id through SessionOperations.
type SessionOperation interface {
	AssistantOperation
	Run(context.Context, func(context.Context, SessionService) error) error
}

// SessionEndOperation retires one authoritative ID under the session gate, and
// releases it at retirement. Before runs after
// retirement and before teardown, so transport can mark the expected exit and
// cancel the session's resize lane without exposing the ID to another attach.
// A non-nil returned session means retirement committed even if teardown failed.
//
// The session gate's interval ends at the retirement commit, not at process
// completion. A teardown can wait on a helper or an ssh channel, and a gate
// held across that wait is a slow holder of the control plane: it makes
// unrelated session work wait while the session is already retired. The close
// still does not RETURN until teardown settles, so its caller still learns the
// teardown's outcome.
type SessionEndOperation interface {
	AssistantOperation
	Run(context.Context, func(session.Session)) (session.Session, error)
}

type sessionEndOperation struct {
	sessionGate control.Admission
	registry    session.Registry
	id          session.ID
}

// SessionTarget is the immutable routing snapshot a long-lived auxiliary
// operation may use after releasing the session gate. SSHOptions is copied
// from the live session so auxiliary SSH channels resolve through the exact
// route and pooled identity the terminal used (AD-4).
type SessionTarget struct {
	Kind       session.Kind
	Host       string
	SSHOptions []ssh.ConnectOption
}

// SessionTargetOperation snapshots one live session under the session gate,
// then releases that gate before running its callback. Slow remote I/O is
// bounded by its own declared max without blocking resize, close, or another
// session lookup.
type SessionTargetOperation interface {
	AssistantOperation
	Run(context.Context, func(context.Context, SessionTarget) error) error
}

type sessionTargetOperation struct {
	sessionGate control.Admission
	registry    session.Registry
	id          session.ID
	disposition Disposition
}

// SessionOperations builds per-session operations. The KIND of resource is
// compile-time (a SessionOperation can only reach sessions); the id is
// runtime. ForSession returns an error for an unknown id and never nil — a
// nil handle is not enforcement. A session can close between ForSession and
// the operation's Run; the per-call Get inside the callback then errors.
type SessionOperations struct {
	sessionGate control.Admission
	registry    session.Registry
	usage       session.ProfileUsageTracker
}

// NewSessionOperations wires the per-session factory. usage may be nil: an
// unwired tracker answers an empty last-used map, exactly as the transport
// handles a nil tracker today.
func NewSessionOperations(sessionGate control.Admission, registry session.Registry, usage session.ProfileUsageTracker) *SessionOperations {
	return &SessionOperations{sessionGate: sessionGate, registry: registry, usage: usage}
}

// ForSession returns a SessionOperation scoped to id, or an error when the
// registry holds no session with that id. Never nil on success.
func (f *SessionOperations) ForSession(id session.ID) (SessionOperation, error) {
	if _, err := f.registry.Get(id); err != nil {
		return nil, fmt.Errorf("capability: unknown session %q", id)
	}
	g := &guard{}
	return newOperation[SessionService](Adapted("terminal.resize", "session operations enforce assistant-owned session identity"), f.sessionGate, g, newSessionService(g, f.registry, f.usage)), nil
}

// ForSessionEnd builds the terminal operation for one live session. Ordinary
// SessionOperation cannot perform this work: its gate spans the entire callback,
// whereas a helper's close round trip must not exclude unrelated sessions.
func (f *SessionOperations) ForSessionEnd(id session.ID) (SessionEndOperation, error) {
	if _, err := f.registry.Get(id); err != nil {
		return nil, fmt.Errorf("capability: unknown session %q", id)
	}
	return &sessionEndOperation{
		sessionGate: f.sessionGate,
		registry:    f.registry,
		id:          id,
	}, nil
}

func (op *sessionEndOperation) Disposition() Disposition {
	return Adapted("terminal.close", "session closure retires its authoritative identity before ending the process")
}

func (op *sessionEndOperation) Run(ctx context.Context, before func(session.Session)) (sess session.Session, err error) {
	// Queued tasks still run during shutdown to settle their callers, but an
	// uncommitted close has no authority after cancellation. Admissions may
	// take their free fast path without consulting ctx, so check each handoff.
	refuseCancelled := func() error {
		if ctx.Err() != nil {
			rej := control.Rejection{Scope: GateSession, Reason: ctx.Err().Error()}
			control.ReportRefusal(ctx, rej)
			return &RefusedError{Rejection: rej}
		}
		return nil
	}
	if cancelled := refuseCancelled(); cancelled != nil {
		return nil, cancelled
	}
	sessionPermit, rej := op.sessionGate.TryAcquire(ctx)
	if rej != nil {
		return nil, refused(ctx, rej)
	}
	defer sessionPermit.Release()
	if control.HoldsOrder(op.sessionGate) {
		ctx = control.WithOrderHeld(ctx)
	}
	if cancelled := refuseCancelled(); cancelled != nil {
		return nil, cancelled
	}

	sess, finish, err := op.registry.PrepareEndSession(op.id)
	if err != nil {
		return nil, err
	}
	// Retirement is the commit point, and it is where the gate's interval
	// closes: before it the close may still be refused and leave the session
	// live; after it the identity is gone and nothing can conflict with it.
	// The teardown below may wait on a helper or an ssh channel, and holding
	// the session gate across that wait refuses unrelated session work while
	// the session is already retired. The deferred release remains the
	// failure path's answer; Release is idempotent.
	sessionPermit.Release()
	// Cancellation or a failing callback may not strand the removed process;
	// completion still runs before this call returns, so the caller is told
	// the teardown's outcome.
	defer func() { err = finish() }()
	before(sess)
	return sess, nil
}

// ForSessionTarget returns a staged read operation for immutable connection
// facts. It differs from ForSession deliberately: the callback receives no
// registry service and runs after the session gate is released, so it cannot
// mutate or re-read session state outside the protected interval.
func (f *SessionOperations) ForSessionTarget(id session.ID) (SessionTargetOperation, error) {
	if _, err := f.registry.Get(id); err != nil {
		return nil, fmt.Errorf("capability: unknown session %q", id)
	}
	return &sessionTargetOperation{
		sessionGate: f.sessionGate,
		registry:    f.registry,
		id:          id,
		disposition: Adapted("terminal.reattach", "session targets require an assistant-owned session scope"),
	}, nil
}

func (op *sessionTargetOperation) Disposition() Disposition {
	return op.disposition
}

func (op *sessionTargetOperation) Run(ctx context.Context, fn func(context.Context, SessionTarget) error) error {
	sessionPermit, rej := op.sessionGate.TryAcquire(ctx)
	if rej != nil {
		return refused(ctx, rej)
	}
	defer sessionPermit.Release()
	if control.HoldsOrder(op.sessionGate) {
		ctx = control.WithOrderHeld(ctx)
	}

	sess, err := op.registry.Get(op.id)
	if err != nil {
		return err
	}
	target := SessionTarget{
		Kind:       sess.Kind(),
		Host:       sess.Host(),
		SSHOptions: append([]ssh.ConnectOption(nil), sess.SSHOptions()...),
	}
	sessionPermit.Release()
	return fn(ctx, target)
}

// NewSessionOperation builds a single SessionOperation — for handlers whose
// operation is fixed at construction (sessions.status, and the session
// half of open) rather than keyed by a per-request id.
func NewSessionOperation(sessionGate control.Admission, registry session.Registry, usage session.ProfileUsageTracker) SessionOperation {
	g := &guard{}
	return newOperation[SessionService](Adapted("terminal.resize", "session operations enforce assistant-owned session identity"), control.NewComposite(sessionGate), g, newSessionService(g, registry, usage))
}

// newSessionService builds the concrete session service bound to guard g.
func newSessionService(g *guard, registry session.Registry, usage session.ProfileUsageTracker) *sessionService {
	return &sessionService{guard: g, registry: registry, usage: usage}
}

type sessionService struct {
	guard    *guard
	registry session.Registry
	usage    session.ProfileUsageTracker
}

func (s *sessionService) Get(id session.ID) (session.Session, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.registry.Get(id)
}

func (s *sessionService) Close(id session.ID) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.registry.Close(id)
}

func (s *sessionService) List() []session.Session {
	if !s.guard.ok() {
		return nil
	}
	return s.registry.List()
}

func (s *sessionService) LastUsedForProfiles(profileIDs []string) (map[string]time.Time, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	if s.usage == nil {
		return map[string]time.Time{}, nil
	}
	return s.usage.LastUsedForProfiles(profileIDs)
}
