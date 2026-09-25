package client_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// lifecycle-entered's contract (nocx-2v80t.3.28 gave the op the entry's
// identity, so its shape moved and its schema is written with it): the real
// session service, over the real socket, answering the op a coordinator
// actually sends. The params are validated encoder-side and the result as the
// raw bytes this call received — the same split, for the same reason, as
// TestLifecycleCompleteOverTheWireConformsToContract.
func TestLifecycleEnteredOverTheWireConformsToContract(t *testing.T) {
	c, rec := hostedSessionsRecording(t)
	params := loadHelperSchema(t, "session.lifecycle-entered.params.schema.json")
	result := loadHelperSchema(t, "session.lifecycle-entered.schema.json")

	var spawned proto.SpawnResult
	if err := c.Call(context.Background(), proto.ServiceSession, proto.OpSpawn,
		proto.SpawnParams{Cwd: "/", Cols: 80, Rows: 24}, &spawned); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	in := proto.LifecycleEnteredParams{
		Session: spawned.Entry.Session,
		Incarnation: proto.Incarnation{
			Session:    spawned.Entry.Session.Session,
			Generation: 1,
		},
		Entry: "dom-child",
	}
	var out proto.LifecycleEnteredResult
	if err := c.Call(context.Background(), proto.ServiceSession, proto.OpLifecycleEntered, in, &out); err != nil {
		t.Fatalf("lifecycle-entered: %v", err)
	}

	if err := validateHelperJSON(params, mustMarshal(t, in)); err != nil {
		t.Fatalf("lifecycle-entered params do not satisfy the contract: %v", err)
	}
	if err := validateHelperJSON(result, rec.lastResult(t)); err != nil {
		t.Fatalf("the bytes the real lifecycle-entered answered with do not satisfy the contract: %v", err)
	}
}

// Exact or decoration: the schema refuses a field nobody declared, and an
// entry with no identity, which the helper refuses too.
func TestTheLifecycleEnteredContractRefusesAnUndeclaredFieldAndAnEmptyEntry(t *testing.T) {
	params := loadHelperSchema(t, "session.lifecycle-entered.params.schema.json")
	base := func() map[string]any {
		return map[string]any{
			"session":     map[string]any{"generation": "testhash", "session": "0123456789abcdef0123456789abcdef"},
			"incarnation": map[string]any{"session": "0123456789abcdef0123456789abcdef", "generation": 1},
			"entry":       "dom-child",
		}
	}
	valid, err := json.Marshal(base())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err = validateHelperJSON(params, valid); err != nil {
		t.Fatalf("the contract refused a well-formed entry: %v", err)
	}

	forged := base()
	forged["argv"] = []string{"rm", "-rf", "/"}
	raw, err := json.Marshal(forged)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err = validateHelperJSON(params, raw); err == nil {
		t.Fatal("the contract accepted an undeclared field; additionalProperties is not holding")
	}

	empty := base()
	empty["entry"] = ""
	raw, err = json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err = validateHelperJSON(params, raw); err == nil {
		t.Fatal("the contract accepted an entry with no identity")
	}
}
