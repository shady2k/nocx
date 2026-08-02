// Package vault owns the routing of secret references to storage providers,
// the seal lifecycle and the key material that unlocks them. It is the only
// implementation of credential.SecretStore the composition root wires.
package vault

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/credential"
)

// ProviderID names a storage provider. These tags are PERSISTED PROTOCOL:
// they appear inside secret references stored in profiles.json, so they can
// never be renamed when packages or implementations change. The tag names the
// store; the blob names its own format (spec §4.1).
type ProviderID string

const (
	ProviderSystem ProviderID = "system"
	ProviderFile   ProviderID = "file"
)

const (
	idPrefix      = "sec"
	idVersion     = "v1"
	idMaterialLen = 32 // hex characters == 16 random bytes
	maxProviderID = 32 // bytes; bounds the parser against a hostile record
)

var errMalformedID = errors.New("malformed secret reference")

// mintID produces a fresh reference bound to p. Only the Vault calls this:
// with routing encoded in the reference, a caller that could mint one would
// be choosing a provider, which is the Vault's policy to make (spec §4.2).
func mintID(p ProviderID) (credential.SecretID, error) {
	if err := validProviderTag(p); err != nil {
		return "", err
	}
	var b [idMaterialLen / 2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("mint secret reference: %w", err)
	}
	return credential.SecretID(strings.Join(
		[]string{idPrefix, idVersion, string(p), hex.EncodeToString(b[:])}, ":",
	)), nil
}

// parseID extracts the provider from a reference. It judges SYNTAX ONLY: an
// unregistered tag parses successfully, because a provider may be absent from
// this build, removed, or newer than this binary, and the record stays valid
// (spec §6 invariant 3). Availability is the registry's business.
//
// There is no defaulting on failure. A reference we cannot parse is an invalid
// record, never a reason to reach for the default provider.
func parseID(id credential.SecretID) (ProviderID, error) {
	parts := strings.Split(string(id), ":")
	if len(parts) != 4 {
		return "", fmt.Errorf("%w: want 4 components, got %d", errMalformedID, len(parts))
	}
	if parts[0] != idPrefix || parts[1] != idVersion {
		return "", fmt.Errorf("%w: bad prefix %q:%q", errMalformedID, parts[0], parts[1])
	}
	p := ProviderID(parts[2])
	if err := validProviderTag(p); err != nil {
		return "", err
	}
	if err := validMaterial(parts[3]); err != nil {
		return "", err
	}
	return p, nil
}

func validProviderTag(p ProviderID) error {
	switch {
	case p == "":
		return fmt.Errorf("%w: empty provider tag", errMalformedID)
	case len(p) > maxProviderID:
		return fmt.Errorf("%w: provider tag longer than %d bytes", errMalformedID, maxProviderID)
	case strings.ToLower(string(p)) != string(p):
		return fmt.Errorf("%w: provider tag must be lower case, got %q", errMalformedID, p)
	case strings.ContainsAny(string(p), ":"):
		return fmt.Errorf("%w: provider tag contains a separator", errMalformedID)
	}
	return nil
}

func validMaterial(s string) error {
	if len(s) != idMaterialLen {
		return fmt.Errorf("%w: want %d hex chars, got %d", errMalformedID, idMaterialLen, len(s))
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return fmt.Errorf("%w: non-lowercase-hex material", errMalformedID)
		}
	}
	return nil
}
