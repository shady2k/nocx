package transport

// The harness owns model conversation state. The transport still records
// every question and its prose so restore and ledger recall remain whole.
import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
)

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

func TestAgentAsk_TheFollowUpCarriesTheEarlierTurnsProseWhole(t *testing.T) {
	h := newAskHarnessWithOpts(t, protocol.Script{Turns: [][]protocol.Step{
		{{Kind: protocol.StepAnswer, Text: "let me look: "}, readFileStep(t), {Kind: protocol.StepAnswer, Text: "it says hello, "}, readFileStep(t), {Kind: protocol.StepAnswer, Text: "so the config is fine."}, {Kind: protocol.StepEnd}},
		answerScript("glad to hear it.").Steps,
	}}, WithAgentPolicy(autonomousPolicyStore(t)))
	h.createEndpoint()
	sid := openSessionInAskPane(t, h.conn, 1)
	askAndSettle(t, h, sid, "ask-1", "what does the config say?", 2)
	first := h.prompt()
	turn := askAndSettle(t, h, sid, "ask-2", "and is that a problem?", 3)
	follow := h.prompt()
	if first.ConversationID != askPaneID || follow.ConversationID != first.ConversationID {
		t.Fatalf("conversation changed: %q -> %q", first.ConversationID, follow.ConversationID)
	}
	if !strings.HasPrefix(follow.Text, "and is that a problem?\n") {
		t.Fatalf("follow-up question lost: %q", follow.Text)
	}
	prior, err := h.db.Ledger().PriorTurns(t.Context(), askPaneID, turn)
	if err != nil || len(prior) != 1 {
		t.Fatalf("PriorTurns: %+v, %v", prior, err)
	}
	if prior[0].Question != "what does the config say?" || prior[0].Prose.Text != "let me look: it says hello, so the config is fine." || prior[0].Prose.Blocks != 3 {
		t.Fatalf("earlier turn not whole: %+v", prior[0])
	}
}

func TestAgentAsk_ThirdQuestionCarriesAllEarlierTurnsOldestFirst(t *testing.T) {
	h := newAskHarness(t, protocol.Script{Turns: [][]protocol.Step{answerScript("answer one").Steps, answerScript("answer two").Steps, answerScript("answer three").Steps}})
	h.createEndpoint()
	sid := openSessionInAskPane(t, h.conn, 1)
	first := askAndSettle(t, h, sid, "ask-1", "question one", 2)
	before, err := h.db.Ledger().PriorTurns(t.Context(), askPaneID, first)
	if err != nil || len(before) != 0 {
		t.Fatalf("first turn has history: %+v, %v", before, err)
	}
	askAndSettle(t, h, sid, "ask-2", "question two", 3)
	third := askAndSettle(t, h, sid, "ask-3", "question three", 4)
	prior, err := h.db.Ledger().PriorTurns(t.Context(), askPaneID, third)
	if err != nil || len(prior) != 2 {
		t.Fatalf("PriorTurns: %+v, %v", prior, err)
	}
	for i, want := range []struct{ q, a string }{{"question one", "answer one"}, {"question two", "answer two"}} {
		if prior[i].Question != want.q || prior[i].Prose.Text != want.a {
			t.Errorf("turn %d: %+v", i, prior[i])
		}
	}
}

func TestAgentAsk_AnEvictedEarlierAnswerSaysSoRatherThanLeavingAHole(t *testing.T) {
	h := newAskHarness(t, protocol.Script{Turns: [][]protocol.Step{answerScript("a long answer nobody will read again.").Steps, answerScript("ok.").Steps, answerScript("ok again.").Steps}})
	h.createEndpoint()
	sid := openSessionInAskPane(t, h.conn, 1)
	askAndSettle(t, h, sid, "ask-1", "what does the config say?", 2)
	second := askAndSettle(t, h, sid, "ask-2", "sure?", 3)
	prior, err := h.db.Ledger().PriorTurns(t.Context(), askPaneID, second)
	if err != nil || len(prior) != 1 || prior[0].Prose.Text != "a long answer nobody will read again." {
		t.Fatalf("before eviction: %+v, %v", prior, err)
	}
	if _, evictErr := h.db.Ledger().EvictBodies(t.Context(), content.BodyEvictionRequest{KeepBytes: 0, Max: 100}); evictErr != nil {
		t.Fatal(evictErr)
	}
	third := askAndSettle(t, h, sid, "ask-3", "and now?", 4)
	prior, err = h.db.Ledger().PriorTurns(t.Context(), askPaneID, third)
	if err != nil || len(prior) != 2 {
		t.Fatalf("after eviction: %+v, %v", prior, err)
	}
	if !prior[0].Prose.Evicted || prior[0].Prose.Text != "" || prior[0].Question != "what does the config say?" || prior[1].Question != "sure?" {
		t.Fatalf("eviction lost metadata or retained text: %+v", prior)
	}
}

func TestAgentAsk_AnEarlierAnswerThatWasCutShortIsMarkedPartial(t *testing.T) {
	for _, failed := range []bool{false, true} {
		terminal := protocol.Step{Kind: protocol.StepEnd}
		if failed {
			terminal = protocol.Step{Kind: protocol.StepFail, Reason: "the provider hung up mid-answer"}
		}
		h := newAskHarness(t, protocol.Script{Turns: [][]protocol.Step{{{Kind: protocol.StepAnswer, Text: "I was halfway through saying"}, terminal}, answerScript("ok.").Steps}})
		h.createEndpoint()
		sid := openSessionInAskPane(t, h.conn, 1)
		askAndSettle(t, h, sid, "ask-1", "what does the config say?", 2)
		next := askAndSettle(t, h, sid, "ask-2", "and then?", 3)
		prior, err := h.db.Ledger().PriorTurns(t.Context(), askPaneID, next)
		if err != nil || len(prior) != 1 {
			t.Fatalf("PriorTurns: %+v, %v", prior, err)
		}
		want := content.RunCompleted
		if failed {
			want = content.RunFailed
		}
		if prior[0].Prose.State != want || prior[0].Prose.Text != "I was halfway through saying" {
			t.Fatalf("partial state/text: %+v", prior[0])
		}
	}
}

// The agent's actor owns context limits; the transport must not truncate the
// durable ledger when a conversation is larger than its former engine budget.
func TestAgentAsk_ConversationBudgetKeepsDurableTurnsWhole(t *testing.T) {
	oldAnswer := strings.Repeat("old answer ", 400)
	newAnswer := strings.Repeat("new answer ", 1000)
	h := newAskHarness(t, protocol.Script{Turns: [][]protocol.Step{answerScript(oldAnswer).Steps, answerScript(newAnswer).Steps, answerScript("final answer").Steps}})
	h.createEndpoint()
	sid := openSessionInAskPane(t, h.conn, 1)
	askAndSettle(t, h, sid, "ask-1", "old question", 2)
	askAndSettle(t, h, sid, "ask-2", "new question", 3)
	last := askAndSettle(t, h, sid, "ask-3", "final question", 4)
	prior, err := h.db.Ledger().PriorTurns(t.Context(), askPaneID, last)
	if err != nil || len(prior) != 2 || prior[0].Prose.Text != oldAnswer || prior[1].Prose.Text != newAnswer {
		t.Fatalf("durable context truncated: %+v, %v", prior, err)
	}
	if h.prompt().ConversationID != askPaneID {
		t.Fatalf("lost actor identity: %+v", h.prompt())
	}
}

func TestAgentAsk_DestructiveCommandEvidenceSurvivesIntoNextTurn(t *testing.T) {
	script := protocol.Script{Turns: [][]protocol.Step{
		{{Kind: protocol.StepAnswer, Text: "the file is deleted."}, {Kind: protocol.StepEnd, Model: "fixture-model"}},
		{{Kind: protocol.StepAnswer, Text: "I can see the recorded action."}, {Kind: protocol.StepEnd, Model: "fixture-model"}},
	}}
	h := newAskHarnessWithOpts(t, script, WithAgentPolicy(autonomousPolicyStore(t)))
	sid := openSessionInAskPane(t, h.conn, 1)
	turnID := askAndSettle(t, h, sid, "ask-1", "delete the file", 2)

	ctx := context.Background()
	led := h.db.Ledger()
	envID := content.EnvironmentIDFor(content.EnvLocal, "")
	if err := led.EnsureEnvironment(ctx, content.Environment{ID: envID, Kind: content.EnvLocal}); err != nil {
		t.Fatalf("EnsureEnvironment: %v", err)
	}
	if _, err := led.RecordObservation(ctx, content.Observation{
		EnvironmentID: envID, Criticality: content.CriticalityRoutine,
	}); err != nil {
		t.Fatalf("RecordObservation: %v", err)
	}
	actionID := "11111111-1111-7111-8111-111111111111"
	actionPayload := `{"tool":"session.run","effect":"mutate-destructive","args":{"command":"rm -f target.txt"},"opensBlock":true}`
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: actionID, Client: "agent", EnvironmentID: envID, Cwd: "/repo",
		Kind: content.EntryAction, Source: content.SourceAssistant,
		Intent: "session.run", Payload: actionPayload,
	}); err != nil {
		t.Fatalf("Submit action: %v", err)
	}
	if _, err := led.AddCause(ctx, turnID, actionID); err != nil {
		t.Fatalf("AddCause action: %v", err)
	}
	commandID := "22222222-2222-7222-8222-222222222222"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: commandID, Client: "renderer", EnvironmentID: envID, Cwd: "/repo",
		Kind: content.EntryShell, Intent: "rm -f target.txt",
	}); err != nil {
		t.Fatalf("Submit command: %v", err)
	}
	if _, err := led.AddCause(ctx, turnID, commandID); err != nil {
		t.Fatalf("AddCause command: %v", err)
	}
	shellExec, err := led.StartExecution(ctx, content.StartExecution{EntryID: commandID})
	if err != nil {
		t.Fatalf("StartExecution command: %v", err)
	}
	artifactID := "33333333-3333-7333-8333-333333333333"
	if _, appendErr := led.AppendArtifact(ctx, content.AppendArtifact{
		EntryID: commandID, ExecutionID: &shellExec, ID: artifactID,
		MediaType: content.MediaText, CaptureMethod: content.CaptureRawOutput,
	}); appendErr != nil {
		t.Fatalf("AppendArtifact command: %v", appendErr)
	}
	if chunkErr := led.AppendChunk(ctx, artifactID, 1, []byte("removed\n")); chunkErr != nil {
		t.Fatalf("AppendChunk command: %v", chunkErr)
	}
	if finishShellErr := led.FinishExecution(ctx, shellExec, content.FinishExecution{
		EndedAt: 2, TerminationReason: content.TermCompleted, Status: content.EntrySuccess,
	}); finishShellErr != nil {
		t.Fatalf("FinishExecution command: %v", finishShellErr)
	}
	actionExec, err := led.StartExecution(ctx, content.StartExecution{EntryID: actionID, Attempt: 1})
	if err != nil {
		t.Fatalf("StartExecution action: %v", err)
	}
	if finishActionErr := led.FinishExecution(ctx, actionExec, content.FinishExecution{
		EndedAt: 3, TerminationReason: content.TermCompleted, Status: content.EntrySuccess,
	}); finishActionErr != nil {
		t.Fatalf("FinishExecution action: %v", finishActionErr)
	}

	nextTurn := askAndSettle(t, h, sid, "ask-2", "what happened?", 3)
	prior, err := h.db.Ledger().PriorTurns(ctx, askPaneID, nextTurn)
	if err != nil || len(prior) != 1 {
		t.Fatalf("PriorTurns: %+v, %v", prior, err)
	}
	evidence := strings.Join(prior[0].ToolLines, "\n")
	if strings.Contains(evidence, "removed") {
		t.Fatalf("tool output leaked: %q", evidence)
	}
	if !strings.Contains(evidence, "ran rm -f target.txt → 1 lines") {
		t.Fatalf("missing completed action evidence: %q", evidence)
	}
	action, err := led.Entry(ctx, actionID)
	if err != nil || action == nil || action.ParentID == nil || *action.ParentID != turnID {
		t.Fatalf("action linkage = %+v (err=%v), want same turn", action, err)
	}
	command, err := led.Entry(ctx, commandID)
	if err != nil || command == nil || command.ParentID == nil || *command.ParentID != turnID {
		t.Fatalf("command linkage = %+v (err=%v), want same turn", command, err)
	}
}

func TestAgentAsk_ASessionWithNoPaneCarriesNoConversation(t *testing.T) {
	for _, anchored := range []bool{false, true} {
		h := newAskHarness(t, protocol.Script{Turns: [][]protocol.Step{answerScript("the config is fine.").Steps, answerScript("ok.").Steps}})
		sid := openLocalSession(t, h.conn)
		if anchored {
			sid = openSessionInAskPane(t, h.conn, 2)
		}
		askAndSettle(t, h, sid, "ask-1", "what does the config say?", 3)
		turn := askAndSettle(t, h, sid, "ask-2", "sure?", 4)
		prior, err := h.db.Ledger().PriorTurns(t.Context(), askPaneID, turn)
		if err != nil {
			t.Fatal(err)
		}
		if anchored && (len(prior) != 1 || prior[0].Prose.Text != "the config is fine.") {
			t.Fatalf("anchored history: %+v", prior)
		}
		if !anchored && len(prior) != 0 {
			t.Fatalf("unanchored history: %+v", prior)
		}
	}
}
