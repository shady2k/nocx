package app

// A dropped transport takes its shell back, and only a CONFIRMED absence opens
// another (nocx-oalo1.7.2, design §5.4, C16).
//
// WHAT A USER CAN DO THAT THEY COULD NOT BEFORE, and what these tests watch: a
// session the far helper is still holding comes back under the SAME id after
// the connection that carried it died — the coordinator asks the helper's
// inventory, finds the shell, and adopts it again without spawning anything.
// The other two answers are asserted separately because they are different
// facts with different consequences: a helper that answered and does not hold
// it is the one path to a replacement, and a shell this process holds no
// carrier for is a WAIT, never a replacement.
//
// THE HARNESS IS THE RESTART PATH'S (session_readopt_test.go): the same shipped
// opener, the same helper daemon behind every lane, the same session service.
// What differs is where the coordinator is in its own life: this is a transport
// loss inside one process, so the session is closed locally and the carrier
// that opened it is still registered — which is the fact the whole reconnection
// path is built on.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
)

// reattachFixture is one coordinator, one helper daemon, and one live hosted
// shell whose transport has just been lost.
type reattachFixture struct {
	coordinator *coordinator
	svc         *helpersession.Service
	tp          *transport.WSServer
	binding     string
	paneID      string
	reattacher  *paneShellReattacher
}

func newReattachFixture(t *testing.T) *reattachFixture {
	t.Helper()
	svc := helpersession.New(helpersession.Options{
		Generation: proto.GenerationID(syntheticArtifactHash),
		Spawner:    &scriptedSpawner{},
		Log:        discardLogger(t),
	})
	// The daemon ends with the test that started it, so the sessions it holds
	// do too (nocx-1ebrs.66.1.29).
	t.Cleanup(svc.Close)
	provider := &fakeLaneProvider{owner: t.Name(), peer: sharedHelperPeer(t, svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-rewire")
	tp := transport.NewWSServer(log.NewSlogAdapter(discardLogger(t)), first.sess)
	if err := tp.Start(context.Background()); err != nil {
		t.Fatalf("start the transport half: %v", err)
	}
	t.Cleanup(func() { _ = tp.Stop(context.Background()) })
	return &reattachFixture{
		coordinator: first, svc: svc, tp: tp,
		binding: binding.SessionID, paneID: binding.PaneID,
		reattacher: &paneShellReattacher{
			registry: first.reg,
			pass:     &readoptPass{registry: first.reg, adopter: tp},
			timeout:  30 * time.Second,
		},
	}
}

// lose closes the session the way a dead carrier does — the coordinator lets go
// of the session, the helper keeps the shell — and leaves the registry without
// it, which is what makes an adoption of the same id legal.
func (f *reattachFixture) lose(t *testing.T) {
	t.Helper()
	if err := f.coordinator.sess.Close(session.ID(f.binding)); err != nil {
		t.Fatalf("close the dropped session: %v", err)
	}
	if _, err := f.coordinator.sess.Get(session.ID(f.binding)); err == nil {
		t.Fatal("the dropped session is still in the registry, so nothing was lost")
	}
}

func (f *reattachFixture) spec(sid string) transport.OpenSpec {
	return transport.OpenSpec{
		PaneID: f.paneID, Kind: "ssh", ProfileID: "profile-1",
		Reattach: &transport.PaneShellRef{Session: session.ID(sid), Host: "host.example", Account: "alice", Generation: "gen-1"},
	}
}

func (f *reattachFixture) route() session.Config {
	return session.Config{Kind: session.KindRemote, Host: "host.example", PaneID: f.paneID, ProfileID: "profile-1"}
}

// helperShell is the address of one shell on the host: its id, the moment the
// helper spawned it, and its pid when this machine owns the process.
//
// THE SPAWN MOMENT IS THE PROCESS'S NAME, and it is the field every launch kind
// carries: an ssh channel has no pid on this machine at all (the pid is the far
// host's, and the .7.4 stand is where a real one is read), while a local PTY
// does. A shell that came back under the same id but a different spawn moment
// is a different process wearing an old name, which is exactly what design
// §5.4's "the same shell" must exclude.
type helperShell struct {
	id        string
	startedAt string
	pid       int
}

func shellOnHost(t *testing.T, svc *helpersession.Service, sid string) (helperShell, bool) {
	t.Helper()
	for _, entry := range helperSessionsOnHost(t, svc) {
		if entry.Session.Session != sid {
			continue
		}
		shell := helperShell{id: sid, startedAt: entry.StartedAt}
		if entry.Launch.Local != nil {
			shell.pid = entry.Launch.Local.Pid
		}
		return shell, true
	}
	return helperShell{}, false
}

func TestAReconnectTakesTheSameFarShellBack(t *testing.T) {
	f := newReattachFixture(t)
	before, ok := shellOnHost(t, f.svc, f.binding)
	if !ok {
		t.Fatalf("the fixture's shell %q is not on the host", f.binding)
	}
	f.lose(t)

	taken, ok, err := f.reattacher.ReattachPaneShell(context.Background(), f.spec(f.binding), f.route())
	if err != nil {
		t.Fatalf("re-attach: %v", err)
	}
	if !ok {
		t.Fatal(`a helper that holds the shell answered "absent"`)
	}
	if taken.Session == nil {
		t.Fatal("the re-attachment answered taken but adopted nothing")
	}
	if got := string(taken.Session.ID()); got != f.binding {
		t.Fatalf("re-attached %q, want the same shell %q", got, f.binding)
	}
	if _, err := f.coordinator.sess.Get(session.ID(f.binding)); err != nil {
		t.Fatalf("the taken-back shell is not in the registry: %v", err)
	}
	// THE SAME PROCESS, not only the same id: the host's own record of the pid
	// is what says the build is still running.
	after, ok := shellOnHost(t, f.svc, f.binding)
	if !ok {
		t.Fatalf("the shell %q left the host's inventory during the re-attachment", f.binding)
	}
	if after.startedAt != before.startedAt || (before.pid > 0 && after.pid != before.pid) {
		t.Fatalf("the re-attachment answers a different process: before %+v after %+v", before, after)
	}
	// AND THE HOST HOLDS EXACTLY ONE SHELL: a reattach spawns nothing.
	if shells := helperSessionsOnHost(t, f.svc); len(shells) != 1 {
		t.Fatalf("the host holds %d entries after the re-attachment, want its one shell", len(shells))
	}
	// AND THE PANE CAN TYPE INTO IT: the adopted session is a writable
	// attachment, not a read-only view of a shell nobody owns.
	if _, err := taken.Session.Write([]byte("echo alive\r")); err != nil {
		t.Fatalf("typing into the taken-back shell: %v", err)
	}
}

// TestAReconnectTakesAnUnsavedRouteBack is the same answer for a route no
// profile names: the request carries host and account and no profile, which is
// how a typed host reaches the wire (design §5.4). The carrier is the route,
// so the profile store is never consulted — and the shell comes back the same
// way it does for a saved one.
func TestAReconnectTakesAnUnsavedRouteBack(t *testing.T) {
	f := newReattachFixture(t)
	before, ok := shellOnHost(t, f.svc, f.binding)
	if !ok {
		t.Fatalf("the fixture's shell %q is not on the host", f.binding)
	}
	f.lose(t)

	spec := f.spec(f.binding)
	spec.ProfileID = ""
	spec.Host = "host.example"
	spec.User = "alice"
	route := session.Config{Kind: session.KindRemote, Host: "host.example", PaneID: f.paneID}
	taken, ok, err := f.reattacher.ReattachPaneShell(context.Background(), spec, route)
	if err != nil {
		t.Fatalf("re-attach a typed route: %v", err)
	}
	if !ok || taken.Session == nil {
		t.Fatal(`a helper that holds the shell answered "absent" for an unsaved route`)
	}
	if got := string(taken.Session.ID()); got != f.binding {
		t.Fatalf("re-attached %q, want the same shell %q", got, f.binding)
	}
	after, ok := shellOnHost(t, f.svc, f.binding)
	if !ok || after.startedAt != before.startedAt || (before.pid > 0 && after.pid != before.pid) {
		t.Fatalf("the unsaved-route re-attachment did not take the same process back: before %+v after %+v", before, after)
	}
}

func TestAReconnectSpawnsNothingWhenTheHelperCannotBeAsked(t *testing.T) {
	f := newReattachFixture(t)
	f.lose(t)
	before := len(helperSessionsOnHost(t, f.svc))

	// A coordinator that holds no carrier for this shell is one that cannot put
	// the question. It is NOT an absence, and the answer must say so — C16's
	// whole point is that a slow or unreachable helper costs a wait, never a
	// second shell beside a live one.
	blind := &paneShellReattacher{registry: f.coordinator.reg, pass: f.reattacher.pass, timeout: time.Second}
	var waiting *transport.PaneShellWait
	if _, ok, err := blind.ReattachPaneShell(context.Background(), f.spec("ffffffffffffffffffffffffffffffff"), f.route()); err == nil || ok {
		t.Fatalf("a shell nobody can be asked about answered ok=%t err=%v, want a refusal", ok, err)
	} else if !errors.As(err, &waiting) {
		t.Fatalf("a shell nobody can be asked about refused with %v, want a WAIT the automatic loop retries (C16)", err)
	}
	if after := len(helperSessionsOnHost(t, f.svc)); after != before {
		t.Fatalf("a refused re-attachment changed what the host holds: %d, want %d", after, before)
	}
}

func TestAReconnectReportsAbsenceOnlyFromAnAnsweringHelper(t *testing.T) {
	f := newReattachFixture(t)
	// The carrier's own binding, under an id the helper never spawned: the
	// daemon answers, and its inventory does not hold this session.
	real := f.coordinator.reg.hosts[session.ID(f.binding)]
	if real == nil {
		t.Fatal("the fixture's carrier is not registered, so there is nothing to derive a stranger from")
	}
	stranger := session.ID("fffffffffffffffffffffffffffffff1")
	f.coordinator.reg.mu.Lock()
	f.coordinator.reg.hosts[stranger] = &hostedShellCarrier{
		f: &hostedShellBinding{
			reg: f.coordinator.reg, sid: stranger,
			host: real.f.host, account: real.f.account, fp: real.f.fp,
			opts: real.f.opts, install: real.f.install,
		},
		lanes: real.lanes, log: real.log,
	}
	f.coordinator.reg.mu.Unlock()
	before := len(helperSessionsOnHost(t, f.svc))

	taken, ok, err := f.reattacher.ReattachPaneShell(context.Background(), f.spec(string(stranger)), f.route())
	if err != nil {
		t.Fatalf("an answering helper's \"I do not hold that\" is an answer, not a failure: %v", err)
	}
	if ok {
		t.Fatalf("an inventory that does not hold the session answered taken: %+v", taken.Session)
	}
	if after := len(helperSessionsOnHost(t, f.svc)); after != before {
		t.Fatalf("a confirmed absence spawned a shell here: %d, want %d — the CALLER opens the replacement", after, before)
	}
}
