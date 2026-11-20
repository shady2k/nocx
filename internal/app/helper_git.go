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
		if status.State != farhelper.StateServing {
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
	lease, err := f.far.Acquire(ctx, id)
	if err != nil {
		var unavailable *farhelper.Unavailable
		if errors.As(err, &unavailable) {
			return nil, git.OpenOutcome{}, fmt.Errorf("git on %s: %w", f.host, unavailable)
		}
		return nil, git.OpenOutcome{}, err
	}
	openCtx, cancel := leaseContext(ctx, lease.Ctx)
	defer cancel()
	bounded, end := context.WithTimeout(openCtx, gitOpenTimeout)
	defer end()
	repo, outcome, err := helpergit.NewFactory(lease.Client).Open(bounded, cwd)
	if repo != nil {
		repo = &leasedGitRepo{Repo: repo, lifetime: lease.Ctx}
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
	lifetime context.Context
}

func (r *leasedGitRepo) Status(ctx context.Context) (git.Status, error) {
	ctx, cancel := leaseContext(ctx, r.lifetime)
	defer cancel()
	return r.Repo.Status(ctx)
}

func (r *leasedGitRepo) Diff(ctx context.Context, path string, side git.Side, maxBytes int64) (git.Diff, error) {
	ctx, cancel := leaseContext(ctx, r.lifetime)
	defer cancel()
	return r.Repo.Diff(ctx, path, side, maxBytes)
}

func (r *leasedGitRepo) Log(ctx context.Context, max int) (git.Log, error) {
	ctx, cancel := leaseContext(ctx, r.lifetime)
	defer cancel()
	return r.Repo.Log(ctx, max)
}

func (r *leasedGitRepo) Stage(ctx context.Context, paths []string) (git.Status, error) {
	ctx, cancel := leaseContext(ctx, r.lifetime)
	defer cancel()
	return r.Repo.Stage(ctx, paths)
}

func (r *leasedGitRepo) Unstage(ctx context.Context, paths []string) (git.Status, error) {
	ctx, cancel := leaseContext(ctx, r.lifetime)
	defer cancel()
	return r.Repo.Unstage(ctx, paths)
}

func (r *leasedGitRepo) StageAll(ctx context.Context) (git.Status, error) {
	ctx, cancel := leaseContext(ctx, r.lifetime)
	defer cancel()
	return r.Repo.StageAll(ctx)
}

func (r *leasedGitRepo) UnstageAll(ctx context.Context) (git.Status, error) {
	ctx, cancel := leaseContext(ctx, r.lifetime)
	defer cancel()
	return r.Repo.UnstageAll(ctx)
}

func (r *leasedGitRepo) Commit(ctx context.Context, msg string, amend bool) (git.CommitOutcome, error) {
	ctx, cancel := leaseContext(ctx, r.lifetime)
	defer cancel()
	return r.Repo.Commit(ctx, msg, amend)
}

func (r *leasedGitRepo) HeadMessage(ctx context.Context) (git.HeadMessage, error) {
	ctx, cancel := leaseContext(ctx, r.lifetime)
	defer cancel()
	return r.Repo.HeadMessage(ctx)
}

func (r *leasedGitRepo) RemoteURL(ctx context.Context) (string, error) {
	ctx, cancel := leaseContext(ctx, r.lifetime)
	defer cancel()
	return r.Repo.RemoteURL(ctx)
}

func (r *leasedGitRepo) AddWorktree(ctx context.Context, branch, base, path string) (git.WorktreeAdded, error) {
	ctx, cancel := leaseContext(ctx, r.lifetime)
	defer cancel()
	return r.Repo.AddWorktree(ctx, branch, base, path)
}

func (r *leasedGitRepo) Worktrees(ctx context.Context, base string) ([]git.Worktree, error) {
	ctx, cancel := leaseContext(ctx, r.lifetime)
	defer cancel()
	return r.Repo.Worktrees(ctx, base)
}

func (r *leasedGitRepo) RemoveWorktree(ctx context.Context, path string, opts git.RemoveWorktreeOptions) (git.WorktreeDiscard, error) {
	ctx, cancel := leaseContext(ctx, r.lifetime)
	defer cancel()
	return r.Repo.RemoveWorktree(ctx, path, opts)
}

func (r *leasedGitRepo) DeleteWorktreeBranch(ctx context.Context, branch, base string) error {
	ctx, cancel := leaseContext(ctx, r.lifetime)
	defer cancel()
	return r.Repo.DeleteWorktreeBranch(ctx, branch, base)
}
