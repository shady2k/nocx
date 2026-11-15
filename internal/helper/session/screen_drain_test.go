package session

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
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

// A reader keeps up by receiving each bounded batch before the next is
// published. Publishing all forty first races the drain's scheduler and can
// legitimately exhaust MaxPendingFrames; that is the slow-reader case below.
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

	const revisions = 40
	const batch = sessionruntime.MaxPendingFrames / 2
	base := uint64(hs.runtime.Revision())
	want := make([]uint64, 0, revisions)
	for i := uint64(1); i <= revisions; i++ {
		revision := base + i
		ingestScreenRevision(t, hs, revision)
		want = append(want, revision)
		if i%batch == 0 || i == revisions {
			assertScreenRevisions(t, sink.waitForScreenFrames(t, hs.raw, len(want)), sub, want)
		}
	}
	t.Logf("reading subscriber received all %d revisions in order: %v", revisions, want)
}

func ingestScreenRevision(t *testing.T, hs *hostSession, revision uint64) {
	t.Helper()
	if err := hs.runtime.Ingest([]byte(fmt.Sprintf("\x1b[Hrevision %03d", revision))); err != nil {
		t.Fatalf("ingest revision %d: %v", revision, err)
	}
	if got := uint64(hs.runtime.Revision()); got != revision {
		t.Fatalf("ingest published revision %d, want %d", got, revision)
	}
}

func (s *drainSink) waitForScreenFrames(t *testing.T, session [16]byte, count int) []proto.ScreenDataFrame {
	t.Helper()
	timer := time.NewTimer(hangLimit)
	defer timer.Stop()
	for {
		// Take the waiter BEFORE the count: a delivery after the count must
		// close this channel, not its replacement (nocx-2v80t.3.56).
		next := s.waiter()
		got := s.wholeScreenFrames(session)
		if len(got) >= count {
			return got
		}
		select {
		case <-next:
		case <-timer.C:
			t.Fatalf("only %d of %d whole screen frames arrived: %+v", len(got), count, got)
		}
	}
}

func assertScreenRevisions(t *testing.T, got []proto.ScreenDataFrame, sub proto.SubscriberID, want []uint64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("received %d frames, want exactly %d revisions %v", len(got), len(want), want)
	}
	subscriber := mustSubscriberBytes(t, sub)
	for i, frame := range got {
		if frame.Revision != want[i] || frame.Subscriber != subscriber {
			t.Fatalf("frame %d: revision=%d subscriber=%x, want revision=%d subscriber=%x", i, frame.Revision, frame.Subscriber, want[i], subscriber)
		}
		var screen sessionruntime.Frame
		if err := json.Unmarshal(frame.Payload, &screen); err != nil {
			t.Fatalf("decode revision %d: %v", frame.Revision, err)
		}
		text := fmt.Sprintf("revision %03d", want[i])
		if screen.Revision != want[i] || len(screen.Rows) == 0 || screen.Rows[0].Text != text {
			t.Fatalf("frame %d does not carry the screen at revision %d: %s", i, want[i], frame.Payload)
		}
	}
}

func TestASlowScreenSubscriberKeepsBoundedOrderedFramesAndResumes(t *testing.T) {
	sink := newDelayedScreenSink()
	svc, hs := spawnScripted(t, newScriptedProcess(""), 0)
	release := svc.Bind(sink)
	t.Cleanup(release)
	t.Cleanup(sink.release)
	sub := proto.SubscriberID("0123456789abcdef0123456789abcdef")
	params, err := json.Marshal(proto.AttachParams{Session: hs.id, Subscriber: sub})
	if err != nil {
		t.Fatalf("attach params: %v", err)
	}
	if _, err := svc.Call(host.WithConnection(context.Background(), sink), proto.OpAttach, params); err != nil {
		t.Fatalf("attach: %v", err)
	}
	hs.mu.Lock()
	consumer := hs.subs[sub].screenCons
	hs.mu.Unlock()

	base := uint64(hs.runtime.Revision())
	ingestScreenRevision(t, hs, base+1)
	select {
	case <-sink.screenEntered:
	case <-time.After(hangLimit):
		t.Fatal("first screen frame never reached the blocked sink")
	}
	// The real drain is now inside SendScreenFrame. It cannot Take again
	// until this sink resumes, so the remaining publications fill its queue.
	const pending = 39
	for i := uint64(2); i <= pending+1; i++ {
		ingestScreenRevision(t, hs, base+i)
		if got := consumer.Pending(); got > sessionruntime.MaxPendingFrames {
			t.Fatalf("blocked reader holds %d frames, bound %d", got, sessionruntime.MaxPendingFrames)
		}
	}
	const lost = pending - sessionruntime.MaxPendingFrames
	if consumer.Pending() != sessionruntime.MaxPendingFrames || consumer.Coalesced() != lost || !consumer.Stale() {
		t.Fatalf("blocked reader: pending=%d coalesced=%d stale=%v, want pending=%d coalesced=%d stale=true",
			consumer.Pending(), consumer.Coalesced(), consumer.Stale(), sessionruntime.MaxPendingFrames, lost)
	}
	want := []uint64{base + 1}
	for i := uint64(lost + 2); i <= pending+1; i++ {
		want = append(want, base+i)
	}
	sink.release()
	assertScreenRevisions(t, sink.waitForScreenFrames(t, hs.raw, len(want)), sub, want)
	if consumer.Pending() != 0 || consumer.Coalesced() != lost || consumer.Stale() {
		t.Fatalf("resumed reader: pending=%d coalesced=%d stale=%v, want pending=0 coalesced=%d stale=false",
			consumer.Pending(), consumer.Coalesced(), consumer.Stale(), lost)
	}
	t.Logf("pending=%d coalesced=%d delivered=%d/40 ordered revisions=%v", pending, lost, len(want), want)
	// The reader must receive new output after its pending allowance is
	// refunded, not just its last queued screen.
	ingestScreenRevision(t, hs, base+41)
	want = append(want, base+41)
	assertScreenRevisions(t, sink.waitForScreenFrames(t, hs.raw, len(want)), sub, want)
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
	return s.drainSink.SendScreenFrame(frame)
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
