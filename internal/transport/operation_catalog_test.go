package transport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/transport/control"
)

// TestEveryRegisteredMethodHasOneDeclaration is the counter-check to
// buildMethodSpecs: construction proves a registration HAS a handle, this
// proves the catalogue and the server describe the same method set, in both
// directions.
func TestEveryRegisteredMethodHasOneDeclaration(t *testing.T) {
	server := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	declared := map[string]catalog.Declaration{}
	for _, d := range catalog.Declarations() {
		if d.Kind == catalog.KindMethod {
			declared[string(d.ID)] = d
		}
	}
	for method := range server.methods {
		if _, ok := declared[method]; !ok {
			t.Errorf("registered method %q has no declaration", method)
		}
	}
	for method := range declared {
		if _, ok := server.methods[method]; !ok {
			t.Errorf("declared method %q is not registered", method)
		}
	}
}

func TestARegistrationWithoutADeclarationIsRefused(t *testing.T) {
	if _, err := buildMethodSpecs([]methodSpec{{
		method:     "planted.method",
		submission: control.NewBoundedSubmission(control.NewSemaphore("planted", 1)),
		validate:   noParams(),
	}}); err == nil {
		t.Fatal("a methodSpec with no catalogue handle must not build")
	}
}

func TestARegistrationWithTheWrongHandleIsRefused(t *testing.T) {
	if _, err := buildMethodSpecs([]methodSpec{{
		method:     "planted.method",
		op:         catalog.OpAck,
		submission: control.NewBoundedSubmission(control.NewSemaphore("planted", 1)),
		validate:   noParams(),
	}}); err == nil {
		t.Fatal("a methodSpec whose handle names another method must not build")
	}
}

type operationManifest struct {
	Operations []json.RawMessage `json:"operations"`
}

func manifestOperations(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(contractDir, "operations.json")) //nolint:gosec // repo-relative contract fixture
	if err != nil {
		t.Fatalf("read operations manifest: %v", err)
	}
	var manifest operationManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode operations manifest: %v", err)
	}
	out := map[string]json.RawMessage{}
	for _, op := range manifest.Operations {
		var head struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(op, &head); err != nil {
			t.Fatal(err)
		}
		out[head.ID] = op
	}
	return out
}

// TestOpenRPCManifestCarriesEveryDeclarationInFull compares the complete
// metadata, not the ids: a manifest entry with a stale deadline or a dropped
// phase is exactly the drift a method-set comparison cannot see.
func TestOpenRPCManifestCarriesEveryDeclarationInFull(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(contractDir, "openrpc.json")) //nolint:gosec // repo-relative contract fixture
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Methods []json.RawMessage `json:"methods"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	declarations := manifestOperations(t)
	seen := map[string]bool{}
	for _, methodRaw := range manifest.Methods {
		var entry struct {
			Name      string          `json:"name"`
			Operation json.RawMessage `json:"x-nocx-operation"`
		}
		if err := json.Unmarshal(methodRaw, &entry); err != nil {
			t.Fatal(err)
		}
		seen[entry.Name] = true
		if len(entry.Operation) == 0 {
			t.Errorf("%s has no x-nocx-operation", entry.Name)
			continue
		}
		assertSameMetadata(t, entry.Name, entry.Operation, declarations[entry.Name])
	}
	for id := range declarations {
		if strings.HasPrefix(id, "internal.") {
			continue // internal operations cross no wire and have no JSON-RPC method
		}
		if !seen[id] {
			t.Errorf("declaration %q is absent from the OpenRPC manifest", id)
		}
	}
}

func assertSameMetadata(t *testing.T, id string, got, want json.RawMessage) {
	t.Helper()
	if len(want) == 0 {
		t.Fatalf("%s: no operations.json entry", id)
	}
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("%s: OpenRPC metadata differs from the catalogue\n got: %v\nwant: %v", id, gotValue, wantValue)
	}
}

// TestAPlantedStaleManifestEntryIsCaught proves the comparison above refuses a
// changed field rather than only a missing entry.
func TestAPlantedStaleManifestEntryIsCaught(t *testing.T) {
	declarations := manifestOperations(t)
	var stale map[string]any
	if err := json.Unmarshal(declarations["ack"], &stale); err != nil {
		t.Fatal(err)
	}
	max, ok := stale["max"].(map[string]any)
	if !ok {
		t.Fatal("the fixture has no max object")
	}
	max["millis"] = float64(1)
	raw, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(json.RawMessage(raw), declarations["ack"]) {
		t.Fatal("the planted entry is identical; the fixture is wrong")
	}
	if jsonEqual(raw, declarations["ack"]) {
		t.Fatal("a changed max must compare unequal to the declared metadata")
	}
}

func jsonEqual(a, b []byte) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

// TestADispatchedInvocationCarriesItsScope proves the dispatch path, not only
// the catalog: a handler reached over the real socket sees the scope its
// catalogue handle began, and the scope settles when the handler returns.
func TestADispatchedInvocationCarriesItsScope(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	server := NewWSServer(logger, newRegWithStub(logger))
	seen := make(chan *catalog.Scope, 1)
	server.methods["test.scope"] = reg(
		control.NewBoundedSubmission(control.NewSemaphore("test-scope", 1)),
		catalog.OpFsComplete,
		noParams(),
		func(_ *wsConn, _ *connState, r Responder) handlerFunc {
			return func(ctx context.Context, req jsonrpcRequest) {
				scope, ok := catalog.ScopeFrom(ctx)
				if !ok {
					_ = r.TryError(req.ID, RPCError{Code: -32603, Message: "no scope in the invocation"})
					return
				}
				seen <- scope
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
	_ = jsonrpcCall(t, conn, "test.scope", nil)
	var scope *catalog.Scope
	select {
	case scope = <-seen:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never ran")
	}
	if got := scope.ID(); got != catalog.ID("fs.complete") {
		t.Fatalf("scope id = %q, want fs.complete", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if outcome, ok := scope.Outcome(); ok {
			if outcome != catalog.OutcomeCompleted {
				t.Fatalf("outcome = %q, want completed", outcome)
			}
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the dispatched invocation never settled")
}
