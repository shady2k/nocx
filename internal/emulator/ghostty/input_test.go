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

	// An empty paste outside bracketed paste is silence: there is no encoding
	// for "nothing" there, and an empty clipboard is not an error.
	if got, err = term.Paste(nil); err != nil || got != nil {
		t.Errorf("an empty paste gave (%q, %v), want no bytes and no error", got, err)
	}

	// INSIDE bracketed paste it is the opposite (nocx-1ebrs.14): the mode's
	// whole purpose is that the program is TOLD a paste happened, so an empty
	// body is a statement rather than an absence — it is how a client reports
	// a clipboard holding an image and no text, which the program then fetches
	// over its own path. Asserted as exact bytes, because the interesting
	// failure is the pair with something inside it.
	ingest(t, term, "\x1b[?2004h")
	if got, err = term.Paste(nil); err != nil {
		t.Fatalf("an empty paste inside bracketed paste mode: %v", err)
	} else if want := "\x1b[200~\x1b[201~"; string(got) != want {
		t.Errorf("an empty bracketed paste = %q, want the bare pair %q", got, want)
	}
	ingest(t, term, "\x1b[?2004l")

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

func TestAlternateScrollUsesTheProgramsCursorKeyMode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		modes string
		event emulator.MouseEvent
		want  string
	}{
		{"default alternate scroll up", "\x1b[?1049h", emulator.MouseEvent{Button: emulator.MouseWheelUp}, "\x1b[A"},
		{"normal down", "\x1b[?1049h\x1b[?1007h", emulator.MouseEvent{Button: emulator.MouseWheelDown}, "\x1b[B"},
		{"application up ignores wheel modifiers", "\x1b[?1049h\x1b[?1h", emulator.MouseEvent{Button: emulator.MouseWheelUp, Mods: emulator.Mods{Ctrl: true, Alt: true, Shift: true, Super: true}}, "\x1bOA"},
		{"application down", "\x1b[?1049h\x1b[?1h", emulator.MouseEvent{Button: emulator.MouseWheelDown}, "\x1bOB"},
		{"disabled alternate scroll", "\x1b[?1049h\x1b[?1007l", emulator.MouseEvent{Button: emulator.MouseWheelUp}, ""},
		{"primary never navigates", "\x1b[?1007h", emulator.MouseEvent{Button: emulator.MouseWheelDown}, ""},
		{"returned primary never navigates", "\x1b[?1049h\x1b[?1049l", emulator.MouseEvent{Button: emulator.MouseWheelUp}, ""},
		{"horizontal left has no vertical fallback", "\x1b[?1049h", emulator.MouseEvent{Button: emulator.MouseWheelLeft}, ""},
		{"horizontal right has no vertical fallback", "\x1b[?1049h", emulator.MouseEvent{Button: emulator.MouseWheelRight}, ""},
		{"wheel release has no fallback", "\x1b[?1049h", emulator.MouseEvent{Action: emulator.MouseRelease, Button: emulator.MouseWheelUp}, ""},
		{"wheel motion has no fallback", "\x1b[?1049h", emulator.MouseEvent{Action: emulator.MouseMotion, Button: emulator.MouseWheelDown}, ""},
		{"reporting precedes fallback with modifiers", "\x1b[?1049h\x1b[?1h\x1b[?1000h\x1b[?1006h", emulator.MouseEvent{Button: emulator.MouseWheelUp, Mods: emulator.Mods{Ctrl: true, Alt: true, Shift: true}}, "\x1b[<92;1;1M"},
		{"reporting survives disabled alternate scroll", "\x1b[?1049h\x1b[?1007l\x1b[?1000h\x1b[?1006h", emulator.MouseEvent{Button: emulator.MouseWheelDown}, "\x1b[<65;1;1M"},
		{"X10 reporting declines wheel without fallback", "\x1b[?1049h\x1b[?9h", emulator.MouseEvent{Button: emulator.MouseWheelUp}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := newTerminal(t, 20, 4)
			ingest(t, term, tc.modes)
			got, err := term.Mouse(tc.event)
			if tc.want == "" {
				if !errors.Is(err, emulator.ErrUnsupported) || got != nil {
					t.Fatalf("Mouse = (%q, %v), want no bytes and ErrUnsupported", got, err)
				}
			} else if err != nil || string(got) != tc.want {
				t.Fatalf("Mouse = (%q, %v), want (%q, nil)", got, err, tc.want)
			}
		})
	}
}

func TestAlternateScrollFollowsModeChangesBetweenEvents(t *testing.T) {
	term := newTerminal(t, 20, 4)
	ev := emulator.MouseEvent{Button: emulator.MouseWheelUp}
	for _, tc := range []struct {
		modes string
		want  string
	}{
		{"\x1b[?1049h", "\x1b[A"},
		{"\x1b[?1007l", ""},
		{"\x1b[?1007h\x1b[?1h", "\x1bOA"},
		{"\x1b[?1000h\x1b[?1006h", "\x1b[<64;1;1M"},
		{"\x1b[?1000l", "\x1bOA"},
		{"\x1b[?1049l", ""},
	} {
		ingest(t, term, tc.modes)
		got, err := term.Mouse(ev)
		if tc.want == "" {
			if !errors.Is(err, emulator.ErrUnsupported) || got != nil {
				t.Fatalf("after %q: Mouse = (%q, %v), want no bytes and ErrUnsupported", tc.modes, got, err)
			}
		} else if err != nil || string(got) != tc.want {
			t.Fatalf("after %q: Mouse = (%q, %v), want (%q, nil)", tc.modes, got, err, tc.want)
		}
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
