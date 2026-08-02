package transport

import "testing"

// A traditional PEM-encrypted private key — `-----BEGIN RSA PRIVATE KEY-----`
// with a Proc-Type header. It works in every other SSH client, and nocx's own
// ssh_auth.go opens exactly this shape with ParsePrivateKeyWithPassphrase.
//
// It used to be refused, with a message telling the user to convert it. That
// turned "I cannot compute the fingerprint until it is unlocked" into "your
// key is invalid", and the conversion command it suggested was for a PUBLIC
// key format, so it would not have helped either. Reported by a user whose
// key works everywhere else.
func TestParsePrivateKeyMaterial_AcceptsEncryptedPEMWithoutAFingerprint(t *testing.T) {
	// gosec sees a PEM header and calls it a hardcoded credential. It is a
	// fixture: the body is the words "this is not real ciphertext" in base64,
	// and the headers are what the parser branches on.
	const encryptedPEM = //nolint:gosec // fixture, not a credential — see above
	`-----BEGIN RSA PRIVATE KEY-----
Proc-Type: 4,ENCRYPTED
DEK-Info: AES-128-CBC,0123456789ABCDEF0123456789ABCDEF

dGhpcyBpcyBub3QgcmVhbCBjaXBoZXJ0ZXh0IGJ1dCB0aGUgaGVhZGVycyBhcmUg
-----END RSA PRIVATE KEY-----
`
	fingerprint, wantsPassphrase, err := parsePrivateKeyMaterial(encryptedPEM)
	if err != nil {
		t.Fatalf("rejected a usable encrypted key: %v", err)
	}
	if fingerprint != "" {
		t.Errorf("fingerprint = %q, want empty — the public half is behind the passphrase", fingerprint)
	}
	if !wantsPassphrase {
		t.Error("passphraseWanted = false — the renderer would never ask for the passphrase")
	}
}
