package lifecycleremote

import (
	"errors"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log"
)

// §6.2's loss events, and the teardown that leaves nothing running.
//
// Before this the remote adapter reported no cause at all, and the consequence
// was worse here than on the local path: a remote session whose shell never
// spoke established no domain, so the kernel published nothing, so the
// session's integration axis stayed at `starting` for the life of the tab.
// §7 says `starting` can never be permanent, and nothing enforced it here.

// recordingLoss captures every cause reported to the sink.
type recordingLoss struct {
	mu     sync.Mutex
	causes []LossCause
	lanes  []lifecycle.LaneID
}

func (r *recordingLoss) report(lane lifecycle.LaneID, c LossCause) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.causes = append(r.causes, c)
	r.lanes = append(r.lanes, lane)
}

func (r *recordingLoss) all() []LossCause {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]LossCause(nil), r.causes...)
}

// The disposal path names itself, and it names itself once. lose is reached
// from the hello timer, the accept loop, the Done watcher and Close, and a
// session that closes normally must not be reported as a failure — the
// transport's own rule is that the `closed` cause says nothing to the product.
func TestLossReporter_ClosePathNamesItselfExactlyOnce(t *testing.T) {
	rec := &recordingLoss{}
	tunnel := newFakeTunnel()
	a, _, err := New(log.NewSlogAdapter(nil), newTestKernel(), tunnel, WithLossReporter(rec.report))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if cerr := a.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	// Idempotent under repeated and concurrent callers: the refusal path,
	// the session's disposal and the connection dying all reach it.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = a.Close() }()
	}
	wg.Wait()

	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("causes = %v, want exactly one — lose is idempotent and the FIRST cause wins", got)
	}
	if got[0] != LossClosed {
		t.Errorf("cause = %q, want %q", got[0], LossClosed)
	}
}

// The underlying SSH transport dying is its own event, and §6.2 is explicit
// about it: losing the underlying transport ENDS THE SESSION. It must never be
// reported as the shell falling silent or as nocx's own channel going away —
// those are recoverable to a working prompt and this is not.
func TestLossReporter_TheUnderlyingTransportIsItsOwnEvent(t *testing.T) {
	rec := &recordingLoss{}
	tunnel := newFakeTunnel()
	a, cfg, err := New(log.NewSlogAdapter(nil), newTestKernel(), tunnel, WithLossReporter(rec.report))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	// The connection dies underneath the lease.
	_ = tunnel.Close()

	waitFor(t, "the transport loss is reported", func() bool { return len(rec.all()) > 0 })
	got := rec.all()
	if got[0] != LossTransportGone {
		t.Fatalf("cause = %q, want %q", got[0], LossTransportGone)
	}
	if rec.lanes[0] != cfg.Lane {
		t.Errorf("reported lane = %q, want the adapter's own %q", rec.lanes[0], cfg.Lane)
	}
	// And the teardown left nothing running: the listener is gone, so no
	// candidate can be served and nothing is left accepting on the far host.
	waitFor(t, "the listener is closed", func() bool {
		c, derr := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(cfg.Port))
		if derr == nil {
			_ = c.Close()
			return false
		}
		return true
	})
}

// The handshake bound is a different event again, and it is the one §6.2's
// second row is mostly about: the channel existed, the shell never proved
// itself, and the session degrades with a named reason rather than sitting in
// `starting`.
func TestLossReporter_TheHandshakeBoundNamesItself(t *testing.T) {
	rec := &recordingLoss{}
	tunnel := newFakeTunnel()
	a, _, err := New(log.NewSlogAdapter(nil), newTestKernel(), tunnel,
		WithHelloTimeout(time.Millisecond), WithLossReporter(rec.report))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	waitFor(t, "the handshake bound is reported", func() bool { return len(rec.all()) > 0 })
	if got := rec.all()[0]; got != LossHelloTimeout {
		t.Errorf("cause = %q, want %q", got, LossHelloTimeout)
	}
}

// Every path this package can take names a cause of its own, and no two share
// one. A path that reported nothing is how the remote session came to sit in
// `starting` forever, so "reported at all" is the assertion, and "distinctly"
// is what makes the report worth reading.
func TestLossReporter_EveryPathThisAdapterTakesIsNamed(t *testing.T) {
	for _, cause := range []LossCause{
		LossHelloTimeout, LossEndOfStream, LossReadError,
		LossListenerGone, LossTransportGone, LossClosed,
	} {
		rec := &recordingLoss{}
		a, _, err := New(log.NewSlogAdapter(nil), newTestKernel(), newFakeTunnel(),
			WithLossReporter(rec.report))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		a.LoseForTest(cause)
		got := rec.all()
		if len(got) != 1 || got[0] != cause {
			t.Errorf("driving %q reported %v", cause, got)
		}
		// A second, different cause never overwrites the first: it is the
		// one that actually ended the channel, and the rest are its
		// consequences.
		a.LoseForTest(LossReadError)
		if got := rec.all(); len(got) != 1 {
			t.Errorf("a later cause was reported after %q: %v", cause, got)
		}
	}
}

// A nil reporter is the default and must stay harmless: the adapter is used by
// tests and by any composition root that has not wired the sink.
func TestLossReporter_NilSinkIsHarmless(t *testing.T) {
	a, _, err := New(log.NewSlogAdapter(nil), newTestKernel(), newFakeTunnel())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if cerr := a.Close(); cerr != nil {
		t.Fatalf("Close with no reporter: %v", cerr)
	}
	if serr := a.Send(lifecycle.Envelope{}); !errors.Is(serr, ErrClosed) {
		t.Fatalf("Send after close = %v, want ErrClosed", serr)
	}
}
