package transport

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

// The three refusals `open` answers before anything is dialed had no test at
// all, which is how a refactor of this handler silently turns a sentence a
// user can act on — "SSH sessions not available" — into "Internal error".
//
// They are asserted over the WIRE and not against the handler, for the reason
// contract tests exist: what a person sees is the frame, and a test that
// validated a payload it built itself would prove the string is well-formed
// rather than that the server sends it.
//
// This is written BEFORE the open path is extracted (nocx-dkawo.6), so the
// extraction has something to be measured against. Each case is a state a
// person genuinely reaches: an ssh profile in a build with no resolver wired,
// an ssh alias with no ~/.ssh/config resolver, and an ssh open naming neither
// a profile nor a host.
func TestOpenRefusesBeforeDialingWithAReasonAUserCanAct(t *testing.T) {
	// A real UUIDv7: the transport validates the pane's shape before
	// anything is spawned, so an invalid one would refuse for the wrong
	// reason and the test would pass while proving nothing.
	const pane = "0198f2b0-0000-7000-8000-0000000000c1"

	for _, tc := range []struct {
		name    string
		params  map[string]any
		code    int
		message string
	}{
		{
			name:    "an ssh profile in a build with no profile resolver",
			params:  map[string]any{"kind": "ssh", "profileId": "p", "cols": 80, "rows": 24, "paneId": pane},
			code:    -32603,
			message: "SSH sessions not available (no profile resolver wired)",
		},
		{
			name:    "an ssh alias with no ssh-config resolver",
			params:  map[string]any{"kind": "ssh", "host": "myhost", "cols": 80, "rows": 24, "paneId": pane},
			code:    -32603,
			message: "SSH config resolver not available",
		},
		{
			// Answered by validateOpenRaw, which runs before the handler.
			// Writing this test found that the handler carries a SECOND
			// answer to the same question — an else-branch replying
			// "Invalid params: profileId or host required for ssh session"
			// — that nothing can reach, because the validator refuses the
			// request first. Two answers to one question is what AD-8
			// forbids, and the unreachable one is deleted rather than left
			// to be maintained; this case is what proves which one speaks.
			name:    "an ssh open naming neither a profile nor a host",
			params:  map[string]any{"kind": "ssh", "cols": 80, "rows": 24, "paneId": pane},
			code:    -32602,
			message: "Invalid params: profileId or host is required for an ssh session",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
			if err := ws.Start(ctx); err != nil {
				t.Fatalf("start: %v", err)
			}
			defer func() { _ = ws.Stop(ctx) }()
			conn := connectWS(t, ws)
			defer func() { _ = conn.Close() }()

			raw := jsonrpcCall(t, conn, "open", tc.params)
			var resp struct {
				Error *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
				Result json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal(raw, &resp); err != nil {
				t.Fatalf("unmarshal response: %v (%s)", err, raw)
			}
			if resp.Error == nil {
				t.Fatalf("open succeeded where it must refuse: %s", raw)
			}
			if resp.Error.Code != tc.code {
				t.Fatalf("code = %d, want %d (%s)", resp.Error.Code, tc.code, raw)
			}
			if resp.Error.Message != tc.message {
				t.Fatalf("message = %q, want %q", resp.Error.Message, tc.message)
			}
		})
	}
}

// A refusal before the dial must leave nothing behind: no session in the
// registry, and therefore no shell, no channel and nothing for a later
// reconciliation to judge. This is the same property the handler's own comment
// claims for the workspace resolution — "a request that cannot be satisfied
// must not cost the user a shell or an ssh handshake" — asserted rather than
// stated.
func TestARefusedOpenLeavesNoSession(t *testing.T) {
	ctx := context.Background()
	reg := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), reg)
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	before := len(reg.List())
	jsonrpcCall(t, conn, "open", map[string]any{
		"kind": "ssh", "cols": 80, "rows": 24,
		"paneId": "0198f2b0-0000-7000-8000-0000000000c2",
	})
	if after := len(reg.List()); after != before {
		t.Fatalf("a refused open left %d session(s) behind", after-before)
	}
}

// The second caller (nocx-dkawo.6): the backend opens a session with no
// renderer connected at all.
//
// This is the property the wave record needs and the thing the old handler
// could not do, because every step of it was written against a request id and
// a connection. It is asserted with no WebSocket open — not merely with the
// connection unused — since a test that dialed one first would pass even if
// the path still depended on it.
func TestTheBackendOpensASessionWithNoClientAttached(t *testing.T) {
	ctx := context.Background()
	reg := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), reg)
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	opened, err := ws.OpenSession(ctx, OpenSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if opened.Session == nil {
		t.Fatalf("no session")
	}
	if opened.Session.ID() == "" {
		t.Fatalf("the session has no id")
	}
	// The workspace is the backend's own conclusion, not something the
	// caller was asked to supply.
	if opened.WorkspaceID == "" {
		t.Fatalf("no workspace resolved")
	}
	// And it is a real session in the registry, which is what makes it
	// something a later attach can pick up.
	if _, err := reg.Get(opened.Session.ID()); err != nil {
		t.Fatalf("the opened session is not in the registry: %v", err)
	}
}

// The backend caller meets the SAME refusals, because it runs the same path.
// If it did not, the two callers would have diverged at the first branch and
// the extraction would have bought nothing.
func TestTheBackendCallerMeetsTheSameRefusals(t *testing.T) {
	ctx := context.Background()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	for _, tc := range []struct {
		name string
		spec OpenSpec
		want string
	}{
		{
			name: "an ssh profile in a build with no profile resolver",
			spec: OpenSpec{Kind: "ssh", ProfileID: "p", Cols: 80, Rows: 24},
			want: "SSH sessions not available (no profile resolver wired)",
		},
		{
			name: "an ssh alias with no ssh-config resolver",
			spec: OpenSpec{Kind: "ssh", Host: "myhost", Cols: 80, Rows: 24},
			want: "SSH config resolver not available",
		},
		{
			// Unreachable from the wire, because validateOpenRaw refuses it
			// first. A backend caller does not pass through that validator,
			// which is exactly why the branch exists rather than being
			// deleted as dead.
			name: "an ssh open naming neither a profile nor a host",
			spec: OpenSpec{Kind: "ssh", Cols: 80, Rows: 24},
			want: "Invalid params: profileId or host is required for an ssh session",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ws.OpenSession(ctx, tc.spec)
			if err == nil {
				t.Fatalf("the backend caller was not refused")
			}
			var refusal *openRefusal
			if !errors.As(err, &refusal) {
				t.Fatalf("err = %v, want an openRefusal", err)
			}
			if refusal.message != tc.want {
				t.Fatalf("message = %q, want %q", refusal.message, tc.want)
			}
		})
	}
}

// The opener is ready as soon as the server is constructed, which is what a
// composition root needs: it wires the wave record before anything serves.
func TestTheBackendCallerIsReadyBeforeTheServerServes(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	opened, err := ws.OpenSession(context.Background(), OpenSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open before Start: %v", err)
	}
	if opened.Session == nil {
		t.Fatalf("no session")
	}
}
