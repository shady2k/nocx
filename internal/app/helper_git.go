package app

import (
	"context"
	"errors"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/git"
	helpergit "github.com/shady2k/nocx/internal/git/helper"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
)

// helperGitFactory only consumes the far helper's decision. Selection never
// probes, installs, asks for consent, or redials a lost client.
func helperGitFactory(far *farhelper.Registry, panes func() *panedriver.Driver) transport.GitFactoryFor {
	return func(sess session.Session) transport.GitOpenSelection {
		id, ok := panes().ConnectionID(sess.PaneID())
		status, known := far.Axis(id)
		if !ok || !known {
			return gitAxisRefusal(farhelper.ReasonConnection, farhelper.ActionNone)
		}
		if status.State != farhelper.StateServing && status.State != farhelper.StateStarting {
			return gitAxisRefusal(status.Reason, status.Action)
		}
		return transport.GitOpenSelection{Factory: &sessionFactory{far: far, panes: panes, paneID: sess.PaneID(), host: sess.Host()}}
	}
}

func gitAxisRefusal(reason farhelper.Reason, action farhelper.Action) transport.GitOpenSelection {
	return transport.GitOpenSelection{Refusal: &transport.GitOpenRefusal{
		Message: helperReasonPhrase(reason),
	}}
}

// helperReasonPhrase says what a far-helper reason means for the Git surface in
// a person's terms. The reason is the wire's closed-set token (ADR-727g63tr
// decision 2) and stays there: a panel names the choice that would change the
// answer, never the token itself.
func helperReasonPhrase(reason farhelper.Reason) string {
	switch reason {
	case farhelper.ReasonModeRaw:
		return "this host's integration method is Raw, which does not run the nocx helper — set it to Helper to open repositories here"
	case farhelper.ReasonModeScript:
		return "this host's integration method is Script, which installs the shell integration but not the nocx helper — set it to Helper to ask for the helper on this machine"
	case farhelper.ReasonConsentPending:
		return "this machine has not been asked yet whether nocx may use its helper — reconnect this connection to be asked"
	case farhelper.ReasonConsentDenied:
		return "this machine has declined to run the nocx helper — change that answer in the footprint screen to allow it"
	case farhelper.ReasonUnsupportedPlatform:
		return "we build no nocx helper for this machine's platform"
	case farhelper.ReasonInstallFailed:
		return "the nocx helper could not be installed on this machine"
	case farhelper.ReasonExecForbidden:
		return "this host refused to run the nocx helper"
	case farhelper.ReasonVersionMismatch:
		return "the nocx helper on this machine answered a different protocol version — reinstall it to recover"
	case farhelper.ReasonHelperLost:
		return "the nocx helper on this machine is no longer reachable — refresh the connection to reach it again"
	case farhelper.ReasonConnection:
		return "this connection is not ready for the nocx helper — reconnect it"
	default:
		return "the nocx helper is not available on this machine"
	}
}

// gitRefusalError renders a far-helper refusal as the phrase above while
// keeping the typed refusal reachable with errors.As. The transport's own
// branch and every caller that classifies a refusal still read the reason and
// the action; only what a person sees changes.
type gitRefusalError struct {
	unavailable *farhelper.Unavailable
}

func (e *gitRefusalError) Error() string { return helperReasonPhrase(e.unavailable.Reason) }
func (e *gitRefusalError) Unwrap() error { return e.unavailable }

func gitRefusal(reason farhelper.Reason, action farhelper.Action) error {
	return &gitRefusalError{unavailable: &farhelper.Unavailable{Reason: reason, Action: action}}
}

type sessionFactory struct {
	far    *farhelper.Registry
	panes  func() *panedriver.Driver
	paneID string
	host   string
}

func (f *sessionFactory) Open(ctx context.Context, cwd string) (git.Repo, git.OpenOutcome, error) {
	id, ok := f.panes().ConnectionID(f.paneID)
	if !ok {
		return nil, git.OpenOutcome{}, gitRefusal(farhelper.ReasonConnection, farhelper.ActionNone)
	}
	bounded, end := context.WithTimeout(ctx, gitOpenTimeout)
	defer end()
	lease, err := f.far.Acquire(bounded, id)
	if err != nil {
		var unavailable *farhelper.Unavailable
		if errors.As(err, &unavailable) {
			return nil, git.OpenOutcome{}, &gitRefusalError{unavailable: unavailable}
		}
		return nil, git.OpenOutcome{}, err
	}
	openCtx, cancel := leaseContext(bounded, lease.Ctx)
	defer cancel()
	binding := &leasedGitRepo{lifetime: lease.Ctx, far: f.far, connectionID: id}
	if unavailable := binding.unavailable(); unavailable != nil {
		return nil, git.OpenOutcome{}, unavailable
	}
	repo, outcome, err := helpergit.NewFactory(lease.Client).Open(openCtx, cwd)
	if unavailable := binding.unavailable(); unavailable != nil {
		if repo != nil {
			_ = repo.Close()
		}
		return nil, git.OpenOutcome{}, unavailable
	}
	if repo != nil {
		binding.Repo = repo
		repo = binding
	}
	return repo, outcome, err
}

// leaseContext preserves request values and cancellation while also ending at
// the registry's serving boundary. The stop function releases the callback at
// the end of each call, so an idle repository holds no pending callbacks.
func leaseContext(ctx, lifetime context.Context) (context.Context, context.CancelFunc) {
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lifetime, cancel)
	if lifetime.Err() != nil {
		cancel()
	}
	return child, func() { stop(); cancel() }
}

// git.open is a read; bound an unresponsive peer without imposing a deadline
// on mutations. A var lets the silent-peer test use a short budget.
var gitOpenTimeout = 20 * time.Second

// Reads end with serving. The helper layer still applies D11 to mutations:
// caller cancellation cannot abandon a commit; registry retirement closes the
// client itself and the helper layer reports an indeterminate transport loss.
type leasedGitRepo struct {
	git.Repo
	lifetime     context.Context
	far          *farhelper.Registry
	connectionID string
}

// An ended lease stays ended even if a new helper now serves the connection.
// Starting/serving carry no refusal reason, so that stale binding names the
// helper it lost, while a current refusal comes from the axis's sole owner.
func (r *leasedGitRepo) unavailable() error {
	if r.lifetime.Err() == nil {
		return nil
	}
	status, known := r.far.Axis(r.connectionID)
	if !known {
		return gitRefusal(farhelper.ReasonConnection, farhelper.ActionNone)
	}
	if status.Reason == "" {
		return gitRefusal(farhelper.ReasonHelperLost, farhelper.ActionNone)
	}
	return gitRefusal(status.Reason, status.Action)
}

func gitLeaseCall[T any](r *leasedGitRepo, ctx context.Context, mutation bool, call func(context.Context) (T, error)) (T, error) {
	if err := r.unavailable(); err != nil {
		var zero T
		return zero, err
	}
	ctx, cancel := leaseContext(ctx, r.lifetime)
	defer cancel()
	result, err := call(ctx)
	// Once admitted, mutations keep their helper-layer D11/D12 result: a
	// lost commit may have happened, and must remain indeterminate.
	if !mutation {
		if unavailable := r.unavailable(); unavailable != nil {
			var zero T
			return zero, unavailable
		}
	}
	return result, err
}

func (r *leasedGitRepo) Status(ctx context.Context) (git.Status, error) {
	return gitLeaseCall(r, ctx, false, func(ctx context.Context) (git.Status, error) {
		return r.Repo.Status(ctx)
	})
}

func (r *leasedGitRepo) Diff(ctx context.Context, path string, side git.Side, maxBytes int64) (git.Diff, error) {
	return gitLeaseCall(r, ctx, false, func(ctx context.Context) (git.Diff, error) {
		return r.Repo.Diff(ctx, path, side, maxBytes)
	})
}

func (r *leasedGitRepo) Log(ctx context.Context, max int) (git.Log, error) {
	return gitLeaseCall(r, ctx, false, func(ctx context.Context) (git.Log, error) {
		return r.Repo.Log(ctx, max)
	})
}

func (r *leasedGitRepo) Stage(ctx context.Context, paths []string) (git.Status, error) {
	return gitLeaseCall(r, ctx, true, func(ctx context.Context) (git.Status, error) {
		return r.Repo.Stage(ctx, paths)
	})
}

func (r *leasedGitRepo) Unstage(ctx context.Context, paths []string) (git.Status, error) {
	return gitLeaseCall(r, ctx, true, func(ctx context.Context) (git.Status, error) {
		return r.Repo.Unstage(ctx, paths)
	})
}

func (r *leasedGitRepo) StageAll(ctx context.Context) (git.Status, error) {
	return gitLeaseCall(r, ctx, true, func(ctx context.Context) (git.Status, error) {
		return r.Repo.StageAll(ctx)
	})
}

func (r *leasedGitRepo) UnstageAll(ctx context.Context) (git.Status, error) {
	return gitLeaseCall(r, ctx, true, func(ctx context.Context) (git.Status, error) {
		return r.Repo.UnstageAll(ctx)
	})
}

func (r *leasedGitRepo) Commit(ctx context.Context, msg string, amend bool) (git.CommitOutcome, error) {
	return gitLeaseCall(r, ctx, true, func(ctx context.Context) (git.CommitOutcome, error) {
		return r.Repo.Commit(ctx, msg, amend)
	})
}

func (r *leasedGitRepo) HeadMessage(ctx context.Context) (git.HeadMessage, error) {
	return gitLeaseCall(r, ctx, false, func(ctx context.Context) (git.HeadMessage, error) {
		return r.Repo.HeadMessage(ctx)
	})
}

func (r *leasedGitRepo) RemoteURL(ctx context.Context) (string, error) {
	return gitLeaseCall(r, ctx, false, func(ctx context.Context) (string, error) {
		return r.Repo.RemoteURL(ctx)
	})
}

func (r *leasedGitRepo) AddWorktree(ctx context.Context, branch, base, path string) (git.WorktreeAdded, error) {
	return gitLeaseCall(r, ctx, true, func(ctx context.Context) (git.WorktreeAdded, error) {
		return r.Repo.AddWorktree(ctx, branch, base, path)
	})
}

func (r *leasedGitRepo) Worktrees(ctx context.Context, base string) ([]git.Worktree, error) {
	return gitLeaseCall(r, ctx, false, func(ctx context.Context) ([]git.Worktree, error) {
		return r.Repo.Worktrees(ctx, base)
	})
}

func (r *leasedGitRepo) RemoveWorktree(ctx context.Context, path string, opts git.RemoveWorktreeOptions) (git.WorktreeDiscard, error) {
	return gitLeaseCall(r, ctx, true, func(ctx context.Context) (git.WorktreeDiscard, error) {
		return r.Repo.RemoveWorktree(ctx, path, opts)
	})
}

func (r *leasedGitRepo) DeleteWorktreeBranch(ctx context.Context, branch, base string) error {
	_, err := gitLeaseCall(r, ctx, true, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, r.Repo.DeleteWorktreeBranch(ctx, branch, base)
	})
	return err
}
