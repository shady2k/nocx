package app

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

func TestGitOpenNeverAnsweredIsBoundedRatherThanHungForever(t *testing.T) {
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
