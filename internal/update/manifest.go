package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/mod/semver"
)

// Manifest is the signed release manifest fetched from GitHub Releases
// (§6 of the distribution-and-updates design).
//
// The raw JSON bytes are verified against a detached ed25519 signature
// before this struct is populated (see [VerifyManifest]).
type Manifest struct {
	Version   string     `json:"version"`
	Released  string     `json:"released"`
	NotesURL  string     `json:"notesUrl"`
	Artifacts []Artifact `json:"artifacts"`
}

// Artifact describes one downloadable update payload.
type Artifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Format string `json:"format"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// VerifyManifest verifies the ed25519 detached signature over the raw
// manifest body and, if valid, returns the parsed [Manifest].
//
// Verification happens before JSON parsing so the parser never sees
// bytes that did not originate from a trusted signer. sigBase64 is
// the contents of manifest.json.sig (base64, standard encoding).
// keyring is the set of acceptable public keys; any one of them may
// have produced the signature.
//
// Errors are safe to log and surface — they never include key material.
func VerifyManifest(body []byte, sigBase64 string, keyring []ed25519.PublicKey) (*Manifest, error) {
	sig, err := base64.StdEncoding.DecodeString(sigBase64)
	if err != nil {
		return nil, fmt.Errorf("manifest signature: invalid base64: %w", err)
	}

	if len(keyring) == 0 {
		return nil, errors.New("manifest signature verification: keyring is empty")
	}

	var verified bool
	for _, key := range keyring {
		if ed25519.Verify(key, body, sig) {
			verified = true
			break
		}
	}
	if !verified {
		return nil, errors.New("manifest signature verification failed: no key in the keyring validated the signature — the manifest may be forged or the key may have been rotated")
	}

	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("manifest parse after signature verification: %w", err)
	}

	return &m, nil
}

// IsNewer returns true when remote is a newer semver than current.
//
// Both versions are normalised to "vMAJOR.MINOR.PATCH" canonical form
// via [semver.Canonical] before comparison. If either version string
// is not valid semver the function returns false (an unparseable
// version is treated as "not newer" rather than panicking).
func IsNewer(current, remote string) bool {
	cur := semver.Canonical("v" + current)
	rem := semver.Canonical("v" + remote)
	if cur == "" || rem == "" {
		return false
	}
	return semver.Compare(rem, cur) > 0
}

// MatchArtifact selects the [Artifact] from the manifest whose
// {OS, Arch, Format} triplet matches id.
//
// The matching rules follow ADR-0006:
//   - An exact match on all three fields wins.
//   - For darwin targets where the runtime reports arm64 or amd64 but
//     the manifest carries universal, the universal entry matches any
//     architecture. An exact-arch match (if present) still wins.
//   - If nothing matches, an error is returned.
func MatchArtifact(artifacts []Artifact, id ArtifactID) (*Artifact, error) {
	// Pass 1: exact match across all three fields.
	for i := range artifacts {
		a := &artifacts[i]
		if a.OS == id.OS && a.Arch == id.Arch && a.Format == id.Format {
			return a, nil
		}
	}

	// Pass 2: darwin universal fallback — a universal artefact
	// matches any darwin architecture.
	if id.OS == "darwin" && id.Arch != "universal" {
		for i := range artifacts {
			a := &artifacts[i]
			if a.OS == "darwin" && a.Arch == "universal" && a.Format == id.Format {
				return a, nil
			}
		}
	}

	return nil, fmt.Errorf("no artifact in manifest matching os=%s arch=%s format=%s", id.OS, id.Arch, id.Format)
}
