package transport

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	agentharness "github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
	"github.com/shady2k/nocx/internal/notify"
)

// steerScript keeps the turn open after it has said something: the moment a
// person types while the agent answers.
func steerScript() protocol.Script {
	return protocol.Script{Steps: []protocol.Step{
		{Kind: protocol.StepAnswer, Text: "reading the logs"},
		{Kind: protocol.StepHang},
	}}
}

func TestAgentSteer_DTOConformsToContract(t *testing.T) {
	for _, outcome := range []string{steerSteered, steerNextQuestion} {
		validateJSON(t, loadSchema(t, "agent.steer.schema.json"), mustMarshal(agentSteerResponse{
			RunID: 7, Outcome: outcome,
		}), "agent.steer DTO "+outcome)
	}
}

// A message typed while the conversation's turn runs reaches the agent that is
// answering it, over the real socket, and is answered as delivered.
func TestAgentSteer_OverTheWireReachesTheRunningAgent(t *testing.T) {
	h := newAskHarness(t, steerScript())
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "steer-ask-1", "sessionId": sid, "question": "what is in the log?", "cwd": "/repo",
	}, 1)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	h.waitHung()

	const message = "also check the second log"
	resp := jsonrpcCallWithID(t, h.conn, "agent.steer", map[string]any{"runId": res.RunID, "text": message}, 2)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode agent.steer: %v\nraw: %s", err, resp)
	}
	if env.Error != nil {
		t.Fatalf("agent.steer: %+v", env.Error)
	}
	validateJSON(t, loadSchema(t, "agent.steer.schema.json"), env.Result, "agent.steer result (real socket)")
	var result agentSteerResponse
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("decode agent.steer result: %v", err)
	}
	if result.RunID != res.RunID || result.Outcome != steerSteered {
		t.Fatalf("agent.steer result = %+v, want run %d steered into the turn", result, res.RunID)
	}
	// The agent itself received it, as the person wrote it.
	if got := strings.TrimSpace(string(h.agent.read(h.agent.script.SteerFile))); got != message {
		t.Fatalf("the agent received %q, want the person's message", got)
	}
	_ = jsonrpcCallWithID(t, h.conn, "agent.cancel", map[string]any{"runId": res.RunID}, 3)
}

// After the turn has ended there is nowhere for a message to land, and the
// answer says so instead of pretending it was delivered.
// A message naming a run of THIS conversation that has already closed is not an
// error: the renderer's turn was still on screen when the person typed, and the
// message is asked as the conversation's next question (ADR-4khgzeyp decision 3,
// nocx-a62cl.6.1). An id that names nothing is still refused.
func TestAgentSteer_AClosedRunOfThisConversationAsksTheMessageNext(t *testing.T) {
	h := newAskHarness(t, answerScript("done"))
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "steer-ask-2", "sessionId": sid, "question": "what is in the log?", "cwd": "/repo",
	}, 1)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	state := readNotification(t, h.conn, "agent.runState", 15*time.Second)
	var settled agentRunState
	if err := json.Unmarshal(state, &settled); err != nil {
		t.Fatalf("decode agent.runState: %v", err)
	}
	if settled.RunID != res.RunID || settled.State != string(content.RunCompleted) {
		t.Fatalf("runState = %+v, want run %d completed", settled, res.RunID)
	}
	// The run is closed, and the message is not lost.
	reply := jsonrpcCallWithID(t, h.conn, "agent.steer", map[string]any{"runId": res.RunID, "text": "too late"}, 2)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(reply, &env); err != nil {
		t.Fatalf("decode agent.steer: %v\nraw: %s", err, reply)
	}
	if env.Error != nil {
		t.Fatalf("agent.steer after the turn ended: %+v", env.Error)
	}
	validateJSON(t, loadSchema(t, "agent.steer.schema.json"), env.Result, "agent.steer result (a closed run)")
	var result agentSteerResponse
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("decode agent.steer result: %v", err)
	}
	if result.Outcome != steerNextQuestion {
		t.Fatalf("agent.steer on a closed run = %+v, want %q", result, steerNextQuestion)
	}
}

// An id that names no run at all is still refused, and says why: nothing can be
// assumed about a message addressed to nothing.
func TestAgentSteer_AnUnknownRunIsRefused(t *testing.T) {
	h := newAskHarness(t, answerScript("done"))
	sid := openLocalSession(t, h.conn)
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "steer-ask-3", "sessionId": sid, "question": "what is in the log?", "cwd": "/repo",
	}, 1); errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	_ = readNotification(t, h.conn, "agent.runState", 15*time.Second)
	reply := jsonrpcCallWithID(t, h.conn, "agent.steer", map[string]any{"runId": 987654, "text": "hello?"}, 2)
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(reply, &env); err != nil {
		t.Fatalf("decode refused agent.steer: %v\nraw: %s", err, reply)
	}
	if env.Error == nil || !strings.Contains(env.Error.Message, "not active") {
		t.Fatalf("agent.steer for an unknown run = %s, want an honest not-active error", reply)
	}
}

// A message typed while a call awaits approval declines THAT call, in the
// person's own words, and then reaches the agent (ADR-anntnxdj decision 4).
// The question is retired at once, nothing is decided in the approval store —
// the person answered with a message, not with a button — and the call's
// result the model reads carries what they wrote.
// suspendedSteerableRun is a run whose one call asks, whose result the agent
// keeps even when the call is refused, and which holds the turn open after it
// so a second message can reach the agent before the turn ends on its own.
func suspendedSteerableRun(t *testing.T, policy assistant.GlobalPolicy) *scopeHarness {
	t.Helper()
	script := approvalToolScript("session.read", map[string]any{})
	script.Steps = []protocol.Step{
		{Kind: protocol.StepTool, Name: "session.read", Arguments: map[string]any{}},
		{Kind: protocol.StepGate},
		{Kind: protocol.StepAnswer, Text: "done"},
		{Kind: protocol.StepEnd, Model: "test-model"},
	}
	script.ResultFile = filepath.Join(t.TempDir(), "declined-exchange")
	script.Steps[0].ResultFile = script.ResultFile
	return suspendedRunWith(t, policy, script)
}

func TestAgentSteer_DeclinesTheCallThePersonTypedDuring(t *testing.T) {
	h := suspendedSteerableRun(t, askObserveStore(t))
	const message = "use the second log instead"
	resp := jsonrpcCallWithID(t, h.conn, "agent.steer", map[string]any{"runId": h.runID, "text": message}, 2)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode agent.steer: %v\nraw: %s", err, resp)
	}
	if env.Error != nil {
		t.Fatalf("agent.steer: %+v", env.Error)
	}
	validateJSON(t, loadSchema(t, "agent.steer.schema.json"), env.Result, "agent.steer result (real socket)")

	// The prompt goes at once: a question the backend has answered cannot go
	// on advertising an answer.
	retired := readNotification(t, h.conn, "agent.approvalRetired", 5*time.Second)
	var withdrew agentApprovalRetired
	if err := json.Unmarshal(retired, &withdrew); err != nil {
		t.Fatalf("agent.approvalRetired: %v", err)
	}
	if withdrew.CallID != h.asked.CallID || withdrew.Tool != h.asked.Tool {
		t.Fatalf("retired %+v, want the question about %s/%s", withdrew, h.asked.Tool, h.asked.CallID)
	}

	// The call the agent was waiting on comes back refused, in the person's
	// words — that sentence is the call's RESULT, which is what the model
	// reads and acts on.
	result := h.toolResult()
	if !strings.Contains(result, message) || !strings.Contains(result, "answered with a message instead") {
		t.Fatalf("the call's result = %q, want the person's message in it", result)
	}

	// Nothing was decided in the store: answering the withdrawn prompt now is
	// refused rather than reading as a second answer to one question.
	if _, errObj := approveOverWire(t, h.conn, h.answer(false, "once"), 3); errObj == nil || !strings.Contains(errObj.Message, "nothing pending") {
		t.Fatalf("agent.approve after the message = %+v, want an honest nothing-pending refusal", errObj)
	}

	// And the message itself reached the agent.
	if got := strings.TrimSpace(string(h.agent.read(h.agent.script.SteerFile))); got != message {
		t.Fatalf("the agent received %q, want the person's message", got)
	}
	// The turn goes on and ends on its own terms: the message cost the call,
	// not the turn.
	h.release()
	if raw := readNotification(t, h.conn, "agent.runState", 10*time.Second); !strings.Contains(string(raw), "completed") {
		t.Fatalf("runState = %s, want the run to finish normally", raw)
	}
}

// A connector whose agent cannot steer still applies the person's answer to the
// pending approval at once; the message is not lost but becomes the
// conversation's next question (ADR-anntnxdj decision 4), and the reply says so
// so the block can draw it that way.
func TestAgentSteer_QueueingAgentDeclinesTheCallAndKeepsTheMessageForNext(t *testing.T) {
	script := approvalToolScript("session.read", map[string]any{})
	script.Steps = []protocol.Step{
		{Kind: protocol.StepTool, Name: "session.read", Arguments: map[string]any{}},
		{Kind: protocol.StepGate},
		{Kind: protocol.StepAnswer, Text: "done"},
		{Kind: protocol.StepEnd, Model: "test-model"},
	}
	script.ResultFile = filepath.Join(t.TempDir(), "declined-exchange")
	script.Steps[0].ResultFile = script.ResultFile
	h := newAskHarnessQueueing(t, script, WithAgentPolicy(askObserveStore(t)), WithScreenCaptureSource(&recordingScreenCapture{body: screenCaptureBody(t, 12, "fixture screen")}))
	// Make the fact explicit here so a change of fixture cannot silently pass
	// this test.
	if got := h.agent.harness.Facts().Steering; got != agentharness.QueuesNextQuestion {
		t.Fatalf("fixture steering = %q, want the queueing half of the contract", got)
	}
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "steer-ask-queue", "sessionId": sid, "question": "what is in the log?", "cwd": "/repo",
	}, 1)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	asked := readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)
	var question agentApprovalRequested
	if err := json.Unmarshal(asked, &question); err != nil {
		t.Fatalf("agent.approvalRequested: %v", err)
	}

	const message = "ask me the other thing instead"
	reply := jsonrpcCallWithID(t, h.conn, "agent.steer", map[string]any{"runId": res.RunID, "text": message}, 2)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(reply, &env); err != nil {
		t.Fatalf("decode agent.steer: %v\nraw: %s", err, reply)
	}
	if env.Error != nil {
		t.Fatalf("agent.steer: %+v", env.Error)
	}
	validateJSON(t, loadSchema(t, "agent.steer.schema.json"), env.Result, "agent.steer result (real socket)")
	var result agentSteerResponse
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("decode agent.steer result: %v", err)
	}
	if result.Outcome != steerNextQuestion {
		t.Fatalf("agent.steer result = %+v, want %q for an agent that cannot steer", result, steerNextQuestion)
	}

	// THE DECLINE HAPPENED AT ONCE, in the person's own words: the question is
	// retired and the call's result carries the message (6.2's path, unchanged
	// by the connector's inability to steer).
	retired := readNotification(t, h.conn, "agent.approvalRetired", 5*time.Second)
	var withdrew agentApprovalRetired
	if err := json.Unmarshal(retired, &withdrew); err != nil {
		t.Fatalf("agent.approvalRetired: %v", err)
	}
	if withdrew.CallID != question.CallID {
		t.Fatalf("retired %+v, want the question about %s", withdrew, question.CallID)
	}
	if got := h.toolResult(); !strings.Contains(got, message) {
		t.Fatalf("the call's result = %q, want the person's message in it", got)
	}
	// The agent itself was never sent the message: it cannot take one.
	if _, err := os.Stat(h.agent.script.SteerFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the queueing agent was sent a steer: %v", err)
	}
	h.release()
	if raw := readNotification(t, h.conn, "agent.runState", 10*time.Second); !strings.Contains(string(raw), "completed") {
		t.Fatalf("runState = %s, want the turn to finish on its own", raw)
	}

	// THE NEXT QUESTION IS A QUESTION: the caller asks the text the reply kept
	// for it, and it is answered by a turn of its own.
	next, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "steer-ask-next", "sessionId": sid, "question": message, "cwd": "/repo",
	}, 4)
	if errObj != nil {
		t.Fatalf("the next question: %+v", errObj)
	}
	if next.RunID == res.RunID {
		t.Fatal("the next question reused the turn that handed it back")
	}
	// It is a turn of its own, with its own block, and the text is its question.
	if next.EntryID == res.EntryID {
		t.Fatal("the next question was recorded on the previous turn's block")
	}
	_ = jsonrpcCallWithID(t, h.conn, "agent.cancel", map[string]any{"runId": next.RunID}, 5)
}

// An EGRESS question answered by a message declines it like any other, and the
// pipeline ends the run: a result that was withheld has nothing to continue
// with (ADR-anntnxdj §3, "a declined egress ends the run").
func TestAgentSteer_DeclinesTheEgressQuestionAndEndsTheRun(t *testing.T) {
	h := suspendedEgressRun(t, askPolicyStore(t))
	const message = "that key is not for you — use the other file"
	reply := jsonrpcCallWithID(t, h.conn, "agent.steer", map[string]any{"runId": h.runID, "text": message}, 2)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(reply, &env); err != nil {
		t.Fatalf("decode agent.steer: %v\nraw: %s", err, reply)
	}
	if env.Error != nil {
		t.Fatalf("agent.steer: %+v", env.Error)
	}
	var result agentSteerResponse
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("decode agent.steer result: %v", err)
	}
	// The message is DECLINED either way. Whether the turn took it inside or
	// handed it back is a race the pipeline decides, because the withheld
	// result ends the run at once — and in the second case the person's message
	// is simply the next question.
	if result.Outcome != steerSteered && result.Outcome != steerNextQuestion {
		t.Fatalf("agent.steer result = %+v", result)
	}
	// The question is retired at once...
	readNotification(t, h.conn, "agent.approvalRetired", 5*time.Second)
	// ...and the run ENDS rather than continuing with a withheld result.
	raw := readNotification(t, h.conn, "agent.runState", 10*time.Second)
	var state agentRunState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("agent.runState: %v", err)
	}
	if state.State != string(content.RunFailed) || !strings.Contains(state.Error, "declined") {
		t.Fatalf("runState = %s, want the run the withheld result ended as the person's decline", raw)
	}
	// Nothing was released to the model: the agent's next call never happened
	// (the run ended with the decline), so the withheld bytes were never sent.
	if calls := h.agent.read(h.agent.script.PromptFile); len(calls) == 0 {
		t.Fatal("the first prompt never reached the agent")
	}
}

// A message pi (or any steering agent) had QUEUED and a cancel dropped is not
// lost: the agent's own queue says what it removed, the cancelled turn hands
// those back, and the wire carries them on the reserved cancel response and on
// the runState so the block can ask them next (ADR-4khgzeyp decision 3).
func TestAgentSteer_TheCancelNamesTheMessageTheAgentDropped(t *testing.T) {
	h := newAskHarness(t, steerScript())
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "steer-ask-dropped", "sessionId": sid, "question": "what is in the log?", "cwd": "/repo",
	}, 1)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	h.waitHung()
	const message = "ask the other thing instead"
	steerResponse := jsonrpcCallWithID(t, h.conn, "agent.steer", map[string]any{"runId": res.RunID, "text": message}, 2)
	var steered struct {
		Result agentSteerResponse `json:"result"`
		Error  *jsonrpcErrorObj   `json:"error"`
	}
	if err := json.Unmarshal(steerResponse, &steered); err != nil || steered.Error != nil {
		t.Fatalf("agent.steer: %v %+v", err, steered.Error)
	}
	if steered.Result.Outcome != steerSteered {
		t.Fatalf("agent.steer = %+v, want the agent to have taken the message", steered.Result)
	}

	raw, notifications := cancelCallPreservingNotifications(t, h.conn, res.RunID, 3)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode agent.cancel: %v\nraw: %s", err, raw)
	}
	if env.Error != nil {
		t.Fatalf("agent.cancel: %+v", env.Error)
	}
	validateJSON(t, loadSchema(t, "agent.cancel.schema.json"), env.Result, "agent.cancel result (real socket)")
	var cancelled agentCancelResponse
	if err := json.Unmarshal(env.Result, &cancelled); err != nil {
		t.Fatalf("decode agent.cancel result: %v", err)
	}
	if len(cancelled.DroppedSteers) != 1 || cancelled.DroppedSteers[0] != message {
		t.Fatalf("agent.cancel droppedSteers = %v, want exactly the message the agent never used", cancelled.DroppedSteers)
	}

	// The notification says the same thing, because the block is drawn from it
	// when it arrives first.
	var state agentRunState
	found := false
	for _, notification := range notifications {
		var candidate agentRunState
		if json.Unmarshal(notification, &candidate) == nil && candidate.State == string(content.RunCancelled) {
			state, found = candidate, true
		}
	}
	if !found {
		state = cancelledRunState(t, h)
	}
	if len(state.DroppedSteers) != 1 || state.DroppedSteers[0] != message {
		t.Fatalf("runState droppedSteers = %v, want the same message", state.DroppedSteers)
	}
}

// cancelledRunState reads the runState notification a test has not already seen.
func cancelledRunState(t *testing.T, h *askHarness) agentRunState {
	t.Helper()
	raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	var state agentRunState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("agent.runState: %v", err)
	}
	return state
}

// The person's message is part of the turn's RECORD, in order with the answer's
// runs: the ledger is the record of what is shown (ADR-0019), so a restored
// block draws the message where it was sent instead of an answer with a gap in
// it (nocx-a62cl.6.4).
func TestAgentSteer_TheMessageIsWrittenIntoTheTurnsRecord(t *testing.T) {
	h := newAskHarness(t, steerScript())
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "steer-ask-record", "sessionId": sid, "question": "what is in the log?", "cwd": "/repo",
	}, 1)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	h.waitHung()
	const message = "also check the second log"
	steerResponse := jsonrpcCallWithID(t, h.conn, "agent.steer", map[string]any{"runId": res.RunID, "text": message}, 2)
	var steered struct {
		Result agentSteerResponse `json:"result"`
		Error  *jsonrpcErrorObj   `json:"error"`
	}
	if err := json.Unmarshal(steerResponse, &steered); err != nil || steered.Error != nil {
		t.Fatalf("agent.steer: %v %+v", err, steered.Error)
	}
	if steered.Result.Outcome != steerSteered {
		t.Fatalf("agent.steer = %+v, want the agent to have taken the message", steered.Result)
	}
	_ = jsonrpcCallWithID(t, h.conn, "agent.cancel", map[string]any{"runId": res.RunID}, 3)

	// THE TURN'S CHILDREN, in seat order: the answer's first run of prose, the
	// person's message, and nothing else invented.
	kids, err := h.db.Ledger().Caused(t.Context(), res.EntryID)
	if err != nil {
		t.Fatalf("Caused: %v", err)
	}
	if len(kids) != 2 {
		t.Fatalf("the turn's children = %+v, want the prose and the message", kids)
	}
	if kids[0].Kind != content.EntryText || kids[0].Source != content.SourceAssistant || kids[0].Position != 0 {
		t.Fatalf("seat 0 = %+v, want the answer's prose", kids[0])
	}
	if kids[1].Kind != content.EntryText || kids[1].Source != content.SourceUser ||
		kids[1].Position != 1 || kids[1].Intent != "" {
		t.Fatalf("seat 1 = %+v, want the person's message as text at the next seat", kids[1])
	}
	// Its words are its own body, complete.
	entry, err := h.db.Ledger().Entry(t.Context(), kids[1].EntryID)
	if err != nil || entry == nil || len(entry.Artifacts) != 1 {
		t.Fatalf("the message's row = %+v (%v)", entry, err)
	}
	body, err := h.db.Ledger().Artifact(t.Context(), entry.Artifacts[0].ID)
	if err != nil || body == nil {
		t.Fatalf("Artifact: %v", err)
	}
	var said strings.Builder
	for _, chunk := range body.Chunks {
		said.Write(chunk)
	}
	if said.String() != message {
		t.Fatalf("the message's body = %q, want %q", said.String(), message)
	}

	// AND THE RESTORE READS IT: ledger.get's caused carries the child's kind
	// and source, which is the pair the renderer draws the line from.
	got := vaultCall(t, h.conn, "ledger.get", map[string]any{"id": res.EntryID}, 4)
	if got.Error != nil {
		t.Fatalf("ledger.get: %+v", got.Error)
	}
	validateJSON(t, loadSchema(t, "ledger.get.schema.json"), got.Result, "ledger.get result (a turn with a message)")
	var restored struct {
		Caused []struct {
			EntryID string `json:"entryId"`
			Kind    string `json:"kind"`
			Source  string `json:"source"`
		} `json:"caused"`
	}
	if err := json.Unmarshal(got.Result, &restored); err != nil {
		t.Fatalf("decode ledger.get: %v", err)
	}
	if len(restored.Caused) != 2 || restored.Caused[1].Kind != "text" || restored.Caused[1].Source != "user" {
		t.Fatalf("restored caused = %+v, want the person's text child second", restored.Caused)
	}
}

// I2 — THE CUTOFF IS TAKEN WITH THE SETTLEMENT, NOT AFTER IT (nocx-a62cl.6.2).
//
// The answer means "the calls that were queued behind this one when I was
// given". That set is knowable only at the moment the wait settles, so it is
// read inside the same lock that decides the settlement; a reading taken when
// the pipeline gets round to it has moved, and calls that arrived in between
// would be refused for something the person had not yet seen.
func TestTheAnswerWaitStampsTheArrivalCutoffAsItSettles(t *testing.T) {
	ctx := t.Context()
	wait := newAnswerWait(notify.NewManualClock(), "session.read", answerLimit)
	wait.begin()
	defer wait.stop()

	arrivals := uint64(3)
	wait.cutoff = func() uint64 { return arrivals }
	// The calls arriving between the settlement and the pipeline reading it
	// must not be covered: the cutoff was fixed when the wait settled.
	if !wait.settleLocked(assistant.Declined{Reason: "use the other log"}) {
		t.Fatal("the settlement was refused")
	}
	arrivals = 9
	answer, ok := wait.result(ctx).(assistant.Declined)
	if !ok {
		t.Fatalf("the answer = %T, want a decline", wait.result(ctx))
	}
	if answer.Through != 3 {
		t.Fatalf("the decline covers %d arrivals, want the 3 that existed when it settled", answer.Through)
	}
	if answer.Reason != "use the other log" {
		t.Fatalf("reason = %q", answer.Reason)
	}
}
