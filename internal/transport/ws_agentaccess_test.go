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
	// And the shape is a MACHINE and not an empty object: a test that compared
	// three absences would pass over a wire that carried no machine at all.
	// Both branches are checked, because they are what makes the contract
	// exact — a local machine carries nothing else, an ssh one carries all
	// three — and a copied union with the requirements dropped from one branch
	// would still compare identical to itself.
	var machine map[string]any
	if err := json.Unmarshal([]byte(canonical[0]), &machine); err != nil {
		t.Fatalf("machine shape: %v", err)
	}
	branches, ok := machine["oneOf"].([]any)
	if !ok || len(branches) != 2 {
		t.Fatalf("the machine is not a two-branch union: %s", canonical[0])
	}
	requiredBy := map[string][]string{}
	for _, raw := range branches {
		branch, isObject := raw.(map[string]any)
		if !isObject {
			t.Fatalf("a machine branch is not an object: %v", raw)
		}
		props, isObject := branch["properties"].(map[string]any)
		if !isObject {
			t.Fatalf("a machine branch has no properties: %v", branch)
		}
		kind, isObject := props["kind"].(map[string]any)
		if !isObject {
			t.Fatalf("a machine branch has no kind: %v", branch)
		}
		constant, isString := kind["const"].(string)
		if !isString {
			t.Fatalf("a machine branch's kind is not a constant: %v", kind)
		}
		required, _ := branch["required"].([]any)
		for _, entry := range required {
			name, isString := entry.(string)
			if !isString {
				t.Fatalf("a required entry is not a string: %v", entry)
			}
			requiredBy[constant] = append(requiredBy[constant], name)
		}
	}
	local, hasLocal := requiredBy["local"]
	ssh, hasSSH := requiredBy["ssh"]
	if !hasLocal || !hasSSH {
		t.Fatalf("the machine's branches are not local and ssh: %v", requiredBy)
	}
	if len(local) != 1 || local[0] != "kind" {
		t.Fatalf("a local machine requires %v, want kind alone", local)
	}
	want := map[string]bool{"kind": true, "host": true, "account": true, "hostKey": true}
	if len(ssh) != len(want) {
		t.Fatalf("an ssh machine requires %v, want all four facts", ssh)
	}
	for _, name := range ssh {
		if !want[name] {
			t.Fatalf("an ssh machine requires %q, which is not one of its four facts", name)
		}
	}
}

// The machine contract is EXACT, and this is the half the Go validator cannot
// hold: a local machine may not carry a host, and an ssh one may not omit one.
// Without it the schema would accept `{"kind":"ssh"}` while
// validateMachineFacts refuses it — two sources of truth disagreeing about the
// same payload, and a renderer whose generated type promises narrowing the
// wire does not guarantee (nocx-50w7p.16). The validator's own half is
// TestAgentAccessForget_RefusesAMachineThatCannotBeDerived, over the same
// cases.
func TestTheMachineContractIsExact(t *testing.T) {
	schema := loadSchema(t, "agentAccess.forget.params.schema.json")
	const head = `{"executable":"/usr/bin/claude","digest":"` + "55640c4f3b8769e625c91e6aeaac3032c713a8bd0b83e04c9265772d7cb40825" + `","workspace":"default"`
	cases := []struct {
		name    string
		machine string
		valid   bool
	}{
		{"the machine this backend runs on", `{"kind":"local"}`, true},
		{"an ssh machine with all three facts", `{"kind":"ssh","host":"build.example.com","account":"deploy","hostKey":"SHA256:key-a"}`, true},
		{"no machine at all", ``, false},
		{"a kind nobody derives", `{"kind":"container"}`, false},
		{"an empty kind", `{"kind":""}`, false},
		{"an ssh machine with no host", `{"kind":"ssh","account":"deploy","hostKey":"SHA256:k"}`, false},
		{"an ssh machine with no account", `{"kind":"ssh","host":"build.example.com","hostKey":"SHA256:k"}`, false},
		{"an ssh machine with no host key", `{"kind":"ssh","host":"build.example.com","account":"deploy"}`, false},
		{"a local machine carrying a host", `{"kind":"local","host":"build.example.com"}`, false},
		{"a machine with a field nobody declared", `{"kind":"local","fingerprint":"SHA256:k"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := head
			if tc.machine != "" {
				params += `,"machine":` + tc.machine
			}
			params += `}`
			err := validateJSONErr(schema, []byte(params))
			if tc.valid && err != nil {
				t.Fatalf("the schema refused a valid machine: %v", err)
			}
			if !tc.valid && err == nil {
				t.Fatalf("the schema accepted %s", params)
			}
		})
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
