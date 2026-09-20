package client_test

// The completion downlink, end to end over the real helper socket: a real
// kernel, the real client and host framing, and the real session service —
// the same shape internal/app/helper_hosted.go wires for a hosted pane.
// The rendezvous half of the arrival is judged in internal/helper/session's
// own tests; what this file judges is the WIRE: exactly one op per accepted
// completion, carrying the kernel's nonce and the session the spawn
// returned, on a local pane and on an ssh-hosted one — and nothing at all
// for a finish the kernel refused.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
)

// dlTransport is the transport id every stand binds its kernel under; the
// envelope addressing is transport-scoped, so the drive helpers and the
// failure test must name the same one.
const dlTransport = lifecycle.TransportID("t-under-test")

// --- the stand ---------------------------------------------------------------

// dlProcess satisfies session.Process with a forever-silent program: nothing
// here drives a PTY, and the lifecycle facts are driven straight into the
// kernel by the test.
type dlProcess struct {
	done chan struct{}
	once sync.Once
}

func (p *dlProcess) Read(b []byte) (int, error) {
	<-p.done
	return 0, io.EOF
}
func (p *dlProcess) Write(b []byte) (int, error) { return len(b), nil }
func (p *dlProcess) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}
func (p *dlProcess) Done() <-chan struct{}                             { return p.done }
func (p *dlProcess) WaitErr() (error, bool)                            { return nil, false }
func (p *dlProcess) Pid() int                                          { return 4242 }
func (p *dlProcess) Shell() string                                     { return "/bin/fake" }
func (p *dlProcess) ForegroundProcessGroup() (int, error)              { return 4343, nil }
func (p *dlProcess) Resize(_ context.Context, _, _, _, _ uint16) error { return nil }

type dlSpawner struct {
	mu sync.Mutex
	n  int
}

func (s *dlSpawner) Spawn(session.SpawnRequest) (session.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return &dlProcess{done: make(chan struct{})}, nil
}

// dlSSHSpawner stands in for the tagged ssh client: spawn-ssh's process is a
// shell channel on a far host, and the route under test only needs A process
// the session service can own, not a far host.
type dlSSHSpawner struct{}

func (s *dlSSHSpawner) SpawnSSH(_ context.Context, _ session.SSHSpawnRequest) (session.Process, error) {
	return &dlProcess{done: make(chan struct{})}, nil
}

// capabilityFromHex decodes a wire-spelled capability into the kernel's own
// type; a conversion from the string would not even compile, which is the
// type system keeping the bearer from being treated as text.
func capabilityFromHex(t *testing.T, spelling string) lifecycle.Capability {
	t.Helper()
	raw, err := hex.DecodeString(spelling)
	if err != nil || len(raw) != 32 {
		t.Fatalf("bad capability spelling: %v", err)
	}
	var c lifecycle.Capability
	copy(c[:], raw)
	return c
}

// recordingSession fronts the real session service and keeps every
// lifecycle-complete op that crossed the wire — the observable "the helper
// session RECEIVED a completion" is a request arriving here, decoded off the
// real framing.
type recordingSession struct {
	*session.Service
	mu        sync.Mutex
	got       []proto.LifecycleCompleteParams
	rawParams []json.RawMessage
	seen      chan struct{}
}

func (r *recordingSession) Call(ctx context.Context, op string, params json.RawMessage) (any, error) {
	if op == proto.OpLifecycleComplete {
		var p proto.LifecycleCompleteParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		r.mu.Lock()
		r.got = append(r.got, p)
		// The bytes AS THEY CROSSED, kept beside the decoded copy: the
		// conformance test validates these, not a re-marshal of the DTO,
		// which would silently drop a wire field the struct never declared.
		r.rawParams = append(r.rawParams, append(json.RawMessage(nil), params...))
		r.mu.Unlock()
		r.seen <- struct{}{}
	}
	return r.Service.Call(ctx, op, params)
}

func (r *recordingSession) received() []proto.LifecycleCompleteParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]proto.LifecycleCompleteParams(nil), r.got...)
}

// completionStand builds the helper peer with the real session service over
// a fake exec carrier, and returns the client, the recording front and the
// raw kernel the test drives.
func completionStand(t *testing.T, wire session.SSHSpawner) (*client.Client, *recordingSession, *lifecyclepub.Publisher) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := session.New(session.Options{
		Generation: "gen-under-test",
		Spawner:    &dlSpawner{},
		SSHSpawner: wire,
		Log:        log,
	})
	rec := &recordingSession{Service: svc, seen: make(chan struct{}, 64)}
	t.Cleanup(svc.Close)

	conn := newFakeConn(func(in io.Reader, out io.Writer) int {
		h := hostFor(in, out, log)
		h.Register(rec)
		release := svc.Bind(h)
		defer release()
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	})
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	// The PUBLISHER, exactly as the composition root injects it: the kernel
	// seam the adapter drives, which also delivers the accepts it mints on
	// its own authority (ADR-0062). The raw kernel stays reachable through
	// it for the attempt reads below.
	return c, rec, lifecyclepub.New(lifecycle.New(lifecycle.Options{}))
}

// driveAcceptedCompletion walks one full authenticated execution through the
// kernel through the OBSERVING wrapper — hello, accept, a submitted and
// started attempt, then the completion — exactly what the shell and the
// adapter produce in production, with the test as the far end.
func driveAcceptedCompletion(t *testing.T, k *client.CompletionObservingKernel, pub *lifecyclepub.Publisher) (lifecycle.ExecutionAttempt, lifecycle.DomainHandle, [32]byte) {
	t.Helper()
	const lane = lifecycle.LaneID("lane-under-test")
	port := &lifecycleSink{}
	if err := pub.BindTransport(dlTransport, port); err != nil {
		t.Fatalf("bind transport: %v", err)
	}
	h, err := pub.RequestDomain(lane, nil, dlTransport)
	if err != nil {
		t.Fatalf("request domain: %v", err)
	}
	env := func(seq uint64, evt lifecycle.Event) lifecycle.Envelope {
		return lifecycle.Envelope{
			Version: lifecycle.ProtocolVersion, Lane: lane, Domain: h.Domain,
			Epoch: h.Epoch, Sequence: seq, Capability: h.Capability, Event: evt,
		}
	}
	if helloErr := k.Ingest(dlTransport, env(1, lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "/bin/fake"}})); helloErr != nil {
		t.Fatalf("hello: %v", helloErr)
	}
	att, err := pub.SubmitAttempt(h.Domain, "true", "/tmp", "", "submit-under-test")
	if err != nil {
		t.Fatalf("submit attempt: %v", err)
	}
	id := att.ID
	if err := k.Ingest(dlTransport, env(2, lifecycle.Event{Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &id, Command: "true"}})); err != nil {
		t.Fatalf("start: %v", err)
	}
	var fence [32]byte
	fence[0] = 0xCC
	code := 7
	if err := k.Ingest(dlTransport, env(3, lifecycle.Event{Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{AttemptID: &id, ExitCode: &code, Fence: lifecycle.FenceNonce(fence)}})); err != nil {
		t.Fatalf("complete: %v", err)
	}
	return att, h, fence
}

// lifecycleSink collects the outbound envelopes the kernel mints; it is the
// transport port, and delivering from it is the caller's own act.
type lifecycleSink struct{}

func (lifecycleSink) Send(env lifecycle.Envelope) error { return nil }

// awaitCompletion waits for the next lifecycle-complete the service front
// received, without polling a clock.
func awaitCompletion(t *testing.T, rec *recordingSession) proto.LifecycleCompleteParams {
	t.Helper()
	select {
	case <-rec.seen:
	case <-time.After(5 * time.Second):
		t.Fatal("the accepted completion never reached the helper session")
	}
	got := rec.received()
	return got[len(got)-1]
}

// --- the criteria -----------------------------------------------------------

// TestAnAcceptedCompletionRidesDownToLocalPaneExactlyOnce is criterion 1,
// local pane: after the kernel accepts the finish, the helper session
// receives exactly ONE completion, naming the session the spawn returned,
// the runtime incarnation that generation mints ({session, 1} — the fact
// TestTheRuntimeIncarnationIsTheSessionAtGenerationOne pins), the kernel's
// nonce, and the shell's exit code. The report seam stays silent: the
// normal path succeeds, which is the paired half of criterion 3.
func TestAnAcceptedCompletionRidesDownToLocalPaneExactlyOnce(t *testing.T) {
	c, rec, pub := completionStand(t, nil)
	ctx := context.Background()

	var reported []error
	downlink := client.NewCompletionDownlink(c, ctx, func(err error) { reported = append(reported, err) })
	observing := client.NewCompletionObservingKernel(pub, downlink)

	spawned, err := c.Spawn(ctx, proto.SpawnParams{
		Cols: 80, Rows: 24,
		Lifecycle: &proto.LifecycleLaunch{Lane: "lane-under-test", Domain: "dom-under-test", Epoch: 7, Capability: hex.EncodeToString(make([]byte, 32))},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	// THE PRODUCTION ORDER: the bind happens only when the spawn RPC has
	// answered, exactly as helper_hosted wires it — everything the kernel
	// accepted before this line must have been buffered, not dropped.
	downlink.Bind(spawned.HostSessionID)

	att, _, fence := driveAcceptedCompletion(t, observing, pub)
	got := awaitCompletion(t, rec)

	if len(rec.received()) != 1 {
		t.Fatalf("%d completions crossed the wire for one accepted finish, want exactly 1", len(rec.received()))
	}
	if got.Session.Session != spawned.HostSessionID.Session || string(got.Session.Generation) != spawned.HostSessionID.Generation {
		t.Fatalf("the completion named %+v, want the spawned session %+v", got.Session, spawned.HostSessionID)
	}
	wantInc := proto.Incarnation{Session: spawned.HostSessionID.Session, Generation: 1}
	if got.Incarnation != wantInc {
		t.Fatalf("the completion's incarnation was %+v, want %+v", got.Incarnation, wantInc)
	}
	if got.Nonce != hex.EncodeToString(fence[:]) {
		t.Fatalf("the completion's nonce was %q, want the kernel's fence %x", got.Nonce, fence)
	}
	if got.ExitCode == nil || *got.ExitCode != 7 {
		t.Fatalf("the completion's exit code was %v, want 7", got.ExitCode)
	}
	if state, _ := pub.Attempt(att.ID); state.State != lifecycle.AttemptCompleted {
		t.Fatalf("the kernel's attempt is %v after a successful downlink, want untouched AttemptCompleted", state.State)
	}
	if len(reported) != 0 {
		t.Fatalf("the normal path reported %v, want a silent report seam", reported)
	}
}

// TestAnAcceptedCompletionRidesDownToAnSSHHostedPane is criterion 1's second
// route: a session whose process is a shell channel on a connection the
// helper dialed. The spawn walks spawn-ssh; the downlink is the same wire,
// and the same exactly-once fact must hold.
func TestAnAcceptedCompletionRidesDownToAnSSHHostedPane(t *testing.T) {
	spawner := &dlSSHSpawner{}
	c, rec, pub := completionStand(t, spawner)
	ctx := context.Background()

	downlink := client.NewCompletionDownlink(c, ctx, nil)
	observing := client.NewCompletionObservingKernel(pub, downlink)

	spawned, err := c.SpawnSSH(ctx, proto.SSHSpawnParams{
		Destination: proto.SSHDestination{
			Host: "far-host-under-test", Port: 22, User: "asset",
			Identity: proto.SSHIdentity{Auth: proto.SSHAuthInteractive},
		},
		Cols: 80, Rows: 24,
		Lifecycle: &proto.LifecycleLaunch{Lane: "lane-under-test", Domain: "dom-under-test", Epoch: 7, Capability: hex.EncodeToString(make([]byte, 32))},
	})
	if err != nil {
		t.Fatalf("spawn-ssh: %v", err)
	}
	downlink.Bind(spawned.HostSessionID)

	_, _, fence := driveAcceptedCompletion(t, observing, pub)
	got := awaitCompletion(t, rec)

	if len(rec.received()) != 1 {
		t.Fatalf("%d completions crossed the wire for one accepted finish on the ssh route, want exactly 1", len(rec.received()))
	}
	if got.Session.Session != spawned.HostSessionID.Session {
		t.Fatalf("the ssh completion named %+v, want the spawned session %+v", got.Session, spawned.HostSessionID)
	}
	if got.Incarnation != (proto.Incarnation{Session: spawned.HostSessionID.Session, Generation: 1}) {
		t.Fatalf("the ssh completion's incarnation was %+v, want the runtime's own", got.Incarnation)
	}
	if got.Nonce != hex.EncodeToString(fence[:]) {
		t.Fatalf("the ssh completion's nonce was %q, want the kernel's fence", got.Nonce)
	}
}

// TestARefusedFinishSendsNothingDown is criterion 2: a finish the kernel
// refuses — a wrong capability, a skipped sequence — reaches nothing. The
// refusals are the kernel's own sentinels, and the observing wrapper's
// `err == nil` gate is the only thing between them and the wire.
func TestARefusedFinishSendsNothingDown(t *testing.T) {
	c, rec, pub := completionStand(t, nil)
	ctx := context.Background()

	downlink := client.NewCompletionDownlink(c, ctx, nil)
	observing := client.NewCompletionObservingKernel(pub, downlink)

	spawned, err := c.Spawn(ctx, proto.SpawnParams{
		Cols: 80, Rows: 24,
		Lifecycle: &proto.LifecycleLaunch{Lane: "lane-under-test", Domain: "dom-under-test", Epoch: 7, Capability: hex.EncodeToString(make([]byte, 32))},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	downlink.Bind(spawned.HostSessionID)

	const lane = lifecycle.LaneID("lane-under-test")
	if bindErr := pub.BindTransport(dlTransport, lifecycleSink{}); bindErr != nil {
		t.Fatalf("bind transport: %v", bindErr)
	}
	h, err := pub.RequestDomain(lane, nil, dlTransport)
	if err != nil {
		t.Fatalf("request domain: %v", err)
	}
	if ingestErr := observing.Ingest(dlTransport, lifecycle.Envelope{
		Version: lifecycle.ProtocolVersion, Lane: lane, Domain: h.Domain,
		Epoch: h.Epoch, Sequence: 1, Capability: h.Capability,
		Event: lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "/bin/fake"}},
	}); ingestErr != nil {
		t.Fatalf("hello: %v", ingestErr)
	}
	att, err := pub.SubmitAttempt(h.Domain, "true", "/tmp", "", "submit-under-test")
	if err != nil {
		t.Fatalf("submit attempt: %v", err)
	}
	id := att.ID

	var fence [32]byte
	fence[0] = 0xCC
	code := 7
	// A completion under a foreign capability: authentication terminates
	// before any state is consulted, and nothing is sent down.
	if err := observing.Ingest(dlTransport, lifecycle.Envelope{
		Version: lifecycle.ProtocolVersion, Lane: lane, Domain: h.Domain,
		Epoch: h.Epoch, Sequence: 2, Capability: capabilityFromHex(t, hex.EncodeToString(make([]byte, 32))),
		Event: lifecycle.Event{Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{AttemptID: &id, ExitCode: &code, Fence: lifecycle.FenceNonce(fence)}},
	}); err == nil {
		t.Fatal("a completion under a foreign capability was accepted, want a kernel refusal")
	}
	// A completion that skips the start's sequence: the monotonic rule
	// refuses it, and nothing is sent down either.
	if err := observing.Ingest(dlTransport, lifecycle.Envelope{
		Version: lifecycle.ProtocolVersion, Lane: lane, Domain: h.Domain,
		Epoch: h.Epoch, Sequence: 9, Capability: h.Capability,
		Event: lifecycle.Event{Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{AttemptID: &id, ExitCode: &code, Fence: lifecycle.FenceNonce(fence)}},
	}); err == nil {
		t.Fatal("a completion that skipped a sequence was accepted, want a kernel refusal")
	}

	if got := rec.received(); len(got) != 0 {
		t.Fatalf("%d completions crossed the wire for finishes the kernel refused, want none: %+v", len(got), got)
	}
	select {
	case <-rec.seen:
		t.Fatal("a completion was recorded for a refused finish")
	default:
	}
}

// TestAFailedDownlinkLeavesTheKernelStateAsTheKernelSetIt is criterion 3's
// failure half: the helper session is gone when the completion fires, the
// delivery fails, the failure is reported — and the kernel's execution
// state is exactly what the kernel set when it accepted the completion.
func TestAFailedDownlinkLeavesTheKernelStateAsTheKernelSetIt(t *testing.T) {
	c, rec, pub := completionStand(t, nil)
	ctx := context.Background()

	var reported []error
	downlink := client.NewCompletionDownlink(c, ctx, func(err error) { reported = append(reported, err) })
	observing := client.NewCompletionObservingKernel(pub, downlink)

	spawned, err := c.Spawn(ctx, proto.SpawnParams{
		Cols: 80, Rows: 24,
		Lifecycle: &proto.LifecycleLaunch{Lane: "lane-under-test", Domain: "dom-under-test", Epoch: 7, Capability: hex.EncodeToString(make([]byte, 32))},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	downlink.Bind(spawned.HostSessionID)

	att, handle, _ := driveAcceptedCompletion(t, observing, pub)
	awaitCompletion(t, rec)

	// The session goes away; the NEXT accepted completion then has nowhere
	// to land. The kernel completes a second attempt, the send fails, and
	// the state stands.
	if closeErr := c.CloseSession(ctx, client.HostSessionID{
		Generation: spawned.HostSessionID.Generation, Session: spawned.HostSessionID.Session,
	}); closeErr != nil {
		t.Fatalf("close the session: %v", closeErr)
	}
	// ... the second attempt rides the same kernel and the same lane.
	// driveAcceptedCompletion again would re-hello; drive only the attempt
	// tail against the still-live domain. The shell reaches its next prompt
	// FIRST — submit requires a ready prompt, which is the kernel's own rule.
	prompt := func(seq uint64, evt lifecycle.Event) {
		if promptErr := observing.Ingest(dlTransport, lifecycle.Envelope{
			Version: lifecycle.ProtocolVersion, Lane: att.Lane, Domain: att.Domain,
			Epoch: handle.Epoch, Sequence: seq, Capability: handle.Capability, Event: evt,
		}); promptErr != nil {
			t.Fatalf("ingest seq %d: %v", seq, promptErr)
		}
	}
	prompt(4, lifecycle.Event{Kind: lifecycle.KindPromptReady, PromptReady: &lifecycle.PromptReady{}})
	att2, err := pub.SubmitAttempt(att.Domain, "false", "/tmp", "", "submit-under-test-2")
	if err != nil {
		t.Fatalf("submit the second attempt: %v", err)
	}
	id2 := att2.ID
	prompt(5, lifecycle.Event{Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &id2, Command: "false"}})
	var fence2 [32]byte
	fence2[0] = 0xEE
	code := 1
	prompt(6, lifecycle.Event{Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{AttemptID: &id2, ExitCode: &code, Fence: lifecycle.FenceNonce(fence2)}})

	if len(reported) == 0 {
		t.Fatal("the delivery to a closed session was not reported")
	}
	got, ok := pub.Attempt(att2.ID)
	if !ok || got.State != lifecycle.AttemptCompleted || got.ExitCode == nil || *got.ExitCode != 1 {
		t.Fatalf("the kernel's second attempt is %+v, want AttemptCompleted with exit 1 — the failed downlink must not touch it", got)
	}
	if got.Fence != lifecycle.FenceNonce(fence2) {
		t.Fatalf("the kernel's attempt carries fence %x, want the one it accepted", got.Fence)
	}
}
