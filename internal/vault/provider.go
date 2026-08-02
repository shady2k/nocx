package vault

import (
	"context"

	"github.com/shady2k/nocx/internal/credential"
)

// Provider stores and fetches secret values. It knows nothing about sealing,
// routing or policy — the Vault refuses before it ever delegates (spec §4.5).
//
// ctx bounds how long the CALLER waits. It does NOT cancel the effect: neither
// /usr/bin/security nor an in-flight D-Bus call can be aborted, so a Put that
// times out may still land. That is why the journal is written before the call
// (spec §4.2, §7.2).
type Provider interface {
	ID() ProviderID
	Status(ctx context.Context) Status
	Get(ctx context.Context, id credential.SecretID) (credential.Secret, error)
}

// WritableProvider is the write capability. It is discovered by type assertion,
// never by inspecting a tag or a mode string (AD-8).
type WritableProvider interface {
	Provider
	Put(ctx context.Context, id credential.SecretID, s credential.Secret) error
	Delete(ctx context.Context, id credential.SecretID) error
}

// Reason is a machine-readable discriminator that a Provider.Status()
// returns when Ready is false. Two states plus a reason beat a longer
// enum of states nothing in CI can exercise (spec §4.4): the UI needs to
// distinguish "start a Secret Service" from "unlock the login keychain"
// from "this build has no such provider", and those are reasons, not
// states.
//
// ReasonUnknownProvider is intentionally absent from this type — it is a
// reference routing error, not a health status any registered provider
// can report.
type Reason string

const (
	ReasonNoService           Reason = "no-service"
	ReasonLocked              Reason = "locked"
	ReasonDenied              Reason = "denied"
	ReasonTimeout             Reason = "timeout"
	ReasonUnsupportedPlatform Reason = "unsupported-platform"
)

// Status is what a provider reports about itself.
type Status struct {
	Ready  bool
	Reason Reason // set only when Ready is false
}
