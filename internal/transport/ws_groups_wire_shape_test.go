package transport

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
)

// The group objects below are written the way the RENDERER writes them, not
// the way a handler reads them. That is the point of both tests: every
// existing groups.* test hand-builds params the validator is known to accept,
// so none of them could report that the product was sending something else
// (AGENTS.md testing rules 1 and 5).
//
// `editable` is backend-owned and comes back on every group derived from
// ~/.ssh/config or a Tabby import, so the editor round-trips it. `children`
// is added by buildGroupTree for the sidebar and is not part of any group
// contract.

func startGroupsServer(t *testing.T) (*WSServer, func()) {
	t.Helper()
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger),
		WithProfileRepository(ps), WithGroupRepository(ps))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, func() { _ = ws.Stop(ctx) }
}

func rpcError(t *testing.T, resp json.RawMessage) string {
	t.Helper()
	var envelope struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if envelope.Error == nil {
		return ""
	}
	return envelope.Error.Message
}

// TestGroupsWire_EditableRoundTripsOnEveryGroupMethod is the regression for
// the half of nocx-a0pf3 the schemas got wrong: editable is accepted by every
// group validator and was declared by none of the four params contracts,
// which all carry additionalProperties: false. A renderer that echoed back
// the group it was given was therefore sending something the published
// contract called invalid.
func TestGroupsWire_EditableRoundTripsOnEveryGroupMethod(t *testing.T) {
	ws, stop := startGroupsServer(t)
	defer stop()
	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck

	group := map[string]any{"id": "g1", "name": "Prod", "editable": true}

	if msg := rpcError(t, jsonrpcCall(t, conn, "groups.create", group)); msg != "" {
		t.Fatalf("groups.create refused an editable group: %s", msg)
	}
	if msg := rpcError(t, jsonrpcCall(t, conn, "groups.impact",
		map[string]any{"group": group})); msg != "" {
		t.Fatalf("groups.impact refused an editable group: %s", msg)
	}
	if msg := rpcError(t, jsonrpcCall(t, conn, "groups.update", group)); msg != "" {
		t.Fatalf("groups.update refused an editable group: %s", msg)
	}
	if msg := rpcError(t, jsonrpcCall(t, conn, "groups.apply",
		[]any{group})); msg != "" {
		t.Fatalf("groups.apply refused an editable group: %s", msg)
	}

	// And the contracts must say the same thing the socket just did.
	for method, payload := range map[string]any{
		"groups.create": group,
		"groups.update": group,
		"groups.impact": map[string]any{"group": group},
		"groups.apply":  []any{group},
	} {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("%s marshal: %v", method, err)
		}
		if err := validateJSONErr(loadSchema(t, method+".params.schema.json"), raw); err != nil {
			t.Errorf("%s contract refuses what the server accepts: %v", method, err)
		}
	}
}

// TestGroupsWire_DisplayOnlyFieldIsRefusedByEveryGroupMethod pins the other
// half. `children` is the sidebar's field; sending it is a renderer bug, and
// the answer must be the same on all four methods. It was not: groups.apply
// used a plain json.Unmarshal and dropped the key, so saving a group appeared
// to work while groups.impact answered -32602 for the same object — which is
// exactly why the impact preview was dead for every existing group and
// nobody noticed (nocx-a0pf3).
func TestGroupsWire_DisplayOnlyFieldIsRefusedByEveryGroupMethod(t *testing.T) {
	ws, stop := startGroupsServer(t)
	defer stop()
	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck

	node := map[string]any{"id": "g1", "name": "Prod", "children": []any{}}

	for method, payload := range map[string]any{
		"groups.create": node,
		"groups.update": node,
		"groups.impact": map[string]any{"group": node},
		"groups.apply":  []any{node},
	} {
		msg := rpcError(t, jsonrpcCall(t, conn, method, payload))
		if !strings.Contains(msg, "children") {
			t.Errorf("%s accepted a display-only field: error = %q, want it named", method, msg)
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("%s marshal: %v", method, err)
		}
		if err := validateJSONErr(loadSchema(t, method+".params.schema.json"), raw); err == nil {
			t.Errorf("%s contract accepts a display-only field the server refuses", method)
		}
	}
}
