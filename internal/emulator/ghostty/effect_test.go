package ghostty

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// The tests below drive the PORT — emulator.Terminal — and never the adapter's
// own types, for the same reason the rest of the package's tests do: the
// effects are a contract with the session runtime, and the runtime will hold an
// emulator.Terminal.
//
// Each effect is asserted with its KIND AND ITS BODY, because either one alone
// is satisfiable by a wrong answer: a bell reported as a title change has the
// right shape and the wrong meaning, and a title reported with an empty body is
// a title the runtime cannot set.

// TestEffectsAreTheProgramsNonVisualRequests is the whole effect path in one
// chunk: a program rings, retitles, reports a directory, writes the clipboard
// and asks for a notification, and the port reports those five things, in that
// order, with those arguments.
//
// The two sequences AFTER them are the negative half, and they are what keeps
// this from being satisfied by "any escape sequence produces something": OSC
// 9;4 is a progress report and OSC 133 is a shell-integration fence, neither is
// an effect, and an implementation that installed a callback for every OSC
// would report them. The count assertion is what makes that bind — the five
// effects asked for arrived and nothing else did.
func TestEffectsAreTheProgramsNonVisualRequests(t *testing.T) {
	term := newTerminal(t, 20, 4)
	// The replies are captured and asserted empty: an effect is REPORTED, not
	// ANSWERED. A program that sets a title is owed nothing back, and an
	// implementation that turned an effect into a reply would be writing bytes
	// into the program's input that no protocol asked for.
	replies := ingest(t, term, ""+
		"\x07"+
		"\x1b]2;a title\x07"+
		"\x1b]7;file://host/tmp\x07"+
		"\x1b]52;c;aGVsbG8=\x07"+ // base64 of "hello": the port carries the payload, not the encoding
		"\x1b]9;build finished\x07"+
		"\x1b]9;4;1;50\x07"+ // progress: a hint, deliberately not an effect
		"\x1b]133;D;0\x07") // a shell-integration fence: not an effect either
	if len(replies) != 0 {
		t.Errorf("the chunk answered the program with %q, want nothing", replies)
	}

	want := []emulator.Effect{
		{Kind: emulator.EffectBell},
		{Kind: emulator.EffectTitle, Body: []byte("a title")},
		{Kind: emulator.EffectCwdReport, Body: []byte("file://host/tmp")},
		{Kind: emulator.EffectClipboard, Body: []byte("hello")},
		{Kind: emulator.EffectNotification, Body: []byte("build finished")},
	}
	got := term.Effects()
	if len(got) != len(want) {
		t.Fatalf("the chunk produced %s, want exactly %d effects", describeEffects(got), len(want))
	}
	for i := range want {
		if got[i].Kind != want[i].Kind {
			t.Errorf("effect %d is kind %s, want %s", i, effectKindName(got[i].Kind), effectKindName(want[i].Kind))
		}
		if string(got[i].Body) != string(want[i].Body) {
			t.Errorf("effect %d body = %q, want %q", i, got[i].Body, want[i].Body)
		}
	}

	// OSC 0 is the other spelling of the same request, and it is the same
	// effect: which of the two sequences set the title is not something a
	// consumer can act on.
	ingest(t, term, "\x1b]0;another\x07")
	got = term.Effects()
	if len(got) != 1 || got[0].Kind != emulator.EffectTitle || string(got[0].Body) != "another" {
		t.Errorf("OSC 0 produced %s, want one title effect reading \"another\"", describeEffects(got))
	}
}

// TestEffectsDrainOnce is the half of the delivery rule a caller is most likely
// to get wrong: an effect is read exactly once. A second call returning the same
// list would ring the bell twice, retitle the window twice and write the
// clipboard twice, and nothing in a caller's code would look wrong.
func TestEffectsDrainOnce(t *testing.T) {
	term := newTerminal(t, 20, 4)
	if got := term.Effects(); got != nil {
		t.Fatalf("a fresh terminal has effects %s, want none", describeEffects(got))
	}
	ingest(t, term, "\x07")
	if got := term.Effects(); len(got) != 1 {
		t.Fatalf("after a BEL the effects are %s, want one", describeEffects(got))
	}
	if got := term.Effects(); got != nil {
		t.Errorf("the effects were handed over twice: %s", describeEffects(got))
	}
}

// describeEffects renders an effect list for a failure message: the kinds by
// name and the bodies as quoted bytes, because an effect that is wrong is
// usually wrong in one of those two and a bare count says neither.
func describeEffects(effects []emulator.Effect) string {
	var sb strings.Builder
	sb.WriteString("[")
	for i, e := range effects {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(effectKindName(e.Kind))
		if len(e.Body) > 0 {
			sb.WriteString(fmt.Sprintf(" %q", e.Body))
		}
	}
	sb.WriteString("]")
	return sb.String()
}

func effectKindName(k emulator.EffectKind) string {
	switch k {
	case emulator.EffectBell:
		return "bell"
	case emulator.EffectNotification:
		return "notification"
	case emulator.EffectClipboard:
		return "clipboard"
	case emulator.EffectTitle:
		return "title"
	case emulator.EffectCwdReport:
		return "cwd"
	default:
		return fmt.Sprintf("none(%d)", k)
	}
}
