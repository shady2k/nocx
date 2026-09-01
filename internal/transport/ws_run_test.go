package transport

// run end to end (nocx-tjppv): the headline tool over a REAL socket,
// through the REAL engine. The grant permits run on a live session (the
// harness names the workspace policy preset on the server — the run's
// authority, ADR-0020 decision 5); the model calls the tool; the backend
// asks the renderer through the broker; the renderer (this test, standing
// in for the frontend) answers with the completed run body; and the
// output's text lands in the model's tool message on the SECOND request
// the engine sends. The wire is a party to the contract: every assertion
// is on bytes that actually crossed the socket or the fake provider.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

// runToolCallingServer is the fake provider for the run wire tests: the
// first request carries the run tool call (args supplied by the test, so
// one server shape serves the granted and the refused runs); later
// requests stream the answer "ok".
type runToolCallingServer struct {
	requests atomic.Int64
	bodies   []string
	args     string
}

func newRunToolCallingServer(args string) (*runToolCallingServer, *httptest.Server) {
	s := &runToolCallingServer{args: args}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n := s.requests.Add(1)
		s.bodies = append(s.bodies, string(body))
		if n == 1 {
			streamToolCallChunk(w, "session.run", s.args)
			return
		}
		streamOKChunks(w)
	}))
	return s, srv
}

// runResolvedWire is the renderer's answer to a run request: the completed
// run body — entry id, exit status, output window — under the closed
// "completed" outcome, in the exact wire shape validateRunResolvedRaw
// checks.
func runResolvedWire(rid, entryID string, exitCode int, status string, total, start, end int, text string) map[string]any {
	return map[string]any{
		"requestId": rid,
		"outcome":   "completed",
		"entryId":   entryID,
		"exitCode":  exitCode,
		"status":    status,
		"stopped":   false,
		"total":     total,
		"start":     start,
		"end":       end,
		"text":      text,
	}
}

// A completed resolution may name NO entry, and the ingress must let it
// through (nocx-9sqii).
//
// The renderer answers with the id the STORE minted for the command — the
// only id the backend can join anything to, since the caused-by edge is a
// foreign key into entries. That id is minted when the record is written, so
// when the store writes no row (History is off, or the record was dropped)
// there is honestly none to send. Refusing the resolution there would leave
// the request pending until the broker's timeout and fail a command that had
// already run, over a relation that is an arrangement.
//
// The length bound is unchanged: an id that is present is still bounded.
func TestRunResolved_ACompletedOutcomeMayNameNoEntry(t *testing.T) {
	none := runResolvedWire("req-1", "", 0, "success", 1, 0, 1, "ok")
	raw, err := json.Marshal(none)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if msg := validateRunResolvedRaw(raw); msg != "" {
		t.Fatalf("a completed resolution naming no entry was refused: %s", msg)
	}
	missingStop := runResolvedWire("req-1", "", 0, "success", 1, 0, 1, "ok")
	delete(missingStop, "stopped")
	raw, err = json.Marshal(missingStop)
	if err != nil {
		t.Fatalf("marshal missing stopped: %v", err)
	}
	if msg := validateRunResolvedRaw(raw); msg == "" {
		t.Fatal("a completed resolution without the stopped fact was accepted")
	}
	// And the bound still bites: an id longer than the ledger's is refused
	// exactly as it was.
	long := runResolvedWire("req-1", strings.Repeat("x", maxIDRunes+1), 0, "success", 1, 0, 1, "ok")
	raw, err = json.Marshal(long)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if msg := validateRunResolvedRaw(raw); msg == "" {
		t.Fatal("an over-long entry id was accepted")
	}
}

// TestRun_ContextCancellationWithdrawsBeforeExecution proves the first
// protocol phase: cancellation addresses the broker request id, the renderer
// is told to withdraw it, and no command can create an execution artifact.
func TestRun_ContextCancellationWithdrawsBeforeExecution(t *testing.T) {
	client, _, err := assistant.NewClientAndRegistry(nil, nil, content.Floor{}, nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	h := newAskHarness(t, client)
	sid := openLocalSession(t, h.conn)
	pidFile := t.TempDir() + "/child.pid"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, reqErr := h.ws.RequestRun(ctx, sid, "printf %s $$ > "+pidFile)
		done <- reqErr
	}()
	request := readNotification(t, h.conn, "agent.runRequest", 10*time.Second)
	var req struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(request, &req); err != nil {
		t.Fatalf("runRequest unmarshal: %v", err)
	}
	if req.RequestID == "" {
		t.Fatal("runRequest carries no requestId")
	}

	cancel()
	withdrawal := readNotification(t, h.conn, "agent.runCancel", 10*time.Second)
	var withdrawn struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(withdrawal, &withdrawn); err != nil {
		t.Fatalf("runCancel unmarshal: %v", err)
	}
	if withdrawn.RequestID != req.RequestID {
		t.Fatalf("runCancel requestId = %q, want %q", withdrawn.RequestID, req.RequestID)
	}
	if reqErr := <-done; reqErr == nil || reqErr.Error() != "run: submission expired before execution" {
		t.Fatalf("RequestRun error = %v, want exact pre-execution sentence", reqErr)
	}
	if _, statErr := os.Stat(pidFile); !os.IsNotExist(statErr) {
		t.Fatalf("command created pid file after withdrawal: stat error %v", statErr)
	}
}

// TestRun_EndToEndOverTheRealSocket is criterion 1's backend half: a run
// whose grant permits the tool on a live session submits the command
// through the broker, the renderer's resolution crosses the socket, and the
// command's output lands in the model's tool message. The engine is real,
// the socket is real, the provider is fake; the renderer half is THIS test.
func TestRun_EndToEndOverTheRealSocket(t *testing.T) {
	fake, srv := newRunToolCallingServer("")
	defer srv.Close()

	client, _, err := assistant.NewClientAndRegistry(nil, nil, content.Floor{}, nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	h := newAskHarnessWithOpts(t, client, WithAgentPolicy(autonomousPolicyStore(t)))
	h.createEndpointAt(srv.URL)

	sid := openLocalSession(t, h.conn)
	fake.args = `{"command":"ls -la"}`

	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "ask-run-1",
		"sessionId": sid,
		"question":  "list the files",
		"cwd":       "/repo",
	}, 2)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	if res.State != "prepared" {
		t.Fatalf("ask state = %q, want prepared", res.State)
	}

	// The renderer half: the broker's request arrives on the socket, naming
	// the lane the grant permitted, with the command a person would type.
	raw := readNotification(t, h.conn, "agent.runRequest", 10*time.Second)
	if raw == nil {
		t.Fatalf("no runRequest within 10s; provider requests=%d bodies=%v", fake.requests.Load(), fake.bodies)
	}
	var req struct {
		RequestID string `json:"requestId"`
		SessionID string `json:"sessionId"`
		Command   string `json:"command"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("runRequest unmarshal: %v\nraw: %s", err, raw)
	}
	if req.RequestID == "" {
		t.Fatal("runRequest carries no requestId")
	}
	if req.SessionID != sid {
		t.Fatalf("runRequest sessionId = %q, want %q", req.SessionID, sid)
	}
	if req.Command != "ls -la" {
		t.Fatalf("runRequest command = %q, want the submitted command", req.Command)
	}

	// Answer with the completed run body, over the same socket: the
	// resolution RPC the broker correlates.
	reply := jsonrpcCall(t, h.conn, "agent.runResolved", runResolvedWire(req.RequestID, "entry-42", 0, "success", 2, 0, 2, "file1\nfile2"))
	var rerr struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(reply, &rerr); err != nil {
		t.Fatalf("resolution response unmarshal: %v", err)
	}
	if rerr.Error != nil {
		t.Fatalf("runResolved refused: %+v", rerr.Error)
	}

	// The engine streams the tool message (the output window) as a delta,
	// then re-asks the model, whose streamed "ok" is the answer. Collect
	// deltas until the run terminalizes.
	var answer string
	var st struct {
		RunID int64  `json:"runId"`
		State string `json:"state"`
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		raw = readNotification(t, h.conn, "agent.runDelta", 10*time.Second)
		var d struct {
			RunID int64  `json:"runId"`
			Text  string `json:"text"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("runDelta unmarshal: %v\nraw: %s", err, raw)
		}
		if d.RunID != res.RunID {
			t.Fatalf("runDelta runId = %d, want %d", d.RunID, res.RunID)
		}
		answer += d.Text
		if strings.Contains(answer, "ok") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("answer never arrived; collected %q", answer)
		}
	}
	raw = readNotification(t, h.conn, "agent.runState", 10*time.Second)
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("runState unmarshal: %v\nraw: %s", err, raw)
	}
	if st.RunID != res.RunID || st.State != "completed" {
		t.Fatalf("runState = runId %d state %q, want %d completed", st.RunID, st.State, res.RunID)
	}

	// The wire evidence: the engine's SECOND request carries the tool
	// message with the command's output — the output crossed the socket and
	// landed in the model's tool message (criterion 1's "the answer names
	// something that appeared in it").
	if fake.requests.Load() < 2 {
		t.Fatalf("provider received %d requests, want >= 2; bodies=%v", fake.requests.Load(), fake.bodies)
	}
	second := fake.bodies[1]
	if !strings.Contains(second, "file1") || !strings.Contains(second, "file2") {
		t.Fatalf("the tool message lacks the command's output; second body: %s", second)
	}
}
