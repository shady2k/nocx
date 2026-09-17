package ssh

// Fixtures that construct a RealClient, shared by test files on BOTH sides of
// the nocx_local_ssh split — which is why this file carries no build tag.
//
// A tagged test file is compiled by both passes (the tagged pass compiles the
// whole package, untagged files included), but an untagged one is not compiled
// by the tagged pass alone: it is compiled by the untagged pass, which is the
// COORDINATOR's build — the one where the dial half does not exist. So a
// fixture that only needs the constructor and the options belongs here, and one
// that needs the pool belongs in a tagged file. The two helpers below are the
// first kind: NewReal, a known_hosts path and a stub resolver are all the
// untagged half (ssh_real.go).

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
)

// memSecretStore is the in-memory credential.Resolver the auth-chain and
// prompt tests both stand in for a vault: Create mints an id, and Resolve
// answers the stored value through Secret.Use like a real one does. It is
// here, beside the client fixtures and for the same reason — the untagged
// password_prompt_test.go needs it, and the untagged pass is the build where
// a tagged file does not exist.

// newTestRealClient builds a RealClient with test-safe defaults.
func newTestRealClient(t *testing.T) *RealClient {
	t.Helper()
	dir := t.TempDir()
	rc, err := NewReal(
		log.NewSlogAdapter(nil), // nil handler → slog falls back
		WithKnownHostsFile(filepath.Join(dir, "known_hosts")),
		WithConfigResolver(NewStubConfigResolver()),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	return rc
}

// newTrustClient builds a RealClient whose known_hosts lives at the given
// path (which may not exist yet).
//
// It registers no cleanup, and the reason is the split rather than an
// oversight: RealClient.Close is the TAGGED half's (it closes the pool), so a
// fixture the untagged pass compiles cannot call it, and there is nothing here
// for it to close — the coordinator's client holds no connection. The tagged
// tests that dial through this client close what they dialed (probeOnce closes
// the one connection it opens), so nothing survives them either.
func newTrustClient(t *testing.T, khPath string) *RealClient {
	t.Helper()
	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	return client
}

type memSecretStore struct {
	mu   sync.Mutex
	m    map[credential.SecretID]credential.Secret
	next int
}

func (s *memSecretStore) Create(_ context.Context, value credential.Secret) (credential.SecretID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := credential.SecretID(fmt.Sprintf("mem-%d", s.next))
	if s.m == nil {
		s.m = make(map[credential.SecretID]credential.Secret)
	}
	s.m[id] = value
	return id, nil
}

func (s *memSecretStore) Get(_ context.Context, id credential.SecretID) (credential.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[id]
	if !ok {
		return credential.Secret{}, nil
	}
	return v, nil
}

func (s *memSecretStore) Resolve(ctx context.Context, id credential.SecretID, why credential.Stance) (credential.Secret, error) {
	return credential.NewResolver(s, nil, nil).Resolve(ctx, id, why)
}

func (s *memSecretStore) Delete(_ context.Context, id credential.SecretID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}

func (s *memSecretStore) Exists(_ context.Context, id credential.SecretID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[id]
	return ok, nil
}

func newTestStore() *memSecretStore {
	return &memSecretStore{}
}
