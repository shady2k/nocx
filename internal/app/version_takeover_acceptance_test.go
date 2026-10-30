package app

// nocx-1kejz.1: a version-changing launch stops the old coordinator, not
// its helper-owned shell. The replacement reads the persisted binding and
// takes back that exact shell; its next new pane uses the installed generation.
//
// The launcher, discovery socket, App, content store, helper daemons and PTYs
// are real. Only process management is adapted: Stop calls App.Shutdown and
// Spawn constructs the next App in this process instead of signalling or
// execing a coordinator. SignalStopper's OS behaviour belongs to its own tests.
// Both helpers are built from this tree and speak the SAME proto.Version.
// This test makes no claim about adopting a different helper protocol.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	coord "github.com/shady2k/nocx/internal/coordinator"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"
)

func TestAVersionTakeoverKeepsTheLiveShellAndRoutesNewPanesToTheInstalledGeneration(t *testing.T) {
	// Build before moving HOME, exactly as realHelperArtifacts requires. A
	// different linker build id changes the content-addressed generation,
	// not the helper protocol or the implementation behind the shell.
	oldSource := realHelperArtifacts(t)
	newSource := versionTakeoverHelperArtifacts(t)
	if oldSource.hash() == newSource.hash() {
		t.Fatal("the two installed helpers have the same generation; routing would be untested")
	}
	home := storagetest.IsolateWithHome(t)
	ctx := t.Context()
	stand := &versionTakeoverStand{
		t: t, dir: storagetest.SocketDir(t), source: newSource,
		build: coord.Build{Version: "0.6.2", Commit: "after-version-change"},
	}
	var shellPIDs []int
	t.Cleanup(func() {
		if stand.socket != nil {
			_ = stand.socket.Close()
		}
		for _, src := range []fakeArtifacts{oldSource, newSource} {
			endTheDaemon(t, filepath.Join(helperRoot(home, src.hash()), "nocx-helper"))
		}
		waitForThePanes(t, shellPIDs)
	})

	first := bootLocalAppOn(t, oldSource)
	stand.current = first
	oldBuild := coord.Build{Version: "0.6.1", Commit: "before-version-change"}
	if err := stand.serve(oldBuild); err != nil {
		t.Fatalf("publish the first coordinator: %v", err)
	}

	// A real pane identity makes the durable binding prove which pane gets
	// its pipe back, not just whether a bare session id can be attached.
	db, ok := first.contentCloser.(content.ContentDB)
	if !ok {
		t.Fatal("the App has no content store")
	}
	const paneID = "pane-version-takeover"
	if _, err := db.Layout().CreateWorkspace(ctx,
		content.Workspace{ID: "workspace-version-takeover", Name: "version takeover"},
		content.Tab{ID: "tab-version-takeover", Layout: content.LayoutRow},
		content.Pane{ID: paneID, Cwd: home, Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("create the pane's durable layout: %v", err)
	}
	opened, err := first.Transport.OpenSession(ctx, transport.OpenSpec{
		PaneID: paneID, Cols: 100, Rows: 30,
	})
	if err != nil {
		t.Fatalf("open the pane before the update: %v", err)
	}
	sid := opened.Session.ID()
	awaitLocalPaneIntegration(t, first, sid)
	if opened.Hosted == nil || opened.Hosted.Generation != oldSource.hash() {
		t.Fatal("the first pane was not opened on the old installed generation")
	}
	oldPID, known := first.Session.OwnedProcessPID(sid)
	if !known || oldPID <= 0 || !alive(oldPID) {
		t.Fatalf("the old pane has no live shell pid: %d, known=%v", oldPID, known)
	}
	shellPIDs = append(shellPIDs, oldPID)
	if enrolErr := first.paneViews.Enrol(string(sid)); enrolErr != nil {
		t.Fatalf("watch the original pane: %v", enrolErr)
	}
	// The value exists only in the live shell, not in the pane's cwd,
	// ledger or startup files. The expected output is absent from the
	// command's echoed input: a marker wait cannot pass on tty echo alone.
	// Keep the state in the actual native shell, rather than in a disposable
	// POSIX child, so the post-takeover read still proves that shell survived.
	setState := "NOCX_TAKEOVER_STATE=held"
	if shellintegration.LocalShellKind(loginShellPath()) == shellintegration.ShellFish {
		setState = "set -g NOCX_TAKEOVER_STATE held"
	}
	if _, writeErr := opened.Session.Write([]byte(setState + "; printf 'BEFORE-%s\\n' \"$NOCX_TAKEOVER_STATE\"\n")); writeErr != nil {
		t.Fatalf("use the original shell: %v", writeErr)
	}
	waitForMarkerAnywhere(t, first, sid, "BEFORE-held")

	self := coord.ClientIdentity{
		Version: stand.build.Version, Commit: stand.build.Commit, Protocol: coord.ProtocolVersion,
	}
	client, err := coord.NewClient(coord.ClientConfig{
		Socket: coord.SocketPathIn(stand.dir), Self: self,
		Dialer: coord.SystemDialer{}, Logger: discardLogger(t),
	})
	if err != nil {
		t.Fatalf("construct the updated window's discovery client: %v", err)
	}
	launcher, err := coord.NewLauncher(coord.LauncherConfig{
		Dir: stand.dir, Self: self, Client: client,
		Spawner: stand, Stopper: stand, Announcer: stand, Logger: discardLogger(t),
	})
	if err != nil {
		t.Fatalf("construct the updated window's launcher: %v", err)
	}
	launched, err := launcher.Launch(ctx)
	if err != nil {
		t.Fatalf("launch after the Version change: %v", err)
	}
	if !launched.TakenOver || !launched.Spawned || launched.Replaced {
		t.Fatalf("launch = spawned:%v takenOver:%v replaced:%v; want a preserving takeover",
			launched.Spawned, launched.TakenOver, launched.Replaced)
	}
	if stand.stops != 1 || stand.spawns != 1 || !first.lifecycleStopping.Load() {
		t.Fatalf("coordinator lifecycle = stops:%d spawns:%d shutdown:%v; want one orderly restart",
			stand.stops, stand.spawns, first.lifecycleStopping.Load())
	}
	if len(stand.notices) != 0 {
		t.Fatalf("the preserving takeover raised a notice: %+v", stand.notices)
	}
	if launched.Hello.Build != stand.build || launched.Hello.Protocol != self.Protocol {
		t.Fatalf("the window did not reach the new coordinator: %+v", launched.Hello.Build)
	}

	// Captured from the NEW App's real store before Start reconciles it:
	// this is a disk round trip, not a binding manufactured by this test.
	if len(stand.carried) != 1 || stand.carried[0].SessionID != string(sid) ||
		stand.carried[0].Generation != oldSource.hash() || stand.carried[0].PaneID != paneID {
		t.Fatalf("the replacing App inherited %+v, want the original pane's old-generation binding", stand.carried)
	}
	second := stand.current
	if second == first {
		t.Fatal("the launch reused the first App instead of starting the new coordinator")
	}
	restored, err := second.Session.Get(sid)
	if err != nil {
		t.Fatalf("the replacing App did not readopt the live session %s: %v", sid, err)
	}
	afterPID, known := second.Session.OwnedProcessPID(sid)
	if !known || afterPID != oldPID || !alive(afterPID) || restored.PaneID() != paneID {
		t.Fatalf("readopted pane = %q, pid:%d known:%v; want pane %q and original live pid %d",
			restored.PaneID(), afterPID, known, paneID, oldPID)
	}
	if enrolErr := second.paneViews.Enrol(string(sid)); enrolErr != nil {
		t.Fatalf("watch the returned pane: %v", enrolErr)
	}
	if _, writeErr := restored.Write([]byte("printf 'AFTER-%s\\n' \"$NOCX_TAKEOVER_STATE\"\n")); writeErr != nil {
		t.Fatalf("type into the returned shell: %v", writeErr)
	}
	waitForMarkerAnywhere(t, second, sid, "AFTER-held")
	oldEntries := versionTakeoverInventory(t, home, oldSource.hash())
	if len(oldEntries) != 1 || oldEntries[0].HostSessionID.Session != string(sid) ||
		oldEntries[0].Launch == nil || oldEntries[0].Launch.Pid != oldPID || oldEntries[0].Exit != nil {
		t.Fatalf("the old helper no longer holds exactly the original live shell: %+v", oldEntries)
	}

	// Opening a NEW pane is the distinguishing routing assertion: it must
	// not go through the old connection held for the readopted pane.
	fresh, err := second.Transport.OpenSession(ctx, transport.OpenSpec{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("open a fresh pane after the takeover: %v", err)
	}
	awaitLocalPaneIntegration(t, second, fresh.Session.ID())
	if fresh.Hosted == nil || fresh.Hosted.Generation != newSource.hash() || fresh.Session.ID() == sid {
		t.Fatal("the fresh pane did not name the new installed generation and a new session")
	}
	newPID, known := second.Session.OwnedProcessPID(fresh.Session.ID())
	if !known || newPID <= 0 || newPID == oldPID || !alive(newPID) {
		t.Fatalf("the fresh pane has no distinct live shell: pid:%d known:%v", newPID, known)
	}
	shellPIDs = append(shellPIDs, newPID)
	newEntries := versionTakeoverInventory(t, home, newSource.hash())
	if len(newEntries) != 1 || newEntries[0].HostSessionID.Session != string(fresh.Session.ID()) ||
		newEntries[0].HostSessionID.Generation != newSource.hash() ||
		newEntries[0].Launch == nil || newEntries[0].Launch.Pid != newPID {
		t.Fatalf("the new helper did not actually spawn the fresh pane: %+v", newEntries)
	}
	if enrolErr := second.paneViews.Enrol(string(fresh.Session.ID())); enrolErr != nil {
		t.Fatalf("watch the fresh pane: %v", enrolErr)
	}
	if _, writeErr := fresh.Session.Write([]byte("printf 'FRESH-%s\\n' usable\n")); writeErr != nil {
		t.Fatalf("type into the new generation's shell: %v", writeErr)
	}
	waitForMarkerAnywhere(t, second, fresh.Session.ID(), "FRESH-usable")
}

// The same production helper with a distinct build identity. No protocol
// constants or binary payloads are patched, so both generations really run.
func versionTakeoverHelperArtifacts(t *testing.T) fakeArtifacts {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "nocx-helper")
	out, err := exec.Command("go", "build", "-ldflags=-buildid=nocx-version-takeover", "-o", binary, "../../cmd/nocx-helper").CombinedOutput() //nolint:gosec // fixed test-owned build arguments
	if err != nil {
		t.Fatalf("build the second helper generation: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(binary) //nolint:gosec // the test's own build output
	if err != nil {
		t.Fatalf("read the second helper generation: %v", err)
	}
	src, err := packArtifacts(raw)
	if err != nil {
		t.Fatalf("pack the second helper generation: %v", err)
	}
	return src
}

func versionTakeoverInventory(t *testing.T, home, generation string) []helperclient.SessionEntry {
	t.Helper()
	c, err := helperlocal.Open(t.Context(), helperlocal.Config{
		Dir: endpoint.Dir(home), Generation: proto.GenerationID(generation), Log: discardLogger(t),
	})
	if err != nil {
		t.Fatalf("reach the already-running helper generation %s: %v", generation, err)
	}
	defer func() { _ = c.Close() }()
	entries, err := c.Sessions(t.Context())
	if err != nil {
		t.Fatalf("ask generation %s for its actual sessions: %v", generation, err)
	}
	return entries
}

// These adapters replace OS process management only. Discovery goes through
// the real Unix socket, and Launch itself decides whether Stop and Spawn run.
// No fake discovery answer can manufacture a successful version takeover.
type versionTakeoverStand struct {
	t       *testing.T
	dir     string
	source  fakeArtifacts
	build   coord.Build
	current *App
	socket  *coord.Server
	stops   int
	spawns  int
	carried []content.PendingSession
	notices []coord.Notice
}

func (s *versionTakeoverStand) serve(build coord.Build) error {
	socket, err := coord.NewServer(coord.Config{
		Dir: s.dir, Build: build, Backend: versionTakeoverBackend{s.current.Transport},
		Peers: coord.SystemPeerCredentials{}, Owner: coord.SystemPathOwner{},
		SelfUID: coord.SelfUID(), Logger: discardLogger(s.t),
	})
	if err != nil {
		return err
	}
	if err := socket.Start(); err != nil {
		return err
	}
	s.socket = socket
	return nil
}

func (s *versionTakeoverStand) Stop(ctx context.Context, sighting coord.Sighting) error {
	if s.socket == nil || sighting.Hello.WSAddress != s.current.Transport.Addr() {
		return errors.New("takeover tried to stop a coordinator the stand does not hold")
	}
	// Match nocx-server's deferred teardown: unpublish discovery, then shut
	// the App down. Spawn below cannot start until both have returned.
	if err := s.socket.Close(); err != nil {
		return err
	}
	s.socket = nil
	s.current.Shutdown(ctx)
	s.stops++
	return nil
}

func (s *versionTakeoverStand) Spawn(ctx context.Context) (coord.Spawned, error) {
	if s.socket != nil || s.stops != 1 || !s.current.lifecycleStopping.Load() {
		return coord.Spawned{}, errors.New("new coordinator started before the old App stopped")
	}
	a, err := newTestApp(s.t, withLocalHelperArtifacts(s.source))
	if err != nil {
		return coord.Spawned{}, err
	}
	s.current = a
	s.carried, err = a.sessionReconciler.Pending(ctx)
	if err != nil {
		return coord.Spawned{}, fmt.Errorf("read persisted bindings before Start: %w", err)
	}
	if err := a.Start(s.t.Context()); err != nil {
		return coord.Spawned{}, err
	}
	if err := s.serve(s.build); err != nil {
		return coord.Spawned{}, err
	}
	s.spawns++
	return coord.Spawned{Command: "in-process App.Start"}, nil
}

func (s *versionTakeoverStand) Announce(notice coord.Notice) {
	s.notices = append(s.notices, notice)
}

type versionTakeoverBackend struct{ ws *transport.WSServer }

func (b versionTakeoverBackend) WSAddress() string { return b.ws.Addr() }
func (b versionTakeoverBackend) WSToken() string   { return b.ws.Token() }
