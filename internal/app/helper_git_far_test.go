package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/git/hostsvc"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

type gitFarSeams struct {
	t            *testing.T
	probes       atomic.Int32
	installs     atomic.Int32
	dials        atomic.Int32
	peer         func(io.Reader, io.Writer) int
	probeStarted chan struct{}
	probeRelease <-chan struct{}
}

func (s *gitFarSeams) Probe(ctx context.Context, _ farhelper.Target) (farhelper.Platform, error) {
	s.probes.Add(1)
	if s.probeStarted != nil {
		close(s.probeStarted)
		select {
		case <-s.probeRelease:
		case <-ctx.Done():
			return farhelper.Platform{}, ctx.Err()
		}
	}
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
	return gitFarStandWithSeams(t, mode, &gitFarSeams{t: t, peer: helperPeerWithoutSession(t)}, true)
}

func gitFarStandWithSeams(t *testing.T, mode hostmode.Mode, seams *gitFarSeams, settled bool) (*farhelper.Registry, *panedriver.Driver, *gitFarSeams) {
	t.Helper()
	source := newFarHelperConnections()
	source.Observe(farhelper.Connection{ID: "conn", Destination: proto.SSHDestination{Host: "far.example", Port: 22, User: "dev"}, HostKeyFingerprint: "SHA256:test-host", State: hostconn.Connected, Shells: 1})
	modes := newTestHostModes(t)
	mustRecordHostMode(t, modes, "SHA256:test-host", mode)
	consent, _ := testConsentStores(t)
	far := farhelper.New(farhelper.Config{Clock: hostconntest.New(), Connections: source, Modes: modes, Consents: consent, Probe: seams, Install: seams, Dial: seams, Log: farHelperTestLogger(t)})
	t.Cleanup(far.Close)
	panes := panedriver.New("instance", farHelperTestLogger(t))
	panes.ApplyFrom("local", proto.ConnectionStateEvent{ConnectionID: "conn", Seq: 1, State: proto.ConnectionConnected, PlainShells: 1})
	panes.Bind("pane", "conn")
	if settled {
		waittest.WaitFor(t, "far helper decision", func() bool { status, ok := far.Axis("conn"); return ok && status.State != farhelper.StateStarting })
	}
	return far, panes, seams
}

func TestGitRawRefusalNamesTheFarHelperAxis(t *testing.T) {
	far, panes, seams := gitFarStand(t, hostmode.Raw)
	selectGit := helperGitFactory(far, func() *panedriver.Driver { return panes })
	for range 2 {
		got := selectGit(&gitPaneSession{fakeRemoteSession: &fakeRemoteSession{host: "far.example"}, pane: "pane"})
		if got.Factory != nil || got.Refusal == nil || got.Refusal.State != "" ||
			strings.Contains(got.Refusal.Message, "mode-raw") ||
			!strings.Contains(got.Refusal.Message, "Raw") ||
			!strings.Contains(got.Refusal.Message, "nocx helper") {
			t.Fatalf("selection = %+v, want a Raw phrase naming the nocx helper", got)
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
	if refusal.Factory != nil || refusal.Refusal == nil ||
		strings.Contains(refusal.Refusal.Message, "helper-lost") ||
		!strings.Contains(refusal.Refusal.Message, "nocx helper") {
		t.Fatalf("selection after loss=%+v, want a phrase naming the nocx helper", refusal)
	}
	_, err = repo.Status(t.Context())
	assertGitUnavailable(t, err, farhelper.ReasonHelperLost, farhelper.ActionRefresh)
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
	far, _, _ := gitFarStand(t, hostmode.Raw)
	repo := &leasedGitRepo{Repo: blockingGitRead{entered: entered}, lifetime: lifetime, far: far, connectionID: "conn"}
	done := make(chan error, 1)
	go func() { _, err := repo.Status(t.Context()); done <- err }()
	<-entered
	end()
	assertGitUnavailable(t, <-done, farhelper.ReasonModeRaw, farhelper.ActionHostSettings)
}

func assertGitUnavailable(t *testing.T, err error, reason farhelper.Reason, action farhelper.Action) {
	t.Helper()
	var unavailable *farhelper.Unavailable
	if !errors.As(err, &unavailable) || unavailable.Reason != reason || unavailable.Action != action {
		t.Fatalf("error = %v, want far helper unavailable %s (%s)", err, reason, action)
	}
}

func TestGitStartingSelectionWaitsForDecision(t *testing.T) {
	for _, finish := range []bool{true, false} {
		t.Run(map[bool]string{true: "serves", false: "bounded"}[finish], func(t *testing.T) {
			old := gitOpenTimeout
			if !finish {
				gitOpenTimeout = 100 * time.Millisecond
			}
			defer func() { gitOpenTimeout = old }()
			release := make(chan struct{})
			seams := &gitFarSeams{t: t, peer: helperPeerWithoutSession(t), probeStarted: make(chan struct{}), probeRelease: release}
			far, panes, _ := gitFarStandWithSeams(t, hostmode.Helper, seams, false)
			<-seams.probeStarted
			selected := helperGitFactory(far, func() *panedriver.Driver { return panes })(&gitPaneSession{fakeRemoteSession: &fakeRemoteSession{host: "far.example"}, pane: "pane"})
			if selected.Factory == nil || selected.Refusal != nil {
				t.Fatalf("starting selection = %+v, want factory, not an empty-reason refusal", selected)
			}
			cwd := fixtureRepo(t)
			if finish {
				close(release)
				repo, outcome, err := selected.Factory.Open(t.Context(), cwd)
				if err != nil || outcome.State != git.OpenOK {
					t.Fatalf("open = %+v, %v", outcome, err)
				}
				_ = repo.Close()
			} else {
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				defer cancel()
				_, _, err := selected.Factory.Open(ctx, cwd)
				if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
					t.Fatalf("acquire = %v, caller = %v; want internal deadline", err, ctx.Err())
				}
			}
		})
	}
}

func TestGitEndedLeaseRefusesEveryBindingCall(t *testing.T) {
	far, panes, _ := gitFarStand(t, hostmode.Helper)
	selected := helperGitFactory(far, func() *panedriver.Driver { return panes })(&gitPaneSession{fakeRemoteSession: &fakeRemoteSession{host: "far.example"}, pane: "pane"})
	repo, _, err := selected.Factory.Open(t.Context(), fixtureRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	far.CloseFor("SHA256:test-host")
	calls := map[string]func() error{
		"status":               func() error { _, e := repo.Status(t.Context()); return e },
		"diff":                 func() error { _, e := repo.Diff(t.Context(), "f.txt", git.Side("worktree"), 100); return e },
		"log":                  func() error { _, e := repo.Log(t.Context(), 1); return e },
		"stage":                func() error { _, e := repo.Stage(t.Context(), []string{"f.txt"}); return e },
		"unstage":              func() error { _, e := repo.Unstage(t.Context(), []string{"f.txt"}); return e },
		"stageAll":             func() error { _, e := repo.StageAll(t.Context()); return e },
		"unstageAll":           func() error { _, e := repo.UnstageAll(t.Context()); return e },
		"commit":               func() error { _, e := repo.Commit(t.Context(), "message", false); return e },
		"headMessage":          func() error { _, e := repo.HeadMessage(t.Context()); return e },
		"remoteURL":            func() error { _, e := repo.RemoteURL(t.Context()); return e },
		"addWorktree":          func() error { _, e := repo.AddWorktree(t.Context(), "branch", "base", "path"); return e },
		"worktrees":            func() error { _, e := repo.Worktrees(t.Context(), "base"); return e },
		"removeWorktree":       func() error { _, e := repo.RemoveWorktree(t.Context(), "path", git.RemoveWorktreeOptions{}); return e },
		"deleteWorktreeBranch": func() error { return repo.DeleteWorktreeBranch(t.Context(), "branch", "base") },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) { assertGitUnavailable(t, call(), farhelper.ReasonHelperLost, farhelper.ActionNone) })
	}
}

type waitingGitOpen struct{ entered chan struct{} }

func (f waitingGitOpen) Open(ctx context.Context, _ string) (git.Repo, git.OpenOutcome, error) {
	close(f.entered)
	<-ctx.Done()
	return nil, git.OpenOutcome{}, ctx.Err()
}

func TestGitOpenLosingAcquiredLeaseNamesCurrentAxis(t *testing.T) {
	entered := make(chan struct{})
	seams := &gitFarSeams{t: t, peer: func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, syntheticArtifactHash, "instance-1", discardLogger(t))
		h.Register(hostsvc.New(waitingGitOpen{entered: entered}))
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}}
	far, panes, _ := gitFarStandWithSeams(t, hostmode.Helper, seams, true)
	selected := helperGitFactory(far, func() *panedriver.Driver { return panes })(&gitPaneSession{fakeRemoteSession: &fakeRemoteSession{host: "far.example"}, pane: "pane"})
	done := make(chan error, 1)
	go func() { _, _, err := selected.Factory.Open(t.Context(), "/repo"); done <- err }()
	<-entered
	far.CloseFor("SHA256:test-host")
	assertGitUnavailable(t, <-done, farhelper.ReasonHelperLost, farhelper.ActionNone)
}

type waitingGitCommit struct {
	git.Repo
	entered chan struct{}
	release chan struct{}
}

func (r waitingGitCommit) Commit(ctx context.Context, _ string, _ bool) (git.CommitOutcome, error) {
	// The helper mutation seam removes cancellation before dispatch (D11).
	ctx = context.WithoutCancel(ctx)
	close(r.entered)
	<-r.release
	if err := ctx.Err(); err != nil {
		return git.CommitOutcome{}, err
	}
	return git.CommitOutcome{State: git.CommitIndeterminate}, nil
}

func TestGitAdmittedCommitKeepsIndeterminateOnLeaseLoss(t *testing.T) {
	lifetime, end := context.WithCancel(t.Context())
	defer end()
	far, _, _ := gitFarStand(t, hostmode.Raw)
	commit := waitingGitCommit{entered: make(chan struct{}), release: make(chan struct{})}
	repo := &leasedGitRepo{Repo: commit, lifetime: lifetime, far: far, connectionID: "conn"}
	done := make(chan struct{})
	go func() {
		defer close(done)
		outcome, err := repo.Commit(t.Context(), "message", false)
		if err != nil || outcome.State != git.CommitIndeterminate {
			t.Errorf("commit = %+v, %v; want indeterminate", outcome, err)
		}
	}()
	<-commit.entered
	end()
	close(commit.release)
	<-done
}
