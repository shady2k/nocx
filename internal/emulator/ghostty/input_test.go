package ghostty

import (
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// The tests below drive the PORT, like the rest of the package's: what a paste
// or a mouse event turns into is the terminal's answer about its own state, and
// it is asserted as exact bytes rather than as "something came back". A
// non-empty check would pass on the wrong tracking mode, on the wrong output
// format, and on the right bytes followed by a stray one.

// TestPasteIsFramedByTheProgramsOwnMode proves the state dependence in both
// directions in one test: the same text is bare before the program asks for
// bracketed paste, wrapped in ESC[200~ … ESC[201~ after it asks, and bare again
// after it stops asking. A constant answer — always bracketed, or never — fails
// one of the three.
//
// The last two assertions are the two halves of the port's contract that the
// framing does not cover: an empty paste produces no bytes and no error, and
// the caller's own slice is not the encoder's scratch space.
func TestPasteIsFramedByTheProgramsOwnMode(t *testing.T) {
	term := newTerminal(t, 20, 4)
	const text = "pasted text"

	got, err := term.Paste([]byte(text))
	if err != nil {
		t.Fatalf("paste before bracketed paste was enabled: %v", err)
	}
	if string(got) != text {
		t.Errorf("unbracketed paste = %q, want the text itself %q", got, text)
	}

	ingest(t, term, "\x1b[?2004h")
	got, err = term.Paste([]byte(text))
	if err != nil {
		t.Fatalf("paste inside bracketed paste mode: %v", err)
	}
	if want := "\x1b[200~" + text + "\x1b[201~"; string(got) != want {
		t.Errorf("bracketed paste = %q, want %q", got, want)
	}

	ingest(t, term, "\x1b[?2004l")
	got, err = term.Paste([]byte(text))
	if err != nil {
		t.Fatalf("paste after bracketed paste was turned off: %v", err)
	}
	if string(got) != text {
		t.Errorf("paste after the mode was reset = %q, want the text itself %q", got, text)
	}

	if got, err = term.Paste(nil); err != nil || got != nil {
		t.Errorf("an empty paste gave (%q, %v), want no bytes and no error", got, err)
	}

	// The library's encoder rewrites the buffer it is given — that is how it
	// removes the bytes that cannot travel through a paste — so the buffer it
	// is given must not be the caller's. A caller that found its own slice
	// rewritten would have the bug in its hands and no way to see it.
	raw := []byte("a\x1bb\x00c")
	got, err = term.Paste(raw)
	if err != nil {
		t.Fatalf("paste of control bytes: %v", err)
	}
	if string(raw) != "a\x1bb\x00c" {
		t.Errorf("paste rewrote the caller's bytes to %q", raw)
	}
	if strings.ContainsAny(string(got), "\x1b\x00") {
		t.Errorf("paste let control bytes through: %q", got)
	}
}

// TestMouseIsEncodedInTheProgramsTrackingModeAndFormat proves that the bytes
// come from the PROGRAM's state and not from a constant: the same press at the
// same cell is ESC[<0;3;4M under SGR, the three-byte legacy form under normal
// tracking alone, a different final byte as a release, and ErrUnsupported with
// no bytes when the program is not tracking at all.
//
// The coordinates are the other half. The port speaks cells and is asked for
// cell (2,3); the wire speaks cells counted from one, so a press at (2,3) is
// 3;4 in both forms — an implementation that passed its caller's numbers
// straight through would be off by one in a way no test of "non-empty" would
// catch.
func TestMouseIsEncodedInTheProgramsTrackingModeAndFormat(t *testing.T) {
	term := newTerminal(t, 20, 4)
	press := emulator.MouseEvent{Action: emulator.MousePress, Button: emulator.MouseLeft, X: 2, Y: 3}

	// Nothing has asked to hear about the mouse.
	if got, err := term.Mouse(press); !errors.Is(err, emulator.ErrUnsupported) || got != nil {
		t.Fatalf("mouse with no tracking mode gave (%q, %v), want ErrUnsupported and no bytes", got, err)
	}

	// Normal tracking plus the SGR output format, which is what a modern
	// program sets: mode 1000 then mode 1006.
	ingest(t, term, "\x1b[?1000h\x1b[?1006h")
	got, err := term.Mouse(press)
	if err != nil {
		t.Fatalf("SGR mouse: %v", err)
	}
	if want := "\x1b[<0;3;4M"; string(got) != want {
		t.Errorf("SGR press at cell 2,3 = %q, want %q", got, want)
	}

	// The same tracking mode without the SGR format: the legacy encoding, one
	// byte per field with 32 added to each.
	ingest(t, term, "\x1b[?1006l")
	got, err = term.Mouse(press)
	if err != nil {
		t.Fatalf("legacy mouse: %v", err)
	}
	if want := "\x1b[M #$"; string(got) != want {
		t.Errorf("legacy press at cell 2,3 = %q, want %q", got, want)
	}

	// A release is the same position with the other final byte, so this also
	// pins the action and not just the position.
	ingest(t, term, "\x1b[?1006h")
	release := press
	release.Action = emulator.MouseRelease
	got, err = term.Mouse(release)
	if err != nil {
		t.Fatalf("SGR release: %v", err)
	}
	if want := "\x1b[<0;3;4m"; string(got) != want {
		t.Errorf("SGR release at cell 2,3 = %q, want %q", got, want)
	}

	// A motion with a button held — a drag — is what DECSET 1002 exists to
	// report, and it is encoded as motion plus button. It is asserted here
	// because the event's own button is the only thing that tells this port a
	// button is down, and an implementation that did not pass that on would
	// answer a drag with nothing at all.
	drag := emulator.MouseEvent{Action: emulator.MouseMotion, Button: emulator.MouseLeft, X: 4, Y: 2}
	ingest(t, term, "\x1b[?1002h")
	got, err = term.Mouse(drag)
	if err != nil {
		t.Fatalf("SGR drag motion: %v", err)
	}
	if want := "\x1b[<32;5;3M"; string(got) != want {
		t.Errorf("SGR drag to cell 4,2 = %q, want %q", got, want)
	}

	// And tracking off again, which is the other way the same call has to
	// report that the program is no longer listening.
	ingest(t, term, "\x1b[?1000l\x1b[?1002l")
	if got, err := term.Mouse(press); !errors.Is(err, emulator.ErrUnsupported) || got != nil {
		t.Errorf("mouse after tracking was turned off gave (%q, %v), want ErrUnsupported and no bytes", got, err)
	}
}

// TestFocusIsSentOnlyWhenTheProgramAskedForIt is the same state dependence for
// the third kind of input: the report is nothing to a program that did not ask
// for it, ESC[I when it gains focus and ESC[O when it loses it once it has, and
// nothing again after it stops asking.
//
// The two exact sequences are the point. Focus reporting is a rare mode, and a
// port that answered `CSI I` for both directions — or that sent the report
// regardless of the mode — would be writing two bytes into a program's input
// stream that it never asked for.
func TestFocusIsSentOnlyWhenTheProgramAskedForIt(t *testing.T) {
	term := newTerminal(t, 20, 4)

	if got, err := term.Focus(true); !errors.Is(err, emulator.ErrUnsupported) || got != nil {
		t.Fatalf("focus with mode 1004 unset gave (%q, %v), want ErrUnsupported and no bytes", got, err)
	}

	ingest(t, term, "\x1b[?1004h")
	got, err := term.Focus(true)
	if err != nil {
		t.Fatalf("focus gained: %v", err)
	}
	if want := "\x1b[I"; string(got) != want {
		t.Errorf("focus gained = %q, want %q", got, want)
	}
	got, err = term.Focus(false)
	if err != nil {
		t.Fatalf("focus lost: %v", err)
	}
	if want := "\x1b[O"; string(got) != want {
		t.Errorf("focus lost = %q, want %q", got, want)
	}

	ingest(t, term, "\x1b[?1004l")
	if got, err := term.Focus(true); !errors.Is(err, emulator.ErrUnsupported) || got != nil {
		t.Errorf("focus after the mode was reset gave (%q, %v), want ErrUnsupported and no bytes", got, err)
	}
}
