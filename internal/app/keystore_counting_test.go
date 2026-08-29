package app

import (
	"sync"

	"github.com/shady2k/nocx/internal/vault/system"
)

// countingKeyring answers nothing and counts everything. It is how "zero
// modal dialogs" is asserted: on macOS the modal is raised by the keyring
// WRITE itself, so a provider that makes no call cannot raise one, and the
// count is the only observable that survives being run on Linux.
type countingKeyring struct {
	mu sync.Mutex
	n  int
}

func (k *countingKeyring) note() { k.mu.Lock(); k.n++; k.mu.Unlock() }

func (k *countingKeyring) calls() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.n
}

func (k *countingKeyring) Set(_, _, _ string) error { k.note(); return system.ErrNoKeystore }
func (k *countingKeyring) Get(_, _ string) (string, error) {
	k.note()
	return "", system.ErrNoKeystore
}
func (k *countingKeyring) Delete(_, _ string) error { k.note(); return system.ErrNoKeystore }
func (k *countingKeyring) DeleteAll(_ string) error { k.note(); return system.ErrNoKeystore }

// newCountedNotInPlayProvider builds the provider decideKeystore builds for
// an out-of-play stance, with the keyring replaced by one that counts.
func newCountedNotInPlayProvider(k *countingKeyring) *system.Provider {
	return system.NotInPlay(system.WithKeyring(k))
}
