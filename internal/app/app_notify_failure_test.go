package app

// A delivery that fails AFTER notify.raise answered reaches the feed
// (nocx-r6pxp, plan D3).
//
// notify.raise answers {} at acceptance, not at delivery: under the policy
// the event may sit in a debounce window and be delivered when it closes,
// seconds after the RPC returned. A failure there used to reach a
// logger.Warn and nothing else — the soft degrade visible only in a log that
// AGENTS.md condemns. These tests watch it through the wired seam, because
// the wiring is the thing that can be missing: internal/content shipped a
// whole store whose write path had no caller outside its own tests, with
// every gate green (nocx-rtg0).

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// errBannerRefused is what the bound attention surface reports.
var errBannerRefused = errors.New("apptest: the banner refused it")

// failingHost is an attention surface that fails every banner and counts how
// many it was asked for. The count is the recursion assertion: the row
// recording a failed delivery must never be handed back to the channel that
// just failed.
type failingHost struct {
	mu      sync.Mutex
	banners int
}

func (h *failingHost) Banner(context.Context, notify.Event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.banners++
	return errBannerRefused
}

func (h *failingHost) Badge(context.Context, int) error { return nil }

func (h *failingHost) Bounce(context.Context) error { return nil }

func (h *failingHost) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.banners
}

// notifyFailureApp builds the composition root with a banner that always
// fails, and returns the event a program's notify.raise would produce.
func notifyFailureApp(t *testing.T) (*App, *failingHost, notify.Event) {
	t.Helper()
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("newTestApp: %v", err)
	}
	t.Cleanup(func() { a.Shutdown(context.Background()) })

	host := &failingHost{}
	a.SetAttentionHost(host)

	// What ws_notify.go stamps for a program's OSC 9: addressing plus the
	// attribution the registry supplied.
	ev := notify.Event{
		SessionID: "s1",
		Title:     "build finished",
		Body:      "42 targets",
		Kind:      notify.KindProgramNotify,
		Trust:     notify.TrustProgramRequest,
		Level:     notify.LevelInfo,
		Attribution: notify.Attribution{
			Backend: "local", Tab: "7", Host: "build-01", Session: "s1",
		},
	}
	return a, host, ev
}

func TestAppRecordsAFailedDeliveryAsAFeedRow(t *testing.T) {
	a, host, ev := notifyFailureApp(t)

	out := a.notifyIngress.Raise(context.Background(), ev)
	if out.Err != nil {
		t.Fatalf("the raise was refused before acceptance: %v", out.Err)
	}
	if host.count() != 1 {
		t.Fatalf("the banner was asked %d times, want 1", host.count())
	}

	snap := a.notifyFeed.Snapshot()
	if len(snap.Occurrences) != 2 {
		t.Fatalf("feed holds %d occurrences, want 2 (the notification and its failure)", len(snap.Occurrences))
	}
	row := snap.Occurrences[0].Event // newest first
	if !strings.Contains(row.Title, "build finished") {
		t.Errorf("failure row title %q does not name the notification that failed", row.Title)
	}
	// Which channel: the router's own word for the destination, and why.
	if !strings.Contains(row.Body, "banner") {
		t.Errorf("failure row body %q does not name the channel", row.Body)
	}
	if !strings.Contains(row.Body, errBannerRefused.Error()) {
		t.Errorf("failure row body %q does not name the reason", row.Body)
	}
	// Beside the notification it is about: the renderer resolves a row to a
	// pane by (backend, session), so a failure with no attribution is a row
	// nobody can act on.
	if row.SessionID != ev.SessionID || row.Attribution != ev.Attribution {
		t.Errorf("failure row attributed to %+v/%q, want the original %+v/%q",
			row.Attribution, row.SessionID, ev.Attribution, ev.SessionID)
	}
	if row.Level != notify.LevelWarning || row.Trust != notify.TrustAttested {
		t.Errorf("failure row is %q/%q, want warning/attested", row.Level, row.Trust)
	}
	if snap.UnreadCount != 2 {
		t.Errorf("unreadCount = %d, want 2", snap.UnreadCount)
	}
}

// TestAppDoesNotDeliverTheFailureRowThroughTheChannelThatFailed is the
// recursion bound, tested the way D3 asks for it: by failing the sink that
// would have carried the failure row. One broken sink must not become an
// unbounded feed of complaints about being broken.
func TestAppDoesNotDeliverTheFailureRowThroughTheChannelThatFailed(t *testing.T) {
	a, host, ev := notifyFailureApp(t)

	a.notifyIngress.Raise(context.Background(), ev)

	// The banner was invoked once: for the notification. The failure row was
	// admitted to the feed directly and never resolved a route, so there was
	// no second delivery to fail.
	if got := host.count(); got != 1 {
		t.Fatalf("the banner was asked %d times, want exactly 1 — the failure row went back through the router", got)
	}
	snap := a.notifyFeed.Snapshot()
	if len(snap.Occurrences) != 2 {
		t.Fatalf("feed holds %d occurrences, want exactly 2 — a failure row produced a failure row", len(snap.Occurrences))
	}
	if snap.Occurrences[0].Count != 1 {
		t.Fatalf("the failure row collapsed %d occurrences, want 1", snap.Occurrences[0].Count)
	}
}

// TestAppRaiseThatFailsBeforeAcceptanceStillFailsTheCaller: the path this
// task must leave alone. A raise refused before acceptance answers the caller
// with an error — which the transport turns into a JSON-RPC error — and
// writes nothing to the feed, because nothing was ever accepted to fail.
func TestAppRaiseThatFailsBeforeAcceptanceStillFailsTheCaller(t *testing.T) {
	a, host, ev := notifyFailureApp(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := a.notifyIngress.Raise(ctx, ev)
	if !errors.Is(out.Err, context.Canceled) {
		t.Fatalf("outcome err = %v, want the caller's cancellation", out.Err)
	}
	if snap := a.notifyFeed.Snapshot(); len(snap.Occurrences) != 0 {
		t.Fatalf("feed holds %d occurrences after a pre-acceptance failure, want 0", len(snap.Occurrences))
	}
	if got := host.count(); got != 0 {
		t.Fatalf("the banner was asked %d times, want 0", got)
	}
}

// TestAppRecordsNoRowForAChannelThatDoesNotExistOnThisHost: the one failure
// that gets no row. A host with no desktop attention surface — cmd/devharness,
// the dev-web harness, an e2e run — reports ErrUnavailable for EVERY raise,
// and a row per notification would repeat "this build has no banner" beside
// every notification the feed already holds. Nothing was lost and nothing the
// user does can change it, so it stays a router outcome and a log line.
//
// The paired positive is the test above: a bound host that FAILS does get its
// row, so this exemption is about the channel not existing, never about
// hiding a failure.
func TestAppRecordsNoRowForAChannelThatDoesNotExistOnThisHost(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("newTestApp: %v", err)
	}
	t.Cleanup(func() { a.Shutdown(context.Background()) })
	// No SetAttentionHost: the holder answers UnavailableHost, exactly as it
	// does on every host that never binds one.

	a.notifyIngress.Raise(context.Background(), notify.Event{
		SessionID: "s1", Title: "build finished",
		Kind: notify.KindProgramNotify, Trust: notify.TrustProgramRequest, Level: notify.LevelInfo,
	})

	snap := a.notifyFeed.Snapshot()
	if len(snap.Occurrences) != 1 {
		t.Fatalf("feed holds %d occurrences, want 1 — an unavailable channel wrote a row", len(snap.Occurrences))
	}
	if snap.Occurrences[0].Event.Title != "build finished" {
		t.Fatalf("the held row is %q, want the notification itself", snap.Occurrences[0].Event.Title)
	}
}
