package registry

import (
	"fmt"

	"github.com/shady2k/nocx/internal/session"
)

// Domain error markers for the binding registry, moved here with the registry
// when internal/git stopped importing internal/session (plan Task 3, D18).
// Transport switches on these to surface the right user-facing state; each
// wraps a distinguishable type the UI layer can map to an action.

// ErrUnknownBinding — Acquire or Close named a binding id that does not exist
// or is already closed. A binding id is not a bearer token; it is also
// unguessable (minted from crypto/rand), so reaching this error through
// guessing is not possible.
type ErrUnknownBinding struct {
	ID string
}

func (e *ErrUnknownBinding) Error() string {
	return fmt.Sprintf("git: unknown binding %q", e.ID)
}

// ErrNotOwned — the caller does not Own the binding's session (D15). The
// binding exists; the caller may not use it. This is the authorisation check
// that lives inside Acquire so no handler can forget it.
type ErrNotOwned struct {
	ID        string
	SessionID session.ID
}

func (e *ErrNotOwned) Error() string {
	return fmt.Sprintf("git: binding %q belongs to session %q, which the caller does not own", e.ID, e.SessionID)
}

// ErrHandleReleased — a method was called on a Handle after its release func
// ran. The handle is valid from Acquire until release and invalid after;
// this error is the second end of that interval.
type ErrHandleReleased struct{}

func (e *ErrHandleReleased) Error() string { return "git: handle released" }
