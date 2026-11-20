package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/hostconn/hostconntest"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/git"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

type gitFarSeams struct {
	t        *testing.T
	probes   atomic.Int32
	installs atomic.Int32
	dials    atomic.Int32
	peer     func(io.Reader, io.Writer) int
}

func (s *gitFarSeams) Probe(context.Context, farhelper.Target) (farhelper.Platform, error) {
	s.probes.Add(1)
	return farhelper.Platform{GOOS: "linux", GOARCH: "amd64"}, nil
}

func (s *gitFarSeams) Find(context.Context, farhelper.Target, farhelper.Platform) (farhelper.Installed, bool, error) {
	return farhelper.Installed{Dir: "/helper", Generation: syntheticArtifactHash}, true, nil
}

func (s *gitFarSeams) Install(context.Context, farhelper.Target, farhelper.Platform) (farhelper.Installed, error) {
	s.installs.Add(1)
	return farhelper.Installed{Dir: "/helper", Generation: syntheticArtifactHash}, nil
}

func (s *gitFarSeams) Dial(ctx context.Context, _ farhelper.Target, _ farhelper.Installed) (*helperclient.Client, error) {
	s.dials.Add(1)
	lane := newFakeLaneConn(s.peer)
	c, err := helperclient.Dial(ctx, helperclient.Config{Exec: lane, ExpectHash: syntheticArtifactHash, Log: discardLogger(s.t)})
	if err != nil {
		_ = lane.Close()
	}
	s.t.Cleanup(func() { _ = lane.Close(); <-lane.exited })
	return c, err
}

type gitPaneSession struct {
	*fakeRemoteSession
	pane string
}

func (s *gitPaneSession) PaneID() string { return s.pane }

func gitFarStand(t *testing.T, mode hostmode.Mode) (*farhelper.Registry, *panedriver.Driver, *gitFarSeams) {
	t.Helper()
	source := newFarHelperConnections()
	source.Observe(farhelper.Connection{ID: "conn", Destination: proto.SSHDestination{Host: "far.example", Port: 22, User: "dev"}, HostKeyFingerprint: "SHA256:test-host", State: hostconn.Connected, Shells: 1})
	modes := newTestHostModes(t)
	mustRecordHostMode(t, modes, "SHA256:test-host", mode)
	consent, _ := testConsentStores(t)
	seams := &gitFarSeams{t: t, peer: helperPeerWithoutSession(t)}
	far := farhelper.New(farhelper.Config{Clock: hostconntest.New(), Connections: source, Modes: modes, Consents: consent, Probe: seams, Install: seams, Dial: seams, Log: farHelperTestLogger(t)})
	t.Cleanup(far.Close)
	panes := panedriver.New("instance", farHelperTestLogger(t))
	panes.ApplyFrom("local", proto.ConnectionStateEvent{ConnectionID: "conn", Seq: 1, State: proto.ConnectionConnected, PlainShells: 1})
	panes.Bind("pane", "conn")
	waittest.WaitFor(t, "far helper decision", func() bool { status, ok := far.Axis("conn"); return ok && status.State != farhelper.StateStarting })
	return far, panes, seams
}

func TestGitRawRefusalNamesTheFarHelperAxis(t *testing.T) {
	far, panes, seams := gitFarStand(t, hostmode.Raw)
	selectGit := helperGitFactory(far, func() *panedriver.Driver { return panes })
	for range 2 {
		got := selectGit(&gitPaneSession{fakeRemoteSession: &fakeRemoteSession{host: "far.example"}, pane: "pane"})
		if got.Factory != nil || got.Refusal == nil || got.Refusal.State != "" || !strings.Contains(got.Refusal.Message, "mode-raw") || !strings.Contains(got.Refusal.Message, "host-settings") {
			t.Fatalf("selection = %+v, want mode-raw (host-settings)", got)
		}
	}
	if seams.probes.Load() != 0 || seams.installs.Load() != 0 || seams.dials.Load() != 0 {
		t.Fatal("Git started remote work on a raw host")
	}
}

func TestGitAcquiresFarHelperIndependentlyOfHostedShell(t *testing.T) {
	for _, hosted := range []bool{false, true} {
		t.Run(map[bool]string{false: "without-hosted-shell", true: "with-hosted-shell"}[hosted], func(t *testing.T) {
			far, panes, seams := gitFarStand(t, hostmode.Helper)
			var shell *coordinator
			var shellID session.ID
			if hosted {
				provider := &fakeLaneProvider{peer: realHelperPeerWithSpawner(t)}
				shell = newCoordinator(t, provider)
				binding := openHostedFixture(t, shell, "pane")
				shellID = session.ID(binding.SessionID)
				t.Cleanup(func() {
					if provider.laneCount() != 1 {
						t.Error("Git redialled the hosted shell carrier")
					}
				})
			}
			selectGit := helperGitFactory(far, func() *panedriver.Driver { return panes })
			sess := &gitPaneSession{fakeRemoteSession: &fakeRemoteSession{host: "far.example"}, pane: "pane"}
			selection := selectGit(sess)
			if selection.Factory == nil {
				t.Fatalf("selection=%+v", selection)
			}
			repo, outcome, err := selection.Factory.Open(t.Context(), fixtureRepo(t))
			if err != nil || outcome.State != git.OpenOK {
				t.Fatalf("open=%+v, %v", outcome, err)
			}
			if _, err = repo.Status(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err = repo.Close(); err != nil {
				t.Fatal(err)
			}
			lease, err := far.Acquire(t.Context(), "conn")
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-lease.Client.Done():
				t.Fatal("Git close ended the far client")
			default:
			}
			if shell != nil {
				held, ok := shell.reg.hostFor(shellID)
				if !ok {
					t.Fatal("hosted shell lost its carrier")
				}
				select {
				case <-held.client.Done():
					t.Fatal("Git closed the hosted shell carrier")
				default:
				}
				if _, shellErr := held.client.Sessions(t.Context()); shellErr != nil {
					t.Fatal(shellErr)
				}
			}
			if seams.dials.Load() != 1 {
				t.Fatalf("far dials=%d, want 1", seams.dials.Load())
			}
		})
	}
}

func TestGitPausesOnHelperLossAndResumesAfterRefresh(t *testing.T) {
	far, panes, seams := gitFarStand(t, hostmode.Helper)
	selectGit := helperGitFactory(far, func() *panedriver.Driver { return panes })
	sess := &gitPaneSession{fakeRemoteSession: &fakeRemoteSession{host: "far.example"}, pane: "pane"}
	selected := selectGit(sess)
	repo, outcome, err := selected.Factory.Open(t.Context(), fixtureRepo(t))
	if err != nil || outcome.State != git.OpenOK {
		t.Fatalf("open=%+v %v", outcome, err)
	}
	defer func() { _ = repo.Close() }()
	lease, err := far.Acquire(t.Context(), "conn")
	if err != nil {
		t.Fatal(err)
	}
	if err = lease.Client.Close(); err != nil {
		t.Fatal(err)
	}
	waittest.WaitFor(t, "helper loss", func() bool { s, _ := far.Axis("conn"); return s.Reason == farhelper.ReasonHelperLost })
	refusal := selectGit(sess)
	if refusal.Factory != nil || refusal.Refusal == nil || !strings.Contains(refusal.Refusal.Message, "helper-lost") {
		t.Fatalf("selection after loss=%+v", refusal)
	}
	if _, err = repo.Status(t.Context()); err == nil {
		t.Fatal("old lease still serves after loss")
	}
	_, _, err = selected.Factory.Open(t.Context(), "/repo")
	var unavailable *farhelper.Unavailable
	if !errors.As(err, &unavailable) || unavailable.Reason != farhelper.ReasonHelperLost {
		t.Fatalf("cached factory error=%v", err)
	}
	if seams.dials.Load() != 1 {
		t.Fatal("Git redialled the lost helper")
	}
	if err = far.Refresh(t.Context(), "conn"); err != nil {
		t.Fatal(err)
	}
	waittest.WaitFor(t, "refreshed helper", func() bool { s, _ := far.Axis("conn"); return s.State == farhelper.StateServing })
	fresh, outcome, err := selected.Factory.Open(t.Context(), fixtureRepo(t))
	if err != nil || outcome.State != git.OpenOK {
		t.Fatalf("refreshed open=%+v %v", outcome, err)
	}
	_ = fresh.Close()
	if seams.dials.Load() != 2 {
		t.Fatalf("dials=%d, want only initial + refresh", seams.dials.Load())
	}
	panes.Unbind("pane")
	_, _, err = selected.Factory.Open(t.Context(), "/repo")
	if !errors.As(err, &unavailable) || unavailable.Reason != farhelper.ReasonConnection {
		t.Fatalf("unbound open=%v", err)
	}
}

func TestGitLeaseContextEndsWithServingAndKeepsRequestValues(t *testing.T) {
	type key struct{}
	lifetime, end := context.WithCancel(context.Background())
	request := context.WithValue(t.Context(), key{}, "request")
	ctx, cancel := leaseContext(request, lifetime)
	defer cancel()
	if ctx.Value(key{}) != "request" {
		t.Fatal("request context lost")
	}
	end()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("serving ended without cancelling Git")
	}
	ctx2, cancel2 := leaseContext(request, lifetime)
	defer cancel2()
	if !errors.Is(ctx2.Err(), context.Canceled) {
		t.Fatal("ended lease admitted a call")
	}
}

// blockingGitRead exposes the context the wrapper gives an in-flight read.
type blockingGitRead struct {
	git.Repo
	entered chan struct{}
}

func (r blockingGitRead) Status(ctx context.Context) (git.Status, error) {
	close(r.entered)
	<-ctx.Done()
	return git.Status{}, ctx.Err()
}

func TestGitInFlightReadEndsWhenServingEnds(t *testing.T) {
	lifetime, end := context.WithCancel(context.Background())
	defer end()
	entered := make(chan struct{})
	repo := &leasedGitRepo{Repo: blockingGitRead{entered: entered}, lifetime: lifetime}
	done := make(chan error, 1)
	go func() { _, err := repo.Status(t.Context()); done <- err }()
	<-entered
	end()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight status=%v", err)
	}
}
