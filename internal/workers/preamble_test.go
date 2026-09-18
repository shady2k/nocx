package workers

// The rules nocx hands a worker before its task (nocx-luqz9.5; design §6).
//
// WHAT THIS PACKAGE CAN ASSERT ON THE TEXT, and what it deliberately leaves to
// internal/app: the tool NAMES the preamble sends the worker to are pinned
// there, because the declarations live in internal/agenttools and the
// composition root is the one place that holds both (worker_preamble_test.go).
// What is here is everything the record itself owns — who the coordinator is,
// which kinds a worker may use, and the one rule a worker cannot be left to
// guess.

import (
	"strings"
	"testing"
)

const preambleCoordinator = "0f2a5c1e-9d3b-4a77-8c0e-5b6d9f1a2c34"

// Criterion: the preamble NAMES the coordinator that started this worker, and
// still says who it is when nocx cannot name one — an absent session is not a
// licence to print a blank in the middle of a sentence about authority.
func TestThePreambleNamesTheCoordinatorThatStartedThisWorker(t *testing.T) {
	named := Preamble(preambleCoordinator)
	if !strings.Contains(named, preambleCoordinator) {
		t.Fatalf("the preamble does not name the coordinator session %q:\n%s", preambleCoordinator, named)
	}
	unnamed := Preamble("")
	if unnamed == named {
		t.Fatal("the preamble reads the same with and without a coordinator session to name")
	}
	if !strings.Contains(unnamed, "coordinator") {
		t.Fatalf("a spawn whose coordinator session nocx does not know says nothing about its coordinator:\n%s", unnamed)
	}
}

// Criterion: every kind a worker's tool may report is named in the text. The
// expectation is built from the record's own vocabulary (reportKinds) rather
// than from a copy of it written out here, so a kind added later reds this
// test until the preamble tells the worker it exists — which is the only way
// the worker will ever use it.
func TestThePreambleNamesEveryKindAWorkerMayReport(t *testing.T) {
	text := Preamble(preambleCoordinator)
	if len(reportKinds) < 3 {
		t.Fatalf("reportKinds = %v: this test asserts the three kinds the design names", reportKinds)
	}
	for _, kind := range reportKinds {
		if !strings.Contains(text, string(kind)) {
			t.Errorf("the preamble never names the kind %q a worker may report:\n%s", kind, text)
		}
	}
}

// Criterion: the preamble says a question does not block — asserted on the
// TEXT, so the sentence cannot be dropped silently (nocx-luqz9.5, acceptance
// 3). A worker that holds its turn open on workers.report is a worker whose
// whole turn is lost to a deadline, which is the mechanism this epic exists to
// replace.
func TestThePreambleSaysAQuestionDoesNotBlock(t *testing.T) {
	// Case-folded, because the sentence may be emphasised as it is said; what
	// the test is about is whether it is SAID.
	text := strings.ToLower(Preamble(preambleCoordinator))
	for _, want := range []struct {
		phrase string
		why    string
	}{
		{"end your turn", "the worker must END ITS TURN after asking, or the answer has nowhere to arrive"},
		{"does not wait", "the call is not a blocking ask, and a worker that assumes one waits out its own turn"},
		{"arrives as your next message", "the worker must know the answer reaches it as ordinary mail rather than as a reply"},
	} {
		if !strings.Contains(text, want.phrase) {
			t.Errorf("the preamble lost %q — %s:\n%s", want.phrase, want.why, text)
		}
	}
}

// Criterion: the preamble says who JUDGES, because the worker's own vocabulary
// has no word for success and a worker that invents one reports a verdict
// rather than a claim (ADR-0070 decision 3).
func TestThePreambleSaysTheCoordinatorJudgesAndNocxDoesNot(t *testing.T) {
	text := Preamble(preambleCoordinator)
	if !strings.Contains(text, "judges") {
		t.Errorf("the preamble does not say who decides what the worker's words mean:\n%s", text)
	}
	if !strings.Contains(text, "no success and no failure") {
		t.Errorf("the preamble leaves a worker free to believe nocx records an outcome:\n%s", text)
	}
}

// Criterion: a worker is told how to READ, not only how to write — the mail its
// coordinator leaves it is the only channel that reaches it besides the task.
func TestThePreambleTellsTheWorkerHowToReadItsMail(t *testing.T) {
	text := Preamble(preambleCoordinator)
	if !strings.Contains(text, "mail") {
		t.Errorf("the preamble never mentions the mail a coordinator leaves its worker:\n%s", text)
	}
}

// The two calls a worker has are named, and they are named as CALLS — the
// namespace and the tool. This is the floor under internal/app's scan (which
// proves every name here is one a worker is really offered): a preamble
// rewritten to describe the mailbox and the report in prose would pass that
// scan by naming no tool at all, and a worker reading prose has no call to
// make.
func TestThePreambleNamesTheToolCallsAWorkerMakes(t *testing.T) {
	text := Preamble(preambleCoordinator)
	for _, want := range []string{"workers.report", "workers.inbox"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the preamble does not name %s:\n%s", want, text)
		}
	}
}

// The text is what a queue PASTES into a pane, so a text nocx could not send as
// one message is not a preamble. It is asserted rather than trusted because the
// ways to get this wrong are all silent: a text that is only whitespace pastes
// as nothing, and a text carrying a NUL or a stray carriage return is refused
// by the first layer that sees it rather than by this one.
//
// AND IT CARRIES NO HARD LINE BREAK, which is the shape its own echo
// confirmation is measured on: a paste with no newline in it is answered with
// the text itself, drawn across however many rows the agent's box wraps it to,
// and that reading is captured and asserted in internal/agentdriver
// (`claude-2.1.272-wrapped-echo`, TestInputTextReadsASingleParagraphAcrossEvery
// WrappedRow). A paste that carries newlines takes the agent's
// `[Pasted text #N +M lines]` placeholder instead — accepted too, and a second
// mechanism between nocx and every worker in the fleet for no gain in what the
// rules say. This test is what makes that a decision somebody has to take
// deliberately rather than a newline that arrives with an edit.
func TestThePreambleIsAPasteableMessageOnOneLine(t *testing.T) {
	text := Preamble(preambleCoordinator)
	if strings.TrimSpace(text) == "" {
		t.Fatal("the preamble is blank")
	}
	for _, forbidden := range []string{"\x00", "\r", "\n"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("the preamble carries %q, which sends it down the multi-line paste's echo path:\n%q", forbidden, text)
		}
	}
	if text != strings.TrimRight(text, " \t") {
		t.Fatalf("the preamble ends in whitespace, which pastes as a trailing blank: %q", text)
	}
}
