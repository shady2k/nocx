// Package vaultreset returns a vault to its uninitialized state, so a user who
// has forgotten the passphrase and has no recovery code can start again.
//
// # Why this is its own package
//
// A reset spans two stores that must not learn about each other. The vault
// owns key material and provider storage; the profiles own the references
// that point at it. Putting the operation on *vault.Vault would make the
// vault package the owner of profiles.json, and putting it on the profile
// store would make the profile store understand provider namespaces.
// The order between them is the whole of the operation, so the order gets a
// module.
//
// # There is no journal, and none is needed
//
// The sequence is chosen so that every interruption leaves a state the same
// button repairs:
//
//  1. Clear the references. ADR-0011 §4 already settled this direction —
//     "metadata-first with a retriable secret deletion after: a brief
//     unreachable orphan is safer than metadata pointing at a secret that is
//     gone". A crash here leaves secrets nothing points at, which is the
//     preferred end of that trade.
//  2. Purge the vault: every provider's material, then the document.
//
// Each step is idempotent, so re-running is the recovery. A durable record
// with phases and a resume-on-startup path was considered and rejected: it
// exists to support deferred background cleanup, and there is none here.
//
// # The keychain may simply not be there
//
// No Secret Service running is an ordinary Linux state, not a fault. Preview
// reports it BEFORE the user confirms, so the choice is made knowing what will
// be left behind, rather than discovered half-way through. Execute proceeds
// regardless and reports the residue: refusing would leave a locked-out user
// with no way back, which is the situation this exists to end.
package vaultreset

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/vault"
)

// VaultPurger is the vault seen from here: something that can describe itself
// and destroy itself. Deliberately narrow — this package has no business
// unsealing, reading or writing secrets.
type VaultPurger interface {
	Snapshot(ctx context.Context) vault.Snapshot
	Purge(ctx context.Context) ([]vault.PurgeFailure, error)
}

// SecretReferenceStore is the config store seen from here: the two bulk
// operations over secret references and nothing else. The store holds
// profiles, groups and AI endpoints (ADR-0030); the endpoint references
// count and clear alongside the profile ones (ADR-0031).
type SecretReferenceStore interface {
	CountSecretReferences() (profile.SecretReferenceImpact, error)
	ClearAllSecretReferences() (profile.SecretReferenceImpact, error)
}

// Impact is what a reset costs, in the quantities that answer different
// questions: what is destroyed, and what behaves differently afterwards.
// The counts are per record kind on purpose (ADR-0031) — "9 connections"
// and "2 endpoints" are different sentences because they are different
// questions.
type Impact struct {
	SecretCount   int
	ProfileCount  int
	EndpointCount int
}

func impactFrom(i profile.SecretReferenceImpact) Impact {
	return Impact{
		SecretCount:   i.SecretCount,
		ProfileCount:  i.ProfileCount,
		EndpointCount: i.EndpointCount,
	}
}

// Preview is what the confirmation dialog is built from.
type Preview struct {
	Impact Impact
	// SystemKeychainReachable is false when the OS keychain is not answering.
	// The user is told before confirming, because it decides whether anything
	// stored there can be removed at all.
	SystemKeychainReachable bool
	// VaultInitialized is false when there is nothing to reset. The action is
	// offered anyway — a vault that failed half-way through a previous reset
	// looks uninitialized and still has references to clear.
	VaultInitialized bool
}

// Residue is material a reset could not remove. Its whole purpose is to stop
// the interface saying "everything was deleted" when it was not.
type Residue struct {
	// Store is the provider id whose material remains.
	Store string
	// Reason is the provider's reason code where it has one, for the same
	// audience the diagnostics block is written for.
	Reason string
}

// Result is what actually happened, not what was asked for.
type Result struct {
	Impact Impact
	// Residue is empty when everything was removed. Never nil — the transport
	// contract requires an array, and null there has already cost this project
	// one defect (nocx-25k9.14).
	Residue []Residue
}

// Service performs the reset. One implementation; the interfaces above exist
// so it can be tested without a real vault or a real profile store.
type Service struct {
	vault  VaultPurger
	refs   SecretReferenceStore
	logger *slog.Logger
}

func New(v VaultPurger, refs SecretReferenceStore, logger *slog.Logger) *Service {
	return &Service{vault: v, refs: refs, logger: logger}
}

// Preview reports what a reset would cost and whether the keychain can be
// reached, changing nothing.
//
// The counts come from the profile records, not from the vault: the vault
// is sealed whenever a reset is wanted — that is why it is wanted — and it
// keeps no catalogue of what it holds in any case.
func (s *Service) Preview(ctx context.Context) (Preview, error) {
	impact, err := s.refs.CountSecretReferences()
	if err != nil {
		return Preview{}, fmt.Errorf("count secret references: %w", err)
	}

	snap := s.vault.Snapshot(ctx)
	return Preview{
		Impact:                  impactFrom(impact),
		SystemKeychainReachable: providerReady(snap, vault.ProviderSystem),
		VaultInitialized:        snap.State != vault.StateUninitialized,
	}, nil
}

func providerReady(snap vault.Snapshot, id vault.ProviderID) bool {
	for _, p := range snap.Providers {
		if p.ID == id {
			return p.Ready
		}
	}
	// Not registered at all. There is nothing there to be unreachable, so the
	// question does not arise and the answer must not read as a problem.
	return true
}

// Execute performs the reset and reports what was actually done.
//
// References first, then the vault — see the package doc for why that order is
// the one that makes a journal unnecessary. A failure to clear the references
// aborts before anything is destroyed; a failure to purge a provider does not,
// because the alternative is leaving the user locked out.
func (s *Service) Execute(ctx context.Context) (Result, error) {
	impact, err := s.refs.ClearAllSecretReferences()
	if err != nil {
		// Nothing has been destroyed. Abort while that is still true.
		return Result{}, fmt.Errorf("clear secret references: %w", err)
	}

	failures, purgeErr := s.vault.Purge(ctx)

	residue := make([]Residue, 0, len(failures))
	for _, f := range failures {
		residue = append(residue, Residue{
			Store:  string(f.Provider),
			Reason: reasonOf(f.Err),
		})
	}

	if purgeErr != nil {
		s.logger.Warn("vault reset finished with residue",
			"stores", len(residue), "error", purgeErr)
	} else {
		s.logger.Info("vault reset complete",
			"secrets", impact.SecretCount, "profiles", impact.ProfileCount)
	}

	// Not an error. The vault IS reset — the document is gone either way — and
	// returning an error here would make the renderer report a failed
	// operation for a state in which the user can set up protection again.
	// What remains travels as Residue so the interface can say so precisely.
	return Result{Impact: impactFrom(impact), Residue: residue}, nil
}

// reasonOf extracts a provider reason code where the error carries one, so the
// interface can say "not answering" rather than quoting a Go error.
func reasonOf(err error) string {
	var pe *vault.ProviderError
	if errors.As(err, &pe) && pe.Reason != "" {
		return string(pe.Reason)
	}
	return ""
}
