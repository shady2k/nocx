package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
)

// signLikeTool reproduces exactly what cmd/manifest-sign writes: the standard
// base64 of a 64-byte ed25519 signature over the exact bytes, plus the trailing
// newline the signer appends and the verifier must tolerate. The keyring is the
// contract between that tool and this package, so the tests sign the way the
// tool signs rather than calling ed25519.Sign with different framing.
func signLikeTool(t *testing.T, priv ed25519.PrivateKey, msg []byte) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg)) + "\n"
}

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestVerifyManifest_ValidSignature(t *testing.T) {
	pub, priv := newKey(t)
	body := []byte(`{"version":"0.2.0"}`)
	if err := VerifyManifest(body, signLikeTool(t, priv, body), Keyring{pub}); err != nil {
		t.Fatalf("a valid signature was rejected: %v", err)
	}
}

func TestVerifyManifest_TamperedBody(t *testing.T) {
	pub, priv := newKey(t)
	// A signature over the real body must not verify one byte of a different one.
	sig := signLikeTool(t, priv, []byte(`{"version":"0.2.0"}`))
	if err := VerifyManifest([]byte(`{"version":"0.2.1"}`), sig, Keyring{pub}); err == nil {
		t.Fatal("a tampered body verified")
	}
}

func TestVerifyManifest_KeyOutsideKeyring(t *testing.T) {
	_, signerPriv := newKey(t) // this key never enters the keyring
	otherPub, _ := newKey(t)
	body := []byte(`{"version":"0.2.0"}`)
	err := VerifyManifest(body, signLikeTool(t, signerPriv, body), Keyring{otherPub})
	if !errors.Is(err, ErrUnverified) {
		t.Fatalf("a signature from a key outside the keyring should be ErrUnverified, got %v", err)
	}
}

func TestVerifyManifest_SecondKeyInKeyring(t *testing.T) {
	// Rotation support (design §6): the keyring carries an old key A and a new
	// key B, and a manifest signed by B alone must still verify.
	aPub, _ := newKey(t)
	bPub, bPriv := newKey(t)
	body := []byte(`{"version":"0.3.0"}`)
	if err := VerifyManifest(body, signLikeTool(t, bPriv, body), Keyring{aPub, bPub}); err != nil {
		t.Fatalf("a signature from the second keyring key was rejected: %v", err)
	}
}

func TestVerifyManifest_MalformedBase64(t *testing.T) {
	pub, _ := newKey(t)
	if err := VerifyManifest([]byte(`{}`), "not valid base64!!!", Keyring{pub}); err == nil {
		t.Fatal("a malformed base64 signature was accepted")
	}
}

func TestVerifyManifest_EmptyKeyring(t *testing.T) {
	// Fail closed: with no compiled-in keys every manifest is rejected, so a
	// build that forgot to bake in a key can never trust an unsigned world.
	_, priv := newKey(t)
	body := []byte(`{}`)
	if err := VerifyManifest(body, signLikeTool(t, priv, body), Keyring{}); err == nil {
		t.Fatal("an empty keyring accepted a signature")
	}
}

func TestVerifyManifest_WrongSignatureLength(t *testing.T) {
	// Valid base64 but not 64 bytes: rejected on length before ed25519.Verify,
	// which is defined only for a correctly sized signature.
	pub, _ := newKey(t)
	short := base64.StdEncoding.EncodeToString([]byte("too short"))
	if err := VerifyManifest([]byte(`{}`), short, Keyring{pub}); err == nil {
		t.Fatal("an undersized signature was accepted")
	}
}

func TestParseKeyring_RoundTripsToolPublicKey(t *testing.T) {
	// A public key printed by `manifest-sign -keygen` (standard base64 of the
	// 32-byte key) must parse and then verify a signature from its private half.
	pub, priv := newKey(t)
	kr, err := ParseKeyring(base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatalf("ParseKeyring rejected a well-formed key: %v", err)
	}
	body := []byte(`{"version":"1.0.0"}`)
	if err := VerifyManifest(body, signLikeTool(t, priv, body), kr); err != nil {
		t.Fatalf("a key parsed from base64 did not verify its own signature: %v", err)
	}
}

func TestParseKeyring_RejectsMalformed(t *testing.T) {
	for _, tc := range []struct{ name, key string }{
		{"not base64", "@@@ not base64 @@@"},
		{"wrong length", base64.StdEncoding.EncodeToString([]byte("short"))},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseKeyring(tc.key); err == nil {
				t.Fatalf("ParseKeyring accepted a malformed key (%s)", tc.name)
			}
		})
	}
}

func TestProductionKeyringIsWellFormed(t *testing.T) {
	// Whatever keys are compiled in (none yet — the first lands with nocx-a75.3)
	// must parse, so a typo in a pasted key fails here rather than in the field
	// where it would silently reject every real manifest.
	if _, err := ProductionKeyring(); err != nil {
		t.Fatalf("the compiled-in keyring is malformed: %v", err)
	}
}
