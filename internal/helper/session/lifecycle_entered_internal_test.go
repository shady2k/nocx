package session

// The helper's half of an environment entry's idempotence (nocx-2v80t.3.28):
// the coordinator's downlink retries a lifecycle-entered whose attempt timed
// out, and that attempt may already have landed. The op crosses the REAL
// dispatch (Ops, params schema, Call) here, and what is read back is the
// runtime's own sealed records — one per interval the entries ended.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

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

// sealedIntervals is how many intervals the session's runtime has sealed.
func sealedIntervals(t *testing.T, svc *Service, id proto.HostSessionID) int {
	t.Helper()
	hs, err := svc.find(id)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	return len(hs.runtime.Observations())
}

func TestLifecycleEnteredTwiceForOneEntrySealsOneInterval(t *testing.T) {
	svc, id := spawnOne(t)

	for i := range 2 {
		if err := enter(t, svc, id, "dom-child"); err != nil {
			t.Fatalf("lifecycle-entered, delivery %d: %v", i+1, err)
		}
	}
	if got := sealedIntervals(t, svc, id); got != 1 {
		t.Fatalf("one entry delivered twice sealed %d intervals, want 1", got)
	}
}

func TestLifecycleEnteredForTwoEntriesSealsTwoIntervals(t *testing.T) {
	svc, id := spawnOne(t)

	for _, entry := range []string{"dom-child", "dom-grandchild"} {
		if err := enter(t, svc, id, entry); err != nil {
			t.Fatalf("lifecycle-entered %s: %v", entry, err)
		}
	}
	if got := sealedIntervals(t, svc, id); got != 2 {
		t.Fatalf("two distinct entries sealed %d intervals, want 2", got)
	}
}

// An entry that names no entry cannot be told from its own retry, so it is
// refused as malformed wire and seals nothing.
func TestLifecycleEnteredRefusesAnEntryWithNoIdentity(t *testing.T) {
	svc, id := spawnOne(t)

	if err := enter(t, svc, id, ""); !errors.Is(err, errBadEntry) {
		t.Fatalf("an entry with no identity: err = %v, want errBadEntry", err)
	}
	if got := sealedIntervals(t, svc, id); got != 0 {
		t.Fatalf("a refused entry sealed %d intervals, want 0", got)
	}
}
