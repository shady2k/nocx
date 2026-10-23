package vault

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
)

var errDeletionOutcomeUncertain = errors.New("vault deletion outcome is unresolved")

// Delete removes a secret after its references have been removed. Unlike
// ScheduleDelete, this ordinary API requires an unsealed vault and reports
// provider failures. Both paths retain durable intent until deletion completes.
func (v *Vault) Delete(ctx context.Context, id credential.SecretID) error {
	pID, err := parseID(id)
	if err != nil {
		return err
	}
	v.mu.Lock()
	switch v.stateLocked() {
	case StateUninitialized:
		v.mu.Unlock()
		return ErrVaultUninitialized
	case StateSealed:
		v.mu.Unlock()
		return ErrVaultSealed
	}
	if _, ok := v.reg.Writable(pID); !ok {
		v.mu.Unlock()
		return unavailable(pID, ReasonUnknownProvider,
			fmt.Errorf("provider %q is not writable", pID))
	}
	gen := v.gen
	err = v.scheduleDeleteLocked(id)
	v.mu.Unlock()
	if err != nil {
		return err
	}
	return v.deleteScheduled(ctx, id, gen)
}

// PrepareDelete records a conditional cleanup candidate before the metadata
// owner removes references. It never touches provider material or the current
// catalogue record, even when unsealed. A failed save is reconciled by readback.
// Only the metadata owner may promote the candidate with ScheduleDelete, after
// checking every reference under its normal mutation admission.
func (v *Vault) PrepareDelete(ctx context.Context, id credential.SecretID) error {
	if _, err := parseID(id); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.stateLocked() == StateUninitialized {
		return ErrVaultUninitialized
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := v.resolveDeletionOutcomeLocked(); err != nil {
		return err
	}
	if hasPendingDelete(&v.doc, id) || hasDeleteCandidate(&v.doc, id) {
		return nil
	}
	rec, _ := recordFor(v.doc.Secrets, id)
	systemOnly := rec.SystemOnly
	for _, entry := range v.doc.Journal {
		if entry.NewID == id {
			systemOnly = systemOnly || entry.SystemOnly
		}
	}
	next := v.doc
	next.Journal = slices.Clone(next.Journal)
	next.Journal = append(next.Journal, JournalEntry{
		Op:         "delete-candidate",
		NewID:      id,
		Phase:      PhasePrepared,
		SystemOnly: systemOnly,
	})
	return v.saveDeletionDocumentLocked(next, id, "prepare delete journal save")
}

// PendingDeleteCandidates is a backend-only snapshot of conditional cleanup
// IDs, available while sealed. It supplies no authority to delete: the metadata
// owner must recheck references under admission before calling ScheduleDelete.
func (v *Vault) PendingDeleteCandidates(ctx context.Context) ([]credential.SecretID, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.stateLocked() == StateUninitialized {
		return nil, ErrVaultUninitialized
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := v.resolveDeletionOutcomeLocked(); err != nil {
		return nil, err
	}
	var ids []credential.SecretID
	for _, entry := range v.doc.Journal {
		if entry.Op == "delete-candidate" && !slices.Contains(ids, entry.NewID) {
			ids = append(ids, entry.NewID)
		}
	}
	return ids, nil
}

// ScheduleDelete durably retires backend-owned material after its metadata
// references have been removed. It never asks for unlock: while sealed it only
// writes identifiers/classification to the existing recovery journal. Success
// means durable intent, not necessarily immediate provider deletion. Startup
// and successful Unseal retry retained intents; calling again is idempotent.
// This backend-only API must not be exposed through user row operations.
func (v *Vault) ScheduleDelete(ctx context.Context, id credential.SecretID) error {
	if _, err := parseID(id); err != nil {
		return err
	}
	v.mu.Lock()
	if v.stateLocked() == StateUninitialized {
		v.mu.Unlock()
		return ErrVaultUninitialized
	}
	if err := ctx.Err(); err != nil {
		v.mu.Unlock()
		return err
	}
	err := v.scheduleDeleteLocked(id)
	unsealed, gen := v.rootKey != nil, v.gen
	v.mu.Unlock()
	if err != nil {
		return err
	}
	if unsealed {
		// The intent is already durable. A provider or final-save failure
		// leaves it available to the same recovery owner, not to the caller.
		if err := v.deleteScheduled(ctx, id, gen); err != nil {
			log.From(ctx).Warn("scheduled secret deletion retained", "secretID", id, "error", err)
		}
	}
	return nil
}

// scheduleDeleteLocked publishes saved or read-back metadata, fencing an
// unresolved outcome. Create entries retain ownership and classification until
// provider deletion succeeds.
func (v *Vault) scheduleDeleteLocked(id credential.SecretID) error {
	if err := v.resolveDeletionOutcomeLocked(); err != nil {
		return err
	}
	rec, _ := recordFor(v.doc.Secrets, id)
	systemOnly := rec.SystemOnly
	for _, entry := range v.doc.Journal {
		if entry.NewID != id {
			continue
		}
		if entry.Op == "delete" {
			return nil // previously persisted intent
		}
		systemOnly = systemOnly || entry.SystemOnly
	}
	next := deletionDocument(v.doc)
	// Promotion and candidate retirement are one durable write. A failed
	// promotion leaves the conditional candidate available to its owner.
	for i := range next.Journal {
		if next.Journal[i].Op == "delete-candidate" && next.Journal[i].NewID == id {
			next.Journal[i] = JournalEntry{}
		}
	}
	next.Journal = append(next.Journal, JournalEntry{
		Op:         "delete",
		NewID:      id,
		Phase:      PhasePrepared,
		SystemOnly: systemOnly,
	})
	dropRecord(&next, id)
	return v.saveDeletionDocumentLocked(next, id, "journal save")
}

// deleteScheduled owns the provider call and durable completion for both APIs
// and post-unseal recovery. Providers are never called while holding v.mu.
func (v *Vault) deleteScheduled(ctx context.Context, id credential.SecretID, gen uint64) error {
	t0 := time.Now()
	v.mu.Lock()
	if v.rootKey == nil || v.gen != gen {
		v.mu.Unlock()
		return ErrVaultSealed
	}
	if err := v.resolveDeletionOutcomeLocked(); err != nil {
		v.mu.Unlock()
		return err
	}
	if !hasPendingDelete(&v.doc, id) {
		v.mu.Unlock()
		return nil // another deletion already completed
	}
	pID, err := parseID(id)
	if err != nil {
		v.mu.Unlock()
		return err
	}
	prov, ok := v.reg.Writable(pID)
	v.mu.Unlock()
	if !ok {
		return unavailable(pID, ReasonUnknownProvider,
			fmt.Errorf("provider %q is not writable", pID))
	}
	delErr := prov.Delete(ctx, id)
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.gen != gen || v.rootKey == nil {
		return ErrVaultSealed
	}
	if delErr != nil {
		return delErr
	}
	next := deletionDocument(v.doc)
	completeDeletion(&next, id)
	if err := v.saveDeletionDocumentLocked(next, id, "save after delete"); err != nil {
		return err
	}
	log.From(ctx).Info("secret deleted", "secretID", id, "provider", pID, "duration", time.Since(t0))
	return nil
}

func (v *Vault) retryScheduledDeletes(ctx context.Context) {
	v.mu.Lock()
	gen := v.gen
	var ids []credential.SecretID
	for _, entry := range v.doc.Journal {
		if entry.Op == "delete" && !slices.Contains(ids, entry.NewID) {
			ids = append(ids, entry.NewID)
		}
	}
	v.mu.Unlock()
	for _, id := range ids {
		if err := v.deleteScheduled(ctx, id, gen); err != nil {
			log.From(ctx).Warn("scheduled secret deletion retained", "secretID", id, "error", err)
		}
	}
}

// Only these slices change during deletion; envelope/key metadata stays shared
// and untouched. Copying isolates the attempted transition for outcome readback.
func deletionDocument(doc Document) Document {
	doc.Secrets = slices.Clone(doc.Secrets)
	doc.Journal = slices.Clone(doc.Journal)
	return doc
}

func hasPendingDelete(doc *Document, id credential.SecretID) bool {
	for _, entry := range doc.Journal {
		if entry.Op == "delete" && entry.NewID == id {
			return true
		}
	}
	return false
}

func hasDeleteCandidate(doc *Document, id credential.SecretID) bool {
	for _, entry := range doc.Journal {
		if entry.Op == "delete-candidate" && entry.NewID == id {
			return true
		}
	}
	return false
}

func completeDeletion(doc *Document, id credential.SecretID) {
	dropRecord(doc, id)
	for i := range doc.Journal {
		if doc.Journal[i].NewID == id {
			doc.Journal[i] = JournalEntry{}
		}
	}
}

// saveDeletionDocumentLocked cannot treat Write errors as rollback: the file
// store may have renamed the new document before directory fsync failed. Only
// journal/catalogue state is reconciled; envelopes, root keys and lifecycle
// state remain owned by their existing operations.
func (v *Vault) saveDeletionDocumentLocked(next Document, id credential.SecretID, operation string) error {
	if err := saveDocument(v.store, next); err != nil {
		persisted, found, readErr := loadDocument(v.store)
		if readErr == nil && found {
			v.doc.Secrets, v.doc.Journal = persisted.Secrets, persisted.Journal
			v.uncertainDeletions = nil
		} else {
			// Keep the attempted metadata so unrelated successful document
			// writes cannot erase a possibly-committed deletion. This is NOT
			// evidence of durability: the fence forbids provider execution,
			// row reuse and idempotent success until readback resolves it.
			v.doc.Secrets, v.doc.Journal = next.Secrets, next.Journal
			if v.uncertainDeletions == nil {
				v.uncertainDeletions = make(map[credential.SecretID]struct{})
			}
			v.uncertainDeletions[id] = struct{}{}
		}
		return fmt.Errorf("%s: %w", operation, err)
	}
	v.doc.Secrets, v.doc.Journal = next.Secrets, next.Journal
	v.uncertainDeletions = nil
	return nil
}

func (v *Vault) resolveDeletionOutcomeLocked() error {
	if len(v.uncertainDeletions) == 0 {
		return nil
	}
	persisted, found, err := loadDocument(v.store)
	if err != nil {
		return fmt.Errorf("%w: %w", errDeletionOutcomeUncertain, err)
	}
	if !found {
		return errDeletionOutcomeUncertain
	}
	v.doc.Secrets, v.doc.Journal = persisted.Secrets, persisted.Journal
	v.uncertainDeletions = nil
	return nil
}
