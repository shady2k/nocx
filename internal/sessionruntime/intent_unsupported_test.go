package sessionruntime

// ONE RULE, NOT A PER-KIND CASE (nocx-evj4b.1.12). The port's ErrUnsupported is
// a statement about the PROGRAM'S MODES, not about the intent: "the modes this
// program has set do not carry this event". Focus reporting (DEC 1004) is one;
// mouse motion is the other, and the more dangerous one — a program with
// 1000/1002 enabled reports button events, not motion, so every button-less
// motion the renderer emits is declined by the port. Left as an error, that
// answer became a REFUSAL, and a refusal the renderer retries is a queue that
// never moves: every later key, and the person's Ctrl-C, sat behind a motion
// the program was never going to be sent. Nothing is owed to the program when
// its own modes do not carry the event, so the intent encodes to NO bytes and
// its record reports executed.

import "testing"

func TestAMotionReportAProgramDidNotAskForEncodesToNothingRatherThanBeingRefused(t *testing.T) {
	s, term, _ := realRuntime(t)
	ctrl := sessionControl(t, s)

	// Button-event tracking: presses and releases are reported, motion without
	// a button held is not — the state a program enters with DEC 1000/1002.
	if err := s.Ingest([]byte("\x1b[?1000h\x1b[?1002h")); err != nil {
		t.Fatalf("the program enables button-event tracking: %v", err)
	}
	admitted, err := s.Admit(Intent{
		At: s.Incarnation(), Under: ctrl.Epoch, By: ctrl.Holder,
		Kind: IntentKindMouse, Payload: []byte("motion none 4 5"),
	})
	if err != nil {
		t.Fatalf("admit a motion intent: %v", err)
	}
	encoded, err := s.Commit(Intent{ID: admitted}, nil)
	if err != nil {
		t.Fatalf("a motion the program's modes do not report was refused: %v", err)
	}
	if len(encoded) != 0 {
		t.Fatalf("a motion the program's modes do not report encoded %q, want no bytes", encoded)
	}
	if got := s.IntentState(admitted); got != IntentStateExecuted {
		t.Errorf("the motion's record is %v, want executed (nothing owed, nothing refused)", got)
	}
	if got := term.Written(); len(got) != 0 {
		t.Fatalf("a motion no mode reports wrote %q to the PTY", got)
	}

	// The pair, so the rule cannot be "swallow mouse": with ANY-motion tracking
	// the same event is reported, and it must still reach the program.
	if err = s.Ingest([]byte("\x1b[?1003h")); err != nil {
		t.Fatalf("the program enables any-motion tracking: %v", err)
	}
	admitted, err = s.Admit(Intent{
		At: s.Incarnation(), Under: ctrl.Epoch, By: ctrl.Holder,
		Kind: IntentKindMouse, Payload: []byte("motion none 4 5"),
	})
	if err != nil {
		t.Fatalf("admit the motion again: %v", err)
	}
	encoded, err = s.Commit(Intent{ID: admitted}, nil)
	if err != nil {
		t.Fatalf("a motion the program asked for was refused: %v", err)
	}
	if len(encoded) == 0 {
		t.Fatalf("any-motion tracking is on and the motion encoded to no bytes")
	}
}
