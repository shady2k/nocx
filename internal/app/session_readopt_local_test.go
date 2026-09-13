package app

// A LOCAL session is taken back after the coordinator that opened it was
// replaced (nocx-ie23r.5 — the local half of nocx-k6p18.30).
//
// WHAT A USER CAN DO THAT THEY COULD NOT BEFORE, and it is what these tests
// watch: start something long in a local pane, quit nocx entirely, open it
// again, and the SAME shell is there — same session id, same process, one
// shell and not two, in the registry `sessions.live` is served from and keyed
// to the pane it was the pipe of.
//
// THESE ARE THE FAST HALF AND restart_screen_test.go IS THE REAL ONE. There
// the daemon and the shell are the SHIPPED binaries under real PTYs, which is
// what makes that test the acceptance; here the daemon is this machine's
// endpoint served by the real host protocol over a real Unix socket, with a
// scripted process behind the spawner, so the branches a real machine cannot
// be arranged into — no endpoint, a daemon that answers and holds nothing, and
// another coordinator holding the keyboard — are reachable in a second.
//
// The VERDICT half of the local route is session_reconcile_local_test.go's.
// What is added here is what the verdict cannot say: whether a pane owns the
// session afterwards, whether the daemon was asked to start a second shell,
// what the pane records about the process, and who holds the connection the
// ask was answered on.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"
)

// TestAReplacingCoordinatorTakesALiveLocalSessionBackOverTheConnectionItAskedOn
// is the criterion, in process: the daemon holds the shell, the coordinator
// that opened it is gone, and the one that replaces it makes the session a
// pane's again — over the ONE connection it asked on, and without spawning
// anything.
func TestAReplacingCoordinatorTakesALiveLocalSessionBackOverTheConnectionItAskedOn(t *testing.T) {
	home := storagetest.IsolateWithHome(t)
	// The artifact the root installs, and therefore the generation its own
	// socket is named for — the endpoint this test binds IS the generation
	// Start installs, which is what makes the route reach the fake daemon
	// rather than start a second one.
	src := fakeArtifacts{payload: syntheticPayload}
	ep := startFakeLocalEndpoint(t, endpoint.Dir(home), src.hash())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := bootLocalAppOn(t, src)
	opened, err := first.Transport.OpenSession(ctx, transport.OpenSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("opening a local pane through the shipped opener: %v", err)
	}
	sid := opened.Session.ID()
	// THE PROCESS THE DAEMON STARTED, read before the shutdown for the reason
	// restart_screen_test.go gives: closing a session releases its record of
	// the launch pid, so read afterwards it would be unknown for a reason that
	// has nothing to do with the re-attachment.
	firstPID, firstKnown := first.Session.OwnedProcessPID(sid)
	if !firstKnown || firstPID <= 0 {
		t.Fatalf("the first coordinator recorded no launch pid for %s (%d, known=%v)",
			sid, firstPID, firstKnown)
	}
	first.Shutdown(ctx)

	// The replacement. Its Start IS the re-adoption: the pass runs before the
	// server listens, so by the time this returns the session is already the
	// new coordinator's.
	second := bootLocalAppOn(t, src)

	if _, err := second.Session.Get(sid); err != nil {
		t.Fatalf("the replacing coordinator did not take session %s back: %v — this machine's daemon "+
			"still holds the shell, so the pane has no pipe and the session is live for nobody", sid, err)
	}
	// AND IT DID NOT OPEN A SECOND SHELL. This is the failure that makes the
	// bead worth having: a pane that starts a fresh shell beside the running
	// one looks identical on screen and leaves the work unreachable.
	if got := ep.spawned(); got != 1 {
		t.Fatalf("this machine's daemon spawned %d shells across the restart, want the 1 the first "+
			"coordinator asked for — the replacement opened one of its own", got)
	}
	// The process the pane is the pipe of, recorded by the replacement exactly
	// as an open records it: two decisions read that fact (worker admission's
	// root-pid check and agent approval's "is this pane ours") and a
	// re-attached pane that lost it would refuse both.
	secondPID, secondKnown := second.Session.OwnedProcessPID(sid)
	if !secondKnown || secondPID != firstPID {
		t.Fatalf("the re-attached pane records launch pid %d (known=%v), want the %d the daemon "+
			"started", secondPID, secondKnown, firstPID)
	}

	// THE CONNECTION THE ATTACHMENT RIDES IS HELD, and it is the ask's probe
	// connection that is not: one connection is what a re-attached session
	// costs. A route that released the connection it attached over would have
	// detached the session it had just recovered, and the adoption above would
	// have failed rather than this assertion.
	//
	// The count is WAITED FOR and not read once, because the daemon notices a
	// client's close on its own goroutine: the probe's release is the state
	// this waits for, and the deadline is only there so a connection nobody
	// releases fails the test instead of hanging it. The assertion is the
	// predicate itself — one connection, meaning the session's own.
	waitFor(t, "the ask's probe connection to be released, leaving the session's own",
		func() bool { return ep.liveNow() == 1 })

	// THE PANE CAN BE CLOSED, and this is not a formality: the session's
	// teardown sends its detach over the very connection the attachment rides,
	// so a connection closed behind that call turns a clean close into
	// "connection lost" — which is what this test caught when the connection
	// was tied to the session's Done channel.
	if err := second.Session.Close(sid); err != nil {
		t.Fatalf("closing the re-attached session: %v", err)
	}

	// AND THE CONNECTION GOES WITH THE COORDINATOR, which is its closing event:
	// the daemon's subscriber was released by the detach above, and what is
	// left is one idle socket per re-attached pane until the process ends.
	second.Shutdown(ctx)
	waitFor(t, "the re-attached session's connection to end with the coordinator", func() bool {
		return ep.liveNow() == 0
	})
}

// TestALocalSessionAnotherCoordinatorIsHoldingIsLeftToIt is the negative that
// keeps the re-attachment from stealing a live pane.
//
// D12 serves a second coordinator; nocx-k6p18.16 binds the write capability to
// the connection that holds it. So a coordinator that finds a local session
// whose keyboard somebody else is holding must NOT adopt it — a pane whose
// keystrokes go nowhere is worse than a pane that opens a shell of its own —
// and the verdict is still `live`, because the session exists.
//
// The connection is the second half: an attempt that refuses must not leave
// the socket it opened behind, or every restart of a busy machine leaks one
// subscriber per session it could not take.
func TestALocalSessionAnotherCoordinatorIsHoldingIsLeftToIt(t *testing.T) {
	home := storagetest.IsolateWithHome(t)
	src := fakeArtifacts{payload: syntheticPayload}
	ep := startFakeLocalEndpoint(t, endpoint.Dir(home), src.hash())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The coordinator that holds the keyboard, and it stays up.
	holder := bootLocalAppOn(t, src)
	opened, err := holder.Transport.OpenSession(ctx, transport.OpenSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("opening a local pane through the shipped opener: %v", err)
	}
	sid := opened.Session.ID()
	// The daemon accepts a connection on its own goroutine, so the holder's
	// own socket is waited for rather than read once: what the rest of the test
	// compares against is the count with the holder and nothing else, and a
	// zero read here would make the final assertion pass for the wrong reason.
	waitFor(t, "the holding coordinator's connection to be accepted", func() bool { return ep.liveNow() >= 1 })
	live := ep.liveNow()

	// A second coordinator's pass, over the same endpoint. Nothing of the
	// holder is touched: the pass reads the binding a previous incarnation
	// left and acts on what it finds.
	other := newCoordinator(t, &fakeLaneProvider{})
	adopter := &stubAdopter{}
	pass := &readoptPass{
		registry: other.reg, adopter: adopter,
		local: &localHelperOpener{dir: ep.dir, log: discardLogger()},
	}
	rec := &recordingReconciler{pending: []content.PendingSession{{
		SessionID: string(sid), Generation: src.hash(), PaneID: "pane-local",
	}}}
	reconcileSessions(ctx, rec, nil, pass, time.Hour, quietLogger())

	if len(rec.applied) != 1 || rec.applied[0].Verdict != content.VerdictLive {
		t.Fatalf("verdict = %+v, want exactly one live: the daemon holds the session, so nothing about "+
			"a refused attach may read as absent", rec.applied)
	}
	if ids := adopter.adoptedIDs(); len(ids) != 0 {
		t.Fatalf("adopted %v, want nothing — another coordinator holds this session's keyboard, and a "+
			"pane adopted here would take keystrokes that go nowhere", ids)
	}
	if err := adopter.failure(); err == nil || !strings.Contains(err.Error(), "keyboard") {
		t.Fatalf("the refusal = %v, want one naming the keyboard another nocx holds", err)
	}
	if _, err := other.sess.Get(sid); err == nil {
		t.Fatal("the second coordinator's registry holds the session after a refused attach")
	}
	// The connection the refused attempt opened is released, so what is left
	// is the holder's own.
	waitFor(t, "the refused attempt's connection to be released", func() bool {
		return ep.liveNow() == live
	})
}

// TestAReattachedPanesScreenIsReadFromTheDaemonThatHoldsIt is the frame-read
// half of the same re-attachment, and it is the half a same-generation restart
// cannot distinguish: with the binding naming the generation this coordinator
// installed, reading the frame from either connection answers.
//
// The case that tells them apart is an UPDATE — nocx replaced while the old
// daemon still holds the shell — and there the two connections lead to
// different daemons: the session is on the old one, and this build's installed
// generation is the new one. Asking the new one for the frame names a session
// it has never heard of, so a pane attached to the old shell would show output
// and no screen. The handle is what decides, and this asserts which id space
// the handle names.
func TestAReattachedPanesScreenIsReadFromTheDaemonThatHoldsIt(t *testing.T) {
	// Two generations, both well-formed content hashes: the one this build
	// installed, and the older one the binding names.
	installed := proto.GenerationID(strings.Repeat("ab", 32))
	binding := proto.GenerationID(strings.Repeat("cd", 32))
	opener := &localHelperOpener{
		dir:       t.TempDir(),
		installed: helperlocal.Installed{Binary: "/nonexistent/nocx-helper", Generation: installed},
		log:       discardLogger(),
	}
	// The connection a re-attached pane's attachment rides, as the re-adoption
	// leaves it: keyed by the session, carrying the generation its endpoint is
	// named for.
	const sid = "session-on-an-older-generation"
	conn := &helperclient.Client{}
	opener.reattached = map[session.ID]localSessionConn{
		session.ID(sid): {client: conn, generation: binding},
	}

	client, handle, err := opener.screenClient(context.Background(), sid)
	if err != nil {
		t.Fatalf("naming the pane's runtime: %v", err)
	}
	if client != conn {
		t.Fatal("the frame is read over a connection that is not the one the pane's attachment rides")
	}
	if handle.Generation != string(binding) {
		t.Fatalf("the frame is asked of generation %s, want %s — the daemon that holds the session, "+
			"not the one this build installed", handle.Generation, binding)
	}
	if handle.Session != sid {
		t.Fatalf("the frame is asked for session %s, want %s", handle.Session, sid)
	}
}
