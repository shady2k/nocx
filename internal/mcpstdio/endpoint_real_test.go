package mcpstdio

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/toolendpoint"
)

// THE REAL ENDPOINT, because a fake would prove nothing here. The refusal the
// reported hang turned into comes from the endpoint's own rule — Auth.Admit
// runs ONCE PER CONNECTION and the session's caller slot is held for the
// connection's whole lifetime — so a hand-written fake that answered whatever
// it was asked would pass while the product kept refusing the coordinator's
// second call.

// testUID and the two kernel seams that carry it. The endpoint will not speak
// to a connection whose peer uid is not the uid it believes it is running as,
// and it refuses a runtime directory owned by anybody else; both answers come
// from these seams here, and the check itself is toolendpoint's own subject.
// What this test needs is a connection the endpoint ADMITS, because admission
// is what holds the session's caller slot and therefore what a second
// connection cannot get.
const testUID = uint32(1000)

type testUIDPeers struct{}

func (testUIDPeers) PeerUID(*net.UnixConn) (uint32, error) { return testUID, nil }

type testUIDOwner struct{}

func (testUIDOwner) OwnerUID(string) (uint32, error) { return testUID, nil }

// slotAuthorizer is the composition root's caller-slot rule at the scope the
// endpoint applies it: one live connection may hold a session's slot, and a
// second connection asking for the same session while the first is open is
// refused. It is reproduced rather than simplified away, because with
// per-call dialling this is what turned the second call into a refusal that
// reads as an identity failure.
type slotAuthorizer struct {
	mu   sync.Mutex
	held bool
}

func (a *slotAuthorizer) Admit(toolendpoint.Peer) (assistant.ToolInvocation, func(), error) {
	a.mu.Lock()
	if a.held {
		a.mu.Unlock()
		return assistant.ToolInvocation{}, nil, toolendpoint.ErrSessionCallerActive
	}
	a.held = true
	a.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			a.mu.Lock()
			a.held = false
			a.mu.Unlock()
		})
	}
	return assistant.ToolInvocation{
		Context:    context.Background(),
		RunContext: agenttools.RunContext{Session: "session"},
	}, release, nil
}

// heldDispatcher is the tool surface on the far side: one call that does not
// answer until the test lets it, and one that answers at once.
type heldDispatcher struct {
	held     chan struct{}
	release  chan struct{}
	holdOnce sync.Once
}

func (d *heldDispatcher) Dispatch(inv assistant.ToolInvocation) (string, error) {
	if inv.Method != "alpha.hold" {
		return `{"screen":true}`, nil
	}
	d.holdOnce.Do(func() { close(d.held) })
	select {
	case <-d.release:
		return `{"held":true}`, nil
	case <-inv.Context.Done():
		return "", inv.Context.Err()
	}
}

func (d *heldDispatcher) Catalogue(content.Grant) []agenttools.Tool {
	return []agenttools.Tool{
		{
			Declaration:  agenttools.Declaration{Name: "alpha.hold", Description: "holds the call open"},
			ParamsSchema: json.RawMessage(`{"type":"object"}`),
			ResultSchema: json.RawMessage(`{"type":"object"}`),
		},
		{
			Declaration:  agenttools.Declaration{Name: "alpha.screen", Description: "answers while another call is held"},
			ParamsSchema: json.RawMessage(`{"type":"object"}`),
			ResultSchema: json.RawMessage(`{"type":"object"}`),
		},
	}
}

// startRealEndpoint starts the shipped endpoint over a real socket with the
// test's own authorizer and tool surface. Nothing here is a double except those
// two seams: the socket, the kernel-stamped peer, the admission and the
// dispatch pipeline are the ones the product runs.
func startRealEndpoint(t *testing.T, auth toolendpoint.Authorizer, dispatch assistant.ToolDispatcher) *toolendpoint.Endpoint {
	t.Helper()
	endpoint, err := toolendpoint.New(toolendpoint.Config{
		Dir:      t.TempDir(),
		Peers:    testUIDPeers{},
		Owner:    testUIDOwner{},
		SelfUID:  testUID,
		Auth:     auth,
		Dispatch: dispatch,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("toolendpoint.New: %v", err)
	}
	if err := endpoint.Start(); err != nil {
		t.Fatalf("start endpoint: %v", err)
	}
	t.Cleanup(func() { _ = endpoint.Close() })
	return endpoint
}

// toolRefusal returns the text of a refused call.
func toolRefusal(t *testing.T, envelope map[string]json.RawMessage) string {
	t.Helper()
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(envelope["result"], &result); err != nil {
		t.Fatalf("decode refusal of %v: %v", envelope, err)
	}
	if !result.IsError || len(result.Content) != 1 {
		t.Fatalf("expected a refusal, got %v", envelope)
	}
	return result.Content[0].Text
}

// refusingAuthorizer refuses every peer, the way the shipped one does when the
// answer behind an admission has gone or another caller holds the session's
// slot.
type refusingAuthorizer struct{ refusal error }

func (a refusingAuthorizer) Admit(toolendpoint.Peer) (assistant.ToolInvocation, func(), error) {
	return assistant.ToolInvocation{}, nil, a.refusal
}

// The reported failure, on the real path: with one call outstanding at the
// endpoint, the next call is answered — over the SAME connection, because that
// connection is the session's admission interval and a second one could not be
// admitted while the first is open.
func TestASecondCallCrossesTheRealEndpointWhileTheFirstIsHeld(t *testing.T) {
	dispatcher := &heldDispatcher{held: make(chan struct{}), release: make(chan struct{})}
	endpoint := startRealEndpoint(t, &slotAuthorizer{}, dispatcher)

	driver := startStdio(t, endpoint.SocketPath())
	initialization(driver)

	driver.send(2, "tools/call", `{"name":"alpha.hold"}`)
	<-dispatcher.held
	driver.send(3, "tools/call", `{"name":"alpha.screen"}`)
	if text := toolText(t, driver.nextID(3)); text != `{"screen":true}` {
		t.Fatalf("the call sent beside a held one answered %s", text)
	}

	close(dispatcher.release)
	if text := toolText(t, driver.nextID(2)); text != `{"held":true}` {
		t.Fatalf("the held call answered %s", text)
	}
}

// A REFUSAL THAT ARRIVES FOR THE CONNECTION IS STILL AN ANSWER TO THE CALLER.
// An admission failure is written with a null id — the endpoint has read no
// request to attach it to — so there is no waiter to hand it to. Dropped, all
// of them arrive as "the endpoint is unavailable": the one answer that names no
// action, for exactly the refusals this surface exists to deliver. The parent
// decoded the error without needing a matching id; the multiplexed reader has
// to keep that property deliberately.
func TestAConnectionRefusalReachesTheCallerAsItsReason(t *testing.T) {
	refusals := map[string]struct {
		err      error
		sentence string
	}{
		"not enrolled":            {toolendpoint.ErrNotEnrolled, "not in a pane nocx has enrolled"},
		"another caller holds it": {toolendpoint.ErrSessionCallerActive, "serves one at a time"},
	}
	for name, refusal := range refusals {
		t.Run(name, func(t *testing.T) {
			endpoint := startRealEndpoint(t, refusingAuthorizer{refusal: refusal.err}, &heldDispatcher{
				held: make(chan struct{}), release: make(chan struct{}),
			})
			driver := startStdio(t, endpoint.SocketPath())

			driver.send(2, "tools/call", `{"name":"alpha.screen"}`)
			reason := toolRefusal(t, driver.nextID(2))
			if strings.Contains(reason, "unavailable") {
				t.Fatalf("the refusal was reported as an unavailable endpoint: %q", reason)
			}
			if !strings.Contains(reason, refusal.sentence) {
				t.Fatalf("the refusal reached the caller as %q, want the endpoint's own sentence", reason)
			}
		})
	}
}
