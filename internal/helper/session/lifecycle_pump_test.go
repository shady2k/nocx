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
	go func() { _, _ = input.Write([]byte(hello)) }()
	awaitSink(t, sink, "the lifecycle frame", func() bool {
		return string(lifecycleBytes(sink)) == hello
	})

	out := logged.String()
	sessionHex := entry.Session.Session
	for _, want := range []string{
		"lifecycle: the shell's first bytes went to the coordinator",
		"bytes=17",
		"session=" + sessionHex,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the helper's pump does not say %q:\n%s", want, out)
		}
	}
}

type safeLogSink struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (s *safeLogSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeLogSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
