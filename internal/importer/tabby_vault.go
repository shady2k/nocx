package importer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

// Tabby vault format constants (v1).
//
// Verified against upstream Tabby source:
// https://github.com/Eugeny/tabby/blob/master/tabby-core/src/services/vault.service.ts
//   - DERIVE: PBKDF2-SHA512, 100k iterations, 32-byte key, 8-byte salt (raw lines 14-18, 57-64)
//   - ENCRYPT: AES-256-CBC, 16-byte IV (raw lines 17-19, 66-82)
//   - ENCODE: keySalt=hex (toString('hex'), l77), iv=hex (toString('hex'), l78)
//   - ENCODE: contents=base64 (toString('base64'), l76)
//   - MAC: none — unauthenticated envelope. No HMAC/GCM (confirmed l1-340)
//   - PLAINTEXT: JSON {config: any, secrets: [{type,key,value}]}
//
// nocx-25k9.9: verification by source audit, not fixture.
const (
	tabbyPBKDF2Iterations = 100_000
	tabbyKeyLength        = 32 // AES-256
	tabbySaltLen          = 8
	tabbyIVLen            = 16 // AES-CBC block size

	tabbyMaxCiphertextLen  = 1 << 20 // 1 MB
	tabbyMaxPlaintextLen   = 1 << 20 // 1 MB
	tabbyMaxSecrets        = 16384
	tabbyMaxSecretKeyLen   = 4096 // raw json bytes — includes string quotes or object braces
	tabbyMaxSecretTypeLen  = 64
	tabbyMaxSecretValueLen = 1 << 18 // 256 KB
)

// ErrDecryptFailed is returned for every decryption or validation failure,
// making wrong-passphrase indistinguishable from a corrupted file.
var ErrDecryptFailed = errors.New("tabby vault decryption failed")

// TabbySecret is a single secret from the decrypted Tabby vault.
type TabbySecret struct {
	Type  string          `json:"type"`
	Key   json.RawMessage `json:"key"`
	Value json.RawMessage `json:"value"`
}

// TabbyVaultContents is the strictly validated, decrypted contents of a Tabby vault.
// Config is an arbitrary JSON value (open-ended). Secrets are required.
// Both are RawMessage so nil distinguishes "missing" from "explicitly null".
type TabbyVaultContents struct {
	Config  json.RawMessage `json:"config"`
	Secrets json.RawMessage `json:"secrets"`
	// decodedSecrets is populated during validation from Secrets RawMessage.
	decodedSecrets []TabbySecret
}

// DecodedSecrets returns the validated, decoded secret list. Valid only after
// DecryptTabbyVault succeeds.
func (c *TabbyVaultContents) DecodedSecrets() []TabbySecret { return c.decodedSecrets }

// DecryptTabbyVault decrypts a Tabby v1 vault section with the given passphrase.
//
// The passphrase is a parameter and is never stored, cached, or captured.
// Every failure path returns ErrDecryptFailed, so a wrong passphrase is
// indistinguishable from a corrupted or malformed vault.
// The decrypted JSON is strictly validated:
//   - only known fields accepted at every level (DisallowUnknownFields)
//   - no trailing data after the JSON value
//   - config and secrets are both required (but config may be null)
//   - every secret must have non-empty type and value
//   - key is an open-ended JSON value (string, object, etc.), bounded by raw byte length
//   - bounded lengths on every string and array
func DecryptTabbyVault(vault *TabbyVault, passphrase string) (*TabbyVaultContents, error) {
	if vault == nil || !vault.Encrypted || passphrase == "" {
		return nil, ErrDecryptFailed
	}
	if vault.Version != 1 {
		return nil, ErrDecryptFailed
	}

	salt, err := hex.DecodeString(vault.KeySalt)
	if err != nil || len(salt) != tabbySaltLen {
		return nil, ErrDecryptFailed
	}

	iv, err := hex.DecodeString(vault.IV)
	if err != nil || len(iv) != tabbyIVLen {
		return nil, ErrDecryptFailed
	}

	ciphertext, err := base64.StdEncoding.DecodeString(vault.Contents)
	if err != nil || len(ciphertext) > tabbyMaxCiphertextLen {
		return nil, ErrDecryptFailed
	}

	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, ErrDecryptFailed
	}

	key := pbkdf2.Key([]byte(passphrase), salt, tabbyPBKDF2Iterations, tabbyKeyLength, sha512.New)

	plaintext, err := decryptCBC(key, ciphertext, iv)
	if err != nil {
		return nil, ErrDecryptFailed
	}

	if len(plaintext) > tabbyMaxPlaintextLen {
		return nil, ErrDecryptFailed
	}

	contents, err := parseVaultContentsStrict(plaintext)
	if err != nil {
		return nil, ErrDecryptFailed
	}

	return contents, nil
}

// decryptCBC decrypts AES-256-CBC with PKCS#7 unpadding.
func decryptCBC(key, ciphertext, iv []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, ciphertext)

	return pkcs7Unpad(plaintext)
}

// pkcs7Unpad removes PKCS#7 padding. Returns an error on invalid padding so
// that a wrong key or tampered ciphertext produces the same failure mode.
func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("invalid padding")
	}

	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > aes.BlockSize {
		return nil, errors.New("invalid padding")
	}

	for _, b := range data[len(data)-padLen:] {
		if b != byte(padLen) {
			return nil, errors.New("invalid padding")
		}
	}

	return data[:len(data)-padLen], nil
}

// parseVaultContentsStrict decodes and validates vault JSON with strict
// field checking, no trailing tokens, and bounded lengths on every string.
func parseVaultContentsStrict(data []byte) (*TabbyVaultContents, error) {
	if !json.Valid(data) {
		return nil, fmt.Errorf("invalid JSON")
	}

	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()

	var contents TabbyVaultContents
	if err := dec.Decode(&contents); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	// Reject trailing data after the JSON value.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("trailing data after JSON value")
	}

	if err := validateContents(&contents); err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}

	return &contents, nil
}

// validateContents enforces field presence, bounded lengths, and structure.
func validateContents(c *TabbyVaultContents) error {
	// Config and secrets are both required fields. RawMessage encodes
	// field-absent as nil vs. json-null as the bytes "null".
	if c.Config == nil {
		return fmt.Errorf("missing required field: config")
	}
	if c.Secrets == nil {
		return fmt.Errorf("missing required field: secrets")
	}

	var secrets []TabbySecret
	dec := json.NewDecoder(strings.NewReader(string(c.Secrets)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&secrets); err != nil {
		return fmt.Errorf("secrets: %w", err)
	}
	// Reject trailing data after the secrets array.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("secrets: trailing data after array")
	}
	// Reject "secrets": null explicitly.
	if secrets == nil {
		return fmt.Errorf("secrets: null value not allowed")
	}

	if len(secrets) > tabbyMaxSecrets {
		return fmt.Errorf("too many secrets: %d", len(secrets))
	}

	for i, s := range secrets {
		if len(s.Type) == 0 || len(s.Type) > tabbyMaxSecretTypeLen {
			return fmt.Errorf("secret[%d]: invalid type length %d", i, len(s.Type))
		}
		if len(s.Key) == 0 || len(s.Key) > tabbyMaxSecretKeyLen {
			return fmt.Errorf("secret[%d]: invalid key length %d", i, len(s.Key))
		}
		if len(s.Value) == 0 || len(s.Value) > tabbyMaxSecretValueLen {
			return fmt.Errorf("secret[%d]: invalid value length %d", i, len(s.Value))
		}
		if s.Value[0] != '"' {
			return fmt.Errorf("secret[%d]: value is not a JSON string", i)
		}
	}

	c.decodedSecrets = secrets
	return nil
}
