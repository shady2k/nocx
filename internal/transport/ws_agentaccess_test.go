package transport

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/log"
)

// A store a test drives by hand. The real one is internal/agentapproval
// behind internal/app's adapter; what this file is about is the wire.
type fakeAgentAccess struct {
	records   []AgentAccessRecord
	forgotten []string
	answer    bool
	err       error
}

func (f *fakeAgentAccess) ListAgentAccess() []AgentAccessRecord { return f.records }

func (f *fakeAgentAccess) ForgetAgentAccess(executable, digest, workspace string) (bool, error) {
	f.forgotten = append(f.forgotten, executable+"|"+digest+"|"+workspace)
	return f.answer, f.err
}

const testDigest = "55640c4f3b8769e625c91e6aeaac3032c713a8bd0b83e04c9265772d7cb40825"

func agentAccessConnection(t *testing.T, store AgentAccessStore) (*websocket.Conn, func()) {
	t.Helper()
	opts := []WSServerOption{}
	if store != nil {
		opts = append(opts, WithAgentAccess(store))
	}
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), opts...)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn := connectWS(t, ws)
	return conn, func() { _ = conn.Close(); _ = ws.Stop(ctx) }
}

// The real list, off the real socket. A payload the test built itself would
// prove the struct is well-formed, not that the server sends it.
func TestAgentAccessList_OverTheWireConformsToContract(t *testing.T) {
	store := &fakeAgentAccess{records: []AgentAccessRecord{
		{Executable: "/run/current-system/sw/bin/claude", Digest: testDigest, Workspace: "default", Answer: "denied"},
	}}
	conn, cleanup := agentAccessConnection(t, store)
	defer cleanup()

	resp := jsonrpcCall(t, conn, "agentAccess.list", map[string]any{})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error != nil {
		t.Fatalf("agentAccess.list: %+v", env.Error)
	}
	validateJSON(t, loadSchema(t, "agentAccess.list.schema.json"), env.Result, "agentAccess.list wire")

	var result struct {
		Answers []struct {
			Executable string `json:"executable"`
			Digest     string `json:"digest"`
			Workspace  string `json:"workspace"`
			Answer     string `json:"answer"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Answers) != 1 {
		t.Fatalf("answers = %d, want 1", len(result.Answers))
	}
	got := result.Answers[0]
	if got.Executable != "/run/current-system/sw/bin/claude" || got.Digest != testDigest ||
		got.Workspace != "default" || got.Answer != "denied" {
		t.Fatalf("answer = %+v, want the record the store holds", got)
	}
}

// The empty list is a list, not an absence: a person who has decided nothing
// must see a page saying so rather than an error.
func TestAgentAccessList_EmptyIsAnAnswer(t *testing.T) {
	conn, cleanup := agentAccessConnection(t, &fakeAgentAccess{})
	defer cleanup()

	resp := jsonrpcCall(t, conn, "agentAccess.list", map[string]any{})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error != nil {
		t.Fatalf("agentAccess.list: %+v", env.Error)
	}
	validateJSON(t, loadSchema(t, "agentAccess.list.schema.json"), env.Result, "agentAccess.list wire")
	if string(env.Result) != `{"answers":[]}` {
		t.Fatalf("result = %s, want an empty array and not null", env.Result)
	}
}

func TestAgentAccessForget_OverTheWireConformsToContract(t *testing.T) {
	store := &fakeAgentAccess{answer: true}
	conn, cleanup := agentAccessConnection(t, store)
	defer cleanup()

	resp := jsonrpcCall(t, conn, "agentAccess.forget", map[string]any{
		"executable": "/run/current-system/sw/bin/claude",
		"digest":     testDigest,
		"workspace":  "default",
	})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error != nil {
		t.Fatalf("agentAccess.forget: %+v", env.Error)
	}
	validateJSON(t, loadSchema(t, "agentAccess.forget.schema.json"), env.Result, "agentAccess.forget wire")
	if string(env.Result) != `{"forgotten":true}` {
		t.Fatalf("result = %s, want forgotten:true", env.Result)
	}
	want := "/run/current-system/sw/bin/claude|" + testDigest + "|default"
	if len(store.forgotten) != 1 || store.forgotten[0] != want {
		t.Fatalf("store was asked to forget %v, want [%s]", store.forgotten, want)
	}
}

// Forgetting what is not there is what the caller asked for. A second click,
// or a page whose read predates somebody else's forget, is not an error.
func TestAgentAccessForget_NothingToForgetIsSuccess(t *testing.T) {
	conn, cleanup := agentAccessConnection(t, &fakeAgentAccess{answer: false})
	defer cleanup()

	resp := jsonrpcCall(t, conn, "agentAccess.forget", map[string]any{
		"executable": "/usr/bin/codex", "digest": testDigest, "workspace": "default",
	})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error != nil {
		t.Fatalf("agentAccess.forget: %+v", env.Error)
	}
	if string(env.Result) != `{"forgotten":false}` {
		t.Fatalf("result = %s, want forgotten:false", env.Result)
	}
}

// A write that failed leaves the answer standing, and the caller is told so.
// Reporting forgotten:false here would tell a person their revocation landed
// while the agent is still admitted.
func TestAgentAccessForget_AFailedWriteIsAnError(t *testing.T) {
	conn, cleanup := agentAccessConnection(t, &fakeAgentAccess{err: errors.New("disk is gone")})
	defer cleanup()

	resp := jsonrpcCall(t, conn, "agentAccess.forget", map[string]any{
		"executable": "/usr/bin/claude", "digest": testDigest, "workspace": "default",
	})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error == nil {
		t.Fatalf("a failed write answered %s, want an error", env.Result)
	}
}

// No store is not an empty store, and the two must not look alike: a window
// that cannot see the document would otherwise draw "you have decided
// nothing" over a document full of decisions.
func TestAgentAccess_UnwiredSaysSoRatherThanAnsweringEmpty(t *testing.T) {
	conn, cleanup := agentAccessConnection(t, nil)
	defer cleanup()

	for _, method := range []string{"agentAccess.list", "agentAccess.forget"} {
		resp := jsonrpcCall(t, conn, method, map[string]any{
			"executable": "/usr/bin/claude", "digest": testDigest, "workspace": "default",
		})
		var env rpcEnvelope
		if err := json.Unmarshal(resp, &env); err != nil {
			t.Fatal(err)
		}
		if env.Error == nil {
			t.Fatalf("%s with no store answered %s, want an error", method, env.Result)
		}
	}
}

// The params are bounded at the edge, not inside the store.
func TestAgentAccessForget_RefusesAMalformedDigest(t *testing.T) {
	conn, cleanup := agentAccessConnection(t, &fakeAgentAccess{answer: true})
	defer cleanup()

	resp := jsonrpcCall(t, conn, "agentAccess.forget", map[string]any{
		"executable": "/usr/bin/claude", "digest": "not-a-digest", "workspace": "default",
	})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error == nil {
		t.Fatal("a malformed digest was accepted")
	}
}
