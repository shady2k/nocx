package assistant

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

func TestPipelineApprovalIncludesEveryQuestion(t *testing.T) {
	first := Ask{Reason: "publish a file", Facts: []string{"destination is public"}}
	second := Ask{Reason: "spend a credit", Facts: []string{"one credit remains"}}
	var received Ask
	pipeline, planted := pipelineForTest(t, AuthorityChain{
		PreCalls: []PreCall{
			precallFunc(func(context.Context, Call) Decision { return first }),
			precallFunc(func(context.Context, Call) Decision { return second }),
		},
		Approver: approverFunc(func(_ context.Context, _ Call, ask Ask) Answer {
			received = ask
			return Approved{}
		}),
	})
	if _, err := pipeline.Dispatch(pipelineInvocationForTest(), 1, "call-questions"); err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{first.Reason, second.Reason} {
		if !strings.Contains(received.Reason, reason) {
			t.Errorf("approval reason %q omitted %q", received.Reason, reason)
		}
	}
	if !reflect.DeepEqual(received.Facts, []string{first.Facts[0], second.Facts[0]}) {
		t.Errorf("approval facts = %v, want both questions' facts", received.Facts)
	}
	if planted.runs != 1 {
		t.Fatalf("tool ran %d times, want 1", planted.runs)
	}
}

func TestPipelineExecutesTheApprovedArgumentSnapshot(t *testing.T) {
	original := []byte(`{"command":"claude","task":"read it"}`)
	approved := string(original)
	var executed string
	pipeline, _ := pipelineForTest(t, AuthorityChain{
		PreCalls: []PreCall{precallFunc(func(context.Context, Call) Decision { return Ask{Reason: "approve these bytes"} })},
		Approver: approverFunc(func(_ context.Context, call Call, _ Ask) Answer {
			if call.ArgHash() != canonicalArgHash(approved) {
				t.Fatal("approval hash does not describe original arguments")
			}
			copy(original, []byte(`{"command":"claude","task":"edit it"}`))
			return Approved{}
		}),
	})
	runs := 0
	pipeline.operation.executor = func(_ context.Context, _ agenttools.Tool, _ agenttools.Capability, raw []byte) (string, error) {
		runs++
		executed = string(raw)
		return `{}`, nil
	}
	pipeline.operation.results = nil
	invocation := workerInvocationForTest("workers.spawn", workerGrantForTest(), "")
	invocation.RawParams = original
	if _, err := pipeline.Dispatch(invocation, 1, "call-snapshot"); err != nil {
		t.Fatal(err)
	}
	if executed != approved {
		t.Fatalf("tool executed %s, want approved %s", executed, approved)
	}
	if runs != 1 {
		t.Fatalf("tool ran %d times, want 1", runs)
	}
}

func TestPipelineCancellationAfterCapabilityConstructionRunsNothing(t *testing.T) {
	for _, step := range []string{"narrow", "beforeTool"} {
		t.Run(step, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			chain := AuthorityChain{}
			if step == "narrow" {
				chain.prepare = func(p *preparedInvocation) error {
					narrow := p.decl.Narrow
					p.decl.Narrow = func(grant content.Grant, resources []agenttools.ResourceRef, run agenttools.RunContext) (agenttools.Capability, error) {
						cap, err := narrow(grant, resources, run)
						cancel()
						return cap, err
					}
					return nil
				}
			} else {
				chain.beforeTool = func(context.Context, Call, *preparedInvocation, agenttools.Capability) error { cancel(); return nil }
			}
			pipeline, planted := pipelineForTest(t, chain)
			invocation := pipelineInvocationForTest()
			invocation.Context = ctx
			_, err := pipeline.Dispatch(invocation, 1, "call-cancel")
			if !errors.Is(err, context.Canceled) {
				t.Errorf("dispatch error = %v, want context.Canceled", err)
			}
			if planted.runs != 0 {
				t.Fatalf("tool ran %d times after %s cancelled context, want 0", planted.runs, step)
			}
		})
	}
}
