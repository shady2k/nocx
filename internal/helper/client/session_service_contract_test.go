package client_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

// The other half of the frozen helper ABI, and the half that could not be
// pinned before nocx-k6p18.3: the shapes of `spawn` and `sessions`.
//
// abi_contract_test.go's own note says why they are here and not there — its
// service could not be named proto.ServiceSession, because that name was
// reserved and unbuilt, so what it drives is the shapes and the transport
// rather than a service. This file drives the REAL session service, under the
// real name, over the real socket, with a real PTY behind it. It is the third
// of contracts/README.md's three checks and the only one that can catch a
// field the helper never sends.

// hostedSessions builds a helper peer whose session service is the shipped
// one, spawning a real shell. The shell is pinned rather than resolved because
// the assertion is about the SHAPE of the answer, not about which login shell
// this machine has — and pinning it is a composition-root choice, which is
// precisely where the frozen ABI says the choice belongs: no caller over the
// wire can name a program.
func hostedSessions(t *testing.T) *client.Client {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := session.New(session.Options{
		Generation: "testhash",
		Spawner:    session.NewLocalSpawner(log, session.Shell{Path: "/bin/sh"}),
		Inspector:  session.NewProcFS(),
		Log:        log,
		Limits:     session.DefaultLimits(),
	})
	t.Cleanup(svc.Close)

	conn := newFakeConn(func(in io.Reader, out io.Writer) int {
		h := hostFor(in, out, log)
		h.Register(svc)
		release := svc.Bind(h)
		defer release()
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	})
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestSpawnAndInventoryOverTheWireConformToTheirContracts is the check that a
// payload built by the test cannot make: the real service, marshalling a real
// PTY's real launch record, off the real socket.
func TestSpawnAndInventoryOverTheWireConformToTheirContracts(t *testing.T) {
	c := hostedSessions(t)

	spawnSchema := loadHelperSchema(t, "session.spawn.schema.json")
	spawnParams := loadHelperSchema(t, "session.spawn.params.schema.json")
	invSchema := loadHelperSchema(t, "session.sessions.schema.json")
	invParams := loadHelperSchema(t, "session.sessions.params.schema.json")

	// The params the test sends must themselves satisfy the contract, or the
	// result proves nothing about a payload anybody would actually send.
	in := proto.SpawnParams{Cwd: "/", Cols: 100, Rows: 30, WindowBytes: 1 << 20}
	if err := validateHelperJSON(spawnParams, mustMarshal(t, in)); err != nil {
		t.Fatalf("the spawn params the test sends do not satisfy the contract: %v", err)
	}

	var raw json.RawMessage
	if err := c.Call(context.Background(), proto.ServiceSession, proto.OpSpawn, in, &raw); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if err := validateHelperJSON(spawnSchema, raw); err != nil {
		t.Fatalf("the spawn result off the socket does not satisfy its contract:\n%v\n\npayload was:\n%s", err, raw)
	}

	invIn := proto.SessionsParams{}
	if err := validateHelperJSON(invParams, mustMarshal(t, invIn)); err != nil {
		t.Fatalf("the sessions params the test sends do not satisfy the contract: %v", err)
	}
	var invRaw json.RawMessage
	if err := c.Call(context.Background(), proto.ServiceSession, proto.OpSessions, invIn, &invRaw); err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if err := validateHelperJSON(invSchema, invRaw); err != nil {
		t.Fatalf("the inventory off the socket does not satisfy its contract:\n%v\n\npayload was:\n%s", err, invRaw)
	}

	// The entry the spawn answered with and the entry the inventory lists are
	// the same shape by construction — this asserts they are the same VALUE
	// for the identity that matters, so a caller cannot be handed one session
	// under two handles.
	var spawned proto.SpawnResult
	if err := json.Unmarshal(raw, &spawned); err != nil {
		t.Fatalf("decode spawn: %v", err)
	}
	var inv proto.SessionsResult
	if err := json.Unmarshal(invRaw, &inv); err != nil {
		t.Fatalf("decode inventory: %v", err)
	}
	if len(inv.Sessions) != 1 || inv.Sessions[0].Session != spawned.Entry.Session {
		t.Fatalf("the inventory does not list the session that was just spawned: %+v", inv.Sessions)
	}
}

// TestAnEmptyInventoryOffTheSocketIsAnEmptyArray is the case a hand-built
// fixture always gets right and a real encoder gets wrong: a nil Go slice
// marshals to `null`, and `null` is not an empty inventory. The coordinator's
// reconciliation turns an ANSWER into a deletion, so "no sessions" arriving as
// something a decoder has to special-case is exactly where a wrong verdict
// would come from. contracts' first run caught this same defect in
// vault.status's providers field.
func TestAnEmptyInventoryOffTheSocketIsAnEmptyArray(t *testing.T) {
	c := hostedSessions(t)

	var raw json.RawMessage
	if err := c.Call(context.Background(), proto.ServiceSession, proto.OpSessions, proto.SessionsParams{}, &raw); err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if string(raw) != `{"sessions":[]}` {
		t.Fatalf("an empty inventory came off the socket as %s", raw)
	}
	if err := validateHelperJSON(loadHelperSchema(t, "session.sessions.schema.json"), raw); err != nil {
		t.Fatalf("the empty inventory does not satisfy its contract: %v", err)
	}
}

// TestTheHelperRefusesASpawnCarryingArgv closes the loop on D3 at the wire: a
// caller that adds a command or an argument vector to the spawn params is
// refused by the CONTRACT, not merely by the absence of a field to put it in.
// additionalProperties:false is what makes that true, and a schema without it
// would accept the smuggled field and let a later generation decide to read it.
func TestTheHelperRefusesASpawnCarryingArgv(t *testing.T) {
	params := loadHelperSchema(t, "session.spawn.params.schema.json")
	for _, raw := range []string{
		`{"workspace":"","cwd":"/","cols":80,"rows":24,"windowBytes":0,"argv":["sh","-c","rm -rf /"]}`,
		`{"workspace":"","cwd":"/","cols":80,"rows":24,"windowBytes":0,"command":"/bin/sh"}`,
		`{"workspace":"","cwd":"/","cols":80,"rows":24,"windowBytes":0,"args":["-c","x"]}`,
	} {
		if err := validateHelperJSON(params, []byte(raw)); err == nil {
			t.Errorf("the contract accepted spawn params carrying a command line: %s", raw)
		}
	}
}

// TestTheInventoryContractRefusesAHumanAuthoredName is D3's naming decision
// made enforceable at the boundary where it would otherwise be reintroduced.
// A later generation that starts sending a `name` is refused by the schema
// rather than quietly becoming the owner of a concept the local server owns.
func TestTheInventoryContractRefusesAHumanAuthoredName(t *testing.T) {
	schema := loadHelperSchema(t, "session.sessions.schema.json")
	named := `{"sessions":[{` +
		`"session":{"generation":"G","session":"0123456789abcdef0123456789abcdef"},` +
		`"workspace":"","startedAt":"2026-08-31T00:00:00Z",` +
		`"launch":{"shell":"/bin/sh","cwd":"/","pid":1,"pgid":1,"cols":80,"rows":24,"windowBytes":4194304},` +
		`"observed":null,"window":{"base":0,"written":0},"writer":null,"writerEpoch":0,"exit":null,` +
		`"name":"my build"}]}`
	if err := validateHelperJSON(schema, []byte(named)); err == nil {
		t.Fatal("the contract accepted an inventory entry carrying a human-authored name")
	}
}
