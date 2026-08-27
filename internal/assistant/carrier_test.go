package assistant

// THE SWITCH (nocx-d6gn4.8): a person chooses how the assistant composes
// multi-step work, and the next question is answered by the method they
// chose.
//
// These tests exercise the choice through Ask — the seam a question really
// arrives at — rather than through a carrier's own constructor. A carrier
// with its own unit tests and no way to reach it from a question is a
// feature that does not exist (AGENTS.md rule 2, and rule 5's "is the code
// reachable"), which is the state both new carriers were in until this.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// programSource is what a model writes under the program carrier: a chain of
// two DEPENDENT reads, where only the first read can say which file the
// second one names.
func programSource(dir string) string {
	return `
name = files_read(path = "` + dir + `/index.txt")["text"].strip()
answer(files_read(path = "` + dir + `/" + name)["text"])
`
}

func chainedFiles(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "index.txt"), "target.txt\n")
	writeFile(t, filepath.Join(dir, "target.txt"), "the answer is here")
}

// jsonArgs is the model's arguments as a provider sends them: a JSON object,
// as a string.
func jsonArgs(t *testing.T, obj map[string]any) string {
	t.Helper()
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return string(b)
}

// TestAsk_TheChosenCarrierIsTheOneTheModelIsOffered is the switch itself, in
// every direction the bead demands. What a carrier IS, from the model's side,
// is the set of tools it is shown — so this reads the tool set off the real
// request the engine sent.
func TestAsk_TheChosenCarrierIsTheOneTheModelIsOffered(t *testing.T) {
	for _, tc := range []struct {
		carrier CarrierKind
		offered string
		absent  string
	}{
		{CarrierCalls, `"files.read"`, `"run_program"`},
		{CarrierProgram, `"run_program"`, `"files.read"`},
		{CarrierGraph, `"run_plan"`, `"files.read"`},
	} {
		t.Run(string(tc.carrier), func(t *testing.T) {
			grant, _ := testDirGrant(t, autonomousMatrix())
			f, srv := newFakeOpenAI(nil)
			defer srv.Close()

			cl, clErr := newClient(nil, os.DirFS(realToolsFS))
			if clErr != nil {
				t.Fatalf("newClient: %v", clErr)
			}
			p := askParams(srv.URL, &grant, &fakeLedger{}, NewApprovalStore())
			p.Carrier = tc.carrier
			if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
				t.Fatalf("Ask: %v", err)
			}
			body, _ := f.lastBody.Load().(string)
			if !strings.Contains(body, tc.offered) {
				t.Fatalf("%s: the model was not offered %s\n%s", tc.carrier, tc.offered, body)
			}
			if strings.Contains(body, tc.absent) {
				t.Fatalf("%s: the model was still offered %s", tc.carrier, tc.absent)
			}
		})
	}
}

// TestAsk_TheProgramCarrierChainsTwoEffectsInOneModelTurn is the epic's
// sentence measured at the seam a question arrives at: two dependent effects,
// one model response that proposed anything, and the intermediate value never
// sent to the provider.
func TestAsk_TheProgramCarrierChainsTwoEffectsInOneModelTurn(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	chainedFiles(t, dir)

	f, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{
		name: "run_program",
		args: jsonArgs(t, map[string]any{"source": programSource(dir)}),
	}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	p := askParams(srv.URL, &grant, &fakeLedger{}, NewApprovalStore())
	p.Carrier = CarrierProgram

	var calls []ToolCall
	err := cl.Ask(context.Background(), p, func(e AskEvent) error {
		if e.Kind == AskToolCall && e.Call != nil {
			calls = append(calls, *e.Call)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("effects = %d (%+v), want the two the program chained", len(calls), calls)
	}
	body, _ := f.lastBody.Load().(string)
	if !strings.Contains(body, "the answer is here") {
		t.Fatalf("the program's answer never reached the model:\n%s", body)
	}
	// TWO completions: the one that wrote the program, and the one that said
	// the answer. Under the declared-call carrier the same chain is three.
	if n := f.requests.Load(); n != 2 {
		t.Fatalf("model round trips = %d, want 2", n)
	}
}

// The graph carrier reached the same way, so "reachable from a question" is
// asserted for it too and not inferred from the program carrier's passing.
func TestAsk_TheGraphCarrierWalksThePlanItWasGiven(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	chainedFiles(t, dir)

	f, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{
		name: "run_plan",
		args: jsonArgs(t, map[string]any{"plan": planSource(dir)}),
	}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	p := askParams(srv.URL, &grant, &fakeLedger{}, NewApprovalStore())
	p.Carrier = CarrierGraph

	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	body, _ := f.lastBody.Load().(string)
	if !strings.Contains(body, "the answer is here") {
		t.Fatalf("the plan's answer never reached the model:\n%s", body)
	}
}

// TestAsk_AParkedProgramContinuesWhereItStoppedAcrossTheApproval is the
// suspension half of nocx-d6gn4.6 at the seam that actually suspends.
//
// The transport does not resume an eino checkpoint — it calls Ask AGAIN with
// the original messages and the model re-rolls (approval_resume_test.go). For
// a program carrier that would be a replay with no journal: every effect
// before the question would happen a second time. So the run's program is
// parked, and the next Ask CONTINUES it rather than starting another one.
//
// The observable is ordering, and it is decisive: a restarted program walks
// its chain from the top, so the first file would be read again AFTER the
// second one had been.
func TestAsk_AParkedProgramContinuesWhereItStoppedAcrossTheApproval(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	chainedFiles(t, dir)

	approvals := NewApprovalStore()
	_, srv := newFakeOpenAI(reProposingModel("run_program",
		jsonArgs(t, map[string]any{"source": programSource(dir)})))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	p := askParams(srv.URL, &grant, &fakeLedger{}, approvals)
	p.Carrier = CarrierProgram

	// Every question the run asks gets a yes, and the run is re-driven the
	// way the transport re-drives it: same params, same run id. Each
	// EXECUTED effect announces itself once (kernel.onCall), so the events
	// collected across every drive are the whole run's effects in order.
	var ran []string
	answered := 0
	for answered < 4 {
		err := cl.Ask(context.Background(), p, func(e AskEvent) error {
			if e.Kind == AskToolCall && e.Call != nil {
				ran = append(ran, fmt.Sprintf("%v", e.Call.Args["path"]))
			}
			return nil
		})
		if err == nil {
			break
		}
		var ask *ApprovalRequestedError
		if !errors.As(err, &ask) || ask.Request == nil {
			t.Fatalf("Ask %d: %v", answered, err)
		}
		if !approvals.Approve(Approval{
			RunID:   ask.Request.RunID,
			Attempt: ask.Request.Attempt,
			Tool:    ask.Request.Tool,
			CallID:  ask.Request.CallID,
			ArgHash: ask.Request.ArgHash,
		}) {
			t.Fatalf("the proposal the kernel asked about was not pending")
		}
		answered++
	}
	if answered != 2 {
		t.Fatalf("questions asked = %d, want one per effect in the chain", answered)
	}

	// TWO effects for a two-effect chain. A program that restarted after the
	// person's answer would read the first file again on its way back to
	// where it had already been, and that read is an effect that happened
	// twice because of an approval.
	want := []string{filepath.Join(dir, "index.txt"), filepath.Join(dir, "target.txt")}
	if len(ran) != len(want) {
		t.Fatalf("effects executed = %v, want the chain run once: %v", ran, want)
	}
	for i := range want {
		if ran[i] != want[i] {
			t.Fatalf("effects executed = %v, want %v", ran, want)
		}
	}
}

// THE GRANT IS THE VOCABULARY, for a composing carrier as much as for the
// declared-call one. A tool that exists in the registry but is outside this
// run's grant must not be a name a program can spell or a plan can name — and
// for the plan carrier the refusal has to happen at VALIDATION, because a
// preview showing a person a step that was never going to be allowed is worse
// than no preview at all.
//
// session.list is the case: a real registry row, excluded from a path-scoped
// grant because it needs a session resource.
func TestAsk_AComposingCarrierCannotNameAToolOutsideTheGrant(t *testing.T) {
	grant, _ := testDirGrant(t, autonomousMatrix())
	k := kernelFor(t, grant, &fakeLedger{})

	if names := permittedNames(k); len(names) == 0 {
		t.Fatalf("the fixture grant permits nothing; the test would prove nothing")
	}
	for _, name := range permittedNames(k) {
		if name == "session.list" {
			t.Fatalf("session.list is inside this grant; pick a tool that is not")
		}
	}

	// The plan carrier refuses it whole, before running anything.
	outside := `{"steps":[{"id":"a","effect":"session.list","args":{}},{"answer":"'done'"}]}`
	if _, err := newGraphCarrier(granted(k), outside); err == nil {
		t.Fatal("a plan named a tool outside the grant and was accepted")
	}

	// And a program cannot even spell it: the allowlist is the vocabulary, so
	// the name does not exist rather than being refused.
	sc := newStarlarkCarrier(granted(k), permittedNames(k))
	if _, err := sc.Run(context.Background(), `answer(session_list())`); err == nil {
		t.Fatal("a program reached a tool outside the grant")
	}
	if n := len(sc.invocations()); n != 0 {
		t.Fatalf("effects = %d, want none", n)
	}
}

// A carrier nobody declared is not a silent fallback to the shipped one. A
// value that reached AskParams from a settings document somebody edited by
// hand is an error with the value in it, because a run that quietly used a
// different method than the one recorded would make every measurement this
// experiment takes unattributable.
func TestAsk_AnUnknownCarrierIsRefusedRatherThanGuessed(t *testing.T) {
	grant, _ := testDirGrant(t, autonomousMatrix())
	_, srv := newFakeOpenAI(nil)
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	p := askParams(srv.URL, &grant, &fakeLedger{}, NewApprovalStore())
	p.Carrier = CarrierKind("interpretive-dance")
	err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "interpretive-dance") {
		t.Fatalf("Ask = %v, want a refusal naming the carrier", err)
	}
}

// TestAsk_AParkedProgramSurvivesTheDeathOfTheAskThatParkedIt is the same
// resume as the test above, driven the way the TRANSPORT drives it: each Ask
// runs on its own admission task, and that task's context is cancelled the
// moment the Ask returns. The parked program outlives it — it is parked
// BETWEEN asks by construction — so a continuation that dies with the ask
// that parked it is a program every approval kills.
func TestAsk_AParkedProgramSurvivesTheDeathOfTheAskThatParkedIt(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	chainedFiles(t, dir)

	approvals := NewApprovalStore()
	_, srv := newFakeOpenAI(reProposingModel("run_program",
		jsonArgs(t, map[string]any{"source": programSource(dir)})))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	p := askParams(srv.URL, &grant, &fakeLedger{}, approvals)
	p.Carrier = CarrierProgram

	answered := 0
	for answered < 4 {
		askCtx, cancelAsk := context.WithCancel(context.Background())
		err := cl.Ask(askCtx, p, func(AskEvent) error { return nil })
		// The task is over the moment Ask returns, and its context with it.
		cancelAsk()
		if err == nil {
			break
		}
		var ask *ApprovalRequestedError
		if !errors.As(err, &ask) || ask.Request == nil {
			t.Fatalf("Ask %d: %v", answered, err)
		}
		if !approvals.Approve(Approval{
			RunID:   ask.Request.RunID,
			Attempt: ask.Request.Attempt,
			Tool:    ask.Request.Tool,
			CallID:  ask.Request.CallID,
			ArgHash: ask.Request.ArgHash,
		}) {
			t.Fatalf("the proposal the kernel asked about was not pending")
		}
		answered++
	}
	if answered != 2 {
		t.Fatalf("questions asked = %d, want one per effect in the chain", answered)
	}
}
