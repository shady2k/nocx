package app

// D9, watched end to end through production wiring.
//
// The unit halves are asserted where they live — the count in
// internal/transport, the policy in internal/vault. This is the check that
// the two are wired to each other in the shipped composition root: a real
// client on a real socket unlocks a real vault, goes away, and the vault is
// shut when anybody looks again.
//
// It reads the vault's state over vault.status rather than through a test
// accessor, because that is the same fact the product reads, and because a
// backend that seals without telling the renderer has not solved anything.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/waittest"
)

const detachTestPassphrase = "correct horse battery staple"

// startedApp boots the real composition root with a socket to dial.
func startedApp(t *testing.T) *App {
	t.Helper()
	storagetest.IsolateWithHome(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { a.Shutdown(context.Background()) })
	return a
}

// vaultStateOver reads the vault's lifecycle state off the wire.
func vaultStateOver(t *testing.T, conn *websocket.Conn, id int) string {
	t.Helper()
	resp := callAppWS(t, conn, "vault.status", nil, id)
	if resp.Error != nil {
		t.Fatalf("vault.status: %s", resp.Error.Message)
	}
	var status struct {
		State           string `json:"state"`
		DefaultProvider string `json:"defaultProvider"`
		Providers       []struct {
			ID     string `json:"id"`
			Ready  bool   `json:"ready"`
			Reason string `json:"reason"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(resp.Result, &status); err != nil {
		t.Fatalf("decode vault.status: %v", err)
	}
	return status.State
}

// waitForSealedOverTheWire polls the product's own answer: dial, ask
// vault.status, hang up. It waits on that state and never on a duration.
//
// The re-dial is deliberate rather than incidental. A client that attaches
// while the previous one's detach has not yet been noticed is a count of two
// followed by a count of one, and one is not zero — so a single reattach
// cannot be asserted on without a timing assumption about which of the two
// the server processes first. Every poll here ends with its own detach, so
// the sequence converges on a count of zero whatever order the first pair
// landed in, and the claim under test — a detach that leaves nobody attached
// seals the vault — is what each iteration exercises.
func waitForSealedOverTheWire(t *testing.T, a *App) {
	t.Helper()
	var last string
	waittest.WaitForDetail(t, "the vault to seal after the last client detached",
		func() string {
			return "the vault reports " + last + "; the root key is still in a " +
				"coordinator that outlives every window"
		},
		func() bool {
			conn := dialAppWS(t, a)
			defer func() { _ = conn.Close() }()
			last = vaultStateOver(t, conn, 90)
			return last == vault.StateSealed.String()
		})
}

// TestTheVaultSealsWhenTheLastClientDetaches is the epic's sentence: a person
// unlocks the vault, closes the window, and the root key is not sitting in
// the coordinator when they come back.
func TestTheVaultSealsWhenTheLastClientDetaches(t *testing.T) {
	a := startedApp(t)

	first := dialAppWS(t, a)
	if resp := callAppWS(t, first, "vault.setup",
		map[string]any{"passphrase": detachTestPassphrase}, 1); resp.Error != nil {
		t.Fatalf("vault.setup: %s", resp.Error.Message)
	}
	if got := vaultStateOver(t, first, 2); got != vault.StateUnsealed.String() {
		t.Fatalf("after setup the vault is %q, want unsealed; the interval under "+
			"test never starts", got)
	}

	// The window closes. Nothing else happens — no shutdown, no timer.
	_ = first.Close()

	waitForSealedOverTheWire(t, a)

	// And sealing is not a one-way door: the person who came back can open it.
	second := dialAppWS(t, a)
	defer func() { _ = second.Close() }()
	if resp := callAppWS(t, second, "vault.unseal",
		map[string]any{"means": "passphrase", "secret": detachTestPassphrase}, 4); resp.Error != nil {
		t.Fatalf("vault.unseal after reattach: %s", resp.Error.Message)
	}
	if got := vaultStateOver(t, second, 5); got != vault.StateUnsealed.String() {
		t.Fatalf("the vault is %q after unsealing on the second client, want unsealed", got)
	}
}

// A SECOND WINDOW CLOSING IS NOT THE LAST CLIENT LEAVING, and that half is
// asserted in internal/vault (TestClientsAttached_ASecondWindowClosingDoesNotSeal)
// rather than here. From the wire there is no observable that orders "the
// detach has been processed" before a status read, so an end-to-end version
// could only wait out a duration — and a test that needs a slow machine to
// pass is broken on a fast one too (AGENTS.md). The policy is deterministic
// where the policy lives; what this file exists to prove is that the policy
// is wired to a real socket at all.
