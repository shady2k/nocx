package capability

import (
	"context"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/git/registry"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport/control"
)

// GitOpenService is the git.open surface: resolve the session, open the
// repository through the factory and register the binding, and hold the
// binding surface for the inline first status. It is what a
// GitOpenOperation hands its callback.
type GitOpenService interface {
	// Get resolves the session the connection owns (D15) — the handler
	// decides noCwd/remoteUnsupported from its Kind before opening.
	Get(id session.ID) (session.Session, error)
	// OpenBinding opens the repository at cwd through the wired factory
	// and registers it for the session, returning the minted binding id
	// and the open outcome. It owns the ownership-transfer rule (spec
	// §5.1): a live repo on a refusing outcome is closed before the
	// refusal is returned, and a Register failure closes the repo.
	OpenBinding(ctx context.Context, sid session.ID, cwd string) (bindingID string, outcome git.OpenOutcome, err error)
	// Acquire takes the use-guard for one binding — the same per-call
	// authorisation every git.* method applies (D15).
	Acquire(id string, caller registry.Caller) (registry.Handle, func(), error)
}

// GitOpenOperation is the typed operation for git.open. Its gates are
// [session, git]: opening resolves the session and registers the
// repository.
type GitOpenOperation interface {
	Run(context.Context, func(context.Context, GitOpenService) error) error
}

// NewGitOpenOperation builds the git.open operation, acquiring sessionGate
// before gitGate (the canonical order), then the execution lane.
func NewGitOpenOperation(
	sessionGate, gitGate, lane control.Admission,
	registry session.Registry,
	factory git.RepoFactory,
	reg *registry.Registry,
) GitOpenOperation {
	g := &guard{}
	return newOperation[GitOpenService](
		control.NewComposite(sessionGate, gitGate, lane),
		g,
		newGitOpenService(g, registry, factory, reg),
	)
}

// newGitOpenService builds the concrete git.open service bound to guard g.
func newGitOpenService(g *guard, registry session.Registry, factory git.RepoFactory, reg *registry.Registry) *gitOpenService {
	return &gitOpenService{guard: g, registry: registry, factory: factory, reg: reg}
}

type gitOpenService struct {
	guard    *guard
	registry session.Registry
	factory  git.RepoFactory
	reg      *registry.Registry
}

func (s *gitOpenService) Get(id session.ID) (session.Session, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.registry.Get(id)
}

func (s *gitOpenService) OpenBinding(ctx context.Context, sid session.ID, cwd string) (string, git.OpenOutcome, error) {
	if err := s.guard.check(); err != nil {
		return "", git.OpenOutcome{}, err
	}
	repo, outcome, err := s.factory.Open(ctx, cwd)
	if err != nil {
		return "", git.OpenOutcome{}, err
	}
	if outcome.State != git.OpenOK {
		if repo != nil {
			// Refusing outcome with a live repo: the repo is still ours,
			// and it must not leak.
			if cerr := repo.Close(); cerr != nil {
				return "", git.OpenOutcome{}, &openOutcomeCloseError{err: cerr}
			}
		}
		return "", outcome, nil
	}
	if repo == nil {
		// The other direction of the same lie: ok with no repository.
		return "", git.OpenOutcome{}, &openOutcomeNilRepoError{}
	}
	bid, err := s.reg.Register(repo, sid)
	if err != nil {
		// Always a typed error, close outcome or not: the transport's
		// git.open answers every register failure with the "git.open:"
		// prefix, and the raw registry error alone (close succeeded) must
		// not lose that classification.
		re := &openRegisterError{err: err}
		if cerr := repo.Close(); cerr != nil {
			re.closeErr = cerr
		}
		return "", git.OpenOutcome{}, re
	}
	return bid, outcome, nil
}

func (s *gitOpenService) Acquire(id string, caller registry.Caller) (registry.Handle, func(), error) {
	if err := s.guard.check(); err != nil {
		return nil, nil, err
	}
	return s.reg.Acquire(id, caller)
}

// GitBindingService is the per-binding git surface: every git.* method
// after git.open. A binding id is validated per call by Acquire — bindings
// close at any moment, so a construction-time check would be a lie.
type GitBindingService interface {
	Acquire(id string, caller registry.Caller) (registry.Handle, func(), error)
	Close(id string) error
	CloseSession(sessionID session.ID)
}

// GitBindingOperation is the typed operation for the git-binding domain.
// Its gate is [git].
type GitBindingOperation interface {
	Run(context.Context, func(context.Context, GitBindingService) error) error
}

// NewGitBindingOperation builds the git-binding operation, acquiring the
// git gate before the execution lane.
func NewGitBindingOperation(gitGate, lane control.Admission, reg *registry.Registry) GitBindingOperation {
	g := &guard{}
	return newOperation[GitBindingService](control.NewComposite(gitGate, lane), g, newGitBindingService(g, reg))
}

// newGitBindingService builds the concrete git-binding service bound to
// guard g.
func newGitBindingService(g *guard, reg *registry.Registry) *gitBindingService {
	return &gitBindingService{guard: g, reg: reg}
}

type gitBindingService struct {
	guard *guard
	reg   *registry.Registry
}

func (s *gitBindingService) Acquire(id string, caller registry.Caller) (registry.Handle, func(), error) {
	if err := s.guard.check(); err != nil {
		return nil, nil, err
	}
	return s.reg.Acquire(id, caller)
}

func (s *gitBindingService) Close(id string) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.reg.Close(id)
}

func (s *gitBindingService) CloseSession(sessionID session.ID) {
	if !s.guard.ok() {
		return
	}
	s.reg.CloseSession(sessionID)
}

// openOutcomeCloseError reports a refusing git.open outcome whose live repo
// could not be closed. The repo must not leak, and a failed close is a fact
// the operator should see, not a silently dropped error.
type openOutcomeCloseError struct{ err error }

func (e *openOutcomeCloseError) Error() string {
	return "git.open: close repo after refusing outcome: " + e.err.Error()
}
func (e *openOutcomeCloseError) Unwrap() error { return e.err }

// openOutcomeNilRepoError reports a factory that answered ok without a
// repository.
type openOutcomeNilRepoError struct{}

func (e *openOutcomeNilRepoError) Error() string {
	return "git.open: factory answered ok without a repository"
}

// openRegisterError reports a Register failure. closeErr is set only when
// the owned repo could also not be closed — a live repo must not leak, and a
// failed close is a fact the operator should see, not a silently dropped
// error. The "git.open:" prefix is the transport's wire vocabulary for the
// whole open error class.
type openRegisterError struct {
	err      error
	closeErr error
}

func (e *openRegisterError) Error() string {
	if e.closeErr != nil {
		return "git.open: register: " + e.err.Error() + "; close repo: " + e.closeErr.Error()
	}
	return "git.open: register: " + e.err.Error()
}
func (e *openRegisterError) Unwrap() error { return e.err }
