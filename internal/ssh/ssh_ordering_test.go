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
	installer := &recordInstaller{}
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
// The slot: what nocx's own auxiliary work may spend on the pane's connection.

// delegationInstaller is a RemoteInstaller that records what internal/ssh hands
// it and makes no far-side move of its own.
//
// It CANNOT make one: the interface passes a destination, not a client
// (nocx-50w7p.15), and that is the fact these two tests now measure. The old
// pair of tests here drove a double that opened a session channel of its own to
// prove the user's slot was claimed first — and that competition is gone
// rather than reordered, because the publish rides this machine's helper's
// connection and the pane's connection carries the user's session and nothing
// else.
type delegationInstaller struct {
	mu       sync.Mutex
	called   bool
	host     string
	opts     int
	publishE error
}

func (d *delegationInstaller) EnsureInstalledRemote(_ context.Context, host string, opts ...ConnectOption) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.called, d.host, d.opts = true, host, len(opts)
	return d.publishE
}

func (d *delegationInstaller) UninstallRemote(context.Context, *gossh.Client) ([]string, []string, error) {
	return nil, nil, nil
}

func (d *delegationInstaller) snapshot() (bool, string, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.called, d.host, d.opts
}

// The pane's own connection carries ONE session channel — the user's — and the
// publish is delegated with the address the pane dialed.
//
// ADR-0004 makes an ordinary usable terminal absolute, and the one way to lose
// it that is nocx's own fault is to spend the server's last session slot on
// nocx's own work. That risk lived in nocx opening an auxiliary channel here;
// it is retired by the auxiliary channel no longer being here at all.
//
// Asserted as a state and not as a race: the server says how many session
// channels it granted on that connection, and the carrier says it was handed
// the destination. Running it repeatedly and hoping would assert nothing,
// which is exactly what the pinned e2e ordering used to admit.
func TestConnect_ThePanesConnectionCarriesOnlyTheUsersSession(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true}
	installer := &delegationInstaller{}
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
	// terminal outcome, so the reads below are made after it and never after a
	// sleep.
	waitGate(t, launcher)
	called, host, _ := installer.snapshot()
	if !called {
		t.Fatal("the publish was never delegated, so this test measured nothing")
	}
	if host != srv.addr {
		t.Errorf("the publish was handed destination %q, want the address the pane dialed (%q) — the helper keys its pool by what that resolves to", host, srv.addr)
	}
	if n := srv.sessionChannelCount(); n != 1 {
		t.Errorf("the pane's connection was asked for %d session channels, want exactly 1 (the user's): nocx's own work belongs on this machine's helper's connection, and a server at its MaxSessions bound must never have to refuse the user's shell to make room for it", n)
	}
	assertUsable(t, srv, ch)
}

// And the consequence, at the bound the epic's acceptance criterion names: a
// server with ONE session slot leaves the user a working prompt, and the
// delegated publish still reaches a terminal outcome of its own.
//
// One run decides it. Under the old shape the publish competed for that slot
// on this connection; it now competes for nothing here, and the publish's own
// transport is the helper's problem (its own acceptance test asserts that it
// costs one authentication and no second login).
func TestConnect_OneSessionSlotGoesToTheUserAndNotToNocx(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()
	srv.setMaxSessions(1)

	launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true}
	installer := &delegationInstaller{}
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
	if _, _, perr := g.snapshot(); perr != nil {
		t.Errorf("the delegated publish reported %v; it was handed a destination and no client, so this connection's slot is not something it can have taken", perr)
	}
	called, _, _ := installer.snapshot()
	if !called {
		t.Fatal("the publish was never delegated, so the refusal this test is about never happened")
	}
	if n := srv.sessionChannelCount(); n != 1 {
		t.Errorf("the one slot was spent on %d session channels, want exactly 1", n)
	}
	// The user's terminal is the point of all of it.
	assertUsable(t, srv, ch)
}
