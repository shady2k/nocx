package app

// The completion downlink's LIFETIME, driven through the production
// composition — the review's binding condition on these tests, because the
// client-level stands build the wrapper by hand and stayed green while
// production shipped both defects: a downlink that died with the opening
// WebSocket (blocker 3) and no downlink at all after a coordinator restart
// re-adopted the pane (blocker 4).
//
// WHAT A USER CAN DO THAT THEY COULD NOT BEFORE: detach the window (or quit
// and reopen nocx) while a command runs, and the command's completion still
// reaches the helper session that owns the pane — the durable ledger says
// completed and the pane's own runtime agrees, instead of disk saying one
// thing and helper memory waiting for an authentication that never comes.
//
// WHAT IS NOT JUDGED HERE, and why (2026-09-20, review round 2). The
// downlink's own STOP — a completion observed after the session ended being
// delivered nowhere — was asserted in this file and could never have been:
// the mutation that removes the session's end from the downlink's lifetime
// leaves the assertion green with two seconds to spare, because by then the
// coordinator ingests nothing on that pane at all. The shell's write still
// succeeds into a buffer nobody reads, so there is no edge on this side that
// proves the guard was reached, and a negative asserted without one is a
// test that cannot fail. The stop is judged where its input can actually
// arrive, on the downlink's own seam: internal/helper/client's
// TestADownlinkStoppedByItsContextReportsAndLeavesTheKernelState and
// TestADeliveryOnTheWireFinishesAndTheNextOneNeverStarts.
//
// THE OBSERVABLE is the `lifecycle-complete` op arriving at the helper's
// session service (the recording fronts below), the same seam
// internal/helper/client's own tests record at — here it is reached over the
// real composition: the shipped opener or the shipped re-adoption pass, the
// real kernel, the real adapter, the real downlink wiring.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
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
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/waittest"
)

// ── the observables ───────────────────────────────────────────────────────

// recordingCompletions fronts the real session service and keeps every
// lifecycle-complete op that reaches the daemon. "The helper session's
// runtime RECEIVED the authenticated half" is this op arriving, decoded off
// the real framing; what the runtime does with it (rendezvous, expiry) is
// internal/helper/session's own judged territory.
type recordingCompletions struct {
	*helpersession.Service

	mu      sync.Mutex
	got     []proto.LifecycleCompleteParams
	arrived chan struct{}
}

func (r *recordingCompletions) Call(ctx context.Context, op string, params json.RawMessage) (any, error) {
	if op == proto.OpLifecycleComplete {
		var p proto.LifecycleCompleteParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		r.mu.Lock()
		r.got = append(r.got, p)
		r.mu.Unlock()
		select {
		case r.arrived <- struct{}{}:
		default:
		}
	}
	return r.Service.Call(ctx, op, params)
}

func (r *recordingCompletions) received() []proto.LifecycleCompleteParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]proto.LifecycleCompleteParams(nil), r.got...)
}

// awaitArrival waits for the next lifecycle-complete the daemon received,
// without polling a clock.
func awaitArrival(t *testing.T, rec *recordingCompletions) proto.LifecycleCompleteParams {
	t.Helper()
	select {
	case <-rec.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("the accepted completion never reached the helper session's runtime")
	}
	got := rec.received()
	return got[len(got)-1]
}

// sharedRecordingPeer is sharedHelperPeer with the completion recorder in
// front of the session service: the same shared daemon over the same exec
// lane, with the one op the downlink sends made observable.
func sharedRecordingPeer(t testing.TB, svc *helpersession.Service, rec *recordingCompletions) func(in io.Reader, out io.Writer) int {
	contentHash := syntheticArtifactHash
	return func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, contentHash, "instance-1", discardLogger(t))
		h.Register(hostsvc.New(localgit.NewFactory()))
		h.Register(rec)
		release := svc.Bind(h)
		defer release()
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}
}

// lifecycleLocalEndpoint is this machine's daemon for the local-route stands:
// the REAL host protocol and session service over a real Unix socket named
// for the installed generation, with a shell that holds a lifecycle channel
// (the same scripted twin the remote stands use) and the completion recorder
// in front of the service. The shell's socketpair end stays with the test,
// which is what lets one shell outlive a coordinator and speak to the
// replacement — the state a restart scenario needs and a real machine
// arranges by itself.
type lifecycleLocalEndpoint struct {
	ln      net.Listener
	svc     *helpersession.Service
	spawner *lifecycleSpawner
	rec     *recordingCompletions
}

func startLifecycleLocalEndpoint(t *testing.T, dir, generation string) *lifecycleLocalEndpoint {
	t.Helper()
	ln, err := endpoint.Listen(dir, proto.GenerationID(generation))
	if err != nil {
		t.Fatalf("serving this machine's endpoint at %s: %v", dir, err)
	}
	spawner := &lifecycleSpawner{}
	svc := helpersession.New(helpersession.Options{
		Generation: proto.GenerationID(generation),
		Spawner:    spawner,
		Log:        discardLogger(t),
	})
	rec := &recordingCompletions{Service: svc, arrived: make(chan struct{}, 16)}
	ep := &lifecycleLocalEndpoint{ln: ln, svc: svc, spawner: spawner, rec: rec}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
	})
	go func() {
		_ = endpoint.Serve(ctx, ln, func(conn net.Conn) {
			// ONE host protocol engine per connection, and the SERVICE bound
			// beside it — cmd/nocx-helper's own accept loop, for the reason
			// startFakeLocalEndpoint states: the sessions outlive the
			// connection, and that is the whole of D1.
			h := host.New(conn, conn, generation, "instance-1", discardLogger(t))
			h.Register(hostsvc.New(localgit.NewFactory()))
			h.Register(rec)
			release := svc.Bind(h)
			defer release()
			_ = h.Serve(ctx)
		})
	}()
	return ep
}

// sendRefusedEpoch is the finish the criterion refuses, over the same
// channel: the envelope's epoch names no epoch the kernel minted, so
// authentication refuses it before any state is consulted and the observing
// wrapper must never see an acceptance. The shell's own sequence is NOT
// advanced — a refused frame consumed none.
func (s *shellSide) sendRefusedEpoch(t *testing.T, evt lifecycle.Event, fence lifecycle.FenceNonce) {
	t.Helper()
	var capability lifecycle.Capability
	raw, err := hex.DecodeString(s.launch.Capability)
	if err != nil || len(raw) != len(capability) {
		t.Fatalf("the launch capability is not 32 hex bytes: %q", s.launch.Capability)
	}
	copy(capability[:], raw)
	env := lifecycle.Envelope{
		Version:    lifecycle.ProtocolVersion,
		Lane:       lifecycle.LaneID(s.launch.Lane),
		Domain:     lifecycle.DomainID(s.launch.Domain),
		Epoch:      s.launch.Epoch + 1,
		Sequence:   s.seq + 1,
		Capability: capability,
		Event:      evt,
	}
	_ = fence
	done := make(chan error, 1)
	go func() {
		_, werr := lifecyclecodec.Encode(s.conn, env)
		done <- werr
	}()
	select {
	case werr := <-done:
		if werr != nil {
			t.Fatalf("the shell could not write the refused %s: %v", evt.Kind, werr)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the shell's refused %s was never read by anything", evt.Kind)
	}
}

// completeCommand is one full command the shell runs, in the two events the
// protocol gives it, with the fence the test chooses so the arrival can be
// matched to it.
func completeCommand(t *testing.T, shell *shellSide, id string, fence lifecycle.FenceNonce) {
	t.Helper()
	attempt := lifecycle.AttemptID(id)
	shell.send(t, lifecycle.Event{
		Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &attempt, Command: "make"},
	})
	code := 0
	shell.send(t, lifecycle.Event{
		Kind: lifecycle.KindComplete,
		Complete: &lifecycle.Complete{
			ExitCode: &code,
			Fence:    fence,
		},
	})
}

// ── criterion 1: the renderer disconnects; the downlink does not ──────────

// TestACompletionAfterTheRendererDisconnectsReachesTheHelperRuntime is
// criterion 1, through the production composition: the shipped opener
// (OpenSession — the call the session.open handler makes, under the request
// context production cancels when the renderer's socket drops), this
// machine's daemon behind a real socket, and helper_hosted.go's own downlink
// wiring. The renderer disconnects; the pane deliberately lives on (AD-9); a
// command the shell completes afterwards must still reach the helper
// session's runtime.
func TestACompletionAfterTheRendererDisconnectsReachesTheHelperRuntime(t *testing.T) {
	home := storagetest.IsolateWithHome(t)
	src := fakeArtifacts{payload: syntheticPayload}
	ep := startLifecycleLocalEndpoint(t, endpoint.Dir(home), src.hash())

	// THE REQUEST CONTEXT, exactly as production holds it: handleSession
	// derives it from the connection and cancels it when the connection ends.
	openCtx, disconnect := context.WithCancel(context.Background())
	first := bootLocalAppOn(t, src)
	opened, err := first.Transport.OpenSession(openCtx, transport.OpenSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open a local pane through the shipped opener: %v", err)
	}
	sid := opened.Session.ID()
	// THE RENDERER'S CONNECTION DROPS. The open's context dies with it —
	// while the PTY goes on producing output, which is the entire point of
	// the product.
	disconnect()

	shell, _ := ep.spawner.theShell(t)
	shell.drain()
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}})
	fence := lifecycle.FenceNonce{1, 2, 3}
	completeCommand(t, shell, "after-disconnect", fence)

	got := awaitArrival(t, ep.rec)
	if len(ep.rec.received()) != 1 {
		t.Fatalf("%d completions reached the daemon for one accepted finish, want exactly 1", len(ep.rec.received()))
	}
	if got.Session.Session != string(sid) || string(got.Session.Generation) != src.hash() {
		t.Fatalf("the completion named %+v, want the opened session %s at this generation", got.Session, sid)
	}
	if got.Incarnation != (proto.Incarnation{Session: string(sid), Generation: 1}) {
		t.Fatalf("the completion's incarnation was %+v, want the runtime's own (%s, 1)", got.Incarnation, sid)
	}
	if got.Nonce != hex.EncodeToString(fence[:]) {
		t.Fatalf("the completion's nonce was %q, want the accepted fence %x", got.Nonce, fence)
	}
	if got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("the completion's exit code was %v, want the shell's 0", got.ExitCode)
	}
}

// ── criterion 2: a restart and re-adoption, both routes ───────────────────

// TestACompletionAfterAReadoptedRestartReachesTheHelperOnTheSSHRoute is
// criterion 2's remote half, through the production re-adoption pass: the
// coordinator is replaced, the replacement takes the session back over its
// helper channel, and a command completed afterwards reaches the helper
// session's runtime — which, before the fix, received nothing, because the
// re-adopted stream drove the bare kernel with no downlink behind it. A
// finish the kernel refuses is sent down nowhere, so the one completion that
// arrives is the accepted one.
func TestACompletionAfterAReadoptedRestartReachesTheHelperOnTheSSHRoute(t *testing.T) {
	spawner := &lifecycleSpawner{}
	svc := helperWithIntegratedShells(t, spawner)
	rec := &recordingCompletions{Service: svc, arrived: make(chan struct{}, 16)}
	provider := &fakeLaneProvider{peer: sharedRecordingPeer(t, svc, rec)}

	first := newIntegratedCoordinator(t, provider)
	binding := openHostedFixture(t, first.coordinator, "pane-1")
	shell, _ := spawner.theShell(t)
	shell.drain()
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}})
	waittest.WaitFor(t, "the shell integrated with the coordinator that started it", func() bool {
		return len(first.emitter.any()) > 0
	})

	first.quit()
	second := newIntegratedCoordinator(t, provider)
	adopter := &stubAdopter{}
	rc := &recordingReconciler{pending: []content.PendingSession{binding}}
	reconcileSessions(context.Background(), rc, second.reg.inventories(),
		readoptFixture(t, second.coordinator, routesFor(binding), adopter), time.Hour, quietLogger(t))
	if adopted := adopter.adoptedIDs(); len(adopted) != 1 {
		t.Fatalf("the session was not taken back at all (%v); there is nothing to deliver to", adopter.failure())
	}

	// THE FINISH THE KERNEL REFUSES: an epoch nobody minted. Authentication
	// terminates before any state is consulted and nothing rides down.
	shell.sendRefusedEpoch(t, lifecycle.Event{
		Kind:     lifecycle.KindComplete,
		Complete: &lifecycle.Complete{Fence: lifecycle.FenceNonce{9, 9, 9}},
	}, lifecycle.FenceNonce{9, 9, 9})

	// THE COMMAND A USER TYPES AFTER THE RETURN, same domain, same epoch,
	// same capability, a sequence that carries on from where it was.
	fence := lifecycle.FenceNonce{7, 8, 9}
	completeCommand(t, shell, "after-ssh-return", fence)

	got := awaitArrival(t, rec)
	if len(rec.received()) != 1 {
		t.Fatalf("%d completions crossed the wire, want exactly the accepted one", len(rec.received()))
	}
	if got.Session.Session != binding.SessionID {
		t.Fatalf("the completion named session %q, want the taken-back one %q", got.Session.Session, binding.SessionID)
	}
	if got.Nonce != hex.EncodeToString(fence[:]) {
		t.Fatalf("the completion's nonce was %q, want the accepted fence %x — a refused finish must not be the one delivered", got.Nonce, fence)
	}
	if got.Incarnation != (proto.Incarnation{Session: binding.SessionID, Generation: 1}) {
		t.Fatalf("the completion's incarnation was %+v, want the runtime's own", got.Incarnation)
	}
}

// TestACompletionAfterAReadoptedRestartReachesTheHelperOnTheLocalRoute is
// criterion 2's local half: the same restart, carried by THIS machine's
// daemon — the route whose replacement coordinator re-adopts over its own
// connection. The shell was integrated when the first coordinator died; the
// replacement takes the session back in its Start pass; the next completion
// reaches the daemon's runtime through the re-adopted channel's own
// downlink.
func TestACompletionAfterAReadoptedRestartReachesTheHelperOnTheLocalRoute(t *testing.T) {
	home := storagetest.IsolateWithHome(t)
	src := fakeArtifacts{payload: syntheticPayload}
	ep := startLifecycleLocalEndpoint(t, endpoint.Dir(home), src.hash())

	ctx := context.Background()
	first := bootLocalAppOn(t, src)
	opened, err := first.Transport.OpenSession(ctx, transport.OpenSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open a local pane through the shipped opener: %v", err)
	}
	sid := opened.Session.ID()

	// The shell integrates with the coordinator that started it — the state a
	// user is in when they quit nocx.
	shell, _ := ep.spawner.theShell(t)
	shell.drain()
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}})

	// THE COORDINATOR IS REPLACED. The daemon keeps the shell, and the
	// replacement's Start runs the re-adoption pass synchronously before it
	// listens — so when this returns the session has either been taken back
	// or the restart failed and there is nothing to test.
	first.Shutdown(ctx)
	second := bootLocalAppOn(t, src)
	if _, err := second.Session.Get(sid); err != nil {
		t.Fatalf("the replacing coordinator did not take session %s back: %v", sid, err)
	}

	fence := lifecycle.FenceNonce{4, 5, 6}
	completeCommand(t, shell, "after-local-return", fence)

	got := awaitArrival(t, ep.rec)
	if len(ep.rec.received()) != 1 {
		t.Fatalf("%d completions reached the daemon, want exactly the one accepted after the return", len(ep.rec.received()))
	}
	if got.Session.Session != string(sid) || string(got.Session.Generation) != src.hash() {
		t.Fatalf("the completion named %+v, want the re-adopted session %s", got.Session, sid)
	}
	if got.Nonce != hex.EncodeToString(fence[:]) {
		t.Fatalf("the completion's nonce was %q, want the accepted fence %x", got.Nonce, fence)
	}
}

// ── criterion 3: when the hosted session really ends, the downlink stops ──

// ── criterion 1, the other route a fresh pane can take ────────────────────

// TestACompletionOnAFreshFarHelperPaneReachesTheHelperRuntime is the same
// criterion on the REMOTE open: a pane opened on another machine's helper,
// through the shipped composition (helperRegistry.OpenHosted →
// openHoldingLease's DesiredHelper arm → openFarHelper), with the renderer
// dropping exactly as it does locally.
//
// It is a separate test because it is a separate WIRING, and that is what
// the local test cannot report: openFarHelper built its lifecycle stream
// straight over the coordinator's kernel, with no downlink, no observing
// wrapper and no bind — so a command completed on a far host was
// authenticated here and never travelled down to the runtime that owns the
// pane. The fence then sat in the far helper waiting for an authentication
// that had already happened somewhere else.
func TestACompletionOnAFreshFarHelperPaneReachesTheHelperRuntime(t *testing.T) {
	spawner := &lifecycleSpawner{}
	svc := helperWithIntegratedShells(t, spawner)
	rec := &recordingCompletions{Service: svc, arrived: make(chan struct{}, 16)}
	provider := &fakeLaneProvider{peer: sharedRecordingPeer(t, svc, rec)}

	source := stubArtifacts(t)
	store, installs := testConsentStores(t)
	_, reg := helperGitFactory(provider, source, store, installs, discardLogger(t))
	reg.registry = session.New(log.NewSlogAdapter(discardLogger(t)), nil)
	pub := lifecyclepub.New(lifecycle.New(lifecycle.Options{}))
	emitter := &recordingEmitter{}
	pub.SetEmitter(emitter)
	reg.lifecycle = pub

	// THE REQUEST CONTEXT, as production holds it for a remote open too.
	openCtx, disconnect := context.WithCancel(context.Background())
	opened, selected, err := reg.OpenHosted(openCtx, session.Config{
		Kind: session.KindRemote, Host: "host.example", Cwd: t.TempDir(),
		PaneID: "pane-far-1", ProfileID: "profile-1",
		Remote: &ssh.ConnectConfig{User: "u", DesiredMode: "helper"},
	}, "")
	if err != nil {
		t.Fatalf("opening a far-helper pane through the shipped opener: %v", err)
	}
	if !selected {
		t.Fatal("the helper was not selected for a consented machine — the harness is not exercising the far route")
	}
	if opened.StartLifecycle == nil {
		t.Fatal("the far open carried no lifecycle lane to start")
	}
	opened.StartLifecycle()
	t.Cleanup(func() {
		if opened.AbortLifecycle != nil {
			opened.AbortLifecycle()
		}
	})
	sid := opened.Session.ID()

	// THE RENDERER'S CONNECTION DROPS; the far pane lives on (AD-9).
	disconnect()

	shell, _ := spawner.theShell(t)
	shell.drain()
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}})
	waittest.WaitFor(t, "the far shell integrated with this coordinator", func() bool {
		return len(emitter.any()) > 0
	})

	fence := lifecycle.FenceNonce{4, 5, 6}
	completeCommand(t, shell, "after-far-open", fence)

	got := awaitArrival(t, rec)
	if len(rec.received()) != 1 {
		t.Fatalf("%d completions reached the far daemon for one accepted finish, want exactly 1", len(rec.received()))
	}
	if got.Session.Session != string(sid) {
		t.Fatalf("the completion named session %q, want the far pane's own %q", got.Session.Session, sid)
	}
	if got.Nonce != hex.EncodeToString(fence[:]) {
		t.Fatalf("the completion's nonce was %q, want the accepted fence %x", got.Nonce, fence)
	}
	if got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("the completion's exit code was %v, want the shell's 0", got.ExitCode)
	}
}
