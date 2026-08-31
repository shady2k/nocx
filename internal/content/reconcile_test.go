package content_test

// Restart reconciliation (nocx-k6p18.5, the content-store reconciliation
// design; level-1 D5): `Open` stops judging, and the three verdicts are
// applied afterwards by whoever could actually ask.
//
// These tests are written against the REAL `Open`, because that is what the
// bead's first assertion is about: before this change a fresh coordinator
// deleted the sessions row of a running session, deleted its recording,
// nulled the session_id of every entry naming it and closed its open entry
// as `unknown` — all four, in one start, and every one of them was correct
// while a session could not outlive the process that opened it.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
)

// aLiveSessionsWork seeds the state a coordinator replacement finds: a
// session row, a block anchored to a pane and naming that session, an
// execution that never terminated, and a recording with bytes in it. It
// returns the file path, so the caller can reopen the store the way a
// replacing coordinator does.
func aLiveSessionsWork(t *testing.T, sessionID, entryID string) (string, []byte) {
	t.Helper()
	ctx := context.Background()
	db, led, path := newLedgerAt(t)
	aPaneUnder(t, db, "ws-1", "tab-1", "pane-1")
	envReady(t, led, "local")

	if err := led.CreateSession(ctx, content.Session{ID: sessionID, WorkspaceID: "ws-1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: entryID, Client: "test-client", EnvironmentID: "local",
		PaneID: strPtr("pane-1"), SessionID: strPtr(sessionID),
		Cwd: "/repo", Kind: content.EntryShell, Intent: "make build",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if _, err := led.StartExecution(ctx, content.StartExecution{EntryID: entryID}); err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	body := []byte("[  1%] building the thing\r\n")
	if _, err := db.SessionOutput().Append(ctx, content.SessionOutputAppend{
		SessionID: sessionID, Offset: 0, Body: body,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path, body
}

type retentionClock struct {
	now time.Time
}

func (c *retentionClock) Now() time.Time {
	return c.now
}

func reopenStoreWithClock(t *testing.T, path string, clock *retentionClock) (content.ContentDB, error) {
	t.Helper()
	return content.Open(context.Background(), content.Config{
		Path:   path,
		Key:    testKey(),
		Budget: testBudget,
		Clock:  clock.Now,
	})
}

// assertTheWorkSurvived is the four things `Open` used to destroy, checked
// together because they are one fact: this command is still running.
func assertTheWorkSurvived(t *testing.T, db content.ContentDB, sessionID, entryID string, body []byte) {
	t.Helper()
	ctx := context.Background()
	e, err := db.Ledger().Entry(ctx, entryID)
	if err != nil || e == nil {
		t.Fatalf("Entry(%s): %v (nil=%v)", entryID, err, e == nil)
	}
	if e.Phase == content.PhaseClosed {
		t.Fatalf("the open entry was closed as %s/%s — a coordinator replacement declared a "+
			"running command finished", e.Phase, e.Status)
	}
	if e.Status == content.EntryUnknown {
		t.Fatalf("the open entry's status is %s — a running command was judged", e.Status)
	}
	// The sessions row itself: provenance is nulled by the foreign key when
	// the row goes, so a surviving session_id IS the surviving row.
	if e.SessionID == nil || *e.SessionID != sessionID {
		t.Fatalf("session_id = %v, want %q — the sessions row of a live session was deleted",
			e.SessionID, sessionID)
	}
	if e.PaneID == nil || *e.PaneID != "pane-1" {
		t.Fatalf("pane_id = %v, want pane-1 — the anchor is untouched in every branch", e.PaneID)
	}
	rec, err := db.SessionOutput().Read(ctx, sessionID)
	if err != nil {
		t.Fatalf("Read recording: %v", err)
	}
	if len(rec.Runs) != 1 || string(rec.Runs[0].Body) != string(body) {
		t.Fatalf("recording after the replacement = %+v, want the %d bytes it held — deleting a "+
			"recording because a READER went away throws out what the promise exists to keep",
			rec.Runs, len(body))
	}
}

// Assertion 1 and 2 of the design, against the real `Open`.
func TestOpenJudgesNoSession(t *testing.T) {
	const sessionID = "session-that-is-still-running"
	const entryID = "00000000-0000-7000-8000-00000000e001"
	path, body := aLiveSessionsWork(t, sessionID, entryID)

	again, err := reopenStore(t, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = again.Close() }()

	assertTheWorkSurvived(t, again, sessionID, entryID, body)
}

// …and the same state survives the LIVE verdict, which is the whole of what
// `live` does: it clears the mark and writes nothing.
func TestTheLiveVerdictKeepsEverything(t *testing.T) {
	const sessionID = "session-that-is-still-running"
	const entryID = "00000000-0000-7000-8000-00000000e002"
	path, body := aLiveSessionsWork(t, sessionID, entryID)
	ctx := context.Background()

	again, err := reopenStore(t, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = again.Close() }()

	rec := again.Reconcile()
	pending, err := rec.Pending(ctx)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 || pending[0].SessionID != sessionID {
		t.Fatalf("pending = %+v, want the one session carried over", pending)
	}
	if pending[0].Cause != content.CauseNotYetAsked {
		t.Fatalf("cause before anybody was asked = %q, want %q", pending[0].Cause, content.CauseNotYetAsked)
	}
	if applyErr := rec.Apply(ctx, content.SessionJudgement{
		SessionID: sessionID, Verdict: content.VerdictLive,
	}); applyErr != nil {
		t.Fatalf("Apply(live): %v", applyErr)
	}
	assertTheWorkSurvived(t, again, sessionID, entryID, body)

	// A judged session leaves the pending set: it is reconciled, and the
	// product has nothing left to say about it.
	after, err := rec.Pending(ctx)
	if err != nil {
		t.Fatalf("Pending after live: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("pending after the live verdict = %+v, want none", after)
	}
}

// Assertion 3: `absent` is exactly what `Open` used to do, for that session
// alone.
func TestTheAbsentVerdictSweepsThatSessionAlone(t *testing.T) {
	const goneID = "session-the-host-does-not-report"
	const entryID = "00000000-0000-7000-8000-00000000e003"
	path, _ := aLiveSessionsWork(t, goneID, entryID)
	ctx := context.Background()

	again, err := reopenStore(t, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = again.Close() }()

	if applyErr := again.Reconcile().Apply(ctx, content.SessionJudgement{
		SessionID: goneID, Verdict: content.VerdictAbsent,
	}); applyErr != nil {
		t.Fatalf("Apply(absent): %v", applyErr)
	}

	e, err := again.Ledger().Entry(ctx, entryID)
	if err != nil || e == nil {
		t.Fatalf("Entry: %v (nil=%v)", err, e == nil)
	}
	if e.Phase != content.PhaseClosed || e.Status != content.EntryUnknown {
		t.Fatalf("entry after absent = %s/%s, want closed/unknown", e.Phase, e.Status)
	}
	if e.SessionID != nil {
		t.Fatalf("session_id after absent = %v, want nil — the pipe is gone and provenance ends", e.SessionID)
	}
	if e.PaneID == nil || *e.PaneID != "pane-1" {
		t.Fatalf("pane_id after absent = %v, want pane-1 — the anchor survives every branch", e.PaneID)
	}
	rec, err := again.SessionOutput().Read(ctx, goneID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(rec.Runs) != 0 || rec.Bytes != 0 {
		t.Fatalf("recording after absent = %+v, want nothing — the absent path restores the old bound", rec)
	}
}

// A session THIS incarnation opened is not reconcilable at all: it was never
// carried over, so no verdict may be applied to it. Without this, a stale
// verdict arriving after a session id was reused would delete live work.
func TestAVerdictOnASessionNobodyCarriedOverIsRefused(t *testing.T) {
	db, _ := newTestStore(t)
	err := db.Reconcile().Apply(context.Background(), content.SessionJudgement{
		SessionID: "a-session-of-this-incarnation", Verdict: content.VerdictAbsent,
	})
	if !errors.Is(err, content.ErrNotPending) {
		t.Fatalf("Apply on a session nobody carried over = %v, want ErrNotPending", err)
	}
}

// Assertion 5, per failure mode: a refused connection, a timeout, a sealed
// vault and an unreachable host each leave the session pending, and none of
// them is a verdict.
func TestNoFailureModeProducesAbsent(t *testing.T) {
	const sessionID = "session-on-a-host-nobody-could-reach"
	const entryID = "00000000-0000-7000-8000-00000000e004"
	path, body := aLiveSessionsWork(t, sessionID, entryID)
	ctx := context.Background()

	again, err := reopenStore(t, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = again.Close() }()
	rec := again.Reconcile()

	for _, cause := range []content.UnreconciledCause{
		content.CauseConnectionRefused,
		content.CauseTimedOut,
		content.CauseVaultSealed,
		content.CauseHostUnreachable,
	} {
		if err := rec.Apply(ctx, content.SessionJudgement{
			SessionID: sessionID, Verdict: content.VerdictUnknown,
			Cause: cause, Detail: "dial tcp: " + string(cause),
		}); err != nil {
			t.Fatalf("Apply(unknown, %s): %v", cause, err)
		}
		pending, perr := rec.Pending(ctx)
		if perr != nil {
			t.Fatalf("Pending: %v", perr)
		}
		if len(pending) != 1 || pending[0].SessionID != sessionID {
			t.Fatalf("after %s the session left the pending set: %+v — a failure is not a verdict",
				cause, pending)
		}
		if pending[0].Cause != cause {
			t.Fatalf("cause after %s = %q — the product must be able to say WHY nobody could be asked",
				cause, pending[0].Cause)
		}
		// And nothing was written: the row, the recording, the provenance
		// and the open entry are all exactly as they were.
		assertTheWorkSurvived(t, again, sessionID, entryID, body)
	}
}

// Assertion 6: reconciliation is idempotent and resumable. A refused DELETE
// inside the absent path leaves the session pending and nothing half-judged;
// the next pass completes it.
//
// This replaces TestOpenFailsWhenTheDeadSessionSweepIsRefused, which asserted
// the same property of the sweep that used to run inside `Open`. The property
// did not move: the deletion is still refused, still leaves the file exactly
// as it was, and is still repaired by the next attempt. What moved is WHO
// runs it.
func TestARefusedAbsentSweepLeavesNothingHalfJudged(t *testing.T) {
	const sessionID = "session-whose-sweep-is-refused"
	const entryID = "00000000-0000-7000-8000-00000000e005"
	path, body := aLiveSessionsWork(t, sessionID, entryID)
	ctx := context.Background()

	if err := rawLedger(t, path, hex.EncodeToString(testKey()),
		`CREATE TRIGGER sweep_boom BEFORE DELETE ON sessions
		 BEGIN SELECT RAISE(ABORT, 'sweep refused'); END`,
	); err != nil {
		t.Fatalf("install sweep trigger: %v", err)
	}

	again, err := reopenStore(t, path)
	if err != nil {
		t.Fatalf("reopen: %v — Open judges nothing now, so a refused DELETE cannot stop it", err)
	}
	rec := again.Reconcile()
	if applyErr := rec.Apply(ctx, content.SessionJudgement{
		SessionID: sessionID, Verdict: content.VerdictAbsent,
	}); applyErr == nil {
		t.Fatal("Apply(absent) succeeded while the DELETE was refused")
	}
	// Nothing half-judged: the entry is still open, the recording is still
	// whole, and the session is still pending for the next attempt.
	assertTheWorkSurvived(t, again, sessionID, entryID, body)
	pending, err := rec.Pending(ctx)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending after the refusal = %+v, want the session still awaiting a verdict", pending)
	}
	if closeErr := again.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	// The other end of the interval: with the refusal lifted the next pass
	// completes exactly what the first one could not.
	if dropErr := rawLedger(t, path, hex.EncodeToString(testKey()), `DROP TRIGGER sweep_boom`); dropErr != nil {
		t.Fatalf("remove sweep trigger: %v", dropErr)
	}
	healthy, err := reopenStore(t, path)
	if err != nil {
		t.Fatalf("reopen after the refusal was lifted: %v", err)
	}
	defer func() { _ = healthy.Close() }()
	if applyErr := healthy.Reconcile().Apply(ctx, content.SessionJudgement{
		SessionID: sessionID, Verdict: content.VerdictAbsent,
	}); applyErr != nil {
		t.Fatalf("Apply(absent) on the next pass: %v", applyErr)
	}
	e, err := healthy.Ledger().Entry(ctx, entryID)
	if err != nil || e == nil {
		t.Fatalf("Entry: %v (nil=%v)", err, e == nil)
	}
	if e.Phase != content.PhaseClosed || e.SessionID != nil {
		t.Fatalf("entry after the completing pass = %s/%v, want closed with no provenance", e.Phase, e.SessionID)
	}
	_ = body
}

// Applying the same verdict twice is not an error and does not double-judge:
// an interrupted pass may be re-run wholesale.
func TestApplyingAbsentTwiceIsIdempotent(t *testing.T) {
	const sessionID = "session-judged-twice"
	const entryID = "00000000-0000-7000-8000-00000000e006"
	path, _ := aLiveSessionsWork(t, sessionID, entryID)
	ctx := context.Background()

	again, err := reopenStore(t, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = again.Close() }()

	j := content.SessionJudgement{SessionID: sessionID, Verdict: content.VerdictAbsent}
	if applyErr := again.Reconcile().Apply(ctx, j); applyErr != nil {
		t.Fatalf("Apply(absent): %v", applyErr)
	}
	if applyErr := again.Reconcile().Apply(ctx, j); !errors.Is(applyErr, content.ErrNotPending) {
		t.Fatalf("second Apply(absent) = %v, want ErrNotPending — the session is already reconciled", applyErr)
	}
	e, err := again.Ledger().Entry(ctx, entryID)
	if err != nil || e == nil {
		t.Fatalf("Entry: %v (nil=%v)", err, e == nil)
	}
	if e.Phase != content.PhaseClosed || e.Status != content.EntryUnknown {
		t.Fatalf("entry after two absent verdicts = %s/%s", e.Phase, e.Status)
	}
}

// Assertion 10 and the retention regression: the mark is when this
// incarnation became unable to reach the host, not when the remote command
// started. An eight-day-old command that has been unreconciled for one second
// is still inside the seven-day bound because the replacement just happened.
func TestRetentionMeasuresFromCoordinatorReplacement(t *testing.T) {
	const sessionID = "session-on-a-host-that-was-just-lost"
	const entryID = "00000000-0000-7000-8000-00000000e007"
	const sessionAge = 8 * 24 * time.Hour
	const unreachableAge = time.Second
	const retentionAge = 7 * 24 * time.Hour
	now := time.UnixMilli(1_800_000_000_000)

	path, body := aLiveSessionsWork(t, sessionID, entryID)
	if err := rawLedger(t, path, hex.EncodeToString(testKey()),
		fmt.Sprintf("UPDATE sessions SET started_at = %d", now.Add(-sessionAge).UnixMilli())); err != nil {
		t.Fatalf("age session: %v", err)
	}

	clock := &retentionClock{now: now.Add(-unreachableAge)}
	again, err := reopenStoreWithClock(t, path, clock)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = again.Close() }()

	clock.now = now
	swept, err := again.Reconcile().SweepStale(context.Background(), retentionAge)
	if err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if swept != 0 {
		t.Fatalf("SweepStale removed %d sessions after only %s unreconciled", swept, unreachableAge)
	}
	pending, err := again.Reconcile().Pending(context.Background())
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 || !pending[0].Since.Equal(now.Add(-unreachableAge)) {
		t.Fatalf("pending = %+v, want one session marked at %s", pending, now.Add(-unreachableAge))
	}
	got, err := again.SessionOutput().Read(context.Background(), sessionID)
	if err != nil || len(got.Runs) == 0 || string(got.Runs[0].Body) != string(body) {
		t.Fatalf("recording after one-second outage = %+v (err %v), want the seeded bytes", got, err)
	}
}

// The paired positive: once the mark itself is past the bound, the recording
// is swept, and the block is closed in the same operation so it cannot render
// as running after its session disappears.
func TestRetentionSweepsSessionPastTheMarkAgeAndClosesEntry(t *testing.T) {
	const sessionID = "session-on-a-host-that-never-came-back"
	const entryID = "00000000-0000-7000-8000-00000000e008"
	const retentionAge = 7 * 24 * time.Hour
	now := time.UnixMilli(1_800_000_000_000)

	path, _ := aLiveSessionsWork(t, sessionID, entryID)
	if err := rawLedger(t, path, hex.EncodeToString(testKey()),
		fmt.Sprintf("UPDATE sessions SET started_at = %d", now.Add(-8*24*time.Hour).UnixMilli())); err != nil {
		t.Fatalf("age session: %v", err)
	}

	clock := &retentionClock{now: now.Add(-retentionAge - time.Second)}
	again, err := reopenStoreWithClock(t, path, clock)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = again.Close() }()

	clock.now = now
	swept, err := again.Reconcile().SweepStale(context.Background(), retentionAge)
	if err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if swept != 1 {
		t.Fatalf("SweepStale removed %d sessions, want one past-mark session", swept)
	}
	got, readErr := again.SessionOutput().Read(context.Background(), sessionID)
	if readErr != nil || len(got.Runs) != 0 {
		t.Fatalf("recording after the age bound = %+v (err %v), want nothing", got, readErr)
	}
	e, err := again.Ledger().Entry(context.Background(), entryID)
	if err != nil || e == nil {
		t.Fatalf("Entry after the age bound: %v (nil=%v)", err, e == nil)
	}
	if e.Phase != content.PhaseClosed || e.Status != content.EntryUnknown {
		t.Fatalf("entry after the age bound = %s/%s, want closed/unknown", e.Phase, e.Status)
	}
	if e.SessionID != nil {
		t.Fatalf("session_id after the age bound = %v, want nil", e.SessionID)
	}
	if e.PaneID == nil || *e.PaneID != "pane-1" {
		t.Fatalf("pane_id after the age bound = %v, want pane-1", e.PaneID)
	}
}

// Assertion 12, at the store: the page a pane restores from says the row is
// unreconciled, and says WHY. Without this the same row comes back as `open`
// and the restore path draws a block that claims to be running — the same lie
// the forced close told from the other end.
func TestThePageSaysWhichRowsAreAwaitingAVerdict(t *testing.T) {
	const sessionID = "session-nobody-could-ask-about"
	const entryID = "00000000-0000-7000-8000-00000000e008"
	path, _ := aLiveSessionsWork(t, sessionID, entryID)
	ctx := context.Background()

	again, err := reopenStore(t, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = again.Close() }()
	rec := again.Reconcile()

	page := entriesForPane(t, again.Ledger(), "pane-1")
	if len(page.Entries) != 1 {
		t.Fatalf("page = %+v, want the one block", page.Entries)
	}
	row := page.Entries[0]
	if row.Unreconciled == nil {
		t.Fatal("the page does not say the row is unreconciled — a restored block would claim to be running")
	}
	if *row.Unreconciled != content.CauseNotYetAsked {
		t.Fatalf("cause on the row = %q, want %q", *row.Unreconciled, content.CauseNotYetAsked)
	}

	// The cause is the one the last attempt recorded, so the sentence a person
	// reads follows what actually happened.
	if applyErr := rec.Apply(ctx, content.SessionJudgement{
		SessionID: sessionID, Verdict: content.VerdictUnknown, Cause: content.CauseVaultSealed,
	}); applyErr != nil {
		t.Fatalf("Apply(unknown): %v", applyErr)
	}
	row = entriesForPane(t, again.Ledger(), "pane-1").Entries[0]
	if row.Unreconciled == nil || *row.Unreconciled != content.CauseVaultSealed {
		t.Fatalf("cause after the vault refused = %v, want %q", row.Unreconciled, content.CauseVaultSealed)
	}

	// And a judged session leaves nothing on the row: live means the block is
	// running, and the mark would contradict it.
	if applyErr := rec.Apply(ctx, content.SessionJudgement{
		SessionID: sessionID, Verdict: content.VerdictLive,
	}); applyErr != nil {
		t.Fatalf("Apply(live): %v", applyErr)
	}
	row = entriesForPane(t, again.Ledger(), "pane-1").Entries[0]
	if row.Unreconciled != nil {
		t.Fatalf("a reconciled row is still marked: %v", *row.Unreconciled)
	}
}
