package capability_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/transport/control"
)

// A HANDLER THAT RESOLVES MATERIAL INSIDE ITS OPERATION FAILS LOUDLY
// (nocx-0b1n2).
//
// This is the defect of nocx-o3606 and nocx-9fzkk written as the thing a
// handler actually does: take the config operation, and resolve an endpoint's
// credential from inside the callback. The config operation is composed from
// the vault gate, vault.unseal needs that gate to answer the unlock this
// read raises, and the person is shown a dialog whose Unlock is refused
// "Control plane busy".
//
// Before the fence, the assertion available here was "it blocks" — untestable
// without a timeout, which is why the rule stayed a comment through two
// occurrences. Now the read answers ErrUnlockUnanswerable, and the
// unsealer — the thing that shows the dialog — is never called at all.
func TestOperationHoldingTheAnsweringGateFencesAMaterialRead(t *testing.T) {
	store := &fenceStore{sealed: true}
	resolver := credential.NewResolver(store, nil, store)

	op := capability.NewConfigOperation(
		capability.Gate(capability.GateConfig, 1, 8, time.Second),
		capability.UnlockAnsweringGate(capability.GateVault, 1, 8, time.Second),
		testLane(), &fakeProfileRepo{}, &fakeGroupRepo{}, nil, nil, newProfileService(t), nil, nil, nil)

	var got error
	if err := op.Run(context.Background(), func(ctx context.Context, _ capability.ConfigService) error {
		_, got = resolver.Resolve(ctx, "sec:v1:file:endpoint", credential.Operation("audit a skill"))
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !errors.Is(got, credential.ErrUnlockUnanswerable) {
		t.Fatalf("Resolve error = %v, want ErrUnlockUnanswerable", got)
	}
	if store.ensureCalls != 0 {
		t.Fatalf("EnsureUnsealed calls = %d, want 0: the unlock was raised and could not be answered", store.ensureCalls)
	}
}

// THE SAME READ, ONE STEP LATER, IS THE FIX — which is what makes the fence a
// guard rather than a ban: the operation resolves what it needs from the
// store, releases, and reads the material outside.
func TestTheSameReadAfterTheOperationSucceeds(t *testing.T) {
	store := &fenceStore{sealed: true}
	resolver := credential.NewResolver(store, nil, store)

	op := capability.NewConfigOperation(
		capability.Gate(capability.GateConfig, 1, 8, time.Second),
		capability.UnlockAnsweringGate(capability.GateVault, 1, 8, time.Second),
		testLane(), &fakeProfileRepo{}, &fakeGroupRepo{}, nil, nil, newProfileService(t), nil, nil, nil)

	ctx := context.Background()
	if err := op.Run(ctx, func(context.Context, capability.ConfigService) error { return nil }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := resolver.Resolve(ctx, "sec:v1:file:endpoint", credential.Operation("audit a skill")); err != nil {
		t.Fatalf("Resolve after the operation: %v", err)
	}
	if store.ensureCalls != 1 {
		t.Fatalf("EnsureUnsealed calls = %d, want 1", store.ensureCalls)
	}
}

// AND AN OPERATION THAT HOLDS NO SUCH GATE IS UNTOUCHED. This is the ssh
// dial's shape — openOperation's Dial phase holds the execution lane and no
// domain gate — and it resolves a key passphrase inside its callback on
// purpose. A fence applied to every admission would have broken it, which is
// why the gate is declared rather than assumed.
func TestAnOperationWithoutTheAnsweringGateStillResolves(t *testing.T) {
	store := &fenceStore{sealed: true}
	resolver := credential.NewResolver(store, nil, store)

	op := capability.NewContentOperation(control.NewSemaphore(capability.GateContent, 1), testLane(), nil)

	var got error
	if err := op.Run(context.Background(), func(ctx context.Context, _ capability.ContentService) error {
		_, got = resolver.Resolve(ctx, "sec:v1:file:key", credential.Operation("load the key passphrase"))
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != nil {
		t.Fatalf("Resolve inside a lane-only operation failed: %v", got)
	}
	if store.ensureCalls != 1 {
		t.Fatalf("EnsureUnsealed calls = %d, want 1", store.ensureCalls)
	}
}

// fenceStore is a MaterialStore and an Unsealer in one, so a test can assert
// on the same object both that the material was reachable and whether the
// dialog was ever raised.
type fenceStore struct {
	sealed      bool
	ensureCalls int
}

func (s *fenceStore) Create(context.Context, credential.Secret) (credential.SecretID, error) {
	return "sec:v1:file:endpoint", nil
}
func (s *fenceStore) Delete(context.Context, credential.SecretID) error { return nil }
func (s *fenceStore) Exists(context.Context, credential.SecretID) (bool, error) {
	return true, nil
}

func (s *fenceStore) Get(context.Context, credential.SecretID) (credential.Secret, error) {
	if s.sealed {
		return credential.Secret{}, errors.New("vault is sealed")
	}
	return credential.NewSecret("sk-test"), nil
}

func (s *fenceStore) EnsureUnsealed(context.Context, string) error {
	s.ensureCalls++
	s.sealed = false
	return nil
}
