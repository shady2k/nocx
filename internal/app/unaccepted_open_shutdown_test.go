package app

// An open that never reached a coordinator is the ONLY thing that can close
// the helper session it made: the session id lives in the answer nobody
// accepted, so no registry entry, no pane and no later coordinator can name
// it. When shutdown cancels the open while it is held in Attach, the rollback
// that ends that session must not inherit the cancellation that caused it.
//
// These tests drive the shipped composition root (App.Shutdown, the real
// transport, a real helper service through the helper's own wire) and hold
// ATTACH at the helper, which is after the spawn has answered. They then ask a
// SEPARATE helper connection what the helper holds.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	helperhost "github.com/shady2k/nocx/internal/helper/host"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"
)

// attachGate holds the helper's session.attach operation until the test has
// seen it. Spawn has necessarily answered by then: the client sends attach
// only after it has the spawn's result.
type attachGate struct {
	mu       sync.Mutex
	armed    bool
	entered  chan struct{}
	enteredO *sync.Once
	release  chan struct{}
	releaseO *sync.Once
}

func newAttachGate() *attachGate { return &attachGate{} }

// arm starts holding the next attach and answers the channel that closes when
// one is held, plus the release the caller must run to let it finish.
func (g *attachGate) arm() (<-chan struct{}, func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	entered := make(chan struct{})
	release := make(chan struct{})
	g.armed = true
	g.entered = entered
	g.enteredO = &sync.Once{}
	g.release = release
	g.releaseO = &sync.Once{}
	return entered, func() { g.unarm() }
}

func (g *attachGate) unarm() {
	g.mu.Lock()
	releaseO := g.releaseO
	g.armed = false
	g.mu.Unlock()
	if releaseO != nil {
		releaseO.Do(func() { close(g.release) })
	}
}

// holdAttach parks an attach before the real service sees it, and reports
// whether it parked. Every other operation runs untouched.
func (g *attachGate) holdAttach(op string) {
	if op != proto.OpAttach {
		return
	}
	g.mu.Lock()
	if !g.armed {
		g.mu.Unlock()
		return
	}
	entered, enteredO := g.entered, g.enteredO
	release := g.release
	g.mu.Unlock()
	if enteredO != nil {
		enteredO.Do(func() { close(entered) })
	}
	<-release
}

// attachHoldingService is the real helper service with one operation held. It
// is registered on the helper host exactly as the real service is, so the wire
// under test is the production wire.
type attachHoldingService struct {
	*helpersession.Service
	gate *attachGate
}

func (s *attachHoldingService) Call(ctx context.Context, op string, params json.RawMessage) (any, error) {
	s.gate.holdAttach(op)
	if op == proto.OpSpawnSSH {
		// The wire's ssh spawn asks for a credential reference this test's
		// direct-host open never had. The interactive identity is the one the
		// protocol defines as carrying none, and the scripted spawner below
		// never dials.
		var p proto.SSHSpawnParams
		if json.Unmarshal(params, &p) == nil && p.Destination.Identity.Auth != proto.SSHAuthInteractive {
			p.Destination.Identity = proto.SSHIdentity{Auth: proto.SSHAuthInteractive}
			if raw, err := json.Marshal(p); err == nil {
				params = raw
			}
		}
	}
	return s.Service.Call(ctx, op, params)
}

// fakeSSHService answers the ssh operations the coordinator's pre-selection
// binding asks for. The session under test is still the real session service;
// this only names the connection a pane rides.
type fakeSSHService struct{}

func (fakeSSHService) Name() string { return proto.ServiceSSH }

func (fakeSSHService) Ops() []string {
	return []string{proto.OpConnection, proto.OpConnections, proto.OpReconnect, proto.OpReleaseConnection}
}

func (fakeSSHService) ParamsSchema(op string) *helperhost.Schema {
	switch op {
	case proto.OpConnection:
		return helperhost.SchemaFor(proto.ConnectionParams{})
	case proto.OpConnections:
		return helperhost.SchemaFor(proto.ConnectionsParams{})
	case proto.OpReconnect:
		return helperhost.SchemaFor(proto.ReconnectParams{})
	case proto.OpReleaseConnection:
		return helperhost.SchemaFor(proto.ReleaseConnectionParams{})
	default:
		return helperhost.SchemaFor(struct{}{})
	}
}

func (fakeSSHService) Call(_ context.Context, op string, _ json.RawMessage) (any, error) {
	switch op {
	case proto.OpConnection:
		return proto.ConnectionResult{ConnectionID: "connection-under-test"}, nil
	case proto.OpConnections:
		return proto.ConnectionsResult{}, nil
	case proto.OpReconnect:
		return proto.ReconnectResult{}, nil
	case proto.OpReleaseConnection:
		return proto.ReleaseConnectionResult{}, nil
	default:
		return nil, fmt.Errorf("fake ssh service: unknown op %q", op)
	}
}

// staticSSHTarget is the destination resolver's scripted answer. Nothing in
// these tests dials a host; the far helper's own lane is scripted.
type staticSSHTarget struct{}

func (staticSSHTarget) ResolveTarget(_ context.Context, host string, _ ...ssh.ConnectOption) (ssh.DialTarget, error) {
	return ssh.DialTarget{DialEndpoint: ssh.DialEndpoint{Host: host, Port: 22, User: "dev"}}, nil
}

// helperOpenStand is the shipped App backed by a real in-process helper
// service. localSvc is reached through the App's own local opener; farSvc is
// reached through the App's far-helper registry and its scripted lane.
type helperOpenStand struct {
	t        *testing.T
	app      *App
	host     string
	remote   bool
	farMode  hostmode.Mode
	localSvc *helpersession.Service
	farSvc   *helpersession.Service
	spawner  *scriptedSpawner
	gate     *attachGate
	probe    *helperclient.Client
}

const (
	hostOnThisMachine = "this machine's helper"
	hostOnFarHelper   = "the far helper"
)

func newHelperOpenStand(t *testing.T, host string, remote bool, farMode hostmode.Mode) *helperOpenStand {
	t.Helper()
	// A disposable HOME is what the shipped App's own paths and backgrounds
	// resolve against. The helper here is in-process, so nothing forks a
	// shell, but the App still owns per-user files.
	storagetest.IsolateWithHome(t)
	gate := newAttachGate()
	spawner := &scriptedSpawner{}
	stand := &helperOpenStand{t: t, host: host, remote: remote, farMode: farMode, spawner: spawner, gate: gate}

	makeHelper := func() (*helpersession.Service, *helperclient.Client) {
		base := helpersession.New(helpersession.Options{
			Generation: proto.GenerationID(syntheticArtifactHash),
			Spawner:    spawner,
			SSHSpawner: spawner,
			Log:        discardLogger(t),
		})
		svc := &attachHoldingService{Service: base, gate: gate}
		t.Cleanup(base.Close)
		client := dialGateHelper(t, svc)
		return base, client
	}

	var localClient *helperclient.Client
	stand.localSvc, localClient = makeHelper()
	stand.farSvc, _ = makeHelper()

	// The probe is a SEPARATE helper connection to the service that owns the
	// open: it is the question "what does the helper hold", asked by somebody
	// who is not the coordinator that asked for the session.
	probeSvc := stand.localSvc
	if host == hostOnFarHelper {
		probeSvc = stand.farSvc
	}
	stand.probe = dialGateHelper(t, &attachHoldingService{Service: probeSvc, gate: gate})

	app, err := newTestApp(t)
	if err != nil {
		t.Fatalf("build the shipped app: %v", err)
	}
	stand.app = app
	installTestLocalHelper(t, app, localClient)
	installTestFarHelper(t, app, stand.farSvc, gate, farMode)

	// Explicit shutdown is the subject of the test; the App's own cleanup is
	// only a safety net for a fatal before it is reached.
	t.Cleanup(func() { app.Shutdown(context.Background()) })
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("start the shipped app: %v", err)
	}
	return stand
}

// installTestLocalHelper points the shipped opener at this test's helper
// connection. The install at Start is opted out by newTestApp, so these
// fields are the whole of the route.
func installTestLocalHelper(t *testing.T, a *App, client *helperclient.Client) {
	t.Helper()
	if a.localHelper == nil {
		t.Fatal("the shipped app built no local helper opener")
	}
	a.localHelper.client = client
	a.localHelper.installed = helperlocal.Installed{
		Binary: "/unused/nocx-helper", Generation: proto.GenerationID(syntheticArtifactHash),
	}
	a.localHelper.dir = t.TempDir()
	a.localHelper.sshTargets = staticSSHTarget{}
	// The real resolver would publish and hold a connection. The scripts below
	// are about the session, and raw integration is its own path.
	a.localHelper.modes = nil
	a.localHelper.kernel = nil
	a.localHelper.reportShellReplaced = nil
}

// installTestFarHelper wires the registry's far route to the scripted lane.
func installTestFarHelper(t *testing.T, a *App, svc *helpersession.Service, gate *attachGate, mode hostmode.Mode) {
	t.Helper()
	provider := &fakeLaneProvider{
		owner: t.Name(),
		peer:  helperPeerWithGate(t, &attachHoldingService{Service: svc, gate: gate}),
		home:  t.TempDir(),
	}
	store, _ := testConsentStores(t)
	modes := newTestHostModes(t)
	_ = modes.Set("SHA256:fake", mode)
	a.helperRegistry.lanes = provider
	a.helperRegistry.install = provider
	a.helperRegistry.source = stubArtifacts(t)
	a.helperRegistry.consent = store
	a.helperRegistry.modes = modes
	a.helperRegistry.tools = nil
	a.helperRegistry.lifecycle = nil
	endStandOnCleanup(t, provider, a.helperRegistry)
}

// helperPeerWithGate serves one scripted lane over a real helper host.
func helperPeerWithGate(t testing.TB, svc *attachHoldingService) func(in io.Reader, out io.Writer) int {
	return func(in io.Reader, out io.Writer) int {
		h := helperhost.New(in, out, syntheticArtifactHash, "instance-1", discardLogger(t))
		h.Register(fakeSSHService{})
		h.Register(svc)
		release := svc.Bind(h)
		defer release()
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}
}

// dialInProcessHelper opens one real helper connection over a socket pair,
// with the real hello handshake and the real session service behind it.
func dialGateHelper(t *testing.T, svc *attachHoldingService) *helperclient.Client {
	t.Helper()
	server, client := net.Pipe()
	h := helperhost.New(server, server, syntheticArtifactHash, "instance-1", discardLogger(t))
	h.Register(fakeSSHService{})
	h.Register(svc)
	release := svc.Bind(h)
	served := make(chan error, 1)
	go func() { served <- h.Serve(context.Background()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := helperclient.Dial(ctx, helperclient.Config{
		Exec: helperclient.NewSocketConn(client), ExpectHash: syntheticArtifactHash,
		SentinelTTL: time.Second, Log: discardLogger(t),
	})
	if err != nil {
		_ = client.Close()
		release()
		t.Fatalf("dial the in-process helper: %v", err)
	}
	t.Cleanup(func() {
		_ = c.Close()
		release()
		_ = client.Close()
		<-served
	})
	return c
}

// openSpec is the entry's request against this stand's host.
func (s *helperOpenStand) openSpec(paneID string) transport.OpenSpec {
	if s.remote {
		return transport.OpenSpec{
			Kind: "ssh", Host: "far.example", User: "dev",
			PaneID: paneID, Cols: 80, Rows: 24,
		}
	}
	return transport.OpenSpec{Kind: "local", PaneID: paneID, Cols: 80, Rows: 24}
}

// waitForAttach proves the gate is holding an attach, which is after spawn.
func (s *helperOpenStand) waitForAttach(entered <-chan struct{}, done <-chan error) {
	s.t.Helper()
	select {
	case <-entered:
	case err := <-done:
		s.t.Fatalf("the open ended before Attach was held: %v", err)
	case <-time.After(30 * time.Second):
		s.t.Fatalf("timed out waiting for the helper to be holding Attach (%d spawns so far)", s.spawner.spawnCount())
	}
	if got := s.spawner.spawnCount(); got < 1 {
		s.t.Fatal("Attach was held before the helper had spawned a session")
	}
}

// helperSessions asks the SEPARATE probe connection what the helper holds.
func (s *helperOpenStand) helperSessions() []helperclient.SessionEntry {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	entries, err := s.probe.Sessions(ctx)
	if err != nil {
		s.t.Fatalf("ask the helper's separate connection what it holds: %v", err)
	}
	return entries
}

func (s *helperOpenStand) assertHoldsNoSession(what string) {
	s.t.Helper()
	if entries := s.helperSessions(); len(entries) != 0 {
		s.t.Fatalf("%s: the helper still holds %d session(s): %+v", what, len(entries), entries)
	}
}

// spawnCount is the scripted spawner's count under its own lock.
func (s *scriptedSpawner) spawnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spawned
}

// waitForAttachOnly is waitForAttach for an entry with no error channel of its
// own: a renderer's JSON-RPC open answers on the socket, not to a caller.
func (s *helperOpenStand) waitForAttachOnly(entered <-chan struct{}) {
	s.t.Helper()
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		s.t.Fatalf("timed out waiting for the helper to be holding Attach (%d spawns so far)", s.spawner.spawnCount())
	}
	if got := s.spawner.spawnCount(); got < 1 {
		s.t.Fatal("Attach was held before the helper had spawned a session")
	}
}

// seedPane makes a real pane in the App's own layout store. An open that names
// a pane resolves its workspace through that store, so this is the state a
// renderer creates before it asks for a session.
func seedPane(t *testing.T, a *App) (*websocket.Conn, string) {
	t.Helper()
	conn := dialAppWS(t, a)
	state := callAppWS(t, conn, "layout.read", map[string]any{}, 1)
	if state.Error != nil {
		t.Fatalf("layout.read: %+v", state.Error)
	}
	var layout struct {
		DefaultWorkspaceID string `json:"defaultWorkspaceId"`
	}
	if err := json.Unmarshal(state.Result, &layout); err != nil || layout.DefaultWorkspaceID == "" {
		t.Fatalf("layout.read = %s (err %v): no default workspace", state.Result, err)
	}
	paneID := uuid.Must(uuid.NewV7()).String()
	created := callAppWS(t, conn, "tabs.create", map[string]any{
		"id":          uuid.Must(uuid.NewV7()).String(),
		"workspaceId": layout.DefaultWorkspaceID,
		"position":    0,
		"layout":      "column",
		"firstPane": map[string]any{
			"id": paneID, "cwd": "/", "kind": "local", "sizeShare": 1,
		},
	}, 2)
	if created.Error != nil {
		t.Fatalf("tabs.create: %+v", created.Error)
	}
	return conn, paneID
}

func openParamsOnTheWire(spec transport.OpenSpec) map[string]any {
	params := map[string]any{"cols": spec.Cols, "rows": spec.Rows, "paneId": spec.PaneID}
	if spec.Kind != "" {
		params["kind"] = spec.Kind
	}
	if spec.Host != "" {
		params["host"] = spec.Host
	}
	if spec.User != "" {
		params["user"] = spec.User
	}
	return params
}

// openOverWS sends a renderer's open and answers the binding generation the
// driver published for the pane. The answer is read off the same socket a
// renderer uses, so both the ack and pane.connection are the product's.
func openOverWS(t *testing.T, conn *websocket.Conn, spec transport.OpenSpec, id int) uint64 {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "open", "params": openParamsOnTheWire(spec),
	})
	if err != nil {
		t.Fatalf("marshal open: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write open: %v", err)
	}
	deadline := time.Now().Add(60 * time.Second)
	var generation uint64
	answered := false
	for !answered || generation == 0 {
		_ = conn.SetReadDeadline(deadline)
		mt, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read the open answer: %v", err)
		}
		if mt != websocket.TextMessage {
			continue
		}
		var env struct {
			ID     uint64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		if env.Method == "pane.connection" {
			var connection struct {
				PaneID     string `json:"paneId"`
				Generation uint64 `json:"generation"`
			}
			if json.Unmarshal(env.Params, &connection) == nil && connection.PaneID == spec.PaneID {
				if connection.Generation > generation {
					generation = connection.Generation
				}
			}
			continue
		}
		if env.ID != uint64(id) {
			continue
		}
		if env.Error != nil {
			t.Fatalf("open over the wire: %d %s", env.Error.Code, env.Error.Message)
		}
		answered = true
	}
	return generation
}

// writeOpenOverWS sends a renderer's open and does not wait for its answer:
// Attach is held, so the answer cannot arrive yet.
func writeOpenOverWS(t *testing.T, conn *websocket.Conn, spec transport.OpenSpec, id int) {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "open", "params": openParamsOnTheWire(spec),
	})
	if err != nil {
		t.Fatalf("marshal open: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write open: %v", err)
	}
}

// TestAnUnacceptedRPCOpenLeavesNoLiveHelperSessionAfterShutdown is the same
// defect at the renderer's entry: the transport's open handler is already
// shutdown work, and the rollback it runs is the app one.
func TestAnUnacceptedRPCOpenLeavesNoLiveHelperSessionAfterShutdown(t *testing.T) {
	for _, host := range []string{hostOnThisMachine, hostOnFarHelper} {
		t.Run(host, func(t *testing.T) {
			stand := newHelperOpenStand(t, host, host == hostOnFarHelper, hostmode.Helper)
			conn, paneID := seedPane(t, stand.app)
			entered, release := stand.gate.arm()
			defer release()

			writeOpenOverWS(t, conn, stand.openSpec(paneID), 7)
			stand.waitForAttachOnly(entered)
			stand.app.Shutdown(context.Background())
			stand.assertHoldsNoSession("after shutdown")
		})
	}
}

// TestAnUnacceptedReconnectLeavesNoLiveHelperSessionAfterShutdown covers the
// supervisor's entry. It is a real reconnect of a real pane: the first open is
// accepted and names the pane, the reconnect replays that open, and its Attach
// is held when shutdown begins.
func TestAnUnacceptedReconnectLeavesNoLiveHelperSessionAfterShutdown(t *testing.T) {
	for _, host := range []string{hostOnThisMachine, hostOnFarHelper} {
		t.Run(host, func(t *testing.T) {
			farMode := hostmode.Helper
			if host == hostOnThisMachine {
				// A remote pane whose far host declined is hosted by THIS
				// machine's helper through spawn-ssh: the reachable route for
				// a reconnect on the local helper.
				farMode = hostmode.Raw
			}
			stand := newHelperOpenStand(t, host, true, farMode)
			conn, paneID := seedPane(t, stand.app)
			generation := openOverWS(t, conn, stand.openSpec(paneID), 7)

			entered, release := stand.gate.arm()
			defer release()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- stand.app.Transport.ReconnectPane(ctx, paneID, generation) }()

			stand.waitForAttach(entered, done)
			stand.app.Shutdown(context.Background())
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				cancel()
				<-done
				t.Fatal("Shutdown returned while the reconnect was still held in Attach: ReconnectPane is not part of the transport's in-flight drain")
			}
			stand.assertHoldsNoSession("after shutdown")
		})
	}
}

// TestAnUnacceptedOpenLeavesNoLiveHelperSessionAfterShutdown is the defect.
// The spawn has answered, Attach is held, and then the App shuts down. The
// rollback that ends the session it made must not carry shutdown's own
// cancellation to the helper.
func TestAnUnacceptedOpenLeavesNoLiveHelperSessionAfterShutdown(t *testing.T) {
	for _, host := range []string{hostOnThisMachine, hostOnFarHelper} {
		t.Run(host, func(t *testing.T) {
			stand := newHelperOpenStand(t, host, host == hostOnFarHelper, hostmode.Helper)
			entered, release := stand.gate.arm()
			defer release()

			openCtx, cancelOpen := context.WithCancel(context.Background())
			defer cancelOpen()
			done := make(chan error, 1)
			go func() {
				_, err := stand.app.Transport.OpenSession(openCtx, stand.openSpec(""))
				done <- err
			}()

			stand.waitForAttach(entered, done)
			stand.app.Shutdown(context.Background())
			// SHUTDOWN MUST HAVE CANCELLED AND JOINED THIS OPEN, not merely
			// returned beside it. That join is what runs the rollback before
			// the helpers it rides are torn down.
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				cancelOpen()
				<-done
				t.Fatal("Shutdown returned while the backend open was still held in Attach: OpenSession is not part of the transport's in-flight drain")
			}

			stand.assertHoldsNoSession("after shutdown")
		})
	}
}

// TestAnAcceptedSessionRemainsLiveOnItsHelperAfterShutdown is the other half:
// shutdown is the coordinator leaving, not the session ending. The helper must
// still hold a session the open returned.
func TestAnAcceptedSessionRemainsLiveOnItsHelperAfterShutdown(t *testing.T) {
	for _, host := range []string{hostOnThisMachine, hostOnFarHelper} {
		t.Run(host, func(t *testing.T) {
			stand := newHelperOpenStand(t, host, host == hostOnFarHelper, hostmode.Helper)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			opened, err := stand.app.Transport.OpenSession(ctx, stand.openSpec(""))
			if err != nil {
				t.Fatalf("open an accepted session: %v", err)
			}
			sid := string(opened.Session.ID())

			stand.app.Shutdown(context.Background())

			entries := stand.helperSessions()
			var live bool
			for _, entry := range entries {
				if entry.HostSessionID.Session == sid {
					live = true
				}
			}
			if !live {
				t.Fatalf("an accepted session %s is not live on its helper after shutdown: %+v", sid, entries)
			}
			// End it through the separate connection, so the helper does not
			// keep a session for a coordinator the test removed.
			if closeErr := stand.probe.CloseSession(context.Background(), helperclient.HostSessionID{
				Generation: syntheticArtifactHash, Session: sid,
			}); closeErr != nil {
				t.Fatalf("close the accepted session on the helper: %v", closeErr)
			}
		})
	}
}
