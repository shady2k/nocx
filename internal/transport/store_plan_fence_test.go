package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/operation/catalog"
)

// THE FENCE TRAVELS WITH THE PLAN THE ROOT HANDS THE CONFIG OPERATION.
//
// The config operation writes the config document and resolves vault row
// handles, so it holds the vault's store — and the answer to the unlock a
// resolve raises needs that same store. The mark is applied where the plan is
// built (domainGates), and this is the assertion that it reaches the operation
// a handler actually gets: a handler resolving material inside its callback
// must fail loudly rather than deadlock a person's Unlock (nocx-o3606,
// nocx-9fzkk — twice burned, which is why this is a test and not a comment).
func TestTheWiredConfigPlanFencesAnOperationStanceRead(t *testing.T) {
	store := &fencedStore{sealed: true}
	resolver := credential.NewResolver(store, nil, store)

	server := &WSServer{storeGuard: capability.NewStoreGuard(catalog.Declarations(), time.Second)}
	op, _ := server.buildConfigOp(server.domainGates().configVault)

	var read error
	err := op.Run(context.Background(), func(ctx context.Context, _ capability.ConfigService) error {
		_, read = resolver.Resolve(ctx, "sec:v1:file:endpoint", credential.Operation("audit a skill"))
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !errors.Is(read, credential.ErrUnlockUnanswerable) {
		t.Fatalf("a resolve inside the wired config operation = %v, want ErrUnlockUnanswerable", read)
	}
	if store.ensureCalls != 0 {
		t.Fatalf("EnsureUnsealed calls = %d: the unlock was raised and could not be answered", store.ensureCalls)
	}
}

// fencedStore is a MaterialStore and an Unsealer in one, so the test can see
// both that the store was reachable and whether the dialog was ever raised.
type fencedStore struct {
	sealed      bool
	ensureCalls int
}

func (s *fencedStore) Create(context.Context, credential.Secret) (credential.SecretID, error) {
	return "sec:v1:file:endpoint", nil
}

func (s *fencedStore) Delete(context.Context, credential.SecretID) error { return nil }

func (s *fencedStore) Exists(context.Context, credential.SecretID) (bool, error) { return true, nil }

func (s *fencedStore) Get(context.Context, credential.SecretID) (credential.Secret, error) {
	return credential.Secret{}, errors.New("the read reached the store")
}

func (s *fencedStore) EnsureUnsealed(context.Context, string) error {
	s.ensureCalls++
	return nil
}
