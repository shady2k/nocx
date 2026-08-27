package transport

// A PROGRAM THAT STOPS FOR A PERSON'S ANSWER FINISHES AFTER THEY GIVE IT
// (nocx-d6gn4.8). The engine-level shape is asserted in internal/assistant
// (TestAsk_AParkedProgramContinuesWhereItStoppedAcrossTheApproval); nothing
// asserted it over the SOCKET, with the real engine, the real approval wire
// and the real broker — and that is where it was broken: the first live
// program that asked for anything died on the approval with "the connection
// was lost while the answer was streaming", because the parked continuation
// was cancelled between the ask that parked it and the approve that resumed
// it (2026-08-27).
//
// The transport's other approval tests drive a SCRIPTED engine, which cannot
// park anything, so no suite could see it. This one uses the real client, and
// the effect the program asks for is `run` — the InRenderer tool, resolved by
// this test standing in for the renderer, exactly as production does.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/storage"
)

// programCallingServer streams a sentence and then ONE run_program call on
// the first request, and the answer on every later one — the resume spends no
// model request (nocx-igu4y), so a second scripted proposal would be a new
// call.
//
// THE SENTENCE IS NOT DECORATION. A live model says what it is about to do
// before it does it, which opens a prose block — and the call that follows
// SEALS that block, a durable write through the driving ask's context. That
// seal is where the live failure landed: a program resumed after an approval
// sealed through the context of the ask that had parked it, which had long
// returned. A scripted model that goes straight to the tool call has no
// block to seal, never reaches the write, and cannot see the defect (it did
// not: this test passed against the broken build until the sentence was
// added).
func programCallingServer(source string) (*runToolCallingServer, *httptest.Server) {
	s := &runToolCallingServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		if s.requests.Add(1) == 1 {
			args, _ := json.Marshal(map[string]any{"source": source})
			streamProseThenToolCall(w, "let me look", "run_program", string(args))
			return
		}
		streamOKChunks(w)
	}))
	return s, srv
}

// streamProseThenToolCall is one response that says something and then calls
// a tool — the ordinary shape of a model turn, and the one the transport's
// prose boundary (ADR-0040) exists for.
func streamProseThenToolCall(w http.ResponseWriter, prose, name, args string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	send := func(delta map[string]any, finish string) {
		d := map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion.chunk", "created": 0,
			"model": "probe-model",
			"choices": []map[string]any{{
				"index": 0, "delta": delta, "finish_reason": finish,
			}},
		}
		b, _ := json.Marshal(d)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	}
	send(map[string]any{"role": "assistant", "content": prose}, "")
	send(map[string]any{
		"role": "assistant",
		"tool_calls": []map[string]any{{
			"id": "call_program", "type": "function",
			"function": map[string]any{"name": name, "arguments": args},
		}},
	}, "tool_calls")
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestAgentAsk_AProgramThatStopsForAnApprovalFinishesAfterIt(t *testing.T) {
	reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})
	client, err := assistant.NewClient(nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	h := newAskHarnessWithOpts(t, client,
		WithSettingsRegistry(reg), WithAgentPolicy(askPolicyStore(t)))
	sid := openLocalSession(t, h.conn)

	// The program a live model wrote, in the shape it wrote it: one effect
	// whose result is the answer.
	source := fmt.Sprintf("result = run(command = \"ls -la\", sessionId = %q)\nanswer(result[\"text\"])\n", sid)
	fake, srv := programCallingServer(source)
	t.Cleanup(srv.Close)
	h.createEndpointAt(srv.URL)

	if resp := jsonrpcCall(t, h.conn, "settings.set", map[string]any{
		"key": "assistant.carrier", "value": string(assistant.CarrierProgram),
	}); isErrorResponse(t, resp) {
		t.Fatalf("settings.set refused the carrier: %s", resp)
	}

	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "ask-program-approval",
		"sessionId": sid,
		"question":  "what is here?",
		"cwd":       "/repo",
	}, 3)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}

	// The program's effect escalates: the person is asked about the RUN the
	// program proposed, not about the program.
	raw := readNotification(t, h.conn, "agent.approvalRequested", 10*time.Second)
	if raw == nil {
		t.Fatalf("no approvalRequested within 10s; provider requests=%d", fake.requests.Load())
	}
	var ap approvalBinding
	if err := json.Unmarshal(raw, &ap); err != nil {
		t.Fatalf("approvalRequested unmarshal: %v\nraw: %s", err, raw)
	}
	if ap.RunID != strconv.FormatInt(res.RunID, 10) || ap.Tool != "run" {
		t.Fatalf("approvalRequested binding = %+v, want run %d tool run", ap, res.RunID)
	}

	got, errObj := approveOverWire(t, h.conn, map[string]any{
		"runId": ap.RunID, "attempt": ap.Attempt, "tool": ap.Tool,
		"callId": ap.CallID, "argHash": ap.ArgHash, "approved": true, "scope": "once",
	}, 4)
	if errObj != nil {
		t.Fatalf("agent.approve: %+v", errObj)
	}
	if got.State != "streaming" {
		t.Fatalf("approve state = %q, want streaming (the resume is in flight)", got.State)
	}

	// THE POINT: the approved effect reaches the renderer. A continuation
	// that died with the ask that parked it never gets here — the program
	// wakes to a cancelled context and the run fails as a lost connection.
	raw = readNotification(t, h.conn, "agent.runRequest", 10*time.Second)
	if raw == nil {
		t.Fatalf("the approved effect never reached the renderer; provider requests=%d", fake.requests.Load())
	}
	var req struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("runRequest unmarshal: %v\nraw: %s", err, raw)
	}
	reply := jsonrpcCall(t, h.conn, "agent.runResolved",
		runResolvedWire(req.RequestID, "entry-42", 0, "success", 2, 0, 2, "file1\nfile2"))
	if isErrorResponse(t, reply) {
		t.Fatalf("runResolved refused: %s", reply)
	}

	// And the run finishes: the program answered, the answer streamed, the
	// run closed completed.
	var answer string
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(answer, "ok") {
		raw = readNotification(t, h.conn, "agent.runDelta", 10*time.Second)
		if raw == nil {
			t.Fatalf("no delta; collected %q", answer)
		}
		var d struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("runDelta unmarshal: %v\nraw: %s", err, raw)
		}
		answer += d.Text
		if time.Now().After(deadline) {
			t.Fatalf("the answer never arrived; collected %q", answer)
		}
	}
	raw = readNotification(t, h.conn, "agent.runState", 10*time.Second)
	var st struct {
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("runState unmarshal: %v\nraw: %s", err, raw)
	}
	if st.State != "completed" {
		t.Fatalf("runState = %q (%s), want completed", st.State, st.Error)
	}
}
