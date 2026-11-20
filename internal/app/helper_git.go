package app

import (
	"context"
	"errors"
	"fmt"
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
		Message: (&farhelper.Unavailable{Reason: reason, Action: action}).Error(),
	}}
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
		return nil, git.OpenOutcome{}, &farhelper.Unavailable{Reason: farhelper.ReasonConnection, Action: farhelper.ActionNone}
	}
	bounded, end := context.WithTimeout(ctx, gitOpenTimeout)
	defer end()
	lease, err := f.far.Acquire(bounded, id)
	if err != nil {
		var unavailable *farhelper.Unavailable
		if errors.As(err, &unavailable) {
			return nil, git.OpenOutcome{}, fmt.Errorf("git on %s: %w", f.host, unavailable)
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
		return &farhelper.Unavailable{Reason: farhelper.ReasonConnection, Action: farhelper.ActionNone}
	}
	if status.Reason == "" {
		return &farhelper.Unavailable{Reason: farhelper.ReasonHelperLost, Action: farhelper.ActionNone}
	}
	return &farhelper.Unavailable{Reason: status.Reason, Action: status.Action}
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
