package vault

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/shady2k/nocx/internal/credential"
)

// SecretMeta is the catalogue metadata a creator attaches to a secret at
// create time (ADR-0016): the display name and the kind. The vault persists
// both with the SecretID and is the single owner of them afterwards.
//
// Name may be empty — a nameless secret is legal and renders by fallback —
// but when present it must never be derived from secret material: the name is
// metadata, readable by anyone who can read the vault document.
type SecretMeta struct {
	Name string
	Kind string
}

// The secret-kind vocabulary, closed (registry spec §4.1). Only password,
// key-passphrase and private-key are created today, but the format carries
// the set from day one so a new kind does not degrade into "unknown"
// (spec §7).
const (
	KindPassword      = "password"
	KindKeyPassphrase = "key-passphrase"
	KindPrivateKey    = "private-key"
	KindPublicKey     = "public-key"
	KindOTPSeed       = "otp-seed"
)

// validateKind rejects a kind outside the vocabulary.
func validateKind(kind string) error {
	switch kind {
	case KindPassword, KindKeyPassphrase, KindPrivateKey, KindPublicKey, KindOTPSeed:
		return nil
	}
	return fmt.Errorf("unknown secret kind %q", kind)
}

// kindLabel is the user-facing fallback for a nameless secret with no owner
// to derive from: the kind carries the row (ADR-0016). Never blank, never
// the SecretID.
func kindLabel(kind string) string {
	switch kind {
	case KindPassword:
		return "Password"
	case KindKeyPassphrase:
		return "Key passphrase"
	case KindPrivateKey:
		return "Private key"
	case KindPublicKey:
		return "Public key"
	case KindOTPSeed:
		return "OTP seed"
	}
	return "Unknown secret"
}

// rowID derives the renderer-addressable handle for a secret row: an opaque,
// one-way derivative of the SecretID. It is not a secret reference — no
// provider tag, nothing to route, not invertible — so holding it is
// something the renderer is allowed to do, which is the line nocx-jb20.1
// draws for the reference itself. Rename and the Secrets page address rows
// by this handle; the backend resolves it back to the SecretID.
func rowID(id credential.SecretID) string {
	sum := sha256.Sum256([]byte(id))
	return "secrow:" + hex.EncodeToString(sum[:16])
}

// RowFor derives the renderer-addressable handle for a secret reference —
// the pure half of row resolution. The transport uses it to translate
// backend-owned references to row handles before a response crosses the
// wire (ADR-0017 §1); the inverse (row → reference) needs the vault and
// goes through ResolveRow.
func RowFor(id credential.SecretID) string {
	return rowID(id)
}
