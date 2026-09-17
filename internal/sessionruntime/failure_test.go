package sessionruntime

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/emulator/ghostty"
)

// The runtime's failure paths (AGENTS.md testing rule 3), each with the
// success it is the failure OF: a path that has never been exercised in the
// direction that works is a path nobody knows the shape of.
//
// Nothing here is about the emulator's or the terminal's own behaviour: what is
// under test is what the RUNTIME does when the other side fails — which state
// it reports, what it leaves behind, and that it repairs rather than leaves two
// sides disagreeing about every cell after a column.

// ---------------------------------------------------------------------------
// The PTY's write fails
// ---------------------------------------------------------------------------

// TestAPTYWriteThatFailsReportsFailedAndNotExecuted is the failure half: a
// write that did not land may not be reported as delivered, and it may not be
// reported as cancelled either — a write can fail part-way, and the bytes it
// did take are beyond recall.
func TestAPTYWriteThatFailsReportsFailedAndNotExecuted(t *testing.T) {
	s, term, _ := realRuntime(t)
	ctrl := sessionControl(t, s)

	term.failNextWrites(1)
	id, state, err := admittedThenExecuted(t, s, ctrl, IntentKindKey, []byte("l"))
	if err == nil || state != IntentStateFailed {
		t.Fatalf("a write the terminal refused: state=%s err=%v, want failed and an error", intentStateName(state), err)
	}
	if got := s.IntentState(id); got != IntentStateFailed {
		t.Fatalf("the intent reads %s after its write failed, want failed", intentStateName(got))
	}
	if got := term.Written(); len(got) != 0 {
		t.Fatalf("a failed write left %q recorded as having reached the PTY", got)
	}

	// And the success it is the failure of: the same intent, the same
	// terminal, no failure — the bytes reach the program and the state says so.
	id, state, err = admittedThenExecuted(t, s, ctrl, IntentKindKey, []byte("l"))
	if err != nil || state != IntentStateExecuted {
		t.Fatalf("the same intent with the terminal writing: state=%s err=%v, want executed", intentStateName(state), err)
	}
	if got := term.Written(); len(got) != 1 || string(got[0]) != "l" {
		t.Fatalf("the executed intent reached the PTY as %q, want %q", got, "l")
	}
	if got := s.IntentState(id); got != IntentStateExecuted {
		t.Fatalf("the intent reads %s after its write, want executed", intentStateName(got))
	}
}

// TestAShortWriteIsAFailure is the part-way half, and the reason
// IntentStateFailed exists at all: n bytes were taken and the rest were not,
// so nobody may say the bytes landed and nobody may say they did not.
func TestAShortWriteIsAFailure(t *testing.T) {
	s, term, _ := realRuntime(t)
	ctrl := sessionControl(t, s)

	// The intent is a paste rather than a key, because a short write needs
	// more than one byte to be short by.
	term.shortNextWrite(1)
	_, state, err := admittedThenExecuted(t, s, ctrl, IntentKindPaste, []byte("make test"))
	if !errors.Is(err, io.ErrShortWrite) || state != IntentStateFailed {
		t.Fatalf("a write the terminal took one byte short: state=%s err=%v, want failed and %v",
			intentStateName(state), err, io.ErrShortWrite)
	}

	// The pair: a write the terminal takes whole is executed.
	_, state, err = admittedThenExecuted(t, s, ctrl, IntentKindPaste, []byte("make test"))
	if err != nil || state != IntentStateExecuted {
		t.Fatalf("a write the terminal took whole: state=%s err=%v, want executed", intentStateName(state), err)
	}
}

// TestAReplyThatCannotBeWrittenDoesNotSwallowWhatTheStreamProduced is the
// failure of the ordered write path that carries the program's own answers:
// the screen moved and the program's bell rang, so neither the frame nor the
// effect may be dropped because the reply could not be delivered.
func TestAReplyThatCannotBeWrittenDoesNotSwallowWhatTheStreamProduced(t *testing.T) {
	s, term, _ := realRuntime(t)
	c := s.Consumers().Attach()

	term.failNextWrites(1)
	err := s.Ingest([]byte("\x1b[2;5H\x1b[6n\x07"))
	if err == nil {
		t.Fatalf("an ingest whose reply could not be written reported success")
	}
	if got := len(c.Effects()); got != 1 {
		t.Fatalf("the consumer holds %d effects after a bell the program rang while a reply failed, want one", got)
	}
	if c.Pending() == 0 {
		t.Fatalf("the consumer was owed a frame for a screen that moved and holds none")
	}

	// The pair: with the write path working, the same ingest answers the
	// program and reports nothing.
	term.acceptWrites()
	if err := s.Ingest([]byte("\x1b[2;5H\x1b[6n")); err != nil {
		t.Fatalf("an ingest whose reply was written: %v", err)
	}
	if got := term.Written(); len(got) != 1 || string(got[0]) != "\x1b[2;5R" {
		t.Fatalf("the program's own answer reached it as %q, want %q", got, "\x1b[2;5R")
	}
}

// ---------------------------------------------------------------------------
// The resize, refused by one side or the other
// ---------------------------------------------------------------------------

// TestATerminalThatRefusesTheSizeLeavesTheCommitStanding is the refusal the
// terminal owns: nothing was taken, so nothing is put back and the commit in
// force simply stands.
func TestATerminalThatRefusesTheSizeLeavesTheCommitStanding(t *testing.T) {
	s, term, screen := realRuntime(t)
	first, err := s.CommitGeometry(harnessGeometry(100, 30))
	if err != nil {
		t.Fatalf("commit the size the session runs at: %v", err)
	}

	term.RefuseResize()
	if _, refuseErr := s.CommitGeometry(harnessGeometry(120, 40)); refuseErr == nil {
		t.Fatalf("the terminal refused the size and CommitGeometry reported success")
	}
	if got := s.Geometry(); got != first {
		t.Fatalf("the commit in force is %+v after the terminal refused, want %+v", got, first)
	}
	if got, running := term.Size(), screen.Size(); got != first.Geometry || running != first.Geometry {
		t.Fatalf("after the terminal refused, the terminal is at %+v and the emulator at %+v, want both at %+v",
			got, running, first.Geometry)
	}

	// The pair: with the refusal over, the same commit opens on both sides.
	term.AcceptResize()
	second, err := s.CommitGeometry(harnessGeometry(120, 40))
	if err != nil {
		t.Fatalf("the same size with the terminal accepting: %v", err)
	}
	if second.Geometry != harnessGeometry(120, 40) || second.Revision <= first.Revision {
		t.Fatalf("the commit after the refusal is %+v, want the new size at a revision after %d", second, first.Revision)
	}
	if got, running := term.Size(), screen.Size(); got != second.Geometry || running != second.Geometry {
		t.Fatalf("after the commit the terminal is at %+v and the emulator at %+v, want both at %+v",
			got, running, second.Geometry)
	}
}

// TestAnEmulatorThatRefusesLeavesNeitherSideAtAnUncommittedSize is the half
// that makes the rule about the COMMIT rather than about the calls: the
// terminal had already taken the size, and a size nobody committed is not one
// either side may keep.
func TestAnEmulatorThatRefusesLeavesNeitherSideAtAnUncommittedSize(t *testing.T) {
	s, term, screen := realRuntime(t)
	first, err := s.CommitGeometry(harnessGeometry(100, 30))
	if err != nil {
		t.Fatalf("commit the size the session runs at: %v", err)
	}

	screen.RefuseResize()
	if _, refuseErr := s.CommitGeometry(harnessGeometry(90, 20)); refuseErr == nil {
		t.Fatalf("the emulator refused the size and CommitGeometry reported success")
	}
	if got := s.Geometry(); got != first {
		t.Fatalf("the commit in force is %+v after the emulator refused, want %+v", got, first)
	}
	if got, running := term.Size(), screen.Size(); got != first.Geometry || running != first.Geometry {
		t.Fatalf("the terminal is at %+v and the emulator at %+v, want both at the commit in force %+v: the "+
			"terminal had already taken the refused size, and a size nobody committed is not one either side may keep",
			got, running, first.Geometry)
	}

	// The pair: with the refusal over, both sides take it and the commit opens.
	screen.AcceptResize()
	if _, err := s.CommitGeometry(harnessGeometry(90, 20)); err != nil {
		t.Fatalf("the same size with the emulator accepting: %v", err)
	}
	if got, running := term.Size(), screen.Size(); got != harnessGeometry(90, 20) || running != harnessGeometry(90, 20) {
		t.Fatalf("the terminal is at %+v and the emulator at %+v after the commit, want both at 90x20", got, running)
	}
}

// TestAReplyThatNeverReachedTheProgramDoesNotPublishTheCommit is the third way
// a commit fails to open, and the one that is neither side's refusal: both took
// the size, and the program's own report of it — the in-band size report a
// terminal writes when the program asked for one — did not reach it. The
// attempt did not complete, so it publishes nothing and both sides go back to
// the commit that stands.
func TestAReplyThatNeverReachedTheProgramDoesNotPublishTheCommit(t *testing.T) {
	s, term, screen := realRuntime(t)
	first, err := s.CommitGeometry(harnessGeometry(100, 30))
	if err != nil {
		t.Fatalf("commit the size the session runs at: %v", err)
	}

	// The emulator answers the resize with the report a program that enabled
	// in-band size reports is owed, and the write of it fails.
	screen.answerResizeWith([]byte("\x1b[48;30;100;600;240t"))
	term.failNextWrites(1)
	if _, lostErr := s.CommitGeometry(harnessGeometry(100, 40)); lostErr == nil {
		t.Fatalf("the program's report could not be written and CommitGeometry reported success")
	}
	if got := s.Geometry(); got != first {
		t.Fatalf("the commit in force is %+v after the report was lost, want %+v", got, first)
	}
	if got, running := term.Size(), screen.Size(); got != first.Geometry || running != first.Geometry {
		t.Fatalf("the terminal is at %+v and the emulator at %+v, want both back at the commit in force %+v",
			got, running, first.Geometry)
	}

	// The pair: the same commit with the write path working publishes, and the
	// program hears about it.
	term.acceptWrites()
	screen.answerResizeWith(nil)
	second, err := s.CommitGeometry(harnessGeometry(100, 40))
	if err != nil {
		t.Fatalf("the same size with the report reaching the program: %v", err)
	}
	if second.Geometry != harnessGeometry(100, 40) {
		t.Fatalf("the commit in force is %+v, want 100x40", second)
	}
	if got := term.Written(); len(got) != 1 || string(got[0]) != "\x1b[48;30;100;600;240t" {
		t.Fatalf("the program's in-band size report reached it as %q", got)
	}
}

// ---------------------------------------------------------------------------
// The emulator cannot be created
// ---------------------------------------------------------------------------

// TestAnEmulatorThatCannotBeCreatedIsReportedAndNothingRuns is the boundary
// this runtime is constructed on: a size no terminal can have is refused by the
// emulator's own constructor, and a runtime built over a terminal nobody could
// create is a session nobody can direct.
func TestAnEmulatorThatCannotBeCreatedIsReportedAndNothingRuns(t *testing.T) {
	if _, err := ghostty.New(emulator.Geometry{}); err == nil {
		t.Fatalf("an emulator was created for a geometry no terminal can have")
	}
	// And the pair: the geometry a terminal CAN have yields one that ingests.
	term, err := ghostty.New(harnessGeometry(80, 24))
	if err != nil {
		t.Fatalf("create an emulator at 80x24: %v", err)
	}
	defer term.Close()
	if _, ingestErr := term.Ingest([]byte("ok\r\n")); ingestErr != nil {
		t.Fatalf("ingest into the emulator that was created: %v", ingestErr)
	}
	row, err := term.Row(0)
	if err != nil {
		t.Fatalf("read the emulator's first row: %v", err)
	}
	if got := rowText(row); got != "ok" {
		t.Fatalf("the emulator's first row reads %q, want %q", got, "ok")
	}
}

// ---------------------------------------------------------------------------
// The emulator is closed underneath the runtime
// ---------------------------------------------------------------------------

// TestAClosedEmulatorIsReportedRatherThanCrashed is the shutdown path: a
// terminal closed while the runtime still holds it. Every call that reads or
// writes it answers an error, the session says what it can honestly claim about
// the stream, and nothing panics — a runtime shutting down races its own
// readers, and that race must not take the process with it.
func TestAClosedEmulatorIsReportedRatherThanCrashed(t *testing.T) {
	s, _, screen := realRuntime(t)

	// The pair: before the close, the same ingest reaches the screen.
	if err := s.Ingest([]byte("before the close\r\n")); err != nil {
		t.Fatalf("ingest while the emulator is open: %v", err)
	}
	if got := string(s.Snapshot().Screen); got != "before the close" {
		t.Fatalf("the screen reads %q before the close, want %q", got, "before the close")
	}
	screen.Terminal.Close()

	if err := s.Ingest([]byte("after the close\r\n")); !errors.Is(err, emulator.ErrClosed) {
		t.Fatalf("ingest into a closed emulator returned %v, want %v", err, emulator.ErrClosed)
	}
	if got := s.Completeness(); got != CompletenessLostIngest {
		t.Fatalf("after an emulator refused bytes it was handed, completeness is %s, want lost-ingest: what it "+
			"holds is not the whole stream and no attach may be told otherwise", completenessName(got))
	}
	// A read of a closed screen answers no screen at all rather than a stale
	// one, and it does not panic.
	if got := s.Snapshot().Screen; got != nil {
		t.Fatalf("the closed screen reads %q, want no screen", got)
	}
	if got := screen.Size(); got.Valid() {
		t.Fatalf("a closed emulator reports the size %+v, want no size at all", got)
	}
}

// TestClosingTheEmulatorDuringIngestDoesNotCrash is the same path under
// concurrency, which is how it happens in a product: a teardown closes the
// terminal while the reader goroutine is still feeding it. Every ingest either
// succeeded or answered an error, and no call panicked.
//
// It is also the check the port's own locking exists for (ADR-0065 point 3):
// an ingest and a close reaching the same handle is a pair the adapter must
// serialise, and the failure it prevents is a use-after-free rather than a
// wrong answer.
func TestClosingTheEmulatorDuringIngestDoesNotCrash(t *testing.T) {
	s, _, screen := realRuntime(t)

	var wg sync.WaitGroup
	wg.Add(1)
	ingested := make(chan error, 1)
	go func() {
		defer wg.Done()
		for range 1 << 12 {
			if err := s.Ingest([]byte("output while the session goes away\r\n")); err != nil {
				ingested <- err
				return
			}
		}
		ingested <- nil
	}()

	// The close races the reader by construction: nothing here waits, because
	// the property under test is that the pair is safe whenever it happens.
	screen.Terminal.Close()
	wg.Wait()

	err := <-ingested
	if err != nil && !errors.Is(err, emulator.ErrClosed) {
		t.Fatalf("an ingest racing a close answered %v, want either success or %v", err, emulator.ErrClosed)
	}
	if got := s.Snapshot().Screen; got != nil && !bytes.Contains(got, []byte("output while the session goes away")) {
		t.Fatalf("the screen after the race reads %q, which is neither empty nor what was ingested", got)
	}
}

// rowText is a row's text, the way Session.screenTextLocked reads it: the
// cluster once, its spacer columns skipped, trailing blanks dropped.
func rowText(row emulator.Row) string {
	var b []byte
	for _, cell := range row.Cells {
		switch cell.Width {
		case emulator.WidthSpacerTail, emulator.WidthSpacerHead:
			continue
		}
		if !cell.HasText {
			b = append(b, ' ')
			continue
		}
		b = append(b, cell.Grapheme...)
	}
	return string(bytes.TrimRight(b, " "))
}
