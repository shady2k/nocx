package app

// A LOCAL session's verdict is decided by asking THIS machine's daemon
// (nocx-ie23r.2 — L5's missing local inventory).
//
// WHAT WAS WRONG. Every helper route was gated on a remote destination: the
// registry refuses anything that is not `session.KindRemote`, and the readopt
// pass refuses a binding without a Host, a ProfileID and a HelperCommand —
// which is exactly what a local binding has none of, because its route back is
// a socket named after its generation. So a carried-over local session was
// `noInventory` for ever, and the notice a person read told them their command
// "may still be running" when the daemon holding it had been asked nothing.
//
// THE TWO HALVES, AND WHY THEY LIVE IN TWO TESTS.
//
//  1. The VERDICT, and what it does to the rows, is asserted here against a
//     REAL content store and a REAL endpoint socket: `absent` is the one
//     verdict with a durable trace, and "the rows survive" is the whole of
//     what `unknown` means to a person's work. The store is opened and read
//     exactly as internal/app/session_reconcile_fault_test.go opens it — a
//     fresh incarnation over the same file, which reports what is on disk
//     rather than what a double remembers.
//  2. The WIRING — that the SHIPPED root dials this machine's generation at
//     all — is asserted by the daemon's own connection counter, in
//     TestTheShippedRootAsksThisMachinesDaemonAndNeverStartsOne. A route that
//     is never wired would pass none of what follows.
//
// There is no wire assertion of the cause, and that is a property of the tree
// rather than a gap: the cause reaches the renderer through a ledger row that
// NAMES the session (content/reconcile_sqlite.go's unreconciledCause), and
// today's shell-entry writer deliberately leaves entries.session_id NULL
// (ws_ledger.go's `command` says why). So the page, the schema and the notice
// are asserted where each of them lives — this file's page assertion below,
// the contract's enum, and frontend/src/unreconciled-notice.test.tsx — and no
// second surface is invented to make a test convenient.

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/git/hostsvc"
	localgit "github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"
)

const (
	// A generation is a content hash, and endpoint.socketName validates that:
	// the socket's name is derived from it, so a non-hex one is refused before
	// anything is dialled.
	localReconGeneration  = "0123456789abcdef0123456789abcdef"
	localReconSession     = "session-this-machine-still-holds"
	localReconPane        = "pane-local"
	localReconEntry       = "00000000-0000-7000-8000-00000000e0aa"
	localReconEnvironment = "local"
	localReconWorkspace   = "workspace:local"
)

// ── this machine's endpoint, served for real ─────────────────────────────

// fakeLocalEndpoint IS this machine's daemon: a real Unix socket in a real
// endpoint directory, served by the REAL host protocol over a real
// helpersession.Service, exactly as cmd/nocx-helper serves one — the same
// shape helper_git_test.go's realHelperPeer uses over an exec lane.
//
// It is a fake in one respect only: the shell behind it is scriptedSpawner, so
// these tests do not depend on this machine's shell or on cmd/nocx-helper
// having been built. What is being asserted is which daemon the coordinator
// asked and what it did with the answer, and neither of those needs a fork.
type fakeLocalEndpoint struct {
	generation string
	dir        string
	ln         net.Listener
	svc        *helpersession.Service
	spawner    *scriptedSpawner

	mu       sync.Mutex
	accepted int
}

func startFakeLocalEndpoint(t *testing.T, dir, generation string) *fakeLocalEndpoint {
	t.Helper()
	ln, err := endpoint.Listen(dir, proto.GenerationID(generation))
	if err != nil {
		t.Fatalf("serving this machine's endpoint at %s: %v", dir, err)
	}
	spawner := &scriptedSpawner{}
	svc := helpersession.New(helpersession.Options{
		Generation: proto.GenerationID(generation),
		Spawner:    spawner,
		Log:        discardLogger(),
	})
	ep := &fakeLocalEndpoint{generation: generation, dir: dir, ln: ln, svc: svc, spawner: spawner}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
	})
	go func() {
		_ = endpoint.Serve(ctx, ln, func(conn net.Conn) {
			ep.mu.Lock()
			ep.accepted++
			ep.mu.Unlock()
			// ONE host protocol engine per connection, and the SERVICE bound
			// beside it — cmd/nocx-helper's own accept loop (Serve's doc says
			// why the sessions outlive the connection).
			h := host.New(conn, conn, generation, "instance-1", discardLogger())
			h.Register(hostsvc.New(localgit.NewFactory()))
			h.Register(svc)
			release := svc.Bind(h)
			defer release()
			_ = h.Serve(ctx)
		})
	}()
	return ep
}

// asked is how many connections this machine's daemon has been reached over.
// It is the instrument for "the local inventory WAS attempted": a route that
// never asks never dials, and there is nothing else in an isolated home that
// would.
func (e *fakeLocalEndpoint) asked() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.accepted
}

func (e *fakeLocalEndpoint) spawned() int {
	e.spawner.mu.Lock()
	defer e.spawner.mu.Unlock()
	return e.spawner.spawned
}

// stop ends the daemon the way a machine ends one: the listener closes and the
// socket stays behind, stale. That is the state a killed helper leaves, and
// endpoint.Dial answers it exactly as it answers a missing socket —
// ErrNoEndpoint, an answer rather than a failure to interpret.
func (e *fakeLocalEndpoint) stop() {
	_ = e.ln.Close()
}

// ── the durable input, written by a PREVIOUS incarnation ─────────────────

// aStoreThatCarriedALocalSessionOver writes what a coordinator that opened a
// local pane leaves behind: the binding, the recording and a block anchored to
// the pane — then closes and reopens, because the carried-over set is computed
// at Open and is exact only when nothing this incarnation wrote exists yet.
func aStoreThatCarriedALocalSessionOver(t *testing.T, path string) content.ContentDB {
	t.Helper()
	ctx := context.Background()
	first := openFaultPassStore(t, path)
	if _, err := first.Layout().CreateWorkspace(ctx,
		content.Workspace{ID: localReconWorkspace, Name: "local"},
		content.Tab{ID: "tab-local", WorkspaceID: localReconWorkspace, Layout: content.LayoutRow},
		content.Pane{ID: localReconPane, TabID: "tab-local", Cwd: "/", Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		_ = first.Close()
		t.Fatalf("CreateWorkspace: %v", err)
	}
	// THE BINDING, in the shape a local open writes: a generation and the pane,
	// and none of the three facts an ssh route needs (helper_local.go's
	// OpenHosted leaves Host, Account and HelperCommand empty and says why).
	if err := first.Ledger().CreateSession(ctx, content.Session{
		ID: localReconSession, WorkspaceID: localReconWorkspace,
		Generation: localReconGeneration, PaneID: localReconPane,
	}); err != nil {
		_ = first.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	// A recording, because the recording is what the two verdicts are
	// asymmetric ABOUT: `absent` deletes it, `unknown` keeps it for a week.
	res, err := first.SessionOutput().Append(ctx, content.SessionOutputAppend{
		SessionID: localReconSession, Offset: 0,
		Body: []byte("what this machine's shell printed"),
	})
	if err != nil || !res.Kept {
		_ = first.Close()
		t.Fatalf("record the pane's output: %v (kept=%v)", err, res.Kept)
	}
	// And a block that NAMES the session, which is the row the notice is
	// derived from (content/reconcile_sqlite.go's unreconciledCause). The
	// environment is recorded first because entries.environment_id is a real
	// foreign key into environments.
	if err := first.Ledger().EnsureEnvironment(ctx, content.Environment{
		ID: localReconEnvironment, Kind: content.EnvLocal,
	}); err != nil {
		_ = first.Close()
		t.Fatalf("EnsureEnvironment: %v", err)
	}
	if _, err := first.Ledger().Submit(ctx, content.SubmitEntry{
		ID: localReconEntry, Client: "test-client", EnvironmentID: localReconEnvironment,
		PaneID: new(localReconPane), SessionID: new(localReconSession),
		Cwd: "/", Kind: content.EntryShell, Source: content.SourceUser,
		Intent: "echo hi", Payload: "{}",
	}); err != nil {
		_ = first.Close()
		t.Fatalf("Submit: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close the previous incarnation's store: %v", err)
	}
	return openFaultPassStore(t, path)
}

// ── the route as a double, for the branches the real one cannot reach ────

type countingLocalRoute struct {
	generations []string
	inv         sessionInventory
	err         error
}

func (r *countingLocalRoute) LocalInventory(_ context.Context, generation string) (sessionInventory, error) {
	r.generations = append(r.generations, generation)
	return r.inv, r.err
}

// ── the verdict ─────────────────────────────────────────────────────────

// ONE durable input, TWO endpoint states, and the same pass both times. The
// outcome differs because the endpoint does, which is the only reason the
// test can claim the daemon is what decides.
func TestALocalSessionIsUnknownWhenItsEndpointCannotBeAskedAndAbsentWhenItDeniesIt(t *testing.T) {
	ctx := context.Background()

	t.Run("nothing is serving the generation: unknown, and the rows survive", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "content.db")
		store := aStoreThatCarriedALocalSessionOver(t, path)
		// The endpoint directory exists and holds no socket: the state a
		// machine is in when its daemon has been killed or was never started.
		// It hangs off an isolated HOME rather than a test temp directory for
		// the reason the endpoint package gives: a Unix socket path is bounded
		// by sun_path (103 bytes here, darwin's), and a nested /tmp/Test… path
		// is already most of it.
		dir := endpoint.Dir(storagetest.IsolateWithHome(t))

		reconcileSessions(ctx, store.Reconcile(), nil,
			&readoptPass{local: &localInventoryRoute{dir: dir, log: discardLogger()}},
			time.Hour, quietLogger())

		pending, err := store.Reconcile().Pending(ctx)
		if err != nil {
			t.Fatalf("Pending: %v", err)
		}
		if len(pending) != 1 {
			t.Fatalf("pending = %+v, want the session still awaiting a verdict", pending)
		}
		if got := pending[0]; got.Cause != content.CauseLocalEndpointUnreachable {
			t.Fatalf("cause = %q, want %q — nobody could be asked was asked, and the other end of the "+
				"ask is this machine's own endpoint", got.Cause, content.CauseLocalEndpointUnreachable)
		}
		if pending[0].Detail == "" {
			t.Fatal("no detail was carried — a bug report needs the error's own words")
		}
		if pending[0].SessionExists {
			t.Fatal("the session reads as existing on a machine that was never reached")
		}

		// The page a pane restores from says why, which is the row the notice
		// is drawn from — the same assertion internal/content makes for the
		// cause (TestThePageSaysWhichRowsAreAwaitingAVerdict).
		row, err := store.Ledger().Entry(ctx, localReconEntry)
		if err != nil || row == nil {
			t.Fatalf("Entry(%s): %v (nil=%v)", localReconEntry, err, row == nil)
		}
		if row.Unreconciled == nil || *row.Unreconciled != content.CauseLocalEndpointUnreachable {
			t.Fatalf("the page says %v, want %q — a restored block would otherwise claim to be running",
				row.Unreconciled, content.CauseLocalEndpointUnreachable)
		}
		if row.SessionID == nil || *row.SessionID != localReconSession {
			t.Fatalf("the block stopped naming its session: %v", row.SessionID)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close after the unanswered pass: %v", err)
		}

		// A fresh incarnation over the same file: the row and its bytes are
		// still there. `unknown` costs a week of disk precisely so this is true.
		still := stillOnDisk(t, path)
		kept, ok := still[localReconSession]
		if !ok {
			t.Fatal("the session was deleted by an ask that never reached its endpoint — a failure is " +
				"never a verdict, and absent deletes the recording and closes the block")
		}
		if kept == 0 {
			t.Fatalf("the session survived with no bytes (%d) — the recording is what a person comes back for", kept)
		}

		// And the probe did not start anything: an endpoint nothing dialled is
		// an endpoint nothing bound. A route that could start a helper — the
		// pane opener's Binary — would have tried, and a probe that spawned
		// would answer its own question.
		if _, err := endpoint.Path(dir, proto.GenerationID(localReconGeneration)); err != nil {
			t.Fatalf("derive the endpoint path: %v", err)
		}
		if _, err := endpoint.Dial(ctx, dir, proto.GenerationID(localReconGeneration)); !errors.Is(err, endpoint.ErrNoEndpoint) {
			t.Fatalf("dialling the generation after the pass = %v, want ErrNoEndpoint — something is "+
				"serving a generation nothing was supposed to start", err)
		}
	})

	t.Run("the daemon answers and does not hold it: absent, and the rows are closed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "content.db")
		store := aStoreThatCarriedALocalSessionOver(t, path)
		ep := startFakeLocalEndpoint(t, endpoint.Dir(storagetest.IsolateWithHome(t)), localReconGeneration)

		reconcileSessions(ctx, store.Reconcile(), nil,
			&readoptPass{local: &localInventoryRoute{dir: ep.dir, log: discardLogger()}},
			time.Hour, quietLogger())

		if ep.asked() == 0 {
			t.Fatal("this machine's daemon was never reached, so nothing could have judged the session")
		}
		// The whole asymmetry, in one number: asking must never spawn. The
		// daemon holds nothing, and a coordinator that opened a shell to find
		// that out would have replaced the session it was judging.
		if got := ep.spawned(); got != 0 {
			t.Fatalf("%d shells were spawned by the ask, want none — reconciliation judges a session, "+
				"it does not start one", got)
		}

		pending, err := store.Reconcile().Pending(ctx)
		if err != nil {
			t.Fatalf("Pending: %v", err)
		}
		if len(pending) != 0 {
			t.Fatalf("pending = %+v, want none: the daemon was asked and answered", pending)
		}
		row, err := store.Ledger().Entry(ctx, localReconEntry)
		if err != nil || row == nil {
			t.Fatalf("Entry(%s): %v (nil=%v)", localReconEntry, err, row == nil)
		}
		if row.Phase != content.PhaseClosed || row.Status != content.EntryUnknown {
			t.Fatalf("the block after the daemon denied the session = %s/%s, want closed/unknown",
				row.Phase, row.Status)
		}
		if row.SessionID != nil {
			t.Fatalf("session_id = %v, want nil — the pipe it named is gone", *row.SessionID)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close after the answered pass: %v", err)
		}
		if still := stillOnDisk(t, path); len(still) != 0 {
			t.Fatalf("sessions survived an answered `absent`: %+v", still)
		}
	})

	t.Run("a route that never asks answers noInventory, which is not this cause", func(t *testing.T) {
		// The safe default this bead exists to replace, asserted so the two
		// cannot be confused: with no local route wired the session is still
		// `unknown`, and the sentence a person reads blames a host that was
		// never going to be asked.
		path := filepath.Join(t.TempDir(), "content.db")
		store := aStoreThatCarriedALocalSessionOver(t, path)
		defer func() { _ = store.Close() }()

		reconcileSessions(ctx, store.Reconcile(), nil, &readoptPass{}, time.Hour, quietLogger())

		pending, err := store.Reconcile().Pending(ctx)
		if err != nil {
			t.Fatalf("Pending: %v", err)
		}
		if len(pending) != 1 || pending[0].Cause != content.CauseNoInventory {
			t.Fatalf("pending = %+v, want one session with %q — the state this bead replaces",
				pending, content.CauseNoInventory)
		}
	})

	t.Run("a remote binding is never asked over this machine's socket", func(t *testing.T) {
		// The local route must be chosen by the SHAPE of the binding, so a
		// session on a host keeps going to the route that can reach a host.
		path := filepath.Join(t.TempDir(), "content.db")
		store := aStoreThatCarriedSessionsOver(t, path)
		defer func() { _ = store.Close() }()
		local := &countingLocalRoute{}

		reconcileSessions(ctx, store.Reconcile(), nil,
			&readoptPass{local: local}, time.Hour, quietLogger())

		if len(local.generations) != 0 {
			t.Fatalf("the local route was asked about %v, want nothing — a host's session is not this "+
				"machine's, and asking the local daemon about it would turn its truthful \"I do not "+
				"hold that\" into somebody else's deletion", local.generations)
		}
	})
}

// The route's own failure classification, at the seam, without a store: the
// sentinel is what causeFor reads, and both halves of the ask carry it — the
// dial AND the answer, because a connection that goes away after the handshake
// is still this machine's helper not answering.
func TestTheLocalRouteMarksBothHalvesOfItsAsk(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	route := &localInventoryRoute{dir: dir, log: discardLogger()}

	_, err := route.LocalInventory(context.Background(), localReconGeneration)
	if err == nil {
		t.Fatal("an endpoint nothing is serving answered a question")
	}
	if !errors.Is(err, errLocalEndpointUnreachable) {
		t.Fatalf("the dial failure = %v, want the local sentinel", err)
	}
	if cause := causeFor(err); cause != content.CauseLocalEndpointUnreachable {
		t.Fatalf("causeFor(%v) = %q, want %q", err, cause, content.CauseLocalEndpointUnreachable)
	}

	// A generation that is not a content hash is refused by the endpoint
	// package, and it is still this machine's failure to reach, not a host's.
	if _, err := route.LocalInventory(context.Background(), "not-a-generation"); !errors.Is(err, errLocalEndpointUnreachable) {
		t.Fatalf("a malformed generation = %v, want the local sentinel", err)
	}
}

// ── the shipped root's wiring ───────────────────────────────────────────

// The composition root DOES dial this machine's generation, and the daemon
// says so. Nothing in this package would otherwise reach the local route:
// helperRegistry.inventories() answers for helper channels a tab has opened,
// and a coordinator that has opened no tab holds none — so a wiring that
// forgot the local route would report every carried-over local session as
// `noInventory` with every other test in this file still green.
func TestTheShippedRootAsksThisMachinesDaemonAndNeverStartsOne(t *testing.T) {
	home := storagetest.IsolateWithHome(t)
	// The artifact the root installs, and therefore the generation its own
	// socket is named for — the same bytes syntheticSource serves, so the
	// endpoint this test binds IS the generation Start installs.
	src := fakeArtifacts{payload: syntheticPayload}
	generation := src.hash()
	ep := startFakeLocalEndpoint(t, endpoint.Dir(home), generation)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := bootLocalAppOn(t, src)
	opened, err := first.Transport.OpenSession(ctx, transport.OpenSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("opening a local pane through the shipped opener: %v", err)
	}
	sid := opened.Session.ID()
	if ep.spawned() != 1 {
		t.Fatalf("the pane spawned %d sessions through this machine's daemon, want 1", ep.spawned())
	}
	first.Shutdown(ctx)

	// ── the ask ─────────────────────────────────────────────────────────
	before := ep.asked()
	second := bootLocalAppOn(t, src)
	if ep.asked() <= before {
		t.Fatal("the shipped root did not reach this machine's daemon for the session it carried " +
			"over — a local binding was refused as a route-less one, which is the defect this bead " +
			"exists to end")
	}
	if got := ep.spawned(); got != 1 {
		t.Fatalf("%d sessions were spawned in total, want 1: the ask happens on the daemon's own "+
			"connection and must not open a shell", got)
	}
	// Serving, not merely "Start returned nil": the renderer's own socket
	// answers a real method, so the synchronous pass did not wedge the start.
	conn := dialAppWS(t, second)
	if resp := callAppWS(t, conn, "history.query", map[string]any{
		"scope": "directory", "cwd": "/srv", "host": "", "limit": 1,
	}, 1); resp.Error != nil {
		t.Fatalf("the backend does not serve after reconciling a local session: %+v", resp.Error)
	}
	_ = conn.Close()
	second.Shutdown(ctx)

	// ── and when nothing is serving ─────────────────────────────────────
	// The same carried-over binding, and no daemon: the ask fails, the start
	// still succeeds, and — the half that would be a silent defect — nothing
	// was started to answer it.
	ep.stop()
	before = ep.asked()
	third := bootLocalAppOn(t, src)
	if ep.asked() != before {
		t.Fatalf("the daemon was reached after it stopped serving (%d connections, want %d)",
			ep.asked(), before)
	}
	if _, err := endpoint.Dial(ctx, endpoint.Dir(home), proto.GenerationID(generation)); !errors.Is(err, endpoint.ErrNoEndpoint) {
		t.Fatalf("dialling the generation after a start with nothing serving = %v, want ErrNoEndpoint — "+
			"the probe started a helper rather than reporting that none is running", err)
	}
	conn = dialAppWS(t, third)
	if resp := callAppWS(t, conn, "history.query", map[string]any{
		"scope": "directory", "cwd": "/srv", "host": "", "limit": 1,
	}, 1); resp.Error != nil {
		t.Fatalf("the backend does not serve after an ask that could not be answered: %+v", resp.Error)
	}
	_ = conn.Close()
	// sid is asserted to have been the session the root opened and then asked
	// about, so a future edit that made the first boot open nothing would fail
	// above rather than leave this test asserting a pass over an empty store.
	if sid == "" {
		t.Fatal("the first boot opened no session, so there was nothing carried over to ask about")
	}
}
