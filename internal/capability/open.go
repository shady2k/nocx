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

// OpenService is the session-open surface: resolve the profile, open the
// session, and clean up on failure. It is what an OpenOperation hands its
// callback.
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
	Open(ctx context.Context, cfg session.Config) (session.Session, error)
	Close(id session.ID) error
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
