package content_test

import (
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestRunUsageSurvivesRestart(t *testing.T) {
	for _, usage := range []*content.RunUsage{nil, {}, {Input: 123, Output: 45, CacheRead: 67, CacheWrite: 8}} {
		t.Run(fmtUsage(usage), func(t *testing.T) {
			db, led, path := newLedgerAt(t)
			ctx := t.Context()
			ask, err := led.SubmitAgentAsk(ctx, content.AgentAsk{ID: nextAskID(), Client: "usage", Env: content.Environment{ID: "local", Kind: content.EnvLocal}, Question: "why?", Facts: content.RunFacts{Mode: "explain"}})
			if err != nil {
				t.Fatal(err)
			}
			if err = led.FinishAgentRun(ctx, ask.RunID, content.FinishAgentRun{State: content.RunCompleted, TerminationReason: content.TermCompleted, Usage: usage, EndedAt: 1}); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := reopenStore(t, path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reopened.Close() }()
			entry, err := reopened.Ledger().Entry(ctx, ask.EntryID)
			if err != nil {
				t.Fatal(err)
			}
			var facts content.RunFacts
			if err = json.Unmarshal([]byte(entry.Executions[0].Payload), &facts); err != nil {
				t.Fatal(err)
			}
			if usage == nil {
				if facts.Usage != nil {
					t.Fatalf("invented usage: %+v", facts.Usage)
				}
				return
			}
			if facts.Usage == nil || *facts.Usage != *usage {
				t.Fatalf("usage: %+v want %+v", facts.Usage, usage)
			}
		})
	}
}

func fmtUsage(u *content.RunUsage) string {
	if u == nil {
		return "absent"
	}
	if u.Input == 0 {
		return "zero"
	}
	return "reported"
}
