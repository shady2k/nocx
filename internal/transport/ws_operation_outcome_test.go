package transport

// Mutation identity over the real socket (ADR-8eej3e66 §4): a call carries an
// identity, the backend retains what became of it, operation.outcome answers
// for it, and a second invocation of a mutation the record says ran is refused
// before admission. The failure this exists to prevent is a double effect from
// a client that was not sure, so the refusal is proven at the seam a client
// actually reaches — the request frame.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/waittest"
)

// mutationCall sends a request whose envelope carries an identity, and returns
// its response envelope.
func mutationCall(t *testing.T, conn *websocket.Conn, id int, method string, params any, mutation string) map[string]any {
	t.Helper()
	request := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if mutation != "" {
		request["mutation"] = mutation
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if werr := conn.WriteMessage(websocket.TextMessage, raw); werr != nil {
		t.Fatalf("write request: %v", werr)
	}
	frame, err := awaitFrame(conn, time.Now().Add(wantWithin), isResponseTo(id))
	if err != nil {
		t.Fatalf("read response to %s (id %d): %v", method, id, err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(frame, &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return envelope
}

// sendMutation writes the request without waiting for its response, for the
// calls a test withdraws or leaves running.
func sendMutation(t *testing.T, conn *websocket.Conn, id int, method string, params any, mutation string) {
	t.Helper()
	request := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if mutation != "" {
		request["mutation"] = mutation
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if werr := conn.WriteMessage(websocket.TextMessage, raw); werr != nil {
		t.Fatalf("write request: %v", werr)
	}
}

// askOutcome asks operation.outcome over the socket and returns its result.
func askOutcome(t *testing.T, conn *websocket.Conn, id int, mutation string) (string, string) {
	t.Helper()
	envelope := mutationCall(t, conn, id, "operation.outcome", map[string]any{"mutation": mutation}, "")
	raw, err := json.Marshal(envelope["result"])
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	var result operationOutcomeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode operation.outcome result %s: %v", raw, err)
	}
	return result.Outcome, result.Operation
}

func TestAMutationAnswersDoneAndIsNeverAcceptedTwice(t *testing.T) {
	_, logger := logtest.New(t)
	server := NewWSServer(logger, newRegWithStub(logger))
	server.methods["test.mutation"] = reg(
		control.AsyncSubmission{},
		catalog.OpSettingsSet,
		noParams(),
		func(_ *wsConn, _ *connState, r Responder) handlerFunc {
			return func(_ context.Context, req jsonrpcRequest) {
				_ = r.TryResult(req.ID, json.RawMessage(`{"saved":true}`))
			}
		},
	)
	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(ctx) })
	conn := connectWS(t, server)
	t.Cleanup(func() { _ = conn.Close() })

	if envelope := mutationCall(t, conn, 1, "test.mutation", map[string]any{}, "m-done"); envelope["error"] != nil {
		t.Fatalf("the first mutation was refused: %+v", envelope["error"])
	}
	if outcome, operation := askOutcome(t, conn, 2, "m-done"); outcome != string(catalog.AnswerDone) || operation != "settings.set" {
		t.Fatalf("outcome = %q for %q, want done for settings.set", outcome, operation)
	}

	// The replay. The identity is the backend's only way to know this ran, so
	// the refusal is the guarantee, not a client convention.
	envelope := mutationCall(t, conn, 3, "test.mutation", map[string]any{}, "m-done")
	failure, ok := envelope["error"].(map[string]any)
	if !ok {
		t.Fatalf("a second invocation of a finished mutation was accepted: %s", mustMarshal(envelope))
	}
	if code, _ := failure["code"].(float64); int(code) != MutationReplayErrorCode {
		t.Fatalf("code = %v, want %d", failure["code"], MutationReplayErrorCode)
	}
	data, _ := failure["data"].(map[string]any)
	if data["reason"] != mutationReplayReason || data["outcome"] != string(catalog.AnswerDone) || data["operation"] != "settings.set" {
		t.Fatalf("refusal data = %+v, want mutation-replayed/done/settings.set", data)
	}
}

func TestAMutationRefusedBeforeItStartsAnswersNotStarted(t *testing.T) {
	_, logger := logtest.New(t)
	server := NewWSServer(logger, newRegWithStub(logger))
	// A submission closed before the call arrives does not run it: the work
	// never starts, and the outcome says so.
	refusing := control.NewOrderedSubmission("test-mutation-refused")
	if shutdownable, ok := refusing.(control.Shutdownable); ok {
		shutdownable.Shutdown()
	}
	server.methods["test.mutation"] = reg(
		refusing,
		catalog.OpSettingsSet,
		noParams(),
		func(_ *wsConn, _ *connState, r Responder) handlerFunc {
			return func(_ context.Context, req jsonrpcRequest) {
				_ = r.TryResult(req.ID, json.RawMessage(`{}`))
			}
		},
	)
	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(ctx) })
	conn := connectWS(t, server)
	t.Cleanup(func() { _ = conn.Close() })

	envelope := mutationCall(t, conn, 1, "test.mutation", map[string]any{}, "m-refused")
	if envelope["error"] == nil {
		t.Fatal("a refused submission answered successfully")
	}
	if outcome, _ := askOutcome(t, conn, 2, "m-refused"); outcome != string(catalog.AnswerNotStarted) {
		t.Fatalf("outcome = %q, want not-started", outcome)
	}
}

func TestAMutationReplayedWhileItRunsIsRefused(t *testing.T) {
	_, logger := logtest.New(t)
	server := NewWSServer(logger, newRegWithStub(logger))
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	server.methods["test.mutation"] = reg(
		control.AsyncSubmission{},
		catalog.OpSettingsSet,
		noParams(),
		func(_ *wsConn, _ *connState, r Responder) handlerFunc {
			return func(_ context.Context, req jsonrpcRequest) {
				entered <- struct{}{}
				<-release
				_ = r.TryResult(req.ID, json.RawMessage(`{}`))
			}
		},
	)
	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(ctx) })
	conn := connectWS(t, server)
	t.Cleanup(func() { _ = conn.Close() })

	sendMutation(t, conn, 1, "test.mutation", map[string]any{}, "m-running")
	select {
	case <-entered:
	case <-time.After(wantWithin):
		t.Fatal("the mutation never started")
	}
	// While the first copy runs, the same identity is "checking what
	// happened", and a second run is exactly the double effect the record
	// refuses.
	if outcome, _ := askOutcome(t, conn, 2, "m-running"); outcome != string(catalog.AnswerUnknown) {
		t.Fatalf("outcome while running = %q, want outcome-unknown", outcome)
	}
	envelope := mutationCall(t, conn, 3, "test.mutation", map[string]any{}, "m-running")
	failure, ok := envelope["error"].(map[string]any)
	if !ok {
		t.Fatalf("a mutation replayed while it runs was accepted: %s", mustMarshal(envelope))
	}
	if code, _ := failure["code"].(float64); int(code) != MutationReplayErrorCode {
		t.Fatalf("code = %v, want %d", failure["code"], MutationReplayErrorCode)
	}
	close(release)
	waittest.WaitFor(t, "the running mutation to record its answer", func() bool {
		outcome, _ := askOutcome(t, conn, 4, "m-running")
		return outcome == string(catalog.AnswerDone)
	})
}

func TestASealedRefusalLeavesTheMutationNotStartedAndItsRetryRuns(t *testing.T) {
	_, logger := logtest.New(t)
	server := NewWSServer(logger, newRegWithStub(logger))
	runs := 0
	server.methods["test.mutation"] = reg(
		control.AsyncSubmission{},
		catalog.OpVaultUnseal,
		noParams(),
		func(_ *wsConn, _ *connState, r Responder) handlerFunc {
			return func(_ context.Context, req jsonrpcRequest) {
				runs++
				if runs == 1 {
					// The shape every sealed failure is normalized to: the
					// vault refused before the operation touched a store, so
					// ADR-0032's unlock replay may run it again.
					_ = r.TryError(req.ID, RPCError{Code: -32001, Message: "vault is sealed"})
					return
				}
				_ = r.TryResult(req.ID, json.RawMessage(`{"unsealed":true}`))
			}
		},
	)
	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(ctx) })
	conn := connectWS(t, server)
	t.Cleanup(func() { _ = conn.Close() })

	first := mutationCall(t, conn, 1, "test.mutation", map[string]any{}, "m-sealed")
	if first["error"] == nil {
		t.Fatal("the sealed refusal never reached the wire")
	}
	if outcome, operation := askOutcome(t, conn, 2, "m-sealed"); outcome != string(catalog.AnswerNotStarted) || operation != "vault.unseal" {
		t.Fatalf("outcome = %q for %q, want not-started for vault.unseal", outcome, operation)
	}
	// The one replay the record sanctions, under the same identity.
	second := mutationCall(t, conn, 3, "test.mutation", map[string]any{}, "m-sealed")
	if second["error"] != nil {
		t.Fatalf("the sanctioned retry was refused: %+v", second["error"])
	}
	if runs != 2 {
		t.Fatalf("the handler ran %d times, want 2", runs)
	}
	if outcome, _ := askOutcome(t, conn, 4, "m-sealed"); outcome != string(catalog.AnswerDone) {
		t.Fatalf("outcome after the retry = %q, want done", outcome)
	}
}

func TestAWithdrawnMutationAnswersOneOfThreeAndIsNeverReplayedItself(t *testing.T) {
	_, logger := logtest.New(t)
	server := NewWSServer(logger, newRegWithStub(logger))
	entered := make(chan struct{}, 4)
	server.methods["test.mutation"] = reg(
		control.AsyncSubmission{},
		catalog.OpSettingsSet,
		noParams(),
		func(_ *wsConn, _ *connState, r Responder) handlerFunc {
			return func(ctx context.Context, _ jsonrpcRequest) {
				entered <- struct{}{}
				// It stops because its context ended, and it writes nothing:
				// exactly a call whose response the caller gave up on.
				<-ctx.Done()
			}
		},
	)
	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(ctx) })
	conn := connectWS(t, server)
	t.Cleanup(func() { _ = conn.Close() })

	sendMutation(t, conn, 1, "test.mutation", map[string]any{}, "m-withdrawn")
	select {
	case <-entered:
	case <-time.After(wantWithin):
		t.Fatal("the mutation never started")
	}
	if response := jsonrpcCallWithID(t, conn, "rpc.cancel", map[string]any{"id": 1}, 2); response == nil {
		t.Fatal("rpc.cancel answered nothing")
	}
	waittest.WaitFor(t, "the withdrawn mutation to record its unknown outcome", func() bool {
		outcome, _ := askOutcome(t, conn, 3, "m-withdrawn")
		return outcome == string(catalog.AnswerUnknown)
	})
	// "Checking what happened" is not a licence to try again: the work ran.
	envelope := mutationCall(t, conn, 4, "test.mutation", map[string]any{}, "m-withdrawn")
	if envelope["error"] == nil {
		t.Fatal("a mutation whose outcome is unknown was accepted again")
	}
}

func TestAnIdentityOnAReadRetainsNothing(t *testing.T) {
	_, logger := logtest.New(t)
	server := NewWSServer(logger, newRegWithStub(logger))
	server.methods["test.read"] = reg(
		control.AsyncSubmission{},
		catalog.OpVaultStatus,
		noParams(),
		func(_ *wsConn, _ *connState, r Responder) handlerFunc {
			return func(_ context.Context, req jsonrpcRequest) {
				_ = r.TryResult(req.ID, json.RawMessage(`{}`))
			}
		},
	)
	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(ctx) })
	conn := connectWS(t, server)
	t.Cleanup(func() { _ = conn.Close() })

	if envelope := mutationCall(t, conn, 1, "test.read", map[string]any{}, "r-1"); envelope["error"] != nil {
		t.Fatalf("a read carrying an identity was refused: %+v", envelope["error"])
	}
	if outcome, operation := askOutcome(t, conn, 2, "r-1"); outcome != string(catalog.AnswerNoRecord) || operation != "" {
		t.Fatalf("outcome = %q for %q, want no record", outcome, operation)
	}
}

func TestAnIdentityBoundedByLengthAndNoRecordAnswersUnknown(t *testing.T) {
	_, logger := logtest.New(t)
	server := NewWSServer(logger, newRegWithStub(logger))
	server.methods["test.mutation"] = reg(
		control.AsyncSubmission{},
		catalog.OpSettingsSet,
		noParams(),
		func(_ *wsConn, _ *connState, r Responder) handlerFunc {
			return func(_ context.Context, req jsonrpcRequest) {
				_ = r.TryResult(req.ID, json.RawMessage(`{}`))
			}
		},
	)
	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(ctx) })
	conn := connectWS(t, server)
	t.Cleanup(func() { _ = conn.Close() })

	over := strings.Repeat("x", MutationIdentityLimit+1)
	envelope := mutationCall(t, conn, 1, "test.mutation", map[string]any{}, over)
	failure, ok := envelope["error"].(map[string]any)
	if !ok {
		t.Fatal("an over-long identity was accepted")
	}
	if code, _ := failure["code"].(float64); int(code) != -32602 {
		t.Fatalf("code = %v, want -32602", failure["code"])
	}
	if outcome, _ := askOutcome(t, conn, 2, "never-seen"); outcome != string(catalog.AnswerNoRecord) {
		t.Fatalf("an identity never seen answers %q, want unknown", outcome)
	}
}

// TestOperationOutcomeOverTheWireConformsToContract validates the REAL result
// off the REAL socket: a struct that marshals correctly proves nothing about
// what the handler sends.
func TestOperationOutcomeOverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "operation.outcome.schema.json")
	_, logger := logtest.New(t)
	server := NewWSServer(logger, newRegWithStub(logger))
	server.methods["test.mutation"] = reg(
		control.AsyncSubmission{},
		catalog.OpSettingsSet,
		noParams(),
		func(_ *wsConn, _ *connState, r Responder) handlerFunc {
			return func(_ context.Context, req jsonrpcRequest) {
				_ = r.TryResult(req.ID, json.RawMessage(`{}`))
			}
		},
	)
	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(ctx) })
	conn := connectWS(t, server)
	t.Cleanup(func() { _ = conn.Close() })

	if envelope := mutationCall(t, conn, 1, "test.mutation", map[string]any{}, "m-contract"); envelope["error"] != nil {
		t.Fatalf("the mutation was refused: %+v", envelope["error"])
	}
	envelope := mutationCall(t, conn, 2, "operation.outcome", map[string]any{"mutation": "m-contract"}, "")
	validateJSON(t, schema, mustMarshal(envelope["result"]), "operation.outcome result")
	envelope = mutationCall(t, conn, 3, "operation.outcome", map[string]any{"mutation": "no-such-mutation"}, "")
	validateJSON(t, schema, mustMarshal(envelope["result"]), "operation.outcome result")
}
