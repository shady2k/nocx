package vault

import (
	"context"
	"fmt"

	"github.com/shady2k/nocx/internal/credential"
)

// Phase tracks how far a cross-store operation progressed before interruption.
// The journal is written first, then the provider call (§4.2), because a
// go-keyring call that times out may still complete later — and an unjournalled
// late write would be permanently undiscoverable.
type Phase string

const (
	// PhasePrepared means the identifier was journaled but the provider has not
	// been called yet.
	PhasePrepared Phase = "prepared"

	// PhaseSecretWritten means the new secret was written to the provider but
	// the caller has not yet attached a metadata target.
	PhaseSecretWritten Phase = "secret-written"

	// PhaseMetadataRepointed means the metadata owner has been atomically
	// repointed to the new identifier. The old secret is still in the provider
	// and should be deleted best-effort.
	PhaseMetadataRepointed Phase = "metadata-repointed"
)

// JournalEntry records one in-flight cross-store operation. Identifiers and
// routing only — never secret bytes (ADR-0011 §4).
type JournalEntry struct {
	Op     string              `json:"op"`
	OldID  credential.SecretID `json:"oldId,omitempty"`
	NewID  credential.SecretID `json:"newId"`
	Target string              `json:"target"`
	Phase  Phase               `json:"phase"`
	// Name and Kind carry the secret's catalogue record through the create
	// sequence (ADR-0016): the name joins the journal at PhasePrepared and
	// lands in the durable record at PhaseSecretWritten. They are metadata,
	// never secret bytes.
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// String returns a compact representation for logging. It emits identifiers
// and routing only — never secret bytes.
func (e JournalEntry) String() string {
	if e.Op == "" {
		return "<cleared>"
	}
	return fmt.Sprintf("%s old=%s new=%s target=%q phase=%s", e.Op, e.OldID, e.NewID, e.Target, e.Phase)
}

// Reconcile processes journal entries left from an interrupted operation.
// It is idempotent: the second run is a no-op.
//
// Entries in PhasePrepared or PhaseSecretWritten with an empty Target
// represent a new secret that was never referenced by metadata. The record
// (doc.Secrets, ADR-0016) decides what happened:
//
//   - no record — the create's durable half never landed; the orphan is
//     deleted and the entry cleared;
//   - record present — the create completed (value and record were written in
//     one journal step); the entry is cleared and the secret kept.
//
// Entries in PhaseMetadataRepointed represent a completed metadata repoint
// with a possibly-stale old secret. The new secret is verified accessible,
// then the old secret is deleted best-effort and its record, if any, removed.
//
// An entry whose provider is not in reg, or whose identifier is malformed,
// is retained (never cleared) and returned in the blocked slice. It is never
// re-routed to another provider (spec §6 invariant 5).
//
// doc is modified in place. The caller must save the document after Reconcile
// returns.
func Reconcile(ctx context.Context, doc *Document, reg *Registry) []JournalEntry {
	var blocked []JournalEntry

	for i := range doc.Journal {
		entry := &doc.Journal[i]
		if entry.Op == "" {
			continue // already cleared
		}
		// Validate Op is a known operation (defect 10).
		switch entry.Op {
		case "create", "delete", "rotate", "replace":
		default:
			blocked = append(blocked, *entry)
			continue
		}

		providerID, err := parseID(entry.NewID)
		if err != nil {
			blocked = append(blocked, *entry)
			continue
		}

		wp, ok := reg.Writable(providerID)
		if !ok {
			// Provider unknown or not writable — retain and report.
			// Never re-route to another provider (spec §6).
			blocked = append(blocked, *entry)
			continue
		}
		switch entry.Phase {
		case PhasePrepared, PhaseSecretWritten:
			if entry.Op == "replace" {
				// A replace writes a new value under an EXISTING id: whichever
				// half of the write landed (the put, the clear, or neither),
				// the id still names a valid secret — there is no orphan to
				// delete and no dangling reference to repair. The record's
				// presence is irrelevant (an unrecorded legacy reference is
				// just as live as a recorded one), so the entry is cleared.
				*entry = JournalEntry{}
				continue
			}
			if entry.Target == "" {
				if hasRecord(doc, entry.NewID) {
					// The create's durable half landed: value and record were
					// written together. Nothing downstream can have happened
					// (no metadata target), and the record proves the secret
					// exists, so the entry is simply cleared.
					*entry = JournalEntry{}
					continue
				}
				// Nothing downstream can have happened — the caller had not
				// yet attached a metadata target and no record landed.
				// Delete the orphan secret and clear the entry.
				if err := wp.Delete(ctx, entry.NewID); err != nil {
					// Transient provider failure — retain the entry so the
					// orphan is retried on the next startup.
					blocked = append(blocked, *entry)
					continue
				}
				*entry = JournalEntry{}
			} else {
				// Non-empty target but phase not yet repointed: metadata was
				// changed atomically but the journal was not updated before the
				// crash. Retain for investigation — do not assume forward or back
				// (defect 10).
				blocked = append(blocked, *entry)
			}

		case PhaseMetadataRepointed:
			// Verify the new secret is accessible.
			if _, err := wp.Get(ctx, entry.NewID); err != nil {
				blocked = append(blocked, *entry)
				continue
			}
			// Delete the old secret through its own provider — OldID and NewID
			// may route to different providers (cross-provider rotation).
			if entry.OldID != "" {
				oldProvID, err := parseID(entry.OldID)
				if err != nil {
					blocked = append(blocked, *entry)
					continue
				}
				oldWp, ok := reg.Writable(oldProvID)
				if !ok {
					blocked = append(blocked, *entry)
					continue
				}
				if err := oldWp.Delete(ctx, entry.OldID); err != nil {
					blocked = append(blocked, *entry)
					continue
				}
				dropRecord(doc, entry.OldID)
			}
			*entry = JournalEntry{}

		default:
			// Unknown phase — report (defect 10).
			blocked = append(blocked, *entry)
		}
	}

	return blocked
}

// hasRecord reports whether the document carries a catalogue record for id.
func hasRecord(doc *Document, id credential.SecretID) bool {
	for _, rec := range doc.Secrets {
		if rec.ID == id {
			return true
		}
	}
	return false
}

// dropRecord removes the catalogue record for id, if any.
func dropRecord(doc *Document, id credential.SecretID) {
	for i := range doc.Secrets {
		if doc.Secrets[i].ID == id {
			doc.Secrets = append(doc.Secrets[:i], doc.Secrets[i+1:]...)
			return
		}
	}
}
