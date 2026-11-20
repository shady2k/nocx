package app

// A git.open accepted by a far helper that never answers must still return an
// error the panel can show (nocx-xn63t.6.4). The caller's context is not that
// guarantee: a WS request can stay live while the browser waits for its promise,
// and client.Call deliberately bounds its wait only by the context it receives.
// Exercise the production Git selection and leased factory against a real
// helper host that dispatches the request but never completes it. The registry
// owns the serving client; bounding this read must not give Git carrier ownership.

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/git/hostsvc"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/waittest"
)

// neverAnsweringGitFactory waits for the far request's context to end, not for
// a delay: receiving git.open proves neither progress nor an eventual answer.
type neverAnsweringGitFactory struct{}

func (neverAnsweringGitFactory) Open(ctx context.Context, _ string) (git.Repo, git.OpenOutcome, error) {
	<-ctx.Done()
	return nil, git.OpenOutcome{}, ctx.Err()
}

// neverAnsweringHelperPeer serves the real helper host and session service, as
// the ordinary peer does, but replaces only the Git factory with one that never
// answers. This keeps the silent Git read distinct from a missing service or a
// failed helper handshake; realHelperPeer covers the answering factory instead.
func neverAnsweringHelperPeer(t testing.TB) func(in io.Reader, out io.Writer) int {
	contentHash := syntheticArtifactHash
	return func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, contentHash, "instance-1", discardLogger(t))
		h.Register(hostsvc.New(neverAnsweringGitFactory{}))
		svc := helpersession.New(helpersession.Options{
			Generation: proto.GenerationID(contentHash),
		})
		// The peer is this test's helper process: its sessions end when the
		// connection that served them does, which here is the test's own end
		// (nocx-1ebrs.66.1.29).
		defer svc.Close()
		h.Register(svc)
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}
}

// The interval starts when the leased factory calls the helper's Open and ends
// when Open returns, successfully or not, within gitOpenTimeout (or earlier on
// caller cancellation or serving loss). A caller that remains live must not
// extend that interval indefinitely just because the far side remains silent.
func TestGitOpenNeverAnsweredIsBoundedRatherThanHungForever(t *testing.T) {
	// Shrink only the test's budget: the assertion is about an internal deadline,
	// not the production duration or a sleep used to guess when work completed.
	old := gitOpenTimeout
	gitOpenTimeout = 200 * time.Millisecond
	defer func() { gitOpenTimeout = old }()
	far, panes, seams := gitFarStand(t, hostmode.Helper)
	// Replace the serving peer through the registry's real refresh path.
	lease, err := far.Acquire(t.Context(), "conn")
	if err != nil {
		t.Fatal(err)
	}
	if err = lease.Client.Close(); err != nil {
		t.Fatal(err)
	}
	waittest.WaitFor(t, "lost helper", func() bool { s, _ := far.Axis("conn"); return s.Reason == farhelper.ReasonHelperLost })
	seams.peer = neverAnsweringHelperPeer(t)
	if err = far.Refresh(t.Context(), "conn"); err != nil {
		t.Fatal(err)
	}
	waittest.WaitFor(t, "silent Git helper serving", func() bool { s, _ := far.Axis("conn"); return s.State == farhelper.StateServing })
	factory := helperGitFactory(far, func() *panedriver.Driver { return panes })
	selection := factory(&gitPaneSession{fakeRemoteSession: &fakeRemoteSession{host: "far.example"}, pane: "pane"})
	if selection.Factory == nil {
		t.Fatalf("selection=%+v", selection)
	}
	_, _, err = selection.Factory.Open(t.Context(), t.TempDir())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("silent Git open = %v, want internal deadline", err)
	}
}
