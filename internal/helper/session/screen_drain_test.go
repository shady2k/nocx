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
	effects []proto.EffectFrame
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

func (s *drainSink) SendSessionData(proto.SessionFrame) error         { return nil }
func (s *drainSink) SendLifecycleData(proto.SessionFrame) error       { return nil }
func (s *drainSink) SendOutputRows(proto.OutputRowsFrame) error       { return nil }
func (s *drainSink) SendIntervalEnd(proto.IntervalEndFrame) error     { return nil }
func (s *drainSink) SendClearBoundary(proto.ClearBoundaryFrame) error { return nil }
func (s *drainSink) SendNotification(proto.Notification) error        { return nil }

func (s *drainSink) SendEffectFrame(f proto.EffectFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.effects = append(s.effects, f)
	s.wake()
	return nil
}

func (s *drainSink) effectFrames() []proto.EffectFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]proto.EffectFrame(nil), s.effects...)
}

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
		// The wake-up channel is taken BEFORE the count, as every other sink
		// wait in this package does: a frame landing after the count closes
		// this channel, where one taken after it would be the replacement
		// nothing closes (nocx-2v80t.3.56).
		next := sink.waiter()
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
		case <-next:
		case <-time.After(hangLimit):
			t.Fatalf("only %d of %d whole screen frames arrived for the subscriber", len(sink.wholeScreenFrames(hs.raw)), revisions)
		}
	}
}

// TestASubscriberReceivesAnIdentityBearingEffect proves that the runtime's
// non-visual queue leaves the helper on its own carrier, not inside a screen
// snapshot. The producer identity survives the helper drain unchanged.
func TestASubscriberReceivesAnIdentityBearingEffect(t *testing.T) {
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
	if err := hs.runtime.Ingest([]byte("\x1b]777;notify;Tests failed;2 failed\x07")); err != nil {
		t.Fatalf("ingest notification: %v", err)
	}
	deadline := time.Now().Add(hangLimit)
	for {
		next := sink.waiter()
		effects := sink.effectFrames()
		if len(effects) > 0 {
			f := effects[0]
			if f.Session != hs.raw || f.Generation != 1 || f.EffectID != 1 || f.Kind != proto.EffectNotification || string(f.Title) != "Tests failed" || string(f.Body) != "2 failed" {
				t.Fatalf("effect frame identity/kind/title/body = %+v", f)
			}
			if f.Subscriber != mustSubscriberBytes(t, sub) {
				t.Fatalf("effect delivered to subscriber %x, want %s", f.Subscriber, sub)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("runtime effect never reached helper carrier")
		}
		select {
		case <-next:
		case <-time.After(hangLimit):
			t.Fatal("runtime effect never reached helper carrier")
		}
	}
}

type orderedOutputItem struct {
	kind   string
	data   []byte
	effect proto.EffectFrame
}

type delayedScreenSink struct {
	*drainSink
	items         chan orderedOutputItem
	screenEntered chan struct{}
	releaseScreen chan struct{}
	releaseOnce   sync.Once
}

func newDelayedScreenSink() *delayedScreenSink {
	return &delayedScreenSink{
		drainSink:     newDrainSink(),
		items:         make(chan orderedOutputItem, 8),
		screenEntered: make(chan struct{}, 1),
		releaseScreen: make(chan struct{}),
	}
}

func (s *delayedScreenSink) SendSessionData(frame proto.SessionFrame) error {
	s.items <- orderedOutputItem{kind: "data", data: append([]byte(nil), frame.Payload...)}
	return nil
}

func (s *delayedScreenSink) SendEffectFrame(frame proto.EffectFrame) error {
	s.items <- orderedOutputItem{kind: "effect", effect: frame}
	return nil
}

func (s *delayedScreenSink) SendScreenFrame(frame proto.ScreenDataFrame) error {
	select {
	case s.screenEntered <- struct{}{}:
	default:
	}
	<-s.releaseScreen
	return nil
}

func (s *delayedScreenSink) release() { s.releaseOnce.Do(func() { close(s.releaseScreen) }) }

func TestPromptBoundaryOrdersOutputWhileScreenEffectConsumerIsDelayed(t *testing.T) {
	proc := newScriptedProcess("")
	svc, hs := spawnScripted(t, proc, 0)
	// Seed a screen frame and its matching output bytes. The attach's
	// independent screen consumer will block on that frame, so it cannot
	// consume the prompt effect before the output pump reaches it.
	seed := []byte("screen seed\r\n")
	hs.owner.ingestOne(seed)
	sink := newDelayedScreenSink()
	defer sink.release()
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
	select {
	case <-sink.screenEntered:
	case <-time.After(hangLimit):
		t.Fatal("screen consumer did not reach its deliberate stall")
	}
	select {
	case seedFrame := <-sink.items:
		if seedFrame.kind != "data" || string(seedFrame.data) != string(seed) {
			t.Fatalf("seed output = %+v, want data %q", seedFrame, seed)
		}
	case <-time.After(hangLimit):
		t.Fatal("output pump did not send the seed before the prompt")
	}

	before := []byte("prompt-prefix\x1b]133;B\x07")
	after := []byte("immediate-live-suffix")
	hs.owner.ingestOne(append(append([]byte(nil), before...), after...))
	hs.win.mu.Lock()
	boundaryCount := len(hs.win.promptBoundaries)
	hs.win.mu.Unlock()
	if boundaryCount != 1 {
		t.Fatalf("synchronous output-window boundaries = %d, want 1", boundaryCount)
	}
	hs.win.mu.Lock()
	boundaryOffset := hs.win.promptBoundaries[0].effect.StreamOffset
	writtenOffset := hs.win.written
	hs.win.mu.Unlock()
	got := make([]orderedOutputItem, 3)
	for i := range got {
		select {
		case got[i] = <-sink.items:
		case <-time.After(hangLimit):
			t.Fatalf("ordered output stalled at item %d", i)
		}
	}
	if got[0].kind != "data" || string(got[0].data) != string(before) {
		t.Fatalf("prefix item = %+v, want data %q (boundary=%d, written=%d)", got[0], before, boundaryOffset, writtenOffset)
	}
	const wantBoundaryOffset = uint64(len("screen seed\r\nprompt-prefix\x1b]133;B\x07"))
	if got[1].kind != "effect" || got[1].effect.Kind != proto.EffectPromptBoundary || got[1].effect.StreamOffset != wantBoundaryOffset {
		t.Fatalf("middle item = %+v, want promptBoundary at %d", got[1], wantBoundaryOffset)
	}
	if got[2].kind != "data" || string(got[2].data) != string(after) {
		t.Fatalf("suffix item = %+v, want data %q", got[2], after)
	}
}

func TestPromptBoundaryCarriesExactOffsetThroughHelperCarrier(t *testing.T) {
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
	hs.owner.ingestOne([]byte("prompt\x1b]133;"))
	hs.owner.ingestOne([]byte("B\x07tail"))

	deadline := time.Now().Add(hangLimit)
	for {
		next := sink.waiter()
		if effects := sink.effectFrames(); len(effects) > 0 {
			got := effects[0]
			want := uint64(len("prompt\x1b]133;B\x07"))
			if got.Kind != proto.EffectPromptBoundary || got.StreamOffset != want {
				t.Fatalf("prompt boundary = %+v, want exclusive stream offset %d", got, want)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("prompt boundary never reached helper carrier")
		}
		select {
		case <-next:
		case <-time.After(hangLimit):
			t.Fatal("prompt boundary never reached helper carrier")
		}
	}
}

func mustSubscriberBytes(t *testing.T, id proto.SubscriberID) [16]byte {
	t.Helper()
	b, err := proto.SessionBytes(string(id))
	if err != nil {
		t.Fatalf("subscriber bytes: %v", err)
	}
	return b
}
