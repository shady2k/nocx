package lifecyclepub_test

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/testwait"
)

// noopPort swallows outbound envelopes (accept, refresh_request). The shell
// side is not reading; the kernel treats send failures as best-effort.
type noopPort struct{}

func (noopPort) Send(lifecycle.Envelope) error { return nil }

// recordingPort records every outbound envelope, so tests can assert what the
// publisher actually flushed to the transport (decision 9: the accept is the
// thing that must not go out before the renderer's acknowledgement).
type recordingPort struct {
	mu   sync.Mutex
	sent []lifecycle.Envelope
	// onSend runs inside Send, so a test can assert the ORDER of a delivery
	// against whatever else the publisher did — not merely that both happened.
	onSend func(lifecycle.Envelope)
}

func (r *recordingPort) Send(env lifecycle.Envelope) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, env)
	if r.onSend != nil {
		r.onSend(env)
	}
	return nil
}

func (r *recordingPort) kinds() []lifecycle.EventKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]lifecycle.EventKind, 0, len(r.sent))
	for _, e := range r.sent {
		out = append(out, e.Event.Kind)
	}
	return out
}

// recorder collects published facts for assertions.
type recorder struct {
	mu    sync.Mutex
	facts []lifecyclepub.Fact
}

func (r *recorder) PublishLifecycle(f lifecyclepub.Fact) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.facts = append(r.facts, f)
}

func (r *recorder) all() []lifecyclepub.Fact {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]lifecyclepub.Fact, len(r.facts))
	copy(out, r.facts)
	return out
}

// env builds an authenticated envelope for a minted domain handle, exactly as
// the lifecycle adapter would after substituting the capability.
func env(lane lifecycle.LaneID, h lifecycle.DomainHandle, seq uint64, evt lifecycle.Event) lifecycle.Envelope {
	return lifecycle.Envelope{
		Version: lifecycle.ProtocolVersion, Lane: lane, Domain: h.Domain,
		Epoch: h.Epoch, Sequence: seq, Capability: h.Capability, Event: evt,
	}
}

func helloEvt() lifecycle.Event {
	return lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}}
}

func promptEvt() lifecycle.Event {
	return lifecycle.Event{Kind: lifecycle.KindPromptReady, PromptReady: &lifecycle.PromptReady{}}
}

func startEvt(id *lifecycle.AttemptID, cmd string) lifecycle.Event {
	return lifecycle.Event{Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: id, Command: cmd}}
}

func completeEvt(id lifecycle.AttemptID, code int, f lifecycle.FenceNonce) lifecycle.Event {
	return lifecycle.Event{Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{AttemptID: &id, ExitCode: &code, Fence: f}}
}

func fenceByte(n byte) lifecycle.FenceNonce {
	var f lifecycle.FenceNonce
	for i := range f {
		f[i] = n
	}
	return f
}

func mustIngest(t *testing.T, pub *lifecyclepub.Publisher, tID lifecycle.TransportID, e lifecycle.Envelope) {
	t.Helper()
	if err := pub.Ingest(tID, e); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
}

// mustAckEstablishment acknowledges the latest published establishment
// generation for the lane, exactly as the renderer does after committing
// the editor presentation (decision 9).
func mustAckEstablishment(t *testing.T, pub *lifecyclepub.Publisher, r *recorder, lane lifecycle.LaneID, h lifecycle.DomainHandle) {
	t.Helper()
	facts := r.all()
	var gen string
	for i := len(facts) - 1; i >= 0; i-- {
		if facts[i].Lane == string(lane) && facts[i].Generation != "" {
			gen = facts[i].Generation
			break
		}
	}
	if gen == "" {
		t.Fatalf("no published fact carries an establishment generation; facts=%+v", facts)
	}
	if err := pub.AcknowledgeEstablishment(lane, h.Domain, h.Epoch, gen); err != nil {
		t.Fatalf("AcknowledgeEstablishment: %v", err)
	}
}

// The publisher is the kernel-shaped seam the lifecyclechannel adapter
// consumes: the composition root injects it where it would have injected the
// kernel, and every mutation an adapter drives is projected. This assertion
// is what keeps that composition compiling.
var _ lifecyclechannel.Kernel = (*lifecyclepub.Publisher)(nil)

// TestPublisherPublishesOnTransitions drives the full happy path through the
// real kernel and asserts the emitted fact sequence: hello → prompt_ready,
// submit → running(open), start attach deduped, complete → running(completed)
// with exit status, timestamps and fence, prompt_ready → prompt_ready, and a
// shell-originated start with its own id and origin.
func TestPublisherPublishesOnTransitions(t *testing.T) {
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k)
	r := &recorder{}
	pub.SetEmitter(r)

	if err := pub.BindTransport("T", noopPort{}); err != nil {
		t.Fatal(err)
	}
	h, err := pub.RequestDomain("L", nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	if got := len(r.all()); got != 0 {
		t.Fatalf("RequestDomain published %d facts, want 0 (a Pending domain changes no lifecycle)", got)
	}

	// hello → accept → the lane is PromptReady(domain): the published
	// domain_established the frontend keys enhanced mode on.
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))
	facts := r.all()
	if len(facts) != 1 {
		t.Fatalf("after hello: %d facts, want 1", len(facts))
	}
	f := facts[0]
	if f.Lane != "L" || f.Lifecycle != lifecyclepub.LifecyclePromptReady {
		t.Fatalf("fact = %+v, want lane L prompt_ready", f)
	}
	if f.Domain != string(h.Domain) || f.Epoch != h.Epoch {
		t.Fatalf("fact must carry the established domain and epoch, got %+v", f)
	}
	if f.Generation == "" {
		t.Fatalf("prompt_ready fact must carry the establishment generation, got %+v", f)
	}
	// The renderer acknowledges the published fact; only then is the accept
	// flushed and the domain live (decision 9).
	mustAckEstablishment(t, pub, r, "L", h)

	// App-originated submit: Running(attempt) with the app-owned text.
	att, err := pub.SubmitAttempt(h.Domain, "make", "/work/nocx", "local")
	if err != nil {
		t.Fatalf("SubmitAttempt: %v", err)
	}
	facts = r.all()
	if len(facts) != 2 {
		t.Fatalf("after submit: %d facts, want 2", len(facts))
	}
	run := facts[1]
	if run.Lifecycle != lifecyclepub.LifecycleRunning || run.Attempt == nil {
		t.Fatalf("submit fact = %+v, want running with an attempt", run)
	}
	if run.Attempt.ID != string(att.ID) || run.Attempt.State != lifecyclepub.AttemptOpen {
		t.Fatalf("submit attempt = %+v", run.Attempt)
	}
	if run.Attempt.Command != "make" || run.Attempt.Origin != lifecyclepub.OriginApp {
		t.Fatalf("app attempt must keep its app-owned text and origin, got %+v", run.Attempt)
	}

	// The shell's start attaches to the pending app attempt: the projection
	// is unchanged, so the dedupe suppresses a duplicate notification.
	mustIngest(t, pub, "T", env("L", h, 2, startEvt(nil, "make")))
	if got := len(r.all()); got != 2 {
		t.Fatalf("attach re-published %d facts, want 2 (projection unchanged)", got)
	}

	// Authenticated completion: the lane stays Running(attempt) but the
	// attempt's projection carries the exit status, the completion
	// timestamp and the render fence.
	mustIngest(t, pub, "T", env("L", h, 3, completeEvt(att.ID, 0, fenceByte(0x51))))
	facts = r.all()
	if len(facts) != 3 {
		t.Fatalf("after complete: %d facts, want 3", len(facts))
	}
	done := facts[2]
	if done.Lifecycle != lifecyclepub.LifecycleRunning || done.Attempt == nil {
		t.Fatalf("complete fact = %+v, want running with a completed attempt", done)
	}
	if done.Attempt.State != lifecyclepub.AttemptCompleted {
		t.Fatalf("attempt state = %q, want completed", done.Attempt.State)
	}
	if done.Attempt.ExitCode == nil || *done.Attempt.ExitCode != 0 {
		t.Fatalf("exit code = %v, want 0", done.Attempt.ExitCode)
	}
	if done.Attempt.CompletedAt == nil || done.Attempt.CompletedAt.IsZero() {
		t.Fatal("completedAt must be set on completion")
	}
	wantFence := "5151515151515151515151515151515151515151515151515151515151515151"
	if done.Attempt.Fence != wantFence {
		t.Fatalf("fence = %q, want %q", done.Attempt.Fence, wantFence)
	}

	// prompt_ready clears the attempt: PromptReady(domain).
	mustIngest(t, pub, "T", env("L", h, 4, promptEvt()))
	facts = r.all()
	if len(facts) != 4 || facts[3].Lifecycle != lifecyclepub.LifecyclePromptReady || facts[3].Attempt != nil {
		t.Fatalf("after prompt_ready: %+v, want prompt_ready with no attempt", facts[3:])
	}

	// A shell-originated start names its own attempt: origin shell.
	shellAtt := lifecycle.AttemptID("att-shell-1")
	mustIngest(t, pub, "T", env("L", h, 5, startEvt(&shellAtt, "ls")))
	facts = r.all()
	if len(facts) != 5 {
		t.Fatalf("after shell start: %d facts, want 5", len(facts))
	}
	sh := facts[4]
	if sh.Attempt == nil || sh.Attempt.ID != string(shellAtt) || sh.Attempt.Origin != lifecyclepub.OriginShell {
		t.Fatalf("shell-originated attempt = %+v, want id att-shell-1 origin shell", sh.Attempt)
	}
}

// TestPublisherRejectedFramePublishesNothing proves the kernel's "invalid
// events mutate nothing" reaches the renderer: a frame the kernel rejects
// (wrong capability) publishes no fact, because the projection is unchanged.
func TestPublisherRejectedFramePublishesNothing(t *testing.T) {
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k)
	r := &recorder{}
	pub.SetEmitter(r)
	_ = pub.BindTransport("T", noopPort{})
	h, _ := pub.RequestDomain("L", nil, "T")
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))

	bad := env("L", h, 2, promptEvt())
	bad.Capability[0] ^= 0xff // a wrong bearer: rejected before any state is consulted
	if err := pub.Ingest("T", bad); err == nil {
		t.Fatal("wrong capability must be rejected")
	}
	if got := len(r.all()); got != 1 {
		t.Fatalf("rejected frame published %d facts, want 1", got)
	}
}

// fakeClock is a controllable time source for kernel budget tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// TestPublisherDesyncAndBudgetRevoke proves the desynchronization entry and
// the budget-exhaustion revocation both cross as facts: Desynchronized(domain)
// on the gap, then native when the scan-byte budget runs out.
func TestPublisherDesyncAndBudgetRevoke(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	k := lifecycle.New(lifecycle.Options{
		Now:     clock.now,
		Budgets: lifecycle.Budgets{ScanBytes: 10},
	})
	pub := lifecyclepub.New(k)
	r := &recorder{}
	pub.SetEmitter(r)
	_ = pub.BindTransport("T", noopPort{})
	h, _ := pub.RequestDomain("L", nil, "T")
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))

	if err := pub.NotifyGap("T", h.Domain, 5, 1); err != nil {
		t.Fatalf("NotifyGap: %v", err)
	}
	facts := r.all()
	if len(facts) != 2 {
		t.Fatalf("after gap: %d facts, want 2", len(facts))
	}
	desync := facts[1]
	if desync.Lifecycle != lifecyclepub.LifecycleDesynchronized || desync.Domain != string(h.Domain) {
		t.Fatalf("gap fact = %+v, want desynchronized on the domain", desync)
	}

	// A second gap lands in the DomainDesynchronized case, where the scan
	// budgets govern: 5 + 6 bytes exceeds the 10-byte limit, the domain is
	// revoked and the lane falls to native — a state change the renderer
	// must see even though the reported gap itself is just an accounting
	// update.
	if err := pub.NotifyGap("T", h.Domain, 6, 1); err != nil {
		t.Fatalf("second NotifyGap: %v", err)
	}
	facts = r.all()
	if len(facts) != 3 || facts[2].Lifecycle != lifecyclepub.LifecycleNative {
		t.Fatalf("after budget exhaustion: %+v, want a native fact", facts[2:])
	}
	if facts[2].Domain != "" || facts[2].Attempt != nil {
		t.Fatalf("native fact must carry no domain or attempt, got %+v", facts[2])
	}
}

// TestPublisherPublishesRevokeWhileQuarantining proves the one mutation a
// REJECTED frame can still cause: the recovery budget expires while the
// domain is desynchronized, so the kernel revokes it (the lane falls to
// native) and then rejects the quarantined frame. Ingest returns an error,
// yet the renderer must hear about the revocation — the publisher publishes
// on failure, and the change-dedupe is what keeps every other rejection
// silent.
func TestPublisherPublishesRevokeWhileQuarantining(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	k := lifecycle.New(lifecycle.Options{
		Now:     clock.now,
		Budgets: lifecycle.Budgets{ScanDuration: time.Second},
	})
	pub := lifecyclepub.New(k)
	r := &recorder{}
	pub.SetEmitter(r)
	_ = pub.BindTransport("T", noopPort{})
	h, _ := pub.RequestDomain("L", nil, "T")
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))

	if err := pub.NotifyGap("T", h.Domain, 5, 1); err != nil {
		t.Fatalf("NotifyGap: %v", err)
	}
	clock.advance(2 * time.Second) // the recovery budget expires

	if err := pub.Ingest("T", env("L", h, 2, promptEvt())); err == nil {
		t.Fatal("a quarantined event must be rejected")
	}
	facts := r.all()
	if len(facts) != 3 || facts[2].Lifecycle != lifecyclepub.LifecycleNative {
		t.Fatalf("after quarantine revoke: %+v, want a native fact", facts[2:])
	}
}

// TestPublisherTransportLostPublishesLost proves a transport loss cascades to
// every lane that used it as a lost fact, and that a lane on another
// transport is untouched (deduped).
func TestPublisherTransportLostPublishesLost(t *testing.T) {
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k)
	r := &recorder{}
	pub.SetEmitter(r)
	_ = pub.BindTransport("T1", noopPort{})
	_ = pub.BindTransport("T2", noopPort{})
	h1, _ := pub.RequestDomain("L1", nil, "T1")
	_, _ = pub.RequestDomain("L2", nil, "T2")
	mustIngest(t, pub, "T1", env("L1", h1, 1, helloEvt()))

	if err := pub.TransportLost("T1"); err != nil {
		t.Fatalf("TransportLost: %v", err)
	}
	facts := r.all()
	if len(facts) != 2 {
		t.Fatalf("after loss: %d facts, want 2 (prompt_ready + lost)", len(facts))
	}
	lost := facts[1]
	if lost.Lifecycle != lifecyclepub.LifecycleLost || lost.Domain != "" || lost.Attempt != nil {
		t.Fatalf("lost fact = %+v, want lost with no domain or attempt", lost)
	}
}

// TestPublisherAbandonPublishesUnknown proves the explicit abandonment path
// (native-mode escape) crosses: the lane stays running but the attempt's
// projection becomes unknown, and no exit status is invented.
func TestPublisherAbandonPublishesUnknown(t *testing.T) {
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k)
	r := &recorder{}
	pub.SetEmitter(r)
	_ = pub.BindTransport("T", noopPort{})
	h, _ := pub.RequestDomain("L", nil, "T")
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))
	mustAckEstablishment(t, pub, r, "L", h)
	att, err := pub.SubmitAttempt(h.Domain, "sleep 1000", "/", "local")
	if err != nil {
		t.Fatal(err)
	}

	if err := pub.AbandonAttempt(att.ID); err != nil {
		t.Fatalf("AbandonAttempt: %v", err)
	}
	facts := r.all()
	if len(facts) != 3 {
		t.Fatalf("after abandon: %d facts, want 3", len(facts))
	}
	ab := facts[2]
	if ab.Attempt == nil || ab.Attempt.State != lifecyclepub.AttemptUnknown {
		t.Fatalf("abandon fact = %+v, want an unknown attempt", ab.Attempt)
	}
	if ab.Attempt.ExitCode != nil || ab.Attempt.CompletedAt != nil {
		t.Fatalf("abandon must not invent an exit status, got %+v", ab.Attempt)
	}
}

// TestPublisherReplayLane re-emits the current projection on demand — the
// AD-9 reconnect resume — even when nothing changed since the last emission,
// and refreshes the dedupe baseline so a later real change still publishes.
func TestPublisherReplayLane(t *testing.T) {
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k)
	r := &recorder{}
	pub.SetEmitter(r)
	_ = pub.BindTransport("T", noopPort{})
	h, _ := pub.RequestDomain("L", nil, "T")
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))
	mustAckEstablishment(t, pub, r, "L", h)

	pub.ReplayLane("L")
	if got := len(r.all()); got != 2 {
		t.Fatalf("after replay: %d facts, want 2 (replay bypasses the dedupe)", got)
	}
	if got := r.all()[1]; got.Lifecycle != lifecyclepub.LifecyclePromptReady {
		t.Fatalf("replayed fact = %+v", got)
	}

	// prompt_ready arrives with no projection change: deduped against the
	// replayed baseline.
	mustIngest(t, pub, "T", env("L", h, 2, promptEvt()))
	if got := len(r.all()); got != 2 {
		t.Fatalf("unchanged event after replay published %d facts, want 2", got)
	}
}

// TestPublisherForwardsErrors proves the publisher returns the kernel's
// errors unchanged: a rejected mutation is an error to the caller, not a
// swallowed success.
func TestPublisherForwardsErrors(t *testing.T) {
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k)
	r := &recorder{}
	pub.SetEmitter(r)

	// Ingest on an unbound transport.
	if err := pub.Ingest("nope", lifecycle.Envelope{}); err == nil {
		t.Fatal("Ingest on an unbound transport must error")
	}
	// RequestDomain on an unbound transport.
	if _, err := pub.RequestDomain("L", nil, "T"); err == nil {
		t.Fatal("RequestDomain on an unbound transport must error")
	}
	// SubmitAttempt for an unknown domain.
	if _, err := pub.SubmitAttempt("dom-nope", "x", "/", "local"); err == nil {
		t.Fatal("SubmitAttempt for an unknown domain must error")
	}
	if got := len(r.all()); got != 0 {
		t.Fatalf("failed mutations published %d facts, want 0", got)
	}
}

// TestPublisherRecoveryProjection proves the publication half of decision 8:
// a lost lane whose domain minted a recovery nonce publishes the recovery
// contract (fence + generation), and RecoverLane — the ack — publishes the
// native transition. The domain stays permanently lost.
func TestPublisherRecoveryProjection(t *testing.T) {
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k)
	r := &recorder{}
	pub.SetEmitter(r)
	_ = pub.BindTransport("T", noopPort{})
	h, _ := pub.RequestDomain("L", nil, "T")
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))

	if err := pub.TransportLost("T"); err != nil {
		t.Fatalf("TransportLost: %v", err)
	}
	facts := r.all()
	lost := facts[len(facts)-1]
	if lost.Lifecycle != lifecyclepub.LifecycleLost {
		t.Fatalf("fact = %+v, want lost", lost)
	}
	wantNonce := hex.EncodeToString(h.Recovery[:])
	if lost.Recovery == nil || lost.Recovery.Fence != wantNonce || lost.Recovery.Generation != wantNonce {
		t.Fatalf("lost fact recovery = %+v, want fence+generation %s", lost.Recovery, wantNonce)
	}

	if err := pub.RecoverLane("L"); err != nil {
		t.Fatalf("RecoverLane: %v", err)
	}
	facts = r.all()
	native := facts[len(facts)-1]
	if native.Lifecycle != lifecyclepub.LifecycleNative {
		t.Fatalf("post-recover fact = %+v, want native", native)
	}
	if d, ok := pub.Domain(h.Domain); ok && d.State != lifecycle.DomainLost {
		t.Fatalf("domain after recover = %v, want permanently DomainLost", d.State)
	}
	// A second recover is idempotent and publishes nothing new.
	if err := pub.RecoverLane("L"); err != nil {
		t.Fatalf("duplicate RecoverLane: %v", err)
	}
	if got := len(r.all()); got != len(facts) {
		t.Fatalf("duplicate recover published %d facts, want %d", got, len(facts))
	}
}

// --- decision 9: no acknowledgement, no accept ------------------------------

// TestPublisherAcceptFlushedOnlyOnAcknowledgement proves the acceptance
// criterion "no acknowledgement means no accept" at the publication
// boundary: the accept sits pending while no ack has landed, a stale
// generation cannot release it, and the exact generation's ack is the
// closing event that flushes it — once.
func TestPublisherAcceptFlushedOnlyOnAcknowledgement(t *testing.T) {
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k)
	r := &recorder{}
	pub.SetEmitter(r)
	port := &recordingPort{}
	_ = pub.BindTransport("T", port)
	h, _ := pub.RequestDomain("L", nil, "T")
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))
	facts := r.all()
	gen := facts[0].Generation
	if gen == "" {
		t.Fatal("hello must publish a fact carrying the establishment generation")
	}
	// No acknowledgement: the accept has not reached the transport.
	if got := port.kinds(); len(got) != 0 {
		t.Fatalf("accept flushed before any acknowledgement: %v", got)
	}
	// A stale generation is refused and releases nothing.
	if err := pub.AcknowledgeEstablishment("L", h.Domain, h.Epoch, "est-0000000000000000"); !errors.Is(err, lifecyclepub.ErrEstablishmentGeneration) {
		t.Fatalf("stale generation = %v, want ErrEstablishmentGeneration", err)
	}
	if got := port.kinds(); len(got) != 0 {
		t.Fatalf("stale ack flushed the accept: %v", got)
	}
	// The domain is not live before the ack: events are rejected.
	if _, err := pub.SubmitAttempt(h.Domain, "make", "/", "local"); !errors.Is(err, lifecycle.ErrDomainPending) {
		t.Fatalf("submit before the ack = %v, want not past accept", err)
	}
	// The exact generation's ack is the closing event.
	if err := pub.AcknowledgeEstablishment("L", h.Domain, h.Epoch, gen); err != nil {
		t.Fatalf("AcknowledgeEstablishment: %v", err)
	}
	if got := port.kinds(); len(got) != 1 || got[0] != lifecycle.KindAccept {
		t.Fatalf("after ack: %v, want exactly one accept", got)
	}
	if _, err := pub.SubmitAttempt(h.Domain, "make", "/", "local"); err != nil {
		t.Fatalf("submit after the ack must succeed, got %v", err)
	}
	// A duplicate ack is refused — the establishment is resolved.
	if err := pub.AcknowledgeEstablishment("L", h.Domain, h.Epoch, gen); !errors.Is(err, lifecyclepub.ErrNoPendingEstablishment) {
		t.Fatalf("duplicate ack = %v, want ErrNoPendingEstablishment", err)
	}
}

// TestPublisherEstablishmentTimeoutRollsBack is fault variant 3: the
// publication path is stalled — an ack that never returns. The accept never
// reaches the transport, the domain is rolled back (decision 9), and the
// resulting safe state (native) is published. There is no window in which a
// suppressed prompt could exist without an editor: the accept simply never
// went out.
func TestPublisherEstablishmentTimeoutRollsBack(t *testing.T) {
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k, lifecyclepub.WithEstablishmentTimeout(40*time.Millisecond))
	r := &recorder{}
	pub.SetEmitter(r)
	port := &recordingPort{}
	_ = pub.BindTransport("T", port)
	h, _ := pub.RequestDomain("L", nil, "T")
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))
	if got := port.kinds(); len(got) != 0 {
		t.Fatalf("accept flushed without an ack: %v", got)
	}
	// The establishment bound expires: the domain is revoked, the lane
	// falls to native, and the accept never goes out.
	testwait.WaitForDetail(t, "establishment timeout to close the domain",
		func() string {
			d, ok := pub.Domain(h.Domain)
			return fmt.Sprintf("the timeout never rolled the domain back; known=%t state=%v", ok, d.State)
		},
		func() bool {
			d, ok := pub.Domain(h.Domain)
			return ok && d.State == lifecycle.DomainClosed
		})
	if got := port.kinds(); len(got) != 0 {
		t.Fatalf("accept flushed after the rollback: %v", got)
	}
	// The rollback's native fact follows the Closed state in the same
	// timer tick — wait for it, then assert it is the final word.
	var last lifecyclepub.Fact
	testwait.WaitForDetail(t, "rollback to publish the native state",
		func() string {
			facts := r.all()
			if len(facts) == 0 {
				return "the rollback published no fact at all"
			}
			return fmt.Sprintf("last fact = %+v", facts[len(facts)-1])
		},
		func() bool {
			facts := r.all()
			if len(facts) == 0 {
				return false
			}
			last = facts[len(facts)-1]
			return last.Lifecycle == lifecyclepub.LifecycleNative
		})
	if last.Domain != "" {
		t.Fatalf("final fact = %+v, want native with no domain", last)
	}
}

// TestPublisherReconnectHelloMintsFreshGeneration proves the reconnect
// decision: every accept-producing hello is a fresh establishment episode
// with a fresh generation and a replayed fact, so the shell's reconnect
// accept can never be released by an old connection's (old generation)
// acknowledgement — and the replay is what carries the fresh generation to
// the renderer, so there is no deadlock (the brief's permitted path).
func TestPublisherReconnectHelloMintsFreshGeneration(t *testing.T) {
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k)
	r := &recorder{}
	pub.SetEmitter(r)
	port := &recordingPort{}
	_ = pub.BindTransport("T", port)
	h, _ := pub.RequestDomain("L", nil, "T")
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))
	gen1 := r.all()[0].Generation
	mustAckEstablishment(t, pub, r, "L", h)
	if got := port.kinds(); len(got) != 1 {
		t.Fatalf("initial accept not flushed, got %v", got)
	}

	// The shell reconnects within the epoch: a fresh accept is minted with
	// a fresh generation, and the unchanged projection is replayed carrying
	// it (the dedupe lets the fresh generation through).
	mustIngest(t, pub, "T", env("L", h, 2, helloEvt()))
	facts := r.all()
	replay := facts[len(facts)-1]
	if replay.Generation == "" || replay.Generation == gen1 {
		t.Fatalf("reconnect must publish a fact with a FRESH generation, got %q (was %q)", replay.Generation, gen1)
	}
	// The old generation can no longer release anything.
	if err := pub.AcknowledgeEstablishment("L", h.Domain, h.Epoch, gen1); !errors.Is(err, lifecyclepub.ErrEstablishmentGeneration) {
		t.Fatalf("old-generation ack = %v, want ErrEstablishmentGeneration", err)
	}
	if got := port.kinds(); len(got) != 1 {
		t.Fatalf("old ack flushed a second accept: %v", got)
	}
	// The fresh generation's ack flushes the reconnect accept.
	if err := pub.AcknowledgeEstablishment("L", h.Domain, h.Epoch, replay.Generation); err != nil {
		t.Fatalf("reconnect ack: %v", err)
	}
	if got := port.kinds(); len(got) != 2 {
		t.Fatalf("after reconnect ack: %v, want two accepts", got)
	}
}

// TestPublisherTransportLostCancelsPendingAccept proves a pending accept can
// never outlive its transport: loss cancels it, and a late acknowledgement
// is refused.
func TestPublisherTransportLostCancelsPendingAccept(t *testing.T) {
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k)
	r := &recorder{}
	pub.SetEmitter(r)
	port := &recordingPort{}
	_ = pub.BindTransport("T", port)
	h, _ := pub.RequestDomain("L", nil, "T")
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))
	gen := r.all()[0].Generation
	if err := pub.TransportLost("T"); err != nil {
		t.Fatalf("TransportLost: %v", err)
	}
	if err := pub.AcknowledgeEstablishment("L", h.Domain, h.Epoch, gen); !errors.Is(err, lifecyclepub.ErrNoPendingEstablishment) {
		t.Fatalf("ack after loss = %v, want ErrNoPendingEstablishment", err)
	}
	if got := port.kinds(); len(got) != 0 {
		t.Fatalf("accept flushed after transport loss: %v", got)
	}
}
