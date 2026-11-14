package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/turnbearer"
)

// Admission is paused inside the endpoint, after the bearer lookup but before
// publication. This is not a request paused in the bridge before connecting:
// that would legitimately connect under the next turn's authority.
type lateAdmissionTools struct {
	*testTurnTools
	entered chan struct{}
	resume  chan struct{}
}

func (t *lateAdmissionTools) Admit(ctx context.Context) (assistant.ToolInvocation, error) {
	close(t.entered)
	select {
	case <-t.resume:
	case <-ctx.Done():
		return assistant.ToolInvocation{}, ctx.Err()
	}
	return t.testTurnTools.Admit(ctx)
}

func TestSessionBearerLateAdmissionCannotPublishUnderNextTurn(t *testing.T) {
	stand := newTurnStand(t, &turnDispatcher{})
	old := &lateAdmissionTools{
		testTurnTools: &testTurnTools{ctx: agenttools.RunContext{RunID: "late-N", Session: string(stand.sess.ID())}, grant: turnGrantForTest(), dispatcher: stand.dispatcher},
		entered:       make(chan struct{}), resume: make(chan struct{}),
	}
	var unblock sync.Once
	bridge, _, err := mintTurn(t, stand, turnbearer.Authority{Tools: old})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unblock.Do(func() { close(old.resume) }) })
	client := newS14MCPClientWithToken(t, stand.endpoint.SocketPath(), envToken(t, bridge.Env))
	delayed := client.startCall(t, "alpha.call", map[string]any{})
	awaitLateSignal(t, old.entered, "endpoint admission to look up N")
	stand.minter.EndTurn("late-N")
	next := &testTurnTools{ctx: agenttools.RunContext{RunID: "late-N+1", Session: string(stand.sess.ID()), PaneAccess: &turnPaneAccess{run: "late-N+1"}}, grant: content.Grant{}, dispatcher: stand.dispatcher}
	if err := stand.minter.BeginTurn("late-N", turnbearer.Authority{Tools: next}); err != nil {
		t.Fatal(err)
	}
	unblock.Do(func() { close(old.resume) })
	if resp := s14Await(t, delayed); resp.failed == "" {
		t.Fatalf("N's delayed admission succeeded while N+1 was running: %+v", resp)
	}
	if calls := stand.dispatcher.recorded(); len(calls) != 0 {
		t.Fatalf("delayed N request reached dispatch: %+v", calls)
	}
	if next.admissions != 0 {
		t.Fatal("late N admission used N+1's grant owner")
	}
	// A fresh request after the refusal really does reconnect under N+1.
	if resp := s14Await(t, client.startCall(t, "alpha.call", map[string]any{})); resp.failed != "" {
		t.Fatal(resp.failed)
	}
	calls := stand.dispatcher.recorded()
	if len(calls) != 1 || !reflect.DeepEqual(calls[0].RunContext, next.ctx) || !reflect.DeepEqual(calls[0].Grant, next.grant) {
		t.Fatalf("fresh connection did not use exactly N+1 authority: %+v", calls)
	}
	stand.minter.EndTurn("late-N")
}

func awaitLateSignal(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for " + what)
	}
}

// Delay the endpoint's dispatcher after it has received the pinned invocation.
// Cancellation alone is not settlement: EndTurn must wait for Dispatch to
// actually return, even if the caller has already received a broken pipe.
type lateSettlingDispatcher struct {
	entered   chan assistant.ToolInvocation
	cancelled chan struct{}
	resume    chan struct{}
	settled   chan struct{}
}

func (d *lateSettlingDispatcher) Catalogue(g content.Grant) []agenttools.Tool {
	return (&turnDispatcher{}).Catalogue(g)
}

func (d *lateSettlingDispatcher) Dispatch(inv assistant.ToolInvocation) (string, error) {
	d.entered <- inv
	<-inv.Context.Done()
	close(d.cancelled)
	<-d.resume
	close(d.settled)
	return "", inv.Context.Err()
}

func TestSessionBearerEndTurnWaitsForDelayedRequestToSettle(t *testing.T) {
	stand := newTurnStand(t, &turnDispatcher{})
	dispatcher := &lateSettlingDispatcher{entered: make(chan assistant.ToolInvocation, 1), cancelled: make(chan struct{}), resume: make(chan struct{}), settled: make(chan struct{})}
	var unblock sync.Once
	old := &testTurnTools{ctx: agenttools.RunContext{RunID: "settle-N", Session: string(stand.sess.ID()), PaneAccess: &turnPaneAccess{run: "settle-N"}}, grant: turnGrantForTest(), dispatcher: dispatcher}
	bridge, _, err := mintTurn(t, stand, turnbearer.Authority{Tools: old})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unblock.Do(func() { close(dispatcher.resume) }) })
	client := newS14MCPClientWithToken(t, stand.endpoint.SocketPath(), envToken(t, bridge.Env))
	delayed := client.startCall(t, "alpha.hold", map[string]any{})
	var invocation assistant.ToolInvocation
	select {
	case invocation = <-dispatcher.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("request did not reach endpoint dispatcher")
	}
	if !stand.minter.Pending("settle-N") {
		t.Fatal("accepted delayed request was not pending")
	}
	ended := make(chan struct{})
	go func() { stand.minter.EndTurn("settle-N"); close(ended) }()
	awaitLateSignal(t, dispatcher.cancelled, "N request cancellation")
	select {
	case <-ended:
		t.Fatal("EndTurn returned before its accepted request settled")
	default:
	}
	next := &testTurnTools{ctx: agenttools.RunContext{RunID: "settle-N+1", Session: string(stand.sess.ID())}, grant: content.Grant{}, dispatcher: stand.dispatcher}
	if err := stand.minter.BeginTurn("settle-N", turnbearer.Authority{Tools: next}); err == nil {
		t.Fatal("N+1 started while N's accepted request was still in dispatch")
	}
	if invocation.Context.Err() != context.Canceled || !reflect.DeepEqual(invocation.RunContext, old.ctx) || !reflect.DeepEqual(invocation.Grant, old.grant) {
		t.Fatalf("delayed request lost its pinned N authority or cancellation: %+v", invocation)
	}
	unblock.Do(func() { close(dispatcher.resume) })
	awaitLateSignal(t, ended, "EndTurn settlement")
	awaitLateSignal(t, dispatcher.settled, "dispatcher return")
	if stand.minter.Pending("settle-N") {
		t.Fatal("settled turn still has pending requests")
	}
	if resp := s14Await(t, delayed); resp.failed == "" {
		t.Fatal("cancelled N request succeeded")
	}
	if err := stand.minter.BeginTurn("settle-N", turnbearer.Authority{Tools: next}); err != nil {
		t.Fatal(err)
	}
	if resp := s14Await(t, client.startCall(t, "alpha.call", map[string]any{})); resp.failed != "" {
		t.Fatal(resp.failed)
	}
	calls := stand.dispatcher.recorded()
	if len(calls) != 1 || calls[0].RunContext.RunID != "settle-N+1" {
		t.Fatalf("unexpected next turn calls: %+v", calls)
	}
	stand.minter.EndTurn("settle-N")
}

func TestSessionBearerLateApprovalAnswerCannotAuthorizeNextTurn(t *testing.T) {
	stand := newTurnStand(t, &turnDispatcher{})
	client, _, err := assistant.NewClientAndRegistry(log.NewSlogAdapter(logtest.Slog(t)), nil, content.Floor{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "approval.txt")
	const payload = "read only after this turn's own approval"
	if writeErr := os.WriteFile(path, []byte(payload), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	policy := content.EffectPolicy{Observe: content.EffectRow{Decision: content.DecisionAsk}}
	type grantOwner struct {
		grant content.Grant
		mints int
	}
	oldOwner := &grantOwner{grant: policy.AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: path}})}
	nextOwner := &grantOwner{grant: policy.AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: dir}})}
	var mintMu sync.Mutex
	assertMints := func(oldWant, nextWant int) {
		t.Helper()
		mintMu.Lock()
		defer mintMu.Unlock()
		if oldOwner.mints != oldWant || nextOwner.mints != nextWant {
			t.Fatalf("grant refresh owners: N minted %d, N+1 minted %d; want %d, %d", oldOwner.mints, nextOwner.mints, oldWant, nextWant)
		}
	}
	store := assistant.NewApprovalStore()
	bind := func(run string, person *turnPerson, owner *grantOwner) assistant.TurnTools {
		t.Helper()
		tools, bindErr := assistant.BindRunTools(context.Background(), client, assistant.AskParams{
			Grant: &owner.grant, FreshGrant: func() *content.Grant {
				mintMu.Lock()
				defer mintMu.Unlock()
				owner.mints++
				return &owner.grant
			},
			RunID: run, SessionID: string(stand.sess.ID()), Attempt: 1,
			AttemptLedger: attemptLedgerForTest(t), KnownMaterial: turnSecret{value: "not-present-in-the-file"}, Approvals: store, Questioner: person,
		}, nil)
		if bindErr != nil {
			t.Fatal(bindErr)
		}
		return tools
	}
	person := func() *turnPerson {
		return &turnPerson{store: store, asked: make(chan *assistant.ApprovalRequest, 1), answer: make(chan struct{}), egress: make(chan struct{})}
	}
	oldPerson, nextPerson := person(), person()
	bridge, _, err := mintTurn(t, stand, turnbearer.Authority{Tools: bind("approval-N", oldPerson, oldOwner)})
	if err != nil {
		t.Fatal(err)
	}
	bridgeClient := newS14MCPClientWithToken(t, stand.endpoint.SocketPath(), envToken(t, bridge.Env))
	oldResult := bridgeClient.startCall(t, "files.read", map[string]any{"path": path})
	oldRequest := awaitLateApproval(t, oldPerson.asked)
	assertMints(1, 0)
	if !stand.minter.Pending("approval-N") {
		t.Fatal("approval waiter did not keep its turn pending")
	}
	stand.minter.EndTurn("approval-N")
	if resp := s14Await(t, oldResult); resp.failed == "" && strings.Contains(string(resp.result), payload) {
		t.Fatal("cancelled approval released file contents")
	}
	if err := stand.minter.BeginTurn("approval-N", turnbearer.Authority{Tools: bind("approval-N+1", nextPerson, nextOwner)}); err != nil {
		t.Fatal(err)
	}
	nextResult := bridgeClient.startCall(t, "files.read", map[string]any{"path": path})
	nextRequest := awaitLateApproval(t, nextPerson.asked)
	assertMints(1, 1)
	if oldRequest.RunID != "approval-N" || nextRequest.RunID != "approval-N+1" {
		t.Fatalf("approval owners crossed: %q, %q", oldRequest.RunID, nextRequest.RunID)
	}
	// Deliver the person's stale answer after N+1's identical file request is
	// waiting. The shared approval store must keep that answer bound to N.
	store.Approve(assistant.Approval{RunID: oldRequest.RunID, Attempt: oldRequest.Attempt, Tool: oldRequest.Tool, CallID: oldRequest.CallID, ArgHash: oldRequest.ArgHash, Effect: oldRequest.Effect})
	if store.IsApproved(assistant.Approval{RunID: nextRequest.RunID, Attempt: nextRequest.Attempt, Tool: nextRequest.Tool, CallID: nextRequest.CallID, ArgHash: nextRequest.ArgHash, Effect: nextRequest.Effect}) {
		t.Fatal("N's stale answer authorized N+1's exact proposal")
	}
	if !stand.minter.Pending("approval-N") {
		t.Fatal("stale approval settled N+1's pending call")
	}
	select {
	case response := <-nextResult:
		t.Fatalf("N's approval answered N+1's call: %s", response.raw)
	default:
	}
	// The stale answer must not refresh either owner. N+1's own answer
	// must refresh only N+1, even though both proposals read the same file.
	assertMints(1, 1)
	close(nextPerson.answer)
	resp := s14Await(t, nextResult)
	if resp.failed != "" || !strings.Contains(string(resp.result), payload) {
		t.Fatalf("N+1's own approval did not release its file: %+v", resp)
	}
	assertMints(1, 2)
	stand.minter.EndTurn("approval-N")
	if len(stand.dispatcher.recorded()) != 0 {
		t.Fatal("approval calls bypassed the real assistant chain")
	}
}

func awaitLateApproval(t *testing.T, requests <-chan *assistant.ApprovalRequest) *assistant.ApprovalRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(10 * time.Second):
		t.Fatal("endpoint call did not ask for approval")
		return nil
	}
}

func TestSessionBearerCancelledSessionRetryCannotReachReplacement(t *testing.T) {
	stand := newTurnStand(t, &turnDispatcher{})
	old := &testTurnTools{ctx: agenttools.RunContext{RunID: "cancelled-N", Session: string(stand.sess.ID())}, grant: turnGrantForTest(), dispatcher: stand.dispatcher}
	bridge, retire, err := mintTurn(t, stand, turnbearer.Authority{Tools: old})
	if err != nil {
		t.Fatal(err)
	}
	oldToken := envToken(t, bridge.Env)
	if resp := callWithToken(t, stand.endpoint.SocketPath(), oldToken, "alpha.call", map[string]any{}); resp.failed != "" {
		t.Fatal(resp.failed)
	}
	// Cancellation ends the session, rather than merely ending its turn.
	retire()
	next := &testTurnTools{ctx: agenttools.RunContext{RunID: "replacement-N+1", Session: string(stand.sess.ID())}, grant: content.Grant{}, dispatcher: stand.dispatcher}
	replacement, _, err := mintTurn(t, stand, turnbearer.Authority{Tools: next})
	if err != nil {
		t.Fatal(err)
	}
	if envToken(t, replacement.Env) == oldToken {
		t.Fatal("replacement session reused retired bearer")
	}
	// A new bridge retries the old session's call while its replacement runs.
	if resp := callWithToken(t, stand.endpoint.SocketPath(), oldToken, "alpha.call", map[string]any{}); resp.failed == "" {
		t.Fatal("cancelled session's retry was admitted")
	}
	if next.admissions != 0 {
		t.Fatal("cancelled session's retry touched replacement authority")
	}
	if calls := stand.dispatcher.recorded(); len(calls) != 1 {
		t.Fatalf("retry reached dispatch: %+v", calls)
	}
	if resp := callWithToken(t, stand.endpoint.SocketPath(), envToken(t, replacement.Env), "alpha.call", map[string]any{}); resp.failed != "" {
		t.Fatal(resp.failed)
	}
	calls := stand.dispatcher.recorded()
	if len(calls) != 2 || calls[1].RunContext.RunID != "replacement-N+1" {
		t.Fatalf("replacement request lost its authority: %+v", calls)
	}
	stand.minter.EndTurn("replacement-N+1")
}
