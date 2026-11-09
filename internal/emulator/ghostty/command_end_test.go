package ghostty

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// THE COMMAND-END MARK (nocx-evj4b.1.2): OSC 133 D, which every integration
// level writes and the authenticated one writes a fence beside. It carries a
// payload, so it is matched as a prefix and read to its BEL — these tests pin
// exactly that: the bare status form, a payload long enough to run past the
// prefix, the sequence straddled across two ingest calls, and a candidate that
// never terminates being abandoned rather than swallowed.
//
// A sighting authorises nothing (ADR-0024 decision 1): what it is FOR is the
// owner knowing a command is no longer running, on a shell whose lifecycle
// channel cannot write the fence.

func wantOneCommandEnd(t *testing.T, got []emulator.Effect) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("the drain holds %s, want exactly one command-end effect", describeEffects(got))
	}
	if got[0].Kind != emulator.EffectCommandEnd {
		t.Fatalf("the effect is %s, want a command end", describeEffects(got))
	}
	if len(got[0].Body) != 0 {
		t.Fatalf("the command end carries body %q, want none", got[0].Body)
	}
}

func TestTheCommandEndMarkIsSighted(t *testing.T) {
	// The plain form a shell with no authenticated channel writes.
	plain := newTerminal(t, 20, 4)
	ingest(t, plain, "\x1b]133;D;0\x07")
	wantOneCommandEnd(t, plain.Effects())

	// The authenticated form carries the fence nonce behind the status.
	tagged := newTerminal(t, 20, 4)
	ingest(t, tagged, "\x1b]133;D;1;1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef\x07")
	wantOneCommandEnd(t, tagged.Effects())

	// A mark split across two reads: the scanner's own state carries it.
	straddled := newTerminal(t, 20, 4)
	ingest(t, straddled, "\x1b]133;D")
	if got := straddled.Effects(); len(got) != 0 {
		t.Fatalf("a partial mark produced %s, want nothing yet", describeEffects(got))
	}
	ingest(t, straddled, ";0\x07")
	wantOneCommandEnd(t, straddled.Effects())
}

func TestACommandEndThatNeverTerminatesIsAbandoned(t *testing.T) {
	term := newTerminal(t, 20, 4)
	ingest(t, term, "\x1b]133;D;"+strings.Repeat("9", maxCommandEndPayload+16))
	if got := term.Effects(); len(got) != 0 {
		t.Fatalf("an unterminated command end produced %s, want nothing", describeEffects(got))
	}
	// And the scanner recovers: a well-formed mark after it is still seen.
	ingest(t, term, "\x1b]133;D;0\x07")
	wantOneCommandEnd(t, term.Effects())
}
