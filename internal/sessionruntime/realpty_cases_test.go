package sessionruntime

import (
	"strings"
	"testing"
)

// The six verification cases, on a REAL PTY (nocx-ygxjv.9).
//
// Each test below starts a real shell on a real pty, hands it to a real runtime
// over the real emulator, and drives the session through the CONTRACT alone: an
// intent goes in, the program does the thing it was sent, and what the test
// asserts is something it can OBSERVE — the screen the runtime hands out, or a
// value the program printed after reading what the runtime sent it. No client
// is attached to any of them.
//
// Nothing here waits on a duration. Every wait ends when the screen comes to
// hold a sentinel the program printed; the hang limit is only the end of the
// wait. The program is always the same shape — raw line discipline, do the
// thing, hex-encode whatever it read — so an assertion can name exact bytes
// without an escape sequence being re-interpreted on the way back.

// rawPreamble is how every program here begins: raw line discipline, so the
// runtime's bytes arrive as bytes rather than as a line the shell is editing,
// and readhex, which reads exactly n bytes and answers their hex.
const rawPreamble = `
stty -icanon -echo min 1 time 0
readhex() { dd bs=1 count="$1" 2>/dev/null | od -An -tx1 | tr -d ' \n'; }
`

// ---------------------------------------------------------------------------
// 1. Unicode widths
//
// Two clusters nobody may treat as one column: a CJK character and an emoji,
// both of which occupy two. The program draws them and then asks the runtime
// where its cursor is, so the evidence is BOTH what the screen holds and how
// many columns the runtime's own state advanced by — a screen read that skipped
// the spacer cell of a wide cluster, or a cursor that only moved one column,
// is a different answer.
//
// Handling under test: the spacer columns of a wide cluster are skipped when
// the screen is read (Session.screenTextLocked's Width test). Removal: the
// screen shows an extra cell for each cluster.
// ---------------------------------------------------------------------------

const unicodeWidthsProgram = rawPreamble + `
printf '\033[1;1H'
printf '漢🙂X'
printf '\033[6n'
reply=$(readhex 6)
printf '\r\nCOL:%s\n' "$reply"
printf 'WIDTH-DONE\n'
`

func TestAWideClusterTakesTwoColumnsOnARealPTY(t *testing.T) {
	p := startProgram(t, unicodeWidthsProgram, harnessGeometry(80, 24))
	p.wait("WIDTH-DONE")

	// 漢 and 🙂 are two columns each, so "X" is the fifth column and the
	// cursor rests on the sixth. Had either been one column, the report would
	// name a column two or four.
	if got := p.screen(); !strings.Contains(got, "COL:1b5b313b3652") {
		t.Fatalf("the cursor report the program read back is missing from the screen:\n%s\nwant COL:1b5b313b3652 "+
			"(row 1, column 6: two clusters of two columns and a one-column X)", got)
	}
	// And the screen's first row is the clusters once each, with no cell the
	// spacer of a wide cluster added.
	if row := strings.Split(p.screen(), "\n")[0]; row != "漢🙂X" {
		t.Fatalf("the drawn row reads %q, want %q", row, "漢🙂X")
	}
}

// ---------------------------------------------------------------------------
// 2. Cursor save and restore
//
// DECSC, a move, a draw, DECRC, and a draw that must land where the cursor was
// SAVED and not where it was when the program came back.
//
// Handling under test: each row of the screen is read by its own index
// (Session.screenTextLocked's Row(y)). Removal: every line comes back as the
// first row.
// ---------------------------------------------------------------------------

const cursorSaveRestoreProgram = rawPreamble + `
printf '\033[1;1HAB\0337\033[3;1HZZ\0338CD'
printf '\r\nSAVE-DONE\n'
`

func TestASavedCursorRestoresWhereTheProgramLeftOffOnARealPTY(t *testing.T) {
	p := startProgram(t, cursorSaveRestoreProgram, harnessGeometry(80, 24))
	p.wait("SAVE-DONE")

	const want = "ABCD\nSAVE-DONE\nZZ"
	if got := p.screen(); got != want {
		t.Fatalf("the screen reads %q, want %q: the restore put the cursor back on the first row, two columns "+
			"along from AB, and the Z's the program drew on the third row are on the third row", got, want)
	}
}

// ---------------------------------------------------------------------------
// 3. The alternate buffer
//
// A full-screen program takes the pane on the alternate buffer and gives it
// back. What a client is sent while it owns the pane is the engine's decision
// (§6.8); what is asserted here is that the runtime's screen FOLLOWS the active
// buffer, and that the shell's own screen is intact afterwards — the two halves
// of one rule.
//
// Handling under test: the screen is read from the emulator at the moment it is
// asked for. Removal: the screen is read once and handed out again.
// ---------------------------------------------------------------------------

const alternateBufferProgram = rawPreamble + `
printf 'PRIMARY-LINE\n'
printf '\033[?1049h'
printf 'ALT-LINE\n'
printf 'ALT-READY\n'
readhex 1 >/dev/null
printf '\033[?1049l'
printf 'BACK-ON-PRIMARY\n'
`

func TestTheAlternateBufferOnARealPTY(t *testing.T) {
	p := startProgram(t, alternateBufferProgram, harnessGeometry(80, 24))
	p.wait("ALT-READY")

	// The full-screen program owns the pane: its own line is what the screen
	// shows, and the shell's line is not in it.
	if got := p.screen(); !strings.Contains(got, "ALT-LINE") || strings.Contains(got, "PRIMARY-LINE") {
		t.Fatalf("while the full-screen program owns the pane the screen reads:\n%s\nwant ALT-LINE and no PRIMARY-LINE", got)
	}

	// The program is told to leave the alternate buffer, and the shell's own
	// screen is back with what it held.
	p.typed("g")
	p.wait("BACK-ON-PRIMARY")
	if got := p.screen(); !strings.Contains(got, "PRIMARY-LINE") || strings.Contains(got, "ALT-LINE") {
		t.Fatalf("after the full-screen program left, the screen reads:\n%s\nwant PRIMARY-LINE and no ALT-LINE", got)
	}
}

// ---------------------------------------------------------------------------
// 4. Mouse modes
//
// A mouse event is input only when the program asked for mouse reporting, and
// what the bytes are is the terminal's decision from the mode and format the
// PROGRAM set. Both halves are asserted here: the SGR-encoded press reaches the
// program while the modes are on, and the same intent reaches nobody once they
// are off — with the program's own next read as the evidence, because a byte
// that never arrived is not something a screen can show.
//
// Handling under test: the event is handed to the emulator, which encodes it
// against the terminal's own state (Session.encode's IntentKindMouse branch).
// Removal: the event is encoded here, so the same click is bytes whether or not
// the program asked.
// ---------------------------------------------------------------------------

const mouseProgram = rawPreamble + `
printf '\033[?1000h\033[?1006h'
printf 'MOUSE-ON\n'
printf 'MOUSE1:%s\n' "$(readhex 9)"
printf '\033[?1006l\033[?1000l'
printf 'MOUSE-OFF\n'
printf 'MOUSE2:%s\n' "$(readhex 1)"
`

func TestMouseModesOnARealPTY(t *testing.T) {
	p := startProgram(t, mouseProgram, harnessGeometry(80, 24))
	p.wait("MOUSE-ON")

	// SGR mouse reporting is on, so the press at cell (2,3) is nine bytes and
	// the program reads them and hands them back.
	p.mustSend(IntentKindMouse, []byte("press left 2 3"))
	p.wait("MOUSE1:1b5b3c303b333b344d")

	// The program turns mouse reporting off, and the SAME intent is now
	// nothing: the emulator declines it rather than this runtime inventing
	// bytes for a click no program asked about.
	p.wait("MOUSE-OFF")
	if state, err := p.send(IntentKindMouse, []byte("press left 2 3")); state != IntentStateRefused || err == nil {
		t.Fatalf("a mouse event with tracking off: state=%s err=%v, want refused with an error", intentStateName(state), err)
	}

	// The one byte the program is waiting on arrives as itself. Had the refused
	// press reached it, this read would have taken the first byte of that
	// sequence instead, and the value below would not be 67.
	p.typed("g")
	p.wait("MOUSE2:67")
}

// ---------------------------------------------------------------------------
// 5. Bracketed paste
//
// One paste intent, two byte sequences, decided by a mode the program set. Both
// directions in one program: wrapped while ?2004h is set, and the text as it is
// once the program turns it off.
//
// Handling under test: the paste goes through the emulator, which frames it
// from the terminal's own mode 2004. Removal: the payload is written as it
// arrived, so the framing is whatever the client sent.
// ---------------------------------------------------------------------------

const bracketedPasteProgram = rawPreamble + `
printf '\033[?2004h'
printf 'PASTE-ON\n'
printf 'PASTE1:%s\n' "$(readhex 21)"
printf '\033[?2004l'
printf 'PASTE-OFF\n'
printf 'PASTE2:%s\n' "$(readhex 9)"
`

func TestBracketedPasteOnARealPTY(t *testing.T) {
	p := startProgram(t, bracketedPasteProgram, harnessGeometry(80, 24))
	p.wait("PASTE-ON")

	// 21 bytes: ESC [ 200 ~, the text, ESC [ 201 ~.
	p.mustSend(IntentKindPaste, []byte("make test"))
	p.wait("PASTE1:1b5b3230307e6d616b6520746573741b5b3230317e")

	// And with the mode off, the same intent is the text and nothing more.
	p.wait("PASTE-OFF")
	p.mustSend(IntentKindPaste, []byte("make test"))
	p.wait("PASTE2:6d616b652074657374")
}
