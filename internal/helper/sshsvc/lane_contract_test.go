//go:build nocx_local_ssh

package sshsvc_test

// The lane's contract, checked on the payloads OFF THE WIRE (nocx-50w7p.10).
//
// contracts/helper/README.md states why this is the check and not a struct
// comparison: both ends of this socket are the same Go package, so a test that
// marshals its own value proves the struct is well-formed. What is validated
// here is what the two ends actually SENT — through the real framing and the
// real response envelopes — because these schemas are the frozen ABI and
// `additionalProperties: false` means a field added later is a break rather
// than an extension.
//
// The lane is the op this matters most for: its params are the whole of D3 at
// this level, and the schema is what refuses a command.

import (
	"io"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// TestTheLaneContractHoldsOverTheWire drives one lane and validates the params
// the coordinator sent and the result the helper answered, in both directions.
func TestTheLaneContractHoldsOverTheWire(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	f.execPeer = func(_ io.Reader, _ io.Writer) int { return 0 }
	stand := newStand(t, &coordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	})

	lane, err := stand.client.OpenLane(t.Context(), laneParams(t, f))
	if err != nil {
		t.Fatalf("open lane: %v", err)
	}
	t.Cleanup(func() { _ = lane.Close() })

	laneParamsSchema := loadHelperSchema(t, "ssh.lane.params.schema.json")
	laneResultSchema := loadHelperSchema(t, "ssh.lane.schema.json")

	// ── what the COORDINATOR sent ──────────────────────────────────────
	requests := 0
	for _, payload := range framesOf(t, stand.toCoord.bytes(), proto.TypeRequest) {
		req := decodeRequest(t, payload)
		if req.Service != proto.ServiceSSH || req.Op != proto.OpLane {
			continue
		}
		if err := validateHelperJSON(laneParamsSchema, req.Params); err != nil {
			t.Errorf("lane params off the wire do not satisfy their contract:\n%v\n\npayload was:\n%s", err, req.Params)
		}
		requests++
	}
	if requests == 0 {
		t.Fatal("no lane request was recorded, so its contract was never checked on the wire")
	}

	// ── what the HELPER answered ───────────────────────────────────────
	results := 0
	for _, payload := range framesOf(t, stand.toHelper.bytes(), proto.TypeResponse) {
		resp := decodeResponse(t, payload)
		if resp.Error != nil {
			continue
		}
		if err := validateHelperJSON(laneResultSchema, resp.Result); err != nil {
			t.Errorf("the lane result off the wire does not satisfy its contract:\n%v\n\npayload was:\n%s", err, resp.Result)
		}
		results++
	}
	if results == 0 {
		t.Fatal("no lane result was recorded, so its contract was never checked on the wire")
	}
}
