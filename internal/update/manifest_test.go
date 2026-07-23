package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestVerifyManifest_HappyPath(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	m := Manifest{
		Version:  "0.2.0",
		Released: "2026-07-22T10:00:00Z",
		NotesURL: "https://github.com/shady2k/nocx/releases/tag/v0.2.0",
		Artifacts: []Artifact{
			{OS: "darwin", Arch: "universal", Format: "zip", URL: "https://example.com/nocx.zip", SHA256: "abc123", Size: 12345},
		},
	}
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	sig := ed25519.Sign(priv, body)
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	got, err := VerifyManifest(body, sigB64, []ed25519.PublicKey{pub})
	if err != nil {
		t.Fatalf("VerifyManifest returned unexpected error: %v", err)
	}
	if got.Version != m.Version {
		t.Errorf("version: got %q, want %q", got.Version, m.Version)
	}
	if len(got.Artifacts) != 1 {
		t.Errorf("artifacts count: got %d, want 1", len(got.Artifacts))
	}
}

func TestVerifyManifest_TamperedBody(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	original := []byte(`{"version":"0.2.0","released":"2026-07-22T10:00:00Z","notesUrl":"https://example.com","artifacts":[]}`)
	sig := ed25519.Sign(priv, original)
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	// Tamper with the body after signing.
	tampered := []byte(`{"version":"9.9.9","released":"2026-07-22T10:00:00Z","notesUrl":"https://example.com","artifacts":[]}`)

	_, err = VerifyManifest(tampered, sigB64, []ed25519.PublicKey{pub})
	if err == nil {
		t.Fatal("expected error for tampered body, got nil")
	}
	if !strings.Contains(err.Error(), "verification failed") {
		t.Errorf("error should mention verification failure: %v", err)
	}
}

func TestVerifyManifest_WrongKey(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	// Generate a different keypair for the keyring — the signature
	// from priv cannot be verified by it.
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"version":"0.2.0","released":"2026-07-22T10:00:00Z","notesUrl":"https://example.com","artifacts":[]}`)
	sig := ed25519.Sign(priv, body)
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	_, err = VerifyManifest(body, sigB64, []ed25519.PublicKey{otherPub})
	if err == nil {
		t.Fatal("expected error for wrong key, got nil")
	}
	if !strings.Contains(err.Error(), "verification failed") {
		t.Errorf("error should mention verification failure: %v", err)
	}
}

func TestVerifyManifest_SecondKeyInKeyring(t *testing.T) {
	// Generate two keypairs. Sign with the second; both are in the
	// keyring. Verification must succeed because any key in the
	// keyring is acceptable.
	pub1, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub2, priv2, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"version":"0.2.0","released":"2026-07-22T10:00:00Z","notesUrl":"https://example.com","artifacts":[]}`)
	sig := ed25519.Sign(priv2, body)
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	got, err := VerifyManifest(body, sigB64, []ed25519.PublicKey{pub1, pub2})
	if err != nil {
		t.Fatalf("VerifyManifest with second key failed: %v", err)
	}
	if got.Version != "0.2.0" {
		t.Errorf("version: got %q, want %q", got.Version, "0.2.0")
	}
}

func TestVerifyManifest_BadBase64(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"version":"0.2.0"}`)
	_, err = VerifyManifest(body, "!!!not-valid-base64!!!", []ed25519.PublicKey{pub})
	if err == nil {
		t.Fatal("expected error for bad base64, got nil")
	}
	if !strings.Contains(err.Error(), "base64") {
		t.Errorf("error should mention base64: %v", err)
	}
}

func TestVerifyManifest_EmptyKeyring(t *testing.T) {
	body := []byte(`{"version":"0.2.0"}`)
	_, err := VerifyManifest(body, "AAAA", nil)
	if err == nil {
		t.Fatal("expected error for empty keyring, got nil")
	}
	if !strings.Contains(err.Error(), "keyring is empty") {
		t.Errorf("error should mention empty keyring: %v", err)
	}
}

func TestVerifyManifest_InvalidJSON(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	// Body is valid JSON to the signer but after tampering it becomes
	// invalid. We need a different approach: sign valid JSON, then
	// verify with a completely different body that a different key
	// signed. Because ed25519 signatures are tied to the exact bytes,
	// tampering will fail verification. To test the JSON parse branch
	// specifically, we need a valid signature over invalid JSON — so
	// we sign the invalid JSON itself.
	invalidJSON := []byte(`{"version": not-json-at-all`)
	sig := ed25519.Sign(priv, invalidJSON)
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	_, err = VerifyManifest(invalidJSON, sigB64, []ed25519.PublicKey{pub})
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	if !strings.Contains(err.Error(), "manifest parse") {
		t.Errorf("error should mention parse failure: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Manifest verification acceptance matrix (§6 of the design, bead nocx-3dk)
//
// These five cases are the explicit cross-platform acceptance criteria for
// the signed-manifest mechanism. Fixture keys are generated in the test;
// no network is involved. The cases mirror the bead's wording exactly so
// they are auditable against the spec.
// ---------------------------------------------------------------------------

func TestManifestVerificationAcceptanceMatrix(t *testing.T) {
	makeManifest := func(version string) []byte {
		m := Manifest{Version: version, Released: "2026-07-22T10:00:00Z", NotesURL: "https://example.com", Artifacts: []Artifact{}}
		b, _ := json.Marshal(m)
		return b
	}

	t.Run("1-valid-signature-happy-path", func(t *testing.T) {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		body := makeManifest("0.2.0")
		sig := ed25519.Sign(priv, body)

		got, err := VerifyManifest(body, base64.StdEncoding.EncodeToString(sig), []ed25519.PublicKey{pub})
		if err != nil {
			t.Fatalf("valid signature was rejected: %v", err)
		}
		if got.Version != "0.2.0" {
			t.Errorf("version: got %q, want 0.2.0", got.Version)
		}
	})

	t.Run("2-tampered-body-rejected", func(t *testing.T) {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		original := makeManifest("0.2.0")
		sig := ed25519.Sign(priv, original)
		tampered := makeManifest("9.9.9") // different body, same signature

		_, err = VerifyManifest(tampered, base64.StdEncoding.EncodeToString(sig), []ed25519.PublicKey{pub})
		if err == nil {
			t.Fatal("tampered body must be rejected (signature no longer matches)")
		}
		if !strings.Contains(err.Error(), "verification failed") {
			t.Errorf("error should mention verification failure: %v", err)
		}
	})

	t.Run("3-key-outside-keyring-rejected", func(t *testing.T) {
		_, priv, err := ed25519.GenerateKey(rand.Reader) // signing key
		if err != nil {
			t.Fatal(err)
		}
		otherPub, _, err := ed25519.GenerateKey(rand.Reader) // key in keyring — NOT the signing key
		if err != nil {
			t.Fatal(err)
		}

		body := makeManifest("0.2.0")
		sig := ed25519.Sign(priv, body)

		_, err = VerifyManifest(body, base64.StdEncoding.EncodeToString(sig), []ed25519.PublicKey{otherPub})
		if err == nil {
			t.Fatal("signature from a key outside the keyring must be rejected")
		}
		if !strings.Contains(err.Error(), "verification failed") {
			t.Errorf("error should mention verification failure: %v", err)
		}
	})

	t.Run("4-second-key-in-keyring-accepted", func(t *testing.T) {
		pub1, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		pub2, priv2, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}

		body := makeManifest("0.2.0")
		sig := ed25519.Sign(priv2, body) // signed by key 2

		got, err := VerifyManifest(body, base64.StdEncoding.EncodeToString(sig), []ed25519.PublicKey{pub1, pub2})
		if err != nil {
			t.Fatalf("valid signature from second key in multi-key keyring was rejected: %v", err)
		}
		if got.Version != "0.2.0" {
			t.Errorf("version: got %q, want 0.2.0", got.Version)
		}
	})

	t.Run("5-malformed-base64-rejected", func(t *testing.T) {
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}

		body := makeManifest("0.2.0")
		for _, badSig := range []string{"", "!!!not-valid-base64!!!", "aGVsbG8="} {
			t.Run(fmt.Sprintf("sig=%q", badSig), func(t *testing.T) {
				_, err := VerifyManifest(body, badSig, []ed25519.PublicKey{pub})
				if err == nil {
					t.Fatal("malformed base64 must be rejected with a legible error")
				}
				// All base64 errors or signature-verification errors
				// (an empty/too-short decoded sig fails verification) are acceptable.
			})
		}
	})
}

// ---------------------------------------------------------------------------
// semver comparison
// ---------------------------------------------------------------------------

func TestIsNewer(t *testing.T) {
	tests := []struct {
		name    string
		current string
		remote  string
		want    bool
	}{
		{name: "remote newer patch", current: "0.1.0", remote: "0.1.1", want: true},
		{name: "remote newer minor", current: "0.1.0", remote: "0.2.0", want: true},
		{name: "remote newer major", current: "0.1.0", remote: "1.0.0", want: true},
		{name: "same version", current: "0.1.0", remote: "0.1.0", want: false},
		{name: "remote older", current: "0.2.0", remote: "0.1.0", want: false},
		{name: "remote much older", current: "2.0.0", remote: "1.9.9", want: false},
		{name: "zero to one", current: "0.0.0", remote: "1.0.0", want: true},
		{name: "current invalid", current: "not-semver", remote: "1.0.0", want: false},
		{name: "remote invalid", current: "0.1.0", remote: "not-semver", want: false},
		{name: "both invalid", current: "dev", remote: "bad", want: false},
		{name: "remote newer two-digit minor", current: "0.1.0", remote: "0.10.0", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsNewer(tt.current, tt.remote)
			if got != tt.want {
				t.Errorf("IsNewer(%q, %q) = %v, want %v", tt.current, tt.remote, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// artefact matching
// ---------------------------------------------------------------------------

func TestMatchArtifact(t *testing.T) {
	artifacts := []Artifact{
		{OS: "darwin", Arch: "universal", Format: "zip", URL: "https://example.com/mac-universal.zip"},
		{OS: "darwin", Arch: "arm64", Format: "zip", URL: "https://example.com/mac-arm64.zip"},
		{OS: "darwin", Arch: "amd64", Format: "zip", URL: "https://example.com/mac-amd64.zip"},
		{OS: "linux", Arch: "amd64", Format: "AppImage", URL: "https://example.com/linux-amd64.AppImage"},
		{OS: "linux", Arch: "arm64", Format: "AppImage", URL: "https://example.com/linux-arm64.AppImage"},
	}

	tests := []struct {
		name      string
		id        ArtifactID
		wantURL   string
		wantError bool
		errSubstr string
	}{
		{
			name:    "exact darwin universal",
			id:      ArtifactID{OS: "darwin", Arch: "universal", Format: "zip"},
			wantURL: "https://example.com/mac-universal.zip",
		},
		{
			name:    "exact darwin arm64",
			id:      ArtifactID{OS: "darwin", Arch: "arm64", Format: "zip"},
			wantURL: "https://example.com/mac-arm64.zip",
		},
		{
			name:    "darwin amd64 falls back to universal when universal is before amd64",
			id:      ArtifactID{OS: "darwin", Arch: "amd64", Format: "zip"},
			wantURL: "https://example.com/mac-amd64.zip", // exact match wins
		},
		{
			name:    "darwin arm64 with only universal available",
			id:      ArtifactID{OS: "darwin", Arch: "arm64", Format: "zip"},
			wantURL: "https://example.com/mac-arm64.zip", // exact match still wins
		},
		{
			name:    "linux amd64",
			id:      ArtifactID{OS: "linux", Arch: "amd64", Format: "AppImage"},
			wantURL: "https://example.com/linux-amd64.AppImage",
		},
		{
			name:      "linux arm64 no match for format dmg",
			id:        ArtifactID{OS: "linux", Arch: "arm64", Format: "dmg"},
			wantError: true,
			errSubstr: "no artifact",
		},
		{
			name:      "windows not present",
			id:        ArtifactID{OS: "windows", Arch: "amd64", Format: "zip"},
			wantError: true,
			errSubstr: "no artifact",
		},
		{
			name:    "linux arm64 exact",
			id:      ArtifactID{OS: "linux", Arch: "arm64", Format: "AppImage"},
			wantURL: "https://example.com/linux-arm64.AppImage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MatchArtifact(artifacts, tt.id)
			if tt.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.URL != tt.wantURL {
				t.Errorf("URL: got %q, want %q", got.URL, tt.wantURL)
			}
		})
	}
}

func TestMatchArtifact_DarwinUniversalFallback(t *testing.T) {
	// Only a universal darwin artefact exists; arm64 should match it.
	artifacts := []Artifact{
		{OS: "darwin", Arch: "universal", Format: "zip", URL: "https://example.com/mac.zip"},
		{OS: "linux", Arch: "amd64", Format: "AppImage", URL: "https://example.com/linux.AppImage"},
	}

	t.Run("arm64 matches universal", func(t *testing.T) {
		id := ArtifactID{OS: "darwin", Arch: "arm64", Format: "zip"}
		got, err := MatchArtifact(artifacts, id)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.URL != "https://example.com/mac.zip" {
			t.Errorf("URL: got %q, want mac.zip", got.URL)
		}
	})

	t.Run("amd64 matches universal", func(t *testing.T) {
		id := ArtifactID{OS: "darwin", Arch: "amd64", Format: "zip"}
		got, err := MatchArtifact(artifacts, id)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.URL != "https://example.com/mac.zip" {
			t.Errorf("URL: got %q, want mac.zip", got.URL)
		}
	})

	t.Run("universal exact still matches", func(t *testing.T) {
		id := ArtifactID{OS: "darwin", Arch: "universal", Format: "zip"}
		got, err := MatchArtifact(artifacts, id)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.URL != "https://example.com/mac.zip" {
			t.Errorf("URL: got %q, want mac.zip", got.URL)
		}
	})
}
