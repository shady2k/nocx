package transfer_test

import (
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/transfer"
)

func TestPut_WritesThroughTempAndPromotes(t *testing.T) {
	fs := newFakeFS()
	s := transfer.NewSink(fs, transfer.DefaultChunk)
	out, err := s.Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 5, OnExists: transfer.Overwrite},
		strings.NewReader("hello"), func(int64) {})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "written" || out.FinalName != "a.txt" {
		t.Fatalf("got %+v", out)
	}
	if got := fs.content("/home/u/a.txt"); got != "hello" {
		t.Fatalf("destination holds %q", got)
	}
	if left := fs.matching("*.nocx-upload-*"); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

// The tests below are design §6's table, one test per row. They are
// deliberately NOT a table-driven test over one shared fake: each row
// asserts a different post-state on the server, and a shared fake is
// exactly what hides the differences the rows exist to record.

// --- paired successes -------------------------------------------------------

func TestPut_OverwriteReplacesTheExistingContent(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")

	out, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.Overwrite},
		strings.NewReader("new"), func(int64) {})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != transfer.StateWritten || out.FinalName != "a.txt" {
		t.Fatalf("got %+v", out)
	}
	if got := fs.content("/home/u/a.txt"); got != "new" {
		t.Fatalf("destination holds %q, want the new content", got)
	}
	if got := fs.paths(); len(got) != 1 {
		t.Fatalf("an overwrite leaves exactly the destination; got %v", got)
	}
}

func TestPut_ZeroByteFileIsWritten(t *testing.T) {
	fs := newFakeFS()

	out, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "empty", Size: 0, OnExists: transfer.Overwrite},
		strings.NewReader(""), func(int64) {})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != transfer.StateWritten {
		t.Fatalf("got %+v", out)
	}
	if !fs.exists("/home/u/empty") {
		t.Fatalf("an empty file is still a file; server holds %v", fs.paths())
	}
}

// --- row: Create refused ----------------------------------------------------

func TestPut_CreateRefused_LeavesTheDestinationUntouched(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")
	refusal := errors.New("permission denied")
	fs.createErr = refusal

	out, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.Overwrite},
		strings.NewReader("new"), func(int64) {})
	if !errors.Is(err, refusal) {
		t.Fatalf("the reason must reach the caller in place; got %v", err)
	}
	if len(out.Stranded) != 0 {
		t.Fatalf("a create that never happened leaves nothing; got %v", out.Stranded)
	}
	if got := fs.content("/home/u/a.txt"); got != "old" {
		t.Fatalf("destination holds %q, want it untouched", got)
	}
}

// --- row: write fails mid-stream --------------------------------------------

func TestPut_WriteFailsMidStream_RemovesTheTempAndLeavesTheDestination(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")
	fs.failWriteAfter(4, errors.New("disk full"))

	out, err := transfer.NewSink(fs, 4).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 12, OnExists: transfer.Overwrite},
		strings.NewReader("0123456789ab"), func(int64) {})
	if err == nil {
		t.Fatal("a failed write must be an error")
	}
	if !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("the reason must be reported; got %v", err)
	}
	if len(out.Stranded) != 0 {
		t.Fatalf("the temp was removable, so nothing is stranded; got %v", out.Stranded)
	}
	if got := fs.content("/home/u/a.txt"); got != "old" {
		t.Fatalf("destination holds %q, want it untouched", got)
	}
	if left := fs.matching("*.nocx-upload-*"); len(left) != 0 {
		t.Fatalf("temp left behind: %v", left)
	}
}

// The temp invariant closes on a SUCCESSFUL Remove, never an attempted one.
// Remove is itself an external call, so here it is the one that fails.
func TestPut_WriteFailsAndRemoveAlsoFails_StrandsTheTemp(t *testing.T) {
	fs := newFakeFS()
	fs.failWriteAfter(0, errors.New("disk full"))
	fs.removeErr = errors.New("permission denied")

	out, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.Overwrite},
		strings.NewReader("new"), func(int64) {})
	if err == nil {
		t.Fatal("a failed write must be an error")
	}
	// §6 says BOTH reasons. A person told only "disk full" never learns
	// that removing the temp was refused, which is the whole reason a path
	// is sitting on their server.
	if !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("the primary failure must be reported; got %v", err)
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("the reason cleanup failed must be reported too; got %v", err)
	}
	temps := fs.matching("*.nocx-upload-*")
	if len(temps) != 1 {
		t.Fatalf("the temp is still on the server; got %v", temps)
	}
	if len(out.Stranded) != 1 || out.Stranded[0] != temps[0] {
		t.Fatalf("stranded must name the temp that is still there: %v vs %v", out.Stranded, temps)
	}
	if fs.exists("/home/u/a.txt") {
		t.Fatal("the destination was never created and must not exist")
	}
}

// --- row: Close fails after all bytes were written --------------------------

// A Close that fails is a failed transfer even though every byte was
// written: the server never confirmed the file, so the temp does not become
// the destination.
//
// And the temp is NOT removed. §6's row leaves it and names it, because a
// failed Close is precisely the case where the server did not confirm the
// file's final state, and removing a file whose state you do not know is
// not cleanup — it is a second uncertain write.
func TestPut_CloseFailsAfterAllBytes_IsAFailureAndTheTempIsStranded(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")
	fs.closeErr = errors.New("quota exceeded on flush")

	out, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.Overwrite},
		strings.NewReader("new"), func(int64) {})
	if err == nil {
		t.Fatal("a failed Close must fail the transfer, not be swallowed")
	}
	if !strings.Contains(err.Error(), "quota exceeded on flush") {
		t.Fatalf("the reason must be reported; got %v", err)
	}
	if got := fs.content("/home/u/a.txt"); got != "old" {
		t.Fatalf("destination holds %q, want it untouched", got)
	}
	temps := fs.matching("*.nocx-upload-*")
	if len(temps) != 1 {
		t.Fatalf("the temp stays on the server; got %v", temps)
	}
	if len(out.Stranded) != 1 || out.Stranded[0] != temps[0] {
		t.Fatalf("stranded %v must name the temp %v", out.Stranded, temps)
	}
	if len(fs.removed) != 0 {
		t.Fatalf("no Remove may even be ATTEMPTED on a file whose state the server never confirmed; removed %v", fs.removed)
	}
}

// The same row with a KeepBoth reservation in play, which is where a Remove
// IS attempted and can fail: the temp is stranded by policy, the empty
// reservation is stranded because the removal was refused, and the outcome
// carries the close reason AND the removal reason.
func TestPut_CloseFailsAndTheReservationCannotBeRemoved_StrandsBothAndSaysWhy(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")
	fs.failCloseAt(2, errors.New("quota exceeded on flush")) // 1 is the reservation
	fs.removeErr = errors.New("permission denied")

	out, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.KeepBoth},
		strings.NewReader("new"), func(int64) {})
	if err == nil {
		t.Fatal("a failed Close must fail the transfer")
	}
	if !strings.Contains(err.Error(), "quota exceeded on flush") {
		t.Fatalf("the primary failure must be reported; got %v", err)
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("the reason cleanup failed must be reported too; got %v", err)
	}
	if findStranded(out.Stranded, ".nocx-upload-") == "" {
		t.Fatalf("the temp must be named; got %v", out.Stranded)
	}
	if findStranded(out.Stranded, "a (1).txt") == "" {
		t.Fatalf("the reservation that could not be removed must be named; got %v", out.Stranded)
	}
}

// --- row: connection lost mid-stream ----------------------------------------

// The lease is poisoned partway through. Nothing the sink knows how to do
// can still reach the server — the Write fails, the Close fails and the
// Remove fails — so the temp stays there and the outcome says so.
func TestPut_ConnectionLostMidStream_StrandsTheTemp(t *testing.T) {
	fs := newFakeFS()
	fs.onWrite = func(total int) {
		if total >= 4 {
			fs.dead = true
		}
	}

	out, err := transfer.NewSink(fs, 4).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 12, OnExists: transfer.Overwrite},
		strings.NewReader("0123456789ab"), func(int64) {})
	if !errors.Is(err, errDisconnected) {
		t.Fatalf("want the lost lease reported; got %v", err)
	}
	temps := fs.matching("*.nocx-upload-*")
	if len(temps) != 1 {
		t.Fatalf("the temp is beyond reach and still on the server; got %v", temps)
	}
	if len(out.Stranded) != 1 || out.Stranded[0] != temps[0] {
		t.Fatalf("stranded %v must name %v", out.Stranded, temps)
	}
}

// --- rows: the source disagrees with the declared size ----------------------

func TestPut_SourceReadFails_LeavesTheDestinationUntouched(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")
	boom := errors.New("source vanished")

	out, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 12, OnExists: transfer.Overwrite},
		&failingReader{err: boom}, func(int64) {})
	if !errors.Is(err, boom) {
		t.Fatalf("the reason must reach the caller; got %v", err)
	}
	if got := fs.content("/home/u/a.txt"); got != "old" {
		t.Fatalf("destination holds %q, want it untouched", got)
	}
	if len(out.Stranded) != 0 || len(fs.matching("*.nocx-upload-*")) != 0 {
		t.Fatalf("nothing should be left: stranded=%v files=%v", out.Stranded, fs.paths())
	}
}

func TestPut_SourceEndsShort_FailsAndLeavesTheDestinationUntouched(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")

	out, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 10, OnExists: transfer.Overwrite},
		strings.NewReader("only4"), func(int64) {})
	var mismatch *transfer.SizeMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("a short source is a typed size mismatch; got %v", err)
	}
	if mismatch.Declared != 10 || mismatch.Got != 5 || mismatch.AtLeast {
		t.Fatalf("got %+v, want the exact counts", mismatch)
	}
	if got := fs.content("/home/u/a.txt"); got != "old" {
		t.Fatalf("destination holds %q, want it untouched", got)
	}
	if len(out.Stranded) != 0 || len(fs.matching("*.nocx-upload-*")) != 0 {
		t.Fatalf("nothing should be left: %v", fs.paths())
	}
}

// A reader delivering MORE than it declared fails too, and the excess is
// never written: a source that lies must not be able to fill the server's
// disk on the strength of a small declared size.
func TestPut_SourceDeliversMoreThanDeclared_FailsWithoutWritingTheExcess(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")

	out, err := transfer.NewSink(fs, 4).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 4, OnExists: transfer.Overwrite},
		strings.NewReader("0123456789ab"), func(int64) {})
	var mismatch *transfer.SizeMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("a long source is a typed size mismatch; got %v", err)
	}
	if !mismatch.AtLeast || mismatch.Declared != 4 {
		t.Fatalf("got %+v, want a lower bound past the declared size", mismatch)
	}
	total := 0
	for _, n := range fs.writeSizes {
		total += n
	}
	if total > 4 {
		t.Fatalf("wrote %d bytes for a declared 4: %v", total, fs.writeSizes)
	}
	if got := fs.content("/home/u/a.txt"); got != "old" {
		t.Fatalf("destination holds %q, want it untouched", got)
	}
	if len(out.Stranded) != 0 || len(fs.matching("*.nocx-upload-*")) != 0 {
		t.Fatalf("nothing should be left: %v", fs.paths())
	}
}

// --- rows: the promote ------------------------------------------------------

func TestPromote_RenameFailureRemovesTheTempAndKeepsTheDestination(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")
	fs.renameErr = errors.New("rename refused")

	out, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.Overwrite},
		strings.NewReader("new"), func(int64) {})
	if err == nil {
		t.Fatal("a failed promote must be an error")
	}
	if got := fs.content("/home/u/a.txt"); got != "old" {
		t.Fatalf("destination holds %q, want the old content", got)
	}
	if len(out.Stranded) != 0 || len(fs.matching("*.nocx-upload-*")) != 0 {
		t.Fatalf("the temp was removable: stranded=%v files=%v", out.Stranded, fs.paths())
	}
}

func TestPut_CancelledByThePerson_LeavesTheDestinationUntouched(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs.onWrite = func(total int) {
		if total >= 4 {
			cancel()
		}
	}

	out, err := transfer.NewSink(fs, 4).Put(ctx,
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 12, OnExists: transfer.Overwrite},
		strings.NewReader("0123456789ab"), func(int64) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want the cancellation reported; got %v", err)
	}
	if got := fs.content("/home/u/a.txt"); got != "old" {
		t.Fatalf("destination holds %q, want it untouched", got)
	}
	if len(out.Stranded) != 0 || len(fs.matching("*.nocx-upload-*")) != 0 {
		t.Fatalf("a cancel cleans up after itself: stranded=%v files=%v", out.Stranded, fs.paths())
	}
}

func TestPut_KeepBoth_ResolvesTheFinalNameBeforeTheTransfer(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")

	out, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.KeepBoth},
		strings.NewReader("new"), func(int64) {})
	if err != nil {
		t.Fatal(err)
	}
	if out.FinalName != "a (1).txt" {
		t.Fatalf("FinalName is %q; the resolved name is what the renderer shows", out.FinalName)
	}
	if got := fs.content("/home/u/a (1).txt"); got != "new" {
		t.Fatalf("the new name holds %q", got)
	}
	if got := fs.content("/home/u/a.txt"); got != "old" {
		t.Fatalf("keeping both means the original survives; it holds %q", got)
	}
	if left := fs.matching("*.nocx-upload-*"); len(left) != 0 {
		t.Fatalf("temp left behind: %v", left)
	}
}

// The arbiter is the O_EXCL create, not a stat: a name another writer
// already holds fails the create, and the next suffix is tried.
func TestPut_KeepBothLosesTheExclRace_TakesTheNextSuffix(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")
	fs.put("/home/u/a (1).txt", "someone else's")

	out, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.KeepBoth},
		strings.NewReader("new"), func(int64) {})
	if err != nil {
		t.Fatal(err)
	}
	if out.FinalName != "a (2).txt" {
		t.Fatalf("FinalName is %q, want the next free suffix", out.FinalName)
	}
	if got := fs.content("/home/u/a (1).txt"); got != "someone else's" {
		t.Fatalf("the lost race must not have been overwritten; it holds %q", got)
	}
	if got := fs.content("/home/u/a (2).txt"); got != "new" {
		t.Fatalf("the new name holds %q", got)
	}
}

func TestPut_KeepBothExhaustsItsAttempts_FailsWithATypedError(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")
	for i := 1; i <= 32; i++ {
		fs.put(fmt.Sprintf("/home/u/a (%d).txt", i), "taken")
	}
	before := len(fs.paths())

	out, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.KeepBoth},
		strings.NewReader("new"), func(int64) {})
	var exhausted *transfer.NameExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("a directory of collisions is a person to tell, not a loop; got %v", err)
	}
	if exhausted.Attempts != 32 || exhausted.Name != "a.txt" {
		t.Fatalf("got %+v", exhausted)
	}
	if exhausted.Err == nil {
		t.Fatal("the last refusal must travel with the error: the wire cannot classify EEXIST, so only the reason distinguishes a race from a permission failure")
	}
	if len(out.Stranded) != 0 {
		t.Fatalf("nothing was created; got %v", out.Stranded)
	}
	if got := len(fs.paths()); got != before {
		t.Fatalf("the directory grew from %d to %d files", before, got)
	}
	if fs.creates != 32 {
		t.Fatalf("an unclassifiable refusal is the ONE shape that spends the bound; saw %d creates", fs.creates)
	}
}

// The reservation is a file the sink created at a name nobody asked for. A
// transfer that then fails must not leave it behind.
func TestPut_KeepBothReservationIsRemovedWhenTheTransferFails(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")
	fs.failWriteAfter(0, errors.New("disk full"))

	out, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.KeepBoth},
		strings.NewReader("new"), func(int64) {})
	if err == nil {
		t.Fatal("a failed write must be an error")
	}
	if fs.exists("/home/u/a (1).txt") {
		t.Fatal("a failed KeepBoth must not leave an empty file at the reserved name")
	}
	if len(out.Stranded) != 0 {
		t.Fatalf("everything was removable; got %v", out.Stranded)
	}
	if got := fs.paths(); len(got) != 1 || got[0] != "/home/u/a.txt" {
		t.Fatalf("only the original should remain; got %v", got)
	}
}

// Close on the reservation handle is an external call too, and it can fail.
func TestPut_KeepBothReservationCloseFails_StrandsTheReservedName(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")
	fs.closeErr = errors.New("lease gone")
	fs.removeErr = errors.New("lease gone")

	out, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.KeepBoth},
		strings.NewReader("new"), func(int64) {})
	if err == nil {
		t.Fatal("a reservation that could not be closed must fail the transfer")
	}
	if len(out.Stranded) != 1 || out.Stranded[0] != "/home/u/a (1).txt" {
		t.Fatalf("the reserved name that could not be removed must be named; got %v", out.Stranded)
	}
}

// --- Skip, and the inputs the sink refuses ----------------------------------

func TestPut_Skip_TouchesNothing(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")

	out, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.Skip},
		strings.NewReader("new"), func(int64) {})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != transfer.StateSkipped {
		t.Fatalf("got %+v", out)
	}
	if got := fs.content("/home/u/a.txt"); got != "old" {
		t.Fatalf("destination holds %q", got)
	}
	if got := fs.paths(); len(got) != 1 {
		t.Fatalf("a skip writes nothing at all; got %v", got)
	}
}

func TestPut_RefusesANameThatIsNotOnePathComponent(t *testing.T) {
	for _, name := range []string{"", ".", "..", "a/b", `a\b`, "/abs"} {
		t.Run(name, func(t *testing.T) {
			fs := newFakeFS()
			_, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
				transfer.Upload{DestDir: "/home/u", Name: name, Size: 3, OnExists: transfer.Overwrite},
				strings.NewReader("new"), func(int64) {})
			if !errors.Is(err, transfer.ErrInvalidUpload) {
				t.Fatalf("name %q must be refused; got %v", name, err)
			}
			if got := fs.paths(); len(got) != 0 {
				t.Fatalf("nothing may be created; got %v", got)
			}
		})
	}
}

func TestPut_RefusesAnUnansweredCollisionDecision(t *testing.T) {
	fs := newFakeFS()
	_, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3},
		strings.NewReader("new"), func(int64) {})
	if !errors.Is(err, transfer.ErrInvalidUpload) {
		t.Fatalf("an absent decision is a caller defect, not a licence to overwrite; got %v", err)
	}
	if got := fs.paths(); len(got) != 0 {
		t.Fatalf("nothing may be created; got %v", got)
	}
}

func TestPut_RefusesAnEmptyDestinationDirectory(t *testing.T) {
	fs := newFakeFS()
	_, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "", Name: "a.txt", Size: 3, OnExists: transfer.Overwrite},
		strings.NewReader("new"), func(int64) {})
	if !errors.Is(err, transfer.ErrInvalidUpload) {
		t.Fatalf("got %v", err)
	}
}

// --- progress and chunking (D2) ---------------------------------------------

// Each chunk is ONE lane call, bounded, so a transfer that takes far longer
// than the lease's 30 s watchdog is still a sequence of short calls. The
// sink's half of that rule is observable here: it never hands the lease a
// single write the size of the file.
func TestPut_WritesOneBoundedChunkPerCallAndReportsTheRunningTotal(t *testing.T) {
	fs := newFakeFS()
	var seen []int64

	out, err := transfer.NewSink(fs, 4).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 10, OnExists: transfer.Overwrite},
		strings.NewReader("0123456789"),
		func(total int64) { seen = append(seen, total) })
	if err != nil {
		t.Fatal(err)
	}
	if out.State != transfer.StateWritten {
		t.Fatalf("got %+v", out)
	}
	want := []int{4, 4, 2}
	if !reflect.DeepEqual(fs.writeSizes, want) {
		t.Fatalf("write sizes %v, want %v — one bounded call per chunk", fs.writeSizes, want)
	}
	if !reflect.DeepEqual(seen, []int64{4, 8, 10}) {
		t.Fatalf("progress reported %v, want the running total after each chunk", seen)
	}
	if got := fs.content("/home/u/a.txt"); got != "0123456789" {
		t.Fatalf("destination holds %q", got)
	}
}

// A nil progress callback is not a crash: the sink is called from places
// that have nobody to tell.
func TestPut_NilProgressCallbackIsAccepted(t *testing.T) {
	fs := newFakeFS()
	if _, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.Overwrite},
		strings.NewReader("new"), nil); err != nil {
		t.Fatal(err)
	}
}

// failingReader fails on the first read — a source that vanished under the
// transfer.
type failingReader struct{ err error }

func (r *failingReader) Read([]byte) (int, error) { return 0, r.err }

// A promote that fails leaves the KeepBoth reservation sitting at a name
// nobody asked for. It is a file the sink created, so it goes the same way
// the temp does — and if it cannot go, it is named.
func TestPut_KeepBothReservationIsRemovedWhenThePromoteFails(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")
	fs.renameErr = errors.New("rename refused")

	out, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.KeepBoth},
		strings.NewReader("new"), func(int64) {})
	if err == nil {
		t.Fatal("a failed promote must be an error")
	}
	if fs.exists("/home/u/a (1).txt") {
		t.Fatal("an empty file at the reserved name is litter the person did not create")
	}
	if len(out.Stranded) != 0 {
		t.Fatalf("everything was removable; got %v", out.Stranded)
	}
	if got := fs.paths(); len(got) != 1 || got[0] != "/home/u/a.txt" {
		t.Fatalf("only the original should remain; got %v", got)
	}
}

// The same, on a lease that cannot remove: the reserved name is reported
// rather than dropped.
func TestPut_KeepBothReservationStrandedWhenThePromoteAndTheRemoveBothFail(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")
	fs.renameErr = errors.New("rename refused")
	fs.removeErr = errors.New("permission denied")

	out, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.KeepBoth},
		strings.NewReader("new"), func(int64) {})
	if err == nil {
		t.Fatal("a failed promote must be an error")
	}
	if findStranded(out.Stranded, "a (1).txt") == "" {
		t.Fatalf("the reserved name must be named; got %v", out.Stranded)
	}
	if findStranded(out.Stranded, ".nocx-upload-") == "" {
		t.Fatalf("the temp must be named too; got %v", out.Stranded)
	}
}

// --- KeepBoth: a refusal that is not a collision stops the search -----------

func TestPut_KeepBothCreateRefusedWithPermission_StopsAtOnceAndReportsTheRefusal(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")
	fs.createErr = fmt.Errorf("fake: open %s: %w", "/home/u/a (1).txt", iofs.ErrPermission)

	out, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.KeepBoth},
		strings.NewReader("new"), func(int64) {})
	var exhausted *transfer.NameExhaustedError
	if errors.As(err, &exhausted) {
		t.Fatalf("a read-only directory is not a directory full of collisions; got %v", err)
	}
	if !errors.Is(err, iofs.ErrPermission) {
		t.Fatalf("the refusal must reach the caller as itself; got %v", err)
	}
	if fs.creates != 1 {
		t.Fatalf("a non-collision stops the search; saw %d creates", fs.creates)
	}
	if len(out.Stranded) != 0 {
		t.Fatalf("nothing was created; got %v", out.Stranded)
	}
	if got := fs.paths(); len(got) != 1 || got[0] != "/home/u/a.txt" {
		t.Fatalf("only the original should remain; got %v", got)
	}
}

// The same rule for a destination directory that is not there: probing 31
// more names inside a directory that does not exist cannot find one.
func TestPut_KeepBothCreateRefusedBecauseTheDirectoryIsGone_StopsAtOnce(t *testing.T) {
	fs := newFakeFS()
	fs.createErr = notFound("open", "/home/u/a (1).txt")

	_, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.KeepBoth},
		strings.NewReader("new"), func(int64) {})
	var exhausted *transfer.NameExhaustedError
	if errors.As(err, &exhausted) {
		t.Fatalf("a missing directory is not \"no free name\"; got %v", err)
	}
	if !errors.Is(err, iofs.ErrNotExist) {
		t.Fatalf("the refusal must reach the caller as itself; got %v", err)
	}
	if fs.creates != 1 {
		t.Fatalf("a non-collision stops the search; saw %d creates", fs.creates)
	}
}

func TestPut_KeepBothCreateRefusedForAnUnclassifiedError_StopsAtOnce(t *testing.T) {
	fs := newFakeFS()
	fs.createErr = errors.New("input/output error")

	_, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(context.Background(),
		transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.KeepBoth},
		strings.NewReader("new"), func(int64) {})
	var exhausted *transfer.NameExhaustedError
	if errors.As(err, &exhausted) {
		t.Fatalf("an I/O error is not a directory full of collisions; got %v", err)
	}
	if !errors.Is(err, fs.createErr) {
		t.Fatalf("the refusal must reach the caller as itself; got %v", err)
	}
	if fs.creates != 1 {
		t.Fatalf("a non-collision stops the search; saw %d creates", fs.creates)
	}
}

// --- cancellation and the source reader -------------------------------------

// Put's contract: the CALLER owns unblocking the reader. An io.Reader has no
// cancellation, so a Read already in flight cannot be bounded by the
// context — cancelling without closing the reader hangs, and the transport
// that supplies a streamed body must close it on cancellation.
//
// This is the shape that contract produces, and what Put owes in return:
// once the reader reports the failure, Put unwinds — the error reaches the
// caller, the temp is removed and the destination is untouched.
func TestPut_ReaderErrsOnCancellation_UnwindsAndCleansUp(t *testing.T) {
	fs := newFakeFS()
	fs.put("/home/u/a.txt", "old")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bodyClosed := errors.New("http: body closed by the caller on cancel")
	r := &blockingReader{ctx: ctx, blocked: make(chan struct{}), err: bodyClosed}

	type result struct {
		out transfer.Outcome
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := transfer.NewSink(fs, transfer.DefaultChunk).Put(ctx,
			transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 3, OnExists: transfer.Overwrite},
			r, func(int64) {})
		done <- result{out, err}
	}()

	<-r.blocked // the read is in flight; the context check has already passed
	cancel()
	got := <-done

	if !errors.Is(got.err, bodyClosed) {
		t.Fatalf("the reason the source stopped must reach the caller; got %v", got.err)
	}
	if c := fs.content("/home/u/a.txt"); c != "old" {
		t.Fatalf("destination holds %q, want it untouched", c)
	}
	if len(got.out.Stranded) != 0 || len(fs.matching("*.nocx-upload-*")) != 0 {
		t.Fatalf("an unwound transfer cleans up after itself: stranded=%v files=%v", got.out.Stranded, fs.paths())
	}
}

// blockingReader is a streamed body: the first Read blocks until the
// context is done — which is what a stalled HTTP body does — and then
// reports the failure the caller's own Close produced. It signals that it
// has entered the blocking read so the test never waits on a duration.
type blockingReader struct {
	ctx     context.Context
	blocked chan struct{}
	once    sync.Once
	err     error
}

func (r *blockingReader) Read([]byte) (int, error) {
	r.once.Do(func() { close(r.blocked) })
	<-r.ctx.Done()
	return 0, r.err
}
