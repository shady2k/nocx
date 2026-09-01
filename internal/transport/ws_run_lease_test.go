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
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/sys/unix"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/file"
	"github.com/shady2k/nocx/internal/waittest"
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

// socketClosedWhy is appended to every "socket closed" failure in this file,
// because that sentence names the messenger and never the cause. The server
// releases a client that sends nothing for heartbeatReadWindow (ws.go:2620,
// DefaultHeartbeatReadWindow = 30s), and the window is refreshed only by frames
// the CLIENT sends — a tap only reads, so any wait longer than the window kills
// the socket before the wait can reach its own deadline and print its own error.
// Four CI failures and three rounds of investigation were spent on the corpse
// before the deadline was found (nocx-a96sf).
const socketClosedWhy = " — the server closes a client that has sent nothing for " +
	"30s (heartbeatReadWindow); a wait longer than that is the first thing to check"

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
				t.Fatalf("socket closed before %s answered%s", method, socketClosedWhy)
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
				t.Fatalf("socket closed before %s arrived%s", method, socketClosedWhy)
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

// unwrap undoes the SOFT LINE WRAP a terminal writes when an echoed line
// reaches the right margin, so a literal can be looked for in what the shell
// echoed rather than in how the terminal drew it.
//
// The margin is not a property of what is being tested. These sessions open at
// 80 columns (openLocalSession) and the commands here embed t.TempDir(), into
// which Go interpolates the TEST NAME — so a command runs to about 115
// characters and the prompt in front of it adds another twenty-odd. The echo
// wraps, and at the wrap the terminal emits the last byte, a CR, and that same
// byte again: `...TerminalizesAnd...` reaches the socket as
// `...TerminalizesAn\rnd...`. The literal is then not in the stream at all.
//
// Which is why this decided the outcome by geometry. Where the wrap falls
// depends on the prompt width (the hostname is in it) and on the TMPDIR path
// length (the test name and a random number are in it), so the same code passed
// on a developer host, failed five of these tests in the CI container, and on
// the GitHub runner got past the echo and failed a later wait instead —
// imitating nocx-2h08, the CLOSED starvation bug whose signature is a 30s
// timeout under a different test name every run, and costing an investigation
// before the bytes were read (nocx-3n0f3).
//
// The rule is exactly one shape, and every observed failure matched it: the
// byte before the CR is the byte after it. Anything else — a real CRLF, a
// program's own carriage return — is left alone.
func unwrap(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\r' && i > 0 && i+1 < len(s) && s[i-1] == s[i+1] {
			i++ // drop the CR and the byte it repeated
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// The wrap rule, stated as the cases that produced it. Every line here is a
// byte sequence taken off a real socket during the run that found this
// (nocx-3n0f3), plus the two shapes that must survive untouched.
func TestUnwrapUndoesTheMarginAndLeavesEverythingElseAlone(t *testing.T) {
	for _, c := range []struct {
		name string
		raw  string
		want string
	}{
		{"wrap inside a word", "TerminalizesAn\rnd", "TerminalizesAnd"},
		{"wrap at a capital", "InactivityTerminalizesA\rAndTheLedger", "InactivityTerminalizesAndTheLedger"},
		{"wrap inside a path", "2>/dev/nul\rll", "2>/dev/null"},
		{"wrap inside a number", "sleep 10\r00", "sleep 100"},
		{"several wraps in one line", "Fo\ror an In\rnt", "For an Int"},
		// A carriage return whose neighbours differ is the terminal saying
		// something else, and it is none of this helper's business.
		{"a real CRLF is not a wrap", "done\r\nnext", "done\r\nnext"},
		{"a bare CR is not a wrap", "progress\rdone", "progress\rdone"},
		{"nothing to do", "plain output", "plain output"},
		{"empty", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := unwrap(c.raw); got != c.want {
				t.Fatalf("unwrap(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

// tapDataFor waits until the session's data plane has carried needle and
// returns everything collected for the session so far.
//
// The needle is looked for in the UNWRAPPED view and the RAW buffer is what is
// returned: the wait is about what the shell echoed, and a caller that wants to
// read the stream wants the bytes that actually arrived.
func tapDataFor(t *testing.T, tap *socketTap, sid, needle string, timeout time.Duration) string {
	t.Helper()
	var buf strings.Builder
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case f, ok := <-tap.data:
			if !ok {
				t.Fatalf("socket closed before the output contained %q%s", needle, socketClosedWhy)
			}
			if string(session.IDFromBytes(f.SessionID)) != sid {
				continue
			}
			buf.Write(f.Payload)
			if strings.Contains(unwrap(buf.String()), needle) {
				return buf.String()
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("the session's output never contained %q; got %q (unwrapped: %q)",
		needle, buf.String(), unwrap(buf.String()))
	return ""
}

// submitLeaseCommand follows the renderer's assistant path: lifecycle.submitAttempt
// completes before the command bytes enter the data plane. A blank requestID
// is the ordinary human-command path and deliberately skips the assistant
// lifecycle RPC.
func (h *runLeaseHarness) submitLeaseCommand(t *testing.T, tap *socketTap, sid, command, requestID string) string {
	t.Helper()
	attemptID := ""
	if requestID != "" {
		resp := tapCall(t, h.conn, tap, 41, "lifecycle.submitAttempt", map[string]string{
			"domain":    string(h.domain.Domain),
			"requestId": requestID,
			"command":   command,
			"cwd":       "/repo",
			"host":      "",
			"source":    "assistant",
		})
		var envelope struct {
			Error  *jsonrpcErrorObj `json:"error"`
			Result struct {
				ID string `json:"id"`
			} `json:"result"`
		}
		if err := json.Unmarshal(resp, &envelope); err != nil {
			t.Fatalf("lifecycle.submitAttempt: unmarshal: %v\nraw: %s", err, resp)
		}
		if envelope.Error != nil {
			t.Fatalf("lifecycle.submitAttempt: %+v", envelope.Error)
		}
		if envelope.Result.ID == "" {
			t.Fatal("lifecycle.submitAttempt returned no attempt id")
		}
		attemptID = envelope.Result.ID
	}
	sidBytes, err := session.IDToBytes(session.ID(sid))
	if err != nil {
		t.Fatalf("id to bytes: %v", err)
	}
	f := Frame{Version: FrameVersion, MsgType: MsgTypeData, SessionID: sidBytes, Payload: []byte(command + "\r")}
	if err := h.conn.WriteMessage(websocket.BinaryMessage, f.Encode()); err != nil {
		t.Fatalf("write command: %v", err)
	}
	if attemptID == "" {
		return attemptID
	}

	// OSC 133 C is represented by the authenticated lifecycle fact. It is
	// emitted after the shell echoes the submitted command, so the bind RPC
	// opens accounting after the echo and before command output.
	tapDataFor(t, tap, sid, command, 10*time.Second)
	attempt := lifecycle.AttemptID(attemptID)
	mustLifecycleIngest(t, h.pub, "T", lifecycleEnv("lane-run-lease", h.domain, 2, lifecycleStartEvt(&attempt, command)))
	resp := tapCall(t, h.conn, tap, 42, "ledger.bind", map[string]any{
		"envelope": map[string]any{
			"id":          attemptID,
			"sessionId":   sid,
			"cwd":         "/repo",
			"kind":        "shell",
			"intent":      command,
			"sensitivity": "normal",
			"clientSeq":   0,
			"attemptId":   attemptID,
		},
		"facts": map[string]any{},
	})
	var bind struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &bind); err != nil {
		t.Fatalf("ledger.bind: unmarshal: %v\nraw: %s", err, resp)
	}
	if bind.Error != nil {
		t.Fatalf("ledger.bind: %+v", bind.Error)
	}
	return attemptID
}

// submitCommand writes a human command into the session over the data plane.
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
	t      *testing.T
	ws     *WSServer
	conn   *websocket.Conn
	db     content.ContentDB
	pub    *lifecyclepub.Publisher
	domain lifecycle.DomainHandle
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
	client, err := assistant.NewClient(nil, nil, content.Floor{})
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

	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel)
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithReal(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(v), WithVaultLifecycle(v),
		WithAgentKnownMaterial(adapterKnownMaterial(v)),
		WithContentDB(db),
		WithAssistantClient(client),
		WithAssistantProbeStore(assistant.NewProbeStore()),
		WithAgentPolicy(autonomousPolicyStore(t)),
		WithLifecyclePublisher(pub),
		WithRunLease(leaseCfg),
	)
	pub.SetEmitter(ws)
	if err := ws.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(t.Context()) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return &runLeaseHarness{t: t, ws: ws, conn: conn, db: db, pub: pub, fake: fake, srv: &testSrv{url: srv.URL}}
}

func (h *runLeaseHarness) openSession(t *testing.T) string {
	t.Helper()
	sid := openLocalSession(t, h.conn)
	h.ws.RegisterIntegration(session.ID(sid), "/bin/bash", IntegrationStarting, ssh.ReasonNone)
	const lane = lifecycle.LaneID("lane-run-lease")
	h.ws.RegisterLifecycleLane(lane, session.ID(sid))
	if err := h.pub.BindTransport("T", noopPort{}); err != nil {
		t.Fatalf("BindTransport: %v", err)
	}
	domain, err := h.pub.RequestDomain(lane, nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	mustLifecycleIngest(t, h.pub, "T", lifecycleEnv(lane, domain, 1, lifecycleHelloEvt()))
	ackEstablishmentFrom(t, h.pub, lane, domain, h.conn)
	h.domain = domain
	return sid
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
	h.fake.args = `{"command":` + strconv.Quote(command) + `}`
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
		if s.Kind == content.EntryAction && s.Intent == "session.run" {
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
	waittest.WaitForTimeout(t, fmt.Sprintf("child %d to exit", pid), 10*time.Second, func() bool {
		return errors.Is(unix.Kill(pid, 0), unix.ESRCH)
	})
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
	var pid int
	waittest.WaitForTimeout(t, "the command to write its pid file", 10*time.Second, func() bool {
		b, err := os.ReadFile(path) //nolint:gosec // the pid file is the test's own temp file, written by the command under test
		if err != nil {
			return false
		}
		parsed, perr := strconv.Atoi(strings.TrimSpace(string(b)))
		if perr != nil || parsed <= 0 {
			t.Fatalf("pid file holds %q", strings.TrimSpace(string(b)))
		}
		pid = parsed
		return true
	})
	return pid
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

func waitForRunStateDetails(t *testing.T, tap *socketTap, wantState string) (int64, string, []string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		raw := tapNotify(t, tap, "agent.runState", 30*time.Second)
		var st struct {
			RunID         int64    `json:"runId"`
			State         string   `json:"state"`
			Error         string   `json:"error"`
			UnarmedBounds []string `json:"unarmedBounds"`
		}
		if err := json.Unmarshal(raw, &st); err != nil {
			t.Fatalf("runState unmarshal: %v\nraw: %s", err, raw)
		}
		if st.State == wantState {
			return st.RunID, st.Error, st.UnarmedBounds
		}
	}
	t.Fatalf("the run never reached %s", wantState)
	return 0, "", nil
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

// A lease can fire while the broker is still delivering the request. When
// delivery fails, no renderer could submit a command: runState must carry the
// submission-expired sentence and must not claim terminalization.
func TestRunLease_BoundBeforeBrokerDeliveryNamesExpiredSubmissionInRunState(t *testing.T) {
	h := newRunLeaseHarness(t, RunLeaseConfig{
		WallClock:   time.Minute,
		SignalGrace: 20 * time.Millisecond,
	})
	h.createEndpointAt()
	sid := h.openSession(t)

	deliverStarted := make(chan struct{})
	releaseDeliver := make(chan struct{})
	var deliverOnce sync.Once
	broker := NewBroker(
		func() []Conn { return []Conn{&harnessConn{}} },
		func(Conn, string, json.RawMessage) error {
			deliverOnce.Do(func() { close(deliverStarted) })
			<-releaseDeliver
			return errors.New("delivery held open for pre-delivery expiry")
		},
	)
	h.ws.broker = broker

	res := h.askRunsTool(sid, "echo never submitted")
	<-deliverStarted

	broker.mu.Lock()
	var lease *runLease
	for _, candidate := range broker.runLeases {
		lease = candidate
		break
	}
	broker.mu.Unlock()
	if lease == nil {
		t.Fatal("broker did not register the run lease before delivery")
	}
	lease.fire(content.TermTimeout)
	close(releaseDeliver)

	tap := newSocketTap(h.conn)
	raw := tapNotify(t, tap, "agent.runState", 10*time.Second)
	var state struct {
		RunID int64  `json:"runId"`
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("runState unmarshal: %v\nraw: %s", err, raw)
	}
	if state.RunID != res.RunID || state.State != "failed" {
		t.Fatalf("runState = %+v, want run %d failed", state, res.RunID)
	}
	if !strings.Contains(state.Error, "run submission expired before execution started") {
		t.Fatalf("runState error = %q, want the submission-expired sentence", state.Error)
	}
	if strings.Contains(state.Error, "terminalized") {
		t.Fatalf("runState error = %q, must not claim terminalization", state.Error)
	}
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
	sid := h.openSession(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	res := h.askRunsTool(sid, "sh -c 'echo $$ > "+pidFile+"; exec sleep 100'")
	tap := newSocketTap(h.conn)

	// The renderer half: the run request arrives, the command is submitted
	// into the real session, and never resolves — the block never freezes.
	raw := tapNotify(t, tap, "agent.runRequest", 10*time.Second)
	requestID, wantSid, _ := decodeRunRequest(t, raw)
	if wantSid != sid {
		t.Fatalf("runRequest session = %q, want %q", wantSid, sid)
	}
	h.submitLeaseCommand(t, tap, sid, "sh -c 'echo $$ > "+pidFile+"; exec sleep 100'", requestID)
	pid := readPidFile(t, pidFile)
	waitChildAlive(t, pid) // the child exists and runs: there is something to cancel

	// The lease fires (wall-clock), kills the execution, and returns a
	// product-authored tool result so the model can explain the outcome.
	runID, sentence := waitForRunState(t, tap, "completed")
	if runID != res.RunID {
		t.Fatalf("runState runId = %d, want %d", runID, res.RunID)
	}
	if sentence != "" {
		t.Fatalf("completed runState carries an error: %q", sentence)
	}
	if len(h.fake.bodies) < 2 || !strings.Contains(h.fake.bodies[1], "wall-clock deadline") {
		t.Fatalf("model request after lease = %v, want the wall-clock tool result", h.fake.bodies)
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

// A dropped renderer connection abandons its own live command. The lease owns
// that process until the request returns, so transport loss runs the same
// INT -> TERM -> KILL ladder rather than leaving an orphan for reconnect.
func TestRunLease_ConnectionLossKillsCommandAndRecordsTransportGone(t *testing.T) {
	h := newRunLeaseHarness(t, RunLeaseConfig{
		WallClock:   30 * time.Second,
		Inactivity:  30 * time.Second,
		SignalGrace: 50 * time.Millisecond,
	})
	h.createEndpointAt()
	sid := openLocalSession(t, h.conn)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	cmd := "sh -c 'echo $$ > " + pidFile + "; trap \"\" INT TERM; sleep 100 & wait'"
	res := h.askRunsTool(sid, cmd)
	tap := newSocketTap(h.conn)

	raw := tapNotify(t, tap, "agent.runRequest", 10*time.Second)
	_, wantSid, wantCommand := decodeRunRequest(t, raw)
	if wantSid != sid || wantCommand != cmd {
		t.Fatalf("runRequest = (%q, %q), want (%q, %q)", wantSid, wantCommand, sid, cmd)
	}
	submitCommand(t, h.conn, sid, cmd)
	pid := readPidFile(t, pidFile)
	waitChildAlive(t, pid)

	// The renderer disappears while this request owns the live command.
	_ = h.conn.Close()
	waitChildDead(t, pid)

	var entry *content.LedgerEntry
	var entryErr error
	waittest.WaitForTimeoutDetail(t, "the disconnected run to terminalize", 10*time.Second,
		func() string {
			return fmt.Sprintf("entry=%v err=%v", entry, entryErr)
		},
		func() bool {
			entry, entryErr = h.db.Ledger().Entry(context.Background(), res.EntryID)
			if entryErr != nil || entry == nil || len(entry.Executions) != 1 {
				return false
			}
			state := entry.Executions[0].State
			return state != nil && *state != content.RunPrepared && *state != content.RunStreaming
		})
	if entryErr != nil {
		t.Fatalf("question entry: %v", entryErr)
	}
	if entry.Executions[0].TerminationReason == nil ||
		*entry.Executions[0].TerminationReason != content.TermTransportGone {
		t.Fatalf("question termination = %v, want transport-gone", entry.Executions[0].TerminationReason)
	}
	if !strings.Contains(entry.Executions[0].Payload, "connection was lost") {
		t.Fatalf("question payload = %q, want the transport-loss sentence", entry.Executions[0].Payload)
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
	sid := h.openSession(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := "sh -c 'echo $$ > " + pidFile + "; exec sleep 100'"
	res := h.askRunsTool(sid, cmd)
	tap := newSocketTap(h.conn)

	raw := tapNotify(t, tap, "agent.runRequest", 10*time.Second)
	requestID, wantSid, _ := decodeRunRequest(t, raw)
	if wantSid != sid {
		t.Fatalf("runRequest session = %q, want %q", wantSid, sid)
	}
	h.submitLeaseCommand(t, tap, sid, cmd, requestID)
	pid := readPidFile(t, pidFile)
	waitChildAlive(t, pid)

	// The echo above broke the silence once; then nothing for the bound.
	runID, sentence := waitForRunState(t, tap, "completed")
	if runID != res.RunID {
		t.Fatalf("runState runId = %d, want %d", runID, res.RunID)
	}
	if sentence != "" {
		t.Fatalf("completed runState carries an error: %q", sentence)
	}
	if len(h.fake.bodies) < 2 || !strings.Contains(h.fake.bodies[1], "inactivity") {
		t.Fatalf("model request after lease = %v, want the inactivity tool result", h.fake.bodies)
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
	sid := h.openSession(t)
	// An ordinary command that COMPLETES on its own — every lease bound is
	// far beyond it, so none can explain anything that happens to it.
	cmd := "sh -c 'echo done'"
	res := h.askRunsTool(sid, cmd)
	tap := newSocketTap(h.conn)

	raw := tapNotify(t, tap, "agent.runRequest", 10*time.Second)
	rid, _, _ := decodeRunRequest(t, raw)
	h.submitLeaseCommand(t, tap, sid, cmd, rid)

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
	runID, sentence, unarmed := waitForRunStateDetails(t, tap, "completed")
	if runID != res.RunID {
		t.Fatalf("runState runId = %d, want %d", runID, res.RunID)
	}
	if sentence != "" {
		t.Fatalf("completed runState carries an error: %q", sentence)
	}
	if len(unarmed) != 0 {
		t.Fatalf("integrated runState unarmedBounds = %v, want absent", unarmed)
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
	sid := h.openSession(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := "sh -c 'echo $$ > " + pidFile + "; trap \"\" INT; sleep 100 & wait'"
	res := h.askRunsTool(sid, cmd)
	tap := newSocketTap(h.conn)

	raw := tapNotify(t, tap, "agent.runRequest", 10*time.Second)
	requestID, wantSid, _ := decodeRunRequest(t, raw)
	if wantSid != sid {
		t.Fatalf("runRequest session = %q, want %q", wantSid, sid)
	}
	h.submitLeaseCommand(t, tap, sid, cmd, requestID)
	pid := readPidFile(t, pidFile)
	waitChildAlive(t, pid)

	runID, sentence := waitForRunState(t, tap, "completed")
	if runID != res.RunID {
		t.Fatalf("runState runId = %d, want %d", runID, res.RunID)
	}
	if sentence != "" {
		t.Fatalf("completed runState carries an error: %q", sentence)
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
	sid := h.openSession(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := "sh -c 'echo $$ > " + pidFile + "; trap \"\" INT TERM; sleep 100 & wait'"
	res := h.askRunsTool(sid, cmd)
	tap := newSocketTap(h.conn)

	raw := tapNotify(t, tap, "agent.runRequest", 10*time.Second)
	requestID, wantSid, _ := decodeRunRequest(t, raw)
	if wantSid != sid {
		t.Fatalf("runRequest session = %q, want %q", wantSid, sid)
	}
	h.submitLeaseCommand(t, tap, sid, cmd, requestID)
	pid := readPidFile(t, pidFile)
	waitChildAlive(t, pid)

	runID, sentence := waitForRunState(t, tap, "completed")
	if runID != res.RunID {
		t.Fatalf("runState runId = %d, want %d", runID, res.RunID)
	}
	if sentence != "" {
		t.Fatalf("completed runState carries an error: %q", sentence)
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
	sid := h.openSession(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	// Output that KEEPS COMING until something stops it, rather than one
	// burst followed by a wait.
	//
	// The burst raced the accounting it was meant to exceed. Budget
	// accounting opens at ledger.bind, which submitLeaseCommand sends after
	// the shell echoes the command; a fixed 2 KiB written before that RPC
	// lands is not counted, and `sleep 100` then produces nothing, so the
	// budget never fires and the WALL CLOCK ends the run 60 seconds later.
	// Whether that happened was decided by how fast the machine was, and on
	// the GitHub runner it happened every time (nocx-3n0f3.1).
	//
	// A stream cannot lose that race: whenever accounting opens, 512 bytes
	// arrive within the next fifth of a second. 512 bytes per 100ms is also
	// slow enough that a run which somehow is NOT bounded floods nothing.
	cmd := "sh -c 'echo $$ > " + pidFile + "; while :; do dd if=/dev/zero bs=256 count=2 2>/dev/null; sleep 0.1; done'"
	res := h.askRunsTool(sid, cmd)
	tap := newSocketTap(h.conn)

	raw := tapNotify(t, tap, "agent.runRequest", 10*time.Second)
	requestID, wantSid, _ := decodeRunRequest(t, raw)
	if wantSid != sid {
		t.Fatalf("runRequest session = %q, want %q", wantSid, sid)
	}
	h.submitLeaseCommand(t, tap, sid, cmd, requestID)
	pid := readPidFile(t, pidFile)

	// No waitChildAlive here on purpose: the command's own output IS the
	// thing being bounded, and under -race the budget can fire — and the
	// escalation kill the child — before the test's next assertion runs.
	// The observable is the runState; the child's death is asserted after
	// it, below.

	runID, sentence := waitForRunState(t, tap, "completed")
	if runID != res.RunID {
		t.Fatalf("runState runId = %d, want %d", runID, res.RunID)
	}
	if sentence != "" {
		t.Fatalf("completed runState carries an error: %q", sentence)
	}
	if len(h.fake.bodies) < 2 || !strings.Contains(h.fake.bodies[1], "output exceeded the budget") {
		t.Fatalf("model request after lease = %v, want the output-budget tool result", h.fake.bodies)
	}
	waitChildDead(t, pid)

	reason := terminationReasonOfRun(t, h)
	if reason == nil || *reason != content.TermOutputBudget {
		t.Fatalf("ledger termination = %v, want output-budget", reason)
	}
}

// A session without a lifecycle lane still runs the command, but the
// output and inactivity bounds are explicitly named as unavailable. The
// wall-clock bound remains active and is the only bound advertised.
func TestRunLease_NoShellIntegrationMakesUnavailableBoundsVisible(t *testing.T) {
	h := newRunLeaseHarness(t, RunLeaseConfig{
		WallClock:    time.Minute,
		Inactivity:   time.Minute,
		OutputBudget: 512,
		SignalGrace:  200 * time.Millisecond,
	})
	h.createEndpointAt()
	sid := openLocalSession(t, h.conn)
	// The session's axis explicitly reports a conventional terminal: shell
	// integration was attempted but no authenticated lifecycle lane exists.
	h.ws.RegisterIntegration(session.ID(sid), "/bin/bash", IntegrationConventional, ssh.ReasonUnsupportedShell)
	cmd := "printf degraded-runs"
	res := h.askRunsTool(sid, cmd)
	tap := newSocketTap(h.conn)

	raw := tapNotify(t, tap, "agent.runRequest", 10*time.Second)
	requestID, wantSid, wantCommand := decodeRunRequest(t, raw)
	if wantSid != sid || wantCommand != cmd {
		t.Fatalf("runRequest = (%q, %q), want (%q, %q)", wantSid, wantCommand, sid, cmd)
	}
	h.submitLeaseCommand(t, tap, sid, cmd, "")
	tapDataFor(t, tap, sid, "degraded-runs", 15*time.Second)
	reply := tapCall(t, h.conn, tap, 42, "agent.runResolved",
		runResolvedWire(requestID, "entry-degraded", 0, "success", 1, 0, 1, "degraded-runs"))
	var rerr struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(reply, &rerr); err != nil {
		t.Fatalf("runResolved unmarshal: %v", err)
	}
	if rerr.Error != nil {
		t.Fatalf("runResolved refused: %+v", rerr.Error)
	}

	raw = tapNotify(t, tap, "agent.runState", 15*time.Second)
	var state struct {
		RunID         int64    `json:"runId"`
		State         string   `json:"state"`
		UnarmedBounds []string `json:"unarmedBounds"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("runState unmarshal: %v\nraw: %s", err, raw)
	}
	if state.RunID != res.RunID || state.State != "completed" {
		t.Fatalf("runState = %+v, want run %d completed", state, res.RunID)
	}
	wantBounds := []string{
		"the inactivity bound is not active because shell integration is unavailable",
		"the output bound is not active because shell integration is unavailable",
	}
	if !reflect.DeepEqual(state.UnarmedBounds, wantBounds) {
		t.Fatalf("unarmedBounds = %v, want %v", state.UnarmedBounds, wantBounds)
	}
	if reason := terminationReasonOfRun(t, h); reason == nil || *reason != content.TermCompleted {
		t.Fatalf("ledger termination = %v, want completed for the command that ran", reason)
	}
}
