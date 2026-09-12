package sessionruntime

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// The real runtime, judged on the six cases this bead's acceptance names
// (nocx-ygxjv.9), each with the handling whose removal makes it fail.
//
// Every test here drives the session through the CONTRACT — admit, execute,
// ingest, snapshot — over a real emulator and a terminal the harness can read,
// with NO consumer attached unless the test attaches one. What each one
// asserts is what a person can observe of a session: the screen the program
// drew, the bytes the program received, and the effects it asked for.
//
// The handling each test is paired with is written into it as a comment, and
// each was verified the only way that claim can be verified: by removing the
// line and watching the test fail (see the bead's report for the run).

// sessionControl grants control to a person at a client, as a session attached
// to a terminal always has one: without a holder the runtime executes nothing
// at all.
func sessionControl(t *testing.T, s *Session) Control {
	t.Helper()
	ctrl, err := s.GrantControl(person())
	if err != nil {
		t.Fatalf("grant control: %v", err)
	}
	return ctrl
}

// admitted admits one intent under ctrl and answers its id.
func admitted(t *testing.T, s *Session, ctrl Control, kind IntentKind, payload []byte) IntentID {
	t.Helper()
	id, err := s.Admit(Intent{
		At:      s.Incarnation(),
		Under:   ctrl.Epoch,
		By:      ctrl.Holder,
		Kind:    kind,
		Payload: payload,
	})
	if err != nil {
		t.Fatalf("admit a %d intent with payload %q: %v", kind, payload, err)
	}
	return id
}

// admittedThenExecuted admits one intent under ctrl and executes it, answering
// where it got to. The id is checked rather than ignored: Execute answering for
// a different intent than the one admitted is itself a defect, and a test that
// dropped the id would hide it.
func admittedThenExecuted(t *testing.T, s *Session, ctrl Control, kind IntentKind, payload []byte) (IntentID, IntentState, error) {
	t.Helper()
	id := admitted(t, s, ctrl, kind, payload)
	gotID, state, err := s.Execute()
	if gotID != id {
		t.Fatalf("executing the admitted intent answered for id %d, want %d (state %s, err %v)", gotID, id, intentStateName(state), err)
	}
	return gotID, state, err
}

// ---------------------------------------------------------------------------
// 1. Unicode widths
//
// A wide cluster occupies two columns and only the first of them holds it, and
// a base codepoint with a combining mark is ONE cell. Both are properties of
// the screen the runtime hands out, and both are lost the moment the cell
// sequence is copied without reading what each cell IS.
//
// Handling under test: the spacer columns of a wide cluster are skipped when
// the screen is read (Session.screenTextLocked's Width test), so a row's text
// is what a person sees rather than what the grid stores. Removal: a row of
// "漢a\u0301z" comes back with the cluster's spacer rendered as a cell of its
// own.
// ---------------------------------------------------------------------------

func TestAWideClusterTakesTwoColumnsAndOneCellOfText(t *testing.T) {
	s, _, _ := realRuntime(t)

	// 漢 is two columns wide; "a\u0301" is a base and a combining mark the
	// terminal assembled into one cluster; z is one column.
	if err := s.Ingest([]byte("漢a\u0301z\r\n")); err != nil {
		t.Fatalf("ingest a row of wide and combined clusters: %v", err)
	}
	const want = "漢a\u0301z"
	if got := string(s.Snapshot().Screen); got != want {
		t.Fatalf("the screen reads %q, want %q: a wide cluster is two columns and one cell of text, and a "+
			"base with a combining mark is one cell", got, want)
	}
}

// ---------------------------------------------------------------------------
// 2. Cursor save and restore
//
// A program saves the cursor, draws elsewhere, restores, and goes on writing
// where it left off. The screen it drew is the only evidence of that, and it is
// evidence only if the runtime reads the row the cell is actually on.
//
// Handling under test: each row of the screen is read by its own index
// (Session.screenTextLocked's Row(y) with the loop's y). Removal: every line
// comes back as the first row.
// ---------------------------------------------------------------------------

func TestASavedCursorRestoresWhereTheProgramLeftOff(t *testing.T) {
	s, _, _ := realRuntime(t)

	// Row 1: "AB". Save. Row 3: "ZZ". Restore. "CD" — so the restored write
	// lands back on row 1, two columns along from "AB", while "ZZ" stays on row
	// 3.
	if err := s.Ingest([]byte("\x1b[1;1HAB\x1b7\x1b[3;1HZZ\x1b8CD")); err != nil {
		t.Fatalf("ingest a save, a move, a draw and a restore: %v", err)
	}
	const want = "ABCD\n\nZZ"
	if got := string(s.Snapshot().Screen); got != want {
		t.Fatalf("the screen reads %q, want %q: the restore put the cursor back on the first row, and the row "+
			"read is the row the cell is on", got, want)
	}
}

// ---------------------------------------------------------------------------
// 3. The alternate buffer
//
// A full-screen program owns the pane on the alternate buffer and the person
// goes back to what the shell printed when it exits. What a client is sent
// while it owns the pane is the engine's decision (§6.8), and it is a decision
// only if the screen the runtime hands out FOLLOWS the active buffer.
//
// Handling under test: the screen is read from the emulator at the moment it
// is asked for (Session.screenTextLocked reads Row, which reads the active
// screen). Removal: the screen is read once and handed out again, so the
// alternate buffer is never seen and the primary never comes back.
// ---------------------------------------------------------------------------

func TestTheScreenFollowsTheActiveBuffer(t *testing.T) {
	s, _, _ := realRuntime(t)

	if err := s.Ingest([]byte("primary\r\n")); err != nil {
		t.Fatalf("ingest the shell's own line: %v", err)
	}
	if got := string(s.Snapshot().Screen); got != "primary" {
		t.Fatalf("the primary screen reads %q, want %q", got, "primary")
	}

	// DECSET 1049: a full-screen program takes the pane.
	if err := s.Ingest([]byte("\x1b[?1049h")); err != nil {
		t.Fatalf("enter the alternate buffer: %v", err)
	}
	if got := string(s.Snapshot().Screen); strings.Contains(got, "primary") {
		t.Fatalf("the screen still holds the shell's own line after DECSET 1049: %q", got)
	}
	if err := s.Ingest([]byte("full-screen")); err != nil {
		t.Fatalf("draw on the alternate buffer: %v", err)
	}
	// The alternate screen holds what the full-screen program drew and none of
	// what the shell had printed: the buffer really was switched, and the
	// cursor it started at is the emulator's business (1049 keeps it, so the
	// program's line may not be the first row of the grid).
	if got := string(s.Snapshot().Screen); !strings.Contains(got, "full-screen") || strings.Contains(got, "primary") {
		t.Fatalf("the alternate screen reads %q, want what the program drew and not the shell's line", got)
	}

	// And on exit the shell's own screen is back, with what it held.
	if err := s.Ingest([]byte("\x1b[?1049l")); err != nil {
		t.Fatalf("leave the alternate buffer: %v", err)
	}
	if got := string(s.Snapshot().Screen); got != "primary" {
		t.Fatalf("the primary screen reads %q after the full-screen program left, want %q", got, "primary")
	}
}

// ---------------------------------------------------------------------------
// 4. Mouse modes
//
// A mouse event is INPUT only when the program asked for mouse reporting: with
// no tracking mode a click is a selection, and sending it anyway is input the
// program never asked for. What the bytes are is the terminal's decision, from
// the mode and the format the PROGRAM set.
//
// Handling under test: the event is handed to the emulator, which encodes it
// against the terminal's own state and declines when there is none
// (Session.encode's IntentKindMouse branch). Removal: the event is encoded
// here, so the same click is bytes whether or not the program asked.
// ---------------------------------------------------------------------------

func TestAMouseEventIsInputOnlyWhenTheProgramAskedForIt(t *testing.T) {
	s, term, _ := realRuntime(t)
	ctrl := sessionControl(t, s)

	// No tracking mode: the emulator declines, nothing is written, and the
	// intent is refused rather than delivered as bytes nobody asked for.
	if _, state, err := admittedThenExecuted(t, s, ctrl, IntentKindMouse, []byte("press left 2 3")); !errors.Is(err, emulator.ErrUnsupported) || state != IntentStateRefused {
		t.Fatalf("a mouse event with no tracking mode: state=%s err=%v, want refused and %v", intentStateName(state), err, emulator.ErrUnsupported)
	}
	if got := term.Written(); len(got) != 0 {
		t.Fatalf("a mouse event no program asked for wrote %q to the PTY", got)
	}

	// The program asks for SGR mouse reporting, and the same event now reaches
	// it — one byte sequence per cell, in the program's own protocol.
	if err := s.Ingest([]byte("\x1b[?1000h\x1b[?1006h")); err != nil {
		t.Fatalf("the program enables mouse tracking: %v", err)
	}
	if _, state, err := admittedThenExecuted(t, s, ctrl, IntentKindMouse, []byte("press left 2 3")); err != nil || state != IntentStateExecuted {
		t.Fatalf("a mouse event the program asked for: state=%s err=%v, want executed", intentStateName(state), err)
	}
	written := term.Written()
	if len(written) != 1 || string(written[0]) != "\x1b[<0;3;4M" {
		t.Fatalf("the press at cell (2,3) reached the program as %q, want %q", written, "\x1b[<0;3;4M")
	}
}

// ---------------------------------------------------------------------------
// 5. Bracketed paste
//
// A paste is one intent whose bytes depend on a mode the program set: framed in
// ESC[200~ … ESC[201~ when it asked for bracketed paste, and the text as it is
// when it did not. A client cannot know which, which is why it sends the intent
// and not the bytes (ADR-0066).
//
// Handling under test: the paste goes through the emulator, which frames it
// from the terminal's own mode 2004 (Session.encode's IntentKindPaste branch).
// Removal: the payload is written as it arrived, so the framing is whatever the
// client sent.
// ---------------------------------------------------------------------------

func TestAPasteIsFramedByTheProgramsBracketedPasteMode(t *testing.T) {
	s, term, _ := realRuntime(t)
	ctrl := sessionControl(t, s)

	// The program has not asked for bracketed paste: the text goes as it is.
	if _, state, err := admittedThenExecuted(t, s, ctrl, IntentKindPaste, []byte("make test")); err != nil || state != IntentStateExecuted {
		t.Fatalf("a paste before the mode was set: state=%s err=%v, want executed", intentStateName(state), err)
	}
	plain := term.Written()
	if len(plain) != 1 || string(plain[0]) != "make test" {
		t.Fatalf("a paste with no bracketed-paste mode reached the program as %q, want the text itself", plain)
	}

	// And with mode 2004 set, the SAME intent is framed.
	if err := s.Ingest([]byte("\x1b[?2004h")); err != nil {
		t.Fatalf("the program enables bracketed paste: %v", err)
	}
	if _, state, err := admittedThenExecuted(t, s, ctrl, IntentKindPaste, []byte("make test")); err != nil || state != IntentStateExecuted {
		t.Fatalf("a paste with the mode set: state=%s err=%v, want executed", intentStateName(state), err)
	}
	framed := term.Written()
	if len(framed) != 2 || string(framed[1]) != "\x1b[200~make test\x1b[201~" {
		t.Fatalf("the same paste with mode 2004 set reached the program as %q, want %q", framed, "\x1b[200~make test\x1b[201~")
	}
}

// ---------------------------------------------------------------------------
// 6. A reply the program asked for
//
// The program asks its terminal a question — here where the cursor is — and the
// answer comes from the runtime's own state, with no client attached and no
// controller needed: replies are the TERMINAL's obligation and not a user
// intent. They travel the ordered write path the intents travel, so an answer
// can never interleave inside an intent's bytes.
//
// Handling under test: the emulator's replies are written back to the PTY
// (Session.Ingest's writeLocked of the replies). Removal: the program's own
// query is answered into nowhere and the program blocks forever.
// ---------------------------------------------------------------------------

func TestTheRuntimeAnswersTheProgramsOwnQuestion(t *testing.T) {
	s, term, _ := realRuntime(t)

	// The program moves its cursor and then asks where it is (DSR, CSI 6n).
	// The answer is the runtime's state and not a constant, which is why the
	// position is one the program chose.
	if err := s.Ingest([]byte("\x1b[2;5H\x1b[6n")); err != nil {
		t.Fatalf("ingest a cursor move and a device status report: %v", err)
	}
	written := term.Written()
	if len(written) != 1 || string(written[0]) != "\x1b[2;5R" {
		t.Fatalf("the program asked where its cursor was and the answer that reached it was %q, want %q",
			written, "\x1b[2;5R")
	}

	// A second query after a second move answers the second position: a reply
	// that were a constant would answer this one wrong.
	if err := s.Ingest([]byte("\x1b[4;1H\x1b[6n")); err != nil {
		t.Fatalf("ingest a second move and report: %v", err)
	}
	written = term.Written()
	if len(written) != 2 || string(written[1]) != "\x1b[4;1R" {
		t.Fatalf("the second answer that reached the program was %q, want %q", written, "\x1b[4;1R")
	}
}

// TestAnEffectBodyBelongsToTheRuntimeAndNotItsCaller is the aliasing half of
// the delivery path: an effect's body is the runtime's memory, so neither the
// producer that handed it over nor a consumer that reads it back can rewrite a
// bell that has already been delivered. Without the copies, the identity the
// duplicate policy is stated over would name different bytes at different
// times.
func TestAnEffectBodyBelongsToTheRuntimeAndNotItsCaller(t *testing.T) {
	s, _, _ := realRuntime(t)
	c := s.Consumers().Attach()

	body := []byte("build finished")
	if err := s.Consumers().Offer(Effect{ID: 1, At: s.Incarnation(), Kind: EffectNotification, Body: body}); err != nil {
		t.Fatalf("offer a notification: %v", err)
	}

	// The producer reuses its buffer, which is what a program's own read buffer
	// is for.
	body[0] = 'X'
	if got := c.Effects(); len(got) != 1 || string(got[0].Body) != "build finished" {
		t.Fatalf("the delivered effect reads %v after the producer reused its buffer, want %q", got, "build finished")
	}

	// And a consumer writes through what it was handed.
	held := c.Effects()
	held[0].Body[0] = 'Y'
	if got := c.Effects(); len(got) != 1 || string(got[0].Body) != "build finished" {
		t.Fatalf("the held effect reads %v after the reader wrote through it, want %q", got, "build finished")
	}
}

// ---------------------------------------------------------------------------
// The two rules whose contract schedules are written against the model's screen
//
// contract_test.go's schedules for the pin and for a wedged consumer end by
// reading the model's screen as a window on the TEXT it has ingested, and a
// real screen is the grid the program drew. The schedules themselves are run
// against this runtime in schedules_test.go and their entries there name that
// difference; the RULES are asserted here, on the same runtime, with evidence a
// real screen can give.
// ---------------------------------------------------------------------------

// TestThePinOutlivesTheScreenItWasTakenFrom is the rule
// scheduleFenceSightedFirstSurvivesScreenTrim exists for: output keeps flowing
// while the authenticated half is in flight, the rows the fence was seen on are
// gone by the time it arrives, and a rendezvous that pinned a ROW NUMBER would
// have lost the capture source. The departure is asserted rather than assumed:
// the screen really does lose the row.
func TestThePinOutlivesTheScreenItWasTakenFrom(t *testing.T) {
	s, _, _ := realRuntime(t)
	source := []byte("the row the fence was drawn over")

	if err := s.Ingest(append(append([]byte(nil), source...), '\r', '\n')); err != nil {
		t.Fatalf("draw the row the fence will be sighted over: %v", err)
	}
	if !bytes.Contains(s.Snapshot().Screen, source) {
		t.Fatalf("the screen does not hold %q, so this test would prove nothing", source)
	}
	if err := s.SightFence(nonceOf(0x66), source); err != nil {
		t.Fatalf("sight the fence over the row: %v", err)
	}

	// Output keeps flowing, and the grid scrolls the row away.
	if err := s.Ingest([]byte(strings.Repeat("x\r\n", 40))); err != nil {
		t.Fatalf("keep the program writing: %v", err)
	}
	if bytes.Contains(s.Snapshot().Screen, source) {
		t.Fatalf("the screen still holds the sighted row, so the trim this test is about did not happen")
	}
	if got := s.Rendezvous().PinnedSource; !bytes.Equal(got, source) {
		t.Fatalf("the pin is %q after the screen moved past it, want %q", got, source)
	}

	s.AuthenticatedEvents().Completed(s.Incarnation(), nonceOf(0x66), 0)
	if got := s.Rendezvous().State; got != RendezvousComplete {
		t.Fatalf("the matching completion left the rendezvous %s, want complete", rendezvousName(got))
	}
}

// TestAWedgedConsumerCostsNoIngest is the rule
// scheduleConsumerThatNeverReads exists for: a subscriber that stops reading is
// coalesced for, and what the program said still reaches the screen. The
// evidence is the screen a real one can give.
func TestAWedgedConsumerCostsNoIngest(t *testing.T) {
	s, _, _ := realRuntime(t)
	wedged := s.Consumers().Attach()

	const ingests = 4 * MaxPendingFrames
	for range ingests {
		if err := s.Ingest([]byte("a line of output arrived\r\n")); err != nil {
			t.Fatalf("ingest for a consumer that never reads: %v", err)
		}
	}
	if got := wedged.Pending(); got > MaxPendingFrames {
		t.Fatalf("the runtime holds %d payloads for a consumer that read none of the %d it was sent, want at most %d",
			got, ingests, MaxPendingFrames)
	}
	if wedged.Coalesced() == 0 || !wedged.Stale() {
		t.Fatalf("the consumer was sent %d payloads it never read, %d are counted as dropped and its staleness "+
			"reads %v, want a count above zero and a consumer that knows what it holds",
			ingests, wedged.Coalesced(), wedged.Stale())
	}
	if got := s.IngestState().Lost; got != 0 {
		t.Fatalf("%d bytes of output were discarded while a consumer was wedged, want none: a consumer's queue is "+
			"coalescable, the stream is not", got)
	}

	if err := s.Ingest([]byte("the last line\r\n")); err != nil {
		t.Fatalf("ingest after the flood: %v", err)
	}
	if got := string(s.Snapshot().Screen); !strings.Contains(got, "the last line") {
		t.Fatalf("the output ingested after the flood did not reach the screen, which reads %q", got)
	}
}
