package app

// SESSIONS.INVENTORY MUST NAME THE HELPER THAT HOLDS THE PANE (nocx-s8mfn).
//
// The inventory walked ONE registry: `helperRegistry.hosts`, which only a FAR
// helper host fills. Since this machine's daemon opens ssh panes (nocx-50w7p.5)
// the two stopped coinciding — an ssh pane has a remote DESTINATION and a local
// CARRIER — and the RPC answered "no active helper" for exactly the panes the
// epic is about, although a helper was holding every one of them. The defect was
// reported by the epic's own e2e worker (e2e/ssh-helper-happy-path.spec.ts says
// in prose that it cannot use this method for that reason).
//
// ONE OWNER, NOT A SECOND DERIVATION. The question "which helper holds this
// session" already had an answer: paneScreen.owner asks this machine's opener
// first (localHelperOpener.holds) and the far registry second, and the pane's
// screen read is the proof that it is right. The inventory asks the SAME two
// parties, in the same order, for the SAME fact, rather than inventing a third
// rule about which sessions this machine's daemon could be holding.
//
// THE STAND IS THE COMPOSITION ROOT. The app is the shipped one (newTestApp with
// a real install from bytes this test owns), the panes are opened through the
// opener the `open` handler calls, the machine's daemon is the scripted endpoint
// other tests in this package already use (session_reconcile_local_test.go), and
// the call is made over the app's own WebSocket. A harness with an injected
// inventory would prove the last adapter, which is not where the defect was.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	helperhost "github.com/shady2k/nocx/internal/helper/host"
	helperproto "github.com/shady2k/nocx/internal/helper/proto"
	helper "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// farHelperStand boots one in-process helper daemon — the peer a far host runs,
// over a pipe rather than a socket — and registers it in the app's registry as
// the holder of sid, exactly as the remote-helper open route does.
//
// It is the stand BOTH inventory tests share: the one that asserts the far arm
// alone (helper_inventory_wired_test.go) and the one that asserts the far arm
// beside this machine's (below). Two copies of it would be two answers to "what
// is a far helper", and the second one would drift.
//
// It returns the session id the DAEMON minted rather than the one the caller
// registered: the coordinator's own binding key is not what an inventory entry
// names, and a test that read the caller's value would not notice if the answer
// stopped coming from the daemon at all.
func farHelperStand(t *testing.T, a *App, sid session.ID, hostName, account, generation string) string {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := helper.New(helper.Options{
		Generation: helperproto.GenerationID(generation),
		Spawner:    helper.NewLocalSpawner(logger, helper.Shell{Path: "/bin/sh", Args: []string{"-i"}}, ""),
		Inspector:  helper.NewInspector(),
		Log:        logger,
		Limits:     helper.DefaultLimits(),
	})
	serverConn, clientConn := net.Pipe()
	peer := helperhost.New(serverConn, serverConn, generation, "instance-a", logger)
	peer.Register(svc)
	release := svc.Bind(peer)
	serveDone := make(chan error, 1)
	go func() { serveDone <- peer.Serve(ctx) }()

	c, err := helperclient.Dial(ctx, helperclient.Config{
		Exec:        helperclient.NewSocketConn(clientConn),
		ExpectHash:  generation,
		SentinelTTL: time.Second,
		Log:         logger,
	})
	if err != nil {
		release()
		svc.Close()
		_ = serverConn.Close()
		t.Fatalf("helper Dial: %v", err)
	}
	t.Cleanup(func() {
		_ = c.Close()
		svc.Close()
		release()
		if serveErr := <-serveDone; serveErr != nil {
			t.Errorf("helper host: %v", serveErr)
		}
	})

	var spawned helperproto.SpawnResult
	if err := c.Call(ctx, helperproto.ServiceSession, helperproto.OpSpawn,
		helperproto.SpawnParams{Cwd: "/", Cols: 80, Rows: 24}, &spawned); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	f := &sessionFactory{
		reg: a.helperRegistry, sid: sid, host: hostName, account: account,
		install: installedHelper{generation: generation},
	}
	a.helperRegistry.mu.Lock()
	a.helperRegistry.hosts[f.sid] = &hostHelper{f: f, client: c}
	a.helperRegistry.mu.Unlock()
	return spawned.Entry.Session.Session
}

// TestSessionsInventoryNamesTheHelperThatHoldsEachPane is the acceptance for
// nocx-s8mfn: THREE panes, three carriers, one answer that names all of them.
//
//	the ssh pane   this machine's daemon dialed the far host (spawn-ssh), so
//	               its DESTINATION is remote and its CARRIER is the daemon here;
//	the local pane this machine's daemon forked the shell; and
//	the far pane   a helper on another machine holds it, which is the arm that
//	               has always worked and must not change.
//
// THE ASSERTION IS THE ANSWER, NOT THE ERROR. Before this bead the RPC refused
// outright ("no active helper") whenever the registry was empty, so a test that
// only asserted "no error" would pass on a build that reported nothing at all
// once a far helper was present. Each pane is therefore looked up BY ITS OWN
// SESSION ID in the answer, and each entry is asked for the facts its carrier
// can honestly report — the remote launch record for the ssh pane, the local one
// for the local pane, and the far daemon's own minted id for the far one.
func TestSessionsInventoryNamesTheHelperThatHoldsEachPane(t *testing.T) {
	home := storagetest.IsolateWithHome(t)
	src := fakeArtifacts{payload: syntheticPayload}
	gen := src.hash()
	// THE DAEMON, BEFORE THE APP ASKS FOR ONE. This build's generation is
	// served by the scripted endpoint, so the opener dials it instead of
	// starting the bytes Start installed — the same stand the pane-screen owner
	// tests use, and the reason no shell is forked anywhere in this test.
	_ = startFakeLocalEndpoint(t, endpoint.Dir(home), gen)

	a := bootLocalAppOn(t, src)
	// The generation the app would dial is the one that is serving. A mismatch
	// would make this test measure a daemon the app never reached, and the
	// failure would read as "the inventory forgot the pane".
	a.localHelper.mu.Lock()
	installed := a.localHelper.installed.Generation
	a.localHelper.mu.Unlock()
	if string(installed) != gen {
		t.Fatalf("the app installed generation %q, want the %q the scripted endpoint serves", installed, gen)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// ── THE SSH PANE, through the shipped opener ────────────────────────────
	// A named credential, because resolution must produce a target at all: the
	// daemon refuses an ssh spawn whose destination names none. Nothing dials —
	// the endpoint's scripted spawner answers `spawn-ssh` without one.
	sshOpened, selected, err := a.localHelper.OpenHosted(ctx, session.Config{
		Kind: session.KindRemote,
		Host: "host.example",
		Remote: &ssh.ConnectConfig{
			User: "dev", Port: 22, AuthMode: "password",
			Secrets:            rememberedPassword{value: openPasswordFixturePassword},
			SecretID:           "sec:inventory:1",
			AuthorizedEndpoint: "host.example:22",
		},
	}, "")
	if err != nil || !selected {
		t.Fatalf("opening an ssh pane through this machine's helper: selected=%v err=%v", selected, err)
	}
	sshSession := string(sshOpened.Session.ID())
	if sshOpened.Host != "host.example" {
		t.Fatalf("the ssh pane reports destination %q, want the host it was opened for", sshOpened.Host)
	}

	// ── THE LOCAL PANE, the other carrier the same daemon holds ─────────────
	localOpened, selected, err := a.localHelper.OpenHosted(ctx,
		session.Config{Kind: session.KindLocal, Cols: 80, Rows: 24}, "")
	if err != nil || !selected {
		t.Fatalf("opening a local pane through this machine's helper: selected=%v err=%v", selected, err)
	}
	localSession := string(localOpened.Session.ID())

	// ── THE FAR PANE, whose carrier is not on this machine ──────────────────
	const farGeneration = "generation-far"
	farSession := farHelperStand(t, a, session.ID("far-binding"), "build.example.com", "deploy", farGeneration)

	// ── THE ANSWER, over the app's own WebSocket ────────────────────────────
	conn := dialAppWS(t, a)
	t.Cleanup(func() { _ = conn.Close() })
	resp := callAppWS(t, conn, "sessions.inventory", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("sessions.inventory: %+v", resp.Error)
	}
	var result struct {
		Sessions []helperclient.SessionEntry `json:"sessions"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode sessions.inventory: %v (%s)", err, resp.Result)
	}
	bySession := make(map[string]helperclient.SessionEntry, len(result.Sessions))
	for _, entry := range result.Sessions {
		bySession[entry.HostSessionID.Session] = entry
	}

	// THIS MACHINE'S DAEMON HOLDS BOTH OF ITS PANES, and the answer names it for
	// each by the generation its endpoint is named for.
	localHeld, ok := bySession[sshSession]
	if !ok {
		t.Fatalf("the ssh pane this machine's helper is holding is missing from the inventory "+
			"(%d entries: %v): the RPC still walks only the registry a FAR helper host fills",
			len(result.Sessions), bySession)
	}
	if localHeld.HostSessionID.Generation != gen {
		t.Errorf("the ssh pane is reported on generation %q, want this machine's %q",
			localHeld.HostSessionID.Generation, gen)
	}
	// THE FIELDS A FAR-HELPER-HOSTED PANE CARRIES, where they apply: the
	// destination is the ssh launch branch, and the LOCAL branch is ABSENT
	// rather than a record of zeros — pid 0 is the kernel scheduler, and a
	// reader that trusted it would ask the OS about a process nobody started.
	if localHeld.RemoteLaunch == nil {
		t.Fatalf("the ssh pane carries no remote launch record: %+v", localHeld)
	}
	if localHeld.RemoteLaunch.Host != "host.example" || localHeld.RemoteLaunch.User != "dev" ||
		localHeld.RemoteLaunch.Port != 22 {
		t.Errorf("the ssh pane's launch record is %+v, want the destination it was opened for",
			*localHeld.RemoteLaunch)
	}
	if localHeld.RemoteLaunch.Shell == "" {
		t.Error("the ssh pane's launch record names no shell tier, so nothing says what the far side runs")
	}
	if localHeld.Launch != nil {
		t.Errorf("the ssh pane carries a LOCAL launch record (%+v): its process is not on this machine",
			*localHeld.Launch)
	}

	localHeld, ok = bySession[localSession]
	if !ok {
		t.Fatalf("the local pane this machine's helper is holding is missing from the inventory (%v)", bySession)
	}
	if localHeld.Launch == nil {
		t.Fatalf("the local pane carries no local launch record: %+v", localHeld)
	}
	if localHeld.Launch.Shell == "" || localHeld.Launch.Pid == 0 {
		t.Errorf("the local pane's launch record is %+v, want the shell and pid the daemon reported",
			*localHeld.Launch)
	}
	if localHeld.RemoteLaunch != nil {
		t.Errorf("the local pane carries a remote launch record (%+v): nothing dialed for it",
			*localHeld.RemoteLaunch)
	}

	// AND THE FAR ARM IS UNCHANGED: a pane a far helper holds is still reported,
	// under the id THAT daemon minted and the generation it is running as.
	farHeld, ok := bySession[farSession]
	if !ok {
		t.Fatalf("the pane a far helper holds is missing from the inventory (%v): the local arm "+
			"replaced the registry's answer instead of joining it", bySession)
	}
	if farHeld.HostSessionID.Generation != farGeneration {
		t.Errorf("the far pane is reported on generation %q, want %q",
			farHeld.HostSessionID.Generation, farGeneration)
	}
	if farHeld.Launch == nil || farHeld.Observed == nil {
		t.Errorf("the far pane's entry lost the fields it always carried: launch=%v observed=%v",
			farHeld.Launch, farHeld.Observed)
	}
	t.Logf("MEASURED the inventory naming three carriers: ssh=%s (this machine, remote launch), "+
		"local=%s (this machine, local launch), far=%s (generation %s)",
		sshSession, localSession, farSession, farHeld.HostSessionID.Generation)
}

// TestSessionsInventoryRefusesWhenNoHelperCanBeAsked is the PAIR of the test
// above, and it is what stops the fix from being "the inventory now answers
// empty".
//
// The app here holds nothing and the registry holds no helper, so NOBODY was
// asked — and an answered-empty inventory is only safe to read as "no sessions"
// after a helper actually answered. So the refusal stands, and its sentence is
// now about the whole inventory — nobody is holding a session for this
// coordinator — rather than about the far registry alone.
func TestSessionsInventoryRefusesWhenNoHelperCanBeAsked(t *testing.T) {
	storagetest.Isolate(t)
	a := bootLocalAppOn(t, fakeArtifacts{payload: syntheticPayload})

	conn := dialAppWS(t, a)
	t.Cleanup(func() { _ = conn.Close() })
	resp := callAppWS(t, conn, "sessions.inventory", map[string]any{}, 1)
	if resp.Error == nil {
		t.Fatalf("an inventory nobody could answer came back as a result: %s", resp.Result)
	}
	t.Logf("MEASURED the refusal with no helper to ask: %s", resp.Error.Message)
}
