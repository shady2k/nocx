package transport

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

func TestAgentAskReplayPrecedesChangedRoleAndRuntime(t *testing.T) {
	client := &scriptedAssistantClient{deltas: []string{"saved answer"}}
	var enabled atomic.Bool
	var runtimeChecks atomic.Int32
	selector := assistant.NewEngineSelector(
		assistant.Engine{ID: assistant.EngineEino, RuntimeVersion: "eino-original", Client: client},
		assistant.Engine{ID: assistant.EnginePydantic, Client: &scriptedAssistantClient{}},
		func(context.Context) (bool, error) { return enabled.Load(), nil },
		func(context.Context) (string, error) { runtimeChecks.Add(1); return "", errors.New("removed runtime") },
	)
	h := newAskHarnessWithOpts(t, client, WithAssistantEngines(selector))
	created, code := decodeEndpointResult(t, jsonrpcCall(t, h.conn, "endpoints.create", map[string]any{
		"name": "Local", "baseUrl": "http://127.0.0.1:11434/v1", "schema": "openai-compatible",
		"key": "sk-test-123", "models": []map[string]any{{"name": "qwen3"}},
	}))
	if code != 0 {
		t.Fatalf("create endpoint: %d", code)
	}
	if isErrorResponse(t, jsonrpcCall(t, h.conn, "roles.assign", map[string]any{"role": "answering", "endpointId": created.ID, "model": "qwen3"})) {
		t.Fatal("assign role refused")
	}
	sid := openLocalSession(t, h.conn)
	params := map[string]any{"askId": "replay-pinned", "sessionId": sid, "question": "q", "cwd": "/repo", "attachedContent": []any{}}
	first, rpcErr := askOverWire(t, h.conn, params, 2)
	if rpcErr != nil {
		t.Fatalf("first ask: %+v", rpcErr)
	}
	readNotification(t, h.conn, "agent.runState", 5*time.Second)
	enabled.Store(true)
	if isErrorResponse(t, jsonrpcCall(t, h.conn, "endpoints.delete", map[string]any{"id": created.ID})) {
		t.Fatal("delete refused")
	}
	replayed, rpcErr := askOverWire(t, h.conn, params, 3)
	if rpcErr != nil {
		t.Fatalf("replay consulted changed configuration: %+v", rpcErr)
	}
	if !replayed.Replayed || replayed.State != "completed" || replayed.RunID != first.RunID || replayed.Engine != assistant.EngineEino || replayed.RuntimeVersion != "eino-original" || replayed.Model != "qwen3" {
		t.Fatalf("replay lost original state/provenance: %+v", replayed)
	}
	if client.askCount() != 1 || runtimeChecks.Load() != 0 {
		t.Fatalf("replay drove model/runtime again: asks=%d checks=%d", client.askCount(), runtimeChecks.Load())
	}
	var env rpcEnvelope
	if err := json.Unmarshal(jsonrpcCall(t, h.conn, "ledger.get", map[string]any{"id": first.EntryID}), &env); err != nil {
		t.Fatal(err)
	}
	validateJSON(t, loadSchema(t, "ledger.get.schema.json"), env.Result, "replayed run metadata")
	var detail ledgerGetResponse
	if err := json.Unmarshal(env.Result, &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.AgentRuns) != 1 || detail.AgentRuns[0].Engine != assistant.EngineEino || detail.AgentRuns[0].RuntimeVersion != "eino-original" {
		t.Fatalf("stored provenance = %+v", detail.AgentRuns)
	}
}

func TestAgentAskParkedReplayKeepsItsEngineAfterFlagChanges(t *testing.T) {
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{suspend: policySuspension("files.read", "call_1", `{"path":"/repo/a.txt"}`, "hash-a")},
		{deltas: []string{"continued original run"}},
	}}
	other := &scriptedAssistantClient{}
	var enabled atomic.Bool
	enabled.Store(true)
	selector := assistant.NewEngineSelector(
		assistant.Engine{ID: assistant.EngineEino, Client: other}, assistant.Engine{ID: assistant.EnginePydantic, Client: client},
		func(context.Context) (bool, error) { return enabled.Load(), nil },
		func(context.Context) (string, error) { return "manifest-original", nil },
	)
	h := newAskHarnessWithOpts(t, other, WithAssistantEngines(selector))
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	params := map[string]any{"askId": "parked-replay", "sessionId": sid, "question": "q", "cwd": "/repo", "attachedContent": []any{}}
	first, rpcErr := askOverWire(t, h.conn, params, 2)
	if rpcErr != nil {
		t.Fatalf("first ask: %+v", rpcErr)
	}
	readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)
	enabled.Store(false)
	replayed, rpcErr := askOverWire(t, h.conn, params, 3)
	if rpcErr != nil || !replayed.Replayed || replayed.State != "awaiting_approval" || replayed.Engine != assistant.EnginePydantic || replayed.RuntimeVersion != "manifest-original" {
		t.Fatalf("parked replay = %+v, %+v", replayed, rpcErr)
	}
	if client.askCount() != 1 || other.askCount() != 0 {
		t.Fatal("parked replay drove a second stream")
	}
	_, rpcErr = approveOverWire(t, h.conn, map[string]any{
		"runId": strconv.FormatInt(first.RunID, 10), "attempt": 1, "tool": "files.read",
		"callId": "call_1", "argHash": "hash-a", "approved": true, "scope": "once",
	}, 4)
	if rpcErr != nil {
		t.Fatalf("approval after flag change: %+v", rpcErr)
	}
	readNotification(t, h.conn, "agent.runState", 5*time.Second)
	if client.askCount() != 2 || other.askCount() != 0 || len(client.discards()) != 1 {
		t.Fatalf("resume migrated or lost owner: asks=%d other=%d discards=%v", client.askCount(), other.askCount(), client.discards())
	}
	drives := client.params()
	if drives[0].Resume || !drives[1].Resume {
		t.Fatalf("continuation admission was not explicit: %+v", drives)
	}
}

func TestAgentRuntimeLossWhileParkedTerminalizesPinnedOwnerOnce(t *testing.T) {
	client := &scriptedApprovalClient{script: []approvalScriptStep{{suspend: policySuspension("files.read", "call_1", `{"path":"/repo/a.txt"}`, "hash-a")}}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	res, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "runtime-lost", "sessionId": sid, "question": "q", "cwd": "/repo", "attachedContent": []any{}}, 2)
	if rpcErr != nil {
		t.Fatalf("ask: %+v", rpcErr)
	}
	readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)
	params := client.params()
	if len(params) != 1 || params[0].OnRuntimeLost == nil {
		t.Fatal("runtime lifetime callback was not bound before the drive")
	}
	loss := &assistant.StreamError{Message: "the experimental assistant runtime stopped unexpectedly"}
	params[0].OnRuntimeLost(loss)
	params[0].OnRuntimeLost(loss)
	var state agentRunState
	if err := json.Unmarshal(readNotification(t, h.conn, "agent.runState", 5*time.Second), &state); err != nil {
		t.Fatal(err)
	}
	if state.State != "failed" || state.Error != loss.Message {
		t.Fatalf("runtime failure = %+v", state)
	}
	row, err := h.db.Ledger().Entry(t.Context(), res.EntryID)
	if err != nil || row == nil || len(row.Executions) != 1 || row.Executions[0].State == nil || *row.Executions[0].State != content.RunFailed {
		t.Fatalf("runtime loss was not durable: row=%+v err=%v", row, err)
	}
	if len(client.discards()) != 1 {
		t.Fatalf("runtime loss discarded more than once: %v", client.discards())
	}
	h.ws.pendingRunsMu.Lock()
	_, pending := h.ws.pendingRuns[res.RunID]
	h.ws.pendingRunsMu.Unlock()
	if pending {
		t.Fatal("runtime loss left a resumable parked run")
	}
}

type activeRuntimeLossClient struct {
	stubAssistantClient
	entered   chan assistant.AskParams
	exited    chan struct{}
	discarded atomic.Int32
}

func (c *activeRuntimeLossClient) Ask(ctx context.Context, p assistant.AskParams, onEvent func(assistant.AskEvent) error) error {
	if err := onEvent(assistant.AskEvent{Kind: assistant.AskAnswer, Text: "committed partial answer"}); err != nil {
		return err
	}
	c.entered <- p
	<-ctx.Done()
	// A reaping client may wait for its final event attempt. The transport's
	// terminal gate must reject it without holding Discard behind that mutex.
	_ = onEvent(assistant.AskEvent{Kind: assistant.AskAnswer, Text: "must not be appended"})
	close(c.exited)
	return ctx.Err()
}

func (c *activeRuntimeLossClient) Discard(string) {
	c.discarded.Add(1)
	<-c.exited
}

func TestAgentRuntimeLossDuringDriveReapsWithoutFinalEventDeadlock(t *testing.T) {
	client := &activeRuntimeLossClient{entered: make(chan assistant.AskParams, 1), exited: make(chan struct{})}
	selector := assistant.NewEngineSelector(
		assistant.Engine{ID: assistant.EngineEino, Client: &scriptedAssistantClient{}},
		assistant.Engine{ID: assistant.EnginePydantic, Client: client},
		func(context.Context) (bool, error) { return true, nil },
		func(context.Context) (string, error) { return "manifest-active", nil },
	)
	h := newAskHarnessWithOpts(t, client, WithAssistantEngines(selector))
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	result, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "active-runtime-loss", "sessionId": sid, "question": "q", "cwd": "/repo", "attachedContent": []any{}}, 2)
	if rpcErr != nil {
		t.Fatalf("ask: %+v", rpcErr)
	}
	var p assistant.AskParams
	select {
	case p = <-client.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("active drive did not begin")
	}
	loss := &assistant.StreamError{Message: "the experimental assistant runtime stopped unexpectedly"}
	p.OnRuntimeLost(loss)
	p.OnRuntimeLost(loss)
	var state agentRunState
	if err := json.Unmarshal(readNotification(t, h.conn, "agent.runState", 5*time.Second), &state); err != nil {
		t.Fatal(err)
	}
	if state.State != "failed" || state.Error != loss.Message || client.discarded.Load() != 1 {
		t.Fatalf("runtime loss settlement: %+v, discards=%d", state, client.discarded.Load())
	}
	row, err := h.db.Ledger().Entry(t.Context(), result.EntryID)
	if err != nil || row == nil || len(row.Executions) != 1 || row.Executions[0].State == nil || *row.Executions[0].State != content.RunFailed {
		t.Fatalf("active failure not durable: %+v, %v", row, err)
	}
}

func TestAgentRuntimeLossSettlementJoinsShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &activeRuntimeLossClient{exited: make(chan struct{})}
		service := &fakeAgentService{}
		h := newGapHandlers(t, service, client, nil)
		h.inflight = &inflight{}
		rc := gapRunContext(t, h)
		loss := &assistant.StreamError{Message: "runtime lost"}
		go h.handleRuntimeLoss(t.Context(), rc, loss, &gapResponder{})
		synctest.Wait()
		if client.discarded.Load() != 1 {
			t.Fatal("runtime cleanup did not begin")
		}

		drained := make(chan bool, 1)
		go func() {
			h.inflight.stop()
			drained <- h.inflight.waitDrained(time.Second)
		}()
		synctest.Wait()
		select {
		case <-drained:
			t.Error("shutdown returned before the failed run was settled")
		default:
		}
		close(client.exited)
		synctest.Wait()
		select {
		case joined := <-drained:
			if !joined {
				t.Error("shutdown abandoned a cooperative terminal settlement")
			}
		default:
			t.Error("shutdown did not finish after terminal settlement")
		}
		if service.finish == nil || service.finish.State != content.RunFailed {
			t.Fatalf("shutdown did not preserve the failed terminal state: %+v", service.finish)
		}
	})
}

func TestAgentRuntimeLossAfterShutdownCannotWriteTheLedger(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &activeRuntimeLossClient{exited: make(chan struct{})}
		close(client.exited)
		service := &fakeAgentService{}
		h := newGapHandlers(t, service, client, nil)
		h.inflight = &inflight{}
		h.inflight.stop()
		h.handleRuntimeLoss(t.Context(), gapRunContext(t, h), &assistant.StreamError{Message: "runtime lost"}, &gapResponder{})
		synctest.Wait()
		if service.finish != nil {
			t.Fatalf("a late runtime callback wrote after shutdown: %+v", service.finish)
		}
	})
}

func TestProbeKeepsAdmissionEngineAndStatusRejectsItsStaleVersion(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var enabled atomic.Bool
	enabled.Store(true)
	var version atomic.Value
	version.Store("manifest-v1")
	var einoCalls atomic.Int32
	eino := &stubAssistantClient{probe: func(context.Context, assistant.ProbeParams) (assistant.ProbeResult, error) {
		einoCalls.Add(1)
		return assistant.ProbeResult{}, errors.New("wrong operation owner")
	}}
	experimental := &stubAssistantClient{probe: func(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return assistant.ProbeResult{}, ctx.Err()
		}
		return assistant.ProbeResult{EndpointName: p.Name, Model: p.Model, Kind: probeKindFor(p), OK: true, At: time.Now()}, nil
	}}
	selector := assistant.NewEngineSelector(
		assistant.Engine{ID: assistant.EngineEino, RuntimeVersion: "eino-v1", Client: eino},
		assistant.Engine{ID: assistant.EnginePydantic, Client: experimental},
		func(context.Context) (bool, error) { return enabled.Load(), nil },
		func(context.Context) (string, error) {
			value, ok := version.Load().(string)
			if !ok {
				return "", errors.New("runtime version fixture is not a string")
			}
			return value, nil
		},
	)
	h := newAssistantHarnessWith(t, experimental, nil, WithAssistantEngines(selector))
	h.setupAndUnseal()
	ep := h.createEndpoint(t, testEndpointParams())
	h.mustSetDefault(ep.ID, "qwen3")
	result := make(chan []byte, 1)
	go func() {
		result <- jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
			"name": "Local", "baseUrl": "http://127.0.0.1:11434/v1", "key": "sk", "model": "qwen3",
		})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("probe did not begin")
	}
	enabled.Store(false)
	close(release)
	var response rpcEnvelope
	if err := json.Unmarshal(<-result, &response); err != nil {
		t.Fatal(err)
	}
	var probe assistant.ProbeResult
	if err := json.Unmarshal(response.Result, &probe); err != nil {
		t.Fatal(err)
	}
	if !probe.OK || probe.Engine != assistant.EnginePydantic || probe.RuntimeVersion != "manifest-v1" || einoCalls.Load() != 0 {
		t.Fatalf("probe changed owner mid-flight: %+v", probe)
	}
	status, code := decodeAgentStatus(t, jsonrpcCall(t, h.conn, "agent.status", nil))
	if code != 0 || status.LastProbe != nil || status.Engine == nil || *status.Engine != assistant.EngineEino {
		t.Fatalf("other engine certified by old probe: %+v code=%d", status, code)
	}
	enabled.Store(true)
	status, code = decodeAgentStatus(t, jsonrpcCall(t, h.conn, "agent.status", nil))
	if code != 0 || status.LastProbe == nil {
		t.Fatalf("matching selected engine lost probe: %+v code=%d", status, code)
	}
	version.Store("manifest-v2")
	status, code = decodeAgentStatus(t, jsonrpcCall(t, h.conn, "agent.status", nil))
	if code != 0 || status.LastProbe != nil {
		t.Fatalf("new runtime certified by old probe: %+v code=%d", status, code)
	}
}

func TestAgentStatusUnavailableRuntimeRetainsRoleAndCredentialPrecedence(t *testing.T) {
	client := &stubAssistantClient{}
	selector := assistant.NewEngineSelector(
		assistant.Engine{ID: assistant.EngineEino, Client: client},
		assistant.Engine{ID: assistant.EnginePydantic, Client: client},
		func(context.Context) (bool, error) { return true, nil },
		func(context.Context) (string, error) { return "", errors.New("runtime not installed") },
	)
	h := newAssistantHarnessWith(t, client, nil, WithAssistantEngines(selector))
	h.setupAndUnseal()
	status, code := decodeAgentStatus(t, jsonrpcCall(t, h.conn, "agent.status", nil))
	if code != 0 || status.Answering.Reason == nil || *status.Answering.Reason != reasonNoEndpoints {
		t.Fatalf("runtime hid missing endpoint: %+v code=%d", status, code)
	}
	ep := h.createEndpoint(t, testEndpointParams())
	h.mustSetDefault(ep.ID, "qwen3")
	status, code = decodeAgentStatus(t, jsonrpcCall(t, h.conn, "agent.status", nil))
	if code != 0 || status.Answering.Ready || status.Answering.Reason == nil || *status.Answering.Reason != reasonRuntimeUnavailable || status.Engine == nil || *status.Engine != assistant.EnginePydantic || status.RuntimeVersion != nil {
		t.Fatalf("unavailable runtime guessed a usable engine: %+v code=%d", status, code)
	}
}
