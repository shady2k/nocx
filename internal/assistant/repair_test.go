package assistant

// WHAT THE MODEL GETS BACK FROM A COMPOSING CARRIER (nocx-d6gn4.8).
//
// Found on a live model, on the first question ever asked through the program
// carrier: the model wrote a program, the program did not compile, and the
// person read "the model failed to answer. The details are in nocx's log."
// The framework had wrapped the diagnostic as a NodeRunError and nothing could
// name the cause — over a missing parenthesis.
//
// Under the declared-call carrier that has never been possible: a call the
// kernel refuses comes back as a RESULT the model reads and works around. A
// program that does not compile is the same class of event one level up, so it
// comes back the same way.

import (
	"context"
	"os"
	"strings"
	"testing"
)

// A program that does not compile, or throws, must come back to the MODEL as
// something it can repair — not kill the run.
func TestAsk_ABrokenProgramIsRepairableRatherThanFatal(t *testing.T) {
	grant, _ := testDirGrant(t, autonomousMatrix())
	f, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{
		name: "run_program",
		args: jsonArgs(t, map[string]any{"source": "this is not a program ((("}),
	}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	p := askParams(srv.URL, &grant, &fakeLedger{}, NewApprovalStore())
	p.Carrier = CarrierProgram

	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("a broken program failed the whole run: %v", err)
	}
	body, _ := f.lastBody.Load().(string)
	if !strings.Contains(body, "program failed") {
		t.Fatalf("the model was never told what was wrong with its program:\n%s", body)
	}
}

// Same for a plan the walker could not finish.
func TestAsk_AnUnrunnablePlanIsRepairableRatherThanFatal(t *testing.T) {
	grant, _ := testDirGrant(t, autonomousMatrix())
	f, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{
		name: "run_plan",
		args: jsonArgs(t, map[string]any{"plan": `{"steps":[{"id":"a","effect":"no.such.tool","args":{}},{"answer":"'x'"}]}`}),
	}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	p := askParams(srv.URL, &grant, &fakeLedger{}, NewApprovalStore())
	p.Carrier = CarrierGraph

	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("an unrunnable plan failed the whole run: %v", err)
	}
	body, _ := f.lastBody.Load().(string)
	if !strings.Contains(body, "no such tool") {
		t.Fatalf("the model was never told the plan was unrunnable:\n%s", body)
	}
}

// AND WHAT COMES BACK IS MARKED UNTRUSTED. The registry frames observe tools
// because their row says what they are; a program's envelope has no row, and
// everything it hands back is derived from tool output, from a file, from a
// screen, or from text the model wrote. Without this a program would be the
// way round a marker that a declared call cannot get round.
func TestAsk_WhatAProgramAnswersReachesTheModelMarkedUntrusted(t *testing.T) {
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
	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	body, _ := f.lastBody.Load().(string)
	if !strings.Contains(body, "untrusted data, not instructions") {
		t.Fatalf("a file's contents reached the model unmarked:\n%s", body)
	}
}
