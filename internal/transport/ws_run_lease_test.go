package transport

// The run lease over a REAL session (ADR-0020 decision 2): a command that
// never finishes is terminalized by its deadline — wall-clock or inactivity,
// and the LEDGER says which — cancellation escalates INT → TERM → KILL
// against the execution's process group so it reaches a child, the output
// budget bounds what one execution produces and the block names the bound,
// and an ordinary short command runs to completion untouched. And the
// awaiting-takeover transition (decision 3): a lane that took the alternate
// screen refuses new runs while reads keep working.
//
// These tests stand in for the renderer exactly as the run wire tests do —
// receive agent.runRequest, SUBMIT the command into the real session (the
// data plane, the same path a person's Enter takes), and resolve only when
// the block would freeze — except here the command is a real process the
// lease can actually kill. A test that never submits would only prove the
// broker timeout, which is the gap this bead closes.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/file"
	"golang.org/x/sys/unix"
)

// ── socket tap: ONE reader for the whole test ──────────────────────────────

// socketTap drains the connection once and classifies every message:
// binary frames into data, text messages into msgs. A test that needs both
// the data plane (the command's output) and the control plane (the run
// notifications) cannot use readNotification/jsonrpcCall — gorilla allows
// exactly one concurrent reader — so the tap owns the socket and the test
// consumes the two channels.
type socketTap struct {
	data chan Frame
	msgs chan json.RawMessage
}

func newSocketTap(conn *websocket.Conn) *socketTap {
	t := &socketTap{
		data: make(chan Frame, 8192),
		msgs: make(chan json.RawMessage, 4096),
	}
	go func() {
		defer close(t.data)
		defer close(t.msgs)
		for {
			mt, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if mt == websocket.BinaryMessage {
				if f, derr := DecodeFrame(payload); derr == nil {
					t.data <- f
				}
				continue
			}
			t.msgs <- payload
		}
	}()
	return t
}

// tapCall sends one JSON-RPC request over the tap and waits for the
// response carrying the same id (notifications pass through untouched).
func tapCall(t *testing.T, conn *websocket.Conn, tap *socketTap, id int, method string, params any) json.RawMessage {
	t.Helper()
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		t.Fatalf("marshal %s: %v", method, err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
	want := strconv.Itoa(id)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case msg, ok := <-tap.msgs:
			if !ok {
				t.Fatalf("socket closed before %s answered", method)
			}
			var env struct {
				ID *json.RawMessage `json:"id"`
			}
			if json.Unmarshal(msg, &env) != nil || env.ID == nil || string(*env.ID) != want {
				continue // a notification, or another call's response
			}
			return msg
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("%s never answered", method)
	return nil
}

// tapNotify waits for the next notification with the method and returns its
// params. Earlier notifications of other methods are skipped.
func tapNotify(t *testing.T, tap *socketTap, method string, timeout time.Duration) json.RawMessage {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case msg, ok := <-tap.msgs:
			if !ok {
				t.Fatalf("socket closed before %s arrived", method)
			}
			var n struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(msg, &n) != nil || n.Method != method {
				continue
			}
			return n.Params
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("no %s notification within %v", method, timeout)
	return nil
}

// tapDataFor waits until the session's data plane has carried needle and
// returns everything collected for the session so far.
func tapDataFor(t *testing.T, tap *socketTap, sid, needle string, timeout time.Duration) string {
	t.Helper()
	var buf strings.Builder
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case f, ok := <-tap.data:
			if !ok {
				t.Fatalf("socket closed before the output contained %q", needle)
			}
			if string(session.IDFromBytes(f.SessionID)) != sid {
				continue
			}
			buf.Write(f.Payload)
			if strings.Contains(buf.String(), needle) {
				return buf.String()
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("the session's output never contained %q; got %q", needle, buf.String())
	return ""
}

// submitCommand writes command into the session the way the renderer's
// submit path does: the line plus Enter, over the data plane.
func submitCommand(t *testing.T, conn *websocket.Conn, sid, command string) {
	t.Helper()
	sidBytes, err := session.IDToBytes(session.ID(sid))
	if err != nil {
		t.Fatalf("id to bytes: %v", err)
	}
	f := Frame{Version: FrameVersion, MsgType: MsgTypeData, SessionID: sidBytes, Payload: []byte(command + "\r")}
	if err := conn.WriteMessage(websocket.BinaryMessage, f.Encode()); err != nil {
		t.Fatalf("write command: %v", err)
	}
}

// ── harness: the ask flow over REAL sessions with a named lease ───────────

// runLeaseHarness is the ask harness (ws_agent_ask_test.go) with two
// deliberate differences: the session registry spawns REAL local shells (a
// stub pty has no process for the lease to kill), and the server runs under
// the named lease config. The content store is real — the ledger assertion
// (which bound ended the run) is read back from it.
type runLeaseHarness struct {
	t    *testing.T
	ws   *WSServer
	conn *websocket.Conn
	db   content.ContentDB
	// fake is the run-tool provider: first request streams the run tool
	// call, later requests stream the answer.
	fake *runToolCallingServer
	srv  *testSrv
}

// testSrv is the fake provider's server handle (URL + Close), so the
// harness can name the endpoint after construction.
type testSrv struct {
	url string
}

func (s *testSrv) URL() string  { return s.url }
func (s *testSrv) Close() error { return nil }

func newRunLeaseHarness(t *testing.T, leaseCfg RunLeaseConfig) *runLeaseHarness {
	t.Helper()
	fake, srv := newRunToolCallingServer("")
	t.Cleanup(srv.Close)
	client, err := assistant.NewClient(nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}

	dir := t.TempDir()
	docStore := storage.NewDocumentStore(dir)
	reg, err := vault.NewRegistry(file.New(docStore, "vault-blob.json"))
	if err != nil {
		t.Fatalf("vault.NewRegistry: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(nil, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := vault.New(docStore, reg, logger)
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	t.Cleanup(v.Close)
	if _, setupErr := v.Setup(t.Context(), vault.SetupRequest{Passphrase: "test"}); setupErr != nil {
		t.Fatalf("vault Setup: %v", setupErr)
	}
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	db, err := content.Open(t.Context(), content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	askPaneIn(t, db)

	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithReal(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(v), WithVaultLifecycle(v),
		WithAgentKnownMaterial(adapterKnownMaterial(v)),
		WithContentDB(db),
		WithAssistantClient(client),
		WithAssistantProbeStore(assistant.NewProbeStore()),
		WithAgentPolicy(autonomousPolicyStore(t)),
		WithRunLease(leaseCfg),
	)
	if err := ws.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(t.Context()) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return &runLeaseHarness{t: t, ws: ws, conn: conn, db: db, fake: fake, srv: &testSrv{url: srv.URL}}
}

// createEndpointAt makes one answering-role endpoint against the fake
// provider (the same shape the run wire tests use).
func (h *runLeaseHarness) createEndpointAt() {
	h.t.Helper()
	e, code := decodeEndpointResult(h.t, jsonrpcCall(h.t, h.conn, "endpoints.create", map[string]any{
		"name":    "Local",
		"baseUrl": h.srv.url,
		"schema":  "openai-compatible",
		"key":     "sk-test-123",
		"models":  []map[string]any{{"name": "qwen3"}},
	}))
	if code != 0 {
		h.t.Fatalf("endpoints.create: code %d", code)
	}
	if isErrorResponse(h.t, jsonrpcCall(h.t, h.conn, "roles.assign", map[string]any{
		"role":       "answering",
		"endpointId": e.ID,
		"model":      "qwen3",
	})) {
		h.t.Fatalf("roles.assign refused")
	}
}

// askRunsTool drives one ask whose first model response calls the run tool
// with the given session and command, and returns the ask result.
func (h *runLeaseHarness) askRunsTool(sid, command string) askWireResult {
	h.t.Helper()
	h.fake.args = `{"sessionId":` + strconv.Quote(sid) + `,"command":` + strconv.Quote(command) + `}`
	res, errObj := askOverWire(h.t, h.conn, map[string]any{
		"askId":     "ask-lease-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		"sessionId": sid,
		"question":  "run it",
		"cwd":       "/repo",
	}, 7)
	if errObj != nil {
		h.t.Fatalf("ask: %+v", errObj)
	}
	if res.State != "prepared" {
		h.t.Fatalf("ask state = %q, want prepared", res.State)
	}
	return res
}

// runToolActionEntry finds the run tool's action entry (the audit row the
// middleware opens for the tool call) and returns it — the row whose
// execution's TerminationReason says which lease bound ended the run.
func runToolActionEntry(t *testing.T, led content.LedgerRepository) *content.LedgerEntry {
	t.Helper()
	summaries, err := led.ListEntries(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	for _, s := range summaries {
		if s.Kind == content.EntryAction && s.Intent == "run" {
			e, err := led.Entry(context.Background(), s.ID)
			if err != nil {
				t.Fatalf("Entry(%s): %v", s.ID, err)
			}
			return e
		}
	}
	t.Fatalf("no run tool action entry in the ledger (summaries=%d)", len(summaries))
	return nil
}

// terminationReasonOfRun reads the ledger row for the run tool's action
// entry and returns its execution's termination reason.
func terminationReasonOfRun(t *testing.T, h *runLeaseHarness) *content.TerminationReason {
	t.Helper()
	e := runToolActionEntry(t, h.db.Ledger())
	if len(e.Executions) != 1 {
		t.Fatalf("run action executions = %d, want exactly one", len(e.Executions))
	}
	return e.Executions[0].TerminationReason
}

// waitChildDead polls pid until the process is gone (ESRCH) — the
// observable of the escalation having reached it.
func waitChildDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := unix.Kill(pid, 0); errors.Is(err, unix.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child %d survived the lease's cancellation", pid)
}

// waitChildAlive asserts the child is STILL running — the negative half of
// "untouched": the execution exists before the lease is expected to act,
// and a completing run's process must not have been signaled.
func waitChildAlive(t *testing.T, pid int) {
	t.Helper()
	if err := unix.Kill(pid, 0); err != nil {
		t.Fatalf("child %d is not running: %v", pid, err)
	}
}

// readPidFile reads one integer from the command's own pid file, polling
// until it exists — the command writes it a moment after the data frame
// reaches the shell, and the file's appearance is the observable that the
// execution actually started (there is something for the lease to cancel).
func readPidFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path) //nolint:gosec // the pid file is the test's own temp file, written by the command under test
		if err == nil {
			pid, perr := strconv.Atoi(strings.TrimSpace(string(b)))
			if perr == nil && pid > 0 {
				return pid
			}
			t.Fatalf("pid file holds %q", strings.TrimSpace(string(b)))
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the command never wrote its pid file")
	return 0
}

// waitForRunState scans the tap until the run terminalizes with the wanted
// state and returns the run id and the failure sentence.
func waitForRunState(t *testing.T, tap *socketTap, wantState string) (runID int64, errSentence string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		raw := tapNotify(t, tap, "agent.runState", 30*time.Second)
		var st struct {
			RunID int64  `json:"runId"`
			State string `json:"state"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(raw, &st); err != nil {
			t.Fatalf("runState unmarshal: %v\nraw: %s", err, raw)
		}
		if st.State == wantState {
			return st.RunID, st.Error
		}
	}
	t.Fatalf("the run never reached %s", wantState)
	return 0, ""
}

// runRequestParams decodes the agent.runRequest notification.
func decodeRunRequest(t *testing.T, raw json.RawMessage) (rid, sid, command string) {
	t.Helper()
	var req struct {
		RequestID string `json:"requestId"`
		SessionID string `json:"sessionId"`
		Command   string `json:"command"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("runRequest unmarshal: %v\nraw: %s", err, raw)
	}
	if req.RequestID == "" || req.SessionID == "" {
		t.Fatalf("runRequest missing identity: %s", raw)
	}
	return req.RequestID, req.SessionID, req.Command
}

// ── criterion 1 (wall-clock) + criterion 2 (a child, not only the shell) ──

// A command that never finishes is terminalized by its wall-clock deadline;
// the LEDGER says the wall-clock ended it; and the cancellation reached the
// execution's child (the pid the command wrote is gone), not only the shell.
func TestRunLease_WallClockTerminalizesAndTheLedgerNamesIt(t *testing.T) {
	h := newRunLeaseHarness(t, RunLeaseConfig{
		WallClock:   1500 * time.Millisecond,
		Inactivity:  30 * time.Second, // cannot explain the outcome
		SignalGrace: 200 * time.Millisecond,
	})
	h.createEndpointAt()
	sid := openLocalSession(t, h.conn)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	res := h.askRunsTool(sid, "sh -c 'echo $$ > "+pidFile+"; exec sleep 100'")
	tap := newSocketTap(h.conn)

	// The renderer half: the run request arrives, the command is submitted
	// into the real session, and never resolves — the block never freezes.
	raw := tapNotify(t, tap, "agent.runRequest", 10*time.Second)
	_, wantSid, _ := decodeRunRequest(t, raw)
	if wantSid != sid {
		t.Fatalf("runRequest session = %q, want %q", wantSid, sid)
	}
	submitCommand(t, h.conn, sid, "sh -c 'echo $$ > "+pidFile+"; exec sleep 100'")
	pid := readPidFile(t, pidFile)
	waitChildAlive(t, pid) // the child exists and runs: there is something to cancel

	// The lease fires (wall-clock), kills the execution, terminalizes the
	// run. The observable is the terminal runState.
	runID, sentence := waitForRunState(t, tap, "failed")
	if runID != res.RunID {
		t.Fatalf("runState runId = %d, want %d", runID, res.RunID)
	}
	if !strings.Contains(sentence, "wall-clock") {
		t.Fatalf("runState error = %q, want the wall-clock deadline named", sentence)
	}

	// Criterion 2: the cancellation reached the CHILD — the pid the command
	// wrote is dead. A signal aimed only at the shell would have left it.
	waitChildDead(t, pid)

	// Criterion 1, the ledger half: the run tool's execution says the
	// wall-clock bound ended it — the inactivity bound cannot explain it.
	reason := terminationReasonOfRun(t, h)
	if reason == nil || *reason != content.TermTimeout {
		t.Fatalf("ledger termination = %v, want timeout — the ledger must say which deadline ended the run", reason)
	}
}

// ── criterion 1 (inactivity) ───────────────────────────────────────────────

// A silent command — no output for the inactivity bound — is terminalized
// for INACTIVITY, and the ledger says so. The wall-clock is far longer, so
// only the silence can explain the outcome.
func TestRunLease_InactivityTerminalizesAndTheLedgerNamesIt(t *testing.T) {
	h := newRunLeaseHarness(t, RunLeaseConfig{
		WallClock:   60 * time.Second, // cannot explain the outcome
		Inactivity:  1500 * time.Millisecond,
		SignalGrace: 200 * time.Millisecond,
	})
	h.createEndpointAt()
	sid := openLocalSession(t, h.conn)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := "sh -c 'echo $$ > " + pidFile + "; exec sleep 100'"
	res := h.askRunsTool(sid, cmd)
	tap := newSocketTap(h.conn)

	raw := tapNotify(t, tap, "agent.runRequest", 10*time.Second)
	_, wantSid, _ := decodeRunRequest(t, raw)
	if wantSid != sid {
		t.Fatalf("runRequest session = %q, want %q", wantSid, sid)
	}
	submitCommand(t, h.conn, sid, cmd)
	pid := readPidFile(t, pidFile)
	waitChildAlive(t, pid)

	// The echo above broke the silence once; then nothing for the bound.
	runID, sentence := waitForRunState(t, tap, "failed")
	if runID != res.RunID {
		t.Fatalf("runState runId = %d, want %d", runID, res.RunID)
	}
	if !strings.Contains(sentence, "inactivity") {
		t.Fatalf("runState error = %q, want the inactivity bound named", sentence)
	}
	waitChildDead(t, pid)

	reason := terminationReasonOfRun(t, h)
	if reason == nil || *reason != content.TermInactivity {
		t.Fatalf("ledger termination = %v, want inactivity — silence is a different failure from slowness", reason)
	}
}

func TestRunLease_ShortCommandRunsUntouched(t *testing.T) {
	h := newRunLeaseHarness(t, RunLeaseConfig{
		WallClock:    30 * time.Second,
		Inactivity:   30 * time.Second,
		OutputBudget: 1 << 20,
		SignalGrace:  200 * time.Millisecond,
	})
	h.createEndpointAt()
	sid := openLocalSession(t, h.conn)
	// An ordinary command that COMPLETES on its own — every lease bound is
	// far beyond it, so none can explain anything that happens to it.
	cmd := "sh -c 'echo done'"
	res := h.askRunsTool(sid, cmd)
	tap := newSocketTap(h.conn)

	raw := tapNotify(t, tap, "agent.runRequest", 10*time.Second)
	rid, _, _ := decodeRunRequest(t, raw)
	submitCommand(t, h.conn, sid, cmd)

	// The renderer's observable: the command's own output appeared. The
	// block froze; the renderer resolves with the completed run body.
	tapDataFor(t, tap, sid, "done", 15*time.Second)
	reply := tapCall(t, h.conn, tap, 41, "agent.runResolved", runResolvedWire(rid, "entry-lease-ok", 0, "success", 1, 0, 1, "done"))
	var rerr struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(reply, &rerr); err != nil {
		t.Fatalf("resolution response unmarshal: %v", err)
	}
	if rerr.Error != nil {
		t.Fatalf("runResolved refused: %+v", rerr.Error)
	}

	// The run completes — no deadline fired, nothing was bounded — and the
	// ledger agrees: the execution's reason is a plain completed.
	runID, sentence := waitForRunState(t, tap, "completed")
	if runID != res.RunID {
		t.Fatalf("runState runId = %d, want %d", runID, res.RunID)
	}
	if sentence != "" {
		t.Fatalf("completed runState carries an error: %q", sentence)
	}
	reason := terminationReasonOfRun(t, h)
	if reason == nil || *reason != content.TermCompleted {
		t.Fatalf("ledger termination = %v, want completed — nothing about this run was bounded", reason)
	}

	// The session is untouched: the shell still answers a second command —
	// a lease that had signaled anything would have disturbed it.
	submitCommand(t, h.conn, sid, "echo still-alive")
	tapDataFor(t, tap, sid, "still-alive", 15*time.Second)
}

// A process that IGNORES INT dies only when the escalation reaches TERM:
// the death of the child proves the lease escalated past the ignored
// signal — a lease that only sent INT would have left it alive.
func TestRunLease_EscalationReachesTermForAnIntIgnoringProcess(t *testing.T) {
	h := newRunLeaseHarness(t, RunLeaseConfig{
		WallClock:   1500 * time.Millisecond,
		SignalGrace: 300 * time.Millisecond,
	})
	h.createEndpointAt()
	sid := openLocalSession(t, h.conn)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := "sh -c 'echo $$ > " + pidFile + "; trap \"\" INT; sleep 100 & wait'"
	res := h.askRunsTool(sid, cmd)
	tap := newSocketTap(h.conn)

	raw := tapNotify(t, tap, "agent.runRequest", 10*time.Second)
	_, wantSid, _ := decodeRunRequest(t, raw)
	if wantSid != sid {
		t.Fatalf("runRequest session = %q, want %q", wantSid, sid)
	}
	submitCommand(t, h.conn, sid, cmd)
	pid := readPidFile(t, pidFile)
	waitChildAlive(t, pid)

	runID, _ := waitForRunState(t, tap, "failed")
	if runID != res.RunID {
		t.Fatalf("runState runId = %d, want %d", runID, res.RunID)
	}
	// SIGINT alone could not have ended this child (it ignores INT): its
	// death is the proof the escalation reached TERM.
	waitChildDead(t, pid)
}

// A process that ignores BOTH INT and TERM dies only on KILL: the death of
// the child proves the escalation ran the whole INT → TERM → KILL ladder.
func TestRunLease_EscalationReachesKillForAnIntAndTermIgnoringProcess(t *testing.T) {
	h := newRunLeaseHarness(t, RunLeaseConfig{
		WallClock:   1500 * time.Millisecond,
		SignalGrace: 300 * time.Millisecond,
	})
	h.createEndpointAt()
	sid := openLocalSession(t, h.conn)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := "sh -c 'echo $$ > " + pidFile + "; trap \"\" INT TERM; sleep 100 & wait'"
	res := h.askRunsTool(sid, cmd)
	tap := newSocketTap(h.conn)

	raw := tapNotify(t, tap, "agent.runRequest", 10*time.Second)
	_, wantSid, _ := decodeRunRequest(t, raw)
	if wantSid != sid {
		t.Fatalf("runRequest session = %q, want %q", wantSid, sid)
	}
	submitCommand(t, h.conn, sid, cmd)
	pid := readPidFile(t, pidFile)
	waitChildAlive(t, pid)

	runID, _ := waitForRunState(t, tap, "failed")
	if runID != res.RunID {
		t.Fatalf("runState runId = %d, want %d", runID, res.RunID)
	}
	// Neither INT nor TERM could have ended this child: only KILL remains,
	// and KILL cannot be ignored.
	waitChildDead(t, pid)
}

// ── criterion 4: the output budget ─────────────────────────────────────────

// An execution that floods output is terminalized by the budget, and the
// block says it was BOUNDED — a silent truncation is the defect, the
// visible bound is the feature.
func TestRunLease_OutputBudgetBoundsAndTheBlockNamesIt(t *testing.T) {
	h := newRunLeaseHarness(t, RunLeaseConfig{
		WallClock:    60 * time.Second, // cannot explain the outcome
		OutputBudget: 512,
		SignalGrace:  200 * time.Millisecond,
	})
	h.createEndpointAt()
	sid := openLocalSession(t, h.conn)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	// 2 KiB of output (past the 512-byte budget), then it waits — so the
	// budget, not the wall clock or the command's own exit, ends the run.
	cmd := "sh -c 'echo $$ > " + pidFile + "; dd if=/dev/zero bs=256 count=8 2>/dev/null; sleep 100'"
	res := h.askRunsTool(sid, cmd)
	tap := newSocketTap(h.conn)

	raw := tapNotify(t, tap, "agent.runRequest", 10*time.Second)
	_, wantSid, _ := decodeRunRequest(t, raw)
	if wantSid != sid {
		t.Fatalf("runRequest session = %q, want %q", wantSid, sid)
	}
	submitCommand(t, h.conn, sid, cmd)
	pid := readPidFile(t, pidFile)

	// No waitChildAlive here on purpose: the command's own output IS the
	// thing being bounded, and under -race the budget can fire — and the
	// escalation kill the child — before the test's next assertion runs.
	// The observable is the runState; the child's death is asserted after
	// it, below.

	runID, sentence := waitForRunState(t, tap, "failed")
	if runID != res.RunID {
		t.Fatalf("runState runId = %d, want %d", runID, res.RunID)
	}
	if !strings.Contains(sentence, "budget") {
		t.Fatalf("runState error = %q, want the output budget named — a visible bound is the feature", sentence)
	}
	waitChildDead(t, pid)

	reason := terminationReasonOfRun(t, h)
	if reason == nil || *reason != content.TermOutputBudget {
		t.Fatalf("ledger termination = %v, want output-budget", reason)
	}
}
