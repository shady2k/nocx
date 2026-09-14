package app

// A PANE HOSTED BY THE FAR HOST'S OWN HELPER GETS A TOOL SURFACE (nocx-e2bws,
// criterion 1 for case (a)).
//
// # What this proves, and what it does not
//
// The criterion has three parts, and this file takes all three at the seam the
// coordinator owns:
//
//  1. the LAUNCH the far host's helper renders names a socket on that host, and
//     the bearer its agent will present travels with it — read off the
//     `session.spawn` the far daemon actually received, which is the launch's
//     own input;
//  2. the LISTENER is opened against the session the FAR helper minted (AD-7),
//     not against any name this side invented, and the pane's bearer is bound to
//     that session in the book admission reads;
//  3. the SESSION'S END takes both away: the listener is ended and the
//     directory removed, on the same event that retires the bearer.
//
// What it does NOT prove is the pipe itself: that bytes arriving on the far
// socket reach the coordinator's endpoint with the pane record first is
// internal/helper/session's own acceptance (ssh_tool_socket_test.go) and
// internal/helper/sshsvc's (toolsocket_test.go), where a real sshd serves the
// streamlocal listener. Here the far side's sshd is a fake lane and the socket
// is recorded, which is the right scope for the coordinator's half.
//
// # The fakes, and why they are fakes
//
// The far DAEMON is real (the host protocol, the session service, the spawn
// handler), and only its spawner is scripted — because what this test reads is
// the REQUEST that daemon was handed. The far tool surface is a purpose-built
// double rather than an interface every lane provider grew: an implementer of
// "bring a helper up on a host" must not be made to fake "bind a socket on it"
// (farPaneToolSocketter's own note), so the registry takes it as its own
// injected capability and nil means a pane with no tool surface at all.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

// recordingSpawner is the far daemon's spawner with a pen: the request it was
// handed is the launch's own input, so it is kept rather than dropped.
type recordingSpawner struct {
	mu     sync.Mutex
	spawns []helpersession.SpawnRequest
}

func (s *recordingSpawner) Spawn(req helpersession.SpawnRequest) (helpersession.Process, error) {
	s.mu.Lock()
	s.spawns = append(s.spawns, req)
	s.mu.Unlock()
	return &idleProcess{done: make(chan struct{}), pid: 4101, id: req.SessionID}, nil
}

// SpawnSSH satisfies the ssh half of the same seam: this pane is the far host's
// own LOCAL spawn, so nothing here may reach it, and a call would be a product
// regression rather than a fixture gap.
func (s *recordingSpawner) SpawnSSH(context.Context, helpersession.SSHSpawnRequest) (helpersession.Process, error) {
	return nil, errors.New("this stand spawns panes locally; spawn-ssh is this machine's helper's route")
}

func (s *recordingSpawner) spawnsSeen() []helpersession.SpawnRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]helpersession.SpawnRequest(nil), s.spawns...)
}

// openedFarSocket is one listener the coordinator asked for, as the seam saw it.
type openedFarSocket struct {
	routes  farPaneToolRoutes
	session string
}

// fakeFarTools is the far tool surface, recording every act. It is the
// purpose-built double farPaneToolSocketter's note argues for: nothing else in
// this package grows a socket-binding capability to satisfy it.
type fakeFarTools struct {
	mu       sync.Mutex
	prepared []string
	routes   farPaneToolRoutes
	opened   []openedFarSocket
	closed   []proto.ForwardID
	removed  []farPaneToolRoutes
	bearers  map[session.ID]string
	// openErr makes the listener fail, which is the soft degrade ADR-0004 asks
	// for: the pane still opens, with no tools.
	openErr error
	// onOpen runs INSIDE the open, which is how a test states the race the
	// registration order exists for: the session ending while the listener is
	// being created, with no timing involved (a hook is called at the one moment
	// a duration could only approximate).
	onOpen func()
}

func (f *fakeFarTools) PrepareFarPaneToolSocket(_ context.Context, _ string, _ []ssh.ConnectOption, name string) (farPaneToolRoutes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared = append(f.prepared, name)
	f.routes = farPaneToolRoutes{
		Dir:    "/home/far/.nocx/run/" + name,
		Path:   "/home/far/.nocx/run/" + name + "/tool.sock",
		Target: "/run/user/1000/nocx/tool.sock",
	}
	return f.routes, nil
}

func (f *fakeFarTools) OpenFarPaneToolSocket(_ context.Context, _ string, _ []ssh.ConnectOption, routes farPaneToolRoutes, session string) (proto.ForwardID, error) {
	f.mu.Lock()
	if f.openErr != nil {
		err := f.openErr
		f.mu.Unlock()
		return proto.ForwardID{}, err
	}
	f.opened = append(f.opened, openedFarSocket{routes: routes, session: session})
	hook := f.onOpen
	// THE LOCK IS RELEASED BEFORE THE HOOK, and that is not tidiness: the hook
	// is where a test states the session's end, and the teardown calls back into
	// this double (RemoveFarPaneToolSocketDir) — a hook run under the lock would
	// deadlock on a mutex Go does not let a goroutine re-enter.
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return proto.ForwardID{0x42}, nil
}

// lastOpened answers the socket the double most recently recorded, which is how
// a hook inside the open reads the session that open is for.
func (f *fakeFarTools) lastOpened() openedFarSocket {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.opened) == 0 {
		return openedFarSocket{}
	}
	return f.opened[len(f.opened)-1]
}

func (f *fakeFarTools) CloseFarPaneToolSocket(_ context.Context, id proto.ForwardID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, id)
	return nil
}

func (f *fakeFarTools) RemoveFarPaneToolSocketDir(_ context.Context, _ string, _ []ssh.ConnectOption, routes farPaneToolRoutes) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, routes)
	return nil
}

func (f *fakeFarTools) RecordPaneBearer(sid session.ID, token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bearers == nil {
		f.bearers = make(map[session.ID]string)
	}
	f.bearers[sid] = token
}

func (f *fakeFarTools) seen() (prepared []string, opened []openedFarSocket, closed []proto.ForwardID, removed []farPaneToolRoutes) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.prepared...),
		append([]openedFarSocket(nil), f.opened...),
		append([]proto.ForwardID(nil), f.closed...),
		append([]farPaneToolRoutes(nil), f.removed...)
}

// farPaneStand is the coordinator half of case (a): the helper registry over a
// scripted lane to a REAL daemon, with the far tool surface recorded.
type farPaneStand struct {
	reg     *helperRegistry
	tools   *fakeFarTools
	spawner *recordingSpawner
}

func startFarPaneStand(t *testing.T) *farPaneStand {
	t.Helper()
	spawner := &recordingSpawner{}
	svc := helpersession.New(helpersession.Options{
		Generation: proto.GenerationID(syntheticArtifactHash),
		Spawner:    spawner,
		SSHSpawner: spawner,
		Log:        discardLogger(),
	})
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc), home: t.TempDir()}
	store, installs := testConsentStores(t)
	_, reg := helperGitFactory(provider, stubArtifacts(t), store, installs, discardLogger())
	reg.registry = session.New(log.NewSlogAdapter(discardLogger()), &reachPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(discardLogger()))})
	tools := &fakeFarTools{}
	reg.tools = tools
	return &farPaneStand{reg: reg, tools: tools, spawner: spawner}
}

// open opens one far pane through the registry, as the transport asks it to.
func (s *farPaneStand) open(t *testing.T, claim string) session.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	opened, selected, err := s.reg.OpenHosted(ctx, farPaneConfig(), claim)
	if err != nil || !selected {
		t.Fatalf("opening a pane on the far host's helper: selected=%v err=%v", selected, err)
	}
	if opened.Session == nil {
		t.Fatal("the far helper answered no session")
	}
	t.Cleanup(func() { _ = opened.Session.Close() })
	return opened.Session
}

// farPaneConfig is a destination whose own helper hosts the pane: a remote
// session with a named credential, exactly as files_helper_acceptance_test.go
// builds one.
func farPaneConfig() session.Config {
	return session.Config{
		Kind: session.KindRemote, Host: "far.example",
		ProfileID: "ssh:far-tools", PaneID: "pane-far-tools",
		Cols: 80, Rows: 24,
		Remote: &ssh.ConnectConfig{
			User: "dev", Port: 22, AuthMode: "password",
			// AN EXPLICIT HELPER MODE IS THE CONSENT for the binary (§4.3), and
			// it is what makes this destination the far host's OWN helper's
			// pane rather than a fall-through to this machine's `spawn-ssh`:
			// the fake probe lease reports no host-key fingerprint, so an `auto`
			// connection here would resolve to the consent ask instead
			// (helperConnection says the same, one package over).
			DesiredMode:        string(profile.DesiredHelper),
			Secrets:            rememberedPassword{value: openPasswordFixturePassword},
			SecretID:           "sec:far-tools:1",
			AuthorizedEndpoint: "far.example:22",
		},
	}
}

// TestAFarHelperHostedPaneGetsAToolSurface is the criterion, in its three parts.
func TestAFarHelperHostedPaneGetsAToolSurface(t *testing.T) {
	stand := startFarPaneStand(t)
	const claim = "claim-far-tools-1"
	sess := stand.open(t, claim)
	sid := sess.ID()

	// ── 1. THE LAUNCH NAMES THE SOCKET AND CARRIES THE BEARER ───────────────
	spawns := stand.spawner.spawnsSeen()
	if len(spawns) != 1 {
		t.Fatalf("the far daemon was asked to spawn %d pane(s), want exactly 1", len(spawns))
	}
	spawned := spawns[0]
	if spawned.AgentToolEndpoint == "" {
		t.Fatal("the far pane's launch names no tool socket, so its agent has nothing to dial")
	}
	if spawned.AgentToolToken == "" {
		t.Fatal("the far pane's launch carries no bearer, so its agent has nothing to present and the endpoint can admit nobody")
	}
	// AND THE PATH IS DEEPER THAN THE LAUNCH: the socket is one pane's, under the
	// account's endpoint directory, and the directory's NAME is the claim — the
	// caller's own name for this spawn, never a session id (the far helper mints
	// that one, below).
	if want := endpoint.Dir("/home/far") + "/" + claim + "/tool.sock"; spawned.AgentToolEndpoint != want {
		t.Fatalf("the launch names %q, want the pane's own socket under the account's endpoint directory (%q)",
			spawned.AgentToolEndpoint, want)
	}

	// ── 2. THE LISTENER IS OPENED AGAINST THE FAR SESSION, AND THE BEARER IS BOUND ──
	prepared, opened, _, _ := stand.tools.seen()
	if len(prepared) != 1 || prepared[0] != claim {
		t.Fatalf("the surface was prepared from %v, want one preparation named by the claim %q", prepared, claim)
	}
	if len(opened) != 1 {
		t.Fatalf("%d far tool sockets were opened, want exactly 1", len(opened))
	}
	if opened[0].session != string(sid) {
		t.Fatalf("the listener's pane record would name %q, want the session the FAR helper minted (%q)",
			opened[0].session, sid)
	}
	if opened[0].routes.Path != spawned.AgentToolEndpoint {
		t.Fatalf("the listener was bound at %q while the launch named %q: the two must be one path",
			opened[0].routes.Path, spawned.AgentToolEndpoint)
	}
	if got := stand.tools.bearers[sid]; got == "" || got != spawned.AgentToolToken {
		t.Fatalf("the bearer bound to the session is %q, want the one the launch carried (%q) — an interval that bounds a different value admits nobody",
			got, spawned.AgentToolToken)
	}

	// ── 3. THE SESSION'S END TAKES THE LISTENER AND THE DIRECTORY AWAY ──────
	stand.reg.SessionEnded(sid)
	_, _, closed, removed := stand.tools.seen()
	if len(closed) != 1 {
		t.Fatalf("a session's end closed %d listeners, want exactly 1 — a socket on somebody else's host must not outlive the session it was opened for",
			len(closed))
	}
	if len(removed) != 1 || removed[0].Dir != opened[0].routes.Dir {
		t.Fatalf("the teardown removed %v, want the directory the listener was bound in (%q)",
			removed, opened[0].routes.Dir)
	}
	// Idempotent: a second end for one session does nothing.
	stand.reg.SessionEnded(sid)
	if _, _, again, _ := stand.tools.seen(); len(again) != 1 {
		t.Fatalf("a second session-end closed %d listeners, want 1: the teardown is not idempotent", len(again))
	}
}

// TestAFarPaneWhoseSocketCannotBeOpenedStillOpens is the failure path, and it is
// ADR-0004's rule rather than a nicety: a listener that could not be opened is a
// pane that runs with no tools, with its directory swept — never a pane that
// fails to open because nocx's own optional work did.
func TestAFarPaneWhoseSocketCannotBeOpenedStillOpens(t *testing.T) {
	stand := startFarPaneStand(t)
	stand.tools.openErr = errors.New("the far side refused to bind")

	sess := stand.open(t, "claim-far-tools-2")

	if sess.ID() == "" {
		t.Fatal("a pane whose tool socket could not be opened lost its session")
	}
	_, _, _, removed := stand.tools.seen()
	if len(removed) != 1 {
		t.Fatalf("the directory of a socket that never came up was removed %d time(s), want 1 — it is this open's own scaffolding",
			len(removed))
	}
	if got := stand.spawner.spawnsSeen(); len(got) != 1 || got[0].AgentToolEndpoint == "" {
		t.Fatalf("the launch still named no socket (%+v): the path is decided before the listener, so the far shell must be told it either way",
			got)
	}
}

// guard: the double must satisfy the seam the registry takes.
var _ farPaneToolSocketter = (*fakeFarTools)(nil)

// io and log are used by the helpers above through their package-qualified
// names elsewhere in the package; kept explicit here so this file compiles
// standalone if those move.
var (
	_ = io.Discard
	_ = log.NewSlogAdapter
	_ = slog.LevelInfo
)

// TestASessionThatEndsWhileTheListenerIsOpeningIsNotLeaked is the race the
// registration order exists for, stated deterministically: the session ends
// INSIDE the open (the double's hook runs there), so the registry's entry is
// gone before the listener id comes back — and the listener is still closed,
// by the opener, rather than left on somebody else's host for a session this
// coordinator has forgotten.
//
// It is the paired case of the teardown in the acceptance test above: there the
// end arrives after the registration is complete, here during it, and ONE of
// the two must own the listener in each case.
func TestASessionThatEndsWhileTheListenerIsOpeningIsNotLeaked(t *testing.T) {
	stand := startFarPaneStand(t)
	stand.tools.onOpen = func() {
		// The session's own end, at the moment the far side is binding the
		// socket: exactly the window a registration made after the open would
		// miss. The session is the one THIS open is for, read from what the
		// double just recorded rather than guessed.
		held := stand.tools.lastOpened()
		if held.session == "" {
			t.Error("the open recorded no session, so this hook cannot state the race")
			return
		}
		stand.reg.SessionEnded(session.ID(held.session))
	}

	sess := stand.open(t, "claim-far-race")

	_, _, closed, removed := stand.tools.seen()
	if len(closed) != 1 {
		t.Fatalf("the listener was closed %d time(s), want exactly 1: a session that ended while its socket was opening must not leave the socket behind", len(closed))
	}
	if len(removed) != 1 {
		t.Fatalf("the directory was removed %d time(s), want exactly 1 — the end that beat the registration is the one that removes it", len(removed))
	}
	if sess.ID() == "" {
		t.Fatal("the pane lost its session")
	}
}
