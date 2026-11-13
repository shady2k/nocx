package assistant

import (
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestPaneDispatcherAnAskRunsNothingAndAPermitRunsOnce(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decision content.Decision
		wantRuns int
	}{
		{"ask", content.DecisionAsk, 0},
		{"permit", content.DecisionPermit, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &fakeWorkerRecord{}
			dispatcher := toolDispatcherForTest(t, rec)
			grant := workerGrantForTest()
			grant.Policy = grant.Policy.SetRowDecision(content.EffectDelegate, tc.decision)
			_, err := dispatcher.Dispatch(workerInvocationForTest("workers.spawn", grant, spawnPlainParams))
			if tc.wantRuns == 0 {
				var refusal *DispatchRefusalError
				if !errors.As(err, &refusal) || refusal.Reason == "" {
					t.Fatalf("dispatch error = %v, want a refusal with a reason", err)
				}
				var pipelineRefusal *PipelineRefusal
				if !errors.As(err, &pipelineRefusal) || !strings.Contains(pipelineRefusal.Reason, "no approver") {
					t.Fatalf("dispatch error = %v, want the pipeline's unanswered Ask", err)
				}
			} else if err != nil {
				t.Fatalf("permitted dispatch: %v", err)
			}
			if len(rec.registered) != tc.wantRuns {
				t.Fatalf("executor registered %d workers, want %d", len(rec.registered), tc.wantRuns)
			}
		})
	}
}
