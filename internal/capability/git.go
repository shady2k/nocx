package capability

import (
	"context"
	"errors"
	"path"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/git/registry"
	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/operation/schedule"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport/control"
)

// GitOpenFactory resolves the repo factory git.open uses for a resolved
// session — the composition root's answer to "which git runs for this
// session" (AD-8; the files domain's ProviderFactory is the same shape).
// Local sessions resolve to the local factory; SSH sessions resolve to the
// helper-backed factory when one is available, and to nil when the
// selection refuses the session (remote-helper design §6). It must be
// side-effect-free: git.open consults it twice (the handler's refusal
// decision, then the open), and the two calls must agree.
type GitOpenFactory func(sess session.Session) git.RepoFactory

// GitOpenService is the git.open surface: resolve the session, open the
// repository through the factory and register the binding, and hold the
// binding surface for the inline first status. It is what a
// GitOpenOperation hands its callback.
type GitOpenService interface {
	// Get resolves the session the connection owns (D15) — the handler
	// decides noCwd and the §6 refusal states from its Kind before
	// opening.
	Get(id session.ID) (session.Session, error)
	// OpenBinding opens the repository at cwd through the wired factory
	// and registers it for the session, returning the minted binding id
	// and the open outcome. It owns the ownership-transfer rule (spec
	// §5.1): a live repo on a refusing outcome is closed before the
	// refusal is returned, and a Register failure closes the repo.
	OpenBinding(ctx context.Context, sess session.Session, cwd string) (bindingID string, outcome git.OpenOutcome, err error)
	// Acquire takes the use-guard for one binding — the same per-call
	// authorisation every git.* method applies (D15).
	Acquire(id string, caller registry.Caller) (registry.Handle, func(), error)
	// Close closes one binding — the opener's own cleanup when the session
	// it opened for closed while the open was in flight.
	Close(id string) error
}

// GitOpenOperation is the typed operation for git.open, and it is two-phase
// for the reason OpenOperation is (nocx-l21ib.1, nocx-zg3k3.14.17): Resolve
// runs under the session gate alone, Open under the checkout's failure-domain
// bulkhead. On an ssh session the open is a helper
// round trip — a lease, a uname, the open, the first status — and the session
// gate is capacity one for every session request of every pane. Held across
// that, a reloaded window's few ssh panes filled its wait queue and every
// resize, sessions.live and shell request on the server was refused "Control
// plane busy" for as long as the opens took. Resolving is a registry read; it
// is the only part the session gate has any business excluding.
//
// The git gate is gone in stage 5. The open draws on its checkout's mount
// instead, so a hung mount holds the open's permit and a healthy one opens.
type GitOpenOperation interface {
	AssistantOperation
	// Resolve runs fn under the session gate and nothing else.
	Resolve(context.Context, func(context.Context, GitOpenService) error) error
	// Open runs fn under the checkout's bulkhead, no session gate held. The declaration's turn and max are enforced; the
	// failure domain is read from the session and the cwd the open names.
	Open(ctx context.Context, sess session.Session, cwd string, fn func(context.Context, GitOpenService) error) error
}

// NewGitOpenOperation builds the git.open operation.
func NewGitOpenOperation(
	sessionGate control.Admission, bulkhead *schedule.Bulkhead,
	registry session.Registry, factory GitOpenFactory, reg *registry.Registry,
) GitOpenOperation {
	g := &guard{}
	disposition := Adapted("git.open", "opening a repository resolves a session and selects its helper or local binding")
	return &gitOpenOperation{
		resolve:  newOperation[GitOpenService](disposition, control.NewComposite(sessionGate), g, newGitOpenService(g, registry, factory, reg)),
		bulkhead: bulkhead, guard: g,
		svc: newGitOpenService(g, registry, factory, reg), disposition: disposition,
	}
}

// gitOpenOperation is the two phases over ONE guard and ONE service: a handle
// captured in one phase is refused outside every in-flight phase.
type gitOpenOperation struct {
	resolve     *operation[GitOpenService]
	bulkhead    *schedule.Bulkhead
	guard       *guard
	svc         GitOpenService
	disposition Disposition
}

func (o *gitOpenOperation) Disposition() Disposition { return o.disposition }

func (o *gitOpenOperation) Resolve(ctx context.Context, fn func(context.Context, GitOpenService) error) error {
	return o.resolve.Run(ctx, fn)
}

func (o *gitOpenOperation) Open(ctx context.Context, sess session.Session, cwd string, fn func(context.Context, GitOpenService) error) error {
	allow, _ := allowanceFromContext(ctx)
	domain := openFailureDomain(sess, cwd)
	return gitRun(ctx, allow, func(waitCtx context.Context) (func(), func(), error) {
		lease, err := o.bulkhead.Acquire(waitCtx, domain, schedule.Class(gitClass(allow.Origin)))
		if err != nil {
			return nil, nil, gitBulkheadError(ctx, allow, err)
		}
		return func() { lease.Release() }, lease.Abandon, nil
	}, func(workCtx context.Context) error {
		o.guard.begin()
		defer o.guard.end()
		return fn(workCtx, o.svc)
	})
}

// openFailureDomain is the mount the open will run on, before a checkout
// exists: the cwd's device for a local session, the helper session for a
// remote one.
func openFailureDomain(sess session.Session, cwd string) string {
	if sess.Kind() != session.KindLocal {
		return "remote:" + string(sess.ID())
	}
	return git.FailureDomain(cwd)
}

// newGitOpenService builds the concrete git.open service bound to guard g.
func newGitOpenService(g *guard, registry session.Registry, factory GitOpenFactory, reg *registry.Registry) *gitOpenService {
	return &gitOpenService{guard: g, registry: registry, factory: factory, reg: reg}
}

type gitOpenService struct {
	guard    *guard
	registry session.Registry
	factory  GitOpenFactory
	reg      *registry.Registry
}

func (s *gitOpenService) Get(id session.ID) (session.Session, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.registry.Get(id)
}

func (s *gitOpenService) OpenBinding(ctx context.Context, sess session.Session, cwd string) (string, git.OpenOutcome, error) {
	if err := s.guard.check(); err != nil {
		return "", git.OpenOutcome{}, err
	}
	f := s.factory(sess)
	if f == nil {
		// No factory for this session. The transport answers the §6
		// refusal states and the not-available error from the session's
		// origin before opening, so this is the service's honest answer
		// if the two ever disagree.
		return "", git.OpenOutcome{}, errNoFactoryForSession
	}
	repo, outcome, err := f.Open(ctx, cwd)
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
	checkout, domain := gitBindingIdentity(sess, outcome.Toplevel)
	if checkout == "" {
		// A source open that answered ok without a worktree root cannot be
		// ordered per checkout. The registry refuses the identity, and the
		// repo is closed below like any failed register.
		re := &openRegisterError{err: errNoCheckoutIdentity}
		if cerr := repo.Close(); cerr != nil {
			re.closeErr = cerr
		}
		return "", git.OpenOutcome{}, re
	}
	bid, err := s.reg.Register(repo, sess.ID(), checkout, domain)
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

func (s *gitOpenService) Close(id string) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.reg.Close(id)
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
//
// ADR-8eej3e66 stage 5: its whole-domain gate is gone. A mutation chains on
// its CHECKOUT (not its binding — the branch source and the panel hold two
// bindings on one checkout), every call draws on its checkout's failure
// domain through a bulkhead, and the declaration's turn and max are enforced.
// Reads take no key: they have no order to keep and run concurrently.
type GitBindingOperation interface {
	AssistantOperation
	// Run orders and bounds one git.* call on bindingID. Declarations come
	// from the catalogue scope the dispatcher put in ctx; a call without one
	// (a direct capability test) runs unbounded.
	Run(ctx context.Context, bindingID string, fn func(context.Context, GitBindingService) error) error
}

// NewGitBindingOperation builds the git-binding operation. order serialises
// mutations per checkout; bulkhead bounds execution per checkout mount. There
// is no third admission: the execution lane went with the rest of the capacity
// answers (ADR-8eej3e66 stage 8).
func NewGitBindingOperation(order *schedule.Keyed, bulkhead *schedule.Bulkhead, reg *registry.Registry) GitBindingOperation {
	g := &guard{}
	return &gitBindingOperation{
		order: order, bulkhead: bulkhead, reg: reg, g: g,
		svc:         newGitBindingService(g, reg),
		disposition: Adapted("git.refresh", "git calls require an assistant-owned repository lease"),
	}
}

type gitBindingOperation struct {
	order       *schedule.Keyed
	bulkhead    *schedule.Bulkhead
	reg         *registry.Registry
	g           *guard
	svc         GitBindingService
	disposition Disposition
}

func (o *gitBindingOperation) Disposition() Disposition { return o.disposition }

func (o *gitBindingOperation) Run(ctx context.Context, bindingID string, fn func(context.Context, GitBindingService) error) error {
	allow, hasAllowance := allowanceFromContext(ctx)
	identity, known := o.reg.Identity(bindingID)
	ordered := hasAllowance && allow.Merge == catalog.MergeNone
	return gitRun(ctx, allow, func(waitCtx context.Context) (func(), func(), error) {
		if !known {
			// The binding is gone. Nothing to order or bound: Acquire answers
			// the unknown-binding error inside the work, in the one place
			// that checks ownership.
			return func() {}, func() {}, nil
		}
		var releases []func()
		releaseAll := func() {
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
		}
		if ordered {
			release, err := o.order.Acquire(waitCtx, identity.Checkout)
			if err != nil {
				return nil, nil, err
			}
			releases = append(releases, release)
		}
		lease, err := o.bulkhead.Acquire(waitCtx, identity.Domain, schedule.Class(gitClass(allow.Origin)))
		if err != nil {
			releaseAll()
			return nil, nil, gitBulkheadError(ctx, allow, err)
		}
		releases = append(releases, lease.Release)
		return releaseAll, lease.Abandon, nil
	}, func(workCtx context.Context) error {
		o.g.begin()
		defer o.g.end()
		return fn(workCtx, o.svc)
	})
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

// errNoFactoryForSession reports a git.open that reached the factory
// selector with no factory for the session. The transport answers the §6
// refusal states and the not-available error from the session's origin
// before opening, so this is the service's honest answer if the two ever
// disagree.
var errNoFactoryForSession = errors.New("git.open: no repo factory for this session")

// gitBindingIdentity is the checkout and failure-domain identity a binding
// carries. A local checkout is canonicalised and keyed by its mount; a remote
// one is keyed by the helper session that reaches it, because that connection
// is what fails together for work on that host.
func gitBindingIdentity(sess session.Session, toplevel string) (checkout, domain string) {
	if toplevel == "" {
		return "", ""
	}
	if sess.Kind() == session.KindLocal {
		return git.CheckoutIdentity(toplevel)
	}
	id := string(sess.ID())
	return "remote:" + id + ":" + path.Clean(toplevel), "remote:" + id
}

// openOutcomeCloseError reports a refusing git.open outcome whose live repo
// could not be closed. The repo must not leak, and a failed close is a fact
// the operator should see, not a silently dropped error.
type openOutcomeCloseError struct{ err error }

func (e *openOutcomeCloseError) Error() string {
	return "git.open: close repo after refusing outcome: " + e.err.Error()
}
func (e *openOutcomeCloseError) Unwrap() error { return e.err }

// errNoCheckoutIdentity reports an open that answered ok without a worktree
// root. Nothing can be ordered without it, so the open is refused rather than
// registered under a guess.
var errNoCheckoutIdentity = errors.New("git.open: ok outcome without a checkout identity")

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
