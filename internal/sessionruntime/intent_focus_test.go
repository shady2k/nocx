package sessionruntime

// A focus report is one of the input vocabulary's three state-dependent kinds:
// DEC private mode 1004 is the whole of its content, so a program that did not
// ask is not told. The port answers an unrequested report with
// emulator.ErrUnsupported and no bytes — and that answer used to travel out of
// Commit as a REFUSAL, which the renderer's input path read as "the person's
// keystroke was not delivered": a focus change is not a keystroke, and a
// refusal is the vocabulary's word for input the program is owed and did not
// get. Nothing is owed here. The intent encodes to no bytes, exactly as any
// other input the program ignores does, and its record reports executed.
//
// The pair matters: a program that DID ask still gets its report, so the fix
// cannot be "swallow focus".

import "testing"

func TestAFocusReportAProgramDidNotAskForEncodesToNothingRatherThanBeingRefused(t *testing.T) {
	s, _, _ := realRuntime(t)
	ctrl, err := s.GrantControl(person())
	if err != nil {
		t.Fatalf("grant control: %v", err)
	}
	id, err := s.Admit(Intent{
		At: s.Incarnation(), Under: ctrl.Epoch, By: ctrl.Holder,
		Kind: IntentKindFocus, Payload: []byte("in"),
	})
	if err != nil {
		t.Fatalf("admit a focus intent: %v", err)
	}
	encoded, err := s.Commit(Intent{ID: id}, nil)
	if err != nil {
		t.Fatalf("a focus report to a program that did not ask for it was refused: %v", err)
	}
	if len(encoded) != 0 {
		t.Fatalf("a focus report to a program that did not ask for it encoded %q, want no bytes", encoded)
	}
	if got := s.IntentState(id); got != IntentStateExecuted {
		t.Errorf("the focus intent's record is %v, want executed (nothing owed, nothing refused)", got)
	}
}

func TestAFocusReportAProgramAskedForIsStillSent(t *testing.T) {
	s, _, _ := realRuntime(t)
	ctrl, err := s.GrantControl(person())
	if err != nil {
		t.Fatalf("grant control: %v", err)
	}
	// The program asks (DEC private mode 1004), through the one parser.
	if err = s.Ingest([]byte("\x1b[?1004h")); err != nil {
		t.Fatalf("the program enabling focus reporting was refused: %v", err)
	}
	id, err := s.Admit(Intent{
		At: s.Incarnation(), Under: ctrl.Epoch, By: ctrl.Holder,
		Kind: IntentKindFocus, Payload: []byte("in"),
	})
	if err != nil {
		t.Fatalf("admit a focus intent: %v", err)
	}
	encoded, err := s.Commit(Intent{ID: id}, nil)
	if err != nil {
		t.Fatalf("a focus report the program asked for was refused: %v", err)
	}
	if want := "\x1b[I"; string(encoded) != want {
		t.Errorf("focus gained = %q, want %q", encoded, want)
	}
}
