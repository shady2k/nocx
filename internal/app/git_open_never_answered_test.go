package app

// nocx-xn63t.6.4: a git.open sent to a far helper that never answers must
// still end — with an error the panel can show — rather than hang the
// session's shared helper (and the caller waiting on it) forever.
//
// e2e/git-remote.spec.ts and e2e/remote-coordinator-reclaim.spec.ts measured
// this in the container: git.open sent, nothing logged again, and the Git
// panel waits out Playwright's own 60s timeout with nothing to show. A
// goroutine dump of the coordinator at the moment of the hang (SIGQUIT to
// cmd/nocx-server mid-run) named where it does NOT stop: no goroutine was
// blocked in client.(*Client).Call, in hostHelper.open, or on any mutex —
// the request-handling goroutine (control.runAndRelease) had already exited,
// meaning the coordinator's own synchronous handling of that git.open had
// already returned. What could not be established from the container run
// alone is why the browser never saw the answer that implies; this test
// covers the one thing that is unconditionally true regardless of that
// question — client.Client.Call has no timeout of its own (its own doc:
// "the wait is bounded by the context and by nothing else, deliberately"),
// and the context a WS request carries lives as long as the browser's own
// promise, so a far side that simply never replies wedges hostHelper.open's
// mutex, and everything behind it, for good.
//
// This drives the actual production composition root (helperGitFactory,
// hostHelper) against a far daemon that accepts the git.open request and
// answers nothing — the shape a far side that stopped responding takes on
// the wire, indistinguishable at this layer from the container's own hang.

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/git/hostsvc"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
)

// neverAnsweringGitFactory is a git.RepoFactory whose Open blocks until the
// FAR SIDE's own request context ends (the connection closing, or the
// process exiting) — never on its own. It is what a far helper that has
// stopped answering looks like from the wire's perspective: the request
// arrived (the far host.Host dispatched it to this factory), and nothing
// ever comes back.
type neverAnsweringGitFactory struct{}

func (neverAnsweringGitFactory) Open(ctx context.Context, _ string) (git.Repo, git.OpenOutcome, error) {
	<-ctx.Done()
	return nil, git.OpenOutcome{}, ctx.Err()
}

// neverAnsweringHelperPeer serves the real helper host and the real session
// service (git.open is only ever offered on a helper-selected session, which
// needs the session surface to exist for CloseHelpersFor/inventory calls
// elsewhere in this package's fixtures), but with the git service backed by
// a factory that never answers — helperPeerWithoutSession and realHelperPeer
// both wire localgit.NewFactory() instead, which is exactly the "and it
// answers" case those tests already cover.
func neverAnsweringHelperPeer(t testing.TB) func(in io.Reader, out io.Writer) int {
	contentHash := syntheticArtifactHash
	return func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, contentHash, "instance-1", discardLogger(t))
		h.Register(hostsvc.New(neverAnsweringGitFactory{}))
		h.Register(helpersession.New(helpersession.Options{
			Generation: proto.GenerationID(contentHash),
		}))
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}
}

// TestGitOpenNeverAnsweredIsBoundedRatherThanHungForever is the bead's
// second, independent acceptance criterion: an unanswerable git.open ends
// with an answer, not silence, and it ends inside gitOpenTimeout rather than
// riding the caller's own context (which a WS request's does not close on
// its own).
//
// THE INVARIANT, both ends named: hostHelper.open's wait on the far side's
// answer starts when h.factory.Open is called and ends — successfully or
// not — no later than gitOpenTimeout after that, regardless of what the far
// side does or how long the caller's own context stays open.
func TestGitOpenNeverAnsweredIsBoundedRatherThanHungForever(t *testing.T) {
	// Shrunk for the run: the mechanism under test is the bound itself, not
	// production's budget, and a bounded WAIT here is still a wait — a small
	// one keeps this test fast without touching the pattern.
	old := gitOpenTimeout
	gitOpenTimeout = 200 * time.Millisecond
	t.Cleanup(func() { gitOpenTimeout = old })

	provider := &fakeLaneProvider{peer: neverAnsweringHelperPeer(t)}
	source := stubArtifacts(t)
	store, installs := testConsentStores(t)
	factory, _ := helperGitFactory(provider, source, store, installs, discardLogger(t))

	sel := factory(&fakeRemoteSession{id: "s1", host: "host.example"})
	if sel.Factory == nil {
		t.Fatalf("git.open selection carried no factory: refusal=%+v", sel.Refusal)
	}

	type result struct {
		repo    git.Repo
		outcome git.OpenOutcome
		err     error
	}
	done := make(chan result, 1)
	go func() {
		repo, outcome, err := sel.Factory.Open(context.Background(), t.TempDir())
		done <- result{repo: repo, outcome: outcome, err: err}
	}()

	// The bound is on THIS select, racing an observable event (the call
	// returning) against a generous backstop — never a sleep deciding the
	// outcome. If gitOpenTimeout stopped bounding the call, this is what
	// would fire instead of the assertions below.
	select {
	case r := <-done:
		if r.repo != nil {
			_ = r.repo.Close()
		}
		if r.err == nil {
			t.Fatalf("git.open that was never answered returned no error at all: outcome=%+v", r.outcome)
		}
		if !errors.Is(r.err, context.DeadlineExceeded) {
			t.Fatalf("git.open that was never answered = %v, want context.DeadlineExceeded from the internal bound", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("git.open on a far helper that never answers hung well past gitOpenTimeout — the bound is not being applied")
	}
}
