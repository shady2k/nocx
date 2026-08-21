package ssh

import (
	"errors"
	"testing"
	"time"
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
