package assistant

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
)

// precallFunc, approverFunc, guardFunc and screenFunc adapt plain functions to
// the four points, so a test can state one answer without a struct apiece.
type precallFunc func(context.Context, Call) Decision

func (f precallFunc) Decide(ctx context.Context, c Call) Decision { return f(ctx, c) }

type approverFunc func(context.Context, Call, Ask) Answer

func (f approverFunc) Answer(ctx context.Context, c Call, a Ask) Answer { return f(ctx, c, a) }

type guardFunc func(context.Context, Call) (string, bool)

func (f guardFunc) Refuse(ctx context.Context, c Call) (string, bool) { return f(ctx, c) }

type screenFunc func(context.Context, Call, Outbound) Verdict

func (f screenFunc) Inspect(ctx context.Context, c Call, r Outbound) Verdict { return f(ctx, c, r) }

type attemptFunc func(context.Context, Call) error

func (f attemptFunc) Open(ctx context.Context, c Call) error { return f(ctx, c) }

// pipelineTestResult is what the planted tool answers, and it satisfies
// workers.holdings' own result contract so a passing call is a complete call.
const pipelineTestResult = `{"participants":[],"checkoutsComplete":true}`

// plantedTool counts the times the tool's body is entered. Every test in this
// file reads it: the whole point of the pipeline is that the count is 0 or 1,
// and never 2.
type plantedTool struct {
	runs  int
	onRun func()
}

func (p *plantedTool) executor(context.Context, Call, agenttools.Tool, agenttools.Capability, []byte) (string, error) {
	p.runs++
	if p.onRun != nil {
		p.onRun()
	}
	return pipelineTestResult, nil
}

// pipelineForTest builds the pipeline over the package's own operation with a
// planted executor, so what is counted is the tool's body and not the seams a
// real declaration would need.
func pipelineForTest(t *testing.T, chain AuthorityChain) (*Pipeline, *plantedTool) {
	t.Helper()
	registry, err := agenttools.Assemble(toolsDirFS(t))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	validators, results, err := compileDispatchSchemas(registry)
	if err != nil {
		t.Fatalf("compile dispatch schemas: %v", err)
	}
	planted := &plantedTool{}
	operation := &dispatchOperation{
		registry:   registry,
		validators: validators,
		results:    results,
		executor:   planted.executor,
	}
	pipeline, err := NewPipeline(operation, chain)
	if err != nil {
		t.Fatalf("new pipeline: %v", err)
	}
	return pipeline, planted
}

func pipelineInvocationForTest() ToolInvocation {
	return workerInvocationForTest("workers.holdings", workerGrantForTest(), `{}`)
}

// TestPipeline_AnUnansweredAskRunsNothing is the stage's first assertion
// (nocx-a62cl.2.1): no approver, an approver that failed and a context that
// ended during the wait all leave the planted tool's body untouched, and each
// answers a refusal that names why.
func TestPipeline_AnUnansweredAskRunsNothing(t *testing.T) {
	approverFailure := errors.New("the approval service could not be reached")
	askingPreCall := precallFunc(func(context.Context, Call) Decision {
		return Ask{Reason: "this call would run a destructive command"}
	})

	cases := []struct {
		name       string
		approver   func(context.CancelFunc) Approver
		wantReason string
		wantCause  error
	}{
		{
			name:       "no approver",
			approver:   func(context.CancelFunc) Approver { return nil },
			wantReason: "this chain has no approver",
		},
		{
			name: "the approver failed",
			approver: func(context.CancelFunc) Approver {
				return approverFunc(func(context.Context, Call, Ask) Answer {
					return Unanswered{Err: approverFailure}
				})
			},
			wantReason: approverFailure.Error(),
			wantCause:  approverFailure,
		},
		{
			name: "the context ended during the wait",
			approver: func(cancel context.CancelFunc) Approver {
				return approverFunc(func(context.Context, Call, Ask) Answer {
					// A person took longer than the caller was willing to
					// wait, and the approver still came back with a yes:
					// the answer arrived on a context that ended.
					cancel()
					return Approved{}
				})
			},
			wantReason: "cancelled while it waited for an answer",
			wantCause:  context.Canceled,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			pipeline, planted := pipelineForTest(t, AuthorityChain{
				PreCalls: []PreCall{askingPreCall},
				Approver: tc.approver(cancel),
			})
			invocation := pipelineInvocationForTest()
			invocation.Context = ctx

			_, err := pipeline.Dispatch(invocation, 1, "call-1")
			var refusal *PipelineRefusal
			if !errors.As(err, &refusal) {
				t.Fatalf("dispatch error = %T %v, want a PipelineRefusal", err, err)
			}
			if !strings.Contains(refusal.Reason, tc.wantReason) {
				t.Fatalf("refusal reason = %q, want it to name %q", refusal.Reason, tc.wantReason)
			}
			if !strings.Contains(refusal.Error(), "workers.holdings") {
				t.Fatalf("refusal error = %q, want it to name the tool", refusal.Error())
			}
			if tc.wantCause != nil && !errors.Is(err, tc.wantCause) {
				t.Fatalf("refusal = %v, want it to carry %v", err, tc.wantCause)
			}
			if planted.runs != 0 {
				t.Fatalf("the planted tool ran %d times on an unanswered ask, want 0", planted.runs)
			}
		})
	}
}

// TestPipeline_AnApprovedAskRunsTheToolExactlyOnce is the other half of the
// first assertion: a person's yes is what lets the call through, and it reaches
// the tool once.
func TestPipeline_AnApprovedAskRunsTheToolExactlyOnce(t *testing.T) {
	asked := Ask{Reason: "this call would run a destructive command", Facts: []string{"rm -rf build"}}
	answers := 0
	pipeline, planted := pipelineForTest(t, AuthorityChain{
		PreCalls: []PreCall{precallFunc(func(context.Context, Call) Decision { return asked })},
		Approver: approverFunc(func(_ context.Context, _ Call, a Ask) Answer {
			answers++
			if a.Reason != asked.Reason {
				t.Errorf("the approver was asked %q, want the PreCall's own question %q", a.Reason, asked.Reason)
			}
			return Approved{}
		}),
	})

	output, err := pipeline.Dispatch(pipelineInvocationForTest(), 1, "call-1")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if output != pipelineTestResult {
		t.Fatalf("output = %q, want the tool's own result", output)
	}
	if answers != 1 {
		t.Fatalf("the approver was consulted %d times, want exactly 1", answers)
	}
	if planted.runs != 1 {
		t.Fatalf("the planted tool ran %d times after an approval, want exactly 1", planted.runs)
	}
}

// TestPipeline_ARefusingGuardStopsTheCallBesideEveryOtherLink is the stage's
// second assertion: a guard can only refuse, and its position among the other
// guards does not weaken that. With every guard passing, the call runs once.
func TestPipeline_ARefusingGuardStopsTheCallBesideEveryOtherLink(t *testing.T) {
	passing := guardFunc(func(context.Context, Call) (string, bool) { return "", false })
	refusing := func(reason string) Guard {
		return guardFunc(func(context.Context, Call) (string, bool) { return reason, true })
	}

	cases := []struct {
		name   string
		guards []Guard
		reason string
	}{
		{
			name:   "before every other guard",
			guards: []Guard{refusing("the run's grant has expired"), passing, passing},
			reason: "the run's grant has expired",
		},
		{
			name:   "between other guards",
			guards: []Guard{passing, refusing("the file changed while the question was open"), passing},
			reason: "the file changed while the question was open",
		},
		{
			name:   "after every other guard",
			guards: []Guard{passing, passing, refusing("the evidence no longer holds")},
			reason: "the evidence no longer holds",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pipeline, planted := pipelineForTest(t, AuthorityChain{Guards: tc.guards})
			_, err := pipeline.Dispatch(pipelineInvocationForTest(), 1, "call-1")
			var refusal *PipelineRefusal
			if !errors.As(err, &refusal) {
				t.Fatalf("dispatch error = %T %v, want a PipelineRefusal", err, err)
			}
			if refusal.Reason != tc.reason {
				t.Fatalf("refusal reason = %q, want the guard's own reason %q", refusal.Reason, tc.reason)
			}
			if planted.runs != 0 {
				t.Fatalf("the planted tool ran %d times beside a refusing guard, want 0", planted.runs)
			}
		})
	}

	t.Run("with every guard passing the call runs once", func(t *testing.T) {
		pipeline, planted := pipelineForTest(t, AuthorityChain{Guards: []Guard{passing, passing, passing}})
		if _, err := pipeline.Dispatch(pipelineInvocationForTest(), 1, "call-1"); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if planted.runs != 1 {
			t.Fatalf("the planted tool ran %d times with every guard passing, want exactly 1", planted.runs)
		}
	})
}

// TestPipeline_InvalidArgumentsAreRefusedBeforeAnyPreCall is the stage's third
// assertion: the schema is the first thing a call meets, so a malformed call is
// refused before any policy point sees it.
func TestPipeline_InvalidArgumentsAreRefusedBeforeAnyPreCall(t *testing.T) {
	var seen []Call
	recording := precallFunc(func(_ context.Context, c Call) Decision {
		seen = append(seen, c)
		return Allow{}
	})
	pipeline, planted := pipelineForTest(t, AuthorityChain{PreCalls: []PreCall{recording}})

	// workers.spawn requires both a command and a task; the task is missing.
	_, err := pipeline.Dispatch(workerInvocationForTest("workers.spawn", workerGrantForTest(), `{"command":"claude"}`), 1, "call-1")
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("dispatch error = %v, want ErrInvalidParams", err)
	}
	if len(seen) != 0 {
		t.Fatalf("a PreCall saw %d calls for an invalid argument, want 0", len(seen))
	}
	if planted.runs != 0 {
		t.Fatalf("the planted tool ran %d times on invalid arguments, want 0", planted.runs)
	}
}

// TestPipeline_RunsItsPointsInTheOnlyOrderTheyHave asserts the order the ADR
// fixes: schema (already proven above), PreCall*, the approver on an Ask,
// Guard*, the tool, Screen*.
func TestPipeline_RunsItsPointsInTheOnlyOrderTheyHave(t *testing.T) {
	var order []string
	pipeline, planted := pipelineForTest(t, AuthorityChain{
		PreCalls: []PreCall{precallFunc(func(context.Context, Call) Decision {
			order = append(order, "precall")
			return Ask{Reason: "a person must decide"}
		})},
		Approver: approverFunc(func(context.Context, Call, Ask) Answer {
			order = append(order, "approver")
			return Approved{}
		}),
		Guards: []Guard{guardFunc(func(context.Context, Call) (string, bool) {
			order = append(order, "guard")
			return "", false
		})},
		Screens: []Screen{screenFunc(func(context.Context, Call, Outbound) Verdict {
			order = append(order, "screen")
			return Pass{}
		})},
	})
	planted.onRun = func() { order = append(order, "tool") }

	if _, err := pipeline.Dispatch(pipelineInvocationForTest(), 1, "call-1"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	want := []string{"precall", "approver", "guard", "tool", "screen"}
	if len(order) != len(want) {
		t.Fatalf("points ran in order %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("points ran in order %v, want %v", order, want)
		}
	}
}

// TestPipeline_AScreenNeverReplacesTheResult asserts the Screen point's own
// contract: it decides whether the bytes leave, never what they say. A pass and
// an approved ask both release the tool's exact result, and a refusal withholds
// it without running the tool again.
func TestPipeline_AScreenNeverReplacesTheResult(t *testing.T) {
	pass := screenFunc(func(context.Context, Call, Outbound) Verdict { return Pass{} })

	t.Run("a passing screen releases the exact result", func(t *testing.T) {
		pipeline, planted := pipelineForTest(t, AuthorityChain{Screens: []Screen{pass}})
		output, err := pipeline.Dispatch(pipelineInvocationForTest(), 1, "call-1")
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if output != pipelineTestResult {
			t.Fatalf("output = %q, want the tool's exact result", output)
		}
		if planted.runs != 1 {
			t.Fatalf("the planted tool ran %d times, want exactly 1", planted.runs)
		}
	})

	t.Run("an approved ask releases the exact result without re-running the tool", func(t *testing.T) {
		sawResult := false
		pipeline, planted := pipelineForTest(t, AuthorityChain{
			Approver: approverFunc(func(context.Context, Call, Ask) Answer { return Approved{} }),
			Screens: []Screen{screenFunc(func(_ context.Context, _ Call, r Outbound) Verdict {
				sawResult = r.Text == pipelineTestResult && !r.WasError
				return Ask{Reason: "the result carries something a person must see"}
			})},
		})
		output, err := pipeline.Dispatch(pipelineInvocationForTest(), 1, "call-1")
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if !sawResult {
			t.Fatal("the screen was not shown the tool's exact outbound result")
		}
		if output != pipelineTestResult {
			t.Fatalf("output = %q, want the tool's exact result released unchanged", output)
		}
		if planted.runs != 1 {
			t.Fatalf("the planted tool ran %d times, want exactly 1 — an approval must never re-run it", planted.runs)
		}
	})

	t.Run("a refusing screen withholds the result", func(t *testing.T) {
		pipeline, planted := pipelineForTest(t, AuthorityChain{
			Screens: []Screen{screenFunc(func(context.Context, Call, Outbound) Verdict {
				return Refuse{Reason: "the result carries a vault secret"}
			})},
		})
		_, err := pipeline.Dispatch(pipelineInvocationForTest(), 1, "call-1")
		var refusal *PipelineRefusal
		if !errors.As(err, &refusal) {
			t.Fatalf("dispatch error = %T %v, want a PipelineRefusal", err, err)
		}
		if refusal.Reason != "the result carries a vault secret" {
			t.Fatalf("refusal reason = %q, want the screen's own reason", refusal.Reason)
		}
		if planted.runs != 1 {
			t.Fatalf("the planted tool ran %d times, want exactly 1", planted.runs)
		}
	})
}

// TestAuthorityPointsOfferNoAllowToGuardsAndNoContinuation is the test the
// comment on Guard names. The compiler is what forbids an allowing guard and a
// continuation-taking point; this test pins the shape those types have, so a
// change that quietly gave either one a home fails here rather than in review.
func TestAuthorityPointsOfferNoAllowToGuardsAndNoContinuation(t *testing.T) {
	points := []struct {
		name   string
		iface  reflect.Type
		method string
	}{
		{"PreCall", reflect.TypeOf((*PreCall)(nil)).Elem(), "Decide"},
		{"Approver", reflect.TypeOf((*Approver)(nil)).Elem(), "Answer"},
		{"Guard", reflect.TypeOf((*Guard)(nil)).Elem(), "Refuse"},
		{"Screen", reflect.TypeOf((*Screen)(nil)).Elem(), "Inspect"},
	}
	for _, point := range points {
		t.Run(point.name, func(t *testing.T) {
			if point.iface.NumMethod() != 1 || point.iface.Method(0).Name != point.method {
				t.Fatalf("%s has methods %v, want exactly %s", point.name, methodNames(point.iface), point.method)
			}
			signature := point.iface.Method(0).Type
			for i := 0; i < signature.NumIn(); i++ {
				if signature.In(i).Kind() == reflect.Func {
					t.Fatalf("%s.%s takes %v: no point may receive a continuation", point.name, point.method, signature.In(i))
				}
			}
			for i := 0; i < signature.NumOut(); i++ {
				if signature.Out(i).Kind() == reflect.Func {
					t.Fatalf("%s.%s returns %v: no point may hand back a continuation", point.name, point.method, signature.Out(i))
				}
			}
		})
	}

	guardSignature := reflect.TypeOf((*Guard)(nil)).Elem().Method(0).Type
	if guardSignature.NumOut() != 2 || guardSignature.Out(0).Kind() != reflect.String || guardSignature.Out(1).Kind() != reflect.Bool {
		t.Fatalf("Guard.Refuse returns %v, want only (reason string, refused bool) — a guard can refuse and cannot allow", guardSignature)
	}

	sealed := []struct {
		name  string
		iface reflect.Type
	}{
		{"Decision", reflect.TypeOf((*Decision)(nil)).Elem()},
		{"Answer", reflect.TypeOf((*Answer)(nil)).Elem()},
		{"Verdict", reflect.TypeOf((*Verdict)(nil)).Elem()},
	}
	for _, result := range sealed {
		if result.iface.NumMethod() != 1 || result.iface.Method(0).PkgPath == "" {
			t.Fatalf("%s is not sealed by an unexported method: %v", result.name, methodNames(result.iface))
		}
	}
}

func methodNames(iface reflect.Type) []string {
	names := make([]string, iface.NumMethod())
	for i := range names {
		names[i] = iface.Method(i).Name
	}
	return names
}

// TestPipeline_ADenialIsFinal asserts the other Decision answers: a Deny refuses
// at once, and a point that answers nothing at all is refused too rather than
// read as permission.
func TestPipeline_ADenialIsFinal(t *testing.T) {
	cases := []struct {
		name     string
		decision Decision
		want     string
	}{
		{name: "a Deny", decision: Deny{Reason: "the matrix refuses this row"}, want: "the matrix refuses this row"},
		{name: "no decision at all", decision: nil, want: "answered neither allow, deny nor ask"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pipeline, planted := pipelineForTest(t, AuthorityChain{
				PreCalls: []PreCall{precallFunc(func(context.Context, Call) Decision { return tc.decision })},
				Approver: approverFunc(func(context.Context, Call, Ask) Answer {
					t.Error("the approver was consulted about a call nobody asked about")
					return Approved{}
				}),
			})
			_, err := pipeline.Dispatch(pipelineInvocationForTest(), 1, "call-1")
			var refusal *PipelineRefusal
			if !errors.As(err, &refusal) {
				t.Fatalf("dispatch error = %T %v, want a PipelineRefusal", err, err)
			}
			if !strings.Contains(refusal.Reason, tc.want) {
				t.Fatalf("refusal reason = %q, want it to name %q", refusal.Reason, tc.want)
			}
			if planted.runs != 0 {
				t.Fatalf("the planted tool ran %d times on a denied call, want 0", planted.runs)
			}
		})
	}
}

// TestPipeline_AnAllowedCallNeverReachesTheApprover asserts the approver's own
// place in the order: it is consulted on an Ask and on nothing else.
func TestPipeline_AnAllowedCallNeverReachesTheApprover(t *testing.T) {
	pipeline, planted := pipelineForTest(t, AuthorityChain{
		PreCalls: []PreCall{precallFunc(func(context.Context, Call) Decision { return Allow{} })},
		Approver: approverFunc(func(context.Context, Call, Ask) Answer {
			t.Error("the approver was consulted about a call no point asked about")
			return Approved{}
		}),
	})
	if _, err := pipeline.Dispatch(pipelineInvocationForTest(), 1, "call-1"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if planted.runs != 1 {
		t.Fatalf("the planted tool ran %d times, want exactly 1", planted.runs)
	}
}

// TestPipeline_EveryPointSeesTheSameImmutableCall asserts the Call contract: the
// identity a caller states is copied into one Call, and every point sees that
// same call — the run, the attempt, the tool, the call id and the hash of the
// canonical arguments.
func TestPipeline_EveryPointSeesTheSameImmutableCall(t *testing.T) {
	want := Call{
		runID:   "run-1",
		attempt: 3,
		tool:    "workers.holdings",
		callID:  "call-7",
		argHash: canonicalArgHash(`{}`),
	}
	var seen []Call
	pipeline, _ := pipelineForTest(t, AuthorityChain{
		PreCalls: []PreCall{precallFunc(func(_ context.Context, c Call) Decision {
			seen = append(seen, c)
			return Ask{Reason: "a person must decide"}
		})},
		Approver: approverFunc(func(_ context.Context, c Call, _ Ask) Answer {
			seen = append(seen, c)
			return Approved{}
		}),
		Guards: []Guard{guardFunc(func(_ context.Context, c Call) (string, bool) {
			seen = append(seen, c)
			return "", false
		})},
		Screens: []Screen{screenFunc(func(_ context.Context, c Call, _ Outbound) Verdict {
			seen = append(seen, c)
			return Pass{}
		})},
	})

	if _, err := pipeline.Dispatch(pipelineInvocationForTest(), want.attempt, want.callID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(seen) != 4 {
		t.Fatalf("points saw %d calls, want 4", len(seen))
	}
	for i, call := range seen {
		if call.RunID() != want.runID || call.Attempt() != want.attempt || call.Tool() != want.tool ||
			call.CallID() != want.callID || call.ArgHash() != want.argHash {
			t.Fatalf("point %d saw %+v, want the caller's own identity %+v", i, call, want)
		}
		if call.Declaration().Name != want.tool {
			t.Fatalf("point %d saw declaration %q, want %q — every point decides on the same prepared call",
				i, call.Declaration().Name, want.tool)
		}
	}
	if seen[0].RunID() != "run-1" || seen[0].Attempt() != 3 || seen[0].Tool() != "workers.holdings" ||
		seen[0].CallID() != "call-7" || seen[0].ArgHash() != canonicalArgHash(`{}`) {
		t.Fatalf("call accessors = %q/%d/%q/%q/%q, want the caller's own identity", seen[0].RunID(), seen[0].Attempt(), seen[0].Tool(), seen[0].CallID(), seen[0].ArgHash())
	}
	if seen[0].ArgHash() == "" {
		t.Fatal("the call carries no argument hash, so an approval could not be bound to it")
	}
}

// TestPipeline_ACallTheTurnEndCaughtHandsNothingBack is stage 4's assertion
// (nocx-a62cl.4.1): the person's Stop lands while the tool is running, and the
// call's result does not leave afterwards. Nothing was refused before the
// tool — the policy permitted it, the capability was built and the body ran
// exactly once — so the only thing that can stop the result is the pipeline's
// own observation of the context after the execution.
//
// The paired half is the same call with a live turn: it returns its result,
// once. Without it, "the result was withheld" would also be true of a pipeline
// that never returns anything.
func TestPipeline_ACallTheTurnEndCaughtHandsNothingBack(t *testing.T) {
	t.Run("the turn ends while the tool runs", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		pipeline, planted := pipelineForTest(t, AuthorityChain{})
		planted.onRun = cancel

		invocation := pipelineInvocationForTest()
		invocation.Context = ctx

		out, err := pipeline.Dispatch(invocation, 1, "call-stopped")

		if planted.runs != 1 {
			t.Fatalf("the tool's body ran %d times, want exactly 1", planted.runs)
		}
		if out != "" {
			t.Fatalf("the call's result left after the turn ended: %q", out)
		}
		var refusal *PipelineRefusal
		if !errors.As(err, &refusal) {
			t.Fatalf("dispatch error = %v, want a refusal naming the ended turn", err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("refusal cause = %v, want the context's own cancellation", refusal.Cause)
		}
		if !strings.Contains(refusal.Reason, "while the tool was running") {
			t.Fatalf("refusal = %q, want it to say the turn ended while the tool ran", refusal.Reason)
		}
	})

	t.Run("the same call with a live turn returns once", func(t *testing.T) {
		pipeline, planted := pipelineForTest(t, AuthorityChain{})
		out, err := pipeline.Dispatch(pipelineInvocationForTest(), 1, "call-live")
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if planted.runs != 1 {
			t.Fatalf("the tool's body ran %d times, want exactly 1", planted.runs)
		}
		if out != pipelineTestResult {
			t.Fatalf("result = %q, want the tool's own answer %q", out, pipelineTestResult)
		}
	})
}
