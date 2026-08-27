package app

// The toast sink through production wiring (nocx-c6ef, plan D2).
//
// The wiring is the thing that can be missing: internal/content shipped a
// whole store whose write path had no caller outside its own tests, with
// every gate green (nocx-rtg0). So these tests reach the sink the way a
// program's OSC 9 does — through the composition root's own ingress — rather
// than by constructing a router of their own.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// errToastRefused is what a bound toast surface reports when it fails.
var errToastRefused = errors.New("apptest: the renderer refused the toast")

// recordingToast is a bound toast surface that records what reached it.
type recordingToast struct {
	mu     sync.Mutex
	events []notify.Event
	err    error
}

func (p *recordingToast) Toast(_ context.Context, ev notify.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, ev)
	return p.err
}

func (p *recordingToast) seen() []notify.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]notify.Event(nil), p.events...)
}

// toastApp builds the composition root with a toast surface bound in place of
// the transport's, and returns the event a program's notify.raise produces.
//
// Binding over the holder is exactly what the holder is for: the ROUTE was
// decided when the table was built and is not reachable from here, and what
// this replaces is the implementation behind the port.
func toastApp(t *testing.T, p notify.ToastPresenter) (*App, notify.Event) {
	t.Helper()
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("newTestApp: %v", err)
	}
	t.Cleanup(func() { a.Shutdown(context.Background()) })
	a.notifyToast.Set(p)

	return a, notify.Event{
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
}

// A program's notification reaches the toast through the composition root's
// own table. This is the "is it wired" assertion: before this row existed the
// whole toast path was reachable from its own tests and nowhere else.
func TestAppRoutesAProgramNotificationToTheToast(t *testing.T) {
	p := &recordingToast{}
	a, ev := toastApp(t, p)

	if out := a.notifyIngress.Raise(context.Background(), ev); out.Err != nil {
		t.Fatalf("the raise was refused before acceptance: %v", out.Err)
	}

	seen := p.seen()
	if len(seen) != 1 {
		t.Fatalf("the toast saw %d events, want 1", len(seen))
	}
	if seen[0].Title != ev.Title || seen[0].Body != ev.Body {
		t.Errorf("the toast saw %q/%q, want the event's title and body", seen[0].Title, seen[0].Body)
	}
}

// A session that ended is attested and reaches the toast too — the same
// reason it reaches the banner: the user is not looking at the tab, which is
// the only moment either event matters.
func TestAppRoutesASessionEndedToTheToast(t *testing.T) {
	p := &recordingToast{}
	a, _ := toastApp(t, p)

	a.notifyIngress.Raise(context.Background(), notify.Event{
		SessionID: "s1", Title: "session ended", Body: "exit status 0",
		Kind: notify.KindSessionEnded, Trust: notify.TrustAttested, Level: notify.LevelInfo,
	})

	if seen := p.seen(); len(seen) != 1 {
		t.Fatalf("the toast saw %d attested events, want 1", len(seen))
	}
}

// A toast that fails after acceptance produces EXACTLY ONE row, naming the
// channel by the router's own word for it and the reason. One, because a
// failure row may never itself produce a failure row (plan D3) — it is
// admitted to the feed directly rather than raised back through the pipeline
// that just failed.
func TestAppRecordsAFailedToastAsExactlyOneFeedRow(t *testing.T) {
	p := &recordingToast{err: errToastRefused}
	a, ev := toastApp(t, p)

	if out := a.notifyIngress.Raise(context.Background(), ev); out.Err != nil {
		t.Fatalf("the raise was refused before acceptance: %v", out.Err)
	}

	// One toast invocation: for the notification. The failure row never
	// resolved a route, so there was no second delivery to fail.
	if got := len(p.seen()); got != 1 {
		t.Fatalf("the toast was asked %d times, want exactly 1 — the failure row went back through the router", got)
	}
	snap := a.notifyFeed.Snapshot()
	if len(snap.Occurrences) != 2 {
		t.Fatalf("feed holds %d occurrences, want exactly 2 (the notification and its one failure)", len(snap.Occurrences))
	}
	row := snap.Occurrences[0].Event // newest first
	if !strings.Contains(row.Body, notifyToastTarget) {
		t.Errorf("failure row body %q does not name the toast channel", row.Body)
	}
	if !strings.Contains(row.Body, errToastRefused.Error()) {
		t.Errorf("failure row body %q does not name the reason", row.Body)
	}
	if row.SessionID != ev.SessionID || row.Attribution != ev.Attribution {
		t.Errorf("failure row attributed to %+v/%q, want the original %+v/%q",
			row.Attribution, row.SessionID, ev.Attribution, ev.SessionID)
	}
	if snap.Occurrences[0].Count != 1 {
		t.Fatalf("the failure row collapsed %d occurrences, want 1", snap.Occurrences[0].Count)
	}
}

// The host no renderer is attached to: the port reports unavailable, the
// delivery is a VISIBLE failed delivery in the router's outcome, and it
// writes no feed row — the same exemption the banner has on a
// host with no desktop surface, for the same reason. A row per notification
// saying "nobody is looking" is noise the user cannot act on, beside a
// notification that IS in the feed.
func TestAppUnavailableToastWritesNoRow(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("newTestApp: %v", err)
	}
	t.Cleanup(func() { a.Shutdown(context.Background()) })
	// The transport IS bound here, as it is on every host — and with no
	// renderer attached it reports unavailable, which is the state a
	// never-attached backend lives in.
	out := a.notifyIngress.Raise(context.Background(), notify.Event{
		SessionID: "s1", Title: "build finished",
		Kind: notify.KindProgramNotify, Trust: notify.TrustProgramRequest, Level: notify.LevelInfo,
	})
	if out.Err != nil {
		t.Fatalf("the raise was refused before acceptance: %v", out.Err)
	}

	// The failure is visible where it happens — the transport's port reports
	// ErrUnavailable (TestNotifyToast_WithNoRendererAttachedReportsUnavailable)
	// and the router records it in the outcome the result handler reads. What
	// is asserted HERE is the composition root's one decision about it: no
	// row, because nothing was lost and nothing the user does can change it.
	if snap := a.notifyFeed.Snapshot(); len(snap.Occurrences) != 1 {
		t.Fatalf("feed holds %d occurrences, want 1 — an unavailable channel wrote a row", len(snap.Occurrences))
	}
}
