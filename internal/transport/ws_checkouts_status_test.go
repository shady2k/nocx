package transport

// checkouts.status — the surface that says whether the checkout sweep can
// run, and why not (nocx-xn63t.1.6).
//
// These tests drive the real handler over the real socket, the way
// ws_history_status_test.go does for the surface this one is modelled on.
// What they assert is what a user can find out: the method answers even
// with nothing wired, the answer satisfies the contract in both shapes, and
// the degrade the composition root raises is what a renderer reads back.

import (
	"encoding/json"
	"testing"
)

// checkoutsStatusResult is the decoded checkouts.status result for
// assertions. The DTO contract test is what proves the key set exact; this
// names only the fields an assertion is about.
type checkoutsStatusResult struct {
	Available bool    `json:"available"`
	Reason    *string `json:"reason"`
	Detail    *string `json:"detail"`
}

func decodeCheckoutsStatus(t *testing.T, resp *vaultRPCResult) checkoutsStatusResult {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if resp.Result == nil {
		t.Fatal("expected a result")
	}
	var out checkoutsStatusResult
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("decode result: %v\nraw: %s", err, resp.Result)
	}
	return out
}

// The DTO, in both shapes it can take, satisfies the schema: running, and
// degraded with a reason and a detail.
func TestCheckoutsStatus_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "checkouts.status.schema.json")
	reason := CheckoutSweepDegradeNoRecord
	detail := "the content store is unavailable, so the checkout record cannot be read"
	cases := map[string]checkoutsStatusResponse{
		"running": {Available: true},
		"degraded with a detail": {
			Available: false,
			Reason:    &reason,
			Detail:    &detail,
		},
	}
	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "checkouts.status DTO")
		})
	}
}

// A server whose composition root never raised anything reports the sweep
// as able to run. The default has to be "able", not "unknown": the renderer
// has one status to read and no third state to render. And the method
// answers with NO status wired at all — a renderer reads it on every render
// of the Worktrees settings, and a method that vanishes is a worse answer
// than a boolean.
func TestCheckoutsStatus_DefaultIsAvailable(t *testing.T) {
	ws, stop := newHistoryWSServer(t, nil)
	defer stop()
	conn := connectWS(t, ws)
	got := decodeCheckoutsStatus(t, vaultCall(t, conn, "checkouts.status", map[string]any{}, 1))

	if !got.Available {
		t.Fatal("available = false, want true with nothing raised")
	}
	if got.Reason != nil {
		t.Fatalf("reason = %q, want null while available", *got.Reason)
	}
	if got.Detail != nil {
		t.Fatalf("detail = %q, want null while available", *got.Detail)
	}
}

// The degrade the composition root raises is what the method reports —
// reason and detail both, because "cannot run" without a why is a dead end
// for the person reading it. This is the REAL result off the REAL socket,
// not a payload the test built.
func TestCheckoutsStatus_RaiseIsVisibleOverTheWire(t *testing.T) {
	st := NewCheckoutSweepStatus()
	st.RaiseUnavailable(CheckoutSweepDegradeNoRecord,
		"the content store is unavailable, so the checkout record cannot be read")
	ws, stop := newHistoryWSServer(t, nil, WithCheckoutSweepStatus(st))
	defer stop()
	conn := connectWS(t, ws)
	got := decodeCheckoutsStatus(t, vaultCall(t, conn, "checkouts.status", map[string]any{}, 1))

	if got.Available {
		t.Fatal("available = true after a raise, want false")
	}
	if got.Reason == nil || *got.Reason != string(CheckoutSweepDegradeNoRecord) {
		t.Fatalf("reason = %v, want %q", got.Reason, CheckoutSweepDegradeNoRecord)
	}
	if got.Detail == nil || *got.Detail != "the content store is unavailable, so the checkout record cannot be read" {
		t.Fatalf("detail = %v, want the underlying failure", got.Detail)
	}
}
