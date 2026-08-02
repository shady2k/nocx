package vault

import (
	"fmt"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/storage"
)

const vaultDocName = "vault.json"

var vaultModule = storage.Module{
	Name:    "vault",
	Current: 2,
}

// SecretRecord is the vault's durable catalogue entry for one secret:
// SecretID → {name, kind} (ADR-0016). The vault is the single owner of both;
// surfaces read the name from here instead of deriving it from whatever
// happens to reference the secret.
//
// The name is metadata, not a secret: it is written where a reader who has
// the file can read it, and must never be derived from secret material.
// A record whose Name did not land is legal — the surfaces fall back to the
// derived label or the kind — but a secret with a connection using it and no
// record cannot exist: records are what make an unowned secret possible.
type SecretRecord struct {
	ID   credential.SecretID `json:"id"`
	Name string              `json:"name,omitempty"`
	Kind string              `json:"kind"`
}

// Document is the on-disk shape of the vault document. It holds key material,
// provider configuration, a recovery journal, and the catalogue of secrets
// the vault has allocated (SecretID → {name, kind}). The catalogue is the
// vault's answer to "what do we hold?" — the providers cannot say (the OS
// keychain cannot enumerate), so the layer above remembers (spec §4). Its
// fields are deliberately limited: no locators, no secret values, no routing.
type Document struct {
	Version         storage.SchemaVersion `json:"schemaVersion"`
	Instance        string                `json:"instance"`
	DefaultProvider ProviderID            `json:"defaultProvider"`
	Passphrase      *Envelope             `json:"passphrase,omitempty"`
	Recovery        *Envelope             `json:"recovery,omitempty"`
	HasOSKey        bool                  `json:"hasOSKey"`
	AutoSealMinutes int                   `json:"autoSealMinutes"`
	PreferredUnseal string                `json:"preferredUnseal"`
	Secrets         []SecretRecord        `json:"secrets,omitempty"`
	Journal         []JournalEntry        `json:"journal,omitempty"`
}

// loadDocument reads the vault document from the store. When the document does
// not exist it returns (Document{}, false, nil) — a missing vault means
// "uninitialized", not a failure.
func loadDocument(store storage.DocumentStore) (Document, bool, error) {
	var doc Document
	found, err := store.Read(vaultDocName, &doc)
	if err != nil {
		return Document{}, false, err
	}
	if !found {
		return Document{}, false, nil
	}
	// A document written by a newer build is refused rather than read
	// partially. Silently loading it would let this binary drop fields it does
	// not know about on the next save — and the fields most likely to be added
	// here are key envelopes, so the loss would be unrecoverable.
	if doc.Version > vaultModule.Current {
		return Document{}, false, fmt.Errorf("%w: vault document is version %d, this build understands %d",
			storage.ErrVersionTooNew, doc.Version, vaultModule.Current)
	}
	return doc, true, nil
}

// saveDocument writes the vault document to the store, stamping the module's
// current schema version. The caller never sets Version: it is the module's to
// own, and a document that failed to record it would be indistinguishable from
// one written before versioning existed.
func saveDocument(store storage.DocumentStore, doc Document) error {
	doc.Version = vaultModule.Current
	return store.Write(vaultDocName, &doc)
}
