package content_test

// Skip: a recording resumes across a hole nobody recorded (nocx-k6p18.2).
//
// Two kinds of hole exist once a recorder can be ABSENT, and the product may
// not tell them to a user in the same words:
//
//   - evicted — ingested, then dropped to stay inside the per-command cap.
//     The bytes existed here and the bound took them.
//   - never ingested — nothing was there to offer them. The cap never
//     touched them, so naming the cap would be a false statement.
//
// The clause these tests exist for is the last one of Skip's contract: the
// recording is still APPENDABLE afterwards. Without it one missed second
// costs the whole session, because the recorder stops for good the moment it
// loses its place.

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// findGap returns the gap covering [start,end) exactly, or nil.
func findGap(rec content.SessionOutputRecording, start, end int64) *content.Gap {
	for i := range rec.Gaps {
		if rec.Gaps[i].Start == start && rec.Gaps[i].End == end {
			return &rec.Gaps[i]
		}
	}
	return nil
}

// A recording survives a hole nobody recorded, and goes on recording.
//
// The interval, both ends named: from the Skip that advances the cursor
// until the recording is deleted, the range [was, resumeAt) is reported as a
// gap with its own reason and every append at or after resumeAt is accepted.
func TestSessionOutputSkipRecordsAHoleAndTheRecordingStaysAppendable(t *testing.T) {
	db, _ := newRecordingStore(t, content.NewPolicy())
	repo := db.SessionOutput()
	ctx := context.Background()
	sid := "s-skip-resumes"

	head := streamOf(4096)
	appendAll(t, repo, sid, head, 1024)

	res, err := repo.Skip(ctx, sid, 9000, content.GapReasonUnrecorded)
	if err != nil {
		t.Fatalf("Skip: %v", err)
	}
	if !res.Kept {
		t.Fatal("Skip was refused while retention is on")
	}

	// THE CLAUSE THAT MATTERS: the recording is still appendable.
	tail := streamOf(2048)
	for at := 0; at < len(tail); at += 512 {
		if _, appendErr := repo.Append(ctx, content.SessionOutputAppend{
			SessionID: sid,
			Offset:    uint64(9000 + at), //nolint:gosec // a test's own offsets
			Body:      tail[at : at+512],
		}); appendErr != nil {
			t.Fatalf("Append at %d after a skip: %v", 9000+at, appendErr)
		}
	}

	rec, err := repo.Read(ctx, sid)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rec.Produced != 9000+2048 {
		t.Fatalf("Produced = %d, want %d: the cursor did not advance across the hole", rec.Produced, 9000+2048)
	}
	g := findGap(rec, 4096, 9000)
	if g == nil {
		t.Fatalf("no gap at [4096,9000): gaps = %+v", rec.Gaps)
	}
	if g.Reason != content.GapReasonUnrecorded {
		t.Fatalf("gap reason = %q, want %q: the cap never had these bytes", g.Reason, content.GapReasonUnrecorded)
	}
	if len(rec.Runs) != 2 {
		t.Fatalf("runs = %d, want 2 (head and resumed tail): %+v", len(rec.Runs), rec.Runs)
	}
	if rec.Runs[1].Offset != 9000 {
		t.Fatalf("the resumed run starts at %d, want 9000", rec.Runs[1].Offset)
	}
}

// A skip before anything was ever recorded is measured from the start of the
// stream: offsets are the session's own, so a recording that begins at 500
// is missing [0,500) and says so.
func TestSessionOutputSkipBeforeAnyAppendMeasuresFromZero(t *testing.T) {
	db, _ := newRecordingStore(t, content.NewPolicy())
	repo := db.SessionOutput()
	ctx := context.Background()
	sid := "s-skip-first"

	if _, err := repo.Skip(ctx, sid, 500, content.GapReasonUnrecorded); err != nil {
		t.Fatalf("Skip: %v", err)
	}
	body := streamOf(256)
	if _, err := repo.Append(ctx, content.SessionOutputAppend{SessionID: sid, Offset: 500, Body: body}); err != nil {
		t.Fatalf("Append after a first-thing skip: %v", err)
	}

	rec, err := repo.Read(ctx, sid)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rec.Produced != 756 {
		t.Fatalf("Produced = %d, want 756", rec.Produced)
	}
	g := findGap(rec, 0, 500)
	if g == nil || g.Reason != content.GapReasonUnrecorded {
		t.Fatalf("want an unrecorded gap at [0,500): gaps = %+v", rec.Gaps)
	}
	if len(rec.Runs) != 1 || rec.Runs[0].Offset != 500 {
		t.Fatalf("runs = %+v, want one starting at 500", rec.Runs)
	}
}

// The two reasons are two values, and one recording can carry both without
// either range claiming the other's bytes.
func TestSessionOutputCapAndSkipAreDifferentReasons(t *testing.T) {
	policy := content.NewPolicy()
	policy.SetOutputCapBytes(64 << 10)
	db, _ := newRecordingStore(t, policy)
	repo := db.SessionOutput()
	ctx := context.Background()

	// A cap eviction on its own is still `cap`.
	capped := "s-capped"
	appendAll(t, repo, capped, streamOf(256<<10), 8<<10)
	rec, err := repo.Read(ctx, capped)
	if err != nil {
		t.Fatalf("Read(capped): %v", err)
	}
	if len(rec.Gaps) != 1 || rec.Gaps[0].Reason != content.GapReasonCap {
		t.Fatalf("a cap eviction must still be reported as %q: gaps = %+v", content.GapReasonCap, rec.Gaps)
	}
	if rec.Truncated == nil || *rec.Truncated != content.TruncCap {
		t.Fatalf("Truncated = %v, want cap", rec.Truncated)
	}

	// A recording holding both kinds reports both, and the ranges do not
	// overlap: bytes nobody ingested cannot also have been evicted.
	both := "s-both"
	appendAll(t, repo, both, streamOf(256<<10), 8<<10)
	if _, skipErr := repo.Skip(ctx, both, (256<<10)+5000, content.GapReasonUnrecorded); skipErr != nil {
		t.Fatalf("Skip: %v", skipErr)
	}
	if _, appendErr := repo.Append(ctx, content.SessionOutputAppend{
		SessionID: both, Offset: (256 << 10) + 5000, Body: streamOf(4096),
	}); appendErr != nil {
		t.Fatalf("Append after skip: %v", appendErr)
	}
	rec, err = repo.Read(ctx, both)
	if err != nil {
		t.Fatalf("Read(both): %v", err)
	}
	var sawCap, sawUnrecorded bool
	var prevEnd int64
	for _, g := range rec.Gaps {
		if g.Start < prevEnd {
			t.Fatalf("gaps overlap: %+v", rec.Gaps)
		}
		prevEnd = g.End
		switch g.Reason {
		case content.GapReasonCap:
			sawCap = true
		case content.GapReasonUnrecorded:
			sawUnrecorded = true
			if g.Start != 256<<10 || g.End != (256<<10)+5000 {
				t.Fatalf("the never-ingested range is [%d,%d), want [%d,%d)",
					g.Start, g.End, 256<<10, (256<<10)+5000)
			}
		default:
			t.Fatalf("unknown gap reason %q in %+v", g.Reason, rec.Gaps)
		}
	}
	if !sawCap || !sawUnrecorded {
		t.Fatalf("want both a cap gap and an unrecorded gap: %+v", rec.Gaps)
	}
}

// A recording holed only by a skip is not truncated by the cap: `Truncated`
// says `gap` — a range was lost — and never names the bound that did not act.
func TestSessionOutputSkipOnlyRecordingIsNotTruncatedByTheCap(t *testing.T) {
	db, _ := newRecordingStore(t, content.NewPolicy())
	repo := db.SessionOutput()
	ctx := context.Background()
	sid := "s-skip-trunc"

	appendAll(t, repo, sid, streamOf(1024), 1024)
	if _, err := repo.Skip(ctx, sid, 4096, content.GapReasonUnrecorded); err != nil {
		t.Fatalf("Skip: %v", err)
	}
	rec, err := repo.Read(ctx, sid)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rec.Truncated == nil {
		t.Fatal("a holed recording reports no truncation at all")
	}
	if *rec.Truncated != content.TruncGap {
		t.Fatalf("Truncated = %q, want %q: the cap dropped nothing here", *rec.Truncated, content.TruncGap)
	}
}

// Skipping to where the recording already is is a no-op, not a zero-width
// gap: the recorder retries a call whose error it could not classify, and a
// retry must not add a hole.
func TestSessionOutputSkipToTheCursorIsIdempotent(t *testing.T) {
	db, _ := newRecordingStore(t, content.NewPolicy())
	repo := db.SessionOutput()
	ctx := context.Background()
	sid := "s-skip-idem"

	appendAll(t, repo, sid, streamOf(2048), 1024)
	if _, err := repo.Skip(ctx, sid, 5000, content.GapReasonUnrecorded); err != nil {
		t.Fatalf("Skip: %v", err)
	}
	if _, err := repo.Skip(ctx, sid, 5000, content.GapReasonUnrecorded); err != nil {
		t.Fatalf("Skip repeated: %v", err)
	}
	rec, err := repo.Read(ctx, sid)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(rec.Gaps) != 1 {
		t.Fatalf("a repeated skip made %d gaps: %+v", len(rec.Gaps), rec.Gaps)
	}
	if rec.Produced != 5000 {
		t.Fatalf("Produced = %d, want 5000", rec.Produced)
	}
}

// A skip BEHIND the cursor is the caller defect the old sentence described:
// the caller had those bytes. Nothing moves and nothing is written.
func TestSessionOutputSkipBehindTheCursorIsRefused(t *testing.T) {
	db, _ := newRecordingStore(t, content.NewPolicy())
	repo := db.SessionOutput()
	ctx := context.Background()
	sid := "s-skip-back"

	appendAll(t, repo, sid, streamOf(2048), 1024)
	if _, err := repo.Skip(ctx, sid, 1000, content.GapReasonUnrecorded); !errors.Is(err, content.ErrSessionOutputDiscontinuous) {
		t.Fatalf("Skip behind the cursor: err = %v, want ErrSessionOutputDiscontinuous", err)
	}
	rec, err := repo.Read(ctx, sid)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rec.Produced != 2048 || len(rec.Gaps) != 0 {
		t.Fatalf("a refused skip changed the recording: produced=%d gaps=%+v", rec.Produced, rec.Gaps)
	}
}

// A hole with no reason is what makes a reader guess, so the store refuses
// to create one.
func TestSessionOutputSkipWithNoReasonIsRefused(t *testing.T) {
	db, _ := newRecordingStore(t, content.NewPolicy())
	repo := db.SessionOutput()
	if _, err := repo.Skip(context.Background(), "s-skip-noreason", 100, ""); err == nil {
		t.Fatal("Skip accepted a hole with no reason")
	}
	if _, err := repo.Skip(context.Background(), "", 100, content.GapReasonUnrecorded); err == nil {
		t.Fatal("Skip accepted an empty session id")
	}
}

// Retention off refuses a skip the way it refuses an append — an ANSWER, not
// a failure. Nothing is durable, so there is no recording to put a hole in.
func TestSessionOutputSkipRefusedWhenRetentionIsOff(t *testing.T) {
	policy := content.NewPolicy()
	policy.SetOutputEnabled(false)
	db, _ := newRecordingStore(t, policy)
	repo := db.SessionOutput()
	ctx := context.Background()

	res, err := repo.Skip(ctx, "s-skip-off", 500, content.GapReasonUnrecorded)
	if err != nil {
		t.Fatalf("Skip with retention off: %v", err)
	}
	if res.Kept {
		t.Fatal("Skip claims to have kept a hole while retention is off")
	}
	rec, err := repo.Read(ctx, "s-skip-off")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rec.Produced != 0 || len(rec.Gaps) != 0 {
		t.Fatalf("a refused skip wrote something: %+v", rec)
	}
}

// The paired failure path: the store is the external call, and when it is
// gone Skip reports it rather than pretending the hole was recorded.
func TestSessionOutputSkipReportsAClosedStore(t *testing.T) {
	db, _ := newRecordingStore(t, content.NewPolicy())
	repo := db.SessionOutput()
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := repo.Skip(context.Background(), "s-skip-closed", 100, content.GapReasonUnrecorded); err == nil {
		t.Fatal("Skip succeeded against a closed store")
	}
}
