package transport

// The count test the task names: three or more callers racing a sealed
// vault produce exactly ONE vault.unlockRequest, and one answer resolves
// all of them — asserted over the real socket path the existing transport
// tests use (real vault, real WSServer, real WebSocket). The defect is in
// the path, not in a function, so the proof is too: the vault
// (internal/vault/unlock.go) holds the "one unlock pending" state, raises
// one prompt through the transport's RequestUnlock, and every caller that
// arrives while it is outstanding joins it.
//
// The count is deterministic by construction. While the first ask is
// pending, no second ask can exist — the vault's pending gate is the whole
// point of the seam. The resolution is sent only after the vault has been
// unsealed for real (the renderer's actual flow: the user unlocks, then the
// resolution is answered), so a caller that arrives late observes
// StateUnsealed and returns without raising anything. Exactly one ask can
// therefore exist in the whole test, and one answer must release all three
// callers. An implementation that raised one ask per caller would broadcast
// extra frames and leave the extra callers blocked on asks nobody answers —
// the negative frame check and the receive below would both catch it.

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/vault"
)

// countPrefixRe strips the composition prefix ("3 operations need the
// vault: ...") from a recorded reason so its remaining sentences can be
// checked against the callers that asked.
var countPrefixRe = regexp.MustCompile(`^\d+ operations need the vault: `)

// sealedUnlockHarness is a real vault (file provider) that is set up and
// sealed, wired to the real WSServer's RequestUnlock as its prompt carrier,
// with a real renderer connection attached.
type sealedUnlockHarness struct {
	h *configVaultHarness
}

func newSealedUnlockHarness(t *testing.T) *sealedUnlockHarness {
	t.Helper()
	h := newConfigVaultHarness(t)
	h.setupAndUnseal()
	h.v.Seal()
	if h.v.State() != vault.StateSealed {
		t.Fatalf("vault state = %v, want sealed", h.v.State())
	}
	h.v.SetUnlockRequester(unlockRequesterFunc(h.ws.RequestUnlock))
	return &sealedUnlockHarness{h: h}
}

// unlockRequesterFunc adapts the method value to the vault's
// UnlockRequester interface (a method value is a func, not a method).
type unlockRequesterFunc func(ctx context.Context, reason string) error

func (f unlockRequesterFunc) RequestUnlock(ctx context.Context, reason string) error {
	return f(ctx, reason)
}

// unlockRequestFrame is the vault.unlockRequest notification shape.
type unlockRequestFrame struct {
	RequestID string `json:"requestId"`
	Reason    string `json:"reason"`
}

// readUnlockRequestFrame reads the next vault.unlockRequest notification
// off the socket and returns its params.
func readUnlockRequestFrame(t *testing.T, conn *websocket.Conn) unlockRequestFrame {
	t.Helper()
	raw := readNotification(t, conn, "vault.unlockRequest", wantWithin)
	var f unlockRequestFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("unmarshal vault.unlockRequest: %v", err)
	}
	if f.RequestID == "" {
		t.Fatal("empty requestId in vault.unlockRequest")
	}
	return f
}

// answerUnlock resolves the ask with the given outcome and expects the
// resolution RPC to succeed.
func answerUnlock(t *testing.T, conn *websocket.Conn, rid, outcome string) {
	t.Helper()
	resp := vaultCall(t, conn, "vault.unlockResolved", map[string]any{
		"requestId": rid,
		"outcome":   outcome,
	}, 2)
	if resp.Error != nil {
		t.Fatalf("vault.unlockResolved error: %s", resp.Error.Message)
	}
}

func TestVaultEnsureUnsealed_ThreeCallersOnePromptOverTheSocket(t *testing.T) {
	s := newSealedUnlockHarness(t)
	ctx := t.Context()

	reasons := []string{"ssh srv-01 needs the vault", "history needs the content key", "ssh srv-02 needs the vault"}
	done := make(chan error, len(reasons))
	for _, r := range reasons {
		go func(r string) { done <- s.h.v.EnsureUnsealed(ctx, r) }(r)
	}

	// Exactly one vault.unlockRequest reaches the renderer.
	frame := readUnlockRequestFrame(t, s.h.conn)

	// The reason is a real caller's sentence or the composed form — never
	// empty, never a caller that did not exist. (The composition is pinned
	// deterministically in internal/vault; here the socket proves the reason
	// reaches the renderer at all.)
	if frame.Reason == "" {
		t.Fatal("vault.unlockRequest carried an empty reason")
	}
	body := countPrefixRe.ReplaceAllString(frame.Reason, "")
	for _, sentence := range strings.Split(body, "; ") {
		if sentence == "" {
			continue
		}
		known := false
		for _, want := range reasons {
			if sentence == want {
				known = true
				break
			}
		}
		if !known {
			t.Errorf("reason %q names %q, which no caller asked for", frame.Reason, sentence)
		}
	}

	// The renderer's flow: the user unlocks the vault, THEN the resolution
	// is answered. Unsealing first is what makes the count deterministic —
	// a caller arriving late observes StateUnsealed and cannot raise a
	// second prompt.
	if err := s.h.v.Unseal(ctx, vault.UnsealRequest{Passphrase: "test"}); err != nil {
		t.Fatalf("unseal the vault: %v", err)
	}
	answerUnlock(t, s.h.conn, frame.RequestID, "unsealed")

	// One answer resolves all three callers.
	for i := 0; i < len(reasons); i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("caller %d = %v, want nil after the unlock", i, err)
			}
		case <-time.After(wantWithin):
			t.Fatalf("caller %d was never released by the single resolution", i)
		}
	}

	// No second frame ever went out. This is the last read on the
	// connection (gorilla makes a read error permanent), which is exactly
	// where a negative check belongs.
	if second := tryReadNotification(t, s.h.conn, "vault.unlockRequest", 300*time.Millisecond); second != nil {
		t.Fatalf("a second vault.unlockRequest went out: %s", second)
	}
}

// TestVaultUnlockResolved_AnAnswerArrivingAfterTheVaultRetiredTheAskIsAccepted
// pins the other half of the retirement: the CLIENT'S SIDE of it.
//
// A prompt raised for a sealed vault is retired the moment another path
// answers the question it was asking (internal/vault/unlock.go, nocx-1ebrs.7),
// and the renderer that was shown the dialog knows nothing about that — it has
// a person in front of it who may finish typing. Sending the resolution for
// that dialog is the ordinary thing for such a client to do, so it is
// accepted as the no-op it is: success, no state change, no new prompt.
//
// The alternative was measured: the resolution was refused as "Unknown request
// id", which is what made the renderer swallow the result of its own answer
// call — a race every surface that shows a prompt would otherwise inherit.
//
// The refusal for an id that was NEVER minted is the paired case and stays:
// TestUnlockResolved_UnknownRequestID.
func TestVaultUnlockResolved_AnAnswerArrivingAfterTheVaultRetiredTheAskIsAccepted(t *testing.T) {
	s := newSealedUnlockHarness(t)
	ctx := t.Context()

	done := make(chan error, 1)
	go func() { done <- s.h.v.EnsureUnsealed(ctx, "the ssh session needs its key") }()

	frame := readUnlockRequestFrame(t, s.h.conn)

	// Another path answers the question the prompt was asking — the
	// container's unsealIfSealed, Settings, or a second window — while the
	// renderer still has the dialog on screen.
	if err := s.h.v.Unseal(ctx, vault.UnsealRequest{Passphrase: "test"}); err != nil {
		t.Fatalf("unseal the vault from the other path: %v", err)
	}

	// The person finishes the dialog they were given.
	answerUnlock(t, s.h.conn, frame.RequestID, "unsealed")

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the waiting operation = %v, want nil: the vault is open and the question was answered", err)
		}
	case <-time.After(wantWithin):
		t.Fatal("the waiting operation was never released after the vault was unsealed")
	}

	// The late answer raised nothing: no second prompt went out to anybody.
	if second := tryReadNotification(t, s.h.conn, "vault.unlockRequest", 300*time.Millisecond); second != nil {
		t.Fatalf("a prompt went out after the vault was already open: %s", second)
	}
}
