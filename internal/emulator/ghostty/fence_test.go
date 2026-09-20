package ghostty

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// The render fence (ADR-0024 §7 carve-out): the shell writes
// ESC]1337;NOCX_FENCE;<64 lowercase hex>BEL after a command's output, and the
// port reports it as an [emulator.EffectFence] carrying the nonce AND the rows
// it was drawn over. A row number is not enough — later output can overwrite
// or scroll those rows before the authenticated half of the handshake arrives
// — so every test here holds the effect in memory while it moves the screen
// underneath it.
//
// A sighted fence authorises nothing. These tests assert only that the
// emulator SEES the fence and reports what it landed on; completing a command
// is the runtime's authenticated half and is nobody's business here.
//
// The exact byte shape is the renderer's (`parseRenderFence` in
// frontend/src/renderers/xterm.ts) and the shell writer's
// (internal/shellintegration/scripts/nocx.bash); both sides must parse the
// same bytes, so the shape here is exact: prefix NOCX_FENCE;, then exactly 64
// LOWERCASE hex chars, then BEL.

// fenceNonce is a syntactically valid nonce: 64 lowercase hex chars.
const fenceNonce = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func fenceSeq(nonce string) string {
	return "\x1b]1337;NOCX_FENCE;" + nonce + "\x07"
}

// wantOneFence is the assertion every test here shares: the drained effects
// are exactly one fence, carrying this nonce and this source.
func wantOneFence(t *testing.T, got []emulator.Effect, nonce, source string) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("the drain holds %s, want exactly one fence effect", describeEffects(got))
	}
	if got[0].Kind != emulator.EffectFence {
		t.Fatalf("the effect is %s, want a fence", describeEffects(got))
	}
	if string(got[0].Body) != nonce {
		t.Fatalf("the fence carries nonce %q, want %q", got[0].Body, nonce)
	}
	if string(got[0].Source) != source {
		t.Fatalf("the fence carries source %q, want %q", got[0].Source, source)
	}
}

// screenText reads the whole active area as text, the way a row's consumer
// does: cells joined, width-spacer cells skipped. It is what the tests assert
// the screen AGAINST, so that "the row is gone" is measured and not assumed.
func screenText(t *testing.T, term emulator.Terminal, rows int) string {
	t.Helper()
	var sb strings.Builder
	for y := range rows {
		row, err := term.Row(y)
		if err != nil {
			t.Fatalf("read row %d: %v", y, err)
		}
		for _, cell := range row.Cells {
			if cell.Width == emulator.WidthSpacerTail || cell.Width == emulator.WidthSpacerHead {
				continue
			}
			sb.WriteString(cell.Grapheme)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// TestTheFenceEffectCarriesItsNonceAndRows is the sighting itself: output,
// then the fence, and the port reports one fence effect whose body is the
// nonce as the stream carried it and whose source is the row it landed on.
//
// The second terminal is the wrapped case: the fence's row is the last
// physical row of a logical line that spans three, and the source is all
// three — the rows the fence was DRAWN OVER, not just the one the cursor
// sits on. The third is two fences in one chunk: the scanner restarts, and
// each fence is its own effect.
func TestTheFenceEffectCarriesItsNonceAndRows(t *testing.T) {
	term := newTerminal(t, 20, 4)
	ingest(t, term, "the output row")
	if replies := ingest(t, term, fenceSeq(fenceNonce)); len(replies) != 0 {
		t.Errorf("the fence answered the program with %q, want nothing", replies)
	}
	wantOneFence(t, term.Effects(), fenceNonce, "the output row")

	wrapped := newTerminal(t, 10, 6)
	ingest(t, wrapped, "aaaaaaaaaabbbbbbbbbbccccc")
	ingest(t, wrapped, fenceSeq(fenceNonce))
	wantOneFence(t, wrapped.Effects(), fenceNonce, "aaaaaaaaaa\nbbbbbbbbbb\nccccc")

	twice := newTerminal(t, 20, 4)
	ingest(t, twice, "x"+fenceSeq(fenceNonce)+fenceSeq(fenceNonce))
	got := twice.Effects()
	if len(got) != 2 {
		t.Fatalf("two fences in one chunk produced %s, want exactly two effects", describeEffects(got))
	}
	for i, e := range got {
		if e.Kind != emulator.EffectFence || string(e.Body) != fenceNonce {
			t.Fatalf("fence %d is %s, want a fence carrying %q", i, describeEffects(got), fenceNonce)
		}
		if string(e.Source) != "x" {
			t.Fatalf("fence %d carries source %q, want %q", i, e.Source, "x")
		}
	}
}

// TestTheFenceOutlivesTheRowsItWasDrawnOver is the reason the effect carries
// content and not a row number: after the fence, output overwrites the row
// and scrolls it away, and the effect the caller already holds is unchanged.
// The screen's departure is asserted first — a pin that "survives" a screen
// that never moved proves nothing.
//
// The second half is the same-chunk case: the fence and the output that
// overwrites it arrive in ONE ingest, and the fence's source is what was on
// the screen at the fence, not what followed it. The output after the fence
// must still reach the screen — the fence splits the feed, it never eats
// bytes.
func TestTheFenceOutlivesTheRowsItWasDrawnOver(t *testing.T) {
	term := newTerminal(t, 20, 4)
	ingest(t, term, "the drawn row")
	ingest(t, term, fenceSeq(fenceNonce))
	held := term.Effects()
	wantOneFence(t, held, fenceNonce, "the drawn row")

	ingest(t, term, strings.Repeat("x\r\n", 10))
	if text := screenText(t, term, 4); strings.Contains(text, "the drawn row") {
		t.Fatalf("the screen still holds %q, so the trim this test is about did not happen", "the drawn row")
	}
	if string(held[0].Source) != "the drawn row" {
		t.Fatalf("the held fence's source moved to %q, want %q", held[0].Source, "the drawn row")
	}
	if string(held[0].Body) != fenceNonce {
		t.Fatalf("the held fence's nonce moved to %q, want %q", held[0].Body, fenceNonce)
	}

	same := newTerminal(t, 20, 4)
	ingest(t, same, "before"+fenceSeq(fenceNonce)+"\r\nafter")
	got := same.Effects()
	wantOneFence(t, got, fenceNonce, "before")
	if text := screenText(t, same, 4); !strings.Contains(text, "after") {
		t.Fatalf("the screen does not hold the output that followed the fence; the feed lost bytes: %q", text)
	}
}

// TestForeignAndMalformedFencesYieldNothing is the negative half, and it is
// what keeps the fence from being "any OSC produces something": OSC 1337 is a
// private namespace (iTerm2 file transfer lives there), the recovery fence is
// the same ident with a different key, and a nonce that is not exactly 64
// lowercase hex chars is not ours. None of them produce an effect.
//
// The last case is the scanner's recovery: an unterminated candidate that a
// later byte kills must not stick to the stream — the real fence after it
// lands exactly once.
func TestForeignAndMalformedFencesYieldNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		chunk string
	}{
		{"recovery key", "\x1b]1337;NOCX_RECOVERY;" + fenceNonce + "\x07"},
		{"other 1337 key", "\x1b]1337;SomethingElse;" + fenceNonce + "\x07"},
		{"short nonce", "\x1b]1337;NOCX_FENCE;" + fenceNonce[:63] + "\x07"},
		{"long nonce", "\x1b]1337;NOCX_FENCE;" + fenceNonce + "ff\x07"},
		{"uppercase nonce", "\x1b]1337;NOCX_FENCE;" + strings.ToUpper(fenceNonce) + "\x07"},
		{"non-hex nonce", "\x1b]1337;NOCX_FENCE;" + strings.Repeat("g", 64) + "\x07"},
		{"unterminated", "\x1b]1337;NOCX_FENCE;" + fenceNonce},
		{"empty payload", "\x1b]1337;\x07"},
		{"osc 133 marker", "\x1b]133;D;0\x07"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := newTerminal(t, 20, 4)
			ingest(t, term, "out "+tc.chunk+" more")
			if got := term.Effects(); len(got) != 0 {
				t.Fatalf("%s produced %s, want no effects", tc.name, describeEffects(got))
			}
		})
	}

	t.Run("recovers after a dead candidate", func(t *testing.T) {
		term := newTerminal(t, 20, 4)
		ingest(t, term, "\x1b]1337;NOCX_FENCE;"+fenceNonce)
		if got := term.Effects(); len(got) != 0 {
			t.Fatalf("the unterminated fence produced %s, want no effects", describeEffects(got))
		}
		ingest(t, term, fenceSeq(fenceNonce))
		wantOneFence(t, term.Effects(), fenceNonce, "")
	})
}

// TestTheFenceIsFoundAtEverySplitPosition is the chunk-boundary rule: the pty
// hands the emulator bytes in whatever reads arrive, so the same bytes split
// at EVERY position — 0 through the whole length, both empty halves included
// — yield the same single effect. The baseline is read once from an unsplit
// terminal and every split is held to it.
func TestTheFenceIsFoundAtEverySplitPosition(t *testing.T) {
	bytes := []byte("out" + fenceSeq(fenceNonce))

	base := newTerminal(t, 20, 4)
	ingest(t, base, string(bytes))
	baseEffects := base.Effects()
	wantOneFence(t, baseEffects, fenceNonce, "out")

	for split := range len(bytes) + 1 {
		term := newTerminal(t, 20, 4)
		ingest(t, term, string(bytes[:split]))
		ingest(t, term, string(bytes[split:]))
		got := term.Effects()
		if len(got) != 1 {
			t.Fatalf("split %d produced %s, want exactly one fence effect", split, describeEffects(got))
		}
		if string(got[0].Body) != string(baseEffects[0].Body) ||
			string(got[0].Source) != string(baseEffects[0].Source) {
			t.Fatalf("split %d produced nonce %q source %q, want the unsplit nonce %q source %q",
				split, got[0].Body, got[0].Source, baseEffects[0].Body, baseEffects[0].Source)
		}
	}
}

// A second nonce is only ever seen by a test that uses one. The scanner keeps
// the nonce it is matching in a field it reuses for the next candidate, so an
// effect that carried a slice OF that field rather than a copy of it would be
// rewritten by the next fence — silently, after the caller already holds it,
// and invisibly to any test whose two fences share a nonce.
//
// This is a test the mutation asked for: replacing the copy in sightFence
// with an alias leaves every other test in this file green, because they
// drain between fences or repeat one nonce. It fires here and only here.
const fenceNonceSecond = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

func TestEachFenceKeepsItsOwnNonceWhenAnotherFollows(t *testing.T) {
	term := newTerminal(t, 20, 4)
	// Both fences in ONE feed, drained together afterwards: the first
	// effect is held across the second sighting, which is the only moment
	// an aliased buffer could be rewritten under it.
	ingest(t, term, "x"+fenceSeq(fenceNonce)+fenceSeq(fenceNonceSecond))

	got := term.Effects()
	if len(got) != 2 {
		t.Fatalf("two fences produced %s, want exactly two effects", describeEffects(got))
	}
	if string(got[0].Body) != fenceNonce {
		t.Errorf("the first fence carries nonce %q, want %q — the second fence rewrote it", got[0].Body, fenceNonce)
	}
	if string(got[1].Body) != fenceNonceSecond {
		t.Errorf("the second fence carries nonce %q, want %q", got[1].Body, fenceNonceSecond)
	}

	// And across drains, which is how the helper actually reads them: the
	// effect the caller took away stays its own after later output.
	across := newTerminal(t, 20, 4)
	ingest(t, across, "y"+fenceSeq(fenceNonce))
	first := across.Effects()
	if len(first) != 1 {
		t.Fatalf("one fence produced %s, want exactly one effect", describeEffects(first))
	}
	held := first[0]
	ingest(t, across, fenceSeq(fenceNonceSecond))
	if string(held.Body) != fenceNonce {
		t.Errorf("the drained fence's nonce became %q after a later fence, want %q", held.Body, fenceNonce)
	}
}
