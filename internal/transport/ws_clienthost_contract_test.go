package transport

// Wire contracts for the client host (nocx-uo1k6, design D3).
//
// host.request is SERVER-built, so its check is the real payload off the real
// socket. A test that validates a payload it built itself proves the struct is
// well-formed, not that the server sends it — and the one defect this rule was
// bought by (vault.status's missing defaultProvider) was exactly a field the
// server never sent while both suites stayed green.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestHostRequest_OverTheWireConformsToContract — every capability, off the
// socket, against the schema the renderer's generated type was declared from.
// Per capability rather than once for the group, because the arguments differ
// per capability and it is the arguments a schema can be wrong about.
func TestHostRequest_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "host.request.schema.json")
	for _, tc := range hostCapabilityCases {
		t.Run(tc.name, func(t *testing.T) {
			ws := newHostServer(t)
			conn := attachClient(t, ws)
			defer conn.Close() //nolint:errcheck

			out := askAsync(ws, t.Context(), tc.ask)
			raw := readNotification(t, conn, "host.request", 5*time.Second)
			validateJSON(t, schema, raw, "host.request notification params")

			// Settle the ask so the goroutine above exits. A failed outcome
			// is the honest terminal answer for a test that only wants the
			// params.
			var req struct {
				RequestID string `json:"requestId"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatalf("requestId decode: %v", err)
			}
			resolveHost(t, conn, map[string]any{
				"requestId": req.RequestID, "outcome": "failed", "error": "contract test",
			})
			o := settled(t, out)
			if o.err == nil || !strings.Contains(o.err.Error(), "contract test") {
				t.Fatalf("RequestHost returned %v, want the failed-outcome error", o.err)
			}
		})
	}
}

// TestHostResolved_DTOConformsToContract — the client's answer is
// CLIENT-built, so the schema is what the server validates arrivals against
// and the DTO check is the right shape of check for it: every outcome the
// closed vocabulary admits satisfies the schema, and the server accepts each
// one over the real socket in ws_clienthost_test.go.
func TestHostResolved_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "host.resolved.schema.json")
	for name, p := range map[string]hostResolvedParams{
		"ok with a path":               {Outcome: "ok", Path: "/home/dev/key"},
		"ok with none":                 {Outcome: "ok"},
		"cancelled":                    {Outcome: "cancelled"},
		"failed":                       {Outcome: "failed", Error: "no D-Bus session"},
		"unavailable":                  {Outcome: "unavailable", Error: "this client has no native host"},
		"unavailable with no sentence": {Outcome: "unavailable"},
	} {
		body, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		var wire map[string]any
		if decodeErr := json.Unmarshal(body, &wire); decodeErr != nil {
			t.Fatalf("%s: decode: %v", name, decodeErr)
		}
		// The envelope carries the broker-minted requestId beside the body —
		// the client echoes it back, and the broker correlates on it before
		// the kind's shape check runs.
		wire["requestId"] = "0123456789abcdef"
		raw, err := json.Marshal(wire)
		if err != nil {
			t.Fatalf("%s: marshal wire: %v", name, err)
		}
		validateJSON(t, schema, raw, "host.resolved DTO ("+name+")")
	}
}

// TestHostAttentionActivated_DTOConformsToContract — the click notification,
// the same way: client-built, so the schema is the shape the server's
// validator holds arrivals to.
func TestHostAttentionActivated_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "host.attentionActivated.schema.json")
	raw, err := json.Marshal(hostAttentionActivatedParams{SessionID: "s-7"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "host.attentionActivated DTO")
}
