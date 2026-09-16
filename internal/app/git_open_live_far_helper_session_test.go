package app

// nocx-xn63t.6.3: git.open over a live far-helper connection kills the
// session's lifecycle channel in about 3ms, and with it the whole session.
//
// internal/helper/client's own
// TestGitOpenOnALiveConnectionLeavesTheSessionsLifecycleChannelOpen proved
// the wire dispatcher itself — host.Host and client.Client, the session and
// git services registered on ONE connection exactly as cmd/nocx-helper's
// serve() registers them — survives concurrent, racing git-service traffic
// on a connection already carrying a live PTY and lifecycle plane. That
// rules out internal/helper/host, internal/helper/session and
// internal/helper/proto, which the bead names as the suspects.
//
// This file drives the SAME traffic one level up: through the actual
// production composition root (helperGitFactory, helperRegistry,
// hostHelper, sessionFactory — internal/app/helper_git.go) that decides
// whether a git.open reuses a session's own hostHelper, using the SAME
// harness session_readopt_lifecycle_test.go already built for "a helper
// whose shells actually hold a lifecycle channel": a real lifecycle.Kernel
// behind a real lifecyclepub.Publisher, a real shell on the far end of a
// net.Pipe speaking the real wire protocol, and the real git service
// (hostsvc over internal/git/local) on the SAME shared daemon. It complements
// the internal/helper regression rather than duplicating it: this is the
// only level at which helperGitFactory's per-git.open re-probe/re-install
// cycle (probeHelperPlatform + installHelperFor, run again on every git.open
// even for an already-installed, already-connected session) can be exercised
// at all.
import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/waittest"
)

// TestGitOpenOnALiveFarHelperConnectionLeavesTheSessionAndLifecycleChannelOpen
// is the production-composition twin of the internal/helper regression: a
// real helperGitFactory git.RepoFactory, obtained exactly as
// ws_git.go's handleOpen obtains one per git.open request, reused
// concurrently while the SAME session's lifecycle channel is actively
// carrying real protocol traffic.
//
// THE INVARIANT, BOTH ENDS NAMED: the lifecycle lane a helper-hosted open
// starts (helperRegistry.openFarHelper's StartLifecycle) is required to stay
// live from that start until this coordinator's own quit/close — an
// unrelated git.open on the session's shared hostHelper is not a closing
// event for it. reg.lifecycleLoss is wired below to prove the negative
// directly: it must never fire while git.open traffic runs.
func TestGitOpenOnALiveFarHelperConnectionLeavesTheSessionAndLifecycleChannelOpen(t *testing.T) {
	spawner := &lifecycleSpawner{}
	svc := helperWithIntegratedShells(spawner)
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}

	source := stubArtifacts(t)
	store, installs := testConsentStores(t)
	gitFor, reg := helperGitFactory(provider, source, store, installs, discardLogger())
	sessReg := session.New(log.NewSlogAdapter(discardLogger()), nil)
	reg.registry = sessReg
	pub := lifecyclepub.New(lifecycle.New(lifecycle.Options{}))
	emitter := &recordingEmitter{}
	pub.SetEmitter(emitter)
	reg.lifecycle = pub

	// Observe the lifecycle adapter's own loss sink directly (the report
	// path openFarHelper wires as lifecyclechannel.WithLossReporter): this
	// is the exact signal the bead's log line names ("the lifecycle channel
	// reports end-of-stream"), and it must never fire from git.open traffic
	// alone.
	var lossMu sync.Mutex
	var losses []lifecyclechannel.LossCause
	reg.lifecycleLoss = func(_ lifecycle.LaneID, cause lifecyclechannel.LossCause) {
		lossMu.Lock()
		losses = append(losses, cause)
		lossMu.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	opened, selected, err := reg.OpenHosted(ctx, session.Config{
		Kind: session.KindRemote, Host: "host.example", Cwd: t.TempDir(),
		PaneID: "pane-1", ProfileID: "profile-1",
		Remote: &ssh.ConnectConfig{User: "u", DesiredMode: "helper"},
	}, "")
	if err != nil {
		t.Fatalf("opening a helper-hosted session: %v", err)
	}
	if !selected {
		t.Fatal("the helper was not selected for a consented machine")
	}
	if opened.StartLifecycle == nil {
		t.Fatal("the open carried no lifecycle lane to start — the harness is not exercising the scenario under test")
	}
	opened.StartLifecycle()
	t.Cleanup(func() {
		if opened.AbortLifecycle != nil {
			opened.AbortLifecycle()
		}
	})

	shell, _ := spawner.theShell(t)
	shell.drain()
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}})
	waittest.WaitFor(t, "the shell integrated with the coordinator", func() bool {
		return len(emitter.any()) > 0
	})

	repo := fixtureRepo(t)
	sel := gitFor(opened.Session)
	if sel.Factory == nil {
		t.Fatalf("git.open selection carried no factory: refusal=%+v", sel.Refusal)
	}
	if got := provider.laneCount(); got != 1 {
		t.Fatalf("resolving the git.open selection brought up %d lane(s), want 1 — it must reuse the session's own hostHelper, never dial a second one", got)
	}

	// A concurrent "shell keeps working" heartbeat, one command per loop, so
	// the lifecycle plane is not idle while git.open traffic runs beside it
	// — exactly the shape the bead's own e2e failure has (a live pane, an
	// autoloaded git panel).
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		for i := 0; i < 10; i++ {
			attempt := lifecycle.AttemptID("heartbeat")
			shell.send(t, lifecycle.Event{Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &attempt, Command: "true"}})
			code := 0
			shell.send(t, lifecycle.Event{Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{ExitCode: &code, Fence: lifecycle.FenceNonce{byte(i + 1)}}})
		}
	}()

	// THE TRAFFIC UNDER TEST: git.open, concurrently and repeatedly, through
	// the SAME selection a real git.open request resolves per call
	// (ws_git.go calls h.helperFor(sess) fresh on every request) — reusing
	// the session's own hostHelper (helperRegistry.helper keys by session
	// id) exactly as production does once a pane's PTY is opened through the
	// far helper from the start (ADR-0057/ADR-0068).
	const openers = 6
	const opensEach = 10
	errCh := make(chan error, openers*opensEach)
	var wg sync.WaitGroup
	for w := 0; w < openers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < opensEach; i++ {
				s := gitFor(opened.Session)
				if s.Factory == nil {
					errCh <- fmt.Errorf("git.open selection carried no factory: refusal=%+v", s.Refusal)
					return
				}
				repoHandle, outcome, err := s.Factory.Open(ctx, repo)
				if err != nil {
					errCh <- fmt.Errorf("git.open: %w", err)
					return
				}
				if outcome.State != git.OpenOK {
					errCh <- fmt.Errorf("git.open: state=%q message=%q", outcome.State, outcome.Message)
					return
				}
				_ = repoHandle.Close()
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	<-heartbeatDone

	// One more real op, to prove the shared client this test's own
	// resolutions reused is still the one live — not a fresh, silently
	// re-dialed one that just happens to answer the same way.
	{
		s := gitFor(opened.Session)
		repoHandle, outcome, err := s.Factory.Open(ctx, repo)
		if err != nil {
			t.Fatalf("git.open after the traffic above: %v", err)
		}
		if outcome.State != git.OpenOK {
			t.Fatalf("git.open after the traffic above: state=%q message=%q", outcome.State, outcome.Message)
		}
		_ = repoHandle.Close()
	}
	// THE DIRECT PROOF the session's connection was never redialed: a
	// silently-healed ErrLost (sessionFactory.Open's own one-retry-on-loss)
	// would have brought the lane count to 2 while still answering every
	// git.open with OpenOK, which is exactly the shape that let this defect
	// hide behind a passing git panel while the pane underneath it died.
	if got := provider.laneCount(); got != 1 {
		t.Fatalf("git.open traffic brought up %d lane(s) in total, want 1 — a second lane means the shared connection was lost and silently redialed", got)
	}

	select {
	case <-opened.Session.Done():
		t.Fatal("the session ended while git.open ran on its shared hostHelper")
	default:
	}

	lossMu.Lock()
	got := append([]lifecyclechannel.LossCause(nil), losses...)
	lossMu.Unlock()
	if len(got) != 0 {
		t.Fatalf("the lifecycle channel reported loss while git.open traffic ran on the shared connection: %v", got)
	}

	// The heartbeat kept landing throughout: if the lifecycle plane had died
	// silently (a hang the loss reporter never saw), no further facts would
	// have been published after the git.open traffic started.
	if n := len(emitter.any()); n < 2 {
		t.Fatalf("only %d lifecycle fact(s) were published; the heartbeat commands during git.open traffic produced none", n)
	}
}

// TestGitOpenRefusingNotARepositoryOnALiveFarHelperConnectionLeavesTheSession
// is the OTHER site this bead's fix touches: hostHelper.open's own
// refusing-outcome branch, hit whenever git.open resolves to anything but
// OpenOK — the ORDINARY answer for the pane's very first git.open, made
// before whatever directory the shell started in is known to hold a
// repository at all. Before the fix this ran with h.refs == 0 (a session
// hosted here has never had a successful git binding to count) and closed
// the shared client on exactly that basis, tearing down the session on the
// most common possible git.open answer.
func TestGitOpenRefusingNotARepositoryOnALiveFarHelperConnectionLeavesTheSession(t *testing.T) {
	spawner := &lifecycleSpawner{}
	svc := helperWithIntegratedShells(spawner)
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}

	source := stubArtifacts(t)
	store, installs := testConsentStores(t)
	gitFor, reg := helperGitFactory(provider, source, store, installs, discardLogger())
	sessReg := session.New(log.NewSlogAdapter(discardLogger()), nil)
	reg.registry = sessReg
	pub := lifecyclepub.New(lifecycle.New(lifecycle.Options{}))
	emitter := &recordingEmitter{}
	pub.SetEmitter(emitter)
	reg.lifecycle = pub

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opened, selected, err := reg.OpenHosted(ctx, session.Config{
		Kind: session.KindRemote, Host: "host.example", Cwd: t.TempDir(),
		PaneID: "pane-1", ProfileID: "profile-1",
		Remote: &ssh.ConnectConfig{User: "u", DesiredMode: "helper"},
	}, "")
	if err != nil {
		t.Fatalf("opening a helper-hosted session: %v", err)
	}
	if !selected {
		t.Fatal("the helper was not selected for a consented machine")
	}
	if opened.StartLifecycle != nil {
		opened.StartLifecycle()
	}
	t.Cleanup(func() {
		if opened.AbortLifecycle != nil {
			opened.AbortLifecycle()
		}
	})

	shell, _ := spawner.theShell(t)
	shell.drain()
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}})
	waittest.WaitFor(t, "the shell integrated with the coordinator", func() bool {
		return len(emitter.any()) > 0
	})

	// A directory with no .git in it: the ordinary notARepository answer,
	// not a failure of anything.
	notARepo := t.TempDir()
	sel := gitFor(opened.Session)
	if sel.Factory == nil {
		t.Fatalf("git.open selection carried no factory: refusal=%+v", sel.Refusal)
	}
	repoHandle, outcome, err := sel.Factory.Open(ctx, notARepo)
	if err != nil {
		t.Fatalf("git.open on a non-repository directory: %v", err)
	}
	if outcome.State != git.OpenNotARepository {
		t.Fatalf("git.open on a non-repository directory: state=%q, want %q", outcome.State, git.OpenNotARepository)
	}
	if repoHandle != nil {
		t.Fatalf("a refusing outcome carried a repo handle: %+v", repoHandle)
	}

	// closeLocked's own effect on the shared client is asynchronous from
	// here: Close ends the lane, and the client's read pump only notices
	// and calls lose() once it next runs. A handful of further, real round
	// trips give that goroutine every chance to run before the check below
	// — deterministic because each is a real Call that either answers on
	// the still-live client (want) or observes ErrLost and forces
	// sessionFactory.Open's own one retry, which redials and shows up in
	// laneCount (a fact, not a clock).
	for i := 0; i < 5; i++ {
		s := gitFor(opened.Session)
		if _, _, err := s.Factory.Open(ctx, notARepo); err != nil {
			t.Fatalf("git.open[%d] after the refusing open: %v", i, err)
		}
	}

	select {
	case <-opened.Session.Done():
		t.Fatal("the session ended after a refusing (notARepository) git.open on its shared hostHelper")
	default:
	}
	if got := provider.laneCount(); got != 1 {
		t.Fatalf("git.open traffic brought up %d lane(s), want 1 — a second lane means the refusing open closed the shared client and it had to be silently redialed", got)
	}
}
