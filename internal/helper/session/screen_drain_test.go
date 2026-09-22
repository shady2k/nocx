package session

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// drainSink is the screen drain test's own recording sink: internal-package,
// because the assertion reaches hs.runtime — the object the drain drains —
// and records only the screen plane, which is all this test judges.
type drainSink struct {
	mu      sync.Mutex
	frames  []proto.ScreenDataFrame
	arrived chan struct{}
}

func newDrainSink() *drainSink {
	return &drainSink{arrived: make(chan struct{})}
}

func (s *drainSink) wake() {
	close(s.arrived)
	s.arrived = make(chan struct{})
}

func (s *drainSink) waiter() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.arrived
}

func (s *drainSink) SendSessionData(proto.SessionFrame) error   { return nil }
func (s *drainSink) SendLifecycleData(proto.SessionFrame) error { return nil }
func (s *drainSink) SendNotification(proto.Notification) error  { return nil }

func (s *drainSink) SendScreenFrame(f proto.ScreenDataFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = append(s.frames, f)
	s.wake()
	return nil
}

// wholeScreenFrames answers the unsplit screen frames one session's drain
// delivered, in arrival order.
func (s *drainSink) wholeScreenFrames(session [16]byte) []proto.ScreenDataFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []proto.ScreenDataFrame
	for _, f := range s.frames {
		if f.Session == session && f.Whole() {
			out = append(out, f)
		}
	}
	return out
}

// The screen drain's acceptance, at the helper: a subscriber that reads
// receives every revision the session's screen publishes. The drain parks on
// the runtime's Ready and Takes what it is owed — and Take refunds the
// allowance, which is why a flood far past MaxPendingFrames arrives whole
// instead of shedding: a reader that keeps up is never capped by what it has
// already taken away.
func TestASubscriberThatReadsReceivesEveryRevisionTheScreenPublishes(t *testing.T) {
	sink := newDrainSink()
	proc := newScriptedProcess("")
	svc, hs := spawnScripted(t, proc, 0)
	release := svc.Bind(sink)
	t.Cleanup(release)

	sub := proto.SubscriberID("0123456789abcdef0123456789abcdef")
	params, err := json.Marshal(proto.AttachParams{Session: hs.id, Subscriber: sub})
	if err != nil {
		t.Fatalf("attach params: %v", err)
	}
	if _, err := svc.Call(host.WithConnection(context.Background(), sink), proto.OpAttach, params); err != nil {
		t.Fatalf("attach: %v", err)
	}

	// Forty revisions through the runtime's own port — far past
	// MaxPendingFrames, every one of them owed to a reader that keeps up.
	const revisions = 40
	for i := 0; i < revisions; i++ {
		if err := hs.runtime.Ingest([]byte("revision line\r\n")); err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
	}

	deadline := time.Now().Add(hangLimit)
	for {
		got := sink.wholeScreenFrames(hs.raw)
		if len(got) >= revisions {
			for i := range got[:revisions] {
				if i > 0 && got[i].Revision <= got[i-1].Revision {
					t.Fatalf("screen frame %d carries revision %d after %d: the drain must deliver revisions in order", i, got[i].Revision, got[i-1].Revision)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d whole screen frames arrived for the subscriber", len(got), revisions)
		}
		select {
		case <-sink.waiter():
		case <-time.After(hangLimit):
			t.Fatalf("only %d of %d whole screen frames arrived for the subscriber", len(sink.wholeScreenFrames(hs.raw)), revisions)
		}
	}
}
