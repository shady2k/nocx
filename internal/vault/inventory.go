package vault

import (
	"context"
	"fmt"
	"sort"

	"github.com/shady2k/nocx/internal/credential"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// InventoryEntry is one item in the vault.inventory response. The secret owns
// its name (ADR-0016): Name comes from the vault's catalogue record, falling
// back to the derived label where an owner exists and to the kind otherwise —
// never blank, never the SecretID.
type InventoryEntry struct {
	ID        string `json:"id"`        // renderer-addressable row handle; never a SecretID
	Name      string `json:"name"`      // the secret's display name, owned by the vault
	Kind      string `json:"kind"`      // "password" | "key-passphrase" | ...
	Provider  string `json:"provider"`  // provider tag from the secret reference
	OwnerID   string `json:"ownerId"`   // credential ID that owns this secret, "" when unowned
	UsedBy    int    `json:"usedBy"`    // how many profiles reference the owning credential
	Reachable bool   `json:"reachable"` // whether the provider reports Status().Ready
}

// CredentialInventory contains the metadata needed to derive inventory entries
// for one credential. This is a plain-data projection — no profile package types.
type CredentialInventory struct {
	ID                  string
	Username            string
	AuthMode            string // "password", "publicKey", "agent", etc.
	SecretID            string
	PassphraseSecretID  string
	KeyMaterialSecretID string
	KeyFingerprint      string
	UsageCount          int
	// For single-use passwords, the effective host and port of the sole profile.
	SingleHost string
	SinglePort int
}

// secretRef is an internal representation of a unique secret reference found
// during traversal of a credential's record-level fields.
type secretRef struct {
	ref            credential.SecretID
	kind           string // "password" | "key-passphrase"
	keyFingerprint string // non-empty only for "key-passphrase"
}

// collectRefs gathers unique secret references from a credential's
// record-level fields. Unconditional collection of all three followed by
// deduplication ensures no secret is missed.
func collectRefs(cred CredentialInventory) []secretRef {
	seen := make(map[credential.SecretID]bool)
	var refs []secretRef

	if cred.SecretID != "" {
		id := credential.SecretID(cred.SecretID)
		if !seen[id] {
			seen[id] = true
			refs = append(refs, secretRef{ref: id, kind: "password"})
		}
	}
	if cred.PassphraseSecretID != "" {
		id := credential.SecretID(cred.PassphraseSecretID)
		if !seen[id] {
			seen[id] = true
			refs = append(refs, secretRef{
				ref:            id,
				kind:           "key-passphrase",
				keyFingerprint: cred.KeyFingerprint,
			})
		}
	}
	if cred.KeyMaterialSecretID != "" {
		id := credential.SecretID(cred.KeyMaterialSecretID)
		if !seen[id] {
			seen[id] = true
			refs = append(refs, secretRef{ref: id, kind: "private-key"})
		}
	}

	return refs
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// ProviderOf extracts the provider tag from a secret reference. This is the
// only public API for reference parsing — no consumer branches on the prefix
// (AD-8, spec §4.1).
func ProviderOf(id credential.SecretID) (ProviderID, error) {
	return parseID(id)
}

// ProviderStatus returns the provider tag and reachability for a secret
// reference. When the provider tag is not registered, ready is false and err
// is nil — the caller treats the entry as unreachable rather than failing
// the whole call.
func (v *Vault) ProviderStatus(ctx context.Context, id credential.SecretID) (provider ProviderID, ready bool, reason Reason, err error) {
	p, err := parseID(id)
	if err != nil {
		return "", false, "", fmt.Errorf("provider status: %w", err)
	}
	prov, ok := v.reg.Get(p)
	if !ok {
		return p, false, ReasonUnknownProvider, nil
	}
	status := prov.Status(ctx)
	return p, status.Ready, status.Reason, nil
}

// BuildInventory assembles the full inventory. It traverses the credential
// metadata for unique secret references, reads each secret's name from the
// vault's own catalogue record (ADR-0016) instead of deriving it, and also
// enumerates records no credential references — the unowned secrets the ADR
// exists to make possible. Reference parsing stays private to this package.
//
// A secret whose name did not land falls back to the derived label where an
// owner exists, and to the kind otherwise. It never renders blank, and never
// renders the SecretID.
//
// An unregistered provider tag does not fail the call: the entry reports
// reachable=false and the caller continues.
//
// Returns ErrVaultSealed when the vault is sealed. Returns
// ErrVaultUninitialized when the vault has not been set up.
func (v *Vault) BuildInventory(ctx context.Context, inputs []CredentialInventory) ([]InventoryEntry, error) {
	v.mu.Lock()
	state := v.stateLocked()
	records := append([]SecretRecord(nil), v.doc.Secrets...)
	v.mu.Unlock()

	switch state {
	case StateUninitialized:
		return nil, ErrVaultUninitialized
	case StateSealed:
		return nil, ErrVaultSealed
	}

	// Never a nil slice: it marshals to `"entries": null` and the renderer's
	// field is typed as an array, so the Secrets page died on `.length` with
	// an empty vault. usage.go:54-57 documents this exact trap — "a Go test
	// asserting len == 0 passes either way, which is exactly how the wrong
	// wire format stays green" — and this is it happening again.
	entries := make([]InventoryEntry, 0, len(inputs)+len(records))
	referenced := make(map[credential.SecretID]bool, len(inputs))

	for _, cred := range inputs {
		refs := collectRefs(cred)

		for _, sr := range refs {
			referenced[sr.ref] = true

			providerID, err := parseID(sr.ref)
			if err != nil {
				// Malformed reference — skip this entry, don't fail the whole call.
				continue
			}

			// Check reachability
			prov, provOK := v.reg.Get(providerID)
			reachable := false
			if provOK {
				status := prov.Status(ctx)
				reachable = status.Ready
			}

			kind := sr.kind
			name := ""
			if rec, ok := recordFor(records, sr.ref); ok {
				kind = rec.Kind
				name = rec.Name
			}
			if name == "" {
				// The name did not land (crash gap, or a pre-ADR secret):
				// fall back to the derived label where an owner exists. The
				// RECORD's kind is the authority on what the secret is
				// (ADR-0016), so the label derives from it — a passphrase
				// must not render as "SSH password for …" just because the
				// input slot was the password field.
				srForLabel := sr
				srForLabel.kind = kind
				name = deriveLabel(srForLabel, cred)
			}
			if name == "" {
				name = kindLabel(kind)
			}

			entries = append(entries, InventoryEntry{
				ID:        rowID(sr.ref),
				Name:      name,
				Kind:      kind,
				Provider:  string(providerID),
				OwnerID:   cred.ID,
				UsedBy:    cred.UsageCount,
				Reachable: reachable,
			})
		}
	}

	// Unowned records: the vault holds a secret no credential references.
	// The record is the only source — nothing derived it, and a record whose
	// name did not land renders as the kind.
	for _, rec := range records {
		if referenced[rec.ID] {
			continue
		}
		providerID, err := parseID(rec.ID)
		if err != nil {
			continue
		}
		prov, provOK := v.reg.Get(providerID)
		reachable := false
		if provOK {
			status := prov.Status(ctx)
			reachable = status.Ready
		}
		name := rec.Name
		if name == "" {
			name = kindLabel(rec.Kind)
		}

		entries = append(entries, InventoryEntry{
			ID:        rowID(rec.ID),
			Name:      name,
			Kind:      rec.Kind,
			Provider:  string(providerID),
			OwnerID:   "",
			UsedBy:    0,
			Reachable: reachable,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].OwnerID < entries[j].OwnerID
	})

	return entries, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// recordFor finds the catalogue record for id in a snapshot of doc.Secrets.
func recordFor(records []SecretRecord, id credential.SecretID) (SecretRecord, bool) {
	for _, rec := range records {
		if rec.ID == id {
			return rec, true
		}
	}
	return SecretRecord{}, false
}

// deriveLabel produces the fallback label for a secret entry whose name did
// not land, from the owner that references it.
//
// Rules:
//   - password used by one profile → "SSH password for {user}@{host}:{port}"
//   - password used by several    → "SSH password for {user}"
//   - key passphrase              → "Passphrase for key SHA256:{first 8 of fingerprint}…"
//   - private key                 → "Private key for {user}@{host}:{port}" (one
//     profile) or "Private key for {user}" (several)
func deriveLabel(sr secretRef, cred CredentialInventory) string {
	switch sr.kind {
	case "password":
		if cred.UsageCount == 1 && cred.SingleHost != "" {
			return fmt.Sprintf("SSH password for %s@%s:%d", cred.Username, cred.SingleHost, cred.SinglePort)
		}
		return fmt.Sprintf("SSH password for %s", cred.Username)

	case "key-passphrase":
		fp := sr.keyFingerprint
		if len(fp) > 8 {
			fp = fp[:8]
		}
		return fmt.Sprintf("Passphrase for key SHA256:%s…", fp)

	case "private-key":
		if cred.UsageCount == 1 && cred.SingleHost != "" {
			return fmt.Sprintf("Private key for %s@%s:%d", cred.Username, cred.SingleHost, cred.SinglePort)
		}
		return fmt.Sprintf("Private key for %s", cred.Username)

	default:
		return "Unknown secret"
	}
}
