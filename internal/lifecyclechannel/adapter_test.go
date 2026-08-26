package lifecyclechannel

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/testwait"
)

// seqRand yields 1, 2, 3, … (wrapping to 1 after 255) — never a zero byte,
// so capabilities minted from it are never all-zero and every domain gets a
// distinct capability.
type seqRand struct{ b byte }

func (r *seqRand) Read(p []byte) (int, error) {
	for i := range p {
		r.b++
		p[i] = r.b
	}
	return len(p), nil
}

// newTestKernel builds the adapter's kernel seam the way the composition root
// does: the PUBLISHER wrapping the raw kernel, with an emitter that
// acknowledges every establishment synchronously — the renderer applying the
// published fact instantly (decision 9). The adapter drives the publisher;
// the raw kernel no longer satisfies the adapter seam because it returns
// outbound unsent.
func newTestKernel() *lifecyclepub.Publisher {
	k := lifecycle.New(lifecycle.Options{Rand: &seqRand{}})
	pub := lifecyclepub.New(k)
	pub.SetEmitter(ackingEmitter{pub: pub})
	return pub
}

// ackingEmitter acknowledges every published establishment fact immediately,
// as a renderer that commits the editor presentation on receipt would. The
// accept then flushes through the publisher (decision 9).
type ackingEmitter struct {
	pub *lifecyclepub.Publisher
}

func (e ackingEmitter) PublishLifecycle(f lifecyclepub.Fact) {
	if f.Generation == "" || f.Domain == "" {
		return
	}
	_ = e.pub.AcknowledgeEstablishment(
		lifecycle.LaneID(f.Lane), lifecycle.DomainID(f.Domain), f.Epoch, f.Generation)
}

// shellEnv builds an authenticated envelope for the adapter's minted domain.
func shellEnv(a *Adapter, seq uint64, evt lifecycle.Event) lifecycle.Envelope {
	return lifecycle.Envelope{
		Version:    lifecycle.ProtocolVersion,
		Lane:       a.lane,
		Domain:     a.domain,
		Epoch:      a.epoch,
		Sequence:   seq,
		Capability: a.capability,
		Event:      evt,
	}
}

// mustEstablish runs the full handshake over the wire and returns a decoder
// for the shell end (the outbound side of the port).
func mustEstablish(t *testing.T, a *Adapter, child *os.File) *lifecyclecodec.Decoder {
	t.Helper()
	sh := lifecyclecodec.NewDecoder(child, lifecyclecodec.Config{}, nil)

	_, err := lifecyclecodec.Encode(child, shellEnv(a, 1, lifecycle.Event{
		Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"},
	}))
	if err != nil {
		t.Fatalf("write hello: %v", err)
	}
	testwait.WaitFor(t, "domain established", func() bool {
		d, ok := a.kernel.Domain(a.domain)
		return ok && d.State == lifecycle.DomainEstablished
	})
	accept, err := sh.ReadFrame()
	if err != nil {
		t.Fatalf("read accept: %v", err)
	}
	if accept.Event.Kind != lifecycle.KindAccept {
		t.Fatalf("want accept, got %s", accept.Event.Kind)
	}
	if accept.Lane != a.lane || accept.Domain != a.domain || accept.Epoch != a.epoch {
		t.Fatalf("accept addressing mismatch: %+v", accept)
	}
	if accept.Capability != a.capability {
		t.Fatal("accept does not carry the domain capability")
	}
	return sh
}

// TestHandshakeThroughTheWire proves the full establishment path: the shell
// end writes a hello, the kernel answers accept, and the accept comes back
// over the descriptor with the domain's addressing and capability.
func TestHandshakeThroughTheWire(t *testing.T) {
	k := newTestKernel()
	a, child, err := New(log.NewSlogAdapter(nil), k)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()
	defer func() { _ = child.Close() }()

	mustEstablish(t, a, child)

	ls, err := k.State(a.lane)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if ls.Lifecycle != lifecycle.LifecyclePromptReady {
		t.Fatalf("want PromptReady after handshake, got %v", ls.Lifecycle)
	}
}

// TestGarbageBeforeHelloStillEstablishes proves garbage during the handshake
// (before the domain is live) is scanned past without breaking the hello:
// NotifyGap on a Pending domain is a no-op and the following hello still
// completes the handshake.
func TestGarbageBeforeHelloStillEstablishes(t *testing.T) {
	k := newTestKernel()
	a, child, err := New(log.NewSlogAdapter(nil), k)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()
	defer func() { _ = child.Close() }()

	_, _ = child.Write([]byte("garbage before any frame"))
	_, err = lifecyclecodec.Encode(child, shellEnv(a, 1, lifecycle.Event{
		Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"},
	}))
	if err != nil {
		t.Fatalf("write hello: %v", err)
	}
	testwait.WaitFor(t, "domain established past garbage", func() bool {
		d, ok := k.Domain(a.domain)
		return ok && d.State == lifecycle.DomainEstablished
	})
}

// TestGarbageDesyncsAndSnapshotRestores proves the desync round trip over
// the wire: garbage desynchronizes the established domain and emits
// refresh_request; an authenticated envelope found by scanning is still
// delivered (the kernel quarantines it, mutating nothing); and only a
// snapshot answering the refresh request restores authority.
func TestGarbageDesyncsAndSnapshotRestores(t *testing.T) {
	k := newTestKernel()
	a, child, err := New(log.NewSlogAdapter(nil), k)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()
	defer func() { _ = child.Close() }()
	sh := mustEstablish(t, a, child)

	// Garbage, then an authenticated prompt_ready. The scanner finds the
	// prompt_ready and delivers it; the domain is Desynchronized by then, so
	// the kernel quarantines the event (rejects it, mutating nothing).
	_, _ = child.Write([]byte("corruption"))
	_, err = lifecyclecodec.Encode(child, shellEnv(a, 2, lifecycle.Event{
		Kind: lifecycle.KindPromptReady, PromptReady: &lifecycle.PromptReady{},
	}))
	if err != nil {
		t.Fatalf("write prompt_ready: %v", err)
	}

	testwait.WaitFor(t, "domain desynchronized", func() bool {
		d, ok := k.Domain(a.domain)
		return ok && d.State == lifecycle.DomainDesynchronized
	})
	ls, _ := k.State(a.lane)
	if ls.Lifecycle != lifecycle.LifecycleDesynchronized {
		t.Fatalf("want Desynchronized lifecycle, got %v", ls.Lifecycle)
	}

	// The kernel's refresh_request arrives on the shell end.
	refresh, err := sh.ReadFrame()
	if err != nil {
		t.Fatalf("read refresh_request: %v", err)
	}
	if refresh.Event.Kind != lifecycle.KindRefreshRequest {
		t.Fatalf("want refresh_request, got %s", refresh.Event.Kind)
	}
	rid := refresh.Event.RefreshRequest.RequestID

	// Only the snapshot answering it restores authority.
	snap := shellEnv(a, 3, lifecycle.Event{
		Kind: lifecycle.KindSnapshot,
		Snapshot: &lifecycle.Snapshot{
			RequestID:    rid,
			ShellState:   lifecycle.ShellAtPrompt,
			NextSequence: 4,
		},
	})
	_, err = lifecyclecodec.Encode(child, snap)
	if err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	testwait.WaitFor(t, "domain restored", func() bool {
		d, ok := k.Domain(a.domain)
		return ok && d.State == lifecycle.DomainEstablished
	})
	ls, _ = k.State(a.lane)
	if ls.Lifecycle != lifecycle.LifecyclePromptReady {
		t.Fatalf("want PromptReady after snapshot, got %v", ls.Lifecycle)
	}
}

// TestTransportLossMarksDomainLost proves an abrupt close of the shell end
// reports transport loss: the domain is Lost and the lane falls to Lost.
func TestTransportLossMarksDomainLost(t *testing.T) {
	k := newTestKernel()
	a, child, err := New(log.NewSlogAdapter(nil), k)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()
	mustEstablish(t, a, child)

	_ = child.Close() // the shell end dies without saying goodbye

	testwait.WaitFor(t, "domain lost", func() bool {
		d, ok := k.Domain(a.domain)
		return ok && d.State == lifecycle.DomainLost
	})
	ls, _ := k.State(a.lane)
	if ls.Lifecycle != lifecycle.LifecycleLost {
		t.Fatalf("want Lost lifecycle, got %v", ls.Lifecycle)
	}
}

// TestShellExitClosesDomain proves a clean exit: the shell sends
// domain_closed before closing its end, and the domain ends Closed — the
// kernel is not told the transport was lost, so the clean close is not
// relabeled.
func TestShellExitClosesDomain(t *testing.T) {
	k := newTestKernel()
	a, child, err := New(log.NewSlogAdapter(nil), k)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()
	mustEstablish(t, a, child)

	_, err = lifecyclecodec.Encode(child, shellEnv(a, 2, lifecycle.Event{
		Kind: lifecycle.KindDomainClosed, DomainClosed: &lifecycle.DomainClosedEvent{},
	}))
	if err != nil {
		t.Fatalf("write domain_closed: %v", err)
	}
	testwait.WaitFor(t, "domain closed", func() bool {
		d, ok := k.Domain(a.domain)
		return ok && d.State == lifecycle.DomainClosed
	})
	_ = child.Close() // the shell's end closes after the event

	testwait.WaitFor(t, "pump to observe EOF", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.closed
	})
	d, ok := k.Domain(a.domain)
	if !ok || d.State != lifecycle.DomainClosed {
		t.Fatalf("want the clean close preserved (Closed), got %+v", d)
	}
}

// TestShellDiesWithoutDomainClosed proves the loss path for a shell that
// dies without saying goodbye: the domain is Lost and its open attempt
// becomes unknown (never successful, never assigned an exit code).
func TestShellDiesWithoutDomainClosed(t *testing.T) {
	k := newTestKernel()
	a, child, err := New(log.NewSlogAdapter(nil), k)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()
	mustEstablish(t, a, child)

	seq := uint64(2)
	for _, evt := range []lifecycle.Event{
		{Kind: lifecycle.KindPromptReady, PromptReady: &lifecycle.PromptReady{}},
		{Kind: lifecycle.KindStart, Start: &lifecycle.Start{Command: "sleep 100"}},
	} {
		_, err = lifecyclecodec.Encode(child, shellEnv(a, seq, evt))
		if err != nil {
			t.Fatalf("write event %d: %v", seq, err)
		}
		seq++
	}
	testwait.WaitFor(t, "attempt open", func() bool {
		_, ok := k.OpenAttempt(a.domain)
		return ok
	})
	_ = child.Close()

	testwait.WaitFor(t, "domain lost", func() bool {
		d, ok := k.Domain(a.domain)
		return ok && d.State == lifecycle.DomainLost
	})
	att, ok := k.OpenAttempt(a.domain)
	if ok {
		t.Fatalf("open attempt survived loss: %+v", att)
	}
}

// TestHelloTimeoutAbandonsDomain proves the handshake bound (protocol §5):
// without an authenticated hello the domain is abandoned within the window.
func TestHelloTimeoutAbandonsDomain(t *testing.T) {
	k := newTestKernel()
	a, child, err := New(log.NewSlogAdapter(nil), k, WithHelloTimeout(50*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()
	defer func() { _ = child.Close() }()

	testwait.WaitFor(t, "domain abandoned on hello timeout", func() bool {
		d, ok := k.Domain(a.domain)
		return ok && d.State == lifecycle.DomainLost
	})
}

// TestOversizeHelloThenValidHello proves the 1 KiB hello bound: an oversize
// hello is scanned past as garbage and a following hello still establishes
// the domain.
func TestOversizeHelloThenValidHello(t *testing.T) {
	k := newTestKernel()
	a, child, err := New(log.NewSlogAdapter(nil), k)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()
	defer func() { _ = child.Close() }()

	_, err = lifecyclecodec.Encode(child, shellEnv(a, 1, lifecycle.Event{
		Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: strings.Repeat("x", 2048)},
	}))
	if err != nil {
		t.Fatalf("write oversize hello: %v", err)
	}
	_, err = lifecyclecodec.Encode(child, shellEnv(a, 2, lifecycle.Event{
		Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"},
	}))
	if err != nil {
		t.Fatalf("write valid hello: %v", err)
	}
	testwait.WaitFor(t, "domain established past oversize hello", func() bool {
		d, ok := k.Domain(a.domain)
		return ok && d.State == lifecycle.DomainEstablished
	})
}

// TestCloseEndsSession proves the session-end disposal path: closing the
// adapter loses the domain so its open attempts become unknown.
func TestCloseEndsSession(t *testing.T) {
	k := newTestKernel()
	a, child, err := New(log.NewSlogAdapter(nil), k)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = child.Close() }()
	mustEstablish(t, a, child)

	_ = a.Close()
	testwait.WaitFor(t, "domain lost on close", func() bool {
		d, ok := k.Domain(a.domain)
		return ok && d.State == lifecycle.DomainLost
	})
}

// TestChildDescriptorReachesSpawnedProcess proves the deliverable's core
// mechanism: the child end of the socketpair, handed to a real spawned
// process as fd 3 (exec.Cmd.ExtraFiles), lets that process speak to the
// kernel. The child cats a prebuilt hello frame to fd 3 and the domain
// establishes — end to end, through a process that is not the test.
func TestChildDescriptorReachesSpawnedProcess(t *testing.T) {
	k := newTestKernel()
	a, child, err := New(log.NewSlogAdapter(nil), k)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()

	var frame bytes.Buffer
	_, err = lifecyclecodec.Encode(&frame, shellEnv(a, 1, lifecycle.Event{
		Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "spawned"},
	}))
	if err != nil {
		t.Fatalf("build hello frame: %v", err)
	}
	tmp, err := os.CreateTemp(t.TempDir(), "hello-frame-*")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	if _, err := tmp.Write(frame.Bytes()); err != nil {
		t.Fatalf("write frame file: %v", err)
	}
	_ = tmp.Close()

	// The first ExtraFile becomes fd 3 in the child; the frame reaches the
	// kernel through the inherited descriptor.
	cmd := exec.Command("sh", "-c", "cat \"$1\" >&3", "sh", tmp.Name()) // #nosec G204 -- the command is a fixed test fixture
	cmd.ExtraFiles = []*os.File{child}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("spawned hello writer: %v (%s)", err, out)
	}

	testwait.WaitFor(t, "domain established by the spawned process", func() bool {
		d, ok := k.Domain(a.domain)
		return ok && d.State == lifecycle.DomainEstablished
	})
	// CLOSED AFTER THE ASSERTION, and that ordering is the test.
	//
	// It used to close here — before the wait, "so the parent's copy does not
	// keep the socket open" — and that lost the frame it had just sent. The
	// hello establishes the domain, the test kernel's acking emitter answers
	// synchronously, and the accept goes back out through Adapter.Send into
	// this same socketpair. Nobody reads it: the spawned `cat` wrote and
	// exited. So the shell end held unread data, and closing a unix stream
	// socket in that state resets the connection — the adapter's reader got
	// ECONNRESET, logged "lifecycle transport read error", called lose() and
	// RETURNED. A reader that has returned can never establish anything, so
	// the wait was doomed from that instant rather than merely slow.
	//
	// Bimodal, which is what made it read as a flake: win the race and the
	// hello is decoded first and the test passes; lose it and no deadline is
	// long enough. Measured after the deadline was widened from a flat 2 s to
	// the test's own budget — the failure moved from 2.04 s to 594.81 s and
	// stayed a failure, which is what proved the clock was never the defect
	// (nocx-8b47).
	_ = child.Close()
}

// ── which path lost the channel (nocx-dvql) ───────────────────────────────

// lose is reached from four callers and used to record none of them, so a
// degraded session produced no diagnostic line anywhere — twenty-two seconds
// of one on the owner's machine produced zero. Each caller must be
// distinguishable, because "the shell never answered" and "the shell went
// away" need different fixes and the product renders different reasons for
// them.

// recordCauses collects the causes reported for an adapter's lane.
type causeRecorder struct {
	mu     chan struct{} // a 1-buffered channel as a mutex, so waitFor can read
	causes []LossCause
}

func newCauseRecorder() *causeRecorder {
	r := &causeRecorder{mu: make(chan struct{}, 1)}
	r.mu <- struct{}{}
	return r
}

func (r *causeRecorder) report(_ lifecycle.LaneID, c LossCause) {
	<-r.mu
	r.causes = append(r.causes, c)
	r.mu <- struct{}{}
}

func (r *causeRecorder) all() []LossCause {
	<-r.mu
	out := append([]LossCause(nil), r.causes...)
	r.mu <- struct{}{}
	return out
}

func TestHelloTimeoutReportsItsCause(t *testing.T) {
	k := newTestKernel()
	rec := newCauseRecorder()
	a, child, err := New(log.NewSlogAdapter(nil), k,
		WithHelloTimeout(50*time.Millisecond), WithLossReporter(rec.report))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()
	defer func() { _ = child.Close() }()

	testwait.WaitFor(t, "hello timeout reported as its own cause", func() bool {
		c := rec.all()
		return len(c) == 1 && c[0] == LossHelloTimeout
	})
	// Close afterwards must not append a second cause: the first cause is
	// the one that ended the channel, and a later disposal cannot rewrite
	// it into something the user would be told to fix.
	_ = a.Close()
	if c := rec.all(); len(c) != 1 || c[0] != LossHelloTimeout {
		t.Errorf("causes after a post-loss Close = %v, want exactly [hello-timeout]", c)
	}
}

func TestShellClosingItsEndReportsEndOfStream(t *testing.T) {
	k := newTestKernel()
	rec := newCauseRecorder()
	a, child, err := New(log.NewSlogAdapter(nil), k, WithLossReporter(rec.report))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()
	mustEstablish(t, a, child)

	// The shell goes away without saying goodbye: the domain is live, so
	// this is a loss and not a clean end.
	_ = child.Close()
	testwait.WaitFor(t, "end of stream reported as its own cause", func() bool {
		c := rec.all()
		return len(c) == 1 && c[0] == LossEndOfStream
	})
}

func TestSessionDisposalReportsClosed(t *testing.T) {
	k := newTestKernel()
	rec := newCauseRecorder()
	a, child, err := New(log.NewSlogAdapter(nil), k, WithLossReporter(rec.report))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = child.Close() }()
	mustEstablish(t, a, child)

	// The session is closing. Distinguishable from every failure above, so
	// the product does not paint a tab as broken on its way out.
	_ = a.Close()
	testwait.WaitFor(t, "disposal reported as its own cause", func() bool {
		c := rec.all()
		return len(c) == 1 && c[0] == LossClosed
	})
}

// The reporter runs BEFORE the kernel is told, so a consumer that watches
// both never sees the published consequence before the cause that explains
// it. Asserted rather than assumed: the ordering is the whole reason the two
// seams can be combined at all.
func TestCauseIsReportedBeforeTheKernelIsTold(t *testing.T) {
	k := newTestKernel()
	order := make(chan string, 4)
	a, child, err := New(log.NewSlogAdapter(nil), &orderingKernel{Kernel: k, order: order},
		WithHelloTimeout(50*time.Millisecond),
		WithLossReporter(func(lifecycle.LaneID, LossCause) { order <- "cause" }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()
	defer func() { _ = child.Close() }()

	if first := <-order; first != "cause" {
		t.Errorf("first observation = %q, want the cause before the kernel's TransportLost", first)
	}
	if second := <-order; second != "transport-lost" {
		t.Errorf("second observation = %q, want transport-lost", second)
	}
}

// orderingKernel records when TransportLost was called, delegating
// everything else.
type orderingKernel struct {
	Kernel
	order chan string
}

func (k *orderingKernel) TransportLost(t lifecycle.TransportID) error {
	k.order <- "transport-lost"
	return k.Kernel.TransportLost(t)
}
