package vault

import (
	"context"
	"errors"
	"fmt"
)

// PurgeableProvider is a provider that can destroy everything it holds in one
// operation, without knowing which ids it holds.
//
// Bulk, not a loop, and that is the whole point. The OS keychain exposes no
// enumeration on any platform, so "remove everything nocx stored there" cannot
// be expressed as a walk over known ids — an entry whose reference was lost is
// undiscoverable, and the system provider stores plaintext, so leaving it
// behind leaves a readable password nothing can ever find again.
//
// A provider that does not implement this is skipped. Read-only providers hold
// nothing of ours to destroy.
type PurgeableProvider interface {
	PurgeAll(ctx context.Context) error
}

// PurgeFailure names a provider whose material could not be destroyed, so the
// caller can tell the user which store still holds secrets rather than
// reporting an anonymous failure.
type PurgeFailure struct {
	Provider ProviderID
	Err      error
}

// Purge destroys everything this vault holds and returns it to
// StateUninitialized: every provider's material, then the vault document
// itself, whose absence IS the uninitialized state.
//
// # It works while sealed, and that is the only case that matters
//
// A user resets because they cannot unlock. An implementation that needed the
// root key would refuse at exactly the moment it is wanted, so nothing here
// decrypts anything — the file provider deletes its blob without reading it,
// and the keychain entries are removed by service name.
//
// # A provider that fails does not stop the vault being cleared
//
// The keychain not answering is an ordinary Linux state, not a fault. If
// purging a provider fails, the failure is collected and returned, and the
// vault is still cleared: refusing would leave the user locked out with no way
// back, which is the situation the reset exists to end. The caller reports
// what was left behind — see the transport's reset result.
//
// # It is idempotent
//
// There is no journal for this operation and none is needed: re-running it is
// the recovery. Every step tolerates the work already being done, so a reset
// interrupted anywhere is repaired by pressing the button again.
//
// # It does not touch credential records
//
// Clearing the references that point at the destroyed secrets belongs to the
// owner of those records, above this package. Purge is deliberately only half
// of a reset (ADR-0011: metadata goes first, secrets after).
func (v *Vault) Purge(ctx context.Context) ([]PurgeFailure, error) {
	v.mu.Lock()
	// Wipe the root key and bump the generation before anything is destroyed,
	// so an in-flight operation holding an older generation cannot write a
	// secret into a vault that is being taken apart.
	v.gen++
	for i := range v.rootKey {
		v.rootKey[i] = 0
	}
	v.rootKey = nil
	// Snapshot the registry and release the lock before calling providers —
	// ADR-0011 §4: never call a provider while holding the document lock.
	providers := make([]Provider, len(v.reg.List()))
	copy(providers, v.reg.List())
	v.mu.Unlock()

	var failures []PurgeFailure
	var errs []error
	for _, p := range providers {
		purgeable, ok := p.(PurgeableProvider)
		if !ok {
			continue
		}
		if err := purgeable.PurgeAll(ctx); err != nil {
			// Named, because the user is told which store still holds
			// material and a bare "purge failed" cannot say that.
			failures = append(failures, PurgeFailure{Provider: p.ID(), Err: err})
			errs = append(errs, fmt.Errorf("provider %s: %w", p.ID(), err))
			v.logger.Warn("provider purge failed", "provider", p.ID(), "error", err)
		}
	}

	// The document goes last and goes regardless. It is what State reads, so
	// while it exists the vault is not reset — and a vault left initialized
	// with its providers emptied is the one outcome worse than either end.
	v.mu.Lock()
	v.doc = Document{}
	v.initializing = false
	err := v.store.Delete(vaultDocName)
	v.mu.Unlock()

	if err != nil {
		errs = append(errs, fmt.Errorf("delete vault document: %w", err))
	}

	v.logger.Info("vault purged", "providerFailures", len(failures))

	return failures, errors.Join(errs...)
}
