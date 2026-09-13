package transport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

	// lastMachine is the machine the last forget named, so the test can assert
	// the domain reached the seam rather than only that a call arrived.
	lastMachine MachineFacts
}

func (f *fakeAgentAccess) ListAgentAccess() []AgentAccessRecord { return f.records }

func (f *fakeAgentAccess) ForgetAgentAccess(executable, digest, workspace string, machine MachineFacts) (bool, error) {
	f.forgotten = append(f.forgotten, executable+"|"+digest+"|"+workspace)
	f.lastMachine = machine
	return f.answer, f.err
}

const testDigest = "55640c4f3b8769e625c91e6aeaac3032c713a8bd0b83e04c9265772d7cb40825"

// localMachine and sshMachine are the wire spelling of a machine, built the way
// a client would send it. Every forget request needs one: a row is addressed by
// its machine, so a request without one is not a request about a row.
var localMachine = map[string]any{"kind": "local"}

func sshMachine(host, account, hostKey string) map[string]any {
	return map[string]any{"kind": "ssh", "host": host, "account": account, "hostKey": hostKey}
}

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
		{
			Executable: "/run/current-system/sw/bin/claude", Digest: testDigest, Workspace: "default",
			Answer: "denied", Machine: MachineFacts{Kind: "ssh", Host: "build.example.com", Account: "deploy", HostKey: "SHA256:key-a"},
		},
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
			Machine    struct {
				Kind    string `json:"kind"`
				Host    string `json:"host"`
				Account string `json:"account"`
				HostKey string `json:"hostKey"`
			} `json:"machine"`
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
	// The machine travels, and it is what tells two rows apart: the surface
	// cannot offer to unmake an answer it cannot name the machine of.
	if got.Machine.Kind != "ssh" || got.Machine.Host != "build.example.com" ||
		got.Machine.Account != "deploy" || got.Machine.HostKey != "SHA256:key-a" {
		t.Fatalf("machine = %+v, want the record's own machine", got.Machine)
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
		"machine":    sshMachine("build.example.com", "deploy", "SHA256:key-a"),
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
	// The machine is what the store keys the answer by, so a request that
	// reached the seam without one would forget nothing at all.
	if store.lastMachine.Kind != "ssh" || store.lastMachine.Host != "build.example.com" ||
		store.lastMachine.Account != "deploy" || store.lastMachine.HostKey != "SHA256:key-a" {
		t.Fatalf("the machine that reached the store = %+v, want the one the caller named", store.lastMachine)
	}
}

// Forgetting what is not there is what the caller asked for. A second click,
// or a page whose read predates somebody else's forget, is not an error.
func TestAgentAccessForget_NothingToForgetIsSuccess(t *testing.T) {
	conn, cleanup := agentAccessConnection(t, &fakeAgentAccess{answer: false})
	defer cleanup()

	resp := jsonrpcCall(t, conn, "agentAccess.forget", map[string]any{
		"executable": "/usr/bin/codex", "digest": testDigest, "workspace": "default", "machine": localMachine,
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
		"executable": "/usr/bin/claude", "digest": testDigest, "workspace": "default", "machine": localMachine,
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
			"executable": "/usr/bin/claude", "digest": testDigest, "workspace": "default", "machine": localMachine,
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
		"executable": "/usr/bin/claude", "digest": "not-a-digest", "workspace": "default", "machine": localMachine,
	})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error == nil {
		t.Fatal("a malformed digest was accepted")
	}
}

// The machine shape is declared in three schemas — the approval ask, and both
// agentAccess ones — because each payload is described where it is used, and a
// $ref across them would be a named type generated into files nobody imports
// (the dead-export ratchet counts those). Three copies of one shape drift, and
// a drift here is a person reading one machine's answer under another's name,
// so the copies are HELD identical rather than trusted to stay so
// (nocx-50w7p.16). A change belongs in all three, which is what this fails on.
func TestTheMachineShapeIsDeclaredOnce(t *testing.T) {
	declarations := []struct{ file, path string }{
		{"host.request.schema.json", "properties.machine"},
		{"agentAccess.list.schema.json", "properties.answers.items.properties.machine"},
		{"agentAccess.forget.params.schema.json", "properties.machine"},
	}
	canonical := make([]string, 0, len(declarations))
	for _, decl := range declarations {
		raw, err := os.ReadFile(filepath.Join(contractDir, decl.file)) //nolint:gosec // test-only path under contracts/
		if err != nil {
			t.Fatalf("read %s: %v", decl.file, err)
		}
		var doc map[string]any
		if parseErr := json.Unmarshal(raw, &doc); parseErr != nil {
			t.Fatalf("parse %s: %v", decl.file, parseErr)
		}
		var node any = doc
		for _, key := range strings.Split(decl.path, ".") {
			obj, ok := node.(map[string]any)
			if !ok {
				t.Fatalf("%s: %s is not an object", decl.file, decl.path)
			}
			node, ok = obj[key]
			if !ok {
				t.Fatalf("%s declares no %s", decl.file, decl.path)
			}
		}
		// Marshalling a map sorts its keys, so this is a canonical form of the
		// subtree and not the file's whitespace.
		shape, err := json.Marshal(node)
		if err != nil {
			t.Fatalf("marshal %s: %v", decl.file, err)
		}
		canonical = append(canonical, string(shape))
	}
	for i := 1; i < len(canonical); i++ {
		if canonical[i] != canonical[0] {
			t.Fatalf("the machine shape in %s differs from %s:\n%s\n---\n%s",
				declarations[i].file, declarations[0].file, canonical[0], canonical[i])
		}
	}
	// And the shape is a machine and not an empty object: a test that compared
	// three absences would pass over a wire that carried no machine at all.
	var machine map[string]any
	if err := json.Unmarshal([]byte(canonical[0]), &machine); err != nil {
		t.Fatalf("machine shape: %v", err)
	}
	props, ok := machine["properties"].(map[string]any)
	if !ok {
		t.Fatalf("the declared machine has no properties: %s", canonical[0])
	}
	if _, ok := props["kind"]; !ok {
		t.Fatalf("the declared machine has no kind: %s", canonical[0])
	}
}

// A machine the backend could not have derived is refused at the edge. The row
// is ADDRESSED by these facts, so a partial or invented machine would either
// match nothing or, worse, match another machine's answer.
func TestAgentAccessForget_RefusesAMachineThatCannotBeDerived(t *testing.T) {
	for _, tc := range []struct {
		name    string
		machine any
	}{
		{"absent", nil},
		{"unknown kind", map[string]any{"kind": "container"}},
		{"ssh with no host", map[string]any{"kind": "ssh", "account": "deploy", "hostKey": "SHA256:k"}},
		{"ssh with no account", map[string]any{"kind": "ssh", "host": "h.example", "hostKey": "SHA256:k"}},
		{"ssh with no host key", map[string]any{"kind": "ssh", "host": "h.example", "account": "deploy"}},
		{"local carrying a host", map[string]any{"kind": "local", "host": "h.example"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeAgentAccess{answer: true}
			conn, cleanup := agentAccessConnection(t, store)
			defer cleanup()

			facts := map[string]any{
				"executable": "/usr/bin/claude", "digest": testDigest, "workspace": "default",
			}
			if tc.machine != nil {
				facts["machine"] = tc.machine
			}
			resp := jsonrpcCall(t, conn, "agentAccess.forget", facts)
			var env rpcEnvelope
			if err := json.Unmarshal(resp, &env); err != nil {
				t.Fatal(err)
			}
			if env.Error == nil {
				t.Fatalf("a %s machine was accepted: %s", tc.name, env.Result)
			}
			// And nothing reached the store: a refused request forgets
			// nothing, so a person is never told a revocation landed.
			if len(store.forgotten) != 0 {
				t.Fatalf("a refused request still reached the store: %v", store.forgotten)
			}
		})
	}
}
