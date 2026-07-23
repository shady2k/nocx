// Package update carries the integrity gate for nocx's self-hosted updates.
//
// Because these builds are unsigned by any publisher (there is no Apple
// Developer ID — see docs/decisions/0003), no Gatekeeper check will ever
// validate them, so the signed manifest is the only thing standing between the
// updater and a tampered artefact (distribution design §6, D4). This file holds
// that gate: an ed25519 signature over the exact bytes of manifest.json,
// verified against a keyring of accepted public keys compiled into the binary.
//
// The signature and key framing match cmd/manifest-sign exactly — standard
// base64 of the raw 64-byte signature and 32-byte public key — so the release
// tooling that produces a signature and this code that checks it can never drift
// apart without a test noticing.
package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// ErrUnverified reports that a signature is well-formed but matches no key in
// the keyring. It is distinct from a decoding error so a caller can tell "this
// build does not trust that signer" apart from "that signature is garbage".
var ErrUnverified = errors.New("manifest signature verified against no key in the keyring")

// Keyring is the set of public keys a manifest signature may verify against.
// It is a slice, not a single key, so a client that has upgraded past a
// key-introducing release keeps working across a rotation (design §6).
type Keyring []ed25519.PublicKey

// productionKeys are the standard-base64 ed25519 public keys this build accepts,
// as `manifest-sign -keygen` prints them. It is empty until the first release
// signing key is generated (nocx-a75.3); an empty keyring fails closed, so every
// manifest is rejected until a real key is baked in — never the reverse.
var productionKeys = []string{}

// ProductionKeyring parses the compiled-in keys. A malformed entry (a typo in a
// pasted key) is a build-time mistake, surfaced by a test rather than in the
// field where it would reject every real manifest.
func ProductionKeyring() (Keyring, error) {
	return ParseKeyring(productionKeys...)
}

// ParseKeyring decodes standard-base64 ed25519 public keys into a Keyring,
// rejecting anything that is not valid base64 of exactly ed25519.PublicKeySize
// bytes.
func ParseKeyring(b64Keys ...string) (Keyring, error) {
	kr := make(Keyring, 0, len(b64Keys))
	for i, k := range b64Keys {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(k))
		if err != nil {
			return nil, fmt.Errorf("keyring entry %d: decode base64: %w", i, err)
		}
		if len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("keyring entry %d: %d bytes, want %d", i, len(raw), ed25519.PublicKeySize)
		}
		kr = append(kr, ed25519.PublicKey(raw))
	}
	return kr, nil
}

// VerifyManifest reports whether sig is a valid ed25519 signature over the exact
// bytes of manifest against at least one key in the keyring. sig is the
// standard-base64 detached signature as cmd/manifest-sign writes it, with any
// surrounding whitespace (the signer appends a trailing newline).
//
// It returns a decode error for a malformed or wrong-sized signature, and
// ErrUnverified when the signature is well-formed but no key accepts it.
// Verification is over raw bytes and happens before the JSON is ever parsed
// (design §6), so a manifest that fails here is never interpreted.
func VerifyManifest(manifest []byte, sig string, keyring Keyring) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sig))
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if len(raw) != ed25519.SignatureSize {
		return fmt.Errorf("signature is %d bytes, want %d", len(raw), ed25519.SignatureSize)
	}
	for _, pub := range keyring {
		if ed25519.Verify(pub, manifest, raw) {
			return nil
		}
	}
	return ErrUnverified
}
