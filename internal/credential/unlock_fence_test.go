package credential_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
)

// A READ THAT CANNOT HAVE ITS UNLOCK ANSWERED NEVER RAISES ONE (nocx-0b1n2).
//
// The unsealer is what shows a person the dialog. Asserting that it was not
// called is therefore the assertion that matters here: an unanswerable prompt
// spends somebody's attention on a door with no handle, and answering the
// caller with an error instead is only better if the prompt never appears.
func TestResolverRefusesAnOperationReadInsideTheAnsweringGate(t *testing.T) {
	store := &stancedStore{sealed: true, secret: credential.NewSecret("value")}
	resolver := credential.NewResolver(store, nil, store)

	ctx := credential.WithUnlockAnswerGate(t.Context(), "vault")
	_, err := resolver.Resolve(ctx, "sec:v1:file:test", credential.Operation("audit a skill"))
	if !errors.Is(err, credential.ErrUnlockUnanswerable) {
		t.Fatalf("Resolve error = %v, want ErrUnlockUnanswerable", err)
	}
	if store.ensureCalls != 0 {
		t.Fatalf("EnsureUnsealed calls = %d, want 0: the prompt was raised and could not be answered", store.ensureCalls)
	}
	// The gate's name travels, because the developer who trips this needs to
	// know which admission they were inside.
	if !strings.Contains(err.Error(), `"vault"`) {
		t.Fatalf("the error does not name the gate: %v", err)
	}
}

// And the same read outside the gate is the ordinary one — the fence is a
// property of WHERE the read happens, not of the read.
func TestResolverAllowsTheSameReadOutsideTheGate(t *testing.T) {
	store := &stancedStore{sealed: true, secret: credential.NewSecret("value")}
	resolver := credential.NewResolver(store, nil, store)

	got, err := resolver.Resolve(t.Context(), "sec:v1:file:test", credential.Operation("audit a skill"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if store.ensureCalls != 1 {
		t.Fatalf("EnsureUnsealed calls = %d, want 1", store.ensureCalls)
	}
	if got.IsEmpty() {
		t.Fatal("the secret is empty")
	}
}

// A REPORT READ IS UNTOUCHED BY THE FENCE, and deliberately: it never waits
// for anybody, so there is no unlock to answer. Config reads that describe
// vault state run inside exactly this gate and must keep working.
func TestReportReadIsNotFencedInsideTheAnsweringGate(t *testing.T) {
	store := &stancedStore{sealed: true, secret: credential.NewSecret("value")}
	resolver := credential.NewResolver(store, func(err error) bool { return errors.Is(err, errSealed) }, store)

	ctx := credential.WithUnlockAnswerGate(t.Context(), "vault")
	_, err := resolver.Resolve(ctx, "sec:v1:file:test", credential.Report())
	if !errors.Is(err, credential.ErrSealedQuiet) {
		t.Fatalf("Resolve error = %v, want ErrSealedQuiet", err)
	}
	if store.ensureCalls != 0 {
		t.Fatalf("a report read raised an unlock: %d calls", store.ensureCalls)
	}
}
