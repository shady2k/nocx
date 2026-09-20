package client_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// The check a payload the test itself built cannot make: the real session
// service, over the real socket, answering the op a coordinator actually
// sends. contracts/README.md's third check, and the only one that can catch a
// field the helper never sends or one it sends undeclared.
//
// The RESULT is validated as the raw bytes this very call received off the
// socket (recordingConn.lastResult), which is the half a test-built payload
// cannot fake. The PARAMS are validated encoder-side, because nothing here
// records request frames; what closes that gap is the refusal test below,
// which asks the schema itself whether it would accept a field nobody
// declared — the property that makes this a contract rather than decoration
// (contracts/helper/README.md: the peer may be a generation months old, and a
// shape that was wrong when it shipped stays wrong for the life of the
// sessions it holds).
func TestLifecycleCompleteOverTheWireConformsToContract(t *testing.T) {
	c, rec := hostedSessionsRecording(t)
	params := loadHelperSchema(t, "session.lifecycle-complete.params.schema.json")
	result := loadHelperSchema(t, "session.lifecycle-complete.schema.json")

	var spawned proto.SpawnResult
	if err := c.Call(context.Background(), proto.ServiceSession, proto.OpSpawn,
		proto.SpawnParams{Cwd: "/", Cols: 80, Rows: 24}, &spawned); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	exit := 0
	in := proto.LifecycleCompleteParams{
		Session: spawned.Entry.Session,
		Incarnation: proto.Incarnation{
			Session:    spawned.Entry.Session.Session,
			Generation: 1,
		},
		Nonce:    hex.EncodeToString(make([]byte, 32)),
		ExitCode: &exit,
	}
	var out proto.LifecycleCompleteResult
	if err := c.Call(context.Background(), proto.ServiceSession, proto.OpLifecycleComplete, in, &out); err != nil {
		t.Fatalf("lifecycle-complete: %v", err)
	}

	if err := validateHelperJSON(params, mustMarshal(t, in)); err != nil {
		t.Fatalf("lifecycle-complete params do not satisfy the contract: %v", err)
	}
	if err := validateHelperJSON(result, rec.lastResult(t)); err != nil {
		t.Fatalf("the bytes the real lifecycle-complete answered with do not satisfy the contract: %v", err)
	}
}

// The contract is exact or it is decoration: additionalProperties false plus
// an explicit required list is what makes it a contract at all, and a schema
// that accepts a field nobody declared would accept a generation inventing
// one. Hand-built bytes are the only way to ask, because the typed encoder
// cannot produce them.
func TestTheLifecycleCompleteContractRefusesAnUndeclaredField(t *testing.T) {
	params := loadHelperSchema(t, "session.lifecycle-complete.params.schema.json")
	forged := map[string]any{
		"session":     map[string]any{"generation": "testhash", "session": "0123456789abcdef0123456789abcdef"},
		"incarnation": map[string]any{"session": "0123456789abcdef0123456789abcdef", "generation": 1},
		"nonce":       hex.EncodeToString(make([]byte, 32)),
		"exitCode":    nil,
		"argv":        []string{"rm", "-rf", "/"},
	}
	raw, err := json.Marshal(forged)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := validateHelperJSON(params, raw); err == nil {
		t.Fatal("the contract accepted an undeclared field; additionalProperties is not holding")
	}
}
