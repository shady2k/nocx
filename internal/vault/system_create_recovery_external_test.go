package vault_test

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/vault"
)

func TestSystemCreateRecoveryBeginsOnlyAfterRestart(t *testing.T) {
	for _, state := range []string{"fresh", "target-attached", "metadata-committed"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			v, docs := setupDeletionVault(t)
			id, createErr := v.CreateSystemSecret(ctx, credential.NewSecret("authorization-in-flight"))
			if createErr != nil {
				t.Fatal(createErr)
			}
			if state != "fresh" {
				if err := v.AttachTarget(ctx, id, "mcp:server"); err != nil {
					t.Fatal(err)
				}
			}
			if state == "metadata-committed" {
				if err := v.CommitMetadata(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			// An unrelated configuration mutation only enumerates candidates.
			// Repeated unlocking must not turn a live, not-yet-attached token
			// into something that unrelated reconciliation can collect.
			for range 2 {
				candidates, candidatesErr := v.PendingDeleteCandidates(ctx)
				if candidatesErr != nil || len(candidates) != 0 {
					t.Fatalf("live create exposed to cleanup: %v, %v", candidates, candidatesErr)
				}
				v.Seal()
				unsealDeletionVault(t, v)
				if _, err := v.Get(ctx, id); err != nil {
					t.Fatalf("unlock collected live authorization: %v", err)
				}
			}
			// Only a new Vault over the recovered document ends that live
			// interval. Repeated startup/unseal must retain conditional intent,
			// not silently clear it or assume that a target proves a reference.
			for range 2 {
				v.Close()
				v = openDeletionVault(t, docs)
				candidates, candidatesErr := v.PendingDeleteCandidates(ctx)
				if candidatesErr != nil {
					t.Fatal(candidatesErr)
				}
				if state == "metadata-committed" {
					if len(candidates) != 0 {
						t.Fatalf("committed session became an orphan candidate: %v", candidates)
					}
				} else {
					assertDeleteCandidate(t, v, docs, id)
				}
				unsealDeletionVault(t, v)
				if _, err := v.Get(ctx, id); err != nil {
					t.Fatalf("startup deleted material without a reference decision: %v", err)
				}
				assertDeletionHidden(t, v, id)
			}
			if state != "metadata-committed" {
				// This test's metadata owner committed no reference. Promotion
				// is explicit; the runtime regression exercises the aggregate
				// reference check for both rejected and referenced sessions.
				if err := v.ScheduleDelete(ctx, id); err != nil {
					t.Fatal(err)
				}
				assertDeletionComplete(t, v, docs, id)
			}
		})
	}
}

func TestReconcileRecoveredSystemCreateRetainsConditionalEvidence(t *testing.T) {
	ctx := context.Background()
	reg, registryErr := vault.NewRegistry()
	if registryErr != nil {
		t.Fatal(registryErr)
	}
	const id credential.SecretID = "sec:v1:file:0123456789abcdef0123456789abcdef"
	for _, phase := range []vault.Phase{vault.PhasePrepared, vault.PhaseSecretWritten} {
		for _, target := range []string{"", "mcp:server"} {
			for _, recorded := range []bool{false, true} {
				entry := vault.JournalEntry{
					Op:         "create",
					NewID:      id,
					Target:     target,
					Phase:      phase,
					Kind:       vault.KindPassword,
					SystemOnly: true,
				}
				doc := vault.Document{Journal: []vault.JournalEntry{entry}}
				if recorded {
					doc.Secrets = []vault.SecretRecord{{ID: id, Kind: vault.KindPassword, SystemOnly: true}}
				}
				want := entry
				want.Op, want.Phase = "delete-candidate", vault.PhasePrepared
				for range 2 {
					if blocked := vault.Reconcile(ctx, &doc, reg); len(blocked) != 0 {
						t.Fatalf("conditional conversion needed provider access: %+v", blocked)
					}
					if len(doc.Journal) != 1 || doc.Journal[0] != want {
						t.Fatalf("phase=%s target=%q recorded=%v lost recovery evidence: %+v", phase, target, recorded, doc.Journal)
					}
					if (len(doc.Secrets) != 0) != recorded {
						t.Fatal("conditional conversion changed catalogue ownership")
					}
				}
			}
		}
	}
}
