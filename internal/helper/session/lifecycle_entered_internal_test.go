package session

// The helper's half of an environment entry's idempotence (nocx-2v80t.3.28):
// the coordinator's downlink retries a lifecycle-entered whose attempt timed
// out, and that attempt may already have landed. The op crosses the REAL
// dispatch (Ops, params schema, Call) here, and what is read back is the
// session's row stream — one end marker per interval the entries sealed
// (the runtime's sealed records themselves were retired by nocx-zg3k3.5.4).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// intervalCounter is a sessionruntime.RowStream that counts the intervals
// the runtime seals: one IntervalEnd per sealed interval.
type intervalCounter struct {
	mu     sync.Mutex
	sealed int
}

func (c *intervalCounter) OutputRows(from uint64, rows []emulator.Row, lost uint64) {}

func (c *intervalCounter) IntervalEnd(nonce sessionruntime.FenceNonce, endRow uint64, closing []emulator.Row, settledWithoutFence bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sealed++
}

func (c *intervalCounter) ClearBoundary() {}

func (c *intervalCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sealed
}

const sessionruntimeRemembered = sessionruntime.MaxRememberedEnvironmentEntries

// enter sends one lifecycle-entered for the session through the service's
// real dispatch.
func enter(t *testing.T, svc *Service, id proto.HostSessionID, entry string) error {
	t.Helper()
	raw, err := json.Marshal(proto.LifecycleEnteredParams{
		Session:     id,
		Incarnation: proto.Incarnation{Session: id.Session, Generation: 1},
		Entry:       entry,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, err = svc.Call(context.Background(), proto.OpLifecycleEntered, raw)
	return err
}

// bindIntervalCounter binds a row-stream counter to the session's runtime,
// before any entry is delivered, and answers the count of sealed intervals.
func bindIntervalCounter(t *testing.T, svc *Service, id proto.HostSessionID) *intervalCounter {
	t.Helper()
	hs, err := svc.find(id)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	c := &intervalCounter{}
	hs.runtime.SetRowStream(c)
	return c
}

func TestLifecycleEnteredTwiceForOneEntrySealsOneInterval(t *testing.T) {
	svc, id := spawnOne(t)
	ends := bindIntervalCounter(t, svc, id)

	for i := range 2 {
		if err := enter(t, svc, id, "dom-child"); err != nil {
			t.Fatalf("lifecycle-entered, delivery %d: %v", i+1, err)
		}
	}
	if got := ends.count(); got != 1 {
		t.Fatalf("one entry delivered twice sealed %d intervals, want 1", got)
	}
}

func TestLifecycleEnteredForTwoEntriesSealsTwoIntervals(t *testing.T) {
	svc, id := spawnOne(t)
	ends := bindIntervalCounter(t, svc, id)

	for _, entry := range []string{"dom-child", "dom-grandchild"} {
		if err := enter(t, svc, id, entry); err != nil {
			t.Fatalf("lifecycle-entered %s: %v", entry, err)
		}
	}
	if got := ends.count(); got != 2 {
		t.Fatalf("two distinct entries sealed %d intervals, want 2", got)
	}
}

// An entry that names no entry cannot be told from its own retry, so it is
// refused as malformed wire and seals nothing.
func TestLifecycleEnteredRefusesAnEntryWithNoIdentity(t *testing.T) {
	svc, id := spawnOne(t)
	ends := bindIntervalCounter(t, svc, id)

	if err := enter(t, svc, id, ""); !errors.Is(err, errBadEntry) {
		t.Fatalf("an entry with no identity: err = %v, want errBadEntry", err)
	}
	if got := ends.count(); got != 0 {
		t.Fatalf("a refused entry sealed %d intervals, want 0", got)
	}
}

// enterWith is enter under the caller's own context — the request context
// the host hands the real handler, which a TypeCancel or the transport's end
// cancels once the caller has given up (nocx-2v80t.3.31).
func enterWith(ctx context.Context, t *testing.T, svc *Service, id proto.HostSessionID, entry string) error {
	t.Helper()
	raw, err := json.Marshal(proto.LifecycleEnteredParams{
		Session:     id,
		Incarnation: proto.Incarnation{Session: id.Session, Generation: 1},
		Entry:       entry,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, err = svc.Call(ctx, proto.OpLifecycleEntered, raw)
	return err
}

// A delivery whose caller gave up changes nothing when its handler finally
// runs (nocx-2v80t.3.31, finding 3). The downlink timed the first attempt
// out and cancelled it; its retry landed; more entries followed — more than
// the runtime remembers. The first attempt's handler, scheduled only now,
// used to seal the same entry a second time, because the handler dropped its
// request context and the dedupe had forgotten the id. It is refused by the
// context, which does not depend on how many entries came after it.
func TestALifecycleEnteredWhoseCallerGaveUpChangesNothing(t *testing.T) {
	svc, id := spawnOne(t)
	ends := bindIntervalCounter(t, svc, id)

	// The retry of "dom-child" lands, then nine more entries.
	if err := enter(t, svc, id, "dom-child"); err != nil {
		t.Fatalf("the retry: %v", err)
	}
	for i := range sessionruntimeRemembered + 1 {
		if err := enter(t, svc, id, fmt.Sprintf("dom-later-%d", i)); err != nil {
			t.Fatalf("a later entry: %v", err)
		}
	}
	before := ends.count()

	// The first attempt's handler, its caller long gone.
	gaveUp, cancel := context.WithCancel(context.Background())
	cancel()
	if err := enterWith(gaveUp, t, svc, id, "dom-child"); err == nil {
		t.Fatal("a delivery whose caller gave up was answered as landed")
	}
	if got := ends.count(); got != before {
		t.Fatalf("a delivery whose caller gave up sealed %d more intervals, want none", got-before)
	}

	// Paired: the same late handler with its caller still waiting is an
	// ordinary delivery of a new entry, and seals.
	if err := enterWith(context.Background(), t, svc, id, "dom-next"); err != nil {
		t.Fatalf("a live delivery: %v", err)
	}
	if got := ends.count(); got != before+1 {
		t.Fatalf("a live delivery sealed %d intervals, want 1", got-before)
	}
}
