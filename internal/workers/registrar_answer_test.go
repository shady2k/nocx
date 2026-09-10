package workers

// Answering a held participant's menu (nocx-f545a.4).
//
// The authority is Close's and Screen's, asked in their order, and the act is
// EffectSendInput — which is the one a human takeover suspends.

import (
	"context"
	"errors"
	"testing"
)

type fakeAnswerer struct {
	asked   []ParticipantID
	options []string
	answer  PaneAnswer
	err     error
}

func (f *fakeAnswerer) Answer(_ context.Context, p Participant, option string) (PaneAnswer, error) {
	f.asked = append(f.asked, p.ID)
	f.options = append(f.options, option)
	return f.answer, f.err
}

func registeredForAnswer(t *testing.T) (*harness, *fakeAnswerer, Registration) {
	t.Helper()
	h := newHarness(t)
	a := &fakeAnswerer{answer: PaneAnswer{Outcome: "submitted", State: "permission_choice"}}
	h.reg.answerer = a
	reg, err := h.reg.Register(context.Background(), RegisterRequest{
		Group: testGroup, CoordinatorSession: coordSession,
		Role: RoleWorker, Task: "read AGENTS.md and report", Command: "claude",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return h, a, reg
}

func TestTheHoldingSessionAnswersItsWorkersMenu(t *testing.T) {
	h, a, reg := registeredForAnswer(t)
	got, err := h.reg.Answer(context.Background(), coordSession, reg.ID, "Yes, I trust this folder")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if got.Outcome != "submitted" {
		t.Fatalf("answer = %+v, want what the answerer reported", got)
	}
	if len(a.asked) != 1 || a.asked[0] != reg.ID || a.options[0] != "Yes, I trust this folder" {
		t.Fatalf("answerer asked for %v %q, want exactly %q with the named option", a.asked, a.options, reg.ID)
	}
}

func TestAnotherSessionCannotAnswerAWorkersMenu(t *testing.T) {
	h, a, reg := registeredForAnswer(t)
	_, err := h.reg.Answer(context.Background(), "sess-somebody-else", reg.ID, "Yes")
	if !errors.Is(err, ErrNotHeld) {
		t.Fatalf("err = %v, want ErrNotHeld", err)
	}
	if len(a.asked) != 0 {
		t.Fatalf("a refused answer still reached the pane: %v", a.asked)
	}
}

// A person at the worker's keyboard suspends send-input, and answering a menu
// is send-input. The coordinator is refused and the pane is left to the person,
// while reading the screen is still allowed — observe survives the takeover.
func TestAHumanTakeoverStopsTheCoordinatorAnsweringButNotLooking(t *testing.T) {
	h, a, reg := registeredForAnswer(t)
	h.reg.screener = &fakeScreener{screen: PaneScreen{Readable: true}}
	del, err := h.store.Delegation(context.Background(), reg.ID)
	if err != nil {
		t.Fatalf("Delegation: %v", err)
	}
	del.State = DelegationInputSuspended
	if err := h.store.PutDelegation(context.Background(), del); err != nil {
		t.Fatalf("PutDelegation: %v", err)
	}

	if _, err := h.reg.Answer(context.Background(), coordSession, reg.ID, "Yes"); !errors.Is(err, ErrNotDelegated) {
		t.Fatalf("answer during a takeover: err = %v, want ErrNotDelegated", err)
	}
	if len(a.asked) != 0 {
		t.Fatalf("an answer during a takeover reached the pane: %v", a.asked)
	}
	if _, err := h.reg.Screen(context.Background(), coordSession, reg.ID); err != nil {
		t.Fatalf("looking during a takeover was refused: %v", err)
	}
}

func TestAnEndedWorkerHasNoMenuToAnswer(t *testing.T) {
	h, a, reg := registeredForAnswer(t)
	if _, err := h.reg.Exited(context.Background(), reg.ID, reg.Liveness, Exit{}); err != nil {
		t.Fatalf("Exited: %v", err)
	}
	if _, err := h.reg.Answer(context.Background(), coordSession, reg.ID, "Yes"); !errors.Is(err, ErrTerminal) {
		t.Fatalf("err = %v, want ErrTerminal", err)
	}
	if len(a.asked) != 0 {
		t.Fatalf("an ended worker's pane was answered: %v", a.asked)
	}
}

func TestAnUnwiredAnswererRefuses(t *testing.T) {
	h, _, reg := registeredForAnswer(t)
	h.reg.answerer = nil
	if _, err := h.reg.Answer(context.Background(), coordSession, reg.ID, "Yes"); err == nil {
		t.Fatal("an unwired answerer answered")
	}
}
