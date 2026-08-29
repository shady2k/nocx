package vault

import (
	"context"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
)

// ADR-0050 step 1: there is no mode in which the root key is obtainable
// without a passphrase or a recovery code.
//
// Setup used to have a second mode. With an empty passphrase and a system
// provider that reported itself ready, it minted a root key, put it in the OS
// keystore, and minted NEITHER a passphrase envelope NOR a recovery code — so
// the machine that lost that keychain item had lost the vault, and any process
// under the same login could take the key with one `security
// find-generic-password -w`, because the trusted application in the item's ACL
// is /usr/bin/security itself.
//
// The tests below are the closing half of that decision. They are written as
// the interval AGENTS.md asks for rather than as a moment: the refusal, and
// the state the vault is in afterwards, and what the provider was asked to
// store while it was being refused.

func TestSetup_EmptyPassphraseRefusedWithReadySystemProvider(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, store, _ := testVault(t, sys, fp)

	_, err := v.Setup(context.Background(), SetupRequest{})
	if err == nil {
		t.Fatal("Setup with an empty passphrase must be refused, even where the OS keystore is ready")
	}
	if !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("error must name what is required, got %q", err)
	}

	// The vault minted nothing: it is exactly where it started, so the next
	// Setup — the one that carries a passphrase — is the first one.
	if got := v.State(); got != StateUninitialized {
		t.Errorf("state after refusal = %v, want %v", got, StateUninitialized)
	}
	var doc Document
	found, rerr := store.Read("vault.json", &doc)
	if rerr != nil {
		t.Fatalf("Read: %v", rerr)
	}
	if found {
		t.Error("a refused Setup wrote a vault document")
	}

	// And it minted nothing INTO THE KEYSTORE, which is the part that mattered.
	sys.mu.Lock()
	n := len(sys.data)
	sys.mu.Unlock()
	if n != 0 {
		t.Errorf("refused Setup wrote %d item(s) to the system provider, want 0", n)
	}
}

// The paired "and on a normal machine it succeeds" for the refusal above — a
// refusal test alone cannot report that the remaining path still works.
func TestSetup_WithPassphraseMintsBothEnvelopesAndTouchesNoKeystore(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, store, _ := testVault(t, sys, fp)

	result, err := v.Setup(context.Background(), SetupRequest{Passphrase: "hunter2"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if result.RecoveryCode == "" {
		t.Fatal("every vault gets a recovery code; this one got none")
	}
	if v.State() != StateUnsealed {
		t.Fatalf("state = %v, want %v", v.State(), StateUnsealed)
	}

	var doc Document
	found, err := store.Read("vault.json", &doc)
	if err != nil || !found {
		t.Fatalf("Read: %v found=%v", err, found)
	}
	if doc.Passphrase == nil {
		t.Error("no passphrase envelope")
	}
	if doc.Recovery == nil {
		t.Error("no recovery envelope")
	}

	// Setup writes no key material to the keystore on ANY path now. The
	// system provider remains a store for individual secrets a person
	// creates (ADR-0050 declined to remove it) — but not for the root key,
	// and not for anything that verifies a passphrase.
	sys.mu.Lock()
	n := len(sys.data)
	sys.mu.Unlock()
	if n != 0 {
		t.Errorf("Setup wrote %d item(s) to the system provider, want 0", n)
	}
}

// ADR-0050 step 2: the keychain keeps NO verification material.
//
// Three candidates, each refused for its own reason, and the reasons are here
// rather than in the ADR alone because the next person to add a convenience
// will read this test and not that document:
//
//   - A HASH OF THE PASSPHRASE opens nothing, so it buys no convenience — and
//     it adds an offline verification oracle the keychain does not otherwise
//     give: copy it and guess forever, with no vault in the loop to rate-limit
//     or notice.
//   - THE ARGON2ID OUTPUT is not a verifier at all. It IS the key-encryption
//     key, so storing it recreates the silent setup this epic removed, with a
//     layer of indirection on top.
//   - THE RECOVERY CODE, or its envelope, exists to survive the LOSS OF THE
//     MACHINE. A copy on that machine defeats the only case it is for.
//
// The assertion is at the seam rather than in a comment: a provider that
// records every Put, driven through the whole lifecycle a vault has.
func TestKeystoreHoldsNoVerificationMaterial(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	ctx := context.Background()

	result, err := v.Setup(ctx, SetupRequest{Passphrase: "first-pass"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if cerr := v.ChangePassphrase(ctx, ChangePassphraseRequest{
		OldPassphrase: "first-pass",
		NewPassphrase: "second-pass",
	}); cerr != nil {
		t.Fatalf("ChangePassphrase: %v", cerr)
	}
	newCode, err := v.RegenerateRecovery(ctx, RegenerateRequest{Passphrase: "second-pass"})
	if err != nil {
		t.Fatalf("RegenerateRecovery: %v", err)
	}
	if newCode == result.RecoveryCode {
		t.Error("RegenerateRecovery returned the original code")
	}

	v.Seal()
	if err := v.Unseal(ctx, UnsealRequest{Passphrase: "second-pass"}); err != nil {
		t.Fatalf("Unseal by passphrase: %v", err)
	}
	v.Seal()
	if err := v.Unseal(ctx, UnsealRequest{RecoveryCode: newCode}); err != nil {
		t.Fatalf("Unseal by recovery code: %v", err)
	}

	sys.mu.Lock()
	ids := make([]credential.SecretID, 0, len(sys.data))
	for id := range sys.data {
		ids = append(ids, id)
	}
	sys.mu.Unlock()
	if len(ids) != 0 {
		t.Errorf("the vault's own lifecycle wrote %d item(s) to the keystore: %v; want none", len(ids), ids)
	}
}

// The recorder is not broken: the same lifecycle, plus one secret a person
// asked for, writes exactly that secret and nothing else.
//
// Without this, the assertion above passes just as well against a provider
// that silently drops every Put — which is the shape of test that reports a
// property it never measured.
func TestKeystoreHoldsExactlyTheSecretsAPersonCreated(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	ctx := context.Background()

	if _, err := v.Setup(ctx, SetupRequest{Passphrase: "first-pass"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	// Setup chooses the system provider when it is ready, which it is here —
	// so a Create lands in the keystore and this test is measuring the store
	// the previous one asserted was untouched.
	if err := v.SetDefaultProvider(ctx, ProviderSystem); err != nil {
		t.Fatalf("SetDefaultProvider: %v", err)
	}

	id, err := v.Create(ctx, credential.NewSecret("the-password"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := v.ChangePassphrase(ctx, ChangePassphraseRequest{
		OldPassphrase: "first-pass",
		NewPassphrase: "second-pass",
	}); err != nil {
		t.Fatalf("ChangePassphrase: %v", err)
	}
	if _, err := v.RegenerateRecovery(ctx, RegenerateRequest{Passphrase: "second-pass"}); err != nil {
		t.Fatalf("RegenerateRecovery: %v", err)
	}
	v.Seal()
	if err := v.Unseal(ctx, UnsealRequest{Passphrase: "second-pass"}); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	sys.mu.Lock()
	ids := make([]credential.SecretID, 0, len(sys.data))
	for gotID := range sys.data {
		ids = append(ids, gotID)
	}
	sys.mu.Unlock()
	if len(ids) != 1 || ids[0] != id {
		t.Errorf("keystore holds %v, want exactly the one created secret %v", ids, id)
	}
}
