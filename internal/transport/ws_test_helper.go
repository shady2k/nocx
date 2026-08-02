package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/shady2k/nocx/internal/credential"
)

// memSecretStore is an in-memory credential.SecretStore for tests.
// It implements the new interface (Create/Get/Delete/Exists with context).
type memSecretStore struct {
	mu sync.Mutex
	m  map[credential.SecretID][]byte
}

// newTestStore returns a fresh in-memory secret store.
func newTestStore() *memSecretStore {
	return &memSecretStore{m: make(map[credential.SecretID][]byte)}
}

func (s *memSecretStore) Create(_ context.Context, value credential.Secret) (credential.SecretID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var idB [32]byte
	if _, err := rand.Read(idB[:]); err != nil {
		return "", err
	}
	id := credential.SecretID(hex.EncodeToString(idB[:]))
	buf := []byte(nil)
	if err := value.Use(func(b []byte) error {
		buf = append(buf, b...)
		return nil
	}); err != nil {
		return "", err
	}
	s.m[id] = buf
	return id, nil
}

func (s *memSecretStore) Get(_ context.Context, id credential.SecretID) (credential.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	buf, ok := s.m[id]
	if !ok {
		return credential.Secret{}, nil
	}
	return credential.NewSecretBytes(buf), nil
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
