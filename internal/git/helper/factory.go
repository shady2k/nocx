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
	"encoding/json"
	"errors"
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

// Stage sends one git.stage operation. The pathspecs ride in the params as
// a NUL-joined literal list (D8) and reach git on the remote host through
// --pathspec-from-file=- — never argv; the fresh status is what comes
// back, field for field what local returns for the same repository.
func (r *repo) Stage(ctx context.Context, paths []string) (git.Status, error) {
	var st git.Status
	if err := r.f.client.Call(mutationCtx(ctx), "git", "stage",
		hostsvc.StageParams{BindingID: r.bindingID, Paths: paths}, &st); err != nil {
		return git.Status{}, classifyRefusal(err)
	}
	return st, nil
}

func (r *repo) Unstage(ctx context.Context, paths []string) (git.Status, error) {
	var st git.Status
	if err := r.f.client.Call(mutationCtx(ctx), "git", "unstage",
		hostsvc.StageParams{BindingID: r.bindingID, Paths: paths}, &st); err != nil {
		return git.Status{}, classifyRefusal(err)
	}
	return st, nil
}

func (r *repo) StageAll(ctx context.Context) (git.Status, error) {
	var st git.Status
	if err := r.f.client.Call(mutationCtx(ctx), "git", "stageAll",
		hostsvc.BindingParams{BindingID: r.bindingID}, &st); err != nil {
		return git.Status{}, classifyRefusal(err)
	}
	return st, nil
}

func (r *repo) UnstageAll(ctx context.Context) (git.Status, error) {
	var st git.Status
	if err := r.f.client.Call(mutationCtx(ctx), "git", "unstageAll",
		hostsvc.BindingParams{BindingID: r.bindingID}, &st); err != nil {
		return git.Status{}, classifyRefusal(err)
	}
	return st, nil
}

// Commit sends one git.commit operation: the message crosses as a JSON
// string and reaches git through commit -F - over stdin on the remote host
// (D8), never argv. Transport loss between the request and its response is
// D12's indeterminate — the commit may have happened, hooks and all — so
// the outcome says so instead of reporting a failure a retry would double.
func (r *repo) Commit(ctx context.Context, msg string, amend bool) (git.CommitOutcome, error) {
	var out git.CommitOutcome
	err := r.f.client.Call(mutationCtx(ctx), "git", "commit",
		hostsvc.CommitParams{BindingID: r.bindingID, Message: msg, Amend: amend}, &out)
	if err != nil {
		if errors.Is(err, client.ErrLost) {
			return git.CommitOutcome{State: git.CommitIndeterminate}, nil
		}
		return git.CommitOutcome{}, classifyRefusal(err)
	}
	return out, nil
}

// HeadMessage is the Amend prefill: the full HEAD message, fetched once
// when the box is ticked. A read, like the reads above: cancellable, and
// an unborn branch's "none" is a domain state inside the result.
func (r *repo) HeadMessage(ctx context.Context) (git.HeadMessage, error) {
	var hm git.HeadMessage
	if err := r.f.client.Call(ctx, "git", "headMessage",
		hostsvc.BindingParams{BindingID: r.bindingID}, &hm); err != nil {
		return git.HeadMessage{}, classifyRefusal(err)
	}
	return hm, nil
}

// RemoteURL is the "open on its hosting" fact, derived by git on the
// remote host. A detached HEAD, a branch with no upstream or a deleted
// remote answer ErrNoRemote — the ordinary "no link to draw" state, never
// an error.
func (r *repo) RemoteURL(ctx context.Context) (string, error) {
	var url string
	if err := r.f.client.Call(ctx, "git", "remoteURL",
		hostsvc.BindingParams{BindingID: r.bindingID}, &url); err != nil {
		return "", classifyRefusal(err)
	}
	return url, nil
}

// Close releases the shared helper client when this was the last repo of
// its factory (the registry drains a binding's use-guard before closing it,
// so no call races this).
func (r *repo) Close() error {
	r.closeOnce.Do(r.f.release)
	return nil
}

// mutationCtx strips the caller's cancellation from a mutation's call
// (D11): the helper refuses a cancel naming a mutation, and the backend
// must not even ask — half-applying a commit is worse than waiting for
// it. The caller's ctx stays alive in the background so the call runs to
// its real answer or the transport dies; a cancelled caller can no longer
// race the lost transport in Call's select, which is what makes D12's
// indeterminate deterministic.
func mutationCtx(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}

// classifyRefusal rebuilds the git domain errors the service coded on the
// wire (D11/D12): the transport switches on the typed errors, so a
// refusal that crossed as "git.nothing_to_commit" must arrive as
// *git.ErrNothingToCommit — fields intact, which is why ErrConflicted's
// path rides in the refusal's structured details — not as an opaque
// refusal. Anything that is not a coded refusal (a transport loss, a
// protocol refusal) passes through untouched.
func classifyRefusal(err error) error {
	var refusal *client.RefusalError
	if !errors.As(err, &refusal) {
		return err
	}
	switch refusal.Code {
	case hostsvc.ErrCodeNothingToCommit:
		return &git.ErrNothingToCommit{}
	case hostsvc.ErrCodeAmendUnborn:
		return &git.ErrAmendUnborn{}
	case hostsvc.ErrCodeConflicted:
		var c git.ErrConflicted
		if len(refusal.Details) > 0 {
			if uerr := json.Unmarshal(refusal.Details, &c); uerr != nil {
				// A malformed detail must not lose the refusal: the
				// type still says conflicted, the message still names
				// the path.
				return &git.ErrConflicted{}
			}
		}
		return &c
	case hostsvc.ErrCodeNoRemote:
		return &git.ErrNoRemote{}
	}
	return err
}
