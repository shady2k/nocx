package assistant

// The program carrier's first question (nocx-d6gn4.6): can a chain of
// DEPENDENT effects happen inside one model turn, with the intermediate
// result never entering the model's context?
//
// That sentence is the epic's acceptance criterion, and this test is it at
// unit scale: a program that reads a file, and then reads the file the FIRST
// read named. Under the declared-call carrier that is two model turns and the
// index file's contents pass through the model on the way. Here the model
// wrote one program and the second argument was computed by the interpreter.
//
// files.read on both steps deliberately: it executes in Go (registry.go,
// Executes: InGo), so this test needs no renderer seam and stays about the
// carrier rather than about a fake.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestStarlarkCarrier_ASecondEffectUsesTheFirstEffectsResult(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	// The chain: index.txt names target.txt, and only reading index.txt can
	// tell the program which file to read next.
	writeFile(t, filepath.Join(dir, "index.txt"), "target.txt\n")
	writeFile(t, filepath.Join(dir, "target.txt"), "the answer is here")

	k := kernelFor(t, grant, &fakeLedger{})
	carrier := newStarlarkCarrier(k, dir)

	out, err := carrier.Run(context.Background(), `
name = files_read(path = "`+dir+`/index.txt")["text"].strip()
answer(files_read(path = "`+dir+`/" + name)["text"])
`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "the answer is here") {
		t.Fatalf("answer = %q, want the second file's contents", out)
	}
	// TWO effects happened, and the second one's argument was computed from
	// the first one's result — inside the interpreter, never in the model.
	calls := carrier.invocations()
	if len(calls) != 2 {
		t.Fatalf("invocations = %d (%+v), want exactly two", len(calls), calls)
	}
	if !strings.Contains(calls[1].rawArgs, "target.txt") {
		t.Fatalf("second call args = %q, want the name the first call returned", calls[1].rawArgs)
	}
}

// A program that never asks for an effect is still a program: no invocation
// happens, and the answer is whatever it computed. The null half — without it
// the test above cannot tell "the chain ran" from "anything at all ran".
func TestStarlarkCarrier_APureProgramReachesNoEffect(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	k := kernelFor(t, grant, &fakeLedger{})
	carrier := newStarlarkCarrier(k, dir)

	out, err := carrier.Run(context.Background(), `answer("nothing to do")`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "nothing to do" {
		t.Fatalf("answer = %q", out)
	}
	if n := len(carrier.invocations()); n != 0 {
		t.Fatalf("invocations = %d, want none", n)
	}
}

// A PROGRAM IS NOT A WAY ROUND THE GATE, and this is the test that says so.
// The same effect proposed by a program meets the same kernel, so a path
// outside the grant is refused exactly as it is refused for a declared call —
// and the refusal arrives as a value the program can read, not as a crash,
// because "a refusal is an answer" belongs to the kernel and every carrier
// inherits it without copying a line (nocx-uvac6.1).
func TestStarlarkCarrier_APathOutsideTheGrantIsRefusedByTheSameKernel(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	k := kernelFor(t, grant, &fakeLedger{})
	carrier := newStarlarkCarrier(k, dir)

	out, err := carrier.Run(context.Background(), `answer(files_read(path = "/etc/passwd"))`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "files.read") {
		t.Fatalf("answer = %q, want the refusal naming the tool", out)
	}
	if strings.Contains(out, "root:") {
		t.Fatalf("answer = %q — the file was read despite the grant", out)
	}
}

// And the vocabulary is an ALLOWLIST: a name the host did not declare does not
// exist for the program. Not "is refused" — is not a name at all, which is the
// same principle Registry.ForGrant states for the declared-call carrier ("the
// strongest refusal is the one never proposed").
func TestStarlarkCarrier_AnUndeclaredNameDoesNotExist(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	k := kernelFor(t, grant, &fakeLedger{})
	carrier := newStarlarkCarrier(k, dir)

	if _, err := carrier.Run(context.Background(), `answer(open("/etc/passwd").read())`); err == nil {
		t.Fatal("a program reached a name the host never declared")
	}
	if n := len(carrier.invocations()); n != 0 {
		t.Fatalf("invocations = %d, want none", n)
	}
}

// THE BOUNDS ARE THE WHOLE REASON THIS LANGUAGE WAS CHOSEN, so they are
// asserted rather than trusted to a dependency's defaults — the dialect used
// to come from the library's process-wide globals, which any other user of
// the library could widen from another package.
func TestStarlarkCarrier_TheDialectCannotLoop(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	carrier := newStarlarkCarrier(kernelFor(t, grant, &fakeLedger{}), dir)

	// The WANTED text is asserted, not merely "an error". A mutation run
	// found this test passing for the wrong reason: with `while` re-enabled
	// the loop still errored, because the step budget cut it off — so the
	// test said "the dialect refuses while" while actually observing "an
	// infinite loop eventually stops". Naming the refusal is what makes the
	// difference visible.
	for _, program := range []struct{ name, source, want string }{
		{"while", "while True:\n    pass\n", "does not support while loops"},
		{"recursion", "def f(n):\n    return f(n + 1)\nanswer(f(0))\n", "called recursively"},
		{"load", `load("evil.star", "x")`, "load"},
	} {
		_, err := carrier.Run(context.Background(), program.source)
		if err == nil {
			t.Fatalf("%s: a program with no bound ran", program.name)
		}
		if !strings.Contains(err.Error(), program.want) {
			t.Fatalf("%s: error = %v, want it to name %q — a bound that stops a program for another reason is not this bound", program.name, err, program.want)
		}
	}
}

// And a program that terminates but takes far too long getting there is cut
// off by the step budget — the bound the language's structure cannot give,
// because a finite collection can still be large and nested loops multiply.
//
// The counting happens INSIDE a function on purpose. The first version of
// this test reassigned a top-level name, which this dialect forbids
// (GlobalReassign: false), so it failed instantly on a binding error and said
// nothing about the budget at all — found by removing the budget and watching
// the test stay green.
func TestStarlarkCarrier_AProgramThatRunsTooLongIsCutOff(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	carrier := newStarlarkCarrier(kernelFor(t, grant, &fakeLedger{}), dir)

	_, err := carrier.Run(context.Background(), `
def burn():
    n = 0
    for i in range(100000):
        for j in range(100000):
            n += 1
    return n
answer(str(burn()))
`)
	if err == nil {
		t.Fatal("a program ran past the step budget")
	}
	if !strings.Contains(err.Error(), "too many steps") {
		t.Fatalf("error = %v, want the step budget to be what stopped it", err)
	}
	if n := len(carrier.invocations()); n != 0 {
		t.Fatalf("invocations = %d, want none — it never reached an effect", n)
	}
}

// SUSPENSION WITHOUT REPLAY (nocx-d6gn4.6). A program asks for an effect the
// policy says a person must answer for. The program does not fail and does not
// start again: the goroutine running the interpreter PARKS inside the
// intrinsic, the host is handed the question, and when it is answered the same
// call is made again and the program carries on with every local it already
// had.
//
// THE ANTI-REPLAY ASSERTION IS THE `answer("looking")` LINE. Under the
// Temporal-style replay the bead originally proposed, everything before the
// suspended effect runs again on resume, so that sentence would appear twice.
// It appears once. That is the whole reason the parked goroutine was chosen
// over replay, stated as something a test can see rather than as an argument.
func TestStarlarkCarrier_AnApprovalParksTheProgramRatherThanRestartingIt(t *testing.T) {
	grant, dir := testDirGrant(t, content.EffectPolicy{}) // the zero matrix asks for everything
	writeFile(t, filepath.Join(dir, "index.txt"), "target.txt\n")
	writeFile(t, filepath.Join(dir, "target.txt"), "the answer is here")

	approvals := NewApprovalStore()
	carrier := newStarlarkCarrier(kernelForWithApprovals(t, grant, &fakeLedger{}, approvals), dir)

	type outcome struct {
		out string
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		out, err := carrier.Run(context.Background(), `
answer("looking")
name = files_read(path = "`+dir+`/index.txt")["text"].strip()
answer(files_read(path = "`+dir+`/" + name)["text"])
`)
		done <- outcome{out, err}
	}()

	// Two effects, so two questions. Each is answered and released, and the
	// program continues from exactly where it stopped.
	for i := 0; i < 2; i++ {
		select {
		case s := <-carrier.Suspensions():
			req := s.Request
			if !approvals.Approve(Approval{
				RunID: req.RunID, Attempt: req.Attempt, Tool: req.Tool,
				CallID: req.CallID, ArgHash: req.ArgHash,
			}) {
				t.Fatalf("suspension %d: the proposal was not pending", i+1)
			}
			s.Resume()
		case r := <-done:
			t.Fatalf("the program finished without asking (%d asks seen): out=%q err=%v", i, r.out, r.err)
		}
	}

	r := <-done
	if r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}
	if !strings.Contains(r.out, "the answer is here") {
		t.Fatalf("answer = %q, want the second file's contents", r.out)
	}
	if n := strings.Count(r.out, "looking"); n != 1 {
		t.Fatalf("the sentence before the first effect appears %d times, want once — the program was replayed, not resumed", n)
	}
}
