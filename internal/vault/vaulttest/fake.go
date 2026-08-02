// Package vaulttest provides test helpers for the vault package, including an
// in-memory WritableProvider that other packages can use without a real secret
// store.
package vaulttest

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/vault"
)

// Fake is an in-memory WritableProvider for testing. It stores secrets in a
// map guarded by a sync.Mutex. Use NewFake to create one.
type Fake struct {
	mu      sync.Mutex
	store   map[credential.SecretID][]byte
	id      vault.ProviderID
	failure error
	delay   time.Duration
}

// NewFake returns a fresh, empty Fake provider. The reported ProviderID is
// ProviderFile.
func NewFake() *Fake {
	return &Fake{
		store: make(map[credential.SecretID][]byte),
		id:    vault.ProviderFile,
	}
}

// NewFakeWithID creates a Fake with a specific ProviderID. Use this when a
// test needs two fakes with distinct identities (e.g. cross-provider routing).
func NewFakeWithID(id vault.ProviderID) *Fake {
	return &Fake{
		store: make(map[credential.SecretID][]byte),
		id:    id,
	}
}

func (f *Fake) ID() vault.ProviderID { return f.id }

func (f *Fake) Status(_ context.Context) vault.Status {
	return vault.Status{Ready: true}
}

func (f *Fake) Get(_ context.Context, id credential.SecretID) (credential.Secret, error) {
	failure, delay := f.snapshot()
	if failure != nil {
		return credential.Secret{}, failure
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	f.mu.Lock()
	b, ok := f.store[id]
	f.mu.Unlock()
	if !ok {
		return credential.Secret{}, vault.ErrSecretNotFound
	}
	return credential.NewSecretBytes(b), nil
}

func (f *Fake) Put(_ context.Context, id credential.SecretID, s credential.Secret) error {
	failure, delay := f.snapshot()
	if failure != nil {
		return failure
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	var val []byte
	if err := s.Use(func(b []byte) error { val = bytes.Clone(b); return nil }); err != nil {
		return err
	}
	f.mu.Lock()
	f.store[id] = val
	f.mu.Unlock()
	return nil
}

func (f *Fake) Delete(_ context.Context, id credential.SecretID) error {
	failure, delay := f.snapshot()
	if failure != nil {
		return failure
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	f.mu.Lock()
	delete(f.store, id)
	f.mu.Unlock()
	return nil
}

// SetFailure causes every subsequent operation to return err. Pass nil to
// clear. Useful for simulating transient provider errors.
func (f *Fake) SetFailure(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failure = err
}

// SetDelay adds a per-operation sleep so callers can test timeout behaviour.
func (f *Fake) SetDelay(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delay = d
}

// snapshot copies the current failure and delay under the lock so the caller
// can test/apply them without holding the mutex across a sleep or I/O.
func (f *Fake) snapshot() (error, time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failure, f.delay
}
