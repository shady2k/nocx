package hostsvc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/waittest"
)

// The watch's tests drive time instead of waiting for it: every rung of the
// ladder is a clock advance, and every assertion is about what that advance
// made happen. Nothing here sleeps, and nothing depends on how fast the machine
// is — the whole leaf's cadence claim ("the host polls at the transport's
// ladder") is only checkable at all if the two seconds between rungs are the
// test's two seconds.

// fakeClock is the watch's timer seam. Its After arms a waiter; Advance fires
// every armed waiter, which is exactly one per installed watch.
type fakeClock struct {
	mu      sync.Mutex
	waiters []fakeTick
}

type fakeTick struct {
	ch    chan time.Time
	delay time.Duration
}

func (c *fakeClock) After(delay time.Duration) (<-chan time.Time, func()) {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	c.waiters = append(c.waiters, fakeTick{ch: ch, delay: delay})
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.waiters = slices.DeleteFunc(c.waiters, func(tick fakeTick) bool { return tick.ch == ch })
	}
}

// pending counts the armed waiters: one means the watch is between polls.
func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

// advance fires the armed waiter and waits for the watch to arm the next one,
// which is the observable that says the poll it woke has finished.
func (c *fakeClock) advance(t *testing.T) time.Duration {
	t.Helper()
	waittest.WaitFor(t, "the watch to arm its timer", func() bool { return c.pending() > 0 })
	c.mu.Lock()
	waiters := c.waiters
	c.waiters = nil
	c.mu.Unlock()
	if len(waiters) != 1 {
		t.Fatalf("armed timers = %d, want one", len(waiters))
	}
	waiters[0].ch <- time.Now()
	waittest.WaitFor(t, "the watch to re-arm after its poll", func() bool { return c.pending() > 0 })
	return waiters[0].delay
}

// notifySink is the host's wire end: a writer that decodes the frames the host
// emits and keeps the fs notifications. The host is a real one, so
// SendNotification is the shipped write path rather than a stand-in.
type notifySink struct {
	mu      sync.Mutex
	decoder *proto.Decoder
	changes chan proto.FSChanged
}

func newNotifySink() *notifySink {
	s := &notifySink{changes: make(chan proto.FSChanged, 32)}
	s.decoder = proto.NewDecoder(func(ty proto.FrameType, _, _ uint32, payload []byte) {
		if ty != proto.TypeNotify {
			return
		}
		var n proto.Notification
		if err := json.Unmarshal(payload, &n); err != nil {
			return
		}
		if n.Service != proto.ServiceFS || n.Event != proto.EventFSChanged {
			return
		}
		raw, err := json.Marshal(n.Params)
		if err != nil {
			return
		}
		var changed proto.FSChanged
		if err := json.Unmarshal(raw, &changed); err != nil {
			return
		}
		select {
		case s.changes <- changed:
		default:
		}
	}, nil)
	return s
}

func (s *notifySink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.decoder.Feed(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *notifySink) next(t *testing.T) proto.FSChanged {
	t.Helper()
	select {
	case c := <-s.changes:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("the host announced no change")
		return proto.FSChanged{}
	}
}

func (s *notifySink) quiet(t *testing.T) {
	t.Helper()
	select {
	case c := <-s.changes:
		t.Fatalf("a quiet directory announced a change: %+v", c)
	default:
	}
}

// watchHarness is one service, one clock, one connection and the counters a
// cadence assertion needs.
type watchHarness struct {
	svc    *Service
	clock  *fakeClock
	conn   *host.Host
	sink   *notifySink
	polls  atomic.Int64
	polled chan struct{}
	dir    string
}

func newWatchHarness(t *testing.T) *watchHarness {
	t.Helper()
	dir := tempDir(t)
	svc := newTestService(t, local.New(local.WithRoot(dir)))
	clock := &fakeClock{}
	svc.watchClock = clock
	h := &watchHarness{svc: svc, clock: clock, dir: dir, sink: newNotifySink(), polled: make(chan struct{}, 64)}
	svc.watchPolled = func(string) {
		h.polls.Add(1)
		select {
		case h.polled <- struct{}{}:
		default:
		}
	}
	h.conn = host.New(nil, h.sink, "hash", "instance", logtest.Slog(t))
	return h
}

func (h *watchHarness) watch(t *testing.T, id, path string) proto.FSWatchResult {
	t.Helper()
	answer, err := h.svc.Call(host.WithConnection(t.Context(), h.conn), proto.OpWatch,
		callParams(t, proto.FSWatchParams{ID: id, Path: path}))
	if err != nil {
		t.Fatalf("fs.watch: %v", err)
	}
	got, _ := answer.(proto.FSWatchResult)
	return got
}

func (h *watchHarness) unwatch(t *testing.T, id string) {
	t.Helper()
	if _, err := h.svc.Call(host.WithConnection(t.Context(), h.conn), proto.OpUnwatch,
		callParams(t, proto.FSUnwatchParams{ID: id})); err != nil {
		t.Fatalf("fs.unwatch: %v", err)
	}
}

// watchDone answers the exit channel of one installed watch, which is the
// exact observable for "the poller is gone": the goroutine closes it as its
// last act, so a test that waits on it waits on the closing event rather than
// on a sleep.
func (h *watchHarness) watchDone(t *testing.T, id string) <-chan struct{} {
	t.Helper()
	h.svc.watchMu.Lock()
	defer h.svc.watchMu.Unlock()
	w := h.svc.watches[id]
	if w == nil {
		t.Fatalf("no watch %q is installed", id)
	}
	return w.done
}

func awaitDone(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not stop", what)
	}
}

// listRev is the digest a caller holds after listing the directory itself —
// the value fs.watch's answer has to agree with for "changed since I looked" to
// mean anything.
func (h *watchHarness) listRev(t *testing.T, path string) string {
	t.Helper()
	answer, err := h.svc.Call(host.WithConnection(t.Context(), h.conn), proto.OpList,
		callParams(t, proto.FSListParams{Path: path, Offset: 0, Limit: 1}))
	if err != nil {
		t.Fatalf("fs.list: %v", err)
	}
	got, _ := answer.(proto.FSListResult)
	return got.Rev
}

func writeWatchFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestWatchAnnouncesOneChangeAndNothingElse is the leaf's first acceptance
// line: a change fires exactly one fs.changed, the digest it carries is the
// digest a list answers, and a quiet folder sends nothing at all.
func TestWatchAnnouncesOneChangeAndNothingElse(t *testing.T) {
	h := newWatchHarness(t)
	file := filepath.Join(h.dir, "note.txt")
	writeWatchFile(t, file, "one")

	answer := h.watch(t, "w1", h.dir)
	if want := h.listRev(t, h.dir); answer.Rev != want {
		t.Fatalf("fs.watch answered rev %q, want the directory's listed rev %q", answer.Rev, want)
	}

	// A quiet folder: the ladder runs on the host, and nothing crosses the
	// wire. Advancing several rungs must produce polls and no notification.
	h.clock.advance(t)
	h.clock.advance(t)
	h.clock.advance(t)
	h.sink.quiet(t)
	if got := h.polls.Load(); got != 3 {
		t.Fatalf("polls after three advances = %d, want 3", got)
	}

	writeWatchFile(t, file, "changed")
	h.clock.advance(t)
	changed := h.sink.next(t)
	if changed.ID != "w1" {
		t.Fatalf("notification id = %q, want w1", changed.ID)
	}
	if want := h.listRev(t, h.dir); changed.Rev != want {
		t.Fatalf("announced rev %q, want the digest of the new listing %q", changed.Rev, want)
	}

	// One notification for one change: the next rung sees the same digest.
	h.clock.advance(t)
	h.clock.advance(t)
	h.sink.quiet(t)
}

// TestWatchPollsOnTheTransportLadder proves the host's cadence IS the ladder
// the transport uses when it has to poll itself: the same base, the same rungs,
// one poll per advance.
func TestWatchPollsOnTheTransportLadder(t *testing.T) {
	h := newWatchHarness(t)
	h.watch(t, "w1", h.dir)

	// The first timer is the initial observation. The unchanged directory
	// then goes through the quiet ladder.
	for _, seconds := range []time.Duration{1, 1, 2, 5, 10, 10} {
		if got, want := h.clock.advance(t), seconds*time.Second; got != want {
			t.Fatalf("quiet poll delay = %s, want %s", got, want)
		}
	}
	if got := h.polls.Load(); got != 6 {
		t.Fatalf("polls = %d over six rungs, want 6", got)
	}

	// The failure ladder is the same statement on a directory that cannot be
	// listed: the wait stretches, and it still announces nothing, because a
	// directory without a digest has no digest to name.
	if err := os.RemoveAll(h.dir); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(t) // The current quiet timer detects the first error.
	before := h.polls.Load()
	for _, seconds := range []time.Duration{10, 20, 40, 60, 60} {
		if got, want := h.clock.advance(t), seconds*time.Second; got != want {
			t.Fatalf("failed poll delay = %s, want %s", got, want)
		}
	}
	if got := h.polls.Load() - before; got != 5 {
		t.Fatalf("polls = %d over five failed advances, want 5", got)
	}
	h.sink.quiet(t)
}

// TestWatchStopsOnUnwatch proves the release is exact: after fs.unwatch returns
// nothing polls and nothing is announced, even though the directory keeps
// changing.
func TestWatchStopsOnUnwatch(t *testing.T) {
	h := newWatchHarness(t)
	file := filepath.Join(h.dir, "note.txt")
	writeWatchFile(t, file, "one")
	h.watch(t, "w1", h.dir)
	h.clock.advance(t)

	done := h.watchDone(t, "w1")
	h.unwatch(t, "w1")
	awaitDone(t, done, "the released watch")
	if got := h.clock.pending(); got != 0 {
		t.Fatalf("unwatch left %d timers armed", got)
	}
	before := h.polls.Load()
	writeWatchFile(t, file, "changed")
	if got := h.polls.Load(); got != before {
		t.Fatalf("a released watch polled %d more times", got-before)
	}
	h.sink.quiet(t)
}

// TestWatchEndsWithItsConnection is the connection-lifetime half: a connection
// that ended takes every watch it installed with it, and the poll does not
// outlive the socket it would write to.
func TestWatchEndsWithItsConnection(t *testing.T) {
	h := newWatchHarness(t)
	file := filepath.Join(h.dir, "note.txt")
	writeWatchFile(t, file, "one")
	h.watch(t, "w1", h.dir)
	h.clock.advance(t)

	done := h.watchDone(t, "w1")
	h.svc.ConnectionClosed(h.conn)
	awaitDone(t, done, "the ended connection's watch")
	if got := h.clock.pending(); got != 0 {
		t.Fatalf("connection close left %d timers armed", got)
	}
	before := h.polls.Load()
	writeWatchFile(t, file, "changed")
	if got := h.polls.Load(); got != before {
		t.Fatalf("a watch of an ended connection polled %d more times", got-before)
	}
	h.sink.quiet(t)
}

// TestServiceCloseStopsEveryWatch is the daemon's own shutdown: a poll
// goroutine that outlived the service would write down a connection a later
// generation owns.
func TestServiceCloseStopsEveryWatch(t *testing.T) {
	h := newWatchHarness(t)
	h.watch(t, "w1", h.dir)
	h.clock.advance(t)
	done := h.watchDone(t, "w1")
	if err := h.svc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	awaitDone(t, done, "the service's watch")
}

// TestWatchUnderOneIDReplacesTheEarlierSubscription keeps "one id names one
// watch" honest: the caller re-subscribing after a loss it could not tell from
// a loss of the host's watch must not end up with two pollers announcing the
// same change twice.
func TestWatchUnderOneIDReplacesTheEarlierSubscription(t *testing.T) {
	h := newWatchHarness(t)
	file := filepath.Join(h.dir, "note.txt")
	writeWatchFile(t, file, "one")
	h.watch(t, "w1", h.dir)
	h.clock.advance(t)

	replaced := h.watchDone(t, "w1")
	h.watch(t, "w1", h.dir)
	// The replaced subscription stops and cancels its timer. Advancing the
	// clock below must fire the replacement's timer.
	awaitDone(t, replaced, "the replaced watch")
	writeWatchFile(t, file, "changed")
	h.clock.advance(t)
	changed := h.sink.next(t)
	if changed.ID != "w1" {
		t.Fatalf("notification id = %q, want w1", changed.ID)
	}
	h.clock.advance(t)
	h.sink.quiet(t)
}

// TestWatchRefusesAPathThatIsNotADirectory keeps the refusal typed: the caller
// learns the same thing a list would have told it, before anything is polled.
func TestWatchRefusesAPathThatIsNotADirectory(t *testing.T) {
	h := newWatchHarness(t)
	file := filepath.Join(h.dir, "note.txt")
	writeWatchFile(t, file, "one")
	_, err := h.svc.Call(host.WithConnection(t.Context(), h.conn), proto.OpWatch,
		callParams(t, proto.FSWatchParams{ID: "w1", Path: file}))
	if err == nil {
		t.Fatal("fs.watch of a regular file succeeded")
	}
	code, _, _ := refusalOf(err)
	if code != proto.FSCodeNotDir {
		t.Fatalf("refusal = %q, want %q", code, proto.FSCodeNotDir)
	}
	if h.clock.pending() != 0 {
		t.Fatal("a refused watch left a poller behind")
	}
	_, err = h.svc.Call(context.Background(), proto.OpUnwatch, callParams(t, proto.FSUnwatchParams{ID: "never"}))
	if err != nil {
		t.Fatalf("unwatching an unknown id: %v", err)
	}
}
