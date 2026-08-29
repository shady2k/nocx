package vault

import (
	"errors"
	"fmt"
)

// The typed errors each map to a distinct UI action or refusal.
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
	ErrSecretNameLooksLikeRow = errors.New("secret name must not begin with secrow:")

	// ErrNoUnlockClient is what raising the unlock prompt gets when no client
	// is attached to show it. It is declared HERE, in the package that owns
	// the prompt, and internal/transport names the same value
	// ErrNoClientConnected: the vault has to tell "nobody is there to ask"
	// (suspend and wait for a client) from "the person said no" (fail now),
	// and one sentinel with two names is the only shape that does not put a
	// cycle between the two packages. Matching on the message instead would
	// be a second answer to the same question, drifting the first time either
	// side reworded itself.
	ErrNoUnlockClient = errors.New("no client connected to show unlock prompt")

	// ErrUnlockSuspended is the answer to an operation that waited out the
	// whole suspension window without a client ever attaching. It is the
	// "no hang" half of D9: a session that needs a secret while you are away
	// suspends rather than failing, but a suspension has an end, and this is
	// what the caller is told when it is reached.
	ErrUnlockSuspended = errors.New("no client attached to unlock the vault within the suspension window")
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
