package app

// AN SSH PANE THIS MACHINE'S HELPER OPENS MUST HAVE A READABLE SCREEN
// (nocx-50w7p.5).
//
// paneScreen.owner picks a route by asking what KIND a session is: KindLocal is
// this machine's daemon's, everything else is a helper-hosted remote session if
// the registry holds a host for it. That reading was sound while the two
// coincided — a pane was local, or its channel came from a helper on the far
// host, and never both.
//
// 08e90002 is the case where they diverge. An ssh pane is opened by THIS
// machine's daemon, so its DESTINATION is remote (KindRemote is right, and the
// registry must report it) while its CARRIER is the daemon here. The remote
// half of owner's question then has nothing to answer with — the far-helper
// registry knows only panes IT opened — so the pane falls to errNoPaneRuntime
// and its screen can never be read. The backend is meant to answer every
// session; this one it cannot answer at all.
//
// ONE OPENER OPENS AND IS ASKED. The pane is opened through the same
// localHelperOpener that owner consults, because ownership is that opener's
// fact: a stand-in that opened the pane plus a different, hollow opener asked
// about it would agree on nothing, and the test would assert a state no code
// can reach.
//
// The destination carries a named credential because resolution must produce a
// target at all — WireIdentity sets Identity.Credential only when the resolved
// endpoint has one (ssh_helpertarget.go:604-609), and the daemon refuses an
// ssh spawn whose destination names none. Nothing here dials: the endpoint's
// scripted spawner answers `spawn-ssh` without one, so the password is never
// presented anywhere.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

func TestAnSSHPaneOpenedByThisMachinesHelperAnswersItsScreen(t *testing.T) {
	home := storagetest.IsolateWithHome(t)
	src := fakeArtifacts{payload: syntheticPayload}
	gen := src.hash()
	// The endpoint this build's generation is named for, so the local route
	// reaches THIS daemon rather than starting another. Its spawner answers
	// `spawn-ssh` too, which is what lets an ssh pane be held here at all.
	_ = startFakeLocalEndpoint(t, endpoint.Dir(home), gen)

	logger := log.NewSlogAdapter(discardLogger())
	lg := discardLogger()
	reg := session.New(logger, &reachPTYFactory{stub: pty.NewStub(logger)})
	rc, err := ssh.NewReal(logger, ssh.WithKnownHostsFile(home+"/known_hosts"))
	if err != nil {
		t.Fatalf("ssh.NewReal: %v", err)
	}
	opener := &localHelperOpener{
		log:      lg,
		registry: reg,
		dir:      endpoint.Dir(home),
		installed: helperlocal.Installed{
			Binary: "/nonexistent/nocx-helper", Generation: proto.GenerationID(gen),
		},
		sshTargets: rc,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opened, selected, err := opener.OpenHosted(ctx, session.Config{
		Kind: session.KindRemote,
		Host: "host.example",
		Remote: &ssh.ConnectConfig{
			User: "dev", Port: 22, AuthMode: "password",
			// A NAMED credential, which is what puts a reference on the wire:
			// the helper holds no secret, so the destination must say which
			// one to ask this coordinator for.
			Secrets:  rememberedPassword{value: openPasswordFixturePassword},
			SecretID: "sec:screen-proof:1",
			// A linked credential may only be spent on the endpoint its
			// profile names, and the coordinator is the party that checks it —
			// so the target has to be the one this credential is bound to.
			AuthorizedEndpoint: "host.example:22",
		},
	}, "")
	if err != nil || !selected {
		t.Fatalf("opening an ssh pane through this machine's helper: selected=%v err=%v", selected, err)
	}
	sid := opened.Session.ID()
	if got := opened.Session.Kind(); got != session.KindRemote {
		t.Fatalf("the pane's kind is %v, want KindRemote — its DESTINATION is remote, which is why the "+
			"owner must not read the kind as the carrier", got)
	}

	// THE OWNER, built as the composition root builds it: the registry, this
	// machine's route, and the far-helper registry.
	ps := newPaneScreen(lg, reg, opener, &helperRegistry{})

	// Available is the enrolment probe — the question a pane asks before a
	// watch exists over it — and it routes through owner exactly as Screen
	// does.
	err = ps.Available(string(sid))
	if errors.Is(err, errNoPaneRuntime) {
		t.Fatalf("reading this pane's screen answered %q, but this machine's helper IS holding its "+
			"terminal: the owner chose the route by the session's KIND, which says where the "+
			"DESTINATION is, rather than by the helper that opened it", err)
	}
	if err != nil {
		t.Fatalf("the pane's screen was refused for another reason: %v", err)
	}
	t.Logf("MEASURED the screen read for an ssh pane carried by this machine's helper succeeded")
}

// TestAPaneNoHelperHoldsIsStillTheNamedLoss is the PAIR of the test above, and
// it is what stops the fix from being "the refusal never fires".
//
// The session here is in the registry and is nobody's: it was opened straight
// into the registry, never through this machine's opener, and no remote helper
// knows it. That is the case errNoPaneRuntime is FOR (nocx-50w7p.5), and the
// assertion is the sentence rather than merely "an error", because a refusal
// that stopped naming what failed is the failure mode this whole epic's
// ADR-0057 work exists to prevent.
func TestAPaneNoHelperHoldsIsStillTheNamedLoss(t *testing.T) {
	logger := log.NewSlogAdapter(discardLogger())
	lg := discardLogger()
	reg := session.New(logger, &reachPTYFactory{stub: pty.NewStub(logger)})

	// A LOCAL session, opened into the registry directly — so this machine's
	// opener never saw it and does not hold it. Nothing else could hold a local
	// id space, which is why it is the honest example of an unheld pane.
	sess, err := reg.Open(context.Background(), session.Config{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("opening a session into the registry: %v", err)
	}

	ps := newPaneScreen(lg, reg, &localHelperOpener{log: lg}, &helperRegistry{})
	err = ps.Available(string(sess.ID()))
	if !errors.Is(err, errNoPaneRuntime) {
		t.Fatalf("a pane no helper holds answered %v, want the refusal that names it — the owner must "+
			"still refuse, or the fix above would be indistinguishable from never refusing", err)
	}
	t.Logf("MEASURED the unheld pane's refusal: %v", err)
}

// TestReAdoptingARemoteOnLocalBindingRefusesWhenTheProfileMoved exercises the
// READOPT PATH itself for the shape this bead created (nocx-50w7p.5): a binding
// whose Destination is remote and whose carrier is this machine's daemon.
//
// It takes the one branch of that path reachable without a live daemon — the
// binding is the daemon's, and the saved connection now resolves to a DIFFERENT
// host than the binding names — because that branch is where the new code's
// judgement is: adopting the pane here would bind the session to a machine
// nobody recorded, which is the inference nocx-k6p18.15 exists to forbid. A
// profile edited between two runs is the ordinary way it happens.
func TestReAdoptingARemoteOnLocalBindingRefusesWhenTheProfileMoved(t *testing.T) {
	logger := log.NewSlogAdapter(discardLogger())
	reg := session.New(logger, &reachPTYFactory{stub: pty.NewStub(logger)})
	route := &countingLocalRoute{entries: []helperclient.SessionEntry{{
		HostSessionID: helperclient.HostSessionID{Generation: "gen-1", Session: "sess-1"},
		Launch:        &helperclient.LaunchRecord{Cwd: "/home/alice"},
	}}}
	rp := &readoptPass{
		registry: &helperRegistry{registry: reg},
		adopter:  &stubAdopter{},
		local:    route,
		routes:   &stubRoutes{host: "somewhere.else", cfg: &ssh.ConnectConfig{User: "alice"}},
	}

	_, err := rp.readoptLocal(context.Background(), content.PendingSession{
		SessionID: "sess-1", Generation: "gen-1", PaneID: "pane-1",
		Host: "host.example", Account: "alice", ProfileID: "ssh:1",
	})
	if err == nil {
		t.Fatal("re-adopting a binding whose saved connection now resolves elsewhere succeeded, so the pane " +
			"was bound to a machine nobody recorded")
	}
	if !strings.Contains(err.Error(), "nobody recorded") {
		t.Fatalf("the refusal is %q, want one naming the destination the binding does not record", err)
	}
	t.Logf("MEASURED the moved-profile refusal: %v", err)
}

// TestReAdoptingAnSSHPaneKeepsItsDestinationAndAnswersItsScreen is the
// acceptance for the shape this bead created (nocx-50w7p.5), and it is the one
// the four local tests above are not: a REPLACING coordinator takes an ssh pane
// back off this machine's daemon, the pane keeps the FAR destination it was
// opened for, the carrier that holds it says so, and its screen answers.
//
// THE RESTART IS THE SECOND OPENER, which is what a restart is here: the daemon
// and the shell behind it are the same, the coordinator asking is not. Nothing
// is spawned twice — the daemon still holds the one session — so a pass that
// adopted it would be adopting the shell that is already running, which is the
// whole promise of a carried-over binding.
func TestReAdoptingAnSSHPaneKeepsItsDestinationAndAnswersItsScreen(t *testing.T) {
	home := storagetest.IsolateWithHome(t)
	src := fakeArtifacts{payload: syntheticPayload}
	gen := src.hash()
	_ = startFakeLocalEndpoint(t, endpoint.Dir(home), gen)

	logger := log.NewSlogAdapter(discardLogger())
	lg := discardLogger()
	newOpener := func(reg *session.Reg) *localHelperOpener {
		rc, err := ssh.NewReal(logger, ssh.WithKnownHostsFile(home+"/known_hosts"))
		if err != nil {
			t.Fatalf("ssh.NewReal: %v", err)
		}
		return &localHelperOpener{
			log: lg, registry: reg, dir: endpoint.Dir(home), sshTargets: rc,
			installed: helperlocal.Installed{
				Binary: "/nonexistent/nocx-helper", Generation: proto.GenerationID(gen),
			},
		}
	}

	// The coordinator that is replaced: it opens the pane, and the pane becomes
	// the daemon's.
	firstReg := session.New(logger, &reachPTYFactory{stub: pty.NewStub(logger)})
	first := newOpener(firstReg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	opened, selected, err := first.OpenHosted(ctx, session.Config{
		Kind: session.KindRemote, Host: "host.example",
		Remote: &ssh.ConnectConfig{
			User: "dev", Port: 22, AuthMode: "password",
			Secrets: rememberedPassword{value: openPasswordFixturePassword}, SecretID: "sec:readopt:1",
			AuthorizedEndpoint: "host.example:22",
		},
	}, "")
	if err != nil || !selected {
		t.Fatalf("opening the ssh pane that will outlive this coordinator: selected=%v err=%v", selected, err)
	}
	sid := opened.Session.ID()
	if opened.Host == "" {
		t.Fatal("the opened pane reported no destination, so there is nothing for a re-adoption to preserve")
	}

	// THE FIRST COORDINATOR GOES, which is what makes this a restart: it gives
	// up the attachment it holds, so the daemon's keyboard is free for the
	// replacement to take. Without this the pass meets its own predecessor
	// still holding the write lease, which is the refusal a SECOND live
	// coordinator gets and not the situation under test.
	first.close()

	// The replacement: same daemon, same socket, a new coordinator and a new
	// registry.
	secondReg := session.New(logger, &reachPTYFactory{stub: pty.NewStub(logger)})
	second := newOpener(secondReg)
	if second.holds(sid) {
		t.Fatal("the replacement holds the session before the pass has run, so the test cannot tell adoption from a no-op")
	}
	rp := &readoptPass{
		registry: &helperRegistry{registry: secondReg, log: lg},
		adopter:  &stubAdopter{},
		local:    second,
		routes:   &stubRoutes{host: "host.example", cfg: &ssh.ConnectConfig{User: "dev"}},
	}

	if _, rerr := rp.readoptLocal(ctx, content.PendingSession{
		SessionID: string(sid), Generation: gen, PaneID: "pane-1",
		Host: opened.Host, Account: opened.Account, ProfileID: "ssh:readopt",
	}); rerr != nil {
		t.Fatalf("re-adopting the ssh pane off this machine's daemon: %v", rerr)
	}

	// THE THREE FACTS THE ADOPTION MUST CARRY.
	sess, err := secondReg.Get(sid)
	if err != nil {
		t.Fatalf("the replacement's registry does not hold the re-adopted session: %v", err)
	}
	if sess.Kind() != session.KindRemote {
		t.Errorf("the re-adopted pane is %v, want KindRemote — its DESTINATION is the far host, and a "+
			"local kind would claim this machine for a shell that is not on it", sess.Kind())
	}
	if got := sess.Host(); got != opened.Host {
		t.Errorf("the re-adopted pane reports host %q, want the destination its binding named (%q)", got, opened.Host)
	}
	if !second.holds(sid) {
		t.Error("the replacement's opener does not hold the session it just re-adopted, so its screen read " +
			"would refuse a terminal this daemon is holding")
	}

	// AND IT ANSWERS, which is what makes the adoption more than a row: the
	// same probe the enrolment uses.
	ps := newPaneScreen(lg, secondReg, second, &helperRegistry{})
	if err := ps.Available(string(sid)); err != nil {
		t.Fatalf("the re-adopted pane's screen did not answer: %v", err)
	}
	t.Logf("MEASURED the re-adopted ssh pane: kind=%v host=%q held=%v screen=answered",
		sess.Kind(), opened.Host, second.holds(sid))
}

// TestReAdoptingABindingTheDaemonNoLongerHoldsIsALossNotAnOwnership is the pair
// of the acceptance above, and the pair is what keeps the two apart.
//
// The daemon ANSWERS and holds nothing — the session ended while nocx was away
// — so the pass must not adopt, and must not leave the session in the opener's
// set. A `holds` entry left behind would be worse than a missing one: the owner
// would route a pane to this daemon for a terminal that is gone, and the
// refusal a person sees would name the wrong thing.
func TestReAdoptingABindingTheDaemonNoLongerHoldsIsALossNotAnOwnership(t *testing.T) {
	home := storagetest.IsolateWithHome(t)
	src := fakeArtifacts{payload: syntheticPayload}
	gen := src.hash()
	// A daemon that is running and holds NOTHING: the answered-empty case, as
	// opposed to the different one where nobody could be asked at all.
	_ = startFakeLocalEndpoint(t, endpoint.Dir(home), gen)

	logger := log.NewSlogAdapter(discardLogger())
	lg := discardLogger()
	reg := session.New(logger, &reachPTYFactory{stub: pty.NewStub(logger)})
	rc, rerr := ssh.NewReal(logger, ssh.WithKnownHostsFile(home+"/known_hosts"))
	if rerr != nil {
		t.Fatalf("ssh.NewReal: %v", rerr)
	}
	// THE REAL OPENER, because the assertion is about ITS set: a double that
	// panics on the carrier half could not be asked whether a stale entry was
	// dropped, and a fake holds nothing to begin with.
	opener := &localHelperOpener{
		log: lg, registry: reg, dir: endpoint.Dir(home), sshTargets: rc,
		installed: helperlocal.Installed{
			Binary: "/nonexistent/nocx-helper", Generation: proto.GenerationID(gen),
		},
	}
	// A session this coordinator came to believe the daemon was holding.
	opener.noteHeld("sess-gone")

	rp := &readoptPass{
		registry: &helperRegistry{registry: reg, log: lg},
		adopter:  &stubAdopter{},
		local:    opener,
		routes:   &stubRoutes{host: "host.example", cfg: &ssh.ConnectConfig{User: "dev"}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, rerr := rp.readoptLocal(ctx, content.PendingSession{
		SessionID: "sess-gone", Generation: gen, PaneID: "pane-1",
		Host: "host.example", Account: "dev", ProfileID: "ssh:1",
	}); rerr != nil {
		t.Fatalf("a binding the daemon does not hold answered an error (%v), want the loss the callers "+
			"apply as a verdict", rerr)
	}
	if _, gerr := reg.Get("sess-gone"); gerr == nil {
		t.Error("a session the daemon no longer holds was adopted into the registry")
	}
	if opener.holds("sess-gone") {
		t.Error("a session the daemon no longer holds is still in the opener's set, so the owner would " +
			"route a pane to a terminal that is gone")
	}
}

// TestASessionThatEndsLeavesTheOpenersSet is the CLOSE half of the same
// interval (nocx-50w7p.5), and it is the end the first version of this change
// was missing: a session entered the set when it opened and left it only on
// release or on the readopt loss branch, so a pane that simply ENDED stayed in
// it. Nothing routes to such an entry today — the owner asks the registry first
// and a closed session is out of it — but a set that only grows is a set that
// will be believed later.
func TestASessionThatEndsLeavesTheOpenersSet(t *testing.T) {
	home := storagetest.IsolateWithHome(t)
	src := fakeArtifacts{payload: syntheticPayload}
	gen := src.hash()
	_ = startFakeLocalEndpoint(t, endpoint.Dir(home), gen)

	logger := log.NewSlogAdapter(discardLogger())
	lg := discardLogger()
	reg := session.New(logger, &reachPTYFactory{stub: pty.NewStub(logger)})
	rc, rerr := ssh.NewReal(logger, ssh.WithKnownHostsFile(home+"/known_hosts"))
	if rerr != nil {
		t.Fatalf("ssh.NewReal: %v", rerr)
	}
	opener := &localHelperOpener{
		log: lg, registry: reg, dir: endpoint.Dir(home), sshTargets: rc,
		installed: helperlocal.Installed{
			Binary: "/nonexistent/nocx-helper", Generation: proto.GenerationID(gen),
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opened, selected, err := opener.OpenHosted(ctx, session.Config{
		Kind: session.KindRemote, Host: "host.example",
		Remote: &ssh.ConnectConfig{
			User: "dev", Port: 22, AuthMode: "password",
			Secrets: rememberedPassword{value: openPasswordFixturePassword}, SecretID: "sec:close:1",
			AuthorizedEndpoint: "host.example:22",
		},
	}, "")
	if err != nil || !selected {
		t.Fatalf("opening the pane: selected=%v err=%v", selected, err)
	}
	sid := opened.Session.ID()
	if !opener.holds(sid) {
		t.Fatal("the pane is not in the opener's set straight after it opened, so this test cannot " +
			"observe the end")
	}

	if cerr := opened.Session.Close(); cerr != nil {
		t.Fatalf("closing the pane: %v", cerr)
	}

	// An observable state, not a duration: the watcher ends with the session it
	// watches, so what is waited for is the set changing.
	deadline := time.Now().Add(10 * time.Second)
	for opener.holds(sid) {
		if !time.Now().Before(deadline) {
			t.Fatal("the opener still holds a session that has ended, so a later reader of this set would " +
				"be told a terminal is there when it is not")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestTheReadoptPredicateAnswersForTheCarrierNotTheDestination is the focused
// test for the question the readopt pass routes by (nocx-50w7p.5).
//
// The destination-aware work in readoptLocal is dead code unless this predicate
// sends a remote-destination binding there, and that is the exact case the old
// shape — "Host and ProfileID are empty" — answered WRONG. Each row is one
// carrier, and the third is the one the epic created: a shell on somebody
// else's host, carried by the daemon here, which is the only row whose answer
// changed.
func TestTheReadoptPredicateAnswersForTheCarrierNotTheDestination(t *testing.T) {
	const farHelper = "/home/alice/.nocx/helper/gen-test/nocx-helper"
	for _, c := range []struct {
		name string
		p    content.PendingSession
		want bool
	}{
		{"a local pane", content.PendingSession{SessionID: "s1", Generation: "gen"}, true},
		{
			"an ssh pane this machine's daemon carries",
			content.PendingSession{SessionID: "s2", Generation: "gen", Host: "host.example", ProfileID: "ssh:1"},
			true,
		},
		{
			"an ssh pane a far helper carries",
			content.PendingSession{SessionID: "s3", Generation: "gen", Host: "host.example", ProfileID: "ssh:1", HelperCommand: farHelper},
			false,
		},
		{
			"a row no generation qualifies",
			content.PendingSession{SessionID: "s4", Host: "host.example", ProfileID: "ssh:1"},
			false,
		},
	} {
		if got := isLocalBinding(c.p); got != c.want {
			t.Errorf("%s: isLocalBinding = %v, want %v (HelperCommand=%q)", c.name, got, c.want, c.p.HelperCommand)
		}
	}
}
