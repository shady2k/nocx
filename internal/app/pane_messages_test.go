package app

// session.message's own acceptance tests (design §8, Task 10): the queue,
// idempotency, delivery sequencing, cancel's exact response shapes, and the
// authority/restart boundaries of §8.6. These exercise the REAL paneMessages
// (pane_messages.go) against fakes at the paneKeysReader/PaneKeys seam — the
// same style session_keys_test.go already uses for PaneKeys itself (its own
// header explains why: a real helper runtime and PTY were not this task's
// time budget either, and this report says so explicitly).

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/sessionruntime"
	"github.com/shady2k/nocx/internal/workers"
)

// boxFrame is a one-row frame whose only row reads text — the shape
// pasteReady/pasteStep/waitForEcho/confirmSubmission all read the input box
// through (regionText over Rows{0,0}).
func boxFrame(text string) paneview.Frame {
	cells := make([]paneview.Cell, 0, len(text))
	for _, r := range text {
		cells = append(cells, paneview.Cell{Text: string(r), Width: 1})
	}
	return paneview.Frame{Cols: 80, Rows: 1, Lines: [][]paneview.Cell{cells}}
}

// fakeMsgReader is the paneKeysReader (session_keys.go) pane_messages.go's
// delivery steps mint fresh targets through. frame/classification/available
// are mutated live — by the test directly, or by fakeMsgKeys' onSend hook —
// so a script can change what the pane shows exactly when a real helper
// would have (the paste landed; Enter cleared the box), never on a timer.
type fakeMsgReader struct {
	mu             sync.Mutex
	frame          paneview.Frame
	classification agentdriver.State
	// available is which target kind currently mints from this frame — ""
	// means none (an unidentifiable input box, modelling a menu up).
	available sessionruntime.TargetKind
	rows      sessionruntime.RowRange
	tokenSeq  int
	readErr   error
	agent     string

	mintCalls []sessionruntime.TargetKind
	records   map[string]targetRecord

	// flipAfterMints, when > 0, switches which kind is available starting
	// from the mint call AFTER this many have been recorded — modelling a
	// menu appearing partway through a delivery (e.g. between the echo
	// check and the Enter mint) keyed on call count, never on a real wait.
	flipAfterMints   int
	flippedAvailable sessionruntime.TargetKind
}

func newFakeMsgReader(boxText string) *fakeMsgReader {
	return &fakeMsgReader{
		frame: boxFrame(boxText), classification: agentdriver.StateFreeText,
		available: sessionruntime.TargetInput, rows: sessionruntime.RowRange{First: 0, Last: 0},
		agent:   "claude",
		records: make(map[string]targetRecord),
	}
}

func (r *fakeMsgReader) setBox(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frame = boxFrame(text)
}

func (r *fakeMsgReader) setAvailable(kind sessionruntime.TargetKind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.available = kind
}

func (r *fakeMsgReader) AgentFor(string) string { return r.agent }

func (r *fakeMsgReader) Record(tokenID string) (targetRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[tokenID]
	return rec, ok
}

func (r *fakeMsgReader) Read(_ context.Context, access any, _ string, want *sessionruntime.TargetKind, _ *sessionruntime.RowRange) (assistant.PaneRead, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.readErr != nil {
		return assistant.PaneRead{}, r.readErr
	}
	read := assistant.PaneRead{Frame: r.frame, Classification: r.classification}
	if want == nil {
		return read, nil
	}
	r.mintCalls = append(r.mintCalls, *want)
	avail := r.available
	if r.flipAfterMints > 0 && len(r.mintCalls) > r.flipAfterMints {
		avail = r.flippedAvailable
	}
	if avail == "" || *want != avail {
		return read, nil
	}
	r.tokenSeq++
	tokenID := fmt.Sprintf("tok-%d", r.tokenSeq)
	view := assistant.TargetView{Kind: avail, TokenID: tokenID, Rows: r.rows}
	read.Target = &view
	da, _ := access.(*DescendantPaneAccess)
	r.records[tokenID] = targetRecord{Access: da, View: view}
	return read, nil
}

// fakeMsgKeys is the PaneKeys (session_keys.go) delivery spends targets
// through. onSend fires synchronously after recording the call and before
// answering, which is how a script mutates the reader's own frame exactly
// when a real paste or Enter would have changed it.
type fakeMsgKeys struct {
	mu          sync.Mutex
	calls       []assistant.KeysRequest
	pasteResult assistant.KeysResult
	pasteErr    error
	enterResult assistant.KeysResult
	enterErr    error
	onSend      func(req assistant.KeysRequest)
}

func (k *fakeMsgKeys) Send(_ context.Context, _ any, req assistant.KeysRequest) (assistant.KeysResult, error) {
	k.mu.Lock()
	k.calls = append(k.calls, req)
	var result assistant.KeysResult
	var err error
	if req.Text != nil {
		result, err = k.pasteResult, k.pasteErr
	} else {
		result, err = k.enterResult, k.enterErr
	}
	onSend := k.onSend
	k.mu.Unlock()
	if onSend != nil {
		onSend(req)
	}
	return result, err
}

func (k *fakeMsgKeys) callCount() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.calls)
}

func (k *fakeMsgKeys) enterCalls() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := 0
	for _, c := range k.calls {
		if c.Key != nil {
			n++
		}
	}
	return n
}

// happyKeys is a fakeMsgKeys scripted for the ordinary path: the paste
// echoes verbatim into the box and Enter clears it (simulating submission),
// both through onSend so no test waits on a real timer for either.
func happyKeys(reader *fakeMsgReader, text string) *fakeMsgKeys {
	k := &fakeMsgKeys{
		pasteResult: assistant.KeysResult{State: "executed", BytesWritten: len(text)},
		enterResult: assistant.KeysResult{State: "executed"},
	}
	k.onSend = func(req assistant.KeysRequest) {
		if req.Text != nil {
			reader.setBox(*req.Text)
		} else if req.Key != nil {
			reader.setBox("")
		}
	}
	return k
}

// newMessagesTestAccess mints a DescendantPaneAccess over a fresh registrar
// with one registered worker — the same fixture newTestPaneAccess
// (session_keys_test.go) builds, reused here rather than a second
// registrar-and-worker setup for the identical fixture.
func newMessagesTestAccess(t *testing.T, suffix string) (*paneAccessHub, *DescendantPaneAccess, string) {
	t.Helper()
	hub, access, _, sessionID := newTestPaneAccess(t, "msg-"+suffix, &fixedResultHelper{})
	return hub, access, sessionID
}

// messagesTestRules is a real agentdriver.Registry carrying Claude's own
// rule — MenuDisplacesInputBox("claude") answers true from it — so
// deliveryTargetKind picks TargetInput exactly as it does in production,
// matching fakeMsgReader's own agent ("claude") and available kind.
func messagesTestRules(t *testing.T) *agentdriver.Registry {
	t.Helper()
	reg, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("agentdriver registry: %v", err)
	}
	return reg
}

func newTestPaneMessages(t *testing.T, hub *paneAccessHub, reader *fakeMsgReader, keys *fakeMsgKeys) *paneMessages {
	t.Helper()
	return newPaneMessages(keys, reader, hub, messagesTestRules(t), time.Now())
}

// ── idempotency (design §8.4) ──────────────────────────────────────────────

func TestARepeatedIDReturnsTheRecordedPhase(t *testing.T) {
	hub, access, sessionID := newMessagesTestAccess(t, "repeat")
	reader := newFakeMsgReader("")
	keys := happyKeys(reader, "hello")
	pm := newTestPaneMessages(t, hub, reader, keys)

	first, err := pm.Send(context.Background(), access, sessionID, "hello", "now", "id-1", "")
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	if first.Phase != assistant.PhaseSubmitted {
		t.Fatalf("first send phase = %q, want submitted", first.Phase)
	}
	pastesBefore := keys.callCount()

	second, err := pm.Send(context.Background(), access, sessionID, "hello", "now", "id-1", "")
	if err != nil {
		t.Fatalf("repeated send: %v", err)
	}
	if second.Phase != first.Phase || second.ID != first.ID {
		t.Fatalf("repeated send = %+v, want the recorded %+v", second, first)
	}
	if keys.callCount() != pastesBefore {
		t.Fatalf("a repeated id with the same payload re-delivered: calls %d -> %d", pastesBefore, keys.callCount())
	}
}

func TestAReusedIDWithADifferentPayloadIsRefused(t *testing.T) {
	hub, access, sessionID := newMessagesTestAccess(t, "reuse")
	reader := newFakeMsgReader("")
	keys := happyKeys(reader, "hello")
	pm := newTestPaneMessages(t, hub, reader, keys)

	if _, err := pm.Send(context.Background(), access, sessionID, "hello", "now", "id-1", ""); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if _, err := pm.Send(context.Background(), access, sessionID, "goodbye", "now", "id-1", ""); err == nil {
		t.Fatal("send with the same id and a different payload succeeded, want id_reused")
	}
}

// TestKeysDifferingOnlyInControllerIdentityOrAdmissionEpochOrParticipantIncarnationDoNotCollide
// asserts design §8.4's own field list is load-bearing: two calls sharing
// namespace/id/text/when but differing in exactly one of those three do not
// share a record — each gets its own delivery.
func TestKeysDifferingOnlyInControllerIdentityOrAdmissionEpochOrParticipantIncarnationDoNotCollide(t *testing.T) {
	registrar, _ := newGroupTwoCallersRecord()
	ctx := context.Background()
	reader := newFakeMsgReader("")
	keys := happyKeys(reader, "hi")
	hub := newPaneAccessHub(registrar, nil, nil)
	pm := newTestPaneMessages(t, hub, reader, keys)

	register := func(controller string) string {
		w, err := registrar.Register(ctx, workers.RegisterRequest{
			CoordinatorSession: controller, Role: workers.RoleWorker,
			Task: "t", Command: "agent", Environment: "env-local",
		})
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		return string(w.Participant.ID)
	}

	// Same controller session, different admission epoch.
	controllerA := "sess-collide-A"
	sessA := register(controllerA)
	accessEpoch1 := hub.Bind(controllerA, session.Identity{InstanceID: "backend-A", Epoch: 1}, EndpointAuthority{AdmissionEpoch: 1})
	accessEpoch2 := hub.Bind(controllerA, session.Identity{InstanceID: "backend-A", Epoch: 1}, EndpointAuthority{AdmissionEpoch: 2})
	if _, err := pm.Send(ctx, accessEpoch1, sessA, "hi", "now", "same-id", ""); err != nil {
		t.Fatalf("send under epoch 1: %v", err)
	}
	if _, err := pm.Send(ctx, accessEpoch2, sessA, "hi", "now", "same-id", ""); err != nil {
		t.Fatalf("send under epoch 2 with the same id collided with epoch 1's record: %v", err)
	}

	// Different controller session (and so a different participant/chain).
	controllerB := "sess-collide-B"
	sessB := register(controllerB)
	accessB := hub.Bind(controllerB, session.Identity{InstanceID: "backend-A", Epoch: 1}, EndpointAuthority{AdmissionEpoch: 1})
	if _, err := pm.Send(ctx, accessB, sessB, "hi", "now", "same-id", ""); err != nil {
		t.Fatalf("send under a different controller with the same id collided: %v", err)
	}

	// Different backend identity (session.Identity), same controller and epoch.
	accessIdentity2 := hub.Bind(controllerA, session.Identity{InstanceID: "backend-B", Epoch: 1}, EndpointAuthority{AdmissionEpoch: 1})
	if _, err := pm.Send(ctx, accessIdentity2, sessA, "hi", "now", "same-id", ""); err != nil {
		t.Fatalf("send under a different session.Identity with the same id collided: %v", err)
	}
}

// ── the paste precondition (design §8.2 step 1) ─────────────────────────────

func TestTextInTheBoxRefusesThePaste(t *testing.T) {
	hub, access, sessionID := newMessagesTestAccess(t, "typing")
	reader := newFakeMsgReader("someone is typing")
	keys := happyKeys(reader, "hello")
	pm := newTestPaneMessages(t, hub, reader, keys)

	view, err := pm.Send(context.Background(), access, sessionID, "hello", "now", "id-1", "")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if view.Phase != assistant.PhaseRefused {
		t.Fatalf("phase = %q, want refused (text already in the box)", view.Phase)
	}
	if keys.callCount() != 0 {
		t.Fatalf("PaneKeys.Send was reached with text in the box: %d calls", keys.callCount())
	}
}

// TestAMenuAppearingBeforeThePasteRefusesTooWhenNow: the input box cannot be
// identified at all (available == "", modelling a menu up) — for when=="now"
// this is a terminal refusal, exactly as "text in the box" is.
func TestAMenuAppearingBeforeThePasteRefusesTooWhenNow(t *testing.T) {
	hub, access, sessionID := newMessagesTestAccess(t, "menu-before")
	reader := newFakeMsgReader("")
	reader.setAvailable("")
	keys := happyKeys(reader, "hello")
	pm := newTestPaneMessages(t, hub, reader, keys)

	view, err := pm.Send(context.Background(), access, sessionID, "hello", "now", "id-1", "")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if view.Phase != assistant.PhaseRefused {
		t.Fatalf("phase = %q, want refused (no identifiable input box)", view.Phase)
	}
	if keys.callCount() != 0 {
		t.Fatalf("PaneKeys.Send was reached with no identifiable input box: %d calls", keys.callCount())
	}
}

// ── the happy paths ─────────────────────────────────────────────────────────

// TestAMessageDuringATurnIsSubmitted: when=="now", the box starts empty, the
// paste echoes and Enter clears the box (submission confirmed) — ends
// submitted, through fakes at the paneKeysReader/PaneKeys seam rather than a
// mock agent program and a real helper runtime (this task's time budget did
// not reach that; see this commit's own report).
func TestAMessageDuringATurnIsSubmitted(t *testing.T) {
	hub, access, sessionID := newMessagesTestAccess(t, "now-submit")
	reader := newFakeMsgReader("")
	keys := happyKeys(reader, "hello there")
	pm := newTestPaneMessages(t, hub, reader, keys)

	view, err := pm.Send(context.Background(), access, sessionID, "hello there", "now", "id-1", "")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if view.Phase != assistant.PhaseSubmitted {
		t.Fatalf("phase = %q, want submitted", view.Phase)
	}
	if keys.enterCalls() != 1 {
		t.Fatalf("Enter was sent %d times, want exactly 1", keys.enterCalls())
	}
}

// TestAFreeMessageIsDeliveredWhenTheAgentIsFree: when=="free" over a pane
// that is ALREADY free (box empty from the start) delivers without the
// caller waiting on it explicitly — Send returns queued, and the background
// delivery this starts reaches submitted on its own.
func TestAFreeMessageIsDeliveredWhenTheAgentIsFree(t *testing.T) {
	hub, access, sessionID := newMessagesTestAccess(t, "free-submit")
	reader := newFakeMsgReader("")
	keys := happyKeys(reader, "hi")
	pm := newTestPaneMessages(t, hub, reader, keys)

	view, err := pm.Send(context.Background(), access, sessionID, "hi", "free", "id-1", "")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if view.Phase != assistant.PhaseQueued {
		t.Fatalf("immediate phase = %q, want queued (delivery is asynchronous for \"free\")", view.Phase)
	}
	waitForCondition(t, "the free message to reach submitted", func() bool {
		for _, m := range pm.Pending(sessionID) {
			if m.ID == "id-1" {
				return m.Phase == assistant.PhaseSubmitted
			}
		}
		return false
	})
}

// TestAnEchoThatNeverAppearsLeavesPartialAndNoEnter: the paste is written,
// but the box never shows the echo — partial, and Enter is never sent. The
// PTY-level assertion the task names ("zero \r bytes") is a real-helper
// fact this fake layer cannot itself observe; the fake-level equivalent
// asserted here is that PaneKeys.Send is never called with a key at all.
func TestAnEchoThatNeverAppearsLeavesPartialAndNoEnter(t *testing.T) {
	hub, access, sessionID := newMessagesTestAccess(t, "no-echo")
	reader := newFakeMsgReader("")
	keys := &fakeMsgKeys{pasteResult: assistant.KeysResult{State: "executed", BytesWritten: 5}}
	// No onSend: the box is never updated after the paste, so the echo
	// never appears. echoWait is shrunk to milliseconds so this test
	// settles the "never appears" fact fast rather than sleeping out the
	// production bound (AGENTS.md: wait on an observable state change,
	// never a duration — applied here to this package's own hard-coded
	// timeout via the field defaultEchoWait's own doc names).
	pm := newTestPaneMessages(t, hub, reader, keys)
	pm.echoWait = 5 * time.Millisecond

	view, err := pm.Send(context.Background(), access, sessionID, "hello", "now", "id-1", "")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if view.Phase != assistant.PhasePartial {
		t.Fatalf("phase = %q, want partial", view.Phase)
	}
	if keys.enterCalls() != 0 {
		t.Fatalf("Enter was sent %d times, want 0 (no echo was ever confirmed)", keys.enterCalls())
	}
}

// TestAMenuBetweenPasteAndEnterRefusesTheEnter: the echo is confirmed, but
// between the echo and the Enter mint a menu appears (the input box can no
// longer be identified) — partial, and Enter is never sent, since enterStep
// refuses rather than minting under a different kind.
func TestAMenuBetweenPasteAndEnterRefusesTheEnter(t *testing.T) {
	hub, access, sessionID := newMessagesTestAccess(t, "menu-between")
	reader := newFakeMsgReader("")
	// Mint order for one delivery: pasteReady (#1), pasteStep (#2),
	// waitForEcho's first (successful) read (#3), enterStep (#4) — the
	// menu appears starting at #4, after the echo was already confirmed.
	reader.flipAfterMints = 3
	reader.flippedAvailable = ""
	keys := &fakeMsgKeys{
		pasteResult: assistant.KeysResult{State: "executed", BytesWritten: 5},
		enterResult: assistant.KeysResult{State: "executed"},
	}
	keys.onSend = func(req assistant.KeysRequest) {
		if req.Text != nil {
			reader.setBox(*req.Text) // the echo appears
		}
	}
	pm := newTestPaneMessages(t, hub, reader, keys)

	view, err := pm.Send(context.Background(), access, sessionID, "hello", "now", "id-1", "")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if view.Phase != assistant.PhasePartial {
		t.Fatalf("phase = %q, want partial", view.Phase)
	}
	if keys.enterCalls() != 0 {
		t.Fatalf("Enter was sent %d times, want 0 (a menu covered the box first)", keys.enterCalls())
	}
}

// ── cancel (design §8.6) ─────────────────────────────────────────────────────

func TestCancelBeforeClaimReturnsCancelled(t *testing.T) {
	hub, access, sessionID := newMessagesTestAccess(t, "cancel-before")
	reader := newFakeMsgReader("")
	reader.setAvailable("") // the delivery never gets past "queued" on its own
	keys := happyKeys(reader, "hi")
	pm := newTestPaneMessages(t, hub, reader, keys)

	// "free" so Send returns before delivery is attempted; the precondition
	// failure (no identifiable box) keeps runQueue retrying rather than
	// terminating, so the record stays "queued" until cancelled.
	if _, err := pm.Send(context.Background(), access, sessionID, "hi", "free", "id-1", ""); err != nil {
		t.Fatalf("send: %v", err)
	}

	result, err := pm.Cancel(context.Background(), access, sessionID, "id-1")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if result.Result != "cancelled" || result.Phase != assistant.PhaseCancelled {
		t.Fatalf("cancel result = %+v, want {cancelled, cancelled}", result)
	}

	// A retry after a lost response gets the same answer (idempotent).
	retry, err := pm.Cancel(context.Background(), access, sessionID, "id-1")
	if err != nil {
		t.Fatalf("cancel retry: %v", err)
	}
	if retry != result {
		t.Fatalf("cancel retry = %+v, want the same %+v", retry, result)
	}
}

func TestCancelUnknownIDReturnsNoSuchMessage(t *testing.T) {
	hub, access, sessionID := newMessagesTestAccess(t, "cancel-unknown")
	reader := newFakeMsgReader("")
	keys := happyKeys(reader, "hi")
	pm := newTestPaneMessages(t, hub, reader, keys)

	result, err := pm.Cancel(context.Background(), access, sessionID, "never-sent")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if result.Result != "no_such_message" || result.Phase != "" {
		t.Fatalf("cancel result = %+v, want {no_such_message} with no phase", result)
	}
}

// TestCancelAfterClaimIsTooLateAndThePasteIsWritten uses fakeMsgKeys' onSend
// as the "test hook between claim and session.intent" the task names: by
// the time the paste's own Send is reached, the record is already claimed
// (pasting) — cancel called from inside onSend, synchronously, sees exactly
// that state.
func TestCancelAfterClaimIsTooLateAndThePasteIsWritten(t *testing.T) {
	hub, access, sessionID := newMessagesTestAccess(t, "cancel-too-late")
	reader := newFakeMsgReader("")
	keys := happyKeys(reader, "hello")
	var cancelResult assistant.CancelResult
	var cancelErr error
	keys.onSend = func(req assistant.KeysRequest) {
		if req.Text != nil {
			cancelResult, cancelErr = pmUnderTest.Cancel(context.Background(), access, sessionID, "id-1")
			reader.setBox(*req.Text)
		} else if req.Key != nil {
			reader.setBox("")
		}
	}
	pmUnderTest = newTestPaneMessages(t, hub, reader, keys)

	view, err := pmUnderTest.Send(context.Background(), access, sessionID, "hello", "now", "id-1", "")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if cancelErr != nil {
		t.Fatalf("cancel during delivery: %v", cancelErr)
	}
	if cancelResult.Result != "too_late" || cancelResult.Phase != assistant.PhasePasting {
		t.Fatalf("cancel during delivery = %+v, want {too_late, pasting}", cancelResult)
	}
	// The paste proceeds regardless of the cancel — it is written, and the
	// delivery may still legitimately reach submitted.
	if view.Phase != assistant.PhaseSubmitted {
		t.Fatalf("final phase = %q, want submitted (the cancel was too late to stop the paste)", view.Phase)
	}
}

// pmUnderTest is TestCancelAfterClaimIsTooLateAndThePasteIsWritten's own
// package-level handle, needed because fakeMsgKeys.onSend is wired before
// the *paneMessages it will call back into exists — a closure over a var
// set immediately afterwards, never read before Send begins.
var pmUnderTest *paneMessages

// ── authority (design §8.6) ─────────────────────────────────────────────────

// TestRevocationAfterPasteLeavesPartialAndTakesNoFurtherStep: the paste
// lands, then (through onSend) the participant is closed — ending its own
// delegation, which is what StillHolds re-checks before the Enter step —
// and the delivery stops at partial rather than sending Enter under a
// chain that no longer holds.
func TestRevocationAfterPasteLeavesPartialAndTakesNoFurtherStep(t *testing.T) {
	registrar, _ := newGroupTwoCallersRecord()
	ctx := context.Background()
	const controller = "sess-revoke-mid"
	w, err := registrar.Register(ctx, workers.RegisterRequest{
		CoordinatorSession: controller, Role: workers.RoleWorker,
		Task: "t", Command: "agent", Environment: "env-local",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	sessionID := string(w.Participant.ID)
	hub := newPaneAccessHub(registrar, nil, nil)
	access := hub.Bind(controller, session.Identity{InstanceID: "backend-A", Epoch: 1}, EndpointAuthority{AdmissionEpoch: 1})

	reader := newFakeMsgReader("")
	keys := happyKeys(reader, "hello")
	keys.onSend = func(req assistant.KeysRequest) {
		if req.Text != nil {
			reader.setBox(*req.Text)
			if closeErr := registrar.Close(ctx, controller, w.Participant.ID); closeErr != nil {
				t.Errorf("close (revoke) mid-delivery: %v", closeErr)
			}
		}
	}
	pm := newTestPaneMessages(t, hub, reader, keys)

	view, err := pm.Send(ctx, access, sessionID, "hello", "now", "id-1", "")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if view.Phase != assistant.PhasePartial {
		t.Fatalf("phase = %q, want partial (revoked between the paste and the Enter)", view.Phase)
	}
	if keys.enterCalls() != 0 {
		t.Fatalf("Enter was sent %d times, want 0 (authority no longer held)", keys.enterCalls())
	}
}

// TestACallerDisconnectDoesNotCancelADelivery: the ctx a "now" call was made
// under is cancelled the instant the paste's own Send is reached — the
// delivery, running on its own detached background context, still reaches
// submitted.
func TestACallerDisconnectDoesNotCancelADelivery(t *testing.T) {
	hub, access, sessionID := newMessagesTestAccess(t, "disconnect")
	reader := newFakeMsgReader("")
	keys := happyKeys(reader, "hello")
	ctx, cancel := context.WithCancel(context.Background())
	keys.onSend = func(req assistant.KeysRequest) {
		if req.Text != nil {
			cancel() // the caller is gone; the delivery must not notice
			reader.setBox(*req.Text)
		} else if req.Key != nil {
			reader.setBox("")
		}
	}
	pm := newTestPaneMessages(t, hub, reader, keys)

	view, err := pm.Send(ctx, access, sessionID, "hello", "now", "id-1", "")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	// Send itself may return early once ctx is cancelled (it selects on
	// ctx.Done() alongside the delivery's own completion), so the delivery
	// is watched to its OWN conclusion via Pending rather than trusting
	// view's own phase to already be final.
	_ = view
	waitForCondition(t, "the disconnected delivery to reach submitted", func() bool {
		for _, m := range pm.Pending(sessionID) {
			if m.ID == "id-1" {
				return m.Phase == assistant.PhaseSubmitted
			}
		}
		return false
	})
}

// ── restart (design §8.6) ────────────────────────────────────────────────────

func TestARestartReportsDeliveryStateLost(t *testing.T) {
	hub, access, sessionID := newMessagesTestAccess(t, "restart")
	reader := newFakeMsgReader("")
	keys := happyKeys(reader, "hi")
	before := time.Now()
	pm1 := newPaneMessages(keys, reader, hub, messagesTestRules(t), before)
	if _, err := pm1.Send(context.Background(), access, sessionID, "hi", "now", "id-1", ""); err != nil {
		t.Fatalf("send: %v", err)
	}
	if lost := pm1.DeliveryLost(sessionID); lost != nil {
		t.Fatalf("DeliveryLost on the instance that itself delivered = %v, want nil", lost)
	}

	after := before.Add(time.Minute)
	pm2 := newPaneMessages(keys, reader, hub, messagesTestRules(t), after)
	lost := pm2.DeliveryLost(sessionID)
	if lost == nil || !lost.Equal(after) {
		t.Fatalf("DeliveryLost on a fresh instance = %v, want %v (its own startedAt)", lost, after)
	}
	if len(pm2.Pending(sessionID)) != 0 {
		t.Fatalf("Pending on a fresh instance = %v, want empty (the queue is in memory only)", pm2.Pending(sessionID))
	}
}

// ── concurrency (design §8.5's lock order, "no deadlock with a concurrent
// revocation") ───────────────────────────────────────────────────────────────

// TestNoDeadlockUnderConcurrentRevocation drives many sends against a
// pane whose participant is repeatedly revoked and re-registered from
// another goroutine, watchdogged rather than timed: a hang fails the test,
// a slow machine does not.
func TestNoDeadlockUnderConcurrentRevocation(t *testing.T) {
	const iterations = 1000
	registrar, _ := newGroupTwoCallersRecord()
	ctx := context.Background()
	const controller = "sess-concurrent-revoke"

	hub := newPaneAccessHub(registrar, nil, nil)
	reader := newFakeMsgReader("")
	keys := happyKeys(reader, "hi")
	pm := newTestPaneMessages(t, hub, reader, keys)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < iterations; i++ {
			w, err := registrar.Register(ctx, workers.RegisterRequest{
				CoordinatorSession: controller, Role: workers.RoleWorker,
				Task: "t", Command: "agent", Environment: "env-local",
			})
			if err != nil {
				continue
			}
			sessionID := string(w.Participant.ID)
			access := hub.Bind(controller, session.Identity{InstanceID: "backend-A", Epoch: 1}, EndpointAuthority{AdmissionEpoch: 1})
			id := fmt.Sprintf("id-%d", i)
			go func() { _, _ = pm.Send(ctx, access, sessionID, "hi", "now", id, "") }()
			_ = registrar.Close(ctx, controller, w.Participant.ID)
		}
	}()

	select {
	case <-done:
	case <-time.After(watchdogBound):
		t.Fatal("timed out: a concurrent revocation deadlocked a delivery")
	}
}

// waitForCondition polls cond until it is true, watchdogged rather than
// timed (AGENTS.md: wait on an observable state change, never a duration).
func waitForCondition(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(watchdogBound)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(messagePollInterval)
	}
}
