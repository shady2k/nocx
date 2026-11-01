package assistant

import (
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

// A SECOND dispatcher over one registry reads the contracts the first one
// compiled. The value it receives is the SAME *jsonschema.Schema: a compile
// always produces a new one, so identity is what tells reuse from a
// recomputation that merely produced an equal schema. This is the whole of the
// per-process reuse, asserted where a caller can see it (nocx-atpw5.1.17).
func TestASecondDispatcherReusesTheCompiledContracts(t *testing.T) {
	reg, err := agenttools.Assemble(toolsDirFS(t))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	env := content.EnvironmentIDFor(content.EnvLocal, "")
	first, err := NewToolDispatcher(reg, &fakeWorkerRecord{}, env)
	if err != nil {
		t.Fatalf("first dispatcher: %v", err)
	}
	second, err := NewToolDispatcher(reg, &fakeWorkerRecord{}, env)
	if err != nil {
		t.Fatalf("second dispatcher: %v", err)
	}

	firstOp, ok := first.(*dispatchOperation)
	if !ok {
		t.Fatalf("first dispatcher is %T, want *dispatchOperation", first)
	}
	secondOp, ok := second.(*dispatchOperation)
	if !ok {
		t.Fatalf("second dispatcher is %T, want *dispatchOperation", second)
	}
	if len(firstOp.validators) == 0 {
		t.Fatal("the first dispatcher compiled no params schema, so reuse would be vacuous")
	}
	for name, compiled := range firstOp.validators {
		if reused := secondOp.validators[name]; reused != compiled {
			t.Errorf("tool %q: the second dispatcher compiled its own params schema rather than reading the first one's", name)
		}
	}
	if len(firstOp.results) == 0 {
		t.Fatal("the first dispatcher compiled no result schema, so reuse would be vacuous")
	}
	for name, compiled := range firstOp.results {
		if reused := secondOp.results[name]; reused != compiled {
			t.Errorf("tool %q: the second dispatcher compiled its own result schema rather than reading the first one's", name)
		}
	}
}

// A DIFFERENT contract is a different entry, never the cached one. The two
// documents below differ in the property they require, so what tells them apart
// is not the pointer alone but what each one accepts: the cache answers exactly
// the compile its own inputs name (nocx-atpw5.1.17).
func TestADifferentContractIsNotServedFromTheCache(t *testing.T) {
	const url = "https://nocx.local/contracts/tools/cache-probe.schema.json"
	requiresA := []byte(`{"type":"object","required":["a"],"properties":{"a":{"type":"string"}},"additionalProperties":false}`)
	requiresB := []byte(`{"type":"object","required":["b"],"properties":{"b":{"type":"string"}},"additionalProperties":false}`)

	first, err := compiledSchema(url, requiresA)
	if err != nil {
		t.Fatalf("compile the first contract: %v", err)
	}
	again, err := compiledSchema(url, requiresA)
	if err != nil {
		t.Fatalf("compile the first contract again: %v", err)
	}
	if again != first {
		t.Fatal("the same contract compiled twice: the second call did not reuse the first result")
	}

	other, err := compiledSchema(url, requiresB)
	if err != nil {
		t.Fatalf("compile the second contract: %v", err)
	}
	if other == first {
		t.Fatal("a different contract under the same URL was served the cached schema")
	}

	if err := first.Validate(documentFor(t, `{"a":"x"}`)); err != nil {
		t.Errorf("the first contract refused a document it requires: %v", err)
	}
	if err := first.Validate(documentFor(t, `{"b":"x"}`)); err == nil {
		t.Error("the first contract accepted a document only the second one requires, so the cached value is not the document's own")
	}
	if err := other.Validate(documentFor(t, `{"b":"x"}`)); err != nil {
		t.Errorf("the second contract refused a document it requires: %v", err)
	}
}

func documentFor(t *testing.T, raw string) any {
	t.Helper()
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("document %s: %v", raw, err)
	}
	return doc
}

// A contract that cannot be compiled is reported, never remembered: a caller
// that fixes the document must be able to see the fix in the same process.
func TestAFailedCompileIsNotRemembered(t *testing.T) {
	const url = "https://nocx.local/contracts/tools/cache-broken.schema.json"
	if _, err := compiledSchema(url, []byte(`{"type":`)); err == nil {
		t.Fatal("a truncated document compiled, so this test proves nothing")
	}
	good, err := compiledSchema(url, []byte(`{"type":"object"}`))
	if err != nil {
		t.Fatalf("compile the fixed contract under the URL that just failed: %v", err)
	}
	if good == nil {
		t.Fatal("the fixed contract compiled to nothing")
	}
}
