package sessionruntime

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// The screen a program holds, and the frames it costs us.
//
// DEC private mode 2026 — synchronized output — is a program saying "do not
// show what I am drawing until I say so": ratatui and its kind wrap a redraw in
// BSU (`CSI ? 2026 h`) .. ESU (`CSI ? 2026 l`) so a person never sees the
// half-drawn state between clearing the screen and painting it (nocx-1ebrs.33,
// owner finding 55). This runtime published a frame at the end of EVERY ingest
// (runtime.go's Ingest), so a redraw that arrived as several reads was several
// frames, each a half-drawn screen, and a full-screen program's scroll looked
// jerky — the paint is not the culprit: the renderer keeps the newest snapshot
// and paints at most once per animation frame (frontend/src/terminal-content.ts).
//
// The mode is read from the emulator's own tracked state (ADR-0066, AD-6), never
// by scanning the bytes here: this package hands the bytes to one parser and
// asks IT what the program set. What crosses to a consumer is unchanged —
// session.frame, the whole screen (ADR-0081) — and nothing here may carry or
// reconstruct terminal output for a client (ADR-0008).
//
// The three assertions, in the order the bead states them:
//
//  1. A scroll emitted inside BSU..ESU publishes EXACTLY ONE frame.
//  2. A program that opens BSU and then goes quiet still gets a frame after a
//     bounded wait — fail-open, because a person must never be left looking at
//     a screen nobody will ever update.
//  3. Outside 2026 nothing changes: a frame per ingest, exactly as before.

// tallyFrame is one frame a tally took, decoded to the two facts a test here
// judges: the revision it carries and the text its rows hold.
type tallyFrame struct {
	rev  uint64
	text string
}

// frameTally is a consumer that drains what the runtime publishes as it is
// published, so a test can count FRAMES rather than the queue a reader who
// arrives late leaves behind. It parks on the consumer's own signal and takes
// everything held — the loop a real consumer's pump runs
// (internal/transport), and the only way a count is about what the runtime
// published instead of about when the test happened to look.
type frameTally struct {
	t  *testing.T
	c  Consumer
	mu sync.Mutex
	// bytes is every frame taken, oldest first, still encoded: the pump runs
	// on its own goroutine, and decoding (which may fail the test) belongs to
	// the test's.
	bytes [][]byte
	// notify is replaced and closed on every take, so a wait wakes on the
	// next frame rather than on a clock.
	notify chan struct{}
	// beforePark, when set, runs after a wait has read the frames it is about
	// to judge and before it parks. The pump can take a frame in exactly that
	// window; a test that has to pin the window sets this.
	beforePark func()
	stop       chan struct{}
	done       chan struct{}
}

// tallyFrames attaches a draining consumer to s and starts its pump.
func tallyFrames(t *testing.T, s *Session) *frameTally {
	t.Helper()
	f := &frameTally{
		t:      t,
		c:      s.Consumers().Attach(),
		notify: make(chan struct{}),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go func() {
		defer close(f.done)
		for {
			select {
			case <-f.stop:
				f.take()
				return
			case <-f.c.Ready():
				f.take()
			}
		}
	}()
	t.Cleanup(f.close)
	return f
}

// take hands over everything held and records it.
func (f *frameTally) take() {
	deliveries := f.c.Take()
	if len(deliveries) == 0 {
		return
	}
	f.mu.Lock()
	for _, d := range deliveries {
		f.bytes = append(f.bytes, d.Bytes)
	}
	close(f.notify)
	f.notify = make(chan struct{})
	f.mu.Unlock()
}

// taken decodes what has been taken so far. It runs on whichever goroutine
// called it, and every caller is the test's: a decode that fails fails the test
// where a failure can still be reported.
func (f *frameTally) taken() []tallyFrame {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]tallyFrame, 0, len(f.bytes))
	for _, raw := range f.bytes {
		out = append(out, decodeTallyFrame(f.t, raw))
	}
	return out
}

// close ends the pump and drains what is still held: the frames a test counts
// after it are every frame the runtime published, whenever the pump last ran.
func (f *frameTally) close() {
	select {
	case <-f.stop:
		// Closed by an earlier call.
	default:
		close(f.stop)
		<-f.done
	}
}

// settled ends the pump, drains, and answers what it took.
func (f *frameTally) settled() []tallyFrame {
	f.close()
	return f.taken()
}

// reset forgets what was taken, for a test that measures a window of its own.
func (f *frameTally) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bytes = nil
}

// waitForHolding waits until a frame holding want has been taken and returns
// it. It parks on the tally's own signal, so the wait ends on the delivery and
// never on a duration; the limit only ends a wait that can no longer be
// satisfied.
//
// The signal is read BEFORE the frames it guards. take() appends and then
// replaces notify under the same lock, so a take that lands after this read
// closes the channel this wait parks on; reading it afterwards would park on
// the replacement and miss the frame until the limit. The deadline reads the
// frames once more before failing, and the message prints those same frames,
// so the match and the diagnostic always describe one state.
func (f *frameTally) waitForHolding(want string) tallyFrame {
	f.t.Helper()
	deadline := time.NewTimer(hangLimit)
	defer deadline.Stop()
	for {
		f.mu.Lock()
		next := f.notify
		f.mu.Unlock()
		frames := f.taken()
		for _, frame := range frames {
			if strings.Contains(frame.text, want) {
				return frame
			}
		}
		if f.beforePark != nil {
			f.beforePark()
		}
		select {
		case <-next:
		case <-deadline.C:
			frames = f.taken()
			for _, frame := range frames {
				if strings.Contains(frame.text, want) {
					return frame
				}
			}
			f.t.Fatalf("no frame holding %q arrived within %s; the frames taken read %v", want, hangLimit, frameTexts(frames))
		}
	}
}

// oneFrameConsumer holds at most one frame and hands it over once. It is the
// consumer side of the wait test below: the take happens where the test says,
// not on a pump goroutine of its own.
type oneFrameConsumer struct {
	frame FrameDelivery
	ready chan struct{}
}

func (c *oneFrameConsumer) Pending() int      { return 0 }
func (c *oneFrameConsumer) HeldBytes() int    { return 0 }
func (c *oneFrameConsumer) Coalesced() uint64 { return 0 }
func (c *oneFrameConsumer) EffectsLost() uint64 {
	return 0
}
func (c *oneFrameConsumer) Stale() bool            { return false }
func (c *oneFrameConsumer) Effects() []Effect      { return nil }
func (c *oneFrameConsumer) Ready() <-chan struct{} { return c.ready }

func (c *oneFrameConsumer) Take() []FrameDelivery {
	if c.frame.Bytes == nil {
		return nil
	}
	frame := c.frame
	c.frame = FrameDelivery{}
	return []FrameDelivery{frame}
}

// A take that lands between the frames a wait reads and the signal it parks on
// must still wake it: the pump runs on its own goroutine, so that window is an
// ordinary interleaving and not a contrived one. The old wait read f.notify
// AFTER checking the frames, so a take in the window replaced the channel it
// was about to park on and the delivery was lost until the hang limit — while
// the failure message, which re-read the frames, printed the very frame the
// wait had said never arrived.
func TestWaitForHoldingWakesForAFrameTakenWhileItChecks(t *testing.T) {
	raw, err := json.Marshal(Frame{Revision: 7, Rows: []frameRow{{Text: "ARMED"}}})
	if err != nil {
		t.Fatalf("encode the frame the wait is for: %v", err)
	}
	f := &frameTally{
		t:      t,
		c:      &oneFrameConsumer{frame: FrameDelivery{Revision: 7, Bytes: raw}, ready: make(chan struct{})},
		notify: make(chan struct{}),
	}
	taken := false
	f.beforePark = func() {
		if taken {
			return
		}
		taken = true
		f.take()
	}
	got := f.waitForHolding("ARMED")
	if got.rev != 7 || !strings.Contains(got.text, "ARMED") {
		t.Fatalf("the wait returned revision %d reading %q, want the ARMED frame it was woken for", got.rev, got.text)
	}
}

// decodeTallyFrame reads the materialised frame — the wire shape this runtime
// publishes, not a stand-in — down to the revision and the rows' text.
func decodeTallyFrame(t *testing.T, raw []byte) tallyFrame {
	t.Helper()
	var frame Frame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("the runtime published a frame that does not decode: %v", err)
	}
	var text strings.Builder
	for _, row := range frame.Rows {
		text.WriteString(row.Text)
		text.WriteByte('\n')
	}
	return tallyFrame{rev: frame.Revision, text: text.String()}
}

// holdBoundOf is a runtime option that moves the hold's fail-open bound out of
// a test's way. A test about what a RELEASE publishes must not be able to race
// a fail-open it is not about, and a test about the fail-open itself is left at
// the product's own bound ([DefaultSynchronizedOutputHoldBound]) — so the
// duration is stated by the test that needs it and not by the harness.
func holdBoundOf(d time.Duration) func(*Config) {
	return func(c *Config) { c.SynchronizedOutputHoldBound = d }
}

// A redraw split across several ingests inside BSU..ESU is ONE frame: the
// screen the program finished, and nothing in between.
func TestAScreenHeldBySynchronizedOutputPublishesOneFrameWhenThePairCloses(t *testing.T) {
	s, _, _ := realRuntime(t, holdBoundOf(time.Hour))
	c := s.Consumers().Attach()

	held := []string{"\x1b[?2026h", "one\r\n", "two\r\n", "three\r\n"}
	for _, chunk := range held {
		if err := s.Ingest([]byte(chunk)); err != nil {
			t.Fatalf("ingest %q: %v", chunk, err)
		}
	}
	if got := c.Take(); len(got) != 0 {
		t.Fatalf("a hold published %d frame(s) before the program said it was done drawing, want none", len(got))
	}
	if err := s.Ingest([]byte("\x1b[?2026l")); err != nil {
		t.Fatalf("ingest the end of the hold: %v", err)
	}
	frames := c.Take()
	if len(frames) != 1 {
		t.Fatalf("the pair closed and published %d frame(s), want exactly 1: a scroll inside a hold is one screen", len(frames))
	}
	if got := decodeTallyFrame(t, frames[0].Bytes).text; !strings.Contains(got, "three") {
		t.Fatalf("the one frame published carries %q, want the screen the program finished drawing", got)
	}
}

// Criterion 3, and it is the one that keeps the fix from being a freeze: an
// ordinary program — no mode 2026 anywhere — gets exactly one frame per
// ingest, as it did before this bead.
func TestOutsideASynchronizedOutputHoldEveryIngestPublishesAFrame(t *testing.T) {
	s, _, _ := realRuntime(t)
	c := s.Consumers().Attach()
	for _, line := range []string{"one\r\n", "two\r\n", "three\r\n", "four\r\n", "five\r\n"} {
		if err := s.Ingest([]byte(line)); err != nil {
			t.Fatalf("ingest %q: %v", line, err)
		}
	}
	if got := len(c.Take()); got != 5 {
		t.Fatalf("five ingests published %d frame(s), want one each: nothing outside mode 2026 may be held", got)
	}
}

// A hold withholds DELIVERY and nothing else: an attacher joining mid-hold is
// handed the screen as it stood when the hold began, which is what a terminal
// shows a person during one, and the release still ends on the finished
// screen.
func TestAMidHoldAttacherIsHandedTheScreenTheHoldBeganOn(t *testing.T) {
	s, _, _ := realRuntime(t, holdBoundOf(time.Hour))
	if err := s.Ingest([]byte("before\r\n")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if err := s.Ingest([]byte("\x1b[?2026h")); err != nil {
		t.Fatalf("ingest the held begin: %v", err)
	}
	if err := s.Ingest([]byte("half drawn\r\n")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	late := s.Consumers().Attach()
	frames := late.Take()
	if len(frames) != 1 {
		t.Fatalf("an attacher mid-hold was handed %d frame(s), want the one frame the screen showed", len(frames))
	}
	if got := decodeTallyFrame(t, frames[0].Bytes).text; strings.Contains(got, "half drawn") {
		t.Fatalf("an attacher mid-hold was handed the half-drawn screen %q: a hold withholds exactly that", got)
	}
	// The resync is the one reader a hold may never leave waiting, and it is
	// served from the frame the screen last published.
	if err := s.Consumers().Resend(); err != nil {
		t.Fatalf("resend during a hold: %v", err)
	}
	if got := late.Take(); len(got) != 1 {
		t.Fatalf("a resend during a hold handed over %d frame(s), want 1: a resync is state, not a redraw", len(got))
	}
}

// A full-screen program's scroll, emitted inside BSU..ESU on a REAL pty: the
// program draws twelve rows, each one released by a byte this test types, so
// the redraw reaches the runtime as several reads with no clock anywhere in the
// assertion. Before this bead that was twelve frames — twelve half-drawn
// screens, which is the jerk a person sees (the count is in the commit).
//
// The step that keeps the test from passing on a runtime that publishes one
// frame by accident is the REVISION the frame carries: the ingest path ticks
// once per ingest, so the frame this test ends on names how many ingests it
// stands for, and three is the least a split redraw can be.
const synchronizedScrollProgram = rawPreamble + `
printf 'ARMED\n'
dd bs=1 count=1 2>/dev/null >/dev/null
printf '\033[?2026h'
i=0
while [ "$i" -lt 12 ]; do
  dd bs=1 count=1 2>/dev/null >/dev/null
  printf 'scroll %s\n' "$i"
  i=$((i+1))
done
printf '\033[?2026l'
`

func TestAScrollInsideSynchronizedOutputIsPublishedOnceOnARealPTY(t *testing.T) {
	p := startProgram(t, synchronizedScrollProgram, harnessGeometry(80, 24), holdBoundOf(time.Hour))
	tally := tallyFrames(t, p.s)
	p.wait("ARMED")
	// The frame ARMED was drawn on is taken BEFORE the count starts: the
	// program is parked on a read from here, so nothing else can be published
	// until the hold begins, and the revision read here is the one it begins
	// at.
	tally.waitForHolding("ARMED")
	tally.reset()
	revBeforeHold := p.s.Revision()
	p.typed("g")
	for range 12 {
		p.typed("x")
	}

	// The ESU is the program's last write, so the frame this waits for is the
	// frame the release published: had the twelve steps been published as
	// they arrived, this wait would end on the first of them instead.
	finished := tally.waitForHolding("scroll 11")
	frames := tally.settled()
	if len(frames) != 1 {
		t.Fatalf("a twelve-step scroll inside one synchronized-output hold published %d frames, want exactly 1: %v",
			len(frames), frameTexts(frames))
	}
	if !strings.Contains(finished.text, "scroll 0") {
		t.Fatalf("the one frame published reads %q, want the whole screen the program finished drawing", finished.text)
	}
	// Three is the least that can prove the redraw was SPLIT: one ingest for
	// the BSU, one or more for the rows, one for the ESU. A redraw that
	// arrived as a single read would publish one frame on a runtime that does
	// nothing at all, and this test would then measure nothing.
	if got := finished.rev - uint64(revBeforeHold); got < 3 {
		t.Fatalf("the one frame published stands for %d ingest(s) (revision %d, the hold began at %d), want at least 3",
			got, finished.rev, revBeforeHold)
	}
}

// frameTexts is what a set of frames reads, for a failure message.
func frameTexts(frames []tallyFrame) []string {
	out := make([]string, 0, len(frames))
	for _, frame := range frames {
		out = append(out, frame.text)
	}
	return out
}

// Criterion 2 on a real pty: the program opens a hold, draws, and then goes
// quiet with the hold still open — it will not release it until this test types
// the second byte, which it does only after the frame arrived. So the frame
// that arrives is the fail-open's, and the wait is for the FRAME and never for
// a duration.
const quietHoldProgram = rawPreamble + `
printf 'ARMED\n'
dd bs=1 count=1 2>/dev/null >/dev/null
printf '\033[?2026h'
printf 'HELD-LINE\n'
dd bs=1 count=1 2>/dev/null >/dev/null
printf '\033[?2026l'
printf 'RELEASED\n'
`

func TestAHoldThatGoesQuietStillPublishesAFrameOnARealPTY(t *testing.T) {
	p := startProgram(t, quietHoldProgram, harnessGeometry(80, 24))
	tally := tallyFrames(t, p.s)
	p.wait("ARMED")
	tally.waitForHolding("ARMED")
	tally.reset()
	p.typed("g")

	failOpen := tally.waitForHolding("HELD-LINE")
	if strings.Contains(failOpen.text, "RELEASED") {
		t.Fatalf("the frame read %q, which is the release and not the fail-open: the program was not quiet", failOpen.text)
	}

	// And the hold itself was not abandoned by the fail-open: it released the
	// SCREEN, so the next ingest that still sees the mode open arms a fresh
	// window and the release still publishes what the program finished.
	p.typed("x")
	tally.waitForHolding("RELEASED")
}
