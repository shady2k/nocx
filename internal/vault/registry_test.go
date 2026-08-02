package vault

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
)

// fakeProvider is read-only on purpose: it has no Put or Delete, so the
// registry must report it as not writable. There is deliberately no
// `writable bool` field — a flag would let the test assert a tag-driven answer
// the production code is forbidden to give.
type fakeProvider struct {
	id ProviderID
}

func (f fakeProvider) ID() ProviderID                { return f.id }
func (f fakeProvider) Status(context.Context) Status { return Status{Ready: true} }
func (f fakeProvider) Get(context.Context, credential.SecretID) (credential.Secret, error) {
	return credential.Secret{}, nil
}

type fakeWritable struct{ fakeProvider }

func (f fakeWritable) Put(context.Context, credential.SecretID, credential.Secret) error { return nil }

func (f fakeWritable) Delete(context.Context, credential.SecretID) error { return nil }

func TestRegistryRejectsDuplicateID(t *testing.T) {
	_, err := NewRegistry(fakeProvider{id: ProviderFile}, fakeProvider{id: ProviderFile})
	if err == nil {
		t.Fatal("NewRegistry accepted a duplicate provider id")
	}
}

func TestRegistryRejectsInvalidTag(t *testing.T) {
	if _, err := NewRegistry(fakeProvider{id: "Not:Valid"}); err == nil {
		t.Fatal("NewRegistry accepted an invalid provider tag")
	}
}

// Capability is discovered by interface satisfaction (AD-8), never by a tag.
func TestRegistryWritableIsByInterfaceNotByTag(t *testing.T) {
	r, err := NewRegistry(fakeProvider{id: ProviderSystem}, fakeWritable{fakeProvider{id: ProviderFile}})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if _, ok := r.Writable(ProviderSystem); ok {
		t.Fatal("a read-only provider was reported writable")
	}
	if _, ok := r.Writable(ProviderFile); !ok {
		t.Fatal("a writable provider was not reported writable")
	}
}

func TestProviderUnavailableCarriesReason(t *testing.T) {
	err := unavailable(ProviderSystem, ReasonNoService, errors.New("no bus"))
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal("errors.Is(ErrProviderUnavailable) failed")
	}
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Reason != ReasonNoService {
		t.Fatalf("reason not preserved: %#v", pe)
	}
}
