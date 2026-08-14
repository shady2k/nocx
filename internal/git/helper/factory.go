// Package helper is the backend's git factory over the remote helper: a
// git.RepoFactory whose Open sends one git.open operation and whose Repo's
// read methods are one Call each (plan Task 7). The wire shapes are
// hostsvc's — one owner of the git service's contract (AD-8) — and the
// results cross as the domain types of internal/git, verbatim. The ops the
// helper does not serve yet (diff, log, the mutations — nocx-w3i1) answer
// an honest error rather than pretending.
package helper

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/git/hostsvc"
	"github.com/shady2k/nocx/internal/helper/client"
)

// NewFactory builds a git.RepoFactory over one helper client. The factory
// owns the client: every repo it opens shares it, and the LAST repo to
// close releases it — the helper process lives as long as any binding
// references it, which is the design's one process per helper connection
// (D4) bounded by the binding registry.
func NewFactory(c *client.Client) git.RepoFactory {
	return &factory{client: c}
}

type factory struct {
	client *client.Client

	mu   sync.Mutex
	refs int
}

// Open sends one git.open operation. The outcome is the domain OpenOutcome
// verbatim; only an ok outcome carries a repo, exactly as the local factory
// promises (the caller must register it or close it).
func (f *factory) Open(ctx context.Context, cwd string) (git.Repo, git.OpenOutcome, error) {
	var res hostsvc.OpenResult
	if err := f.client.Call(ctx, "git", "open", hostsvc.OpenParams{Cwd: cwd}, &res); err != nil {
		return nil, git.OpenOutcome{}, err
	}
	if res.State != git.OpenOK {
		return nil, res.OpenOutcome, nil
	}
	f.mu.Lock()
	f.refs++
	f.mu.Unlock()
	// The environment the helper resolved at start (D24) is stable for the
	// helper's lifetime, so the open outcome is the fact and the poll never
	return &repo{f: f, bindingID: res.BindingID, envState: res.EnvState, envReason: res.EnvReason}, res.OpenOutcome, nil
}

func (f *factory) release() {
	f.mu.Lock()
	if f.refs > 0 {
		f.refs--
	}
	last := f.refs == 0
	f.mu.Unlock()
	if last {
		_ = f.client.Close()
	}
}

// repo is one binding on the helper: the service-issued id addresses every
// later operation.
type repo struct {
	f         *factory
	bindingID string

	envState  git.EnvState
	envReason string

	closeOnce sync.Once
}

func (r *repo) Status(ctx context.Context) (git.Status, error) {
	var st git.Status
	if err := r.f.client.Call(ctx, "git", "status", hostsvc.BindingParams{BindingID: r.bindingID}, &st); err != nil {
		return git.Status{}, err
	}
	return st, nil
}

// EnvState is the environment the helper resolved once at start (D24),
// carried from the open outcome: stable for the helper's lifetime.
func (r *repo) EnvState() (git.EnvState, string) { return r.envState, r.envReason }

// errOpNotServed is the honest answer for every op the helper does not
// serve yet: the mutations land with nocx-dyib, and a repo that answers
// them must say so rather than inventing a local fallback — the panel's
// remote half is the helper's, by construction (D16).
var errOpNotServed = errors.New("helper: git operation not served by the helper yet")

// Diff sends one git.diff operation. The byte bound is the HELPER's to
// apply (D9): it travels in the params, and the bounded git.Diff — the
// retained prefix, the tooLarge state, the truncated flag — is what comes
// back. The backend never sees the bytes beyond the bound.
func (r *repo) Diff(ctx context.Context, path string, side git.Side, maxBytes int64) (git.Diff, error) {
	var d git.Diff
	if err := r.f.client.Call(ctx, "git", "diff", hostsvc.DiffParams{BindingID: r.bindingID, Path: path, Side: side, MaxBytes: maxBytes}, &d); err != nil {
		return git.Diff{}, err
	}
	return d, nil
}

// Log sends one git.log operation. Completeness and Total are computed by
// the helper, where the repository is (D9); the client never counts.
func (r *repo) Log(ctx context.Context, max int) (git.Log, error) {
	var lg git.Log
	if err := r.f.client.Call(ctx, "git", "log", hostsvc.LogParams{BindingID: r.bindingID, Max: max}, &lg); err != nil {
		return git.Log{}, err
	}
	return lg, nil
}

func (r *repo) Stage(ctx context.Context, paths []string) (git.Status, error) {
	return git.Status{}, fmt.Errorf("%w: stage", errOpNotServed)
}

func (r *repo) Unstage(ctx context.Context, paths []string) (git.Status, error) {
	return git.Status{}, fmt.Errorf("%w: unstage", errOpNotServed)
}

func (r *repo) StageAll(ctx context.Context) (git.Status, error) {
	return git.Status{}, fmt.Errorf("%w: stageAll", errOpNotServed)
}

func (r *repo) UnstageAll(ctx context.Context) (git.Status, error) {
	return git.Status{}, fmt.Errorf("%w: unstageAll", errOpNotServed)
}

func (r *repo) Commit(ctx context.Context, msg string, amend bool) (git.CommitOutcome, error) {
	return git.CommitOutcome{}, fmt.Errorf("%w: commit", errOpNotServed)
}

func (r *repo) HeadMessage(ctx context.Context) (git.HeadMessage, error) {
	return git.HeadMessage{}, fmt.Errorf("%w: headMessage", errOpNotServed)
}

func (r *repo) RemoteURL(ctx context.Context) (string, error) {
	return "", fmt.Errorf("%w: remoteURL", errOpNotServed)
}

// Close releases the shared helper client when this was the last repo of
// its factory (the registry drains a binding's use-guard before closing it,
// so no call races this).
func (r *repo) Close() error {
	r.closeOnce.Do(r.f.release)
	return nil
}
