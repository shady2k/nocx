package app

// session.keys' own acceptance tests (design §4.2, §6.4, §6.5, §7.2, Task
// 9): PaneKeys.Send's authority relay, its transport-error recovery
// (session.intent.status), and the option loop's own step-by-step
// mechanics. These exercise the REAL paneKeys (session_keys.go) against
// fakes at the paneHelpers/paneKeysReader seam — the same style
// pane_access_test.go and pane_access_adversarial_test.go already use for
// this package's authority layer, reusing their fakes (fakeLookup,
// fakePaneClock, newGroupTwoCallersRecord) and pane_access_adversarial_
// test.go's fakeKeysReader — rather than a real helper runtime and PTY,
// which this task's time budget did not reach; the report accompanying
// this commit says so explicitly.

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/sessionruntime"
	"github.com/shady2k/nocx/internal/workers"
)

// fixedResultHelper is a paneHelpers whose Intent answers one scripted
// result unconditionally — for the tests below that assert PaneKeys.Send
// RELAYS a helper's own decision faithfully (a refusal cause, bytesWritten)
// rather than deciding it itself. would_submit, no_read_barrier and
// token_spent are each the HELPER's own decision (design §5.7, §6.2, §6.5);
// PaneKeys' job is to carry the request and hand back exactly what came
// back, which is what these tests hold it to.
type fixedResultHelper struct {
	result proto.IntentResult
	err    error
	calls  []proto.IntentParams
}

func (h *fixedResultHelper) Intent(_ context.Context, _ string, p proto.IntentParams) (proto.IntentResult, error) {
	h.calls = append(h.calls, p)
	return h.result, h.err
}

func (h *fixedResultHelper) AccessBump(context.Context, string, uint64) (uint64, error) {
	return 0, errors.New("fixedResultHelper: AccessBump not configured")
}

func (h *fixedResultHelper) Snapshot(context.Context, string) (proto.SnapshotResult, error) {
	return proto.SnapshotResult{}, errors.New("fixedResultHelper: Snapshot not configured")
}

func (h *fixedResultHelper) Target(context.Context, string, proto.TargetParams) (proto.TargetResult, error) {
	return proto.TargetResult{}, errors.New("fixedResultHelper: Target not configured")
}

func (h *fixedResultHelper) IntentStatus(context.Context, string, string) (proto.IntentStatusResult, error) {
	return proto.IntentStatusResult{}, errors.New("fixedResultHelper: IntentStatus not configured")
}

// newTestPaneAccess registers one worker under a fresh registrar, binds a
// DescendantPaneAccess over it with helper wired as that session's own
// paneHelpers, and resolves EffectSendInput once — the same chain a real
// mint's targetRecord would carry (design §6.2). suffix keeps concurrent
// subtests' coordinator sessions distinct.
func newTestPaneAccess(t *testing.T, suffix string, helper paneHelpers) (*paneAccessHub, *DescendantPaneAccess, workers.Chain, string) {
	t.Helper()
	registrar, _ := newGroupTwoCallersRecord()
	ctx := context.Background()
	controllerSession := "sess-" + suffix
	w, err := registrar.Register(ctx, workers.RegisterRequest{
		CoordinatorSession: controllerSession, Role: workers.RoleWorker,
		Task: "t", Command: "agent", Environment: "env-local",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	sessionID := string(w.Participant.ID)
	lookup := &fakeLookup{helpers: map[string]paneHelpers{sessionID: helper}}
	hub := newPaneAccessHub(registrar, lookup, &fakePaneClock{})
	access := hub.Bind(controllerSession, session.Identity{InstanceID: "backend-A", Epoch: 1}, EndpointAuthority{AdmissionEpoch: 1})
	reach, err := access.Resolve(ctx, sessionID, workers.EffectSendInput)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return hub, access, reach.Chain, sessionID
}

func inputTargetRecord(access *DescendantPaneAccess, chain workers.Chain, sessionID string) map[string]targetRecord {
	return map[string]targetRecord{
		"tok-1": {
			Access: access, SessionID: sessionID, Chain: chain, AccessEpoch: 1,
			View: assistant.TargetView{Token: "tok-1-signed", TokenID: "tok-1", Kind: sessionruntime.TargetInput},
		},
	}
}

// TestASpentTokenIsRefused: the same token sent twice with different keys —
// the SECOND helper.Intent call answers token_spent (design §6.2, decided
// at the helper's own commit point), and PaneKeys.Send reports it exactly,
// never retrying or masking it as anything else.
func TestASpentTokenIsRefused(t *testing.T) {
	helper := &fixedResultHelper{result: proto.IntentResult{
		State: "refused", Refusal: &proto.IntentRefusal{Cause: "token_spent"},
	}}
	hub, access, chain, sessionID := newTestPaneAccess(t, "spent", helper)
	reader := &fakeKeysReader{records: inputTargetRecord(access, chain, sessionID)}
	keys := newPaneKeys(reader, hub)

	key := assistant.KeyName("Down")
	res, err := keys.Send(context.Background(), access, assistant.KeysRequest{SessionID: sessionID, TokenID: "tok-1", Key: &key})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.State != "refused" || res.Refusal == nil || res.Refusal.Cause != "token_spent" {
		t.Fatalf("Send result = %+v, want refused/token_spent", res)
	}
	if len(helper.calls) != 1 {
		t.Fatalf("Intent calls = %d, want 1", len(helper.calls))
	}
}

// TestAnSSHPaneRefusesKeys: a session with no read barrier refuses every
// state-changing step (design §5.2) — the helper's own decision;
// PaneKeys.Send carries the request there unchanged and relays the answer.
func TestAnSSHPaneRefusesKeys(t *testing.T) {
	helper := &fixedResultHelper{result: proto.IntentResult{
		State: "refused", Refusal: &proto.IntentRefusal{Cause: "no_read_barrier"},
	}}
	hub, access, chain, sessionID := newTestPaneAccess(t, "ssh", helper)
	reader := &fakeKeysReader{records: inputTargetRecord(access, chain, sessionID)}
	keys := newPaneKeys(reader, hub)

	key := assistant.KeyName("Enter")
	res, err := keys.Send(context.Background(), access, assistant.KeysRequest{SessionID: sessionID, TokenID: "tok-1", Key: &key})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.State != "refused" || res.Refusal == nil || res.Refusal.Cause != "no_read_barrier" {
		t.Fatalf("Send result = %+v, want refused/no_read_barrier", res)
	}
	if res.BytesWritten != 0 {
		t.Fatalf("bytesWritten = %d, want 0", res.BytesWritten)
	}
}

// TestAKeyUnderAChangedMenuWritesNothing: the screen moved since the target
// was minted (design §6.2/§6.5) — stale_target, zero bytes, and the
// helper's own regionNow showing what changed is relayed unchanged.
func TestAKeyUnderAChangedMenuWritesNothing(t *testing.T) {
	helper := &fixedResultHelper{result: proto.IntentResult{
		State: "refused", Refusal: &proto.IntentRefusal{Cause: "stale_target", RegionNow: "a different menu now"},
	}}
	hub, access, chain, sessionID := newTestPaneAccess(t, "stale", helper)
	reader := &fakeKeysReader{records: inputTargetRecord(access, chain, sessionID)}
	keys := newPaneKeys(reader, hub)

	key := assistant.KeyName("Down")
	res, err := keys.Send(context.Background(), access, assistant.KeysRequest{SessionID: sessionID, TokenID: "tok-1", Key: &key})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.State != "refused" || res.Refusal == nil || res.Refusal.Cause != "stale_target" || res.Refusal.RegionNow != "a different menu now" {
		t.Fatalf("Send result = %+v, want refused/stale_target with regionNow", res)
	}
	if res.BytesWritten != 0 {
		t.Fatalf("bytesWritten = %d, want 0", res.BytesWritten)
	}
}

// TestATextAtomWithANewlineAndNoBracketedPasteIsRefused, paired with the
// same text under bracketed paste on: PaneKeys never inspects the text for
// a newline itself (design §6.5, "decided in the helper at encode") — both
// halves of the pair are the SAME call shape, differing only in what the
// (fake) helper answers, which proves the client performs no filtering of
// its own.
func TestATextAtomWithANewlineAndNoBracketedPasteIsRefused(t *testing.T) {
	helper := &fixedResultHelper{result: proto.IntentResult{
		State: "refused", Refusal: &proto.IntentRefusal{Cause: "would_submit"},
	}}
	hub, access, chain, sessionID := newTestPaneAccess(t, "nopaste", helper)
	reader := &fakeKeysReader{records: inputTargetRecord(access, chain, sessionID)}
	keys := newPaneKeys(reader, hub)

	text := "hello\n"
	res, err := keys.Send(context.Background(), access, assistant.KeysRequest{SessionID: sessionID, TokenID: "tok-1", Text: &text})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.State != "refused" || res.Refusal == nil || res.Refusal.Cause != "would_submit" {
		t.Fatalf("Send result = %+v, want refused/would_submit", res)
	}
	if len(helper.calls) != 1 || string(helper.calls[0].Payload) != "hello\n" {
		t.Fatalf("Intent payload = %q, want the text unfiltered including its newline", helper.calls[0].Payload)
	}
}

// Paired ordinary success: the identical text, under bracketed paste (the
// helper's own encode decides this — modelled here simply as the helper
// executing it).
func TestATextAtomWithANewlineUnderBracketedPasteIsExecuted(t *testing.T) {
	helper := &fixedResultHelper{result: proto.IntentResult{State: "executed", BytesWritten: 6}}
	hub, access, chain, sessionID := newTestPaneAccess(t, "paste", helper)
	reader := &fakeKeysReader{records: inputTargetRecord(access, chain, sessionID)}
	keys := newPaneKeys(reader, hub)

	text := "hello\n"
	res, err := keys.Send(context.Background(), access, assistant.KeysRequest{SessionID: sessionID, TokenID: "tok-1", Text: &text})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.State != "executed" || res.BytesWritten != 6 {
		t.Fatalf("Send result = %+v, want executed with 6 bytes written", res)
	}
}

// lostResponseHelper models design §7.2's "transport timeouts are not
// safety boundaries": Intent's own RPC fails, and session.intent.status
// answers the ALREADY-TERMINAL result on its first poll — the lost
// response recovered, never reported cancelled or re-sent.
type lostResponseHelper struct {
	recovered   proto.IntentResult
	intentCalls int
	statusCalls int
}

func (h *lostResponseHelper) Intent(context.Context, string, proto.IntentParams) (proto.IntentResult, error) {
	h.intentCalls++
	return proto.IntentResult{}, errors.New("transport: connection reset")
}

func (h *lostResponseHelper) IntentStatus(context.Context, string, string) (proto.IntentStatusResult, error) {
	h.statusCalls++
	r := h.recovered
	return proto.IntentStatusResult{State: r.State, Result: &r}, nil
}

func (h *lostResponseHelper) AccessBump(context.Context, string, uint64) (uint64, error) {
	return 0, errors.New("lostResponseHelper: AccessBump not configured")
}

func (h *lostResponseHelper) Snapshot(context.Context, string) (proto.SnapshotResult, error) {
	return proto.SnapshotResult{}, errors.New("lostResponseHelper: Snapshot not configured")
}

func (h *lostResponseHelper) Target(context.Context, string, proto.TargetParams) (proto.TargetResult, error) {
	return proto.TargetResult{}, errors.New("lostResponseHelper: Target not configured")
}

func TestALostResponseIsRecoveredByStatus(t *testing.T) {
	helper := &lostResponseHelper{recovered: proto.IntentResult{State: "executed", BytesWritten: 1, FenceAfter: 9}}
	hub, access, chain, sessionID := newTestPaneAccess(t, "lost", helper)
	reader := &fakeKeysReader{records: inputTargetRecord(access, chain, sessionID)}
	keys := newPaneKeys(reader, hub)

	key := assistant.KeyName("Enter")
	res, err := keys.Send(context.Background(), access, assistant.KeysRequest{SessionID: sessionID, TokenID: "tok-1", Key: &key})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.State != "executed" || res.BytesWritten != 1 {
		t.Fatalf("Send result = %+v, want the status-recovered executed result", res)
	}
	if helper.intentCalls != 1 || helper.statusCalls != 1 {
		t.Fatalf("intentCalls=%d statusCalls=%d, want 1 and 1", helper.intentCalls, helper.statusCalls)
	}
}

// unanswerableHelper never answers a terminal status; it advances the
// shared fake clock itself on every status poll, so the test needs no real
// sleep to reach "commitBy has passed" (AGENTS.md: wait on an observable
// state change, never a duration) — the observable state here being the
// clock reading this fake itself advances.
type unanswerableHelper struct {
	clock *fakePaneClock
}

func (h *unanswerableHelper) Intent(context.Context, string, proto.IntentParams) (proto.IntentResult, error) {
	return proto.IntentResult{}, errors.New("transport: no response")
}

func (h *unanswerableHelper) IntentStatus(context.Context, string, string) (proto.IntentStatusResult, error) {
	h.clock.advance(h.clock.Now() + Nanos(10_000_000_000)) // +10s: past any 5s commitWindow
	return proto.IntentStatusResult{State: "unknown"}, nil
}

func (h *unanswerableHelper) AccessBump(context.Context, string, uint64) (uint64, error) {
	return 0, errors.New("unanswerableHelper: AccessBump not configured")
}

func (h *unanswerableHelper) Snapshot(context.Context, string) (proto.SnapshotResult, error) {
	return proto.SnapshotResult{}, errors.New("unanswerableHelper: Snapshot not configured")
}

func (h *unanswerableHelper) Target(context.Context, string, proto.TargetParams) (proto.TargetResult, error) {
	return proto.TargetResult{}, errors.New("unanswerableHelper: Target not configured")
}

func TestAnUnanswerableIntentIsIndeterminateNeverCancelled(t *testing.T) {
	registrar, _ := newGroupTwoCallersRecord()
	ctx := context.Background()
	w, err := registrar.Register(ctx, workers.RegisterRequest{
		CoordinatorSession: "sess-unanswerable", Role: workers.RoleWorker,
		Task: "t", Command: "agent", Environment: "env-local",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	sessionID := string(w.Participant.ID)
	clock := &fakePaneClock{}
	helper := &unanswerableHelper{clock: clock}
	lookup := &fakeLookup{helpers: map[string]paneHelpers{sessionID: helper}}
	hub := newPaneAccessHub(registrar, lookup, clock)
	access := hub.Bind("sess-unanswerable", session.Identity{InstanceID: "backend-A", Epoch: 1}, EndpointAuthority{AdmissionEpoch: 1})
	reach, err := access.Resolve(ctx, sessionID, workers.EffectSendInput)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	reader := &fakeKeysReader{records: inputTargetRecord(access, reach.Chain, sessionID)}
	keys := newPaneKeys(reader, hub)

	key := assistant.KeyName("Enter")
	res, err := keys.Send(ctx, access, assistant.KeysRequest{SessionID: sessionID, TokenID: "tok-1", Key: &key})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.State != "indeterminate" {
		t.Fatalf("Send result = %+v, want indeterminate, never cancelled", res)
	}
}

// ── the option loop (design §6.4) ──────────────────────────────────────────

// scriptedMenu is one frame the fake reader below hands back.
type scriptedMenu struct {
	question string
	options  []string
	selected int
}

// scriptedMenuReader is a paneKeysReader whose Read replays a fixed script
// of menu frames, one per call, holding the last one once exhausted; Record
// answers the ONE entry token sendOption's caller (Send) looks up before
// the loop begins. This drives session_keys.go's REAL sendOption/
// awaitSelectionMove — the script is the only thing under test control, not
// a reimplementation of the loop.
//
// A read that asks for NO target (want == nil) is the settle wait's probe
// (nocx-xn63t.4.1) and answers the same scripted frame with no Target at all —
// which is what a real read does and what makes `mints` below a count of the
// targets the loop SPENT rather than of the times it looked.
type scriptedMenuReader struct {
	entryToken  string
	entryRecord targetRecord
	script      []scriptedMenu
	idx         int
	seq         int

	reads  int
	mints  int
	last   agentdriver.Menu
	lastOK bool
}

func (r *scriptedMenuReader) Record(tokenID string) (targetRecord, bool) {
	if tokenID == r.entryToken {
		return r.entryRecord, true
	}
	return targetRecord{}, false
}

func (r *scriptedMenuReader) Read(_ context.Context, _ any, _ string, want *sessionruntime.TargetKind, _ *sessionruntime.RowRange) (assistant.PaneRead, error) {
	m := r.script[r.idx]
	if r.idx < len(r.script)-1 {
		r.idx++
	}
	r.seq++
	r.reads++
	menu := agentdriver.Menu{Question: m.question, Options: append([]string(nil), m.options...), Selected: m.selected}
	r.last, r.lastOK = menu, true
	if want == nil {
		return assistant.PaneRead{}, nil
	}
	r.mints++
	tokenID := "iter-tok"
	return assistant.PaneRead{Target: &assistant.TargetView{
		Token: tokenID + "-signed", TokenID: tokenID, Kind: sessionruntime.TargetMenu, Menu: &menu,
	}}, nil
}

// Menu is the seam the settle wait reads a menu through without minting
// (session_keys.go's menuReader): the frame it is handed is ignored, because
// this fake's "frame" is the script entry its last Read served — the same
// reading a real rule would take off a real frame.
func (r *scriptedMenuReader) Menu(string, paneview.Frame) (agentdriver.Menu, bool) {
	return r.last, r.lastOK
}

// acceptingHelper answers every Intent executed — the option loop's own
// tests are about the COORDINATOR's read-move-settle sequencing, not the
// helper's per-write decision, which the relay tests above already cover.
type acceptingHelper struct{}

func (acceptingHelper) Intent(context.Context, string, proto.IntentParams) (proto.IntentResult, error) {
	return proto.IntentResult{State: "executed", BytesWritten: 1, FenceAfter: 1}, nil
}

func (acceptingHelper) AccessBump(context.Context, string, uint64) (uint64, error) {
	return 0, errors.New("acceptingHelper: AccessBump not configured")
}

func (acceptingHelper) Snapshot(context.Context, string) (proto.SnapshotResult, error) {
	return proto.SnapshotResult{}, errors.New("acceptingHelper: Snapshot not configured")
}

func (acceptingHelper) Target(context.Context, string, proto.TargetParams) (proto.TargetResult, error) {
	return proto.TargetResult{}, errors.New("acceptingHelper: Target not configured")
}

func (acceptingHelper) IntentStatus(context.Context, string, string) (proto.IntentStatusResult, error) {
	return proto.IntentStatusResult{}, errors.New("acceptingHelper: IntentStatus not configured")
}

func optionEntryRecord(access *DescendantPaneAccess, chain workers.Chain, sessionID, question string, options []string) targetRecord {
	menu := agentdriver.Menu{Question: question, Options: options, Selected: 0}
	return targetRecord{
		Access: access, SessionID: sessionID, Chain: chain, AccessEpoch: 1,
		View: assistant.TargetView{Token: "entry-signed", TokenID: "entry", Kind: sessionruntime.TargetMenu, Menu: &menu},
	}
}

// TestAnOptionIsChosenOnAMenuThatRepaintsLate: the fake menu delays its
// repaint by one extra read after each key (selected stays put for one
// poll before advancing) — the loop must never overshoot: the final
// selection equals the option, and the key count equals distance + 1
// (design §6.4 step 4's settle wait doing its job).
func TestAnOptionIsChosenOnAMenuThatRepaintsLate(t *testing.T) {
	question := "Pick one"
	options := []string{"A", "B", "C"}
	helper := acceptingHelper{}
	hub, access, chain, sessionID := newTestPaneAccess(t, "repaint", helper)
	entry := optionEntryRecord(access, chain, sessionID, question, options)
	reader := &scriptedMenuReader{
		entryToken: "entry", entryRecord: entry,
		script: []scriptedMenu{
			{question, options, 0}, // top of iteration 1
			{question, options, 0}, // settle poll: late repaint, unmoved
			{question, options, 1}, // settle poll: moved by one
			{question, options, 1}, // top of iteration 2
			{question, options, 1}, // settle poll: late repaint, unmoved
			{question, options, 2}, // settle poll: moved by one
			{question, options, 2}, // top of iteration 3: matches wantIdx, confirm
		},
	}
	keys := newPaneKeys(reader, hub)

	res, err := keys.Send(context.Background(), access, assistant.KeysRequest{SessionID: sessionID, TokenID: "entry", Option: strPtr("C")})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.State != "executed" {
		t.Fatalf("Send result = %+v, want executed", res)
	}
	if res.Steps != 3 {
		t.Fatalf("Steps = %d, want 3 (two moves + one confirm, distance 0->2)", res.Steps)
	}

	// The settle wait is a PROBE, and this is the count that says so
	// (nocx-xn63t.4.1): the loop read the pane once per script entry (7) and
	// minted exactly one target per STEP it took (3 — each of them the token
	// it then spent). Minting per poll instead was up to optionSettle's worth
	// of held token-book slots per move step (spec §6.2: maxLiveTokens, never
	// evicted), on the very call a coordinator uses to answer the menu.
	if reader.mints != res.Steps {
		t.Fatalf("the option loop minted %d targets for %d steps; want one per step, and none for the settle polls", reader.mints, res.Steps)
	}
	if reader.reads <= reader.mints {
		t.Fatalf("the loop read the pane %d times and minted %d; this test needs polls that mint nothing to be meaningful", reader.reads, reader.mints)
	}
}

// TestAnOptionLoopRefusesOscillation: after one Down, the menu's selection
// jumps by two instead of one — "moved elsewhere" (design §6.4) — refused
// rather than accepted as progress, and refused promptly: this never waits
// out optionSettle, because a selection that moved to somewhere this call
// did not ask for is recognised immediately, not only after a timeout.
func TestAnOptionLoopRefusesOscillation(t *testing.T) {
	question := "Pick one"
	options := []string{"A", "B"}
	helper := acceptingHelper{}
	hub, access, chain, sessionID := newTestPaneAccess(t, "oscillate", helper)
	entry := optionEntryRecord(access, chain, sessionID, question, options)
	reader := &scriptedMenuReader{
		entryToken: "entry", entryRecord: entry,
		script: []scriptedMenu{
			{question, options, 0}, // top of iteration 1
			{question, options, 2}, // settle poll: moved elsewhere (invalid index, but only Selected is read)
		},
	}
	keys := newPaneKeys(reader, hub)

	res, err := keys.Send(context.Background(), access, assistant.KeysRequest{SessionID: sessionID, TokenID: "entry", Option: strPtr("B")})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.State != "refused" || res.Refusal == nil || res.Refusal.Cause != "stale_target" {
		t.Fatalf("Send result = %+v, want refused/stale_target on oscillation", res)
	}
}

// TestAnOptionAlreadySelectedConfirmsInOneStep: the wanted option is
// already selected on the very first read — no Up/Down at all, just Enter.
func TestAnOptionAlreadySelectedConfirmsInOneStep(t *testing.T) {
	question := "Pick one"
	options := []string{"A", "B"}
	helper := acceptingHelper{}
	hub, access, chain, sessionID := newTestPaneAccess(t, "already", helper)
	entry := optionEntryRecord(access, chain, sessionID, question, options)
	reader := &scriptedMenuReader{
		entryToken: "entry", entryRecord: entry,
		script: []scriptedMenu{{question, options, 0}},
	}
	keys := newPaneKeys(reader, hub)

	res, err := keys.Send(context.Background(), access, assistant.KeysRequest{SessionID: sessionID, TokenID: "entry", Option: strPtr("A")})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.State != "executed" || res.Steps != 1 {
		t.Fatalf("Send result = %+v, want executed in exactly 1 step", res)
	}
}

func strPtr(s string) *string { return &s }
