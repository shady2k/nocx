package ssh

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// §6.1's ordering and §5.3's hard invalidation, at the seam that owns them.
//
// This package produces the two facts the mint waits on and it holds the
// lifecycle handle, so it is the only place the two properties below can be
// asserted at all: that the publish runs CONCURRENTLY with the loader rather
// than ahead of it, and that a refused bootstrap INVALIDATES the epoch instead
// of leaving it live until the tab closes.

// waitGate blocks until the gate has both facts, and fails rather than
// hanging. The timeout is a failsafe against a hang and never the measurement:
// the facts are events the connect path reports, and the assertion is that
// they arrive at all.
func waitGate(t *testing.T, l *fakeLauncher) *recordingGate {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		l.mu.Lock()
		g := l.gate
		l.mu.Unlock()
		if g != nil {
			if r, p, _ := g.snapshot(); r != "" && p {
				return g
			}
		}
		select {
		case <-deadline:
			t.Fatal("the §6.1 gate never received both facts; a mint would wait for one of them forever")
		default:
		}
	}
}

// §6.1 step 4 and step 5 both reach the gate on an ordinary integrated open.
// Without both, MintGate.Await blocks and the far side sits on its bounded
// timeout with a session stuck in `starting` — which §7 forbids outright.
func TestConnect_BothOrderingFactsReachTheGate(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true}
	installer := &recordInstaller{home: "/home/test"}
	lc := &fakeRemoteLifecycle{launch: RemoteLifecycleLaunch{
		Lane: "lane-1", Domain: "dom-1", Epoch: 7, Port: 40000,
		Capability: "aa", Recovery: "bb",
	}}
	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithRemoteInstaller(installer),
		WithRemoteLifecycle(lc),
		WithDesiredMode("script"),
		WithSessionID("sess-ordering"),
		WithEnhanced(),
	)
	t.Cleanup(func() { _ = ch.Close() })

	g := waitGate(t, launcher)
	receiver, published, perr := g.snapshot()
	if receiver != "ready" {
		t.Errorf("receiver fact = %q, want ready — the lifecycle channel was established", receiver)
	}
	if !published {
		t.Error("the publish never reported a terminal outcome; the mint would wait for it forever")
	}
	if perr != nil {
		t.Errorf("publish error = %v, want nil", perr)
	}
}

// With the lifecycle channel refused, step 4 is answered with a REFUSAL rather
// than left unanswered. The difference is the whole of assertion 12: an
// unanswered fact is a mint that waits; a refusal is a mint that never
// happens and a far side that is told so.
func TestConnect_ARefusedLifecycleChannelAnswersTheGate(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true}
	lc := &fakeRemoteLifecycle{refuse: errors.New("ssh: tcpip-forward denied")}
	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithRemoteLifecycle(lc),
		WithDesiredMode("script"),
		WithSessionID("sess-ordering-refused"),
		WithEnhanced(),
	)
	t.Cleanup(func() { _ = ch.Close() })

	g := waitGate(t, launcher)
	if receiver, _, _ := g.snapshot(); receiver != "unavailable" {
		t.Errorf("receiver fact = %q, want unavailable", receiver)
	}
}

// THE HARD INVALIDATION (design §5.3, and what bounds §6.1's remaining race).
//
// A bootstrap that reaches any terminal outcome other than `accepted` closes
// the lifecycle handle, which ends the domain: a frame of that epoch is
// rejected from then on. Before this the transport stayed live until the
// session ended, so a refusal left a valid epoch behind it for as long as the
// tab was open — and a forged STAGE_READY that outran an honest refusal would
// have produced a bearer good for the whole session rather than one that dies
// with the outcome it outran.
func TestConnect_ARefusedBootstrapInvalidatesTheEpoch(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	launcher := &fakeLauncher{
		cmd: "exec bash -i", reason: ReasonNone, ok: true,
		bootstrapReason: ReasonStageDigestMismatch,
	}
	lc := &fakeRemoteLifecycle{launch: RemoteLifecycleLaunch{
		Lane: "lane-1", Domain: "dom-1", Epoch: 7, Port: 40000,
		Capability: "aa", Recovery: "bb",
	}}
	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithRemoteLifecycle(lc),
		WithDesiredMode("script"),
		WithSessionID("sess-invalidate"),
		WithEnhanced(),
	)
	t.Cleanup(func() { _ = ch.Close() })

	waitBootstrapped(t, ch)
	if !lc.wasClosed() {
		t.Error("a refused bootstrap left the lifecycle handle open; the epoch stays valid " +
			"for the life of the tab and a raced bearer outlives the refusal it outran")
	}
}

// And the converse, which is what makes the assertion above a claim rather
// than "we close it always": an ACCEPTED bootstrap keeps the channel. A
// session that integrated must not have its own epoch invalidated by the
// event that says it worked.
func TestConnect_AnAcceptedBootstrapKeepsTheChannel(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true}
	lc := &fakeRemoteLifecycle{launch: RemoteLifecycleLaunch{
		Lane: "lane-1", Domain: "dom-1", Epoch: 7, Port: 40000,
		Capability: "aa", Recovery: "bb",
	}}
	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithRemoteLifecycle(lc),
		WithDesiredMode("script"),
		WithSessionID("sess-accepted"),
		WithEnhanced(),
	)
	t.Cleanup(func() { _ = ch.Close() })

	waitBootstrapped(t, ch)
	if lc.wasClosed() {
		t.Error("an accepted bootstrap closed the lifecycle handle; the session integrated and " +
			"the invalidation killed the epoch it just proved")
	}
}

// ---------------------------------------------------------------------------
// The slot: whose session channel is opened first, and what a bounded server
// therefore refuses.

// slotProbeInstaller is a RemoteInstaller that makes the same far-side move
// the real one does — it opens a session channel on the connection — and
// records the server's view at the moment it did.
//
// It is the observable the ordering assertion needs. "The publish ran after
// the shell" cannot be read off a duration or a goroutine, but "how many
// session channels had the server granted when nocx asked for its auxiliary
// one" is a state, and it is the state the whole defect is about.
type slotProbeInstaller struct {
	srv *testSSHServer

	mu sync.Mutex
	// called records that the first far-side call happened at all, so a
	// publish that never ran cannot be mistaken for one that behaved.
	called bool
	// grantedBefore is the server's session-channel count as this call
	// started, and auxErr is what the server answered our own open with.
	grantedBefore int
	auxErr        error
}

func (p *slotProbeInstaller) GetRemoteHome(client *gossh.Client) (string, error) {
	before := p.srv.sessionChannelCount()
	sess, err := client.NewSession()
	if err == nil {
		_ = sess.Close()
	}
	p.mu.Lock()
	if !p.called {
		p.called, p.grantedBefore, p.auxErr = true, before, err
	}
	p.mu.Unlock()
	if err != nil {
		return "", err
	}
	return "/home/test", nil
}

func (p *slotProbeInstaller) EnsureInstalledRemote(context.Context, *gossh.Client, string) error {
	return nil
}

func (p *slotProbeInstaller) UninstallRemote(context.Context, *gossh.Client, string) ([]string, []string, error) {
	return nil, nil, nil
}

func (p *slotProbeInstaller) snapshot() (bool, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.called, p.grantedBefore, p.auxErr
}

// The user's interactive session channel exists BEFORE nocx opens an auxiliary
// one. ADR-0004 makes an ordinary usable terminal absolute, and the one way to
// lose it that is nocx's own fault is to spend the server's last session slot
// on nocx's own work — so the claim on that slot is the product's, not the Go
// scheduler's.
//
// Asserted as a state and not as a race: the server says how many session
// channels it had granted at the instant the publish made its first far-side
// call, and the answer must already include the user's. Running it repeatedly
// and hoping would assert nothing, which is exactly what the pinned e2e
// ordering used to admit.
func TestConnect_TheInteractiveSessionIsClaimedBeforeAnyAuxiliaryChannel(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true}
	installer := &slotProbeInstaller{srv: srv}
	lc := &fakeRemoteLifecycle{launch: RemoteLifecycleLaunch{
		Lane: "lane-slot", Domain: "dom-slot", Epoch: 7, Port: 40000,
		Capability: "aa", Recovery: "bb",
	}}
	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithRemoteInstaller(installer),
		WithRemoteLifecycle(lc),
		WithDesiredMode("script"),
		WithSessionID("sess-slot-order"),
		WithEnhanced(),
	)
	t.Cleanup(func() { _ = ch.Close() })

	// The gate's publish fact is the event that says the publish reached a
	// terminal outcome, so the snapshot below is read after it and never
	// after a sleep.
	waitGate(t, launcher)
	called, granted, auxErr := installer.snapshot()
	if !called {
		t.Fatal("the publish never reached the far side, so this test measured nothing")
	}
	if auxErr != nil {
		t.Fatalf("the auxiliary channel was refused by an unbounded server: %v", auxErr)
	}
	if granted < 1 {
		t.Errorf("the server had granted %d session channels when nocx opened its auxiliary one; "+
			"the user's interactive session had not claimed its slot yet, so a server at its "+
			"MaxSessions bound would have refused the user's shell and Connect would return no "+
			"terminal at all", granted)
	}
}

// And the consequence, at the bound the epic's acceptance criterion names: a
// server with ONE session slot leaves the user a working prompt and refuses
// nocx's auxiliary channel instead of the other way round.
//
// One run decides it. The refusal is not a race the test hopes to win — the
// slot is taken before the publish exists — so a single attempt is the whole
// assertion, and a second would only be the same statement made twice.
func TestConnect_OneSessionSlotGoesToTheUserAndRefusesThePublish(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()
	srv.setMaxSessions(1)

	launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true}
	installer := &slotProbeInstaller{srv: srv}
	lc := &fakeRemoteLifecycle{launch: RemoteLifecycleLaunch{
		Lane: "lane-one-slot", Domain: "dom-one-slot", Epoch: 7, Port: 40000,
		Capability: "aa", Recovery: "bb",
	}}
	// Connect returning at all is half the assertion: under the old ordering
	// the publish took the slot first and this call failed with
	// "new session: ssh: rejected", leaving the user no terminal.
	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithRemoteInstaller(installer),
		WithRemoteLifecycle(lc),
		WithDesiredMode("script"),
		WithSessionID("sess-one-slot"),
		WithEnhanced(),
	)
	t.Cleanup(func() { _ = ch.Close() })

	g := waitGate(t, launcher)
	if _, _, perr := g.snapshot(); perr == nil {
		t.Error("the publish reported success under one session slot; it can only have got the " +
			"slot the user's shell needs")
	}
	called, granted, auxErr := installer.snapshot()
	if !called {
		t.Fatal("the publish never reached the far side, so the refusal this test is about never happened")
	}
	if granted < 1 {
		t.Errorf("the server had granted %d session channels when nocx asked for its own", granted)
	}
	if auxErr == nil {
		t.Error("the auxiliary channel was GRANTED under one session slot: nocx took the slot, " +
			"and a shell opened after it would have had none")
	}
	// The user's terminal is the point of all of it.
	assertUsable(t, srv, ch)
}
