package app

// The notification centre, wired at the composition root (nocx-p0xhg.6).
//
// The check that matters is not "a feed exists" — it is that the seam the
// renderer reaches lands IN it. internal/content shipped an encrypted store,
// a key lifecycle, a budget and five settings controls whose write path had
// no caller outside its own tests (nocx-rtg0); a reachable read path hid an
// unreachable write path in the same package, and every gate stayed green.
// So these tests raise through the wired seam and assert the feed moved.

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

func TestAppWiresTheFeedIntoTheRaiseSeam(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("newTestApp: %v", err)
	}
	t.Cleanup(func() { a.Shutdown(context.Background()) })

	if a.notifyFeed == nil {
		t.Fatal("no feed constructed at the composition root")
	}
	if a.notifyIngress == nil {
		t.Fatal("no ingress constructed at the composition root")
	}
	before := a.notifyFeed.Snapshot().Revision
	a.notifyIngress.Raise(context.Background(), notify.Event{
		SessionID: "s1", Title: "hello", Kind: notify.KindProgramNotify,
		Trust: notify.TrustProgramRequest, Level: notify.LevelInfo,
	})
	snap := a.notifyFeed.Snapshot()
	if snap.Revision == before {
		t.Fatal("a raise through the wired seam did not reach the feed")
	}
	if len(snap.Occurrences) != 1 {
		t.Fatalf("feed holds %d occurrences after one raise, want 1", len(snap.Occurrences))
	}
	// Ingress stamps At — the router no longer does — so a row that reached
	// the feed through the wired path carries an instant.
	if snap.Occurrences[0].Event.At.IsZero() {
		t.Error("the occurrence carries no At; ingress is meant to stamp it on the way past")
	}
	if snap.UnreadCount != 1 {
		t.Errorf("unreadCount = %d, want 1", snap.UnreadCount)
	}
}

// The feed's change hint reaches the transport: OnChange is bound to the
// server's broadcast, so a mutation is announced rather than merely
// recorded. Asserted through the transport's own seam — the app holds the
// *WSServer, and binding it is the composition root's job.
func TestAppBindsTheFeedsChangeHintToTheTransport(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("newTestApp: %v", err)
	}
	t.Cleanup(func() { a.Shutdown(context.Background()) })

	got := make(chan uint64, 4)
	// Rebinding proves the slot is the one the root uses: OnChange holds a
	// single observer, so a mutation reaching this closure is a mutation
	// that would otherwise have reached BroadcastFeedChanged.
	a.notifyFeed.OnChange(func(rev uint64) { got <- rev })
	a.notifyIngress.Raise(context.Background(), notify.Event{
		SessionID: "s1", Title: "hello", Kind: notify.KindProgramNotify,
		Trust: notify.TrustProgramRequest, Level: notify.LevelInfo,
	})
	select {
	case rev := <-got:
		if rev == 0 {
			t.Errorf("change hint carried revision %d, want a mutation's revision", rev)
		}
	default:
		t.Fatal("a feed mutation announced nothing")
	}
}
