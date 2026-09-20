package proto

import (
	"encoding/json"
	"strings"
	"testing"
)

// The completion downlink's wire shape (the coordinator carries an
// already-authenticated completion DOWN to the helper; owner decision
// 2026-09-19). These tests decide the shape while nothing is deployed to
// disagree with it.

// TestLifecycleCompleteRoundTrips is the shape's round trip: the params
// marshal to the exact keys the contract will declare and decode back to the
// same value, because a sender and a reader that disagree about one key is a
// completion silently dropped one generation on.
func TestLifecycleCompleteRoundTrips(t *testing.T) {
	exit := 3
	in := LifecycleCompleteParams{
		Session:     HostSessionID{Generation: GenerationID("gen-under-test"), Session: "0123456789abcdef0123456789abcdef"},
		Incarnation: Incarnation{Session: "0123456789abcdef0123456789abcdef", Generation: 1},
		Nonce:       strings.Repeat("ab", 32),
		ExitCode:    &exit,
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out LifecycleCompleteParams
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	if out.Session != in.Session || out.Incarnation != in.Incarnation || out.Nonce != in.Nonce {
		t.Fatalf("round trip changed the shape:\n got %+v\nwant %+v", out, in)
	}
	if out.ExitCode == nil || *out.ExitCode != *in.ExitCode {
		t.Fatalf("round trip changed exitCode: got %v, want %d", out.ExitCode, *in.ExitCode)
	}
}

// TestLifecycleCompleteIncarnationIsANumberNotTheInstallID is the identity
// half, pinned at the wire: the incarnation's generation rides as a NUMBER,
// because it is sessionruntime's per-PTY generation (the helper mints 1), and
// HostSessionID.Generation is the content-addressed INSTALL id — a string.
// Reusing the install field for the runtime fact would parse "gen-under-test"
// as a uint64; this test is where that defect dies.
func TestLifecycleCompleteIncarnationIsANumberNotTheInstallID(t *testing.T) {
	raw := `{"session":{"generation":"gen-under-test","session":"0123456789abcdef0123456789abcdef"},` +
		`"incarnation":{"session":"0123456789abcdef0123456789abcdef","generation":1},` +
		`"nonce":"` + strings.Repeat("00", 32) + `","exitCode":null}`
	var out LifecycleCompleteParams
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Incarnation.Generation != 1 || out.Incarnation.Session != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("incarnation decoded as %+v, want the runtime's own {session, 1}", out.Incarnation)
	}
	if out.Session.Generation != GenerationID("gen-under-test") {
		t.Fatalf("session generation decoded as %q, want the install id", out.Session.Generation)
	}
	if out.ExitCode != nil {
		t.Fatalf("exitCode decoded as %d, want null preserved as nil — null is the shell named none, never a zero", *out.ExitCode)
	}
}

// TestLifecycleCompleteNonceIsSixtyFourLowercaseHex pins the fence's wire
// spelling to the launch's bearer-value spelling: fixed width, lowercase, hex.
// A decoder that accepted anything else would silently truncate a nonce and
// hand the runtime a rendezvous it can never match.
func TestLifecycleCompleteNonceIsSixtyFourLowercaseHex(t *testing.T) {
	exit := 0
	in := LifecycleCompleteParams{
		Session:     HostSessionID{Generation: "g", Session: "0123456789abcdef0123456789abcdef"},
		Incarnation: Incarnation{Session: "0123456789abcdef0123456789abcdef", Generation: 1},
		Nonce:       strings.Repeat("0a", 32),
		ExitCode:    &exit,
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"nonce":"0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a"`) {
		t.Fatalf("nonce did not ride as 64 lowercase hex: %s", raw)
	}
}
