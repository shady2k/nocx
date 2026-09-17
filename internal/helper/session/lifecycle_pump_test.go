package session_test

// The helper's half of the three-hop road the shell's hello travels
// (nocx-n14oo.7).

import (
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

// THE FIRST OF THREE HOPS SAYS IT MOVED SOMETHING (nocx-n14oo.7).
//
// The shell's hello reaches the coordinator's adapter across the helper's
// subscriber pump, the wire, and the coordinator's bridge. On 2026-09-10 the
// helper could say the shell had written 219 bytes and nothing anywhere could
// say whether they were ever sent on, so a loss could be attributed to none of
// the three. This is the first hop clearing itself.
func TestTheHelperSaysWhenTheShellsLifecycleBytesGoOutToTheCoordinator(t *testing.T) {
	var logged safeLogSink
	stream, input := io.Pipe()
	proc := &lifecycleProcess{
		fakeProcess: newFakeProcess(),
		carrier:     &lifecycleCarrier{stream: stream, input: input},
	}
	svc := session.New(session.Options{
		Generation: "gen-under-test",
		Spawner:    &lifecycleSpawner{proc: proc},
		Log:        slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	sink := newSink()
	release := bindTo(svc, sink)
	t.Cleanup(func() {
		release()
		svc.Close()
	})

	entry := call[proto.SpawnResult](t, svc, proto.OpSpawn, proto.SpawnParams{
		Cols: 80, Rows: 24,
		Lifecycle: &proto.LifecycleLaunch{
			Lane: "lane-1", Domain: "dom-1", Epoch: 7,
			Capability: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
	}).Entry
	call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{
		Subscriber: proto.SubscriberID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		Session:    entry.Session, Fresh: true,
	})

	const hello = "the shell's hello"
	sessionHex := entry.Session.Session
	wantLines := []string{
		"lifecycle: the shell's first bytes went to the coordinator",
		"bytes=17",
		"session=" + sessionHex,
	}

	go func() { _, _ = input.Write([]byte(hello)) }()

	// The frame reaching the sink and the pump's log line are two
	// independent effects of the same send: SendLifecycleData returns to
	// the pump goroutine, which THEN calls log.Info. Waiting on the frame
	// alone (nocx-c8am9) raced the test goroutine against that second
	// step, so the wait condition below checks both, waking on a delivery
	// to either the sink or the log rather than on a clock.
	awaitLifecycleLineLogged(t, sink, &logged, "the lifecycle frame and its log line", func() bool {
		if string(lifecycleBytes(sink)) != hello {
			return false
		}
		out := logged.String()
		for _, want := range wantLines {
			if !strings.Contains(out, want) {
				return false
			}
		}
		return true
	})

	out := logged.String()
	for _, want := range wantLines {
		if !strings.Contains(out, want) {
			t.Fatalf("the helper's pump does not say %q:\n%s", want, out)
		}
	}
}

// awaitLifecycleLineLogged blocks until want() is true, waking on every
// delivery to the sink or every write to the log sink — never on a clock —
// mirroring awaitSink's generation-channel idiom across the two independent
// sources the condition depends on.
func awaitLifecycleLineLogged(t *testing.T, s *recordingSink, l *safeLogSink, what string, want func() bool) {
	t.Helper()
	for {
		// Take both wakeups BEFORE testing the condition, or a delivery
		// landing between the test and the park is lost.
		nextFrame := s.waiter()
		nextLog := l.waiter()
		if want() {
			return
		}
		select {
		case <-nextFrame:
		case <-nextLog:
		case <-t.Context().Done():
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

type safeLogSink struct {
	mu      sync.Mutex
	buf     strings.Builder
	arrived chan struct{}
}

// wake must be called with s.mu held. It is a generation channel — closed
// and replaced on every write — so a waiter can never miss a wakeup because
// a buffer was full, the same idiom recordingSink uses for the sink side.
func (s *safeLogSink) wake() {
	if s.arrived == nil {
		s.arrived = make(chan struct{})
	}
	close(s.arrived)
	s.arrived = make(chan struct{})
}

func (s *safeLogSink) waiter() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.arrived == nil {
		s.arrived = make(chan struct{})
	}
	return s.arrived
}

func (s *safeLogSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.buf.Write(p)
	s.wake()
	return n, err
}

func (s *safeLogSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
