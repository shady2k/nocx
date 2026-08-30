package lifecyclechannel

import (
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/waittest"
)

type listenerFixture struct {
	listener *Listener
	kernel   *lifecyclepub.Publisher
	handle   lifecycle.DomainHandle
	lane     lifecycle.LaneID
	recorder *causeRecorder
}

func newListenerFixture(t *testing.T, timeout time.Duration) *listenerFixture {
	t.Helper()
	k := newTestKernel()
	recorder := newCauseRecorder()
	listener, err := NewListener(log.NewSlogAdapter(nil), k,
		WithHelloTimeout(timeout), WithLossReporter(recorder.report))
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	lane := lifecycle.LaneID("lane-listener-expectation")
	handle, err := k.RequestDomain(lane, nil, listener.TransportID())
	if err != nil {
		_ = listener.Close()
		t.Fatalf("RequestDomain: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return &listenerFixture{listener: listener, kernel: k, handle: handle, lane: lane, recorder: recorder}
}

func getListenerExpectation(t *testing.T, listener *Listener, domain lifecycle.DomainID) *listenerExpectation {
	t.Helper()
	listener.mu.Lock()
	defer listener.mu.Unlock()
	expectation := listener.expectations[domain]
	if expectation == nil {
		t.Fatalf("no expectation for domain %s", domain)
	}
	return expectation
}

func sendListenerHello(t *testing.T, f *listenerFixture) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(f.listener.Port())))
	if err != nil {
		t.Fatalf("dial listener: %v", err)
	}
	env := lifecycle.Envelope{
		Version:    lifecycle.ProtocolVersion,
		Lane:       f.lane,
		Domain:     f.handle.Domain,
		Epoch:      f.handle.Epoch,
		Sequence:   1,
		Capability: f.handle.Capability,
		Event:      lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "test"}},
	}
	if _, err := lifecyclecodec.Encode(conn, env); err != nil {
		_ = conn.Close()
		t.Fatalf("encode hello: %v", err)
	}
	return conn
}

func mustExpectListenerDomain(t *testing.T, f *listenerFixture) {
	t.Helper()
	if err := f.listener.ExpectDomain(f.lane, f.handle.Domain); err != nil {
		t.Fatalf("ExpectDomain: %v", err)
	}
}

func TestListenerExpectationArmsAtRegistration(t *testing.T) {
	f := newListenerFixture(t, time.Hour)
	mustExpectListenerDomain(t, f)
	expectation := getListenerExpectation(t, f.listener, f.handle.Domain)
	if expectation.timer == nil {
		t.Fatal("registration did not arm an expectation timer")
	}
	if !expectation.active {
		t.Fatal("new expectation is not active")
	}
}

func TestListenerHelloCancelsExpectationWithoutLoss(t *testing.T) {
	f := newListenerFixture(t, time.Second)
	mustExpectListenerDomain(t, f)
	conn := sendListenerHello(t, f)
	defer func() { _ = conn.Close() }()

	decoder := lifecyclecodec.NewDecoder(conn, lifecyclecodec.Config{}, nil)
	waittest.WaitFor(t, "listener domain established", func() bool {
		domain, ok := f.kernel.Domain(f.handle.Domain)
		return ok && domain.State == lifecycle.DomainEstablished
	})
	accept, err := decoder.ReadFrame()
	if err != nil {
		t.Fatalf("read accept: %v", err)
	}
	if accept.Event.Kind != lifecycle.KindAccept {
		t.Fatalf("got %s, want accept", accept.Event.Kind)
	}
	waittest.WaitFor(t, "expectation canceled after accepted hello", func() bool {
		f.listener.mu.Lock()
		defer f.listener.mu.Unlock()
		_, present := f.listener.expectations[f.handle.Domain]
		return !present
	})
	if causes := f.recorder.all(); len(causes) != 0 {
		t.Fatalf("accepted hello reported losses: %v", causes)
	}
}

func TestListenerExpectationTimeoutReportsHelloTimeoutOnce(t *testing.T) {
	f := newListenerFixture(t, time.Millisecond)
	mustExpectListenerDomain(t, f)

	waittest.WaitFor(t, "listener expectation timeout", func() bool {
		causes := f.recorder.all()
		return len(causes) == 1 && causes[0] == LossHelloTimeout
	})
	if causes := f.recorder.all(); len(causes) != 1 || causes[0] != LossHelloTimeout {
		t.Fatalf("timeout causes = %v, want exactly [%s]", causes, LossHelloTimeout)
	}
	if lanes := f.recorder.allLanes(); len(lanes) != 1 || lanes[0] != f.lane {
		t.Fatalf("timeout lanes = %v, want exactly [%s]", lanes, f.lane)
	}
}

func TestListenerLateHelloAfterExpectationTimeoutHasNoSecondLoss(t *testing.T) {
	f := newListenerFixture(t, 100*time.Millisecond)
	mustExpectListenerDomain(t, f)
	waittest.WaitFor(t, "listener expectation timeout", func() bool {
		return len(f.recorder.all()) == 1
	})

	conn := sendListenerHello(t, f)
	defer func() { _ = conn.Close() }()
	decoder := lifecyclecodec.NewDecoder(conn, lifecyclecodec.Config{}, nil)
	waittest.WaitFor(t, "late hello still establishes", func() bool {
		domain, ok := f.kernel.Domain(f.handle.Domain)
		return ok && domain.State == lifecycle.DomainEstablished
	})
	if _, err := decoder.ReadFrame(); err != nil {
		t.Fatalf("read late accept: %v", err)
	}
	if causes := f.recorder.all(); len(causes) != 1 || causes[0] != LossHelloTimeout {
		t.Fatalf("late hello causes = %v, want exactly [%s]", causes, LossHelloTimeout)
	}
}

func TestListenerCloseCancelsOutstandingExpectations(t *testing.T) {
	f := newListenerFixture(t, time.Hour)
	mustExpectListenerDomain(t, f)
	expectation := getListenerExpectation(t, f.listener, f.handle.Domain)
	if err := f.listener.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-expectation.done:
	default:
		t.Fatal("Close returned while expectation cleanup was still running")
	}
	if causes := f.recorder.all(); len(causes) != 0 {
		t.Fatalf("Close reported losses: %v", causes)
	}
}

func TestListenerExpectationRegistrationAndCancellationAreIdempotent(t *testing.T) {
	f := newListenerFixture(t, time.Hour)
	mustExpectListenerDomain(t, f)
	first := getListenerExpectation(t, f.listener, f.handle.Domain)
	if err := f.listener.ExpectDomain(f.lane, f.handle.Domain); err != nil {
		t.Fatalf("duplicate ExpectDomain: %v", err)
	}
	second := getListenerExpectation(t, f.listener, f.handle.Domain)
	if first != second {
		t.Fatal("duplicate registration replaced the active expectation")
	}
	f.listener.CancelExpectation(f.handle.Domain)
	f.listener.CancelExpectation(f.handle.Domain)
	select {
	case <-first.done:
	default:
		t.Fatal("cancel did not finish expectation cleanup")
	}
	if causes := f.recorder.all(); len(causes) != 0 {
		t.Fatalf("cancellation reported losses: %v", causes)
	}
}

func TestListenerExpectationRejectsEmptyLane(t *testing.T) {
	f := newListenerFixture(t, time.Millisecond)
	if err := f.listener.ExpectDomain("", f.handle.Domain); !errors.Is(err, ErrInvalidExpectation) {
		t.Fatalf("ExpectDomain empty lane error = %v, want %v", err, ErrInvalidExpectation)
	}
	f.listener.mu.Lock()
	_, present := f.listener.expectations[f.handle.Domain]
	f.listener.mu.Unlock()
	if present {
		t.Fatal("invalid expectation was registered")
	}
	if causes := f.recorder.all(); len(causes) != 0 {
		t.Fatalf("invalid expectation reported losses: %v", causes)
	}
}

func TestListenerExpectationOnClosedListenerReturnsErrClosed(t *testing.T) {
	f := newListenerFixture(t, time.Hour)
	if err := f.listener.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := f.listener.ExpectDomain(f.lane, f.handle.Domain); !errors.Is(err, ErrClosed) {
		t.Fatalf("ExpectDomain on closed listener error = %v, want %v", err, ErrClosed)
	}
}
