package app

// What the preamble is ALLOWED to name (nocx-luqz9.5, acceptance 2).
//
// The text a worker is handed tells it which calls it has. A name in that text
// that the registry does not declare is a worker sent to a call that does not
// exist: it will make the call, be refused, and — since the refusal is the only
// thing it learns — report nothing. The failure is silent in exactly the way
// this epic exists to end, so it is not left to a reader's eye: every tool name
// in the text is looked up in the ASSEMBLED registry here, and separately in
// the set the worker's own grant OFFERS it.
//
// TWO CHECKS, AND BOTH ARE LOAD-BEARING. A declaration can exist and still not
// reach a worker — that is A11's whole construction (a worker's grant names a
// workspace sub-scope and no session), so a preamble naming a coordinator's
// tool would satisfy the first check and fail the worker it was written for.
// The registry's declarations are the source of truth for both; the text is
// scanned, never copied, so a preamble edited to name a new tool is checked the
// moment the edit lands.
//
// AND THE CHECK IS PROVED TO BITE, in this same file, rather than assumed to:
// the negative half of the table below runs the SAME scan over a real preamble
// with one tool name swapped for the two ways a name can be wrong — one nothing
// declares, and one that is declared and withheld from a worker. A scan that
// had quietly stopped matching would pass every positive case here and fail
// those two.

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/workers"
)

// preambleToolName is the shape a worker's own call takes in the text: the
// namespace and the tool, exactly as the wire names them, and NOT the sentence
// punctuation around them — a scanner greedy for dots would read the full stop
// at the end of a clause as part of the tool's name and fail on prose that is
// perfectly correct. It is a scanner and not a list of expected names: a list
// here would be the second copy of the vocabulary this test exists to prevent.
var preambleToolName = regexp.MustCompile(`workers\.[a-z][a-z0-9]*(?:\.[a-z][a-z0-9]*)*`)

// preambleToolProblems names every tool the text sends a worker to that the
// registry does not declare, or declares without offering it to a worker.
//
// It answers rather than asserting so that the same check can be run over texts
// nobody ships — see the negative table below, which is what makes the positive
// case evidence instead of a claim.
func preambleToolProblems(t *testing.T, registry agenttools.Registry, text string) []string {
	t.Helper()
	names := preambleToolName.FindAllString(text, -1)
	if len(names) == 0 {
		// NOT AN EMPTY ANSWER: a text naming no tool at all is the one case
		// this scan cannot check, and saying so here is what stops a preamble
		// rewritten into prose from passing by having nothing to look up.
		return []string{"the text names no tool at all, so this check can say nothing about it"}
	}
	offered := make(map[string]bool)
	for _, tool := range registry.ForGrant(participantGrant(workerTestWorkspace)) {
		offered[tool.Name] = true
	}
	var problems []string
	for _, name := range names {
		if _, ok := registry.Lookup(name); !ok {
			problems = append(problems, name+" is named but no declaration in the registry carries it")
			continue
		}
		if !offered[name] {
			problems = append(problems, name+" is declared but this worker's own grant does not offer it")
		}
	}
	return problems
}

func TestEveryToolThePreambleNamesIsDeclaredAndOfferedToAWorker(t *testing.T) {
	registry, err := agenttools.Assemble(os.DirFS("../../contracts/tools"))
	if err != nil {
		t.Fatalf("assemble the real tool registry: %v", err)
	}
	text := workers.Preamble("sess-coordinator")
	for _, problem := range preambleToolProblems(t, registry, text) {
		t.Errorf("%s:\n%s", problem, text)
	}
}

// The negative half: the same scan, run over texts that ARE wrong, in each of
// the two ways a preamble can be. Each case is the REAL preamble with one name
// swapped, so what fails is the swap and not the prose around it.
func TestThePreambleToolScanRejectsANameAWorkerCannotCall(t *testing.T) {
	registry, err := agenttools.Assemble(os.DirFS("../../contracts/tools"))
	if err != nil {
		t.Fatalf("assemble the real tool registry: %v", err)
	}
	real := workers.Preamble("sess-coordinator")
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{
			name: "a tool nothing declares",
			text: strings.Replace(real, "workers.report", "workers.declare", 1),
			want: "no declaration in the registry carries it",
		},
		{
			name: "a tool declared but withheld from a worker",
			text: strings.Replace(real, "workers.inbox", "workers.spawn", 1),
			want: "this worker's own grant does not offer it",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The premise, so a case cannot pass because the replacement did
			// not land: the text must differ from the real one.
			if tc.text == real {
				t.Fatalf("the mutation did not apply, so this case asserts nothing:\n%s", tc.text)
			}
			problems := preambleToolProblems(t, registry, tc.text)
			if len(problems) == 0 {
				t.Fatalf("the scan passed a preamble naming %s:\n%s", tc.name, tc.text)
			}
			joined := strings.Join(problems, "; ")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("problems = %q, want one naming %q", joined, tc.want)
			}
		})
	}
}

// The two calls the design names are named. This test is the floor under the
// scan above: a preamble rewritten to talk about the mailbox and the report in
// prose — still true, still readable, and no longer something a worker can act
// on — passes the "every named tool exists" check by naming none.
func TestThePreambleNamesTheTwoCallsAWorkerActuallyHas(t *testing.T) {
	text := workers.Preamble("sess-coordinator")
	for _, want := range []string{"workers.report", "workers.inbox"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the preamble never names %s:\n%s", want, text)
		}
	}
}
