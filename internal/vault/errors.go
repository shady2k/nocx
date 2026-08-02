package vault

import (
	"errors"
	"fmt"
)

// The five typed errors. Each maps to exactly one UI action; a longer list
// described a runtime external provider this design does not have (spec §6).
var (
	ErrVaultUninitialized = errors.New("vault is not initialized")
	ErrVaultSealed        = errors.New("vault is sealed")

	// ErrVaultGenerationChanged is returned when a write completed against a
	// vault whose generation advanced while the provider call was in flight —
	// the result is discarded because it may belong to a vault state that no
	// longer exists. It is DELIBERATELY not ErrVaultSealed: the two used to
	// share that error, so a generation change told the UI to raise Unlock,
	// and unlocking cannot fix a generation change. The user unlocked, retried,
	// and was asked to unlock again, forever (nocx-25k9.20). Retrying is the
	// correct response to this one; unlocking is not.
	ErrVaultGenerationChanged = errors.New("vault changed while the write was in flight; retry")
	ErrProviderUnavailable    = errors.New("storage provider unavailable")
	ErrSecretNotFound         = errors.New("secret not found")
	ErrUnsealFailed           = errors.New("unseal failed")
)

// ProviderError carries the reason discriminator alongside the sentinel.
type ProviderError struct {
	Provider ProviderID
	Reason   Reason
	Err      error
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("provider %s unavailable (%s): %v", e.Provider, e.Reason, e.Err)
}
func (e *ProviderError) Unwrap() error { return ErrProviderUnavailable }

func unavailable(p ProviderID, r Reason, cause error) error {
	return &ProviderError{Provider: p, Reason: r, Err: cause}
}
