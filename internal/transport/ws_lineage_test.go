package transport

// The parent edge on the wire (nocx-9hu9d). A tab that opens another tab says
// so in the open params, by the FULL identity of the tab it opened from, and
// the ack hands the recorded edge back so the renderer stores what the backend
// admitted rather than what it asked for.
//
// The edge is provenance and nothing else. No handler here reads a parent to
// decide whether a request is allowed, and the tests below assert the wire
// shape, never a permission.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

// openWire is the ack as the renderer decodes it, including the parent edge.
type openWire struct {
	SessionID    string           `json:"sessionId"`
	InstanceID   string           `json:"instanceId"`
	SessionEpoch uint64           `json:"sessionEpoch"`
	Cwd          string           `json:"cwd"`
	DesiredMode  string           `json:"desiredMode"`
	Parent       *openParentWire  `json:"parent"`
	Error        *jsonrpcErrorObj `json:"-"`
}

type openParentWire struct {
	SessionID    string `json:"sessionId"`
	InstanceID   string `json:"instanceId"`
	SessionEpoch uint64 `json:"sessionEpoch"`
}

// callOpen drives a real open over the real socket and returns the decoded
// envelope halves: the raw result (for schema validation) and any error.
func callOpen(t *testing.T, conn *websocket.Conn, params map[string]any, id int) (json.RawMessage, *jsonrpcErrorObj) {
	t.Helper()
	resp := jsonrpcCallWithID(t, conn, "open", params, id)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("open: unmarshal: %v", err)
	}
	return envelope.Result, envelope.Error
}

func decodeOpen(t *testing.T, raw json.RawMessage) openWire {
	t.Helper()
	var got openWire
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("open result: unmarshal: %v", err)
	}
	return got
}

// A tab opened from another tab carries the opener's full identity, all the
// way from the params to the session record to the ack — and the ack's edge is
// the one the backend RECORDED, which is what makes it worth reading back.
func TestOpen_ParentEdgeCrossesTheWire(t *testing.T) {
	schema := loadSchema(t, "open.schema.json")
	fake := newExitFakePTY()
	ws := newExitServer(t, fake)
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	parentRaw, rpcErr := callOpen(t, conn, map[string]any{"cols": 80, "rows": 24}, 1)
	if rpcErr != nil {
		t.Fatalf("open parent: %+v", rpcErr)
	}
	parent := decodeOpen(t, parentRaw)
	if parent.Parent != nil {
		t.Errorf("a root session's ack carries parent = %+v, want null", parent.Parent)
	}

	childRaw, rpcErr := callOpen(t, conn, map[string]any{
		"cols": 80, "rows": 24,
		"parent": map[string]any{
			"sessionId":    parent.SessionID,
			"instanceId":   parent.InstanceID,
			"sessionEpoch": parent.SessionEpoch,
		},
	}, 2)
	if rpcErr != nil {
		t.Fatalf("open child: %+v", rpcErr)
	}
	child := decodeOpen(t, childRaw)
	if child.Parent == nil {
		t.Fatal("the child's ack carries no parent edge")
	}
	if child.Parent.SessionID != parent.SessionID ||
		child.Parent.InstanceID != parent.InstanceID ||
		child.Parent.SessionEpoch != parent.SessionEpoch {
		t.Errorf("ack parent = %+v, want the opener's identity %s/%s/%d",
			child.Parent, parent.SessionID, parent.InstanceID, parent.SessionEpoch)
	}

	// The record, not just the ack: the registry's session is what every
	// later reader sees, and an ack that agreed with the params but not with
	// the record would be the defect this test exists to catch.
	sess, err := ws.registry.Get(session.ID(child.SessionID))
	if err != nil {
		t.Fatalf("registry.Get: %v", err)
	}
	edge, has := sess.Parent()
	if !has {
		t.Fatal("the child session record carries no parent edge")
	}
	if string(edge.ID) != parent.SessionID || edge.Identity.Epoch != parent.SessionEpoch {
		t.Errorf("record edge = %+v, want %s at epoch %d", edge, parent.SessionID, parent.SessionEpoch)
	}

	// And the real bytes off the real socket satisfy the contract — both
	// shapes, because a nullable field is exactly where a schema and a
	// handler drift apart.
	validateJSON(t, schema, parentRaw, "open result, root (real socket)")
	validateJSON(t, schema, childRaw, "open result, child (real socket)")
}

// A parent naming another backend instance is a forged edge: it claims a
// provenance this backend cannot have witnessed. It is refused as bad params,
// and no session is created for it.
func TestOpen_RefusesForgedParentOverTheWire(t *testing.T) {
	fake := newExitFakePTY()
	ws := newExitServer(t, fake)
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	parentRaw, rpcErr := callOpen(t, conn, map[string]any{"cols": 80, "rows": 24}, 1)
	if rpcErr != nil {
		t.Fatalf("open parent: %+v", rpcErr)
	}
	parent := decodeOpen(t, parentRaw)
	before := len(ws.registry.List())

	_, rpcErr = callOpen(t, conn, map[string]any{
		"cols": 80, "rows": 24,
		"parent": map[string]any{
			"sessionId":    parent.SessionID,
			"instanceId":   "ffffffffffffffffffffffffffffffff",
			"sessionEpoch": parent.SessionEpoch,
		},
	}, 2)
	if rpcErr == nil {
		t.Fatal("open with a foreign backend instance succeeded")
	}
	if rpcErr.Code != -32602 {
		t.Errorf("code = %d, want -32602: a forged edge is a bad claim in the params, not a server fault", rpcErr.Code)
	}
	if got := len(ws.registry.List()); got != before {
		t.Errorf("registry holds %d sessions after a refused open, want %d", got, before)
	}
}

// A parent naming a session that does not exist is refused the same way. The
// renderer cannot invent an opener.
func TestOpen_RefusesUnknownParentOverTheWire(t *testing.T) {
	fake := newExitFakePTY()
	ws := newExitServer(t, fake)
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	parentRaw, rpcErr := callOpen(t, conn, map[string]any{"cols": 80, "rows": 24}, 1)
	if rpcErr != nil {
		t.Fatalf("open parent: %+v", rpcErr)
	}
	parent := decodeOpen(t, parentRaw)

	_, rpcErr = callOpen(t, conn, map[string]any{
		"cols": 80, "rows": 24,
		"parent": map[string]any{
			"sessionId":    string(session.NewID()),
			"instanceId":   parent.InstanceID,
			"sessionEpoch": parent.SessionEpoch,
		},
	}, 2)
	if rpcErr == nil {
		t.Fatal("open naming an absent parent succeeded")
	}
	if rpcErr.Code != -32602 {
		t.Errorf("code = %d, want -32602", rpcErr.Code)
	}
}

// The ingress bound: a malformed parent is refused before the handler runs, on
// the same shape rule every other session id on this wire is held to. Driven
// through the registered validator, which is where a bad id must die.
func TestValidateOpenRaw_BoundsTheParentEdge(t *testing.T) {
	cases := map[string]string{
		"parent with a short session id":  `{"cols":80,"rows":24,"parent":{"sessionId":"abc","instanceId":"0123456789abcdef0123456789abcdef","sessionEpoch":1}}`,
		"parent with a short instance id": `{"cols":80,"rows":24,"parent":{"sessionId":"0123456789abcdef0123456789abcdef","instanceId":"abc","sessionEpoch":1}}`,
		"parent with epoch zero":          `{"cols":80,"rows":24,"parent":{"sessionId":"0123456789abcdef0123456789abcdef","instanceId":"0123456789abcdef0123456789abcdef","sessionEpoch":0}}`,
		"parent missing its instance":     `{"cols":80,"rows":24,"parent":{"sessionId":"0123456789abcdef0123456789abcdef","sessionEpoch":1}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if msg := validateOpenRaw(json.RawMessage(raw)); msg == "" {
				t.Errorf("validator accepted %s", raw)
			}
		})
	}

	// And the well-formed edge passes the bound — a validator that refused
	// everything would pass the cases above for the wrong reason.
	ok := `{"cols":80,"rows":24,"parent":{"sessionId":"0123456789abcdef0123456789abcdef","instanceId":"fedcba9876543210fedcba9876543210","sessionEpoch":3}}`
	if msg := validateOpenRaw(json.RawMessage(ok)); msg != "" {
		t.Errorf("validator refused a well-formed parent edge: %s", msg)
	}
}

// The DTO's own conformance for the edge: present as an object, and present as
// null. `required` plus a nullable type is what makes "always sent" real —
// omitempty on this field would silently drop the key for every root session.
func TestOpenParent_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "open.schema.json")

	withParent, err := json.Marshal(openResult{
		SessionID:    "0123456789abcdef0123456789abcdef",
		InstanceID:   "fedcba9876543210fedcba9876543210",
		SessionEpoch: 2,
		// workspaceId became required with a minLength of 1 when nocx-fraus
		// merged; a session is always in a workspace and there is no null.
		WorkspaceID: "workspace:default",
		Cwd:         "~/work",
		DesiredMode: "script",
		// effectiveSize became required with nocx-eidfb.1: every session has
		// a size, so every ack carries one.
		EffectiveSize: sizeResultOf(session.DefaultSize()),
		Parent: &openParentResult{
			SessionID:    "00000000000000000000000000000001",
			InstanceID:   "fedcba9876543210fedcba9876543210",
			SessionEpoch: 1,
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, withParent, "open DTO (with a parent edge)")

	// A partial edge must not satisfy the contract: half an identity is the
	// bare parentId this change exists to refuse.
	partial, err := json.Marshal(map[string]any{
		"sessionId":    "0123456789abcdef0123456789abcdef",
		"instanceId":   "fedcba9876543210fedcba9876543210",
		"sessionEpoch": 2,
		"cwd":          "~/work",
		"desiredMode":  "script",
		"parent":       map[string]any{"sessionId": "00000000000000000000000000000001"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := validateJSONErr(schema, partial); err == nil {
		t.Error("open schema accepts a parent carrying only a sessionId: the full identity is the point of the edge")
	}
}

// The lineage refusals are told from a dial failure at the wire. A bad claim
// is the caller's (-32602); a shell that would not start is the server's
// (-32603). Both used to be the second, which is how a renderer would have
// retried a request that can never succeed.
func TestLineageRefusal_IsAParamsError(t *testing.T) {
	for name, err := range map[string]error{
		"unknown":          session.ErrParentUnknown,
		"foreign instance": session.ErrParentForeignInstance,
		"self":             session.ErrParentSelf,
		"cycle":            session.ErrParentCycle,
		"too deep":         session.ErrTooDeep,
	} {
		t.Run(name, func(t *testing.T) {
			if !isLineageRefusal(err) {
				t.Errorf("%v is not recognised as a lineage refusal, so it would be answered as an internal error", err)
			}
		})
	}
	if isLineageRefusal(context.DeadlineExceeded) {
		t.Error("a dial timeout must not be answered as a bad parent claim")
	}
	if isLineageRefusal(nil) {
		t.Error("nil is not a refusal")
	}
}

// A session opened with no parent has no edge, and the registry says so — the
// null on the wire is a real absence, not a marshalling accident.
func TestOpen_RootSessionHasNoEdge(t *testing.T) {
	reg := session.New(log.NewSlogAdapter(nil), &exitFakePTYFactory{fake: newExitFakePTY()})
	sess, err := reg.Open(context.Background(), session.Config{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, has := sess.Parent(); has {
		t.Error("a session opened with no parent claims one")
	}
}
