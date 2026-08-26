package transport

// The run writes PROSE BLOCKS, and the backend owns the boundary between them
// (ADR-0040, amending ADR-0039).
//
// A turn used to be one entry whose answer was ONE artifact: every delta of
// the whole run appended to it, and the things the turn caused were joined to
// it with an offset saying how much of the text had been written when they
// happened. That offset existed for exactly one reason — the unit that was
// DRAWN (a run of prose between two calls) and the unit that was STORED (the
// whole answer) were different things, so something had to translate. Three
// arrangements were built on that translation and all three drew a turn in an
// order that was not the order it happened in.
//
// These tests assert that the two units are now the same thing: the run opens
// a `text` child on the first delta after a call, appends to it, and seals it
// when the next call arrives. The boundary is a ROW, decided by the backend
// while it holds the stream, and the assertions below are on the STORED ROWS
// — not on the notifications, because a notification is what the renderer
// happens to have been told and the rows are what the restore will read.
//
// Every refusal here is paired with the ordinary run that succeeds (AGENTS.md
// testing rule 3).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

// ── reading the rows ─────────────────────────────────────────────────────

// proseRow is one run of prose as the STORE holds it: the `text` child, its
// seat, and the text its own body carries.
type proseRow struct {
	entryID string
	pos     int
	text    string
	state   content.ArtifactState
}

// proseUnder reads a turn's runs of prose off the ledger, in seat order.
//
// It goes CHILDREN → BODY, which is the shape the restore will read: the
// children in `pos` order (Caused), and each `text` child's own artifact —
// which hangs on the block and on no execution, because prose was printed and
// not attempted. Non-text children are skipped rather than reported, so the
// helper answers exactly the question its name asks and a turn that also ran
// a command does not have to be filtered at every call site.
func proseUnder(t *testing.T, led content.LedgerRepository, turnID string) []proseRow {
	t.Helper()
	ctx := context.Background()
	kids, err := led.Caused(ctx, turnID)
	if err != nil {
		t.Fatalf("Caused(%s): %v", turnID, err)
	}
	out := make([]proseRow, 0, len(kids))
	for _, k := range kids {
		if k.Kind != content.EntryText {
			continue
		}
		row := proseRow{entryID: k.EntryID, pos: k.Position}
		entry, entryErr := led.Entry(ctx, k.EntryID)
		if entryErr != nil || entry == nil {
			t.Fatalf("Entry(%s): %v (nil=%v)", k.EntryID, entryErr, entry == nil)
		}
		if len(entry.Artifacts) != 1 {
			t.Fatalf("the prose block %s carries %d bodies, want exactly one",
				k.EntryID, len(entry.Artifacts))
		}
		row.state = entry.Artifacts[0].State
		body, bodyErr := led.Artifact(ctx, entry.Artifacts[0].ID)
		if bodyErr != nil || body == nil {
			t.Fatalf("Artifact(%s): %v (nil=%v)", entry.Artifacts[0].ID, bodyErr, body == nil)
		}
		var sb strings.Builder
		for _, c := range body.Chunks {
			sb.Write(c)
		}
		row.text = sb.String()
		out = append(out, row)
	}
	return out
}

// childKinds is the turn's whole child list as kinds, in seat order — what a
// reader would draw top to bottom. The assertions about WHERE a prose block
// sits are made on this, because "two blocks either side of the call" is a
// claim about the sequence and not about the prose alone.
func childKinds(t *testing.T, led content.LedgerRepository, turnID string) []content.EntryKind {
	t.Helper()
	kids, err := led.Caused(context.Background(), turnID)
	if err != nil {
		t.Fatalf("Caused(%s): %v", turnID, err)
	}
	out := make([]content.EntryKind, 0, len(kids))
	for _, k := range kids {
		out = append(out, k.Kind)
	}
	return out
}

// proseBodyOf is a turn's whole answer as the store holds it now: its `text`
// children in seat order, concatenated. It is what the one answer artifact
// used to hold, and it exists so that the assertions which were always ABOUT
// THE TEXT — "the durable answer is exactly what streamed" — keep asking that
// question rather than becoming assertions about how many rows it took.
func proseBodyOf(t *testing.T, led content.LedgerRepository, turnID string) string {
	t.Helper()
	var sb strings.Builder
	for _, p := range proseUnder(t, led, turnID) {
		sb.WriteString(p.text)
	}
	return sb.String()
}

// assertProseSealed is the terminal-close invariant: when the run is terminal,
// no block it streamed into still says open. It takes the whole set rather
// than the last block, because a run that failed between opening a block and
// sealing it is exactly the case the check is for.
func assertProseSealed(t *testing.T, led content.LedgerRepository, turnID string) {
	t.Helper()
	for _, p := range proseUnder(t, led, turnID) {
		if p.state != content.ArtifactSealed {
			t.Errorf("the prose block at seat %d is %q after the run terminalized, want sealed",
				p.pos, p.state)
		}
	}
}

// ── the scripted engine, with a call in the middle of the answer ─────────

// toolCallScript is a scripted assistant.Client that plays one ordered
// sequence of ANSWER and TOOL CALL events — the one thing the other scripted
// clients in this package cannot do, and the whole shape under test: prose,
// then a call, then more prose.
//
// It emits through the SAME callback the real engine emits through, so what is
// being tested is the transport's handling of the ordered stream and not a
// second path built for the test.
type toolCallScript struct {
	events []assistant.AskEvent
	// err is returned after the events are played, so one script can be
	// "answer this much, then suspend".
	err func(runID string) error
}

func (s *toolCallScript) Probe(_ context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
	return assistant.ProbeResult{OK: true, Model: p.Model}, nil
}

func (s *toolCallScript) Discard(string) {}

func (s *toolCallScript) Ask(_ context.Context, p assistant.AskParams, onEvent func(assistant.AskEvent) error) error {
	for _, ev := range s.events {
		if err := onEvent(ev); err != nil {
			return err
		}
	}
	if s.err != nil {
		return s.err(p.RunID)
	}
	return nil
}

// answerEvent and callEvent name the two halves of the script so a test reads
// as the sequence a person would see.
func answerEvent(text string) assistant.AskEvent {
	return assistant.AskEvent{Kind: assistant.AskAnswer, Text: text}
}

func callEvent(callID, tool, actionEntry string) assistant.AskEvent {
	return assistant.AskEvent{Kind: assistant.AskToolCall, Call: &assistant.ToolCall{
		Tool: tool, CallID: callID, EntryID: actionEntry, Effect: content.EffectObserve,
	}}
}

// askAndWaitForState drives one ask over the real socket and waits for the
// run to terminalize, so the assertions below read a store nothing is still
// writing to.
func askAndWaitForState(t *testing.T, h *askHarness, sid, question string) string {
	t.Helper()
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": question, "cwd": "/repo",
	}, 1)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	readNotification(t, h.conn, "agent.runState", 5*time.Second)
	return res.EntryID
}

// ── acceptance 1 + 3: a call cuts the prose in two ───────────────────────

// TestAgentAsk_ProseEitherSideOfACallIsTwoBlocks: the run says something,
// calls a tool, says something else — and the store holds TWO runs of prose,
// seated either side of the call's own block.
//
// Asserted on the ROWS and not on the notifications, which is criterion 3's
// whole point: the wire told the renderer a block id per delta, but what makes
// the live view and the restore agree is that the rows say the same thing.
// A test that read the notifications back would be asserting that the server
// said what it said.
func TestAgentAsk_ProseEitherSideOfACallIsTwoBlocks(t *testing.T) {
	h := newAskHarness(t, &toolCallScript{events: []assistant.AskEvent{
		answerEvent("let me "), answerEvent("look:"),
		callEvent("call_1", "files.read", ""),
		answerEvent(" it says"), answerEvent(" hello"),
	}})
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	turn := askAndWaitForState(t, h, sid, "what does it say?")

	prose := proseUnder(t, h.db.Ledger(), turn)
	if len(prose) != 2 {
		t.Fatalf("the run left %d prose blocks, want exactly two — one either side of the call: %+v",
			len(prose), prose)
	}
	// The text did not merge and did not shuffle: what was said BEFORE the
	// call is in the first block, what was said after it in the second.
	if prose[0].text != "let me look:" {
		t.Errorf("the first block reads %q, want what was said before the call", prose[0].text)
	}
	if prose[1].text != " it says hello" {
		t.Errorf("the second block reads %q, want what was said after the call", prose[1].text)
	}
	// And the seats really are either side, which is the fact a reader draws.
	// The action entry is not in this list — this scripted call carries no
	// ledger entry, so the two prose seats are 0 and 1 and the assertion is
	// that they are DIFFERENT seats in the right order.
	if prose[0].pos >= prose[1].pos {
		t.Fatalf("the two blocks are seated %d and %d — the second must come after the first",
			prose[0].pos, prose[1].pos)
	}
	// The first block is SEALED: the call ended it, and nothing may append to
	// it again. The last one is sealed too, by the run's terminal close.
	for i, p := range prose {
		if p.state != content.ArtifactSealed {
			t.Errorf("prose block %d is %q after the run terminalized, want sealed", i, p.state)
		}
	}
}

// TestAgentAsk_ProseEitherSideOfACallWithNoIdIsTwoBlocks: the same scenario
// with an EMPTY call id. The call id is the model's own name for a call —
// optional on the wire, absent from some providers — and nothing about the
// boundary may key on it: a call that cannot name itself is still a call,
// and the prose on either side of it is still two runs. Without this, the
// seam is only proven for the case where the id happens to be present, and
// a server that sends a boundary whose call carries no id merges the two
// blocks again — exactly the mask the renderer fix removed, re-entering
// through the id's optionality.
func TestAgentAsk_ProseEitherSideOfACallWithNoIdIsTwoBlocks(t *testing.T) {
	h := newAskHarness(t, &toolCallScript{events: []assistant.AskEvent{
		answerEvent("let me "), answerEvent("look:"),
		callEvent("", "files.read", ""),
		answerEvent(" it says"), answerEvent(" hello"),
	}})
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	turn := askAndWaitForState(t, h, sid, "what does it say?")

	prose := proseUnder(t, h.db.Ledger(), turn)
	if len(prose) != 2 {
		t.Fatalf("a call with no id left %d prose blocks, want exactly two — one either side of the call: %+v",
			len(prose), prose)
	}
	if prose[0].text != "let me look:" {
		t.Errorf("the first block reads %q, want what was said before the call", prose[0].text)
	}
	if prose[1].text != " it says hello" {
		t.Errorf("the second block reads %q, want what was said after the call", prose[1].text)
	}
	if prose[0].pos >= prose[1].pos {
		t.Fatalf("the two blocks are seated %d and %d — the second must come after the first",
			prose[0].pos, prose[1].pos)
	}
	// Two blocks means two ids, and the wire must be able to tell them
	// apart: this is the fact the renderer keys on.
	if prose[0].entryID == prose[1].entryID {
		t.Fatalf("both blocks carry entry id %q — the wire cannot tell the two runs of prose apart",
			prose[0].entryID)
	}
	for i, p := range prose {
		if p.state != content.ArtifactSealed {
			t.Errorf("prose block %d is %q after the run terminalized, want sealed", i, p.state)
		}
	}
}

// The paired negative for the same mechanism: with NO call in the middle, the
// same amount of text is ONE block. Without this, "two blocks" above could be
// two blocks for any reason at all — one per delta, say — and the test would
// not notice.
func TestAgentAsk_ProseWithNoCallIsOneBlock(t *testing.T) {
	h := newAskHarness(t, &toolCallScript{events: []assistant.AskEvent{
		answerEvent("let me "), answerEvent("look:"),
		answerEvent(" it says"), answerEvent(" hello"),
	}})
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	turn := askAndWaitForState(t, h, sid, "what does it say?")

	prose := proseUnder(t, h.db.Ledger(), turn)
	if len(prose) != 1 {
		t.Fatalf("four deltas with no call left %d prose blocks, want one: %+v", len(prose), prose)
	}
	if prose[0].text != "let me look: it says hello" {
		t.Errorf("the block reads %q, want the whole answer", prose[0].text)
	}
}

// ── acceptance 2: a call before any prose opens nothing ──────────────────

// A run that reaches for a tool before it has said a word leaves NO empty
// prose block. An empty `text` child would draw as a paragraph that was never
// written, and the ordinary turn — the one that looks something up before
// answering — is exactly this shape, so the defect would be the common case.
func TestAgentAsk_ACallBeforeAnyProseOpensNoBlock(t *testing.T) {
	h := newAskHarness(t, &toolCallScript{events: []assistant.AskEvent{
		callEvent("call_1", "files.read", ""),
		answerEvent("it says hello"),
	}})
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	turn := askAndWaitForState(t, h, sid, "what does it say?")

	prose := proseUnder(t, h.db.Ledger(), turn)
	if len(prose) != 1 {
		t.Fatalf("a call before any prose left %d blocks, want exactly the one the answer opened after it: %+v",
			len(prose), prose)
	}
	if prose[0].text != "it says hello" {
		t.Errorf("the block reads %q, want the answer written after the call", prose[0].text)
	}
	// The stronger form of the same claim: NOTHING under this turn is an
	// empty run of prose, wherever it sits.
	for _, p := range prose {
		if p.text == "" {
			t.Fatalf("an empty prose block was opened at seat %d — nothing was printed there", p.pos)
		}
	}
}

// And the run that ends on a call leaves no trailing empty block either: the
// seal happens at the call, and the terminal close opens nothing.
func TestAgentAsk_ACallAfterTheLastProseOpensNoTrailingBlock(t *testing.T) {
	h := newAskHarness(t, &toolCallScript{events: []assistant.AskEvent{
		answerEvent("looking"),
		callEvent("call_1", "files.read", ""),
	}})
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	turn := askAndWaitForState(t, h, sid, "what does it say?")

	prose := proseUnder(t, h.db.Ledger(), turn)
	if len(prose) != 1 || prose[0].text != "looking" {
		t.Fatalf("a run that ended on a call left %+v, want the one block it wrote before it", prose)
	}
	if prose[0].state != content.ArtifactSealed {
		t.Errorf("the block the call ended is %q, want sealed", prose[0].state)
	}
}

// ── acceptance 5: the block id rides the real socket ─────────────────────

// TestAgentRunDelta_CarriesItsBlockOverTheWire: the block id off the REAL
// socket, and it CHANGES at the call — which is the fact the field exists to
// carry. A test that only checked the field was present would pass on a
// server that sent the turn id in it.
//
// The schema conformance of the same notification is asserted beside this in
// ws_contract_test.go (…_OverTheWireConformToContract); this is the product
// claim the schema cannot make: that the id names the piece.
func TestAgentRunDelta_CarriesItsBlockOverTheWire(t *testing.T) {
	h := newAskHarness(t, &toolCallScript{events: []assistant.AskEvent{
		answerEvent("before"),
		callEvent("call_1", "files.read", ""),
		answerEvent("after"),
	}})
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "q", "cwd": "/repo",
	}, 1)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}

	type deltaWire struct {
		EntryID string `json:"entryId"`
		BlockID string `json:"blockId"`
		Text    string `json:"text"`
	}
	var got []deltaWire
	for range 2 {
		raw := readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
		var d deltaWire
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("runDelta unmarshal: %v\nraw: %s", err, raw)
		}
		got = append(got, d)
	}
	readNotification(t, h.conn, "agent.runState", 5*time.Second)

	if got[0].BlockID == "" || got[1].BlockID == "" {
		t.Fatalf("a delta arrived with no block: %+v", got)
	}
	if got[0].BlockID == got[1].BlockID {
		t.Fatalf("both deltas named block %q — the call between them ended the first piece",
			got[0].BlockID)
	}
	// The block is NOT the turn: entryId still routes and blockId places, and
	// a server that sent one in both fields would pass every schema check.
	if got[0].BlockID == res.EntryID || got[1].BlockID == res.EntryID {
		t.Fatalf("a delta's blockId is the turn %q — the block is the piece, not the answer", res.EntryID)
	}
	if got[0].EntryID != res.EntryID || got[1].EntryID != res.EntryID {
		t.Fatalf("the deltas routed to %q/%q, want the turn %q", got[0].EntryID, got[1].EntryID, res.EntryID)
	}
	// And the ids the wire named are the rows the store holds, in that order:
	// this is what makes the live view and the restore one list.
	prose := proseUnder(t, h.db.Ledger(), res.EntryID)
	if len(prose) != 2 || prose[0].entryID != got[0].BlockID || prose[1].entryID != got[1].BlockID {
		t.Fatalf("the wire named %q then %q; the store holds %+v", got[0].BlockID, got[1].BlockID, prose)
	}
}

// runOf is a turn's agent run — the execution the store recorded with the ask.
// The tests below open prose by hand, and a run of prose belongs to the run
// that printed it, so they have to name one.
func runOf(t *testing.T, led content.LedgerRepository, turnID string) int64 {
	t.Helper()
	entry, err := led.Entry(context.Background(), turnID)
	if err != nil || entry == nil {
		t.Fatalf("Entry(%s): %v (nil=%v)", turnID, err, entry == nil)
	}
	for _, ex := range entry.Executions {
		if ex.State != nil {
			return ex.ID
		}
	}
	t.Fatalf("the turn %s carries no agent run: %+v", turnID, entry.Executions)
	return 0
}

// ── acceptance 7: the store refuses what it must, and succeeds otherwise ──

// OpenProse under a parent that is not there is refused with the answer this
// repository already gives to that question, and the refusal writes nothing.
// Paired, on the same store, with the ordinary open that lands — a store that
// refused everything would satisfy the first half and be an outage.
func TestOpenProse_RefusesAParentThatIsNotThereAndOpensOneThatIs(t *testing.T) {
	h := newAskHarness(t, &toolCallScript{events: []assistant.AskEvent{answerEvent("x")}})
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	turn := askAndWaitForState(t, h, sid, "q")
	led := h.db.Ledger()
	ctx := context.Background()

	if _, err := led.OpenProse(ctx, "no-such-turn", 1); err == nil {
		t.Fatal("prose opened under a turn that does not exist")
	}
	// And on an ordinary turn it succeeds — at the seat after the one the run
	// already wrote, because the seat is the store's and not a number a
	// caller hands in.
	before := proseUnder(t, led, turn)
	if len(before) != 1 {
		t.Fatalf("the run left %+v, want the one block it wrote", before)
	}
	opened, err := led.OpenProse(ctx, turn, runOf(t, led, turn))
	if err != nil {
		t.Fatalf("OpenProse on an ordinary turn: %v", err)
	}
	after := proseUnder(t, led, turn)
	if len(after) != 2 || after[1].entryID != opened.EntryID {
		t.Fatalf("after the second open the turn holds %+v, want the new block seated last", after)
	}
	if after[1].pos <= after[0].pos {
		t.Fatalf("the second block took seat %d, which is not after %d", after[1].pos, after[0].pos)
	}
}

// SealProse names a block that carries no body: not an error, because the
// fact being recorded is about the block and is true either way. Paired with
// the seal that does reach a body.
func TestSealProse_IsANoOpOnABodilessBlockAndSealsARealOne(t *testing.T) {
	h := newAskHarness(t, &toolCallScript{events: []assistant.AskEvent{answerEvent("x")}})
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	turn := askAndWaitForState(t, h, sid, "q")
	led := h.db.Ledger()
	ctx := context.Background()

	if err := led.SealProse(ctx, "no-such-block"); err != nil {
		t.Fatalf("sealing a block that carries nothing: %v, want no error", err)
	}
	// The paired success: a block that DOES carry a body seals, and the state
	// on disk says so.
	opened, err := led.OpenProse(ctx, turn, runOf(t, led, turn))
	if err != nil {
		t.Fatalf("OpenProse: %v", err)
	}
	if err = led.AppendChunk(ctx, opened.ArtifactID, 1, []byte("words")); err != nil {
		t.Fatalf("AppendChunk: %v", err)
	}
	body, err := led.Artifact(ctx, opened.ArtifactID)
	if err != nil || body == nil {
		t.Fatalf("Artifact: %v (nil=%v)", err, body == nil)
	}
	if body.State != content.ArtifactOpen {
		t.Fatalf("a freshly opened block is %q, want open — the seal below would prove nothing", body.State)
	}
	if err = led.SealProse(ctx, opened.EntryID); err != nil {
		t.Fatalf("SealProse: %v", err)
	}
	if body, err = led.Artifact(ctx, opened.ArtifactID); err != nil || body == nil {
		t.Fatalf("Artifact after the seal: %v (nil=%v)", err, body == nil)
	}
	if body.State != content.ArtifactSealed {
		t.Fatalf("the block is %q after SealProse, want sealed", body.State)
	}
}

// A prose block carries NO pane anchor, and that is what keeps it out of the
// pane-scoped page. The restore reads one pane's blocks; a paragraph of an
// answer is drawn inside its turn and never at the top level, so an anchor
// would put every piece of every answer into the scrollback as a block of its
// own — and into the model's own blocks.list beside them.
func TestProseBlocksAreNotAnchoredToThePane(t *testing.T) {
	h := newAskHarness(t, &toolCallScript{events: []assistant.AskEvent{answerEvent("hello")}})
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	turn := askAndWaitForState(t, h, sid, "q")
	led := h.db.Ledger()
	ctx := context.Background()

	prose := proseUnder(t, led, turn)
	if len(prose) != 1 {
		t.Fatalf("the run left %+v, want one block", prose)
	}
	row, err := led.Entry(ctx, prose[0].entryID)
	if err != nil || row == nil {
		t.Fatalf("Entry(prose): %v (nil=%v)", err, row == nil)
	}
	if row.PaneID != nil {
		t.Fatalf("the prose block is anchored to pane %q — it is drawn inside its turn, never at the top level", *row.PaneID)
	}
	if row.ParentID == nil || *row.ParentID != turn {
		t.Fatalf("the prose block's parent = %v, want the turn %q", row.ParentID, turn)
	}
	// The paired positive, so "no anchor" is not passing because nothing was
	// anchored at all: the TURN carries one, which is what puts the answer in
	// this pane's restore.
	parent, err := led.Entry(ctx, turn)
	if err != nil || parent == nil {
		t.Fatalf("Entry(turn): %v (nil=%v)", err, parent == nil)
	}
	if parent.PaneID == nil && parent.SessionID == nil {
		t.Fatal("the turn carries neither pane nor session — the fixture anchors nothing, so the assertion above proves nothing")
	}
}

// ── acceptance 4: a resume continues the block it was suspended in ───────

// TestAgentApprove_TheResumeContinuesTheOpenProseBlock: a run says something,
// the EGRESS gate suspends it, a person approves, and the prose that follows
// lands in the block the question interrupted — not in a second one opened
// beside it.
//
// This is the delta numbering's rule one level in. Since nocx-igu4y the resume
// is a real checkpoint resume: the model is not asked to produce the answer
// again, so the text after the approval is the CONTINUATION of the text before
// it. Numbering it from 0 again would collide; opening a second `text` block
// for it would cut one sentence in half at a place nothing happened — which is
// the defect ADR-0040 exists to remove, arriving by the one door the ADR does
// not close.
//
// The suspension is scripted rather than driven through a real tool, and that
// is faithful to what is under test: the middleware's own egress arm withholds
// the tool RESULT and retains it for the approved resume (internal/assistant,
// runWithRetained) — it never touches the answer stream. What the transport
// must get right is that the run context it re-drives still names the block
// the interrupted stream had open, and a scripted suspension exercises exactly
// that seam with nothing else in the way.
func TestAgentApprove_TheResumeContinuesTheOpenProseBlock(t *testing.T) {
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{
			deltas: []string{"let me", " look"},
			suspend: func(runID string) error {
				return &assistant.EgressRequestedError{Request: &assistant.EgressRequest{
					RunID: runID, Attempt: 1, Tool: "files.read", CallID: "call_1",
					Arguments: `{"path":"/repo/a.txt"}`, ArgHash: "hash-a",
					Effect:   content.EffectObserve,
					Resource: &content.GrantScope{Kind: content.ResourcePath, ID: "/repo/a.txt"},
					Findings: []assistant.EgressFinding{{
						Source: assistant.EgressFindingHeuristic, Kind: "openai-api-key",
						Start: 11, End: 43,
					}},
				}}
			},
		},
		{deltas: []string{" — it says", " hello"}},
	}}
	h := suspendedRunWith(t, askPolicyStore(t), client)

	// Before the answer: one block, holding what was said before the gate
	// stopped the run. If this were already two, the assertion after the
	// resume could not tell a continued block from a lucky count.
	led := h.db.Ledger()
	turn := turnOfRun(t, led, h.runID)
	before := proseUnder(t, led, turn)
	if len(before) != 1 || before[0].text != "let me look" {
		t.Fatalf("at the suspension the turn holds %+v, want the one block it had written", before)
	}

	// "once", and it has to be: an egress decision covers this result only —
	// secret-shaped material going to the provider is never a standing answer
	// (design §7.3), and agent.approve refuses the wider scope.
	h.approve(t, "once")
	waitFor(t, "the resume to drive the engine", 5*time.Second, func() bool { return client.askCount() == 2 })
	readNotification(t, h.conn, "agent.runState", 5*time.Second)

	after := proseUnder(t, led, turn)
	if len(after) != 1 {
		t.Fatalf("the resume left %d prose blocks, want the ONE it was suspended in: %+v", len(after), after)
	}
	if after[0].entryID != before[0].entryID {
		t.Fatalf("the resume wrote into block %q; the question interrupted %q",
			after[0].entryID, before[0].entryID)
	}
	if after[0].text != "let me look — it says hello" {
		t.Errorf("the continued block reads %q, want the whole answer in one piece", after[0].text)
	}
	if after[0].state != content.ArtifactSealed {
		t.Errorf("the block is %q after the run terminalized, want sealed", after[0].state)
	}
}

// turnOfRun answers "which entry is this run an attempt of" off the store.
// The scope harness hands back the run id; the prose hangs off the TURN, and
// executions.entry_id is the one mapping between them — the same one
// internal/assistant's middleware documents rather than re-derives.
func turnOfRun(t *testing.T, led content.LedgerRepository, runID int64) string {
	t.Helper()
	page, err := led.ListEntries(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	for _, row := range page {
		entry, entryErr := led.Entry(context.Background(), row.ID)
		if entryErr != nil || entry == nil {
			continue
		}
		for _, ex := range entry.Executions {
			if ex.ID == runID {
				return entry.ID
			}
		}
	}
	t.Fatalf("no entry carries run %d", runID)
	return ""
}

// ── the model can still read back an earlier answer ──────────────────────

// TestBlocksRead_OfATurnReturnsItsProse: `blocks.read` of a finished turn
// hands back what the assistant said.
//
// It is here because moving the answer out of the turn's own artifact and into
// its `text` children is exactly the change that could have broken it in
// silence: the read walked the entry's executions, found no body where one had
// always been, and would have reported a turn that plainly printed something
// as a block that kept nothing. A soft degrade the surface contradicts is how
// a feature that no longer exists survives a release (AGENTS.md).
func TestBlocksRead_OfATurnReturnsItsProse(t *testing.T) {
	h := newAskHarness(t, &toolCallScript{events: []assistant.AskEvent{
		answerEvent("let me look:"),
		callEvent("call_1", "files.read", ""),
		answerEvent(" it says hello"),
	}})
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	turn := askAndWaitForState(t, h, sid, "what does it say?")

	got, err := h.ws.blockBody(context.Background(), h.db.Ledger(), turn)
	if err != nil {
		t.Fatalf("blockBody: %v", err)
	}
	if !got.kept {
		t.Fatal("the turn reports that it kept no body — its answer is its prose children (ADR-0040)")
	}
	// ONE answer, however many pieces the calls cut it into: a reader asking
	// for the block's body is asking what the assistant said, and where the
	// calls fell is a different question.
	if got.text != "let me look: it says hello" {
		t.Errorf("blocks.read of the turn = %q, want the whole answer joined in seat order", got.text)
	}

	// The paired absence, so "kept" above is not simply what this read always
	// answers: a turn that streamed nothing kept nothing, and says so rather
	// than reporting an empty output.
	silent := newAskHarness(t, &toolCallScript{events: []assistant.AskEvent{
		callEvent("call_1", "files.read", ""),
	}})
	silent.createEndpoint()
	silentSID := openLocalSession(t, silent.conn)
	silentTurn := askAndWaitForState(t, silent, silentSID, "q")
	quiet, err := silent.ws.blockBody(context.Background(), silent.db.Ledger(), silentTurn)
	if err != nil {
		t.Fatalf("blockBody on a silent turn: %v", err)
	}
	if quiet.kept {
		t.Errorf("a turn that printed nothing reports a kept body of %q", quiet.text)
	}
}
