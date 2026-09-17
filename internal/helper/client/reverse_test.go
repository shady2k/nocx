package client_test

// The reverse half of the connection, exercised through a real host and a real
// client: a SERVICE asks the coordinator a question and waits for the answer,
// which is the shape the ssh service's credentials take (reverse.go).

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// askingService is the smallest thing that can ask: one op that sends one
// reverse request and answers with what came back. It reaches its connection
// through the request's context, exactly as a real service does — a host serves
// several coordinators, so "the coordinator" is a property of the request and
// never of the service.
type askingService struct{}

func (askingService) Name() string  { return "reverse-test" }
func (askingService) Ops() []string { return []string{"ask"} }
func (askingService) ParamsSchema(string) *host.Schema {
	return host.SchemaFor(struct{}{})
}

func (askingService) Call(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
	conn, _ := host.ConnectionFrom(ctx).(*host.Host)
	if conn == nil {
		return nil, errors.New("no connection on this request")
	}
	var out struct {
		Answer string `json:"answer"`
	}
	if err := conn.Ask(ctx, "coordinator", "echo", map[string]string{"question": "hi"}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Refusal puts this service's codes on the wire, so the test can assert the
// CODE rather than a message: a refusal the coordinator named must arrive as
// itself, which is what lets a helper tell "the vault is sealed" from "the op
// failed".
func (askingService) Refusal(err error) (string, json.RawMessage) {
	var refusal *proto.Refusal
	if errors.As(err, &refusal) {
		return refusal.Code, refusal.Details
	}
	return "", nil
}

func dialReverseTest(t *testing.T, reverse *client.ReverseRegistry) *client.Client {
	t.Helper()
	conn := newFakeConn(hostPeer("testhash", askingService{}))
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash",
		SentinelTTL: time.Second, Reverse: reverse,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestAServiceAsksTheCoordinatorAndGetsItsAnswer(t *testing.T) {
	reverse := client.NewReverseRegistry()
	reverse.Register("coordinator", "echo", func(_ context.Context, params json.RawMessage) (any, error) {
		var in struct {
			Question string `json:"question"`
		}
		if err := json.Unmarshal(params, &in); err != nil {
			return nil, err
		}
		return map[string]string{"answer": "you asked: " + in.Question}, nil
	})
	c := dialReverseTest(t, reverse)

	var out struct {
		Answer string `json:"answer"`
	}
	if err := c.Call(context.Background(), "reverse-test", "ask", struct{}{}, &out); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.Answer != "you asked: hi" {
		t.Fatalf("answer = %q, want the coordinator's own value", out.Answer)
	}
}

func TestAReverseRequestWithNoHandlerIsRefusedByName(t *testing.T) {
	// A registry with something in it, so the refusal cannot be the trivial
	// "no registry at all" case: the service the helper names is simply not one
	// this coordinator answers.
	reverse := client.NewReverseRegistry()
	reverse.Register("something-else", "op", func(context.Context, json.RawMessage) (any, error) {
		return nil, nil
	})
	c := dialReverseTest(t, reverse)

	err := c.Call(context.Background(), "reverse-test", "ask", struct{}{}, nil)
	var refusal *client.RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("Call error = %v, want a refusal", err)
	}
	if refusal.Code != proto.ErrCodeUnknownService {
		t.Fatalf("code = %q, want %q", refusal.Code, proto.ErrCodeUnknownService)
	}
	if refusal.Message == "" {
		t.Fatal("the refusal names nothing: a helper cannot tell what it asked that nobody answers")
	}
}

func TestAReverseRefusalKeepsItsCode(t *testing.T) {
	reverse := client.NewReverseRegistry()
	reverse.Register("coordinator", "echo", func(context.Context, json.RawMessage) (any, error) {
		return nil, &proto.Refusal{Code: proto.ErrCodeVaultSealed, Message: "the vault is sealed"}
	})
	c := dialReverseTest(t, reverse)

	err := c.Call(context.Background(), "reverse-test", "ask", struct{}{}, nil)
	var refusal *client.RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("Call error = %v, want a refusal", err)
	}
	if refusal.Code != proto.ErrCodeVaultSealed {
		t.Fatalf("code = %q, want %q — a named refusal crossed as something else", refusal.Code, proto.ErrCodeVaultSealed)
	}
}

// TestAReverseRequestIsAnUnexpectedFrameNoLonger is the regression the client's
// frame switch would otherwise reintroduce silently: an unhandled TypeRequest
// was logged and DROPPED, and a drop is the one answer that always produces a
// hang, because the helper is waiting for exactly that frame.
func TestAReverseRequestIsAnUnexpectedFrameNoLonger(t *testing.T) {
	reverse := client.NewReverseRegistry()
	reverse.Register("coordinator", "echo", func(context.Context, json.RawMessage) (any, error) {
		return map[string]string{"answer": "answered"}, nil
	})
	c := dialReverseTest(t, reverse)

	done := make(chan error, 1)
	go func() {
		var out struct {
			Answer string `json:"answer"`
		}
		done <- c.Call(context.Background(), "reverse-test", "ask", struct{}{}, &out)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reverse request was never answered: the frame was dropped")
	}
}
