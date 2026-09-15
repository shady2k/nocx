package capability

import (
	"context"

	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport/control"
)

// ProfileResolver maps a profile ID to an SSH host and connect config. It
// is the resolver seam the open flow uses; the composition root wires it
// from the profile service (the transport's ProfileResolver, adapted).
// Passwords are never carried in the returned config — they are late-bound
// via the credential store wired into ConnectConfig, so the resolver reads
// the vault internally and the operation's gates cannot see inside it.
type ProfileResolver interface {
	Resolve(profileID string) (host string, cfg *ssh.ConnectConfig, err error)
}

// OpenService is the session-open surface: resolve the profile, and clean up on
// failure. It is what an OpenOperation hands its callback.
//
// IT NO LONGER OPENS AN SSH SESSION, and that distinction is the whole of
// nocx-50w7p.5's change to this file. `Open` used to reach the session registry,
// which then dialed an ssh destination FROM THIS PROCESS whenever no helper
// claimed it — the Tier A fallback ADR-0057 refuses. An ssh pane is opened by a
// helper now (this machine's, or the far host's own when it has one), and
// session_open.go refuses by name rather than reaching for this method, so no
// caller can drive the coordinator's dial through it.
//
// What it still reaches is the registry's LOCAL arm, which forks a process
// rather than connecting to a host, and which the registry itself documents as a
// seam for a test that legitimately supplies a channel. That is why this method
// survived the deletion that removed its sibling on SessionService: deleting it
// too took out a local PTY seam the test suite depends on, and a suite that
// cannot open a pane has nothing to assert about panes. The two are separated by
// the destination kind, and that separation is enforced at the call site rather
// than here.
//
// What remains alongside it is the half that must stay: resolving a profile is a
// store and vault read, and it is the half the gates below were refined around.
//
// The grain is REFINED (the refinement open.go's own comment used to defer):
// the resolve runs under the [config, session] gates and the dial runs under
// none. Holding a whole-domain gate across the dial is the conservative
// grain, and it cost the product exactly what the doc predicted it might: an
// ssh handshake takes seconds, and a handshake that stops to ask a human for
// a password takes as long as the human does. Every other pane's open, and
// every config, git and files request, waits one second on the held gate and
// is then refused — the renderer says "The terminal is busy — that action was
// refused", and a restored workspace comes back with one live pane and the
// rest dead. Resolving is store and vault reads, measured in microseconds;
// that is the part worth excluding, and it is now the only part that is.
type OpenService interface {
	Resolve(profileID string) (host string, cfg *ssh.ConnectConfig, err error)
	// Open reaches the session registry, and since nocx-50w7p.5 its ONLY caller
	// is a LOCAL destination in a build that wired no helper opener — the local
	// PTY seam this repository keeps for tests (see the registry's own KindLocal
	// arm, which refuses in production and says so).
	//
	// It is NOT the ssh route any more: an ssh destination is opened by a helper
	// or refused by name, so no caller of this method can reach the coordinator's
	// dial. That is the distinction the epic turns on, and it is why this method
	// survived the deletion that removed its sibling on SessionService: a seam
	// that forks a local process is not a seam that connects to a host.
	Open(ctx context.Context, cfg session.Config) (session.Session, error)
	Close(id session.ID) error
	// EndSession is Close's sibling for a caller that knows nobody will ever
	// hold this session — an open that adopted a helper-hosted session and
	// then failed a later step of its own (nocx-isjh4). See
	// session.Reg.EndSession.
	EndSession(id session.ID) error
}

// OpenOperation is the typed operation for the "open" control method, and
// it is two-phase: Prepare resolves under [config, session], Dial opens the
// session under the execution lane alone. Run keeps the whole-open form for
// the short compensating paths (closing a session whose ring could not be
// built), which touch the registry and nothing slow.
//
// The canonical acquisition order survives the split: phase one takes
// config then session and releases both, phase two takes the lane. No
// operation ever holds the lane while waiting for a domain gate, which is
// the inversion the order exists to forbid.
type OpenOperation interface {
	AssistantOperation
	// Prepare runs fn under the [config, session] conflict gates and no
	// lane permit — the resolve is store work, not execution.
	Prepare(context.Context, func(context.Context, OpenService) error) error
	// Dial runs fn on the execution lane with no domain gate held.
	Dial(context.Context, func(context.Context, OpenService) error) error
	// Run holds [config, session] and the lane for the whole callback.
	Run(context.Context, func(context.Context, OpenService) error) error
}

// NewOpenOperation builds an OpenOperation whose phases acquire configGate
// before sessionGate (the canonical order), and the execution lane after
// both have been released.
func NewOpenOperation(
	configGate, sessionGate, lane control.Admission,
	resolver ProfileResolver,
	registry session.Registry,
) OpenOperation {
	g := &guard{}
	svc := newOpenService(g, resolver, registry)
	return &openOperation{
		prepare: newOperation[OpenService](Adapted("terminal.connect", "opening a terminal uses connection/session ownership and registration"), control.NewComposite(configGate, sessionGate), g, svc),
		dial:    newOperation[OpenService](Adapted("terminal.connect", "opening a terminal uses connection/session ownership and registration"), control.NewComposite(lane), g, svc),
		full:    newOperation[OpenService](Adapted("terminal.connect", "opening a terminal uses connection/session ownership and registration"), control.NewComposite(configGate, sessionGate, lane), g, svc),
	}
}

// openOperation is the three admissions over ONE guard and ONE service: the
// service is the same object in every phase, so a handle captured in the
// resolve is still refused outside every in-flight phase, exactly as the
// single-admission operations are.
type openOperation struct {
	prepare, dial, full *operation[OpenService]
}

func (o *openOperation) Disposition() Disposition {
	return o.full.Disposition()
}

func (o *openOperation) Prepare(ctx context.Context, fn func(context.Context, OpenService) error) error {
	return o.prepare.Run(ctx, fn)
}

func (o *openOperation) Dial(ctx context.Context, fn func(context.Context, OpenService) error) error {
	return o.dial.Run(ctx, fn)
}

func (o *openOperation) Run(ctx context.Context, fn func(context.Context, OpenService) error) error {
	return o.full.Run(ctx, fn)
}

// newOpenService builds the concrete open service bound to guard g.
func newOpenService(g *guard, resolver ProfileResolver, registry session.Registry) *openService {
	return &openService{guard: g, resolver: resolver, registry: registry}
}

type openService struct {
	guard    *guard
	resolver ProfileResolver
	registry session.Registry
}

func (s *openService) Resolve(profileID string) (string, *ssh.ConnectConfig, error) {
	if err := s.guard.check(); err != nil {
		return "", nil, err
	}
	return s.resolver.Resolve(profileID)
}

func (s *openService) Open(ctx context.Context, cfg session.Config) (session.Session, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.registry.Open(ctx, cfg)
}

func (s *openService) Close(id session.ID) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.registry.Close(id)
}

func (s *openService) EndSession(id session.ID) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.registry.EndSession(id)
}
