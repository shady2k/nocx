package assistant

// The GRAPH carrier (nocx-d6gn4.11). Same payoff as the program carrier — a
// dependent chain inside one model turn — and one property the program
// carrier cannot have: the WHOLE plan can be shown to a person before
// anything runs.
//
// That is the trade the two carriers exist to compare. A program is
// Turing-complete, so an approval can only ever state the current resolved
// effect plus whatever static bounds are readable off the source. A validated
// graph states every effect site, its dependencies and its budget up front —
// and pays for it by being unable to express anything its node kinds do not
// have.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

const twoStepPlan = `{
  "steps": [
    {"id": "index",  "effect": "files.read", "args": {"path": "%q"}},
    {"id": "target", "effect": "files.read", "args": {"path": "%q + index.text.trim()"}},
    {"answer": "target.text"}
  ]
}`

func TestGraphCarrier_ASecondEffectUsesTheFirstEffectsResult(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "index.txt"), "target.txt\n")
	writeFile(t, filepath.Join(dir, "target.txt"), "the answer is here")

	carrier, err := newGraphCarrier(kernelFor(t, grant, &fakeLedger{}), planSource(dir))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	out, err := carrier.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "the answer is here") {
		t.Fatalf("answer = %q, want the second file's contents", out)
	}
	calls := carrier.invocations()
	if len(calls) != 2 {
		t.Fatalf("invocations = %d (%+v), want exactly two", len(calls), calls)
	}
	if !strings.Contains(calls[1].rawArgs, "target.txt") {
		t.Fatalf("second call args = %q, want the name the first step returned", calls[1].rawArgs)
	}
}

// THE POINT OF THIS CARRIER. Before a single effect runs, the plan can say
// what it will do: every effect site, the tool it calls, and which earlier
// steps it depends on. The dependency is READ OFF THE EXPRESSION, not inferred
// from order — which is also the asymmetry a comparison between the carriers
// has to carry, because a program's dependencies can only ever be guessed at.
func TestGraphCarrier_TheWholePlanIsReadableBeforeAnythingRuns(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	carrier, err := newGraphCarrier(kernelFor(t, grant, &fakeLedger{}), planSource(dir))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	preview := carrier.Preview()
	if len(preview) != 2 {
		t.Fatalf("preview = %+v, want both effect sites", preview)
	}
	if preview[0].Tool != "files.read" || len(preview[0].DependsOn) != 0 {
		t.Fatalf("first site = %+v, want files.read depending on nothing", preview[0])
	}
	if preview[1].Tool != "files.read" || len(preview[1].DependsOn) != 1 || preview[1].DependsOn[0] != "index" {
		t.Fatalf("second site = %+v, want files.read depending on index", preview[1])
	}
	// And it is a PREVIEW: nothing has happened yet.
	if n := len(carrier.invocations()); n != 0 {
		t.Fatalf("invocations = %d, want none before Run", n)
	}
}

// A plan that names a step that does not exist is refused BEFORE anything
// runs. Half a plan executed is worse than no plan executed: the effects that
// already happened cannot be taken back, and a person who approved a whole
// plan approved one that could finish.
func TestGraphCarrier_APlanIsRefusedWholeOrRunWhole(t *testing.T) {
	grant, _ := testDirGrant(t, autonomousMatrix())
	k := kernelFor(t, grant, &fakeLedger{})

	for _, bad := range []struct{ name, source string }{
		{"unknown step", `{"steps":[{"id":"a","effect":"files.read","args":{"path":"nope.missing"}},{"answer":"a.text"}]}`},
		{"unknown tool", `{"steps":[{"id":"a","effect":"files.delete","args":{"path":"'/tmp/x'"}},{"answer":"a.text"}]}`},
		{"duplicate id", `{"steps":[{"id":"a","effect":"files.read","args":{"path":"'/x'"}},{"id":"a","effect":"files.read","args":{"path":"'/y'"}},{"answer":"a.text"}]}`},
		{"forward reference", `{"steps":[{"id":"a","effect":"files.read","args":{"path":"b.text"}},{"id":"b","effect":"files.read","args":{"path":"'/y'"}},{"answer":"a.text"}]}`},
	} {
		if _, err := newGraphCarrier(k, bad.source); err == nil {
			t.Fatalf("%s: a broken plan was accepted", bad.name)
		}
	}
}

// planSource fills the fixture directory into the plan's CEL literals.
func planSource(dir string) string {
	return strings.Replace(
		strings.Replace(twoStepPlan, "%q", `'`+dir+`/index.txt'`, 1),
		"%q", `'`+dir+`/'`, 1)
}
