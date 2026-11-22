package vault

// AN ASK IS A QUESTION ABOUT THE VAULT'S STATE, NOT A DIALOG WITH A LIFE OF
// ITS OWN (nocx-1ebrs.7).
//
// Measured in the e2e stand on the branch that made the prompt visible: a
// restored ssh pane asked the helper's coordinator for a stored password
// while the replacement backend's vault was still sealed and no client was
// attached, so the ask suspended. The spec then unsealed the vault by its
// RPC 104 ms later. At 1.75 s the fresh page attached, the suspended ask was
// re-raised and presented — a passphrase demanded after it had been given,
// holding focus, with nothing behind it to answer.
//
// Two halves are pinned here. The raise loop re-reads the vault's state after
// every wait and answers its waiters with nil when the vault is already open;
// and Unseal retires an ask that is already on its way to a person. The pair
// below is the whole point: an ask suspended against a sealed vault that is
// unsealed by another path must not be raised at the attach, and one still
// sealed at the attach must be, exactly once — otherwise "never raise" would
// pass the first test.

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/waittest"
)

// sealedVaultWithASecret is sealedVault's shape plus the one thing a test
// about "the read proceeds" needs: a secret to read once the vault is open.
func sealedVaultWithASecret(t *testing.T) (*Vault, credential.SecretID) {
	t.Helper()
	v, _, _ := testVault(t, newTestFileProvider(ProviderFile))
	mustSetup(t, v, "hunter2")
	id, err := v.Create(context.Background(), credential.NewSecret("s3cret"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	v.Seal()
	if v.State() != StateSealed {
		t.Fatalf("state = %v, want sealed", v.State())
	}
	return v, id
}

// suspendedOnAbsentClient starts one operation against a sealed vault with
// nobody attached and waits until it is suspended — the state every test here
// starts from, waited for on the vault's own state rather than on a duration.
func suspendedOnAbsentClient(t *testing.T, v *Vault) chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- v.EnsureUnsealed(context.Background(), "the restored ssh pane needs its key")
	}()
	waittest.WaitFor(t, "the operation to suspend on an absent client", func() bool {
		v.mu.Lock()
		defer v.mu.Unlock()
		return v.clientWait != nil
	})
	return done
}

func TestEnsureUnsealed_AnAskSuspendedWithNoClientIsNotRaisedAfterAnotherPathUnseals(t *testing.T) {
	req := &fakeUnlockRequester{entered: make(chan struct{}, 4)}
	v, id := sealedVaultWithASecret(t)
	v.SetUnlockRequester(req)
	v.ClientsAttached(0)

	done := suspendedOnAbsentClient(t, v)
	if n := req.callCount(); n != 0 {
		t.Fatalf("a prompt was raised %d times with nobody attached to answer it", n)
	}

	// Another path answers the question the suspended ask was going to ask:
	// the settings surface, a second window, the upgrade flow — whoever holds
	// the passphrase. The suspended operation never saw a dialog.
	if err := v.Unseal(context.Background(), UnsealRequest{Passphrase: "hunter2"}); err != nil {
		t.Fatalf("Unseal from the other path: %v", err)
	}

	v.ClientsAttached(1)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EnsureUnsealed = %v, want nil: the vault is open and the question was already answered", err)
		}
	case <-time.After(waittest.DefaultTimeout):
		t.Fatal("the waiting operation was never released after the vault was unsealed")
	}
	if n := req.callCount(); n != 0 {
		t.Fatalf("a prompt was raised %d times after another path had already unsealed the vault", n)
	}

	// And the read the operation was waiting to make proceeds.
	got, err := v.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("the read the operation was waiting for: %v", err)
	}
	if err := got.Use(func(b []byte) error {
		if string(b) != "s3cret" {
			t.Errorf("secret = %q, want %q", b, "s3cret")
		}
		return nil
	}); err != nil {
		t.Fatalf("Use: %v", err)
	}
}

// THE PAIRED TEST: the same start, one fact different. Nobody unsealed the
// vault, so the person returning to the window must still be asked — exactly
// once, for the operation that is waiting.
func TestEnsureUnsealed_AStillSealedVaultRaisesExactlyOnePromptAtTheAttach(t *testing.T) {
	req := &fakeUnlockRequester{
		entered: make(chan struct{}, 4),
		release: make(chan error, 1),
	}
	v := sealedVault(t, req)
	v.ClientsAttached(0)

	done := suspendedOnAbsentClient(t, v)
	if n := req.callCount(); n != 0 {
		t.Fatalf("a prompt was raised %d times with nobody attached to answer it", n)
	}

	v.ClientsAttached(1) // nothing unsealed the vault

	select {
	case <-req.entered:
	case <-time.After(waittest.DefaultTimeout):
		t.Fatal("no prompt was raised when a client attached to a still-sealed vault")
	}
	req.release <- nil
	if err := <-done; err != nil {
		t.Fatalf("EnsureUnsealed = %v, want nil after the prompt was answered", err)
	}
	if n := req.callCount(); n != 1 {
		t.Fatalf("prompts raised = %d, want exactly 1 for the one waiting operation", n)
	}
}

// The other half of the same defect: the ask is already on the wire when
// another path answers it. The attempt is abandoned, the raise loop re-reads
// the state, and the waiters are released with the vault open instead of
// being left with a dialog whose question no longer has an answer.
func TestUnseal_RetiresAnAskAlreadyOnTheWire(t *testing.T) {
	req := &fakeUnlockRequester{
		entered: make(chan struct{}, 4),
		release: make(chan error, 1),
	}
	v := sealedVault(t, req)
	v.ClientsAttached(1)

	done := make(chan error, 1)
	go func() { done <- v.EnsureUnsealed(context.Background(), "the restored ssh pane needs its key") }()
	select {
	case <-req.entered:
	case <-time.After(waittest.DefaultTimeout):
		t.Fatal("the prompt was never raised")
	}

	// A person unlocks from somewhere else while the dialog is up. The fake
	// requester's ask is blocked, as the transport's is: it returns only when
	// the renderer answers or its context ends.
	if err := v.Unseal(context.Background(), UnsealRequest{Passphrase: "hunter2"}); err != nil {
		t.Fatalf("Unseal while the ask is on the wire: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EnsureUnsealed = %v, want nil: the unseal answered the ask", err)
		}
	case <-time.After(waittest.DefaultTimeout):
		t.Fatal("the waiter was never released; the ask outlived the answer to its own question")
	}
	waittest.WaitFor(t, "the outstanding prompt to be retired", func() bool {
		v.mu.Lock()
		defer v.mu.Unlock()
		return v.unlockPending == nil
	})
	if n := req.callCount(); n != 1 {
		t.Fatalf("prompts raised = %d, want 1: the ask was retired, not raised again", n)
	}
}
