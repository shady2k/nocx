package helperfs_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/helperfs"
	"github.com/shady2k/nocx/internal/filesystem/hostsvc"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/waittest"
)

// The far side's watch, over a REAL client and a REAL host: the subscription
// is an fs.watch request, the announcement is a notification the shipped
// service wrote, and the change the host saw is a file this test wrote on the
// disk it is watching. Nothing here waits for a duration — the host's poll runs
// on a clock the test advances, and every expectation is about what that
// advance made happen.

// watchClock is the host's timer under the test's control.
type watchClock struct {
	mu      sync.Mutex
	waiters []chan time.Time
	armed   chan time.Duration
}

func (c *watchClock) After(delay time.Duration) (<-chan time.Time, func()) {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	c.waiters = append(c.waiters, ch)
	c.mu.Unlock()
	if c.armed != nil {
		c.armed <- delay
	}
	return ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.waiters = slices.DeleteFunc(c.waiters, func(waiter chan time.Time) bool { return waiter == ch })
	}
}

func (c *watchClock) waitForPollTimer(t *testing.T) {
	t.Helper()
	waittest.WaitFor(t, "the host to arm its next poll", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.waiters) > 0
	})
}

// tick fires the armed timers and answers how many there were, which is the
// number of watches currently polling on this host.
func (c *watchClock) tick() int {
	c.mu.Lock()
	waiters := c.waiters
	c.waiters = nil
	c.mu.Unlock()
	for _, ch := range waiters {
		ch <- time.Now()
	}
	return len(waiters)
}

// pollAndReceive advances one poll and waits for its notification.
func (c *watchClock) pollAndReceive(t *testing.T, watched filesystem.Watch) {
	t.Helper()
	c.waitForPollTimer(t)
	c.tick()
	select {
	case <-watched.Events():
	case <-time.After(5 * time.Second):
		t.Fatal("the host announced no change")
	}
}

type watchPeer struct {
	client *client.Client
	lane   *pipeLane
	obs    *fsObserver
	clock  *watchClock
}

// watchFixture is one connection's worth of the real stack: a helper serving
// the shipped fs service over a pipe, the farhelper registry in front of it,
// and the helperfs provider a binding would hold.
type watchFixture struct {
	root     string
	peers    []*watchPeer
	reg      *farhelper.Registry
	provider *helperfs.Provider
}

func newWatchFixture(t *testing.T, peers int) *watchFixture {
	t.Helper()
	root := tempTree(t)
	f := &watchFixture{root: root}
	clients := make(map[string][]*client.Client, 1)
	for i := 0; i < peers; i++ {
		clock := &watchClock{}
		svc, err := hostsvc.New(local.New(local.WithRoot(root)), hostsvc.JournalConfig{
			Root:       t.TempDir(),
			Hostname:   "watch-host",
			InstanceID: strings.Repeat("b", 32),
		}, hostsvc.WithWatchClock(clock))
		if err != nil {
			t.Fatalf("new fs service: %v", err)
		}
		t.Cleanup(func() { _ = svc.Close() })
		obs := &fsObserver{Service: svc}
		c, lane := servePeer(t, obs, "instance-1")
		clients[panelID] = append(clients[panelID], c)
		f.peers = append(f.peers, &watchPeer{client: c, lane: lane, obs: obs, clock: clock})
	}
	f.reg, _, _ = registry(t, nil, clients)
	f.provider = helperfs.New(f.reg, panelID, root)
	return f
}

// ops answers every RPC the coordinator sent this peer, in order.
func (p *watchPeer) ops() []string {
	var names []string
	for _, call := range p.obs.observed() {
		names = append(names, call.op)
	}
	return names
}

func (p *watchPeer) count(op string) int {
	n := 0
	for _, name := range p.ops() {
		if name == op {
			n++
		}
	}
	return n
}

func (f *watchFixture) write(t *testing.T, name, content string) {
	t.Helper()
	writeFile(t, filepath.Join(f.root, name), []byte(content))
}

// TestWatchAnnouncesTheHostsChangeAndSilencesAfterClose is the leaf's first
// acceptance line end to end: one change on the host is one event on
// Events(), a quiet directory produces none, and Close sends the unwatch that
// stops the host polling.
func TestWatchAnnouncesTheHostsChangeAndSilencesAfterClose(t *testing.T) {
	f := newWatchFixture(t, 1)
	peer := f.peers[0]
	f.write(t, "note.txt", "one")

	w, err := f.provider.Watch(testContext(t), f.root)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	if want := (filesystem.WatchMode{Kind: filesystem.WatchPolling}); w.Mode() != want {
		t.Fatalf("watch mode = %+v, want %+v", w.Mode(), want)
	}
	if got := peer.count("watch"); got != 1 {
		t.Fatalf("fs.watch requests = %d, want 1", got)
	}

	// Quiet: the host polls its own disk, and the connection sends NOTHING.
	peer.clock.waitForPollTimer(t)
	peer.clock.tick()
	peer.clock.waitForPollTimer(t)
	select {
	case <-w.Events():
		t.Fatal("a quiet directory produced a change event")
	default:
	}
	if got := peer.count("list"); got != 0 {
		t.Fatalf("a quiet watch sent %d listings over the wire, want 0", got)
	}

	f.write(t, "note.txt", "changed")
	peer.clock.pollAndReceive(t, w)

	// One change, one hint: the next rung sees the same digest.
	waittest.WaitFor(t, "the host to arm its next poll after the announcement", func() bool {
		return peer.clock.tick() > 0
	})
	select {
	case <-w.Events():
		t.Fatal("one change produced two events")
	case <-time.After(50 * time.Millisecond):
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := peer.count("unwatch"); got != 1 {
		t.Fatalf("fs.unwatch requests = %d, want 1", got)
	}
	// The host stopped polling: firing whatever timer is left must neither
	// announce nor poll again.
	peer.clock.tick()
	f.write(t, "note.txt", "three")
	peer.clock.tick()
	select {
	case <-w.Events():
		t.Fatal("a closed watch still announced")
	default:
	}
}

// TestWatchReSubscribesAfterACutAndCatchesTheChange is the interval the cut
// creates: the subscription dies with the connection, the change happens while
// nobody is listening, and the watch must still announce it once the axis is
// serving again.
func TestWatchReSubscribesAfterACutAndCatchesTheChange(t *testing.T) {
	f := newWatchFixture(t, 2)
	first, second := f.peers[0], f.peers[1]
	f.write(t, "note.txt", "one")

	w, err := f.provider.Watch(testContext(t), f.root)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if got := first.count("watch"); got != 1 {
		t.Fatalf("fs.watch requests on the first connection = %d, want 1", got)
	}

	// The connection is cut, and the change lands while nothing is listening.
	first.lane.dropReply()
	waittest.WaitFor(t, "the axis to leave serving", func() bool {
		status, known := f.reg.Axis(panelID)
		return known && status.State != farhelper.StateServing
	})
	f.write(t, "note.txt", "changed during the cut")

	// The refresh redials; the watch must follow it.
	if err := f.reg.Refresh(t.Context(), panelID); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	waittest.WaitFor(t, "the watch to re-subscribe on the new connection", func() bool {
		return second.count("watch") == 1
	})
	select {
	case <-w.Events():
	case <-time.After(5 * time.Second):
		t.Fatal("the change made during the cut was not announced")
	}

	// And the new subscription is live: a later change is announced too, once.
	f.write(t, "note.txt", "after the return")
	second.clock.pollAndReceive(t, w)
	select {
	case <-w.Events():
		t.Fatal("one change produced two events on the new subscription")
	case <-time.After(50 * time.Millisecond):
	}
}

// TestWatchIsRefusedWhenTheHelperIsNotServing proves the transport's decision
// stays honest at the boundary: a provider that cannot watch says so, and the
// transport's own ladder is then the change signal rather than a watch that
// would never announce anything.
func TestWatchIsRefusedWhenTheHelperIsNotServing(t *testing.T) {
	f := newWatchFixture(t, 1)
	// Cut the only connection and do not redial: the axis is no longer serving.
	f.peers[0].lane.dropReply()
	waittest.WaitFor(t, "the axis to leave serving", func() bool {
		status, known := f.reg.Axis(panelID)
		return known && status.State != farhelper.StateServing
	})
	_, err := f.provider.Watch(testContext(t), f.root)
	var unavailable *filesystem.ErrUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("watch of a paused helper = %v, want *filesystem.ErrUnavailable", err)
	}
	if unavailable.Reason == "" {
		t.Fatal("the refusal carries no reason, so a panel could draw nothing")
	}
}

// TestWatchOfAMissingDirectoryIsRefused keeps the refusal typed through the
// client: a host that cannot list the path says so before installing anything.
func TestWatchOfAMissingDirectoryIsRefused(t *testing.T) {
	f := newWatchFixture(t, 1)
	_, err := f.provider.Watch(testContext(t), filepath.Join(f.root, "nope"))
	var notFound *filesystem.ErrNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("watch of a missing directory = %v, want *filesystem.ErrNotFound", err)
	}
	if got := f.peers[0].count("watch"); got != 1 {
		t.Fatalf("fs.watch requests = %d, want the one that was refused", got)
	}
	if got := f.peers[0].clock.tick(); got != 0 {
		t.Fatalf("a refused watch left %d pollers behind", got)
	}
}

// TestProviderWatchIdIsNotAPath keeps the id opaque: two watches of two
// directories are two subscriptions, and each change announces only its own.
func TestProviderWatchIdIsNotAPath(t *testing.T) {
	f := newWatchFixture(t, 1)
	if err := os.Mkdir(filepath.Join(f.root, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	f.write(t, "one.txt", "a")
	writeFile(t, filepath.Join(f.root, "sub", "two.txt"), []byte("b"))

	first, err := f.provider.Watch(testContext(t), f.root)
	if err != nil {
		t.Fatalf("watch root: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := f.provider.Watch(testContext(t), filepath.Join(f.root, "sub"))
	if err != nil {
		t.Fatalf("watch sub: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if got := f.peers[0].count("watch"); got != 2 {
		t.Fatalf("fs.watch requests = %d, want 2", got)
	}

	// A change in sub is the second watch's; the first (the root) sees it too,
	// because the root's own digest covers its children. Changing a file in the
	// ROOT must not reach the sub watch.
	f.write(t, "one.txt", "changed")
	waittest.WaitFor(t, "both watches to poll", func() bool { return f.peers[0].clock.tick() > 0 })
	select {
	case <-first.Events():
	case <-time.After(5 * time.Second):
		t.Fatal("the root watch announced nothing for a change in the root")
	}
	select {
	case <-second.Events():
		t.Fatal("a change outside the watched directory reached its watch")
	default:
	}
}

func TestWatchReconnectDoesNotRepeatAnObservedChange(t *testing.T) {
	f := newWatchFixture(t, 2)
	first, second := f.peers[0], f.peers[1]
	f.write(t, "note.txt", "initial")
	w, err := f.provider.Watch(testContext(t), f.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	f.write(t, "note.txt", "changed before the cut")
	first.clock.pollAndReceive(t, w)
	first.lane.dropReply()
	waittest.WaitFor(t, "the axis to leave serving", func() bool {
		status, known := f.reg.Axis(panelID)
		return known && status.State != farhelper.StateServing
	})
	if err := f.reg.Refresh(t.Context(), panelID); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	waittest.WaitFor(t, "the watch to re-subscribe on the new connection", func() bool {
		return second.count("watch") == 1
	})
	select {
	case <-w.Events():
		t.Fatal("reconnect replayed the change already observed before the cut")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestWatchRetriesRefusedSubscriptionOnServingLease(t *testing.T) {
	retry := &watchClock{armed: make(chan time.Duration, 1)}
	f := newWatchFixture(t, 2)
	helperfs.SetWatchRetryClock(f.provider, retry)
	first, second := f.peers[0], f.peers[1]
	second.obs.intercept = func(_ context.Context, op string, _ json.RawMessage, call func() (any, error)) (any, error) {
		if op == proto.OpWatch && second.count(proto.OpWatch) == 1 {
			return nil, &filesystem.ErrTimedOut{}
		}
		return call()
	}
	w, err := f.provider.Watch(testContext(t), f.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	first.lane.dropReply()
	waittest.WaitFor(t, "the axis to leave serving", func() bool {
		status, known := f.reg.Axis(panelID)
		return known && status.State != farhelper.StateServing
	})
	f.write(t, "note.txt", "changed during the cut")
	if err := f.reg.Refresh(t.Context(), panelID); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	waittest.WaitFor(t, "the new helper to receive its first watch request", func() bool {
		return second.count(proto.OpWatch) == 1
	})
	select {
	case delay := <-retry.armed:
		if delay != 10*time.Second {
			t.Fatalf("first retry delay = %s, want 10s", delay)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("failed resubscription on a serving lease did not arm a retry")
	}
	if status, _ := f.reg.Axis(panelID); status.State != farhelper.StateServing {
		t.Fatalf("axis during retry = %v, want serving", status.State)
	}
	retry.tick()
	select {
	case <-w.Events():
	case <-time.After(5 * time.Second):
		t.Fatal("the retry did not announce the change made during the cut")
	}
	if got := second.count(proto.OpWatch); got != 2 {
		t.Fatalf("watch requests on the new lease = %d, want 2", got)
	}
}
