package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/session"
)

func newLifecycleLedgerEnv(t *testing.T, withStore bool) (*lifecycleTestEnv, *lifecyclepub.Publisher, lifecycle.LaneID, lifecycle.DomainHandle, string, content.ContentDB) {
	t.Helper()
	var db content.ContentDB
	if withStore {
		db = newLedgerStore(t)
		if _, err := db.Layout().CreateWorkspace(context.Background(),
			content.Workspace{ID: "ws-lifecycle", Name: "lifecycle"},
			content.Tab{ID: "tab-lifecycle", WorkspaceID: "ws-lifecycle", Position: 0, Layout: content.LayoutRow},
			content.Pane{ID: "01930000-0000-7000-8000-0000000000a1", TabID: "tab-lifecycle", Cwd: "/repo", Kind: content.PaneLocal, SizeShare: 1}); err != nil {
			t.Fatalf("CreateWorkspace: %v", err)
		}
	}
	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel)
	e := newLifecycleTestEnv(t, WithContentDB(db), WithLifecyclePublisher(pub))
	pub.SetEmitter(e.ws)
	sid := openLifecycleLedgerSession(t, e, "01930000-0000-7000-8000-0000000000a1")
	const lane = lifecycle.LaneID("lane-lifecycle")
	e.ws.RegisterLifecycleLane(lane, session.ID(sid))
	if err := pub.BindTransport("T", noopPort{}); err != nil {
		t.Fatalf("BindTransport: %v", err)
	}
	h, err := pub.RequestDomain(lane, nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 1, lifecycleHelloEvt()))
	ackEstablishmentFrom(t, pub, lane, h, e.conn)
	return e, pub, lane, h, sid, db
}

func openLifecycleLedgerSession(t *testing.T, e *lifecycleTestEnv, paneID string) string {
	t.Helper()
	resp := jsonrpcCallWithID(t, e.conn, "open", map[string]any{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0, "paneId": paneID,
	}, 1)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("open: unmarshal: %v\nraw: %s", err, resp)
	}
	if envelope.Error != nil {
		t.Fatalf("open: %+v", envelope.Error)
	}
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatalf("open: decode result: %v", err)
	}
	if result.SessionID == "" {
		t.Fatal("open returned an empty session id")
	}
	awaitSubscriber(t, e.ws, session.ID(result.SessionID))
	return result.SessionID
}

// lifecycleSubmitParams is a PERSON's submit — the ordinary case, named
// rather than defaulted: `source` is required on this wire (nocx-iadtt), and
// a helper that omitted it would let a handler regress to a default nobody
// notices. lifecycleSubmitParamsFrom names the other target.
func lifecycleSubmitParams(domain, command string) map[string]string {
	return map[string]string{
		"domain":  domain,
		"command": command,
		"cwd":     "/repo",
		"host":    "",
		"source":  "user",
	}
}

func TestLifecycleSubmitAttempt_OpensLedgerEntryAtAttemptIDAndMasks(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	const secret = "sk-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ" //nolint:gosec // synthetic detector fixture
	command := "deploy --token=" + secret
	got := decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, e.conn, "lifecycle.submitAttempt", lifecycleSubmitParams(string(h.Domain), command), 41))
	if got.ID == "" {
		t.Fatal("submit returned an empty attempt id")
	}
	row := mustEntry(t, db, got.ID)
	if row.ID != got.ID || row.Phase != content.PhaseOpen || row.Status != content.EntryPending {
		t.Fatalf("submit row = id=%q phase=%q status=%q, want attempt id/open/pending", row.ID, row.Phase, row.Status)
	}
	if row.PaneID == nil || *row.PaneID != "01930000-0000-7000-8000-0000000000a1" {
		t.Fatalf("submit row pane = %v, want lifecycle pane", row.PaneID)
	}
	if strings.Contains(row.Intent, secret) || !strings.Contains(row.Intent, "sk-a...GHIJ") {
		t.Fatalf("stored intent = %q, want masked secret", row.Intent)
	}
	masking, err := content.EntryMaskingOf(row.Payload)
	if err != nil {
		t.Fatalf("EntryMaskingOf: %v", err)
	}
	if masking.MaskedCount != 1 || len(masking.Redactions) != 1 {
		t.Fatalf("stored masking receipt = %+v, want one redaction", masking)
	}
	if _, ok := pub.Attempt(lifecycle.AttemptID(got.ID)); !ok {
		t.Fatalf("attempt %q is absent from the lifecycle kernel", got.ID)
	}
	if items, err := e.ws.ListSessionItems(context.Background(), sid, 10); err != nil {
		t.Fatalf("ListSessionItems while open: %v", err)
	} else if len(items.Items) != 1 || items.Items[0].ID != got.ID || items.Items[0].State != "running" {
		t.Fatalf("running session items = %+v, want one running item %q", items.Items, got.ID)
	}
	_ = lane
}

func TestLifecycleLedgerTransitions_ListAndReadByAttemptID(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	const secret = "sk-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ" //nolint:gosec // synthetic detector fixture
	command := "make test --token=" + secret
	got := decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, e.conn, "lifecycle.submitAttempt", lifecycleSubmitParams(string(h.Domain), command), 41))
	row := mustEntry(t, db, got.ID)
	if row.Phase != content.PhaseOpen {
		t.Fatalf("before start phase = %q, want open", row.Phase)
	}

	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 2, lifecycleStartEvt(nil, command)))
	row = mustEntry(t, db, got.ID)
	if row.Phase != content.PhaseBound || len(row.Executions) != 1 || row.Executions[0].EndedAt != nil {
		t.Fatalf("after authenticated start row = phase=%q executions=%+v, want bound with one live execution", row.Phase, row.Executions)
	}
	items, err := e.ws.ListSessionItems(context.Background(), sid, 10)
	if err != nil {
		t.Fatalf("ListSessionItems while bound: %v", err)
	}
	if len(items.Items) != 1 || items.Items[0].ID != got.ID || items.Items[0].State != "running" {
		t.Fatalf("bound session items = %+v, want one running item %q", items.Items, got.ID)
	}

	fence := lifecycleFence(0x44)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(got.ID), 7, fence)))
	row = mustEntry(t, db, got.ID)
	if row.Phase != content.PhaseClosed || row.Status != content.EntryFailure {
		t.Fatalf("after completion row = phase=%q status=%q, want closed/failure", row.Phase, row.Status)
	}
	recordResp := jsonrpcCallWithID(t, e.conn, "history.record", map[string]any{
		"attemptId": got.ID,
		"command":   command,
		"cwd":       "/repo",
		"host":      "",
		"source":    "user",
		"status":    "failure",
		"exitCode":  7,
		"startedAt": nil,
		"endedAt":   nil,
		"paneId":    "01930000-0000-7000-8000-0000000000a1",
	}, 42)
	var recordEnvelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if unmarshalErr := json.Unmarshal(recordResp, &recordEnvelope); unmarshalErr != nil {
		t.Fatalf("history.record response: %v", unmarshalErr)
	}
	if recordEnvelope.Error != nil {
		t.Fatalf("history.record: %+v", recordEnvelope.Error)
	}
	var ack historyRecordResponse
	if ackErr := json.Unmarshal(recordEnvelope.Result, &ack); ackErr != nil {
		t.Fatalf("history.record result: %v", ackErr)
	}
	if ack.EntryID != got.ID {
		t.Fatalf("history.record entry id = %q, want attempt id %q", ack.EntryID, got.ID)
	}
	entries, err := db.Ledger().ListEntries(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != got.ID {
		t.Fatalf("history.record rows = %+v, want one row under %q", entries, got.ID)
	}
	finalRow := mustEntry(t, db, got.ID)
	if strings.Contains(finalRow.Intent, secret) || !strings.Contains(finalRow.Intent, "sk-a...GHIJ") {
		t.Fatalf("completed row intent = %q, want masked command", finalRow.Intent)
	}
	items, err = e.ws.ListSessionItems(context.Background(), sid, 10)
	if err != nil {
		t.Fatalf("ListSessionItems after completion: %v", err)
	}
	if len(items.Items) != 1 || items.Items[0].ID != got.ID || items.Items[0].State != "exited" || items.Items[0].ExitCode == nil || *items.Items[0].ExitCode != 7 {
		t.Fatalf("completed session items = %+v, want one exited item with code 7", items.Items)
	}
	read, err := e.ws.ReadSessionItem(context.Background(), sid, got.ID, 0, 10)
	if err != nil {
		t.Fatalf("ReadSessionItem by attempt id: %v", err)
	}
	if read.ID != got.ID || read.Command != finalRow.Intent || read.State != "exited" || read.ExitCode == nil || *read.ExitCode != 7 {
		t.Fatalf("read item = %+v, want attempt id, command and exit code", read)
	}
}

func TestLifecycleLedger_AbandonsOpenRowsAsUnknownOnTransportLoss(t *testing.T) {
	t.Run("pty write fails before authenticated start", func(t *testing.T) {
		e, pub, _, h, _, db := newLifecycleLedgerEnv(t, true)
		got := decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, e.conn, "lifecycle.submitAttempt", lifecycleSubmitParams(string(h.Domain), "false"), 41))
		if row := mustEntry(t, db, got.ID); row.Phase != content.PhaseOpen {
			t.Fatalf("before loss phase = %q, want open", row.Phase)
		}
		if err := pub.TransportLost("T"); err != nil {
			t.Fatalf("TransportLost: %v", err)
		}
		row := mustEntry(t, db, got.ID)
		if row.Phase != content.PhaseClosed || row.Status != content.EntryUnknown {
			t.Fatalf("after loss row = phase=%q status=%q, want closed/unknown", row.Phase, row.Status)
		}
	})

	t.Run("process is killed after start", func(t *testing.T) {
		e, pub, lane, h, _, db := newLifecycleLedgerEnv(t, true)
		got := decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, e.conn, "lifecycle.submitAttempt", lifecycleSubmitParams(string(h.Domain), "sleep 1000"), 41))
		mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 2, lifecycleStartEvt(nil, "sleep 1000")))
		if row := mustEntry(t, db, got.ID); row.Phase != content.PhaseBound {
			t.Fatalf("before kill phase = %q, want bound", row.Phase)
		}
		if err := pub.TransportLost("T"); err != nil {
			t.Fatalf("TransportLost: %v", err)
		}
		row := mustEntry(t, db, got.ID)
		if row.Phase != content.PhaseClosed || row.Status != content.EntryUnknown {
			t.Fatalf("after kill row = phase=%q status=%q, want closed/unknown", row.Phase, row.Status)
		}
	})
}

func TestLifecycleSubmitAttempt_StoreUnavailableStillRunsCommand(t *testing.T) {
	e, pub, lane, h, _, _ := newLifecycleLedgerEnv(t, false)
	got := decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, e.conn, "lifecycle.submitAttempt", lifecycleSubmitParams(string(h.Domain), "echo hi"), 41))
	if got.ID == "" {
		t.Fatal("store-unavailable submit returned an empty attempt id")
	}
	if _, ok := pub.OpenAttempt(h.Domain); !ok {
		t.Fatal("store-unavailable submit did not leave a live kernel attempt")
	}
	state, err := pub.State(lane)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state.Lifecycle != lifecycle.LifecycleRunning {
		t.Fatalf("state after store degrade = %q, want running", state.Lifecycle)
	}
}

func TestLifecycleLedger_RunningBlockReadKeepsAttemptIDThroughCompletion(t *testing.T) {
	client, err := assistant.NewClient(nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel)
	provider := &lifecycleBlockReadProvider{
		promptID:       make(chan string, 1),
		runningResult:  make(chan struct{}),
		finishedResult: make(chan struct{}),
		releaseSecond:  make(chan struct{}),
	}
	server := httptest.NewServer(http.HandlerFunc(provider.serve))
	defer server.Close()

	h := newAskHarnessWithOpts(t, client,
		WithAgentPolicy(autonomousPolicyStore(t)),
		WithLifecyclePublisher(pub),
	)
	pub.SetEmitter(h.ws)
	h.createEndpointAt(server.URL)
	sid, errObj := openSessionInPane(t, h.conn, askPaneID, 1)
	if errObj != nil {
		t.Fatalf("open session: %+v", errObj)
	}
	provider.session = sid

	const lane = lifecycle.LaneID("lane-block-read")
	h.ws.RegisterLifecycleLane(lane, session.ID(sid))
	if bindErr := pub.BindTransport("T", noopPort{}); bindErr != nil {
		t.Fatalf("BindTransport: %v", bindErr)
	}
	domain, err := pub.RequestDomain(lane, nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, domain, 1, lifecycleHelloEvt()))
	ackEstablishmentFrom(t, pub, lane, domain, h.conn)

	const command = "make deploy"
	submitted := decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, h.conn, "lifecycle.submitAttempt",
		lifecycleSubmitParams(string(domain.Domain), command), 41))
	if submitted.ID == "" {
		t.Fatal("submit returned an empty attempt id")
	}
	attemptID := lifecycle.AttemptID(submitted.ID)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, domain, 2, lifecycleStartEvt(&attemptID, command)))

	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "ask-block-read",
		"sessionId": sid,
		"question":  "what did the deploy command print?",
		"cwd":       "/repo",
		"attachedContent": []any{
			map[string]any{"itemId": submitted.ID, "command": command, "state": "running"},
		},
	}, 42); errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}

	var promptID string
	select {
	case promptID = <-provider.promptID:
	case <-time.After(10 * time.Second):
		t.Fatal("fake model never received the attached-content prompt")
	}
	if promptID == "" {
		t.Fatal("fake model extracted an empty attempt id from the prompt")
	}
	if promptID != submitted.ID {
		t.Fatalf("prompt id = %q, want the submitted attempt id %q", promptID, submitted.ID)
	}

	firstCall := readNotification(t, h.conn, "agent.runToolCall", 10*time.Second)
	var first struct {
		Tool string `json:"tool"`
		Args struct {
			SessionID string `json:"sessionId"`
			ID        string `json:"id"`
		} `json:"args"`
	}
	if err := json.Unmarshal(firstCall, &first); err != nil {
		t.Fatalf("first agent.runToolCall: %v\nraw: %s", err, firstCall)
	}
	if first.Tool != "session.read" || first.Args.SessionID != sid || first.Args.ID != promptID {
		t.Fatalf("first session.read = %+v, want session %q and prompt id %q", first, sid, promptID)
	}

	screenRaw := readNotification(t, h.conn, "agent.readScreenRequest", 10*time.Second)
	var screen struct {
		RequestID string `json:"requestId"`
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(screenRaw, &screen); err != nil {
		t.Fatalf("agent.readScreenRequest: %v\nraw: %s", err, screenRaw)
	}
	if screen.SessionID != sid || screen.RequestID == "" {
		t.Fatalf("screen request = %+v, want session %q and a request id", screen, sid)
	}
	if reply := jsonrpcCall(t, h.conn, "agent.readScreenResolved",
		readScreenFrameWire(t, screen.RequestID, "live block output")); isErrorResponse(t, reply) {
		t.Fatalf("agent.readScreenResolved: %s", reply)
	}

	select {
	case <-provider.runningResult:
	case <-time.After(10 * time.Second):
		t.Fatal("fake model never received the running session.read result")
	}
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, domain, 3, lifecycleCompleteEvt(attemptID, 7, lifecycleFence(0x55))))
	recordResp := jsonrpcCallWithID(t, h.conn, "history.record", map[string]any{
		"attemptId": submitted.ID,
		"command":   command,
		"cwd":       "/repo",
		"host":      "",
		"source":    "user",
		"status":    "failure",
		"exitCode":  7,
		"startedAt": nil,
		"endedAt":   nil,
		"paneId":    askPaneID,
	}, 43)
	var recordEnvelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(recordResp, &recordEnvelope); err != nil {
		t.Fatalf("history.record response: %v", err)
	}
	if recordEnvelope.Error != nil {
		t.Fatalf("history.record: %+v", recordEnvelope.Error)
	}
	captureBody(t, h.db, submitted.ID, "artifact-block-read", "finished block output")
	close(provider.releaseSecond)

	secondCall := readNotification(t, h.conn, "agent.runToolCall", 10*time.Second)
	var second struct {
		Tool string `json:"tool"`
		Args struct {
			SessionID string `json:"sessionId"`
			ID        string `json:"id"`
		} `json:"args"`
	}
	if err := json.Unmarshal(secondCall, &second); err != nil {
		t.Fatalf("second agent.runToolCall: %v\nraw: %s", err, secondCall)
	}
	if second.Tool != "session.read" || second.Args.SessionID != sid || second.Args.ID != promptID {
		t.Fatalf("second session.read = %+v, want session %q and the same prompt id %q", second, sid, promptID)
	}

	select {
	case <-provider.finishedResult:
	case <-time.After(10 * time.Second):
		t.Fatal("fake model never received the completed session.read result")
	}
	thirdCall := readNotification(t, h.conn, "agent.runToolCall", 10*time.Second)
	var third struct {
		Tool string `json:"tool"`
		Args struct {
			ID string `json:"id"`
		} `json:"args"`
	}
	if err := json.Unmarshal(thirdCall, &third); err != nil {
		t.Fatalf("unmarked agent.runToolCall: %v\nraw: %s", err, thirdCall)
	}
	if third.Tool != "session.read" || third.Args.ID != "unmarked-block-id" {
		t.Fatalf("unmarked session.read = %+v, want the deliberate unmarked id", third)
	}
	// Today, an id the person did not mark reaches the ledger and terminalizes
	// the run with a "no such item" refusal. nocx-hnh7r owns changing this
	// baseline; this assertion records today's scope rather than its intent.

	runState := readNotification(t, h.conn, "agent.runState", 10*time.Second)
	var state struct {
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(runState, &state); err != nil {
		t.Fatalf("agent.runState: %v\nraw: %s", err, runState)
	}
	if state.State != "failed" {
		t.Fatalf("agent.runState = %q, want failed after an unmarked read", state.State)
	}
	if !strings.Contains(state.Error, "session.read") || !strings.Contains(state.Error, "no such item") {
		t.Fatalf("agent.runState error = %q, want the current unmarked-read refusal", state.Error)
	}
	if err := provider.failure(); err != nil {
		t.Fatal(err)
	}
}

type lifecycleBlockReadProvider struct {
	session        string
	promptID       chan string
	runningResult  chan struct{}
	finishedResult chan struct{}
	releaseSecond  chan struct{}

	mu      sync.Mutex
	learned string
	errs    []string
}

func (p *lifecycleBlockReadProvider) serve(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		p.fail("read model request: %v", err)
		streamAnswerChunk(w, "provider could not read the request")
		return
	}
	body := string(raw)
	p.mu.Lock()
	if p.learned == "" {
		p.learned = lifecycleBlockReadPromptID(body)
		if p.learned == "" {
			p.errs = append(p.errs, "prompt did not name an attached item id")
		}
		if !strings.Contains(body, "state: running") {
			p.errs = append(p.errs, "prompt did not name the attached item as running")
		}
		learned := p.learned
		p.mu.Unlock()
		p.promptID <- learned
		streamToolCallChunk(w, "session.read", fmt.Sprintf(
			`{"sessionId":%q,"id":%q,"start":0,"count":20}`, p.session, learned))
		return
	}
	learned := p.learned
	p.mu.Unlock()
	tool := lifecycleBlockReadToolResult(body)

	switch {
	case strings.Contains(tool, `"state":"running"`) && strings.Contains(tool, "live block output"):
		if !strings.Contains(tool, learned) {
			p.fail("running session.read result omitted prompt id %q", learned)
		}
		close(p.runningResult)
		<-p.releaseSecond
		streamToolCallChunk(w, "session.read", fmt.Sprintf(
			`{"sessionId":%q,"id":%q,"start":0,"count":20}`, p.session, learned))
	case strings.Contains(tool, `"state":"exited"`) &&
		strings.Contains(tool, `"exitCode":7`) &&
		strings.Contains(tool, "finished block output"):
		if !strings.Contains(tool, learned) {
			p.fail("completed session.read result omitted prompt id %q", learned)
		}
		close(p.finishedResult)
		streamToolCallChunk(w, "session.read",
			`{"sessionId":"`+p.session+`","id":"unmarked-block-id","start":0,"count":20}`)
	default:
		p.fail("unexpected model tool result: %s", tool)
		streamAnswerChunk(w, "unexpected provider request")
	}
}

func (p *lifecycleBlockReadProvider) fail(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.errs = append(p.errs, fmt.Sprintf(format, args...))
}

func (p *lifecycleBlockReadProvider) failure() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.errs) == 0 {
		return nil
	}
	return fmt.Errorf("fake model assertions: %s", strings.Join(p.errs, "; "))
}

func lifecycleBlockReadToolResult(body string) string {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return ""
	}
	latest := ""
	for _, message := range req.Messages {
		if message.Role == "tool" {
			latest = message.Content
		}
	}
	return latest
}

func lifecycleBlockReadPromptID(body string) string {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return ""
	}
	for _, message := range req.Messages {
		if message.Role != "system" {
			continue
		}
		for _, line := range strings.Split(message.Content, "\n") {
			const marker = "- id: "
			if !strings.Contains(line, marker) {
				continue
			}
			id := line[strings.Index(line, marker)+len(marker):]
			if end := strings.Index(id, ";"); end >= 0 {
				id = id[:end]
			}
			return strings.TrimSpace(id)
		}
	}
	return ""
}

// lifecycleSubmitParamsFrom is lifecycleSubmitParams with the submitting
// target named: `source` is required on this wire and has no default, for the
// reason nocx-iadtt gave it no default on history.record's — a submit path
// that could forget it would silently attribute a command to the person.
func lifecycleSubmitParamsFrom(domain, command, source string) map[string]string {
	p := lifecycleSubmitParams(domain, command)
	p["source"] = source
	return p
}

// THE ROW IS BORN WITH THE AUTHOR THAT SUBMITTED IT (nocx-1druc, nocx-iadtt).
// Since nocx-kpqr3 the entry opens HERE, at submit, under the attempt id, and
// this call stamped every one of them 'user'. So the assistant's own `run`
// landed as the person's: history.record carries the renderer's minted source
// and its close leaves the column alone, which means nothing downstream could
// ever repair it, and a restored pane forgot that the assistant had run the
// command (agent-restore.spec.ts — the restore badge is painted from this
// column). Both submitting targets are driven here, because a fix that
// stamped everything 'assistant' would pass a one-sided test.
func TestLifecycleSubmitAttempt_StampsTheAuthorThatSubmitted(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		source  string
		want    content.Source
	}{
		{"a person's own line", "echo typed-by-a-person", "user", content.SourceUser},
		{"the assistant's run", "echo ran-by-the-assistant", "assistant", content.SourceAssistant},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// One env per case: a domain holds ONE open attempt, so two
			// submits in one env is a test about prompt readiness rather
			// than about provenance.
			e, _, _, h, _, db := newLifecycleLedgerEnv(t, true)
			got := decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, e.conn, "lifecycle.submitAttempt",
				lifecycleSubmitParamsFrom(string(h.Domain), tc.command, tc.source), 41))
			if row := mustEntry(t, db, got.ID); row.Source != tc.want {
				t.Fatalf("submit stored source=%q, want %q", row.Source, tc.want)
			}
		})
	}
}

// A source outside the ledger's vocabulary is refused before an attempt is
// opened, and an ABSENT one is refused too: this is the field a submit path
// must not be able to forget (nocx-iadtt: required on the wire, no default).
// An attempt opened and then refused would hold the domain and poison the
// next attach, which is why the refusal is in the validator rather than at
// the store write.
func TestLifecycleSubmitAttempt_RefusesASourceOutsideTheVocabulary(t *testing.T) {
	e, _, _, h, _, _ := newLifecycleLedgerEnv(t, true)
	for _, tc := range []struct{ name, source string }{
		{"absent", ""},
		{"not a subject", "agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := lifecycleSubmitParams(string(h.Domain), "echo hi")
			if tc.source == "" {
				delete(params, "source")
			} else {
				params["source"] = tc.source
			}
			errObj := submitAttemptErr(t, e.conn, params, 61)
			if errObj.Code != -32602 {
				t.Fatalf("error code = %d, want -32602", errObj.Code)
			}
			if !strings.Contains(errObj.Message, "source") {
				t.Fatalf("error message = %q, want it to name source", errObj.Message)
			}
		})
	}
}
