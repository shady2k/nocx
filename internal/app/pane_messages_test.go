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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/assistant"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/sessionruntime"
	"github.com/shady2k/nocx/internal/workers"
)

// boxFrameCols is the width this fake box DRAWS AT when the text it holds fits
// — the same order as a real Claude pane's own 120 columns, narrowed so a
// frame stays cheap to build.
const boxFrameCols = 80

// boxFrame builds a REAL Claude input box around text, not a bare one-row
// frame carrying it raw: pasteReady/waitForEcho/confirmSubmission now answer
// "is the box empty" and "did it echo" through agentdriver.Observation.
// InputText (nocx-6q1uh.18), which reads a rule's own chrome — the two
// full-width "─" rules, the "❯" marker at column 0, a NO-BREAK SPACE after
// it (claude.go's own note has the corpus evidence for why that one cell is
// not the ordinary space it prints as) — rather than trimming a raw span. A
// fake reader whose frame is not shaped like that box answers ok=false on
// every read, since messagesTestRules wires the REAL claude.rule.json and
// this rule cannot find its own chrome in a frame that never drew any.
//
// THE FRAME IS AS WIDE AS THE TEXT IT HOLDS, and that is a fixture decision
// rather than a claim about terminals: a box that truncated a paste at some
// fixed column would answer waitForEcho with a PREFIX of what was pasted, so
// a message the product delivers fine would stop at phase partial here and
// every test in this file would be measuring the fake's own width. What a REAL
// agent draws when a paste is longer than its box — the wrap, the space the
// wrap consumes at each row break, and the reading that puts it back together
// — is the corpus's own business and is asserted there, off real frames
// (TestAQueuedMessageLongerThanOneBoxRowIsPastedEchoedAndEntered, and
// internal/agentdriver's own input_text_test.go).
//
// Two blank rows precede the box for the same reason a real Claude screen
// always has room above it: claude.rule.json's "meter" anchor sits one row
// above the box's own top rule, and an anchor computed from an out-of-frame
// row never binds (agentdriver's own guard) — which would misroute this
// synthetic frame into the "unknown" branch (anchorUnbound: meter) before
// ever reaching free_text.
func boxFrame(text string) paneview.Frame {
	// "❯" then a NO-BREAK SPACE (U+00A0) — the exact two cells every real
	// prompt row in the corpus opens with — then text, padded with ordinary
	// spaces to the frame's own width.
	promptRunes := []rune("❯\u00a0" + text)
	cols := max(boxFrameCols, len(promptRunes)+1)
	rule := func() []paneview.Cell {
		cells := make([]paneview.Cell, cols)
		for x := range cells {
			cells[x] = paneview.Cell{Text: "─", Width: 1}
		}
		return cells
	}
	blank := func() []paneview.Cell {
		cells := make([]paneview.Cell, cols)
		for x := range cells {
			cells[x] = paneview.Cell{Text: " ", Width: 1}
		}
		return cells
	}
	promptRow := make([]paneview.Cell, cols)
	for x := range promptRow {
		if x < len(promptRunes) {
			promptRow[x] = paneview.Cell{Text: string(promptRunes[x]), Width: 1}
			continue
		}
		promptRow[x] = paneview.Cell{Text: " ", Width: 1}
	}
	const promptY = 3
	return paneview.Frame{
		Cols:    cols,
		Rows:    5,
		Lines:   [][]paneview.Cell{blank(), blank(), rule(), promptRow, rule()},
		CursorX: len(promptRunes),
		CursorY: promptY,
	}
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
	// available is the kind this frame HONESTLY offers: TargetInput for a
	// readable input box, TargetMenu for a real menu, "" for a frame that
	// offers the caller nothing (a menu covering the box, or a box nobody
	// can read).
	available sessionruntime.TargetKind
	rows      sessionruntime.RowRange
	tokenSeq  int
	readErr   error
	agent     string
	// chain/accessEpoch are what a minted target's own coordinator record
	// carries (design §6.2) — a test that SPENDS one of this reader's tokens
	// through the REAL paneKeys needs them, because PaneKeys re-checks
	// StillHolds on the chain before committing a step.
	chain       workers.Chain
	accessEpoch uint64

	// reads counts every Read, minted or not. It is what makes a probe's
	// POLLING visible to a test that must assert on it without sleeping:
	// "the delivery re-read the pane at least this many times" is an
	// observable state change, never a duration.
	reads     int
	mintCalls []sessionruntime.TargetKind
	records   map[string]targetRecord

	// bookCapacity models the helper's token book (spec §6.2): at most this
	// many LIVE slots, and a mint with none free is refused `capacity`
	// exactly as the helper refuses it, before a token exists. 0 means
	// unlimited. liveSlots counts the mints this reader has handed out,
	// which is what makes a probe that MINTS WITHOUT SPENDING visible: it
	// holds a slot nobody asked for. capacityRefusals counts the refusals,
	// so a test can say which of the two happened rather than guess.
	bookCapacity     int
	liveSlots        int
	capacityRefusals int

	// onRead, when non-nil, runs after the nth Read (1-based) with r's own
	// lock RELEASED — so a hook may call setBox/setClassification/menuUp
	// without deadlocking. That is how a test makes the pane repaint (an echo
	// arriving late) or draw a menu at a chosen moment in a delivery, rather
	// than on a timer or on a count of the targets somebody minted.
	onRead func(n int)
}

func newFakeMsgReader(boxText string) *fakeMsgReader {
	return &fakeMsgReader{
		frame: boxFrame(boxText), classification: agentdriver.StateFreeText,
		available: sessionruntime.TargetInput, rows: sessionruntime.RowRange{First: 0, Last: 0},
		agent:   "claude",
		records: make(map[string]targetRecord),
	}
}

// mintInputTarget mints a target through reader.Read exactly as a caller's
// own prior session.read would (design §8.1: "when=now requires an input or
// working target"), and returns its tokenID — empty when the box was not
// currently identifiable as sessionruntime.TargetInput, which is the same
// case a real session.read leaves a caller with no tokenId to present.
// pane_messages.go's Send validates this tokenId before ever building a
// message record, so every "now" send below must mint one first: a caller
// that never read one is not a caller the production code admits.
func mintInputTarget(t *testing.T, reader *fakeMsgReader, access any, sessionID string) string {
	t.Helper()
	return mintTarget(t, reader, access, sessionID, sessionruntime.TargetInput)
}

// mintTarget mints a target of any kind through reader.Read, exactly as a
// caller's own prior session.read would (design §6.1) — the general form of
// mintInputTarget below, for a caller answering a MENU rather than typing
// into the box. It returns the tokenID, empty when nothing of that kind
// minted from the frame as it stands.
func mintTarget(t *testing.T, reader *fakeMsgReader, access any, sessionID string, kind sessionruntime.TargetKind) string {
	t.Helper()
	read, err := reader.Read(context.Background(), access, sessionID, &kind, nil)
	if err != nil {
		t.Fatalf("mint %s target: %v", kind, err)
	}
	if read.Target == nil {
		return ""
	}
	return read.Target.TokenID
}

// realMenuFrame replays a real permission-menu moment from the corpus and
// answers it with the state the SHIPPED claude rule classifies it as — the
// pair a real pane hands session.read, rather than a synthetic frame beside
// a classification a test asserted by hand. The premise (this frame IS a
// menu moment) is checked here, so no assertion below can quietly rest on a
// frame that stopped being one.
func realMenuFrame(t *testing.T) (paneview.Frame, agentdriver.State) {
	t.Helper()
	frame := happyReplayCapture(t, "claude-permission", 49000)
	state := messagesTestRules(t).Classify("claude", frame)
	if state != agentdriver.StatePermissionChoice && state != agentdriver.StateModalChoice {
		t.Fatalf("claude-permission@49s classifies as %q, want a menu state", state)
	}
	return frame, state
}

// menuUp puts reader on a real permission-menu moment: the corpus's own
// frame, the shipped rule's own classification, and no input target minting
// from it (nocx-6q1uh.10's measurement — a menu always displaces claude's
// input box, so the box's own span goes unbound).
//
// Earlier tests here modelled "a menu is up" as available == "" alone,
// beside a synthetic empty box. That model stopped being the whole truth the
// moment the readiness probe stopped minting to find out: a probe that reads
// the pane instead of minting from it asks the frame and the classification,
// and a fake that says "no target mints here" while drawing an ordinary
// readable box answers a question no real pane can be in.
func menuUp(t *testing.T, reader *fakeMsgReader) {
	t.Helper()
	frame, state := realMenuFrame(t)
	reader.setFrame(frame, state, "")
}

// setFrame puts the reader on a whole new frame at once — the frame, the
// state the shipped rule classifies it as, and the kind it honestly offers.
// A frame replayed from the corpus is always set this way rather than field
// by field, so no test can leave a reader holding a real screen beside a
// classification or an availability nothing measured.
func (r *fakeMsgReader) setFrame(frame paneview.Frame, state agentdriver.State, available sessionruntime.TargetKind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frame = frame
	r.classification = state
	r.available = available
}

func (r *fakeMsgReader) setBox(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frame = boxFrame(text)
}

func (r *fakeMsgReader) setClassification(state agentdriver.State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.classification = state
}

func (r *fakeMsgReader) setAvailable(kind sessionruntime.TargetKind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.available = kind
}

func (r *fakeMsgReader) readCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads
}

func (r *fakeMsgReader) mintedKinds() []sessionruntime.TargetKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sessionruntime.TargetKind(nil), r.mintCalls...)
}

func (r *fakeMsgReader) bookState() (live, refusals int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.liveSlots, r.capacityRefusals
}

func (r *fakeMsgReader) AgentFor(string) string { return r.agent }

func (r *fakeMsgReader) Record(tokenID string) (targetRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[tokenID]
	return rec, ok
}

func (r *fakeMsgReader) Read(_ context.Context, access any, sessionID string, want *sessionruntime.TargetKind, _ *sessionruntime.RowRange) (assistant.PaneRead, error) {
	r.mu.Lock()
	if r.readErr != nil {
		err := r.readErr
		r.mu.Unlock()
		return assistant.PaneRead{}, err
	}
	r.reads++
	n := r.reads
	read := assistant.PaneRead{Frame: r.frame, Classification: r.classification}
	if want != nil {
		r.mintCalls = append(r.mintCalls, *want)
		// A read asking for a kind this frame does not offer does NOT fail:
		// the read path falls back to a target over the whole screen
		// (session_targets.go's chooseTargetRows), which the helper mints and
		// the token book counts like any other, and the caller simply gets a
		// kind it did not ask for. That fallback is not a detail here — one
		// such mint per messagePollInterval is exactly how the book the owner
		// hit filled (nocx-xn63t.4.1) — so the fake models it rather than
		// answering "nothing minted".
		kind := r.available
		if kind == "" || *want != kind {
			kind = sessionruntime.TargetRegion
		}
		switch {
		case r.bookCapacity > 0 && r.liveSlots >= r.bookCapacity:
			// The helper's own token-book refusal (spec §6.2): a full book
			// refuses BEFORE a token exists, as a wire refusal — the same
			// *RefusalError the real client hands internal/app.
			r.capacityRefusals++
			r.mu.Unlock()
			r.afterRead(n)
			return assistant.PaneRead{}, &helperclient.RefusalError{Code: proto.ErrCodeCapacity, Message: "session: capacity"}
		default:
			r.tokenSeq++
			r.liveSlots++
			tokenID := fmt.Sprintf("tok-%d", r.tokenSeq)
			view := assistant.TargetView{Kind: kind, TokenID: tokenID, Rows: r.rows}
			read.Target = &view
			da, _ := access.(*DescendantPaneAccess)
			r.records[tokenID] = targetRecord{
				Access: da, View: view, Chain: r.chain, AccessEpoch: r.accessEpoch, SessionID: sessionID,
			}
		}
	}
	r.mu.Unlock()
	r.afterRead(n)
	return read, nil
}

// afterRead runs onRead, when a test set one, OUTSIDE the reader's lock —
// see onRead's own doc for why that matters.
func (r *fakeMsgReader) afterRead(n int) {
	r.mu.Lock()
	hook := r.onRead
	r.mu.Unlock()
	if hook != nil {
		hook(n)
	}
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

// textCalls counts the paste atoms this fake was handed that carried exactly
// text — how a test says WHICH message reached the pane, and how many times,
// when more than one delivery can be in flight at once.
func (k *fakeMsgKeys) textCalls(text string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := 0
	for _, c := range k.calls {
		if c.Text != nil && *c.Text == text {
			n++
		}
	}
	return n
}

// happyKeys is a fakeMsgKeys scripted for the ordinary path: the paste
// echoes into the box and Enter clears it (simulating submission), both
// through onSend so no test waits on a real timer for either.
//
// A MULTI-LINE PASTE ECHOES AS CLAUDE'S OWN PLACEHOLDER, never as the text:
// the agent collapses it in the box to "[Pasted text #1 +N lines]", and that
// placeholder is what the shipped echo check accepts for a text carrying
// newlines (boxContainsEcho, measured at nocx-xn63t.4.5). A fake that drew the
// text itself would let a multi-line paste pass a check the real pane fails —
// which is the wrong direction for a fixture whose whole job is to be a pane
// a caller can trust.
func happyKeys(reader *fakeMsgReader, text string) *fakeMsgKeys {
	k := &fakeMsgKeys{
		pasteResult: assistant.KeysResult{State: "executed", BytesWritten: len(text)},
		enterResult: assistant.KeysResult{State: "executed"},
	}
	k.onSend = func(req assistant.KeysRequest) {
		if req.Text != nil {
			reader.setBox(pastedEcho(*req.Text))
		} else if req.Key != nil {
			reader.setBox("")
		}
	}
	return k
}

// pastedEcho is what a real Claude input box shows once a paste has landed:
// the text itself when it fits on one line, and its own placeholder naming the
// extra lines when it does not.
func pastedEcho(text string) string {
	if extra := strings.Count(text, "\n"); extra > 0 {
		return fmt.Sprintf("[Pasted text #1 +%d lines]", extra)
	}
	return text
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

	token := mintInputTarget(t, reader, access, sessionID)
	first, err := pm.Send(context.Background(), access, sessionID, "hello", "now", "id-1", token)
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	if first.Phase != assistant.PhaseSubmitted {
		t.Fatalf("first send phase = %q, want submitted", first.Phase)
	}
	pastesBefore := keys.callCount()

	// The idempotent replay is answered from the record before the tokenId
	// is ever consulted for a live target — same key, same hash — but a
	// "now" send still requires ONE, so a fresh mint stands in for whatever
	// tokenId the retrying caller's own second session.read would have named.
	token2 := mintInputTarget(t, reader, access, sessionID)
	second, err := pm.Send(context.Background(), access, sessionID, "hello", "now", "id-1", token2)
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

	token := mintInputTarget(t, reader, access, sessionID)
	if _, err := pm.Send(context.Background(), access, sessionID, "hello", "now", "id-1", token); err != nil {
		t.Fatalf("first send: %v", err)
	}
	token2 := mintInputTarget(t, reader, access, sessionID)
	if _, err := pm.Send(context.Background(), access, sessionID, "goodbye", "now", "id-1", token2); err == nil {
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
	// Each distinct access below is minted its OWN tokenId: the fake reader
	// records a target against the exact access it was minted under
	// (rec.Access != da refuses a token minted under someone else's), the
	// same binding a real session.read would carry.
	if _, err := pm.Send(ctx, accessEpoch1, sessA, "hi", "now", "same-id", mintInputTarget(t, reader, accessEpoch1, sessA)); err != nil {
		t.Fatalf("send under epoch 1: %v", err)
	}
	if _, err := pm.Send(ctx, accessEpoch2, sessA, "hi", "now", "same-id", mintInputTarget(t, reader, accessEpoch2, sessA)); err != nil {
		t.Fatalf("send under epoch 2 with the same id collided with epoch 1's record: %v", err)
	}

	// Different controller session (and so a different participant/chain).
	controllerB := "sess-collide-B"
	sessB := register(controllerB)
	accessB := hub.Bind(controllerB, session.Identity{InstanceID: "backend-A", Epoch: 1}, EndpointAuthority{AdmissionEpoch: 1})
	if _, err := pm.Send(ctx, accessB, sessB, "hi", "now", "same-id", mintInputTarget(t, reader, accessB, sessB)); err != nil {
		t.Fatalf("send under a different controller with the same id collided: %v", err)
	}

	// Different backend identity (session.Identity), same controller and epoch.
	accessIdentity2 := hub.Bind(controllerA, session.Identity{InstanceID: "backend-B", Epoch: 1}, EndpointAuthority{AdmissionEpoch: 1})
	if _, err := pm.Send(ctx, accessIdentity2, sessA, "hi", "now", "same-id", mintInputTarget(t, reader, accessIdentity2, sessA)); err != nil {
		t.Fatalf("send under a different session.Identity with the same id collided: %v", err)
	}
}

// ── the paste precondition (design §8.2 step 1) ─────────────────────────────

func TestTextInTheBoxRefusesThePaste(t *testing.T) {
	hub, access, sessionID := newMessagesTestAccess(t, "typing")
	reader := newFakeMsgReader("someone is typing")
	keys := happyKeys(reader, "hello")
	pm := newTestPaneMessages(t, hub, reader, keys)

	// A session.read minted before the text was typed still names a live
	// input target — minting never inspects box content, only the target
	// kind — so the refusal below comes from pasteReady's own fresh read of
	// the box's current (non-empty) text, not from this gate.
	token := mintInputTarget(t, reader, access, sessionID)
	view, err := pm.Send(context.Background(), access, sessionID, "hello", "now", "id-1", token)
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

// TestAMenuAppearingBeforeThePasteRefusesTooWhenNow: a menu is up by the
// time the paste would happen — for when=="now" this is a terminal refusal,
// exactly as "text in the box" is.
func TestAMenuAppearingBeforeThePasteRefusesTooWhenNow(t *testing.T) {
	hub, access, sessionID := newMessagesTestAccess(t, "menu-before")
	reader := newFakeMsgReader("")
	keys := happyKeys(reader, "hello")
	pm := newTestPaneMessages(t, hub, reader, keys)

	// The caller's own session.read minted this tokenId while the box was
	// still identifiable; the menu covers it only AFTER that read returns —
	// so Send's own tokenId gate is satisfied, and the refusal below comes
	// from pasteReady's readiness probe (design §8.2 step 1) reading a frame
	// whose own chrome says a menu is up, exactly as "someone is typing"
	// refuses on the box's own text.
	token := mintInputTarget(t, reader, access, sessionID)
	menuUp(t, reader)

	view, err := pm.Send(context.Background(), access, sessionID, "hello", "now", "id-1", token)
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

// TestPasteReadyOnARealIdleFrame is this bead's own acceptance test at this
// package's seam (nocx-6q1uh.18): pasteReady against a REAL captured idle
// Claude frame — replayed through happyReplayCapture
// (worker_happypath_test.go), the same corpus and replay path
// internal/agentdriver's own tests use — rather than boxFrame's synthetic
// chrome. Before the fix, pasteReady answered "is the box empty" by
// trimming strings.TrimSpace over regionText's raw join of the whole minted
// target span; that span's own closing "─" rule row is never whitespace, so
// this exact replayed frame (claude-2.1.266-idle-60 at 38s, the corpus's own
// free_text baseline at the narrow geometry) answered false on every real
// idle box. It must answer true now, through
// agentdriver.Observation.InputText.
func TestPasteReadyOnARealIdleFrame(t *testing.T) {
	hub, access, sessionID := newMessagesTestAccess(t, "real-idle")
	reader := newFakeMsgReader("")
	reader.frame = happyReplayCapture(t, "claude-2.1.266-idle-60", 38000)
	keys := &fakeMsgKeys{}
	pm := newTestPaneMessages(t, hub, reader, keys)

	if !pm.pasteReady(context.Background(), access, sessionID) {
		t.Fatal("pasteReady = false on a real idle Claude frame, want true")
	}
}

// TestPasteReadyOnARealWorkingFrame: a pane whose agent is WORKING still has
// a readable, empty input box — which is the whole reason a coordinator may
// queue a message into a turn (design §8.2 step 1 binds the paste to the box,
// never to the agent being idle), and claude.rule.json's own inputText
// extractor says so by running in "working" as well as "free_text".
//
// It is the pairing AGENTS.md's testing rule 3 asks for, against the test
// below: the readiness probe must refuse a menu and must NOT refuse work.
func TestPasteReadyOnARealWorkingFrame(t *testing.T) {
	hub, access, sessionID := newMessagesTestAccess(t, "real-working")
	reader := newFakeMsgReader("")
	frame := happyReplayCapture(t, "claude-working", 17000)
	reader.frame = frame
	state := messagesTestRules(t).Classify("claude", frame)
	reader.setClassification(state)
	keys := &fakeMsgKeys{}
	pm := newTestPaneMessages(t, hub, reader, keys)

	if !pm.pasteReady(context.Background(), access, sessionID) {
		t.Fatalf("pasteReady = false on a real working Claude frame (classification %q), want true — a message queued during a turn must still be deliverable", state)
	}
}

// TestPasteReadyOnARealMenuFrame: on a real permission menu the probe refuses
// — and refuses by READING, not by minting. Before nocx-xn63t.4.1 it decided
// this by asking the helper to mint a target and treating "nothing of that
// kind minted" as "a menu is up", which is one held token-book slot per poll
// (spec §6.2: maxLiveTokens, never evicted) for an answer the frame's own
// chrome already carries.
func TestPasteReadyOnARealMenuFrame(t *testing.T) {
	hub, access, sessionID := newMessagesTestAccess(t, "real-menu")
	reader := newFakeMsgReader("")
	keys := &fakeMsgKeys{}
	pm := newTestPaneMessages(t, hub, reader, keys)
	menuUp(t, reader)

	// The premise, stated where it can fail on its own: on this frame the
	// shipped rule reads no input box at all — the same fact that makes the
	// classifier call it a menu.
	if text, ok := messagesTestRules(t).Observe("claude", reader.frame).InputText(); ok {
		t.Fatalf("claude-permission@49s answers InputText = (%q, true), want ok=false — no box is on screen", text)
	}

	if pm.pasteReady(context.Background(), access, sessionID) {
		t.Fatal("pasteReady = true with a menu up, want false")
	}
	if got := reader.mintedKinds(); len(got) != 0 {
		t.Fatalf("the readiness probe minted %v; a probe holds no token-book capacity it does not spend (nocx-xn63t.4.1)", got)
	}
}

// ── the queued message behind a menu (nocx-xn63t.4.1) ───────────────────────

// TestAPendingMessageBehindAMenuLeavesThePanesTargetsForTheCaller is
// nocx-xn63t.4.1's own acceptance test. The owner's coordinator queued a task
// for a worker that had stopped on Claude Code's folder-trust menu, and could
// then read the pane with no target but never WITH one: every targeted
// session.read answered `capacity`, so it could not press the answer, and the
// worker stayed stuck.
//
// The cause was this delivery loop. A "free" message behind a menu re-checks
// the box every messagePollInterval, and each check MINTED a one-shot target
// it never spent; the helper's token book holds maxLiveTokens slots and
// releases them only after expiry plus five minutes (spec §6.2), so the book
// filled in seconds and the one caller who could take the menu down was the
// one being refused.
//
// The book here is three slots rather than the production 256: the property
// ("a probe that never spends holds no slot") is the same at three as at 256,
// and 256 would mean thousands of real polls. The polls are waited on as an
// observable state change — the reader's own read count — never a duration.
func TestAPendingMessageBehindAMenuLeavesThePanesTargetsForTheCaller(t *testing.T) {
	// A helper that answers an Enter as executed: the test spends the menu
	// target through the REAL paneKeys a coordinator answers menus with, so
	// "and it succeeds" means the write path, not a fake's call log.
	hub, access, chain, sessionID := newTestPaneAccess(t, "menu-starve", &fixedResultHelper{
		result: proto.IntentResult{State: "executed", BytesWritten: 1},
	})
	reader := newFakeMsgReader("")
	menuUp(t, reader)
	reader.chain = chain
	reader.accessEpoch = 1
	reader.bookCapacity = 3
	keys := happyKeys(reader, "hello")
	pm := newTestPaneMessages(t, hub, reader, keys)

	view, err := pm.Send(context.Background(), access, sessionID, "hello", "free", "id-1", "")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if view.Phase != assistant.PhaseQueued {
		t.Fatalf("immediate phase = %q, want queued (the menu is up, so delivery waits)", view.Phase)
	}
	waitForCondition(t, "the queued message to poll the pane while the menu is up", func() bool {
		return reader.readCount() >= 8
	})

	// The defect, in two assertions: the book — three slots, standing in for
	// the helper's maxLiveTokens, never evicted (spec §6.2) — is exactly what
	// those polls spent, on tokens the delivery will never spend.
	if live, refusals := reader.bookState(); live != 0 || refusals != 0 {
		t.Fatalf("waiting behind the menu left %d live token-book slots and %d capacity refusals; want 0 and 0 — the polls filled the book the caller's own answer needs", live, refusals)
	}
	if got := reader.mintedKinds(); len(got) != 0 {
		t.Fatalf("the delivery minted %v while waiting behind the menu, want nothing: a probe that never spends must not hold the book (nocx-xn63t.4.1)", got)
	}

	// And what the owner could not do: mint a MENU target and spend it. The
	// answer to the menu is the only thing that ever takes it down.
	reader.setAvailable(sessionruntime.TargetMenu)
	token := mintTarget(t, reader, access, sessionID, sessionruntime.TargetMenu)
	if token == "" {
		t.Fatal("the pane minted no menu target while a message was queued behind the menu — this is the deadlock the bead reports")
	}
	enter := assistant.KeyName("Enter")
	result, err := newPaneKeys(reader, hub).Send(context.Background(), access, assistant.KeysRequest{
		SessionID: sessionID, TokenID: token, Key: &enter,
	})
	if err != nil {
		t.Fatalf("spend the menu target: %v", err)
	}
	if result.State != "executed" {
		t.Fatalf("spend result = %+v, want executed (the coordinator's answer reached the pane)", result)
	}
}

// TestTheDeliveryProbesMintOnlyWhatTheDeliverySpends is the same bead's
// criterion 2, stated as a count: the readiness probe and every echo poll
// mint nothing, and a delivered message mints exactly the two targets it
// actually spends — one for the paste, one for the Enter — however many times
// it had to look at the pane meanwhile.
func TestTheDeliveryProbesMintOnlyWhatTheDeliverySpends(t *testing.T) {
	hub, access, sessionID := newMessagesTestAccess(t, "probe-mints")
	reader := newFakeMsgReader("")
	keys := &fakeMsgKeys{
		pasteResult: assistant.KeysResult{State: "executed", BytesWritten: 2},
		enterResult: assistant.KeysResult{State: "executed"},
	}
	// The echo arrives on the THIRD read the delivery makes, so waitForEcho's
	// probe polls with the box still empty at least twice — the polls whose
	// minting was the leak. The hook runs with the reader's lock released.
	reader.onRead = func(n int) {
		if n >= 3 {
			reader.setBox("hi")
		}
	}
	keys.onSend = func(req assistant.KeysRequest) {
		if req.Key != nil {
			reader.setBox("") // Enter clears the box: submission confirmed
		}
	}
	pm := newTestPaneMessages(t, hub, reader, keys)

	if _, err := pm.Send(context.Background(), access, sessionID, "hi", "free", "id-1", ""); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForCondition(t, "the free message to reach submitted", func() bool {
		for _, m := range pm.Pending(sessionID) {
			if m.ID == "id-1" {
				return m.Phase == assistant.PhaseSubmitted
			}
		}
		return false
	})

	// The premise: the delivery really did look at the pane more often than
	// it spent a target, or this test would pass on a delivery that never
	// polled at all.
	if reads := reader.readCount(); reads < 4 {
		t.Fatalf("the delivery read the pane %d times, want at least 4 (a readiness read, the paste, and two echo polls)", reads)
	}
	if got := reader.mintedKinds(); len(got) != 2 {
		t.Fatalf("the delivery minted %d targets (%v), want exactly 2 — the paste and the Enter", len(got), got)
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

	token := mintInputTarget(t, reader, access, sessionID)
	view, err := pm.Send(context.Background(), access, sessionID, "hello there", "now", "id-1", token)
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

// wrappedEchoCapture is the corpus's recording of a one-paragraph task
// bracketed-pasted into a real Claude Code 2.1.272 input box, left
// unsubmitted, drawn over FOUR content rows (internal/agentdriver's
// testdata/captures, recorded for nocx-xn63t.4.5). wrappedEchoIdleMs and
// wrappedEchoPastedMs are the two moments this file reads off it: the query
// box before the paste, and the box the paste filled.
const (
	wrappedEchoCapture  = "claude-2.1.272-wrapped-echo"
	wrappedEchoIdleMs   = 30000
	wrappedEchoPastedMs = 50000
)

// wrappedEchoText is what that capture's box holds, byte for byte — the
// script's own paste payload (session-message-wrapped-echo.script) and the
// text internal/agentdriver's input_text_test.go asserts the same frame's
// reading against. It is duplicated here rather than imported because the
// point of both assertions is that they compare a reading against SOMETHING
// ELSE; a shared helper that produced the text would make each of them a
// check of the reading against itself.
const wrappedEchoText = "Please read internal/app/pane_messages.go and then explain, in a short paragraph, how a queued message longer than one row of the input box is pasted, how its echo is confirmed on the frame, and how the Enter key is finally sent by the coordinator that owns the queue. Finish by naming the file and the function where that confirmation happens. Do not change any file."

// TestAQueuedMessageLongerThanOneBoxRowIsPastedEchoedAndEntered is
// nocx-xn63t.4.5's acceptance on the shape the owner hit: a when=="free"
// message that is longer than one row of the box is pasted, its echo is
// confirmed AND Enter is sent, so the record reaches the delivered phase
// (submitted) instead of stopping at partial.
//
// Every frame here is the corpus's own, replayed — not a box this test drew.
// The delivery's own steps run against them in the order a real pane would:
// the idle box the readiness probe reads (30s), the box the paste fills
// (50s), and the idle box again once Enter has cleared it. What was broken
// is exactly what this sequence measures: the rule's reading of the pasted
// frame was the first TWO of its four rows, so boxContainsEcho could never
// be true for a one-paragraph paste, deliverOne committed partial and no
// Enter was ever spent.
func TestAQueuedMessageLongerThanOneBoxRowIsPastedEchoedAndEntered(t *testing.T) {
	// The premise, checked here so no assertion below rests on a frame that
	// stopped being the shape this test is about: the recorded box reads
	// back as exactly the pasted text, and it is taller than one row.
	pasted := happyReplayCapture(t, wrappedEchoCapture, wrappedEchoPastedMs)
	obs := messagesTestRules(t).Observe("claude", pasted)
	if text, ok := obs.InputText(); !ok || text != wrappedEchoText {
		t.Fatalf("%s@%dms reads back as (%q, ok=%v), want the pasted text — this test's premise is that the recording holds it",
			wrappedEchoCapture, wrappedEchoPastedMs, text, ok)
	}
	if rows := obs.InputBox.Last - obs.InputBox.First - 1; rows < 3 {
		t.Fatalf("%s@%dms draws %d content rows, want 3 or more: the recorded box is no longer longer than one row",
			wrappedEchoCapture, wrappedEchoPastedMs, rows)
	}
	idle := happyReplayCapture(t, wrappedEchoCapture, wrappedEchoIdleMs)

	hub, access, sessionID := newMessagesTestAccess(t, "wrapped-echo")
	reader := newFakeMsgReader("")
	reader.setFrame(idle, agentdriver.StateFreeText, sessionruntime.TargetInput)
	keys := &fakeMsgKeys{
		pasteResult: assistant.KeysResult{State: "executed", BytesWritten: len(wrappedEchoText)},
		enterResult: assistant.KeysResult{State: "executed"},
	}
	// A real pane repaints on the paste and on the Enter: the box the paste
	// filled, then the idle box Enter clears. Nothing here runs on a timer.
	keys.onSend = func(req assistant.KeysRequest) {
		if req.Text != nil {
			reader.setFrame(pasted, agentdriver.StateFreeText, sessionruntime.TargetInput)
			return
		}
		if req.Key != nil {
			reader.setFrame(idle, agentdriver.StateFreeText, sessionruntime.TargetInput)
		}
	}
	pm := newTestPaneMessages(t, hub, reader, keys)

	view, err := pm.Send(context.Background(), access, sessionID, wrappedEchoText, "free", "id-wrapped", "")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if view.Phase != assistant.PhaseQueued {
		t.Fatalf("immediate phase = %q, want queued (delivery is asynchronous for \"free\")", view.Phase)
	}

	var delivered assistant.MessageView
	waitForCondition(t, "the wrapped message to reach submitted", func() bool {
		for _, m := range pm.Pending(sessionID) {
			if m.ID == "id-wrapped" {
				delivered = m
				return m.Phase == assistant.PhaseSubmitted
			}
		}
		return false
	})
	if keys.callCount() != 2 {
		t.Fatalf("PaneKeys.Send was reached %d times, want 2 — one text atom and one Enter, and no re-paste of a message already in the box", keys.callCount())
	}
	if keys.calls[0].Text == nil || *keys.calls[0].Text != wrappedEchoText {
		t.Fatalf("the step PaneKeys was handed was %+v, want the whole pasted text as a text atom", keys.calls[0])
	}
	if keys.enterCalls() != 1 {
		t.Fatalf("Enter was sent %d times, want exactly 1 — the phase above says the echo was confirmed, and a confirmed echo is what the Enter step follows (a phase that reached submitted with no Enter would be the record lying about its own delivery)", keys.enterCalls())
	}
	if delivered.BytesWritten != len(wrappedEchoText) {
		t.Errorf("record says %d bytes were written, want the %d the paste actually carried", delivered.BytesWritten, len(wrappedEchoText))
	}
}

// TestAWhenNowMessageIsDeliveredByItsOwnCallAndNotByTheQueue is
// nocx-xn63t.4.13's own unit check, and it is about WHO delivers a
// when=="now" message: design §8.1 says it "runs the delivery within the
// call", so the pane's queue — the loop a when=="free" message starts, and
// the one a spawn's own briefing keeps alive — is not a second owner of that
// delivery.
//
// The message under test is the corpus's own wrapped paragraph, which is what
// makes this a "box taller than one row" delivery: the frame the call's paste
// fills draws FOUR content rows, and the phase reaching submitted is the
// reading of all four (boxContainsEcho confirms no prefix of that text).
//
// The defect it exists for was measured on the real-helper test under load on
// 2026-09-18 and reproduced there again while this test was written: Send's
// own delivery is inside its readiness probe — one helper round trip, so a
// window wide enough to lose under load — when the queue's scan arrives at
// the message, which is still `queued` because nothing has claimed it yet.
// The queue then started a SECOND delivery of it (both goroutines' stacks
// captured in one run), claim() admitted a second `pasting` claim because only
// a cancelled or terminal record is refused, and the two deliveries wrote the
// paste, bumped the generation under each other and left the caller reading an
// in-flight phase, `partial` after a confirmed echo, or `refused` — with two
// pastes of one message in the pane.
//
// NOTHING HERE WAITS ON A DURATION. The queue's own delivery is held inside
// its paste step (fakeMsgKeys.onSend) and the when=="now" call's delivery is
// held inside its first readiness Read (fakeMsgReader.onRead), so both points
// this test reasons about are states it puts the code into rather than
// moments it hopes for; the finishing line is the queue's SECOND message
// reaching submitted, which the queue can only do by having passed the
// message under test's place in `order` — that is how this test knows the
// queue has had its opportunity without timing anything.
func TestAWhenNowMessageIsDeliveredByItsOwnCallAndNotByTheQueue(t *testing.T) {
	// The premise, checked as the neighbouring tests check theirs: the
	// recording this message is delivered into really is taller than one row.
	pasted := happyReplayCapture(t, wrappedEchoCapture, wrappedEchoPastedMs)
	idle := happyReplayCapture(t, wrappedEchoCapture, wrappedEchoIdleMs)
	box := messagesTestRules(t).Observe("claude", pasted)
	if rows := box.InputBox.Last - box.InputBox.First - 1; rows < 3 {
		t.Fatalf("%s@%dms draws %d content rows, want 3 or more: this test's premise is a box taller than one row",
			wrappedEchoCapture, wrappedEchoPastedMs, rows)
	}

	hub, access, sessionID := newMessagesTestAccess(t, "now-queue")
	reader := newFakeMsgReader("")
	reader.setFrame(idle, agentdriver.StateFreeText, sessionruntime.TargetInput)

	// The three messages this test needs, each named once: the queue's own
	// first delivery (what keeps its loop alive across the window), the
	// message under test, and the queue's second delivery — the finishing
	// line.
	const (
		queueFirstText  = "the message this pane's queue delivers first"
		queueSecondText = "the message the queue delivers after it"
	)

	queueAtPaste := make(chan struct{})
	releaseQueue := make(chan struct{})
	nowAtProbe := make(chan struct{})
	releaseNow := make(chan struct{})

	keys := &fakeMsgKeys{
		pasteResult: assistant.KeysResult{State: "executed", BytesWritten: len(wrappedEchoText)},
		enterResult: assistant.KeysResult{State: "executed"},
	}
	// A real pane repaints on each paste and on each Enter. The queue's own
	// first paste is also where this test holds the queue's delivery still —
	// before its frame changes, so the pane stays the empty box the
	// readiness probes below read.
	keys.onSend = func(req assistant.KeysRequest) {
		switch {
		case req.Text != nil && *req.Text == queueFirstText:
			close(queueAtPaste)
			<-releaseQueue
			reader.setFrame(boxFrame(queueFirstText), agentdriver.StateFreeText, sessionruntime.TargetInput)
		case req.Text != nil && *req.Text == queueSecondText:
			reader.setFrame(boxFrame(queueSecondText), agentdriver.StateFreeText, sessionruntime.TargetInput)
		case req.Text != nil:
			reader.setFrame(pasted, agentdriver.StateFreeText, sessionruntime.TargetInput)
		default:
			reader.setFrame(idle, agentdriver.StateFreeText, sessionruntime.TargetInput)
		}
	}
	pm := newTestPaneMessages(t, hub, reader, keys)

	token := mintInputTarget(t, reader, access, sessionID)

	// The queue's own money in flight: a when=="free" message, which starts
	// the delivery loop whose scan this test has to reach.
	if view, err := pm.Send(context.Background(), access, sessionID, queueFirstText, "free", "id-first", ""); err != nil {
		t.Fatalf("send the queue's first message: %v", err)
	} else if view.Phase != assistant.PhaseQueued {
		t.Fatalf("the queue's first message answered %q, want queued", view.Phase)
	}
	<-queueAtPaste

	// The queue's delivery is held in its paste step now, so the next Read to
	// arrive can only be the when=="now" call's own readiness probe. Parking
	// THAT read is what makes the assertion below a statement about the queue
	// rather than about the scheduler: the call's delivery sits exactly where
	// the defect needs it, with no step claimed yet.
	var parkOnce sync.Once
	reader.onRead = func(int) {
		parked := false
		parkOnce.Do(func() { parked = true })
		if parked {
			close(nowAtProbe)
			<-releaseNow
		}
	}

	// The message under test, delivered within its own call.
	type sendResult struct {
		view assistant.MessageView
		err  error
	}
	nowDone := make(chan sendResult, 1)
	go func() {
		view, err := pm.Send(context.Background(), access, sessionID, wrappedEchoText, "now", "id-now", token)
		nowDone <- sendResult{view: view, err: err}
	}()
	<-nowAtProbe

	// The finishing line, queued while the when=="now" call's delivery is
	// exactly where the defect needs it: the queue reaches this message only
	// after passing the message under test's place in `order`.
	if view, err := pm.Send(context.Background(), access, sessionID, queueSecondText, "free", "id-second", ""); err != nil {
		t.Fatalf("send the queue's second message: %v", err)
	} else if view.Phase != assistant.PhaseQueued {
		t.Fatalf("the queue's second message answered %q, want queued", view.Phase)
	}

	// Let the queue go on: it finishes its own first message and scans, which
	// is where it used to pick the when=="now" message up as well.
	close(releaseQueue)
	waitForCondition(t, "the queue to deliver its second message", func() bool {
		for _, m := range pm.Pending(sessionID) {
			if m.ID == "id-second" {
				return m.Phase == assistant.PhaseSubmitted
			}
		}
		return false
	})

	// THE ASSERTION (nocx-xn63t.4.13). The queue has delivered both of its own
	// messages, so it has walked past the when=="now" message's place in the
	// queue — and that message's own call has not pasted it yet, because its
	// delivery is parked in its readiness probe. The pane must therefore have
	// received NOTHING for it. A paste here is the queue delivering a message
	// it does not own, and on the old code it was followed by the call's own
	// paste: one message, written into the pane twice.
	if got := keys.textCalls(wrappedEchoText); got != 0 {
		t.Fatalf("the pane received %d paste(s) of the when=now message while its own call was still in its readiness probe, want 0 — the delivery of a when=now message belongs to the call that enqueued it, and a second one pastes the same message twice", got)
	}

	// Now let the call finish its own delivery, and assert it is the one that
	// wrote the message — once, through the box taller than one row, with the
	// phase the caller is answered.
	close(releaseNow)
	waitForCondition(t, "the when=now message to reach submitted", func() bool {
		for _, m := range pm.Pending(sessionID) {
			if m.ID == "id-now" {
				return m.Phase == assistant.PhaseSubmitted
			}
		}
		return false
	})
	res := <-nowDone
	if res.err != nil {
		t.Fatalf("the when=now send: %v", res.err)
	}
	if res.view.Phase != assistant.PhaseSubmitted {
		t.Fatalf("the when=now call answered %q, want %q — the phase its own delivery reached", res.view.Phase, assistant.PhaseSubmitted)
	}
	if got := keys.textCalls(wrappedEchoText); got != 1 {
		t.Fatalf("the when=now message was pasted %d time(s), want exactly 1 — its own call, once", got)
	}
	if keys.enterCalls() != 3 {
		t.Fatalf("Enter was sent %d times, want 3 — one per delivered message, and no second Enter for a message that was pasted twice", keys.enterCalls())
	}
}

// TestAMessageTheBoxDoesNotShowIsNotEntered is the pair that keeps the fix
// honest, and it is the same two recorded frames: the box holds the
// recording's own paragraph while the message the caller queued is a
// different sentence — someone else's typing, or this delivery's own text
// never having landed. No echo is confirmed, Enter is never sent, and the
// record stays partial rather than claiming a delivery that did not happen.
//
// The boxContents the record carries is asserted too, and it is the whole
// reason this pair is worth writing on a real frame: it is the FOUR-row
// reading. A comparison that had been loosened instead of the reading fixed
// would confirm the wrong text here, and a reading that still stopped at two
// rows would report a different box than the one on screen.
func TestAMessageTheBoxDoesNotShowIsNotEntered(t *testing.T) {
	const queued = "Run the whole suite and report which tests fail."
	pasted := happyReplayCapture(t, wrappedEchoCapture, wrappedEchoPastedMs)
	idle := happyReplayCapture(t, wrappedEchoCapture, wrappedEchoIdleMs)

	hub, access, sessionID := newMessagesTestAccess(t, "wrapped-no-echo")
	reader := newFakeMsgReader("")
	reader.setFrame(idle, agentdriver.StateFreeText, sessionruntime.TargetInput)
	keys := &fakeMsgKeys{
		pasteResult: assistant.KeysResult{State: "executed", BytesWritten: len(queued)},
		enterResult: assistant.KeysResult{State: "executed"},
	}
	keys.onSend = func(req assistant.KeysRequest) {
		if req.Text != nil {
			// The paste landed, and what the box shows is not what was
			// queued — a real screen's own text, replayed.
			reader.setFrame(pasted, agentdriver.StateFreeText, sessionruntime.TargetInput)
		}
	}
	pm := newTestPaneMessages(t, hub, reader, keys)
	// The echo is confirmed off a frame the pane already drew, so this test
	// settles "not this text" in milliseconds rather than sleeping out the
	// production bound (defaultEchoWait's own doc: the field exists so this
	// fact does not cost a production-sized wait).
	pm.echoWait = 5 * time.Millisecond

	view, err := pm.Send(context.Background(), access, sessionID, queued, "free", "id-other", "")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if view.Phase != assistant.PhaseQueued {
		t.Fatalf("immediate phase = %q, want queued", view.Phase)
	}

	var undelivered assistant.MessageView
	waitForCondition(t, "the message to settle as partial", func() bool {
		for _, m := range pm.Pending(sessionID) {
			if m.ID == "id-other" {
				undelivered = m
				return m.Phase == assistant.PhasePartial
			}
		}
		return false
	})
	if keys.enterCalls() != 0 {
		t.Fatalf("Enter was sent %d times, want 0 — the box never showed this message", keys.enterCalls())
	}
	if undelivered.BoxContents != wrappedEchoText {
		t.Errorf("the partial record reports boxContents %q,\nwant the box's whole reading %q", undelivered.BoxContents, wrappedEchoText)
	}
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

	token := mintInputTarget(t, reader, access, sessionID)
	view, err := pm.Send(context.Background(), access, sessionID, "hello", "now", "id-1", token)
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

	// The pane draws the menu AFTER the read that confirmed the echo — the
	// race design §8.2 names, timed by the pane rather than by a count of the
	// targets this delivery happened to mint. It used to be keyed on
	// flipAfterMints, which is a number the probes moved the moment they
	// stopped minting (nocx-xn63t.4.1): a test that has to be retimed by a fix
	// is a test measuring the wrong event, and this one has to hold with the
	// probes minting and without them.
	//
	// Read order is the same either way: the caller's own session.read (#1 —
	// a "now" send needs its tokenId), the readiness read (#2), the paste
	// read (#3), the echo read (#4). The menu appears once the echo has been
	// confirmed and before the Enter is minted, so the delivery must reach
	// partial with no Enter — under the old probes and under the new ones.
	reader.onRead = func(n int) {
		if n == 4 {
			menuUp(t, reader)
		}
	}
	token := mintInputTarget(t, reader, access, sessionID)

	view, err := pm.Send(context.Background(), access, sessionID, "hello", "now", "id-1", token)
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
	// The box already holds someone else's typing: design §8.2 step 1 refuses
	// the paste ("someone is typing"), and for when=="free" that refusal is a
	// WAIT rather than an ending (deliverOne), so the record is still
	// `queued` — unclaimed — whenever the cancel arrives.
	//
	// This fixture said the same thing with reader.setAvailable("") until
	// nocx-xn63t.4.15, and that lever had stopped reaching the product the
	// moment pasteReady stopped minting and started reading the frame
	// (nocx-xn63t.4.1). Availability is what a MINT answers; readiness is the
	// box's own text, and boxFrame("") is an ordinary readable empty Claude
	// box — so pasteReady answered TRUE, the delivery claimed its paste,
	// pasteStep's own mint came back a whole-screen region target (the kind
	// chooseTargetRows falls back to when the frame offers no input rows) and
	// deliverOne committed `refused`, terminally, before the cancel below was
	// even built. Cancel then answered {too_late, refused}: the phase of a
	// paste that never should have been claimed.
	reader := newFakeMsgReader("someone is typing")
	keys := happyKeys(reader, "hi")
	pm := newTestPaneMessages(t, hub, reader, keys)

	// The premise, checked before anything is asserted of the product: this
	// fixture's frame really is one a "free" delivery waits on. A fixture
	// that stops describing that reds here, rather than letting the
	// assertions below pass for the wrong reason.
	if pm.pasteReady(context.Background(), access, sessionID) {
		t.Fatal(`pasteReady = true on a box that already holds text: this fixture no longer describes a pane a "free" delivery waits for`)
	}

	// "free" so Send returns before delivery is attempted.
	if _, err := pm.Send(context.Background(), access, sessionID, "hi", "free", "id-1", ""); err != nil {
		t.Fatalf("send: %v", err)
	}

	// The delivery really ran, and really waited rather than claimed: an
	// observable state change — the reader's own read count — never a
	// duration. Without this the cancel below would pass against a delivery
	// that had not started at all.
	waitForCondition(t, "the queued delivery to re-check the pane", func() bool {
		return reader.readCount() >= 4
	})
	phase := assistant.MessagePhase("")
	for _, m := range pm.Pending(sessionID) {
		if m.ID == "id-1" {
			phase = m.Phase
		}
	}
	if phase != assistant.PhaseQueued {
		t.Fatalf("phase = %q while the box holds someone else's typing, want queued (design §8.1: a when=free message waits for the pane)", phase)
	}

	result, err := pm.Cancel(context.Background(), access, sessionID, "id-1")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if result.Result != "cancelled" || result.Phase != assistant.PhaseCancelled {
		t.Fatalf("cancel result = %+v, want {cancelled, cancelled}", result)
	}
	// Nothing was written, and that is the same fact the answer above reports
	// (design §8.6: "a cancel never reports cancelled for a message whose
	// paste can still be written") — stated where it can be seen, on the
	// write path itself.
	if keys.callCount() != 0 {
		t.Fatalf("PaneKeys.Send was reached %d times for a message cancelled while queued, want 0", keys.callCount())
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

	token := mintInputTarget(t, reader, access, sessionID)
	view, err := pmUnderTest.Send(context.Background(), access, sessionID, "hello", "now", "id-1", token)
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
			if _, closeErr := registrar.Close(ctx, controller, w.Participant.ID); closeErr != nil {
				t.Errorf("close (revoke) mid-delivery: %v", closeErr)
			}
		}
	}
	pm := newTestPaneMessages(t, hub, reader, keys)

	token := mintInputTarget(t, reader, access, sessionID)
	view, err := pm.Send(ctx, access, sessionID, "hello", "now", "id-1", token)
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

	token := mintInputTarget(t, reader, access, sessionID)
	view, err := pm.Send(ctx, access, sessionID, "hello", "now", "id-1", token)
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
	token := mintInputTarget(t, reader, access, sessionID)
	if _, err := pm1.Send(context.Background(), access, sessionID, "hi", "now", "id-1", token); err != nil {
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
			_, _ = registrar.Close(ctx, controller, w.Participant.ID)
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
