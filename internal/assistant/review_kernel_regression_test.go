package assistant

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

func TestReviewFreshRefusalSurvivesApproval(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "must not leave")
	fresh := grant
	fresh.Policy.Observe.Decision = content.DecisionRefuse
	approvals := NewApprovalStore()
	q := &recordingQuestioner{store: approvals, answer: Approved{}}
	ledger := &fakeLedger{}
	k := kernelForQuestioner(t, grant, ledger, q)
	k.approvals = approvals
	k.onCall = func(ToolCall) error { t.Error("refused call reached execution"); return nil }
	k.runSeams.freshGrant = func() *content.Grant { return &fresh }
	out, err := k.Invoke(context.Background(), "files.read", "c", fmt.Sprintf(`{"path":%q}`, path))
	if err != nil {
		t.Fatal(err)
	}
	if out != refusalResult("files.read", RefusedByDecision, "") {
		t.Fatalf("refreshed refusal ignored: %s", out)
	}
}

func TestReviewEgressRefreshesAuthority(t *testing.T) {
	mints := 0
	pipeline, planted := pipelineForTest(t, AuthorityChain{
		Screens:   []Screen{screenFunc(func(context.Context, Call, Outbound) Verdict { return Ask{Reason: "release"} })},
		Approver:  approverFunc(func(context.Context, Call, Ask) Answer { return Approved{} }),
		MintGrant: func(context.Context, Call) (content.Grant, bool) { mints++; return content.Grant{}, true },
	})
	if _, err := pipeline.Dispatch(pipelineInvocationForTest(), 1, "c"); err != nil {
		t.Fatal(err)
	}
	if mints != 1 || planted.runs != 1 {
		t.Fatalf("mints=%d runs=%d, want 1 each", mints, planted.runs)
	}
}

type reviewBrokenKnown struct{}

func (reviewBrokenKnown) FindKnown(context.Context, string) ([]KnownMatch, error) {
	return nil, errors.New("vault unavailable")
}

func TestReviewFailedScreenClosesAttempt(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "ordinary bytes")
	ledger := &fakeLedger{}
	k := kernelFor(t, grant, ledger)
	k.known = reviewBrokenKnown{}
	_, err := k.Invoke(context.Background(), "files.read", "c", fmt.Sprintf(`{"path":%q}`, path))
	var screening *EgressScreeningError
	if !errors.As(err, &screening) {
		t.Fatalf("error=%v", err)
	}
	if len(ledger.finishCallRecords()) != 1 {
		t.Fatalf("screening left attempt open: %v", ledger.log)
	}
	if !strings.Contains(strings.Join(ledger.log, ","), "finish:failure") {
		t.Fatalf("outcome=%v", ledger.log)
	}
}

func TestReviewEgressApprovalStartsSuccessor(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "known-secret-value-123")
	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	q := &recordingQuestioner{store: approvals, answer: Approved{}}
	k := kernelForQuestioner(t, grant, ledger, q)
	k.approvals = approvals
	k.known = &knownMatcher{value: "known-secret-value-123", name: "token"}
	out, err := k.Invoke(context.Background(), "files.read", "c", fmt.Sprintf(`{"path":%q}`, path))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "known-secret-value-123") {
		t.Fatalf("held bytes lost: %s", out)
	}
	if ledger.started() != 2 || len(ledger.finishCallRecords()) != 2 {
		t.Fatalf("egress continuation did not close/open successor: %v", ledger.log)
	}
	if len(ledger.submissions) != 1 {
		t.Fatalf("successor changed entry: %v", ledger.submissions)
	}
}

func TestReviewCallFactsAreDeepCopies(t *testing.T) {
	grant := content.Grant{Scopes: []content.GrantScope{{ID: "original"}}, Effects: []content.Effect{content.EffectObserve}, Policy: content.EffectPolicy{Observe: content.EffectRow{Scopes: []content.GrantScope{{ID: "original"}}}}}
	c := Call{facts: &callFacts{args: map[string]any{"nested": []any{map[string]any{"value": "original"}}}, decl: agenttools.Tool{Declaration: agenttools.Declaration{Effect: []content.Effect{content.EffectObserve}}, ParamsSchema: []byte(`{}`)}, grant: grant}}
	nestedValue := func() map[string]any {
		t.Helper()
		values, ok := c.Arguments()["nested"].([]any)
		if !ok || len(values) != 1 {
			t.Fatal("nested argument is not a one-element JSON array")
		}
		value, ok := values[0].(map[string]any)
		if !ok {
			t.Fatal("nested array element is not a JSON object")
		}
		return value
	}
	nestedValue()["value"] = "changed"
	d := c.Declaration()
	d.Declaration.Effect[0] = content.EffectDisclose
	d.ParamsSchema[0] = '!'
	g := c.Grant()
	g.Scopes[0].ID = "changed"
	g.Effects[0] = content.EffectDisclose
	g.Policy.Observe.Scopes[0].ID = "changed"
	if nestedValue()["value"] != "original" || c.Declaration().Declaration.Effect[0] != content.EffectObserve || c.Declaration().ParamsSchema[0] != '{' || c.Grant().Scopes[0].ID != "original" || c.Grant().Policy.Observe.Scopes[0].ID != "original" || c.Grant().Effects[0] != content.EffectObserve {
		t.Fatal("a point mutated another point's call facts")
	}
}

func TestReviewConcurrentCallsReadFreshGrantInsideTurn(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "read safely")
	approvals := NewApprovalStore()
	q := &recordingQuestioner{store: approvals, answer: Approved{}}
	k := kernelForQuestioner(t, grant, &fakeLedger{}, q)
	k.approvals = approvals
	k.runSeams.freshGrant = func() *content.Grant { fresh := grant; return &fresh }
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Go(func() {
			_, err := k.Invoke(context.Background(), "files.read", fmt.Sprintf("c-%d", i), fmt.Sprintf(`{"path":%q}`, path))
			if err != nil {
				t.Errorf("Invoke: %v", err)
			}
		})
	}
	wg.Wait()
}

func TestReviewEgressWaitRenewsFollowingCall(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "known-secret-value-123")
	fresh := grant
	approvals := NewApprovalStore()
	q := &recordingQuestioner{store: approvals, answer: Approved{}}
	k := kernelForQuestioner(t, grant, &fakeLedger{}, q)
	k.approvals = approvals
	k.known = &knownMatcher{value: "known-secret-value-123", name: "token"}
	q.before = func() { k.grant.ExpiresAt = 1 }
	k.runSeams.freshGrant = func() *content.Grant { return &fresh }
	args := fmt.Sprintf(`{"path":%q}`, path)
	if _, err := k.Invoke(context.Background(), "files.read", "first", args); err != nil {
		t.Fatal(err)
	}
	q.before = nil
	out, err := k.Invoke(context.Background(), "files.read", "next", args)
	if err != nil || !strings.Contains(out, "known-secret-value-123") {
		t.Fatalf("following call used expired pre-answer grant: %q %v", out, err)
	}
}

type reviewSlotLedger struct {
	*fakeLedger
	check func()
}

func (l reviewSlotLedger) FinishExecution(ctx context.Context, id int64, end content.FinishExecution) error {
	l.check()
	return l.fakeLedger.FinishExecution(ctx, id, end)
}

func TestReviewTurnSlotSpansOutcome(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "safe")
	ledger := reviewSlotLedger{fakeLedger: &fakeLedger{}}
	k := kernelFor(t, grant, ledger)
	checked := false
	ledger.check = func() {
		checked = true
		if len(k.order.slot) != 1 {
			t.Error("turn slot released before outcome recording")
		}
	}
	k.ledger = ledger
	if _, err := k.Invoke(context.Background(), "files.read", "c", fmt.Sprintf(`{"path":%q}`, path)); err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("outcome not recorded")
	}
}
