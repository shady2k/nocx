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
//
// THE ROLLBACK IS HELD TOO, on its own barrier, in the rows about shutdown:
// holding only the attach let a row call Shutdown and then wait five seconds
// for the open before reading the inventory — which passes for a close the
// helper processed after Shutdown had already returned. Holding close-session
// is what lets a row assert that Shutdown itself waited for the answer.
//
// Each helper has its own gate, and every gate event carries the session id
// that operation's own params named, so a row observes the helper and the
// session it is asking about rather than whichever helper happened to move.

import (
	"context"
	"encoding/json"
	"errors"
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

// helperGate holds selected operations of ONE helper until the test has seen
// them, and reports the SESSION each held operation named. One instance per
// helper, deliberately: both helpers used to share one gate and one spawn
// counter, so an attach on the other helper could wake a row whose own helper
// had spawned nothing — and the row then observed the wrong event and could
// not tell. The session id in the event is what lets a row say which helper
// and which session it is talking about.
type helperGate struct {
	mu   sync.Mutex
	arms map[string]*gateArm
}

func newHelperGate() *helperGate { return &helperGate{arms: map[string]*gateArm{}} }

// gateArm is one armed operation kind: the session id the held call named
// (entered), the point at which the real service ANSWERED it (answered, closed
// only for a call this gate held), and the release the row runs to let the
// call finish.
type gateArm struct {
	entered  chan string
	answered chan struct{}
	release  chan struct{}

	taken        bool
	releaseOnce  sync.Once
	answeredOnce sync.Once
}

// arm holds the next op of this kind. A held close also reports when the
// helper answered it, which is the fact that proves Shutdown waited for the
// rollback instead of the test waiting five seconds beside it.
func (g *helperGate) arm(op string) *gateArm {
	g.mu.Lock()
	defer g.mu.Unlock()
	a := &gateArm{
		entered:  make(chan string, 1),
		answered: make(chan struct{}),
		release:  make(chan struct{}),
	}
	g.arms[op] = a
	return a
}

// releaseNow lets a held call finish. Idempotent, so a row's own release and a
// failing row's defer cannot close it twice.
func (a *gateArm) releaseNow() {
	a.releaseOnce.Do(func() { close(a.release) })
}

// hold parks the FIRST operation of an armed kind, after reporting the session
// id its params name, and lets every later one — and every other op — run
// untouched.
func (g *helperGate) hold(op string, params json.RawMessage) *gateArm {
	g.mu.Lock()
	a := g.arms[op]
	if a == nil || a.taken {
		g.mu.Unlock()
		return nil
	}
	a.taken = true
	g.mu.Unlock()
	a.entered <- sessionIDNamedBy(op, params)
	<-a.release
	return a
}

// sessionIDNamedBy is the session an operation's OWN params name. It is read
// from the wire shape rather than inferred from what the row expected, so a
// row can prove the helper saw the session it is asking about.
func sessionIDNamedBy(op string, params json.RawMessage) string {
	switch op {
	case proto.OpAttach:
		var p proto.AttachParams
		if json.Unmarshal(params, &p) == nil {
			return p.Session.Session
		}
	case proto.OpCloseSession:
		var p proto.CloseSessionParams
		if json.Unmarshal(params, &p) == nil {
			return p.Session.Session
		}
	}
	return ""
}

// gateService is the real helper service with selected operations held. It is
// registered on the helper host exactly as the real service is, so the wire
// under test is the production wire. The probe connection is built on the
// plain service, so no row's gate can hold the question "what do you hold".
type gateService struct {
	*helpersession.Service
	gate *helperGate
}

func (s *gateService) Call(ctx context.Context, op string, params json.RawMessage) (any, error) {
	held := s.gate.hold(op, params)
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
	result, err := s.Service.Call(ctx, op, params)
	if held != nil {
		// THE ANSWER EXISTS NOW and the host writes it next: this is the last
		// moment the barrier can say the helper ANSWERED the call it held.
		held.answeredOnce.Do(func() { close(held.answered) })
	}
	return result, err
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
// reached through the App's far-helper registry and its scripted lane. Each
// service has its OWN gate and its OWN spawner, so a row's evidence names one
// helper and one session rather than "some helper, something spawned".
type helperOpenStand struct {
	t            *testing.T
	app          *App
	host         string
	remote       bool
	farMode      hostmode.Mode
	localSvc     *helpersession.Service
	farSvc       *helpersession.Service
	localSpawner *scriptedSpawner
	farSpawner   *scriptedSpawner
	localGate    *helperGate
	farGate      *helperGate
	probe        *helperclient.Client
}

const (
	hostOnThisMachine = "this machine's helper"
	hostOnFarHelper   = "the far helper"
)

// gate is the gate of the helper this stand's opens land on.
func (s *helperOpenStand) gate() *helperGate {
	if s.host == hostOnFarHelper {
		return s.farGate
	}
	return s.localGate
}

// spawner is that same helper's own spawner: "a session was spawned" has to be
// a fact about THIS helper, which a counter shared by both helpers could not
// say.
func (s *helperOpenStand) spawner() *scriptedSpawner {
	if s.host == hostOnFarHelper {
		return s.farSpawner
	}
	return s.localSpawner
}

func newHelperOpenStand(t *testing.T, host string, remote bool, farMode hostmode.Mode) *helperOpenStand {
	t.Helper()
	// A disposable HOME is what the shipped App's own paths and backgrounds
	// resolve against. The helper here is in-process, so nothing forks a
	// shell, but the App still owns per-user files.
	storagetest.IsolateWithHome(t)
	stand := &helperOpenStand{
		t: t, host: host, remote: remote, farMode: farMode,
		localSpawner: &scriptedSpawner{}, farSpawner: &scriptedSpawner{},
		localGate: newHelperGate(), farGate: newHelperGate(),
	}

	makeHelper := func(spawner *scriptedSpawner, gate *helperGate) (*helpersession.Service, *helperclient.Client) {
		base := helpersession.New(helpersession.Options{
			Generation: proto.GenerationID(syntheticArtifactHash),
			Spawner:    spawner,
			SSHSpawner: spawner,
			Log:        discardLogger(t),
		})
		svc := &gateService{Service: base, gate: gate}
		t.Cleanup(base.Close)
		client := dialHelper(t, svc)
		return base, client
	}

	var localClient *helperclient.Client
	stand.localSvc, localClient = makeHelper(stand.localSpawner, stand.localGate)
	stand.farSvc, _ = makeHelper(stand.farSpawner, stand.farGate)

	// The probe is a SEPARATE helper connection to the service that owns the
	// open: it is the question "what does the helper hold", asked by somebody
	// who is not the coordinator that asked for the session — and it is built
	// on the PLAIN service, so no row's gate can hold the question itself.
	probeSvc := stand.localSvc
	if host == hostOnFarHelper {
		probeSvc = stand.farSvc
	}
	stand.probe = dialHelper(t, probeSvc)

	app, err := newTestApp(t)
	if err != nil {
		t.Fatalf("build the shipped app: %v", err)
	}
	stand.app = app
	installTestLocalHelper(t, app, localClient)
	installTestFarHelper(t, app, stand.farSvc, stand.farGate, farMode)

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
func installTestFarHelper(t *testing.T, a *App, svc *helpersession.Service, gate *helperGate, mode hostmode.Mode) {
	t.Helper()
	provider := &fakeLaneProvider{
		owner: t.Name(),
		peer:  helperPeerWithGate(t, &gateService{Service: svc, gate: gate}),
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
func helperPeerWithGate(t testing.TB, svc *gateService) func(in io.Reader, out io.Writer) int {
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

// dialableService is the shipped helper service as these tests register it:
// the host's dispatch, plus the connection binding the session service keeps
// for its sinks.
type dialableService interface {
	helperhost.Service
	Bind(helpersession.Sink) func()
}

// dialHelper opens one real helper connection over a socket pair, with the
// real hello handshake and the real session service behind it.
func dialHelper(t *testing.T, svc dialableService) *helperclient.Client {
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

// waitForAttach proves THIS helper is holding an attach, and answers the
// session that attach names. The session must be one this helper's own spawner
// launched: spawn has necessarily answered before the client could send attach.
func (s *helperOpenStand) waitForAttach(entered <-chan string, done <-chan error) string {
	s.t.Helper()
	var sid string
	select {
	case sid = <-entered:
	case err := <-done:
		s.t.Fatalf("the open ended before Attach was held: %v", err)
	case <-time.After(30 * time.Second):
		s.t.Fatalf("timed out waiting for the helper to be holding Attach (spawned %v so far)", s.spawner().spawnedSessionIDs())
	}
	s.assertSpawnedHere(sid)
	return sid
}

// waitForAttachOnly is waitForAttach for an entry with no error channel of its
// own: a renderer's JSON-RPC open answers on the socket, not to a caller.
func (s *helperOpenStand) waitForAttachOnly(entered <-chan string) string {
	s.t.Helper()
	var sid string
	select {
	case sid = <-entered:
	case <-time.After(30 * time.Second):
		s.t.Fatalf("timed out waiting for the helper to be holding Attach (spawned %v so far)", s.spawner().spawnedSessionIDs())
	}
	s.assertSpawnedHere(sid)
	return sid
}

// assertSpawnedHere proves the session a held operation names is one THIS
// helper's own spawner launched — not the other helper's, and not a session
// that was never spawned at all.
func (s *helperOpenStand) assertSpawnedHere(sid string) {
	s.t.Helper()
	if sid == "" {
		s.t.Fatal("the held operation named no session")
	}
	for _, spawned := range s.spawner().spawnedSessionIDs() {
		if spawned == sid {
			return
		}
	}
	s.t.Fatalf("the held operation named session %s, which this helper did not spawn: it spawned %v", sid, s.spawner().spawnedSessionIDs())
}

// spawnedSessionIDs is what this helper's own spawner actually launched.
func (s *scriptedSpawner) spawnedSessionIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.procs))
	for _, p := range s.procs {
		ids = append(ids, p.id)
	}
	return ids
}

// helperSessions asks the SEPARATE probe connection what the helper holds. The
// inventory keeps sessions that have finished, so presence alone says nothing
// about liveness: Exit is the field that does.
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

// liveEntry answers the entry for sid that the helper still reports as live.
func (s *helperOpenStand) liveEntry(sid string) *helperclient.SessionEntry {
	entries := s.helperSessions()
	for i := range entries {
		if entries[i].HostSessionID.Session == sid && entries[i].Exit == nil {
			return &entries[i]
		}
	}
	return nil
}

// assertLive proves the exact session named by a held operation is running on
// its helper. Without it "no live session afterwards" could be satisfied by a
// session that never existed.
func (s *helperOpenStand) assertLive(sid, what string) {
	s.t.Helper()
	if s.liveEntry(sid) == nil {
		s.t.Fatalf("%s: session %s is not live on the helper: %+v", what, sid, s.helperSessions())
	}
}

// assertNotLive proves the exact session is not running on its helper. A
// finished entry may remain in the inventory; Exit is what tells the two apart.
func (s *helperOpenStand) assertNotLive(sid, what string) {
	s.t.Helper()
	entries := s.helperSessions()
	for _, entry := range entries {
		if entry.HostSessionID.Session == sid && entry.Exit == nil {
			s.t.Fatalf("%s: session %s is still live on the helper: %+v", what, sid, entries)
		}
	}
}

// shutdownWhileHeldInAttach is the second half shared by the open and the
// reconnect rows: it proves that SHUTDOWN waits for the rollback, instead of a
// five-second wait in the test standing beside a Shutdown that already
// returned.
func (s *helperOpenStand) shutdownWhileHeldInAttach(attach *gateArm, openDone <-chan error) string {
	s.t.Helper()
	sid := s.waitForAttach(attach.entered, openDone)
	// The session the held attach names exists and is running: spawn answered,
	// and this is the session the rollback owes a close.
	s.assertLive(sid, "while the open is held in Attach")

	// The close gate is armed BEFORE shutdown: the rollback is what shutdown's
	// cancellation sets in motion.
	closeArm := s.gate().arm(proto.OpCloseSession)
	defer closeArm.releaseNow()

	shutdownDone := make(chan struct{})
	go func() {
		s.app.Shutdown(context.Background())
		close(shutdownDone)
	}()

	var closed string
	select {
	case closed = <-closeArm.entered:
	case <-shutdownDone:
		s.t.Fatal("Shutdown returned without the helper being asked to close the unaccepted session")
	case <-time.After(30 * time.Second):
		s.t.Fatal("timed out waiting for the rollback's close-session to reach the helper")
	}
	if closed != sid {
		s.t.Fatalf("the rollback closed session %q, but the held attach named %q", closed, sid)
	}
	// AND SHUTDOWN IS STILL PARKED IN IT. This is the assertion these rows
	// exist for: a Shutdown that returned here did not wait for the close the
	// open owed, and the inventory read below would be taken over a rollback
	// still in flight.
	select {
	case <-shutdownDone:
		s.t.Fatal("Shutdown returned before the helper answered the close of the unaccepted session")
	default:
	}

	closeArm.releaseNow()
	select {
	case <-closeArm.answered:
	case <-time.After(30 * time.Second):
		s.t.Fatal("the helper never answered the close of the unaccepted session")
	}
	select {
	case <-shutdownDone:
	case <-time.After(30 * time.Second):
		s.t.Fatal("Shutdown did not return after the helper answered the close")
	}

	// READ IMMEDIATELY, before joining the open: what the probe reports now is
	// the state Shutdown left behind, not one the test waited into existence.
	s.assertNotLive(sid, "after shutdown")

	select {
	case <-openDone:
	case <-time.After(30 * time.Second):
		s.t.Fatal("the open did not end although Shutdown returned")
	}
	return sid
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
			attach := stand.gate().arm(proto.OpAttach)
			defer attach.releaseNow()

			writeOpenOverWS(t, conn, stand.openSpec(paneID), 7)
			sid := stand.waitForAttachOnly(attach.entered)
			stand.assertLive(sid, "while the renderer's open is held in Attach")
			stand.app.Shutdown(context.Background())
			stand.assertNotLive(sid, "after shutdown")
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

			attach := stand.gate().arm(proto.OpAttach)
			defer attach.releaseNow()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- stand.app.Transport.ReconnectPane(ctx, paneID, generation) }()

			stand.shutdownWhileHeldInAttach(attach, done)
		})
	}
}

// TestAnUnacceptedOpenLeavesNoLiveHelperSessionAfterShutdown is the defect.
// The spawn has answered, Attach is held, and then the App shuts down. The
// rollback that ends the session it made must not carry shutdown's own
// cancellation to the helper, and Shutdown must WAIT for that close.
func TestAnUnacceptedOpenLeavesNoLiveHelperSessionAfterShutdown(t *testing.T) {
	for _, host := range []string{hostOnThisMachine, hostOnFarHelper} {
		t.Run(host, func(t *testing.T) {
			stand := newHelperOpenStand(t, host, host == hostOnFarHelper, hostmode.Helper)
			attach := stand.gate().arm(proto.OpAttach)
			defer attach.releaseNow()

			openCtx, cancelOpen := context.WithCancel(context.Background())
			defer cancelOpen()
			done := make(chan error, 1)
			go func() {
				_, err := stand.app.Transport.OpenSession(openCtx, stand.openSpec(""))
				done <- err
			}()

			stand.shutdownWhileHeldInAttach(attach, done)
		})
	}
}

// TestAnOpenWhoseCallerDeadlineExpiresLeavesNoLiveHelperSessionAfterShutdown
// is the LOCAL route's own case, and it needs no shutdown to fail. The
// caller's deadline expires while the helper is holding the attach — the
// helper is answering, and the spawn's answer already gave this coordinator
// the session's only name. A local guard that read the deadline as "the helper
// did not answer" skipped the close and left a shell nobody could name.
//
// ON THE FAR ROUTE THIS SCENARIO KEEPS THE GUARD ON PURPOSE: a far helper that
// did not answer the attach will not answer the close either, and waiting out
// another answer budget there is the bound the task preserves. So there is no
// far subtest here: it would assert the opposite of the route's deliberate
// behaviour.
func TestAnOpenWhoseCallerDeadlineExpiresLeavesNoLiveHelperSessionAfterShutdown(t *testing.T) {
	stand := newHelperOpenStand(t, hostOnThisMachine, false, hostmode.Helper)
	attach := stand.gate().arm(proto.OpAttach)
	defer attach.releaseNow()

	// A real deadline short enough to expire while Attach is held, so the
	// failure the open sees is the CALLER giving up, not shutdown.
	openCtx, cancelOpen := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelOpen()
	done := make(chan error, 1)
	go func() {
		_, err := stand.app.Transport.OpenSession(openCtx, stand.openSpec(""))
		done <- err
	}()

	// The session EXISTS and is live while the attach is held: this is the
	// state the defect leaves behind, and the premise that makes the check
	// after shutdown about a session that was really there.
	sid := stand.waitForAttach(attach.entered, done)
	stand.assertLive(sid, "while the caller's deadline is still in the future")

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the open answered %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the open did not end when its own deadline expired")
	}

	// The rollback ran during that open — the close is part of it, on the
	// local route, whichever way the attach failed. Shutdown must not be what
	// closes the session.
	stand.app.Shutdown(context.Background())
	stand.assertNotLive(sid, "after shutdown")
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

			// The inventory keeps finished sessions, so presence alone is not
			// liveness: this must be the SAME session id, still running.
			stand.assertLive(sid, "an accepted session after shutdown")
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
