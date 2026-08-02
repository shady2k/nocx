// Package file provides the encrypted-blob vault provider. It stores secrets
// inside an AES-256-GCM-encrypted JSON document whose data key is wrapped by
// the root key the caller passes to Unlock (spec §5.3). This provider never
// derives a key from a passphrase — that is keys.go's job and it has already
// been done.
package file

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
)

// blob is the on-disk shape of the vault document.
type blob struct {
	Version        int    `json:"version"`
	VaultInstance  string `json:"vaultInstance"`
	WrappedDataKey string `json:"wrappedDataKey"`
	Contents       string `json:"contents"`
}

// Provider stores encrypted secrets in a DocumentStore. Secrets are
// held in memory while unlocked; the blob is re-encrypted and persisted
// on every mutation.
type Provider struct {
	docs          storage.DocumentStore
	name          string
	rootKey       []byte // nil when locked
	dataKey       []byte // nil when locked; set from wrappedKey on Unlock
	vaultInstance string
	wrappedKey    []byte // raw bytes of wrapped data key from the blob
	secrets       map[credential.SecretID][]byte

	mu sync.Mutex
}

// New returns a locked Provider backed by docs. name is the document
// name passed to DocumentStore.Read / Write.
func New(docs storage.DocumentStore, name string) *Provider {
	return &Provider{
		docs:    docs,
		name:    name,
		secrets: make(map[credential.SecretID][]byte),
	}
}

// ID returns the provider tag for routing.
func (p *Provider) ID() vault.ProviderID { return vault.ProviderFile }

// Status reports whether the document store is reachable. It probes the
// store directly — a store that answers is Ready regardless of whether the
// vault is sealed. Sealing is the Vault's concern (provider.go doc comment).
func (p *Provider) Status(_ context.Context) vault.Status {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Probe the document store for reachability. A store that answers
	// (even with "not found") is reachable.
	var b blob
	_, err := p.docs.Read(p.name, &b)
	if err != nil {
		return vault.Status{Ready: false, Reason: vault.ReasonDenied}
	}
	return vault.Status{Ready: true}
}

// SetInstanceID sets the vault instance identifier. Must be called
// before Unlock when initializing a new vault.
func (p *Provider) SetInstanceID(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.vaultInstance = id
}

// NewDataKey generates a fresh 32-byte data key, wraps it with the
// retained root key, and returns the raw (unwrapped) data key so the
// caller can use it. The wrapped key is stored internally and written
// to the blob on the next mutation.
//
// Must be called after Unlock.
func (p *Provider) NewDataKey() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.newDataKeyLocked()
}

// newDataKeyLocked is NewDataKey's body, callable by a holder of p.mu.
func (p *Provider) newDataKeyLocked() ([]byte, error) {
	if p.rootKey == nil {
		return nil, vault.ErrVaultSealed
	}

	dataKey := make([]byte, 32)
	if _, err := rand.Read(dataKey); err != nil {
		return nil, fmt.Errorf("generate data key: %w", err)
	}

	// Wrap the data key with the retained root key.
	aad := p.makeAAD()
	wrapped, err := encryptGCM(p.rootKey, dataKey, aad)
	if err != nil {
		return nil, fmt.Errorf("wrap data key: %w", err)
	}

	p.dataKey = dataKey
	p.wrappedKey = wrapped
	return bytes.Clone(dataKey), nil
}

// Unlock loads the persisted blob, unwraps the data key with rootKey,
// then decrypts the secret map. When no persisted document exists the
// provider starts empty and is ready to accept writes — the first
// mutation creates the blob lazily.
//
// rootKey is the vault root key, already unwrapped from its envelope.
// This provider never derives a key from a passphrase.
func (p *Provider) Unlock(rootKey []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var b blob
	found, err := p.docs.Read(p.name, &b)
	if err != nil {
		return fmt.Errorf("read vault document: %w", err)
	}

	if !found {
		// No document yet — start empty, keep rootKey for later wrapping.
		p.rootKey = bytes.Clone(rootKey)
		p.dataKey = nil
		p.wrappedKey = nil
		p.secrets = make(map[credential.SecretID][]byte)
		if p.vaultInstance == "" {
			id := make([]byte, 16)
			if _, rerr := rand.Read(id); rerr != nil {
				return fmt.Errorf("generate vault instance: %w", rerr)
			}
			p.vaultInstance = hex.EncodeToString(id)
		}
		return nil
	}

	// Use the stored instance; caller's SetInstanceID is for new vaults only.
	p.vaultInstance = b.VaultInstance

	if b.Version != blobVersion {
		return fmt.Errorf("%w: unknown blob version %d", vault.ErrUnsealFailed, b.Version)
	}

	aad := buildAAD(b.Version, b.VaultInstance)

	// Decrypt wrapped data key with root key.
	wrappedKey, err := hex.DecodeString(b.WrappedDataKey)
	if err != nil {
		return fmt.Errorf("%w: malformed wrappedDataKey", vault.ErrUnsealFailed)
	}
	dataKey, err := decryptGCM(rootKey, wrappedKey, aad)
	if err != nil {
		return fmt.Errorf("%w: unwrap data key", vault.ErrUnsealFailed)
	}

	// Decrypt contents with data key.
	contents, err := hex.DecodeString(b.Contents)
	if err != nil {
		return fmt.Errorf("%w: malformed contents", vault.ErrUnsealFailed)
	}
	plaintext, err := decryptGCM(dataKey, contents, aad)
	if err != nil {
		return fmt.Errorf("%w: decrypt contents", vault.ErrUnsealFailed)
	}

	// Unmarshal the secret map.
	var secrets map[credential.SecretID][]byte
	if len(plaintext) > 0 {
		if err := json.Unmarshal(plaintext, &secrets); err != nil {
			return fmt.Errorf("%w: unmarshal secrets: %v", vault.ErrUnsealFailed, err)
		}
	}
	if secrets == nil {
		secrets = make(map[credential.SecretID][]byte)
	}

	p.rootKey = bytes.Clone(rootKey)
	p.dataKey = dataKey
	p.wrappedKey = wrappedKey
	p.secrets = secrets
	return nil
}

// Lock wipes the root key and data key from memory, sealing the provider.
// The encrypted blob on disk is unchanged.
func (p *Provider) Lock() {
	p.mu.Lock()
	defer p.mu.Unlock()
	clear(p.rootKey)
	p.rootKey = nil
	clear(p.dataKey)
	p.dataKey = nil
	p.wrappedKey = nil
	p.secrets = make(map[credential.SecretID][]byte)
}

// Get returns the secret at id. Returns ErrVaultSealed when locked and
// ErrSecretNotFound when the id has no stored value.
func (p *Provider) Get(_ context.Context, id credential.SecretID) (credential.Secret, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.dataKey == nil {
		return credential.Secret{}, vault.ErrVaultSealed
	}

	secret, ok := p.secrets[id]
	if !ok {
		return credential.Secret{}, vault.ErrSecretNotFound
	}
	return credential.NewSecret(string(secret)), nil
}

// Put stores a secret at id. Returns ErrVaultSealed when locked.
func (p *Provider) Put(_ context.Context, id credential.SecretID, s credential.Secret) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// "The first mutation creates the blob lazily" is what Unlock's own doc
	// comment promises when no document exists yet, and this is where the
	// promise has to be kept. It was not: Put refused with ErrVaultSealed, so
	// on a brand-new vault every single write failed with "vault is sealed"
	// while the vault was wide open — and because the renderer turns that
	// reason into an Unlock dialog, the user unlocked, retried, and was asked
	// to unlock again, forever (nocx-25k9.20).
	//
	// No data key AND no root key is the genuinely sealed case. No data key
	// with a root key in hand is the first write to an empty store, and it
	// mints one.
	if p.dataKey == nil {
		if p.rootKey == nil {
			return vault.ErrVaultSealed
		}
		if _, err := p.newDataKeyLocked(); err != nil {
			return fmt.Errorf("mint data key for first write: %w", err)
		}
	}

	var plaintext []byte
	if err := s.Use(func(b []byte) error {
		plaintext = bytes.Clone(b)
		return nil
	}); err != nil {
		return fmt.Errorf("read secret: %w", err)
	}

	p.secrets[id] = plaintext
	return p.saveBlob()
}

// Delete removes the secret at id. Returns ErrVaultSealed when locked.
// Deleting an absent id is not an error.
func (p *Provider) Delete(_ context.Context, id credential.SecretID) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.dataKey == nil {
		return vault.ErrVaultSealed
	}

	delete(p.secrets, id)
	return p.saveBlob()
}

// makeAAD builds the AAD from the current blob version and vault instance.
// Caller must hold p.mu.
// PurgeAll destroys every secret this provider holds.
//
// Its material is one document, so purging is deleting it. The in-memory copy
// goes with it and the keys are wiped: a provider left unlocked would
// otherwise keep serving secrets that no longer exist on disk, and the first
// write after that would recreate the blob from a stale map.
//
// Deleting a blob that is not there succeeds — a reset interrupted after this
// step must be safe to re-run.
func (p *Provider) PurgeAll(_ context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.docs.Delete(p.name); err != nil {
		return err
	}
	p.secrets = make(map[credential.SecretID][]byte)
	p.wrappedKey = nil
	for i := range p.dataKey {
		p.dataKey[i] = 0
	}
	p.dataKey = nil
	for i := range p.rootKey {
		p.rootKey[i] = 0
	}
	p.rootKey = nil
	return nil
}

func (p *Provider) makeAAD() []byte {
	return buildAAD(blobVersion, p.vaultInstance)
}

// saveBlob re-encrypts the current secrets with the data key and writes
// the blob to the DocumentStore. Caller must hold p.mu.
func (p *Provider) saveBlob() error {
	if p.dataKey == nil {
		return vault.ErrVaultSealed
	}

	// Serialize secrets.
	var secretsJSON json.RawMessage
	if len(p.secrets) > 0 {
		var err error
		secretsJSON, err = json.Marshal(p.secrets)
		if err != nil {
			return fmt.Errorf("marshal secrets: %w", err)
		}
	}
	if secretsJSON == nil {
		secretsJSON = json.RawMessage("{}")
	}

	aad := p.makeAAD()

	// Encrypt contents with data key.
	contents, err := encryptGCM(p.dataKey, []byte(secretsJSON), aad)
	if err != nil {
		return fmt.Errorf("encrypt contents: %w", err)
	}

	// If we don't have a wrapped key yet, wrap the data key with the root key.
	if p.wrappedKey == nil {
		if p.rootKey == nil {
			return vault.ErrVaultSealed
		}
		wrapped, err := encryptGCM(p.rootKey, p.dataKey, aad)
		if err != nil {
			return fmt.Errorf("wrap data key: %w", err)
		}
		p.wrappedKey = wrapped
	}

	b := blob{
		Version:        blobVersion,
		VaultInstance:  p.vaultInstance,
		WrappedDataKey: hex.EncodeToString(p.wrappedKey),
		Contents:       hex.EncodeToString(contents),
	}

	if err := p.docs.Write(p.name, &b); err != nil {
		return fmt.Errorf("write vault document: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// AEAD helpers (carried over from internal/credential/vault.go)
// ---------------------------------------------------------------------------

const blobVersion = 1

// buildAAD constructs the GCM additional authenticated data that binds the
// format version and vault instance to the ciphertext.
//
//	AAD = version (4 bytes BE) || len(instance) (4 bytes BE) || instance
//
// This prevents two attacks that version-only AAD permits:
//   - transplanting the wrapped data key + contents between two vault
//     documents that happen to share a version;
//   - swapping the version field in the JSON without detection.
func buildAAD(version int, instance string) []byte {
	aad := make([]byte, 8+len(instance))
	aad[0] = byte(version >> 24)
	aad[1] = byte(version >> 16)
	aad[2] = byte(version >> 8)
	aad[3] = byte(version)
	aad[4] = byte(len(instance) >> 24)
	aad[5] = byte(len(instance) >> 16)
	aad[6] = byte(len(instance) >> 8)
	aad[7] = byte(len(instance))
	copy(aad[8:], instance)
	return aad
}

// encryptGCM encrypts plaintext with AES-256-GCM using key and aad.
// Returns nonce || ciphertext || tag.
func encryptGCM(key, plaintext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	// gcm.Seal appends to dst: nonce || ciphertext || tag
	return gcm.Seal(nonce, nonce, plaintext, aad), nil
}

// decryptGCM decrypts ciphertext (nonce || ciphertext || tag) with
// AES-256-GCM. Returns an error indistinguishable from tampering when
// authentication fails.
func decryptGCM(key, ciphertext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, ct := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return gcm.Open(nil, nonce, ct, aad)
}
