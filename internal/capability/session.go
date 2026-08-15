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
	// Open creates a new session (the open handler's registry half).
	Open(ctx context.Context, cfg session.Config) (session.Session, error)
	// LastUsedForProfiles answers persisted last-used timestamps
	// (sessions.status). An unwired tracker answers an empty map.
	LastUsedForProfiles(profileIDs []string) (map[string]time.Time, error)
}

// SessionOperation is the typed operation for the session domain. Its gate
// is [session]. The operation is scoped by id through SessionOperations.
type SessionOperation interface {
	Run(context.Context, func(context.Context, SessionService) error) error
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
// then releases that gate before running its callback while retaining the
// ordinary execution lane. Slow remote I/O therefore stays bounded without
// blocking resize, close, or another session lookup.
type SessionTargetOperation interface {
	Run(context.Context, func(context.Context, SessionTarget) error) error
}

type sessionTargetOperation struct {
	sessionGate control.Admission
	lane        control.Admission
	registry    session.Registry
	id          session.ID
}

// SessionOperations builds per-session operations. The KIND of resource is
// compile-time (a SessionOperation can only reach sessions); the id is
// runtime. ForSession returns an error for an unknown id and never nil — a
// nil handle is not enforcement. A session can close between ForSession and
// the operation's Run; the per-call Get inside the callback then errors.
type SessionOperations struct {
	sessionGate control.Admission
	lane        control.Admission
	registry    session.Registry
	usage       session.ProfileUsageTracker
}

// NewSessionOperations wires the per-session factory. usage may be nil: an
// unwired tracker answers an empty last-used map, exactly as the transport
// handles a nil tracker today.
func NewSessionOperations(sessionGate, lane control.Admission, registry session.Registry, usage session.ProfileUsageTracker) *SessionOperations {
	return &SessionOperations{sessionGate: sessionGate, lane: lane, registry: registry, usage: usage}
}

// ForSession returns a SessionOperation scoped to id, or an error when the
// registry holds no session with that id. Never nil on success.
func (f *SessionOperations) ForSession(id session.ID) (SessionOperation, error) {
	if _, err := f.registry.Get(id); err != nil {
		return nil, fmt.Errorf("capability: unknown session %q", id)
	}
	g := &guard{}
	return newOperation[SessionService](control.NewComposite(f.sessionGate, f.lane), g, newSessionService(g, f.registry, f.usage)), nil
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
		lane:        f.lane,
		registry:    f.registry,
		id:          id,
	}, nil
}

func (op *sessionTargetOperation) Run(ctx context.Context, fn func(context.Context, SessionTarget) error) error {
	sessionPermit, rej := op.sessionGate.TryAcquire(ctx)
	if rej != nil {
		return &RefusedError{Rejection: *rej}
	}
	defer sessionPermit.Release()

	// Preserve canonical order: conflict admission before the scarce lane.
	// The lane remains held for the callback; only the session gate ends
	// after the immutable facts have been copied.
	lanePermit, rej := op.lane.TryAcquire(ctx)
	if rej != nil {
		return &RefusedError{Rejection: *rej}
	}
	defer lanePermit.Release()

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
func NewSessionOperation(sessionGate, lane control.Admission, registry session.Registry, usage session.ProfileUsageTracker) SessionOperation {
	g := &guard{}
	return newOperation[SessionService](control.NewComposite(sessionGate, lane), g, newSessionService(g, registry, usage))
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

func (s *sessionService) Open(ctx context.Context, cfg session.Config) (session.Session, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.registry.Open(ctx, cfg)
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
