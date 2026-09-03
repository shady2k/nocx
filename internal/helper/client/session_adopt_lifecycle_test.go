package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// helperServing dials a client against one arbitrary service, which is how a
// test stands up a generation whose op set is not this build's.
func helperServing(t *testing.T, svc host.Service) *client.Client {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	conn := newFakeConn(func(in io.Reader, out io.Writer) int {
		h := hostFor(in, out, log)
		h.Register(svc)
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

// The check a payload built by the test cannot make: the real service, over
// the real socket, answering the question a replacing coordinator asks
// (nocx-k6p18.31).
func TestAdoptLifecycleOverTheWireConformsToContract(t *testing.T) {
	c := hostedSessions(t)
	params := loadHelperSchema(t, "session.adopt-lifecycle.params.schema.json")
	result := loadHelperSchema(t, "session.adopt-lifecycle.schema.json")

	var spawned proto.SpawnResult
	if err := c.Call(context.Background(), proto.ServiceSession, proto.OpSpawn,
		proto.SpawnParams{Cwd: "/", Cols: 80, Rows: 24}, &spawned); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	in := proto.AdoptLifecycleParams{Session: spawned.Entry.Session}
	if err := validateHelperJSON(params, mustMarshal(t, in)); err != nil {
		t.Fatalf("adopt-lifecycle params do not satisfy the contract: %v", err)
	}
	var raw proto.AdoptLifecycleResult
	if err := c.Call(context.Background(), proto.ServiceSession, proto.OpAdoptLifecycle, in, &raw); err != nil {
		t.Fatalf("adopt-lifecycle: %v", err)
	}
	if err := validateHelperJSON(result, mustMarshal(t, raw)); err != nil {
		t.Fatalf("the real adopt-lifecycle result does not satisfy the contract: %v", err)
	}

	// The typed client method reads the same answer as "no channel", which
	// is the answer a conventional spawn must produce.
	got, err := c.AdoptLifecycle(context.Background(), client.HostSessionID{
		Generation: string(spawned.Entry.Session.Generation),
		Session:    spawned.Entry.Session.Session,
	})
	if err != nil {
		t.Fatalf("AdoptLifecycle: %v", err)
	}
	if got != nil {
		t.Fatalf("launch = %#v, want none for a conventional session", *got)
	}
}

// An older generation is resident beside a newer one by design, and it
// answers unknown_op. The client must turn that into its own sentinel and not
// into a connection error: the session is fine and can be taken back; it is
// its lifecycle channel that cannot be re-established, which is a different
// sentence for the product to say.
func TestAdoptLifecycleOnAGenerationThatDoesNotKnowTheOpIsItsOwnAnswer(t *testing.T) {
	c := helperServing(t, olderGeneration{})
	_, err := c.AdoptLifecycle(context.Background(), client.HostSessionID{
		Generation: "testhash", Session: "0123456789abcdef0123456789abcdef",
	})
	if !errors.Is(err, client.ErrLifecycleAdoptUnsupported) {
		t.Fatalf("AdoptLifecycle against a generation without the op = %v, want ErrLifecycleAdoptUnsupported", err)
	}
	if errors.Is(err, client.ErrLost) {
		t.Fatal("an op an older generation does not know is a refusal, never a lost connection")
	}
}

// olderGeneration is a session service from before adopt-lifecycle existed:
// it serves the frozen ABI and answers unknown_op for anything newer, which
// is exactly what a resident older helper does.
type olderGeneration struct{}

func (olderGeneration) Name() string  { return proto.ServiceSession }
func (olderGeneration) Ops() []string { return []string{proto.OpSessions} }
func (olderGeneration) ParamsSchema(op string) *host.Schema {
	if op == proto.OpSessions {
		return host.SchemaFor(proto.SessionsParams{})
	}
	return nil
}

func (olderGeneration) Call(_ context.Context, op string, _ json.RawMessage) (any, error) {
	if op == proto.OpSessions {
		return proto.SessionsResult{Sessions: []proto.SessionEntry{}}, nil
	}
	return nil, errors.New("session: no op " + op)
}
