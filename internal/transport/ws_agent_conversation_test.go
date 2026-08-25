package transport

// The follow-up question carries the turn before it (bead nocx-dc2fr.5,
// ADR-0040's closing consequence: "the conversation is assembled from the
// children, in pos order, per run").
//
// A turn's answer used to be one stored string, so "send the previous answer"
// was a field read. It is several rows now — one `text` block per run of prose
// between two tool calls — and these tests assert the thing a person can do
// because of that: ask a second question and have the model see, whole and in
// order, what it said to the first.
//
// The assertions are on the MESSAGES THE CLIENT RECEIVED, which is the seam
// the model is on the other side of. Asserting on the ledger instead would
// test the store twice (internal/content's conversation tests already do) and
// would never notice a transport that read the rows correctly and sent none of
// them.
//
// Every "carries nothing when…" here is paired with the "and with a
// conversation it carries it" that keeps it honest (AGENTS.md testing rule 3).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

// ── the recording client ─────────────────────────────────────────────────

// conversationClient plays one event script per ask and records every
// AskParams it was handed. The script is per ASK rather than one list, because
// the whole point is what the SECOND ask receives after the first one ran.
type conversationClient struct {
	mu       sync.Mutex
	scripts  [][]assistant.AskEvent
	fail     []error // per ask; nil entries answer normally
	count    int
	received []assistant.AskParams
}

func (c *conversationClient) Probe(_ context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
	return assistant.ProbeResult{OK: true, Model: p.Model}, nil
}

func (c *conversationClient) Discard(string) {}

func (c *conversationClient) Ask(_ context.Context, p assistant.AskParams, onEvent func(assistant.AskEvent) error) error {
	c.mu.Lock()
	n := c.count
	c.count++
	c.received = append(c.received, p)
	var script []assistant.AskEvent
	if n < len(c.scripts) {
		script = c.scripts[n]
	}
	var failure error
	if n < len(c.fail) {
		failure = c.fail[n]
	}
	c.mu.Unlock()
	for _, ev := range script {
		if err := onEvent(ev); err != nil {
			return err
		}
	}
	return failure
}

// nth is the AskParams of the nth ask (0-based), and it fails loudly when
// there was no nth ask — "the messages were empty" and "the ask never
// happened" must not look alike.
func (c *conversationClient) nth(t *testing.T, n int) assistant.AskParams {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if n >= len(c.received) {
		t.Fatalf("ask %d never reached the client — only %d did", n, len(c.received))
	}
	return c.received[n]
}

// ── reading the assembled context ────────────────────────────────────────

// roles is the message shape, for an assertion that reads like the
// conversation: "system, user, assistant, user".
func roles(msgs []assistant.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Role)
	}
	return out
}

// theAnswerIn is the ONE assistant-role message the assembly may contain, and
// it fails when there is not exactly one: two would mean the earlier turn's
// prose was sent as pieces, which is the defect this whole bead is about.
func theAnswerIn(t *testing.T, msgs []assistant.Message) string {
	t.Helper()
	var found []string
	for _, m := range msgs {
		if m.Role == "assistant" {
			found = append(found, m.Content)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the assembled context carries %d assistant messages, want exactly one — "+
			"the prose of one run is ONE message: %+v", len(found), msgs)
	}
	return found[0]
}

// noAnswerIn asserts the assembly carries no earlier answer at all.
func noAnswerIn(t *testing.T, msgs []assistant.Message, why string) {
	t.Helper()
	for _, m := range msgs {
		if m.Role == "assistant" {
			t.Fatalf("%s, yet the context carries an assistant message %q", why, m.Content)
		}
	}
}

// ── the fixture ──────────────────────────────────────────────────────────

// openSessionInAskPane opens a session that IS the pipe of the harness's pane,
// which is what the product opens and what anchors a turn (nocx-4em1z). The
// conversation is read by pane, so an unanchored session has no thread — the
// paired test below is about exactly that.
func openSessionInAskPane(t *testing.T, conn *websocket.Conn, id int) string {
	t.Helper()
	resp := jsonrpcCallWithID(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0, "paneId": askPaneID,
	}, id)
	var opened struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &opened); err != nil || opened.Result.SessionID == "" {
		t.Fatalf("open with a pane: %v\nraw: %s", err, resp)
	}
	return opened.Result.SessionID
}

// askAndSettle sends one question and waits for the run to terminalize, so the
// NEXT ask reads a store nothing is still writing to. It waits on the terminal
// agent.runState notification rather than on a duration — a test that needed a
// slow machine to pass would be broken on a fast one too.
func askAndSettle(t *testing.T, h *askHarness, sid, askID, question string, id int) string {
	t.Helper()
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": askID, "sessionId": sid, "question": question, "cwd": "/repo",
	}, id)
	if errObj != nil {
		t.Fatalf("ask %q: %+v", askID, errObj)
	}
	readNotification(t, h.conn, "agent.runState", 5*time.Second)
	return res.EntryID
}

// ── acceptance 1: the earlier turn's prose arrives WHOLE, in seat order ──

// The first turn says something, calls a tool, says something else, calls
// another, concludes. The follow-up question then carries that turn to the
// model as its question and ONE assistant message holding all three runs of
// prose, in the order they were written.
//
// The order is the meaning: a sentence written before a call explains why the
// call was made, and a sentence written after it is a conclusion drawn from
// its output. A message that shuffled them would contain every word and say
// something the model never said.
func TestAgentAsk_TheFollowUpCarriesTheEarlierTurnsProseWhole(t *testing.T) {
	client := &conversationClient{scripts: [][]assistant.AskEvent{
		{
			answerEvent("let me look: "),
			callEvent("call_1", "files.read", ""),
			answerEvent("it says hello, "),
			callEvent("call_2", "files.read", ""),
			answerEvent("so the config is fine."),
		},
		{answerEvent("glad to hear it.")},
	}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openSessionInAskPane(t, h.conn, 1)

	askAndSettle(t, h, sid, "ask-1", "what does the config say?", 2)
	askAndSettle(t, h, sid, "ask-2", "and is that a problem?", 3)

	follow := client.nth(t, 1)
	if got := roles(follow.Messages); len(got) != 4 ||
		got[0] != "system" || got[1] != "user" || got[2] != "assistant" || got[3] != "user" {
		t.Fatalf("the follow-up's context reads %v, want system, the earlier question, "+
			"the earlier answer, then this question: %+v", got, follow.Messages)
	}
	if follow.Messages[1].Content != "what does the config say?" {
		t.Errorf("the earlier question reads %q, want what was asked first", follow.Messages[1].Content)
	}
	const whole = "let me look: it says hello, so the config is fine."
	if answer := theAnswerIn(t, follow.Messages); answer != whole {
		t.Fatalf("the earlier answer reads\n  %q\nwant its three runs of prose whole, in order\n  %q",
			answer, whole)
	}
	if follow.Messages[3].Content != "and is that a problem?" {
		t.Errorf("the follow-up question reads %q, want the second question", follow.Messages[3].Content)
	}
}

// The paired half, and the one that says nothing regressed: the FIRST question
// in a pane carries no conversation — there is none — and its context is
// exactly what it was before this bead: the standing prompt and the question.
func TestAgentAsk_TheFirstQuestionCarriesNoConversation(t *testing.T) {
	client := &conversationClient{scripts: [][]assistant.AskEvent{{answerEvent("hello.")}}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openSessionInAskPane(t, h.conn, 1)

	askAndSettle(t, h, sid, "ask-1", "what does the config say?", 2)

	first := client.nth(t, 0)
	if got := roles(first.Messages); len(got) != 2 || got[0] != "system" || got[1] != "user" {
		t.Fatalf("the first question's context reads %v, want the standing prompt and the question: %+v",
			got, first.Messages)
	}
	noAnswerIn(t, first.Messages, "nothing was asked before the first question")
}

// ── acceptance 4: a turn that made no calls assembles as it always did ───

// One question, one uninterrupted answer, one message. This is the shape that
// existed before the epic, and if the join only worked for turns that
// interleaved, this is the regression nobody would have gone looking for.
func TestAgentAsk_AnEarlierTurnThatMadeNoCallsIsOneMessage(t *testing.T) {
	client := &conversationClient{scripts: [][]assistant.AskEvent{
		{answerEvent("the config "), answerEvent("is fine.")},
		{answerEvent("good.")},
	}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openSessionInAskPane(t, h.conn, 1)

	askAndSettle(t, h, sid, "ask-1", "what does the config say?", 2)
	askAndSettle(t, h, sid, "ask-2", "sure?", 3)

	if answer := theAnswerIn(t, client.nth(t, 1).Messages); answer != "the config is fine." {
		t.Fatalf("the earlier answer reads %q, want the whole of it", answer)
	}
}

// ── acceptance 3: an evicted answer SAYS SO ──────────────────────────────

// Retention takes the prose of one run as a unit (ADR-0040's retention rule),
// and a turn whose prose has gone keeps every block. The follow-up must then
// carry the earlier question with a STATED absence where the answer was —
// never a hole (which reads as "that question was never answered") and never
// invented or paraphrased text. AGENTS.md: a soft degrade must be visible, and
// the surface it has to be visible on here is the model's own context.
func TestAgentAsk_AnEvictedEarlierAnswerSaysSoRatherThanLeavingAHole(t *testing.T) {
	client := &conversationClient{scripts: [][]assistant.AskEvent{
		{answerEvent("a long answer "), answerEvent("nobody will read again.")},
		{answerEvent("ok.")},
		{answerEvent("ok again.")},
	}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openSessionInAskPane(t, h.conn, 1)

	askAndSettle(t, h, sid, "ask-1", "what does the config say?", 2)

	// The paired half FIRST, on the same store: while the bodies are here the
	// follow-up carries the real text. Without it, the sentence below could be
	// what this path always sends.
	askAndSettle(t, h, sid, "ask-2", "sure?", 3)
	if answer := theAnswerIn(t, client.nth(t, 1).Messages); answer != "a long answer nobody will read again." {
		t.Fatalf("before eviction the earlier answer reads %q, want its text", answer)
	}

	if _, err := h.db.Ledger().EvictBodies(context.Background(),
		content.BodyEvictionRequest{KeepBytes: 0, Max: 100}); err != nil {
		t.Fatalf("EvictBodies: %v", err)
	}

	askAndSettle(t, h, sid, "ask-3", "and now?", 4)
	third := client.nth(t, 2)
	answer := theAnswerIn(t, third.Messages)
	if answer != proseGoneNotice {
		t.Fatalf("after eviction the earlier answer reads %q, want the sentence that says the text is gone (%q)",
			answer, proseGoneNotice)
	}
	// Nothing was invented in its place: none of the evicted words survive
	// anywhere in the context the model is handed.
	for _, m := range third.Messages {
		if strings.Contains(m.Content, "nobody will read again") {
			t.Fatalf("the evicted text reappeared in a %s message: %q", m.Role, m.Content)
		}
	}
	// And the question it was an answer to is still there — which is what
	// makes saying so possible rather than merely honest.
	if third.Messages[1].Content != "sure?" {
		t.Errorf("the earlier question reads %q after eviction, want it intact", third.Messages[1].Content)
	}
}

// ── trap 3: a partial answer is marked, not passed off as a whole one ────

// Whether a partial answer is a REAL MESSAGE or an UNFINISHED ATTEMPT is a
// fact about the run's state, never about how much text there is: an
// interrupted run leaves exactly the rows a finished one leaves. So a turn
// whose run did not complete goes to the model with what it managed to say AND
// with the fact that it was cut short.
func TestAgentAsk_AnEarlierAnswerThatWasCutShortIsMarkedPartial(t *testing.T) {
	client := &conversationClient{
		scripts: [][]assistant.AskEvent{
			{answerEvent("I was halfway through saying")},
			{answerEvent("ok.")},
		},
		fail: []error{errors.New("the provider hung up mid-answer"), nil},
	}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openSessionInAskPane(t, h.conn, 1)

	askAndSettle(t, h, sid, "ask-1", "what does the config say?", 2)
	askAndSettle(t, h, sid, "ask-2", "and then?", 3)

	answer := theAnswerIn(t, client.nth(t, 1).Messages)
	if !strings.HasPrefix(answer, "I was halfway through saying") {
		t.Fatalf("the partial answer reads %q, want what was written before the run stopped", answer)
	}
	if !strings.Contains(answer, proseCutShortNotice) {
		t.Fatalf("the partial answer reads %q, want it MARKED partial (%q) — an unfinished attempt "+
			"presented as an answer is the model being told the run said all it meant to",
			answer, proseCutShortNotice)
	}
}

// The pair: a run that COMPLETED is sent unmarked. The two are what a reader
// decides between, so a test that only saw one would not notice a path that
// always marked, or never did.
func TestAgentAsk_AnEarlierAnswerThatFinishedIsNotMarkedPartial(t *testing.T) {
	client := &conversationClient{scripts: [][]assistant.AskEvent{
		{answerEvent("all of it.")},
		{answerEvent("ok.")},
	}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openSessionInAskPane(t, h.conn, 1)

	askAndSettle(t, h, sid, "ask-1", "what does the config say?", 2)
	askAndSettle(t, h, sid, "ask-2", "and then?", 3)

	if answer := theAnswerIn(t, client.nth(t, 1).Messages); answer != "all of it." {
		t.Fatalf("a finished answer reads %q, want exactly what was said and no marker", answer)
	}
}

// ── the thread is the pane ───────────────────────────────────────────────

// A session that is the pipe of no recorded pane has no thread: its turns
// carry no anchor, so there is nothing to read them back by, and answering
// from every pane's turns would put another tab's conversation into this one.
// Paired with the anchored session, which does carry it — the same two asks,
// the same script, one difference.
func TestAgentAsk_ASessionWithNoPaneCarriesNoConversation(t *testing.T) {
	script := [][]assistant.AskEvent{{answerEvent("the config is fine.")}, {answerEvent("ok.")}}

	unanchored := &conversationClient{scripts: script}
	h := newAskHarness(t, unanchored)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	askAndSettle(t, h, sid, "ask-1", "what does the config say?", 2)
	askAndSettle(t, h, sid, "ask-2", "sure?", 3)
	noAnswerIn(t, unanchored.nth(t, 1).Messages, "the session is the pipe of no recorded pane")

	anchored := &conversationClient{scripts: script}
	h2 := newAskHarness(t, anchored)
	h2.createEndpoint()
	sid2 := openSessionInAskPane(t, h2.conn, 1)
	askAndSettle(t, h2, sid2, "ask-1", "what does the config say?", 2)
	askAndSettle(t, h2, sid2, "ask-2", "sure?", 3)
	if answer := theAnswerIn(t, anchored.nth(t, 1).Messages); answer != "the config is fine." {
		t.Fatalf("the anchored session's follow-up carries %q, want the earlier answer", answer)
	}
}
