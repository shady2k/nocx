package content_test

// Session output recording (nocx-22k1c.1) — the backend's own durable sink
// for the bytes a session produced while nothing was attached.
//
// The properties this file carries, in the order they matter:
//
//   - what was produced is on disk, and it is on disk AT ITS OFFSETS, so a
//     recording can be checked against what a client received by coordinate
//     rather than by eye;
//   - exceeding the cap drops the oldest bytes, says so, and never fails a
//     write;
//   - retention off keeps nothing, says which switch did it, and is not an
//     error;
//   - every external call has a test where it fails, paired with one where
//     it succeeds.

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
)

// newRecordingStore opens a store whose History policy the test owns —
// newTestStore takes the defaults, and every property below is a statement
// about a policy.
func newRecordingStore(t *testing.T, policy *content.Policy) (content.ContentDB, string) {
	t.Helper()
	dir := t.TempDir()
	db := openRecordingStore(t, dir, policy)
	return db, dir
}

func openRecordingStore(t *testing.T, dir string, policy *content.Policy) content.ContentDB {
	t.Helper()
	db, err := content.Open(context.Background(), content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    testKey(),
		Budget: testBudget,
		Policy: policy,
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// streamOf is a deterministic byte stream of n bytes. Deliberately not
// constant bytes: an off-by-one in the offset arithmetic is invisible in a
// run of zeroes.
func streamOf(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i*7 + i/251)
	}
	return out
}

// appendAll writes the whole stream through the recorder in the sizes the
// output pump actually produces, returning the total dropped.
func appendAll(t *testing.T, repo content.SessionOutputRepository, sid string, stream []byte, chunk int) uint64 {
	t.Helper()
	var dropped uint64
	for at := 0; at < len(stream); at += chunk {
		end := at + chunk
		if end > len(stream) {
			end = len(stream)
		}
		res, err := repo.Append(context.Background(), content.SessionOutputAppend{
			SessionID: sid,
			Offset:    uint64(at), //nolint:gosec // a test's own offsets, never negative
			Body:      stream[at:end],
		})
		if err != nil {
			t.Fatalf("Append at %d: %v", at, err)
		}
		if !res.Kept {
			t.Fatalf("Append at %d was not kept, and retention is on", at)
		}
		dropped += res.Dropped
	}
	return dropped
}

// assertMatchesStreamByOffset is THE check the acceptance asks for: every
// kept run is compared against the source AT ITS OFFSET. A recording that
// held the right bytes at the wrong coordinates would pass an eyeball and
// fail here.
func assertMatchesStreamByOffset(t *testing.T, rec content.SessionOutputRecording, stream []byte) {
	t.Helper()
	if len(rec.Runs) == 0 {
		t.Fatal("the recording holds no runs at all")
	}
	var prevEnd uint64
	for i, run := range rec.Runs {
		if run.Offset < prevEnd {
			t.Fatalf("run %d starts at %d, inside the run before it (ends %d)", i, run.Offset, prevEnd)
		}
		end := run.Offset + uint64(len(run.Body))
		if end > uint64(len(stream)) {
			t.Fatalf("run %d covers [%d,%d), past the %d bytes produced", i, run.Offset, end, len(stream))
		}
		if !bytes.Equal(run.Body, stream[run.Offset:end]) {
			t.Fatalf("run %d at offset %d does not match what was produced there", i, run.Offset)
		}
		prevEnd = end
	}
}

// The happy path, and the one AGENTS.md asks for beside every failure: on an
// ordinary store an ordinary session's output is kept whole, at its offsets.
func TestSessionOutput_KeepsWhatWasProducedAtItsOffsets(t *testing.T) {
	db, _ := newRecordingStore(t, content.NewPolicy())
	repo := db.SessionOutput()
	stream := streamOf(70 << 10)

	if dropped := appendAll(t, repo, "sess-1", stream, 32<<10); dropped != 0 {
		t.Fatalf("dropped %d bytes inside the cap", dropped)
	}

	rec, err := repo.Read(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rec.Bytes != uint64(len(stream)) {
		t.Errorf("kept %d bytes, produced %d", rec.Bytes, len(stream))
	}
	if rec.Produced != uint64(len(stream)) {
		t.Errorf("Produced = %d, want %d", rec.Produced, len(stream))
	}
	if rec.Truncated != nil {
		t.Errorf("Truncated = %v on a recording inside its cap", *rec.Truncated)
	}
	if len(rec.Gaps) != 0 {
		t.Errorf("Gaps = %v on a recording that lost nothing", rec.Gaps)
	}
	if len(rec.Runs) != 1 {
		t.Errorf("a whole recording is one run, got %d", len(rec.Runs))
	}
	assertMatchesStreamByOffset(t, rec, stream)
}

// Two sessions do not become one recording. The offsets are per-stream, so a
// store that keyed on anything less would splice one session's bytes into
// another's at plausible-looking coordinates.
func TestSessionOutput_RecordingsAreOnePerSession(t *testing.T) {
	db, _ := newRecordingStore(t, content.NewPolicy())
	repo := db.SessionOutput()
	a, b := streamOf(4096), streamOf(2048)
	for i := range b {
		b[i] ^= 0xff
	}
	appendAll(t, repo, "sess-a", a, 1024)
	appendAll(t, repo, "sess-b", b, 1024)

	recA, err := repo.Read(context.Background(), "sess-a")
	if err != nil {
		t.Fatalf("Read a: %v", err)
	}
	recB, err := repo.Read(context.Background(), "sess-b")
	if err != nil {
		t.Fatalf("Read b: %v", err)
	}
	assertMatchesStreamByOffset(t, recA, a)
	assertMatchesStreamByOffset(t, recB, b)
	if recA.Bytes != uint64(len(a)) || recB.Bytes != uint64(len(b)) {
		t.Fatalf("kept %d and %d bytes, produced %d and %d", recA.Bytes, recB.Bytes, len(a), len(b))
	}
}

// The bound: exceeding it drops the OLDEST droppable bytes, keeps the head,
// says so in the recording, and never fails a write.
func TestSessionOutput_ExceedingTheCapDropsOldestFirstAndSaysSo(t *testing.T) {
	policy := content.NewPolicy()
	const capBytes = 64 << 10
	policy.SetOutputCapBytes(capBytes)
	db, _ := newRecordingStore(t, policy)
	repo := db.SessionOutput()

	stream := streamOf(300 << 10)
	dropped := appendAll(t, repo, "sess-cap", stream, 32<<10)
	if dropped == 0 {
		t.Fatal("300 KiB into a 64 KiB cap dropped nothing")
	}

	rec, err := repo.Read(context.Background(), "sess-cap")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rec.Bytes > capBytes {
		t.Errorf("kept %d bytes, over the %d cap", rec.Bytes, capBytes)
	}
	if rec.Produced != uint64(len(stream)) {
		t.Errorf("Produced = %d, want %d — a hole must stay measurable", rec.Produced, len(stream))
	}
	if rec.Truncated == nil || *rec.Truncated != content.TruncCap {
		t.Errorf("Truncated = %v, want cap — a recording that lost bytes has to say so", rec.Truncated)
	}
	if len(rec.Gaps) != 1 {
		t.Fatalf("Gaps = %v, want exactly one contiguous hole", rec.Gaps)
	}
	if rec.Gaps[0].Reason != "cap" {
		t.Errorf("gap reason = %q, want cap", rec.Gaps[0].Reason)
	}
	// The head survives: the invocation is why the head is reserved.
	if rec.Runs[0].Offset != 0 {
		t.Errorf("the head starts at %d, want 0 — the oldest bytes went first and took the head with them", rec.Runs[0].Offset)
	}
	// The tail survives, and it is the NEWEST bytes: errors live there.
	last := rec.Runs[len(rec.Runs)-1]
	if got := last.Offset + uint64(len(last.Body)); got != uint64(len(stream)) {
		t.Errorf("the recording ends at %d, want %d — the tail is what the cap must never drop", got, len(stream))
	}
	// The hole is exactly between the two, and it is a HOLE: the runs on
	// either side are adjacent to it and not to each other.
	if len(rec.Runs) != 2 {
		t.Fatalf("a capped recording is a head and a tail, got %d runs", len(rec.Runs))
	}
	headEnd := rec.Runs[0].Offset + uint64(len(rec.Runs[0].Body))
	gapStart, gapEnd := uint64(rec.Gaps[0].Start), uint64(rec.Gaps[0].End) //nolint:gosec // byte offsets
	if gapStart != headEnd || gapEnd != rec.Runs[1].Offset {
		t.Errorf("gap %v does not span from the head's end (%d) to the tail's start (%d)",
			rec.Gaps[0], headEnd, rec.Runs[1].Offset)
	}
	assertMatchesStreamByOffset(t, rec, stream)
}

// Retention off: nothing is kept, it is not a failure, and the stance names
// the switch that did it. Two switches, two stances — a person has to know
// which one to flip.
func TestSessionOutput_RetentionOffKeepsNothingAndNamesTheSwitch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		off   func(*content.Policy)
		stanc content.SessionOutputStance
	}{
		{"history off", func(p *content.Policy) { p.SetEnabled(false) }, content.SessionOutputHistoryOff},
		{"output retention off", func(p *content.Policy) { p.SetOutputEnabled(false) }, content.SessionOutputRetentionOff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := content.NewPolicy()
			tc.off(policy)
			db, _ := newRecordingStore(t, policy)
			repo := db.SessionOutput()

			res, err := repo.Append(context.Background(), content.SessionOutputAppend{
				SessionID: "sess-off", Offset: 0, Body: streamOf(4096),
			})
			if err != nil {
				t.Fatalf("Append with retention off is a refusal, not a failure: %v", err)
			}
			if res.Kept {
				t.Error("Kept is true with retention off")
			}
			if got := repo.Stance(); got != tc.stanc {
				t.Errorf("Stance = %q, want %q", got, tc.stanc)
			}
			rec, err := repo.Read(context.Background(), "sess-off")
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if rec.Bytes != 0 || len(rec.Runs) != 0 {
				t.Errorf("kept %d bytes in %d runs with retention off", rec.Bytes, len(rec.Runs))
			}
		})
	}
}

// The stance is read live: History applies without a restart, so an answer
// cached at startup would be wrong by lunchtime. Both ends, in one test.
func TestSessionOutput_StanceFollowsThePolicyLive(t *testing.T) {
	policy := content.NewPolicy()
	db, _ := newRecordingStore(t, policy)
	repo := db.SessionOutput()

	if got := repo.Stance(); got != content.SessionOutputKept {
		t.Fatalf("Stance = %q on a default policy, want kept", got)
	}
	policy.SetOutputEnabled(false)
	if got := repo.Stance(); got != content.SessionOutputRetentionOff {
		t.Fatalf("Stance = %q after the toggle, want outputOff", got)
	}
	policy.SetOutputEnabled(true)
	if got := repo.Stance(); got != content.SessionOutputKept {
		t.Fatalf("Stance = %q after the toggle back, want kept", got)
	}
}

// An append that does not continue the recording is refused. The recorder
// advances its cursor by what it wrote, so this is a caller defect — and a
// hole punched here would break the one property the recording is for.
func TestSessionOutput_DiscontinuousAppendIsRefused(t *testing.T) {
	db, _ := newRecordingStore(t, content.NewPolicy())
	repo := db.SessionOutput()
	ctx := context.Background()

	if _, err := repo.Append(ctx, content.SessionOutputAppend{
		SessionID: "sess-d", Offset: 0, Body: streamOf(1024),
	}); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	// A gap forward.
	if _, err := repo.Append(ctx, content.SessionOutputAppend{
		SessionID: "sess-d", Offset: 2048, Body: streamOf(16),
	}); !errors.Is(err, content.ErrSessionOutputDiscontinuous) {
		t.Errorf("append past the end: err = %v, want ErrSessionOutputDiscontinuous", err)
	}
	// A step backwards.
	if _, err := repo.Append(ctx, content.SessionOutputAppend{
		SessionID: "sess-d", Offset: 512, Body: streamOf(16),
	}); !errors.Is(err, content.ErrSessionOutputDiscontinuous) {
		t.Errorf("append behind the end: err = %v, want ErrSessionOutputDiscontinuous", err)
	}
	// And the recording is untouched by either refusal.
	rec, err := repo.Read(ctx, "sess-d")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rec.Bytes != 1024 || rec.Produced != 1024 {
		t.Errorf("recording is %d bytes over %d produced; a refused append changed it", rec.Bytes, rec.Produced)
	}
}

// A replayed append — the same bytes at the same offset, which is what a
// recorder that retried after an ambiguous error would send — is a no-op
// rather than a duplicate. Idempotency is the property that lets the caller
// retry at all.
func TestSessionOutput_ReplayedAppendIsANoOp(t *testing.T) {
	db, _ := newRecordingStore(t, content.NewPolicy())
	repo := db.SessionOutput()
	ctx := context.Background()
	stream := streamOf(2048)

	if _, err := repo.Append(ctx, content.SessionOutputAppend{
		SessionID: "sess-r", Offset: 0, Body: stream,
	}); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	if _, err := repo.Append(ctx, content.SessionOutputAppend{
		SessionID: "sess-r", Offset: 0, Body: stream,
	}); err != nil {
		t.Fatalf("replayed Append: %v", err)
	}
	rec, err := repo.Read(ctx, "sess-r")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rec.Bytes != uint64(len(stream)) || rec.Produced != uint64(len(stream)) {
		t.Errorf("a replay grew the recording to %d bytes over %d produced", rec.Bytes, rec.Produced)
	}
	assertMatchesStreamByOffset(t, rec, stream)
}

// An empty append is accepted and changes nothing: the output pump can hand
// over a zero-length read, and a store that errored on it would turn a
// non-event into a degrade.
func TestSessionOutput_EmptyAppendChangesNothing(t *testing.T) {
	db, _ := newRecordingStore(t, content.NewPolicy())
	repo := db.SessionOutput()
	ctx := context.Background()

	res, err := repo.Append(ctx, content.SessionOutputAppend{SessionID: "sess-e", Offset: 0})
	if err != nil {
		t.Fatalf("empty Append: %v", err)
	}
	if !res.Kept {
		t.Error("an empty append is kept — there was nothing to refuse")
	}
	rec, err := repo.Read(ctx, "sess-e")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rec.Bytes != 0 || rec.Produced != 0 {
		t.Errorf("empty append produced a recording of %d/%d bytes", rec.Bytes, rec.Produced)
	}
}

// Reading a session nothing recorded is an empty recording, not an error:
// the session printed nothing, or its bytes were all dropped, and neither is
// a fault the caller can act on.
func TestSessionOutput_ReadUnknownSessionIsEmpty(t *testing.T) {
	db, _ := newRecordingStore(t, content.NewPolicy())
	rec, err := db.SessionOutput().Read(context.Background(), "never-existed")
	if err != nil {
		t.Fatalf("Read of an unknown session: %v", err)
	}
	if rec.Bytes != 0 || len(rec.Runs) != 0 || rec.Truncated != nil {
		t.Errorf("unknown session read back as %+v", rec)
	}
}

// The failure path for the store call itself, paired with the success above:
// a closed store refuses the write and says which failure it is.
func TestSessionOutput_ClosedStoreRefusesBothCalls(t *testing.T) {
	db, _ := newRecordingStore(t, content.NewPolicy())
	repo := db.SessionOutput()
	ctx := context.Background()

	if _, err := repo.Append(ctx, content.SessionOutputAppend{
		SessionID: "sess-c", Offset: 0, Body: streamOf(64),
	}); err != nil {
		t.Fatalf("Append before Close: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := repo.Append(ctx, content.SessionOutputAppend{
		SessionID: "sess-c", Offset: 64, Body: streamOf(64),
	}); !errors.Is(err, content.ErrClosed) {
		t.Errorf("Append on a closed store: err = %v, want ErrClosed", err)
	}
	if _, err := repo.Read(ctx, "sess-c"); err == nil {
		t.Error("Read on a closed store succeeded")
	}
}

// A cancelled context fails the write rather than half-doing it — the other
// external-call failure this path can meet.
func TestSessionOutput_CancelledContextFailsTheAppend(t *testing.T) {
	db, _ := newRecordingStore(t, content.NewPolicy())
	repo := db.SessionOutput()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repo.Append(ctx, content.SessionOutputAppend{
		SessionID: "sess-x", Offset: 0, Body: streamOf(64),
	}); err == nil {
		t.Error("Append with a cancelled context succeeded")
	}
	// And the same call on a live context still works: a failure test with
	// no paired success proves only that the code can fail.
	if _, err := repo.Append(context.Background(), content.SessionOutputAppend{
		SessionID: "sess-x", Offset: 0, Body: streamOf(64),
	}); err != nil {
		t.Fatalf("Append on a live context: %v", err)
	}
}

// The lifetime, both ends named: a recording exists from the first append
// until the next store-open, because a session cannot outlive the backend
// process (AD-7, D5) and at open no recording names anything live.
func TestSessionOutput_RecordingsDoNotSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	db := openRecordingStore(t, dir, content.NewPolicy())
	appendAll(t, db.SessionOutput(), "sess-old", streamOf(4096), 1024)
	rec, err := db.SessionOutput().Read(context.Background(), "sess-old")
	if err != nil {
		t.Fatalf("Read before close: %v", err)
	}
	if rec.Bytes == 0 {
		t.Fatal("nothing was recorded to begin with")
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	reopened := openRecordingStore(t, dir, content.NewPolicy())
	after, err := reopened.SessionOutput().Read(context.Background(), "sess-old")
	if err != nil {
		t.Fatalf("Read after reopen: %v", err)
	}
	if after.Bytes != 0 || len(after.Runs) != 0 {
		t.Errorf("a recording of a dead pipe survived the restart: %+v", after)
	}
}
