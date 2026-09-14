//go:build nocx_local_ssh

package sshsvc

// The `ssh.tool-socket` op: what this helper refuses, and what ending it does
// (nocx-e2bws).
//
// The end-to-end half — a real far-side socket bound by a real sshd, an agent
// connection piped into an endpoint with the pane record first — rides the
// session package's ssh fixture (internal/helper/session's tool-socket stand),
// where the far side's streamlocal support already exists for the spawn-ssh
// pane. What is here is the half that needs no sshd at all and would otherwise
// be checked only through one: the request's own refusals, the registration
// this service keeps for the id, and the close that ends it.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

func testService() *Service {
	return New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestTheToolSocketOpRefusesAnIncompleteRequest pins every refusal this op
// raises itself, one case per field. Each is a request this helper cannot
// honour, and each is answered BEFORE anything is dialed — a request it cannot
// honour must cost the far host no connection (the rule validatePaneSpec's own
// refusals follow).
func TestTheToolSocketOpRefusesAnIncompleteRequest(t *testing.T) {
	destination := proto.SSHDestination{
		Host: "host.example", Port: 22, User: "dev",
		// The destination carries the rung this dial authenticates with, and
		// the interactive one carries nothing else: a request with no identity
		// is refused by validateIdentity before any path is considered, which
		// is why a "complete" request has to say how it authenticates.
		Identity: proto.SSHIdentity{Auth: proto.SSHAuthInteractive},
	}
	complete := proto.ToolSocketParams{
		Destination: destination, Path: "/home/dev/.nocx/run/pane/tool.sock",
		Target: "/run/user/1000/nocx/tool.sock", Session: "0f9b4d7159d38afee9648a843654516f",
	}
	if err := validateToolSocket(complete); err != nil {
		t.Fatalf("a complete request was refused: %v", err)
	}

	for _, tc := range []struct {
		name string
		mut  func(*proto.ToolSocketParams)
		want error
	}{
		{"no far path", func(p *proto.ToolSocketParams) { p.Path = "" }, errBadToolSocketParams},
		{"no endpoint behind the path", func(p *proto.ToolSocketParams) { p.Target = "" }, errNoToolSocketTarget},
		{"no pane to name", func(p *proto.ToolSocketParams) { p.Session = "" }, errNoToolSocketSession},
		{"no host", func(p *proto.ToolSocketParams) { p.Destination.Host = "" }, errBadToolSocketParams},
		{"no port", func(p *proto.ToolSocketParams) { p.Destination.Port = 0 }, errBadToolSocketParams},
		{"no user", func(p *proto.ToolSocketParams) { p.Destination.User = "" }, errBadToolSocketParams},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := complete
			tc.mut(&p)
			err := validateToolSocket(p)
			if !errors.Is(err, tc.want) {
				t.Fatalf("validateToolSocket = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestUnforwardEndsAToolSocketAndForgetsIt is the closing edge: the id the op
// answered is the id `unforward` ends, and ending it closes the listener the
// far side bound — the half a listener alone cannot do for the connections it
// already accepted (that half is pane_close_test.go's).
func TestUnforwardEndsAToolSocketAndForgetsIt(t *testing.T) {
	svc := testService()
	path := filepath.Join(t.TempDir(), "far.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	id, err := mintForwardID()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	svc.toolSockets = map[proto.ForwardID]*toolSocket{
		id: newToolSocket(svc.log, ln, nil, path, filepath.Join(t.TempDir(), "local.sock"), "0f9b4d7159d38afee9648a843654516f"),
	}

	if _, err := svc.unforward(id); err != nil {
		t.Fatalf("unforward: %v", err)
	}
	if _, held := svc.toolSockets[id]; held {
		t.Fatal("the id is still registered after unforward, so nothing can ever end it again")
	}
	// The listener is CLOSED, which is what the far side hears: a bind that
	// still accepts is a socket the far agent can dial into a pane that is
	// gone.
	if conn, derr := net.Dial("unix", path); derr == nil {
		_ = conn.Close()
		t.Fatal("the far-side listener still accepts after unforward")
	}
	// Idempotent, like every other unforward: the ordinary caller is a session
	// teardown that may run twice.
	if _, err := svc.unforward(id); err != nil {
		t.Fatalf("a second unforward: %v", err)
	}
}

// TestTheToolSocketOpIsDeclaredAndItsParamsAreValidated is the wiring half: the
// op is on the service's own list, it declares a params schema (host.Register
// refuses an op without one), and a payload that is not its shape is refused as
// bad params rather than answered.
func TestTheToolSocketOpIsDeclaredAndItsParamsAreValidated(t *testing.T) {
	svc := testService()
	declared := false
	for _, op := range svc.Ops() {
		if op == proto.OpToolSocket {
			declared = true
		}
	}
	if !declared {
		t.Fatalf("the service does not declare %q, so a coordinator's request reaches no dispatcher", proto.OpToolSocket)
	}
	if svc.ParamsSchema(proto.OpToolSocket) == nil {
		t.Fatalf("no params schema for %q: the host refuses a service that declares an op without one", proto.OpToolSocket)
	}

	// Malformed params are the decoder's refusal, mapped to the caller's own
	// code so an old coordinator reads "bad request" rather than an internal
	// failure of this process.
	_, err := svc.Call(context.Background(), proto.OpToolSocket, json.RawMessage(`{"path":`))
	if !errors.Is(err, errBadToolSocketParams) {
		t.Fatalf("a malformed request answered %v, want a bad-params refusal", err)
	}
}
