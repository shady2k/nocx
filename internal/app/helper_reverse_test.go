package app

// The coordinator's half of the helper's ssh connection, tested against the
// things it delegates to rather than against a description of them: material
// comes from a stanced resolver, host keys from a real known_hosts file, and
// signatures from the same signer construction the dial path uses.
//
// What is NOT here is the ssh server: a coordinator does not dial in these
// handlers, it answers questions about material and trust, so the fixture this
// file needs is a key and a file. The end-to-end path — a real server, a real
// helper, a real probe — is internal/helper/sshsvc's.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/vault"
	gossh "golang.org/x/crypto/ssh"
)

// fakeSecrets is the stanced resolver this file hands the handlers: one method,
// scripted. The real resolver's stance machinery has its own tests in
// internal/credential; what these tests are about is what a handler DOES with
// the answer, including the answer "the vault will not open".
type fakeSecrets struct {
	mu       sync.Mutex
	material map[credential.SecretID][]byte
	err      error
	asked    []credential.SecretID
}

func (f *fakeSecrets) Resolve(_ context.Context, id credential.SecretID, _ credential.Stance) (credential.Secret, error) {
	f.mu.Lock()
	f.asked = append(f.asked, id)
	err := f.err
	material, ok := f.material[id]
	f.mu.Unlock()
	if err != nil {
		return credential.Secret{}, err
	}
	if !ok {
		return credential.Secret{}, vault.ErrSecretNotFound
	}
	return credential.NewSecretBytes(material), nil
}

func (f *fakeSecrets) resolvedIDs() []credential.SecretID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]credential.SecretID(nil), f.asked...)
}

// reverseFixture is one coordinator's answers.
type reverseFixture struct {
	handlers *helperReverse
	secrets  *fakeSecrets
	client   *ssh.RealClient
	host     string
}

func newReverseFixture(t *testing.T, material map[credential.SecretID][]byte) *reverseFixture {
	t.Helper()
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	client, err := ssh.NewReal(log.NewSlogAdapter(nil), ssh.WithKnownHostsFile(knownHosts))
	if err != nil {
		t.Fatalf("ssh.NewReal: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	secrets := &fakeSecrets{material: material}
	return &reverseFixture{
		handlers: &helperReverse{client: client, secrets: secrets, log: slog.New(slog.NewTextHandler(io.Discard, nil))},
		secrets:  secrets,
		client:   client,
		host:     "example.test:22",
	}
}

func testKeyMaterial(t *testing.T, passphrase string) (pemBytes []byte, signer gossh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err = gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = gossh.MarshalPrivateKey(priv, "nocx-test")
	} else {
		block, err = gossh.MarshalPrivateKeyWithPassphrase(priv, "nocx-test", []byte(passphrase))
	}
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	return pem.EncodeToMemory(block), signer
}

func params(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return raw
}

func reverseRefusalCode(t *testing.T, err error) string {
	t.Helper()
	var refusal *proto.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error %v is not a helper refusal, so the caller cannot act on it", err)
	}
	return refusal.Code
}

// ── secret ─────────────────────────────────────────────────────────────

func TestTheSecretHandlerReadsTheMaterialTheReferenceNames(t *testing.T) {
	f := newReverseFixture(t, map[credential.SecretID][]byte{"cred-1": []byte("hunter2")})

	result, err := f.handlers.secret(context.Background(), params(t, proto.SecretParams{
		Credential: proto.SSHCredential{Ref: "cred-1"},
		Purpose:    proto.PurposePassword,
	}))
	if err != nil {
		t.Fatalf("secret: %v", err)
	}
	got, ok := result.(proto.SecretResult)
	if !ok {
		t.Fatalf("result type = %T, want proto.SecretResult", result)
	}
	if string(got.Secret) != "hunter2" {
		t.Fatalf("secret = %q, want the stored material", got.Secret)
	}
	if ids := f.secrets.resolvedIDs(); len(ids) != 1 || ids[0] != "cred-1" {
		t.Fatalf("resolved %v, want exactly the reference the helper asked for", ids)
	}
}

// TestTheSecretHandlerRefusesASealedVaultByItsOwnName is the pair that matters
// most in this file: a vault that will not open is not "wrong password", and
// the code that says so is the one the coordinator's own caller turns into the
// unlock sheet it already owns.
func TestTheSecretHandlerRefusesASealedVaultByItsOwnName(t *testing.T) {
	f := newReverseFixture(t, map[credential.SecretID][]byte{"cred-1": []byte("hunter2")})
	f.secrets.err = vault.ErrVaultSealed

	_, err := f.handlers.secret(context.Background(), params(t, proto.SecretParams{
		Credential: proto.SSHCredential{Ref: "cred-1"},
		Purpose:    proto.PurposePassword,
	}))
	if code := reverseRefusalCode(t, err); code != proto.ErrCodeVaultSealed {
		t.Fatalf("code = %q, want %q", code, proto.ErrCodeVaultSealed)
	}
}

func TestTheSecretHandlerRefusesWhatItCannotResolve(t *testing.T) {
	f := newReverseFixture(t, map[credential.SecretID][]byte{
		"cred-1": []byte("pw"),
		"empty":  {},
	})
	cases := []struct {
		name   string
		params proto.SecretParams
		code   string
	}{
		{
			"a purpose this coordinator does not know",
			proto.SecretParams{Credential: proto.SSHCredential{Ref: "cred-1"}, Purpose: "otp"},
			proto.ErrCodeBadParams,
		},
		{
			"a passphrase asked for by a credential that has none",
			proto.SecretParams{Credential: proto.SSHCredential{Ref: "cred-1"}, Purpose: proto.PurposePassphrase},
			proto.ErrCodeBadParams,
		},
		{
			"a credential reference with nothing behind it",
			proto.SecretParams{Credential: proto.SSHCredential{Ref: "no-such-id"}, Purpose: proto.PurposePassword},
			"",
		},
		{
			"stored material that is empty",
			proto.SecretParams{Credential: proto.SSHCredential{Ref: "empty"}, Purpose: proto.PurposePassword},
			"",
		},
	}
	for _, tc := range cases {
		_, err := f.handlers.secret(context.Background(), params(t, tc.params))
		if err == nil {
			t.Errorf("%s was answered", tc.name)
			continue
		}
		if tc.code == "" {
			// An unrecognised failure stays `internal`, which is the honest
			// answer: a caller can do nothing with a code invented for it.
			var refusal *proto.Refusal
			if errors.As(err, &refusal) {
				t.Errorf("%s produced the code %q, want an unclassified failure", tc.name, refusal.Code)
			}
			continue
		}
		if code := reverseRefusalCode(t, err); code != tc.code {
			t.Errorf("%s: code = %q, want %q", tc.name, code, tc.code)
		}
	}
}

// ── sign ───────────────────────────────────────────────────────────────

func TestTheSignHandlerSignsWithTheStoredKey(t *testing.T) {
	keyPEM, signer := testKeyMaterial(t, "")
	f := newReverseFixture(t, map[credential.SecretID][]byte{"key-1": keyPEM})

	challenge := []byte("the handshake's challenge")
	result, err := f.handlers.sign(context.Background(), params(t, proto.SignParams{
		Credential: proto.SSHCredential{Ref: "key-1"},
		Challenge:  challenge,
		Algorithm:  signer.PublicKey().Type(),
	}))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	got, ok := result.(proto.SignResult)
	if !ok {
		t.Fatalf("result type = %T, want proto.SignResult", result)
	}
	var sig gossh.Signature
	if err := gossh.Unmarshal(got.Signature, &sig); err != nil {
		t.Fatalf("the signature is not an ssh signature: %v", err)
	}
	if err := signer.PublicKey().Verify(challenge, &sig); err != nil {
		t.Fatalf("the signature does not verify against the key: %v", err)
	}
	// The key's bytes are not in the answer — the signature is, and a signature
	// is what a signature is for.
	if bytesContainsAny(got.Signature, keyPEM) {
		t.Fatal("the key's own bytes came back in the signature")
	}
}

// TestTheSignHandlerUnlocksAnEncryptedKeyWithItsPassphrase is the half of the
// credential shape that only exists for this: `passphraseRef` is a reference,
// the coordinator resolves it, and the helper never sees either half.
func TestTheSignHandlerUnlocksAnEncryptedKeyWithItsPassphrase(t *testing.T) {
	const passphrase = "correct horse"
	keyPEM, signer := testKeyMaterial(t, passphrase)
	f := newReverseFixture(t, map[credential.SecretID][]byte{
		"key-1":      keyPEM,
		"key-1-pass": []byte(passphrase),
	})

	challenge := []byte("another challenge")
	result, err := f.handlers.sign(context.Background(), params(t, proto.SignParams{
		Credential: proto.SSHCredential{Ref: "key-1", PassphraseRef: "key-1-pass"},
		Challenge:  challenge,
		Algorithm:  signer.PublicKey().Type(),
	}))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	got, ok := result.(proto.SignResult)
	if !ok {
		t.Fatalf("result type = %T, want proto.SignResult", result)
	}
	var sig gossh.Signature
	if err := gossh.Unmarshal(got.Signature, &sig); err != nil {
		t.Fatalf("the signature is not an ssh signature: %v", err)
	}
	if err := signer.PublicKey().Verify(challenge, &sig); err != nil {
		t.Fatalf("the signature does not verify: %v", err)
	}
}

// TestTheSignHandlerReportsALockedKeyAsNeedsInteractive is that case's refusal
// half: the key is fine, it is locked, and the answer is the one the probe
// vocabulary already has for "a person has to be asked".
func TestTheSignHandlerReportsALockedKeyAsNeedsInteractive(t *testing.T) {
	keyPEM, signer := testKeyMaterial(t, "the passphrase nobody stored")
	f := newReverseFixture(t, map[credential.SecretID][]byte{"key-1": keyPEM})

	_, err := f.handlers.sign(context.Background(), params(t, proto.SignParams{
		Credential: proto.SSHCredential{Ref: "key-1"},
		Challenge:  []byte("challenge"),
		Algorithm:  signer.PublicKey().Type(),
	}))
	if code := reverseRefusalCode(t, err); code != proto.ErrCodeNeedsInteractive {
		t.Fatalf("code = %q, want %q", code, proto.ErrCodeNeedsInteractive)
	}
}

func TestTheSignHandlerRefusesWhatItMustNotSign(t *testing.T) {
	keyPEM, signer := testKeyMaterial(t, "")
	f := newReverseFixture(t, map[credential.SecretID][]byte{"key-1": keyPEM})

	cases := []struct {
		name   string
		params proto.SignParams
	}{
		{
			"no challenge — signing nothing is not a discovery question",
			proto.SignParams{Credential: proto.SSHCredential{Ref: "key-1"}, Algorithm: signer.PublicKey().Type()},
		},
		{
			"no credential",
			proto.SignParams{Challenge: []byte("c"), Algorithm: signer.PublicKey().Type()},
		},
		{
			"no algorithm",
			proto.SignParams{Credential: proto.SSHCredential{Ref: "key-1"}, Challenge: []byte("c")},
		},
		{
			"an algorithm the key is not",
			proto.SignParams{Credential: proto.SSHCredential{Ref: "key-1"}, Challenge: []byte("c"), Algorithm: "ssh-rsa"},
		},
	}
	for _, tc := range cases {
		_, err := f.handlers.sign(context.Background(), params(t, tc.params))
		if err == nil {
			t.Errorf("%s was signed", tc.name)
			continue
		}
		if code := reverseRefusalCode(t, err); code != proto.ErrCodeBadParams {
			t.Errorf("%s: code = %q, want %q", tc.name, code, proto.ErrCodeBadParams)
		}
	}
}

// ── host keys ──────────────────────────────────────────────────────────

// TestTheHostKeyHandlersAnswerTheThreeVerdicts walks one host through the whole
// trust story against a real known_hosts file: nothing recorded, recorded, and
// recorded-but-different. The three are different answers and the middle one
// must never be reported as either of its neighbours.
func TestTheHostKeyHandlersAnswerTheThreeVerdicts(t *testing.T) {
	f := newReverseFixture(t, nil)
	ctx := context.Background()
	first := newTestHostKey(t)
	second := newTestHostKey(t)

	// Nothing is recorded for this host.
	result, err := f.handlers.verifyHostKey(ctx, params(t, proto.VerifyHostKeyParams{
		Host: f.host, Algorithm: first.Type(), Key: first.Marshal(),
	}))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	unknown, ok := result.(proto.VerifyHostKeyResult)
	if !ok {
		t.Fatalf("result type = %T, want proto.VerifyHostKeyResult", result)
	}
	if unknown.Verdict != proto.HostKeyUnknown {
		t.Fatalf("verdict = %q, want unknown", unknown.Verdict)
	}
	if unknown.Fingerprint != gossh.FingerprintSHA256(first) {
		t.Fatalf("fingerprint = %q, want the offered key's", unknown.Fingerprint)
	}
	if unknown.Expected != "" {
		t.Fatalf("expected = %q on an unknown key, want empty", unknown.Expected)
	}

	// The accept path records it — the one write path this repository has.
	trusted, err := f.handlers.trustHostKey(ctx, params(t, proto.TrustHostKeyParams{
		Host: f.host, Algorithm: first.Type(), Key: first.Marshal(),
	}))
	if err != nil {
		t.Fatalf("trust: %v", err)
	}
	recorded, ok := trusted.(proto.TrustHostKeyResult)
	if !ok {
		t.Fatalf("result type = %T, want proto.TrustHostKeyResult", trusted)
	}
	if recorded.Fingerprint != gossh.FingerprintSHA256(first) {
		t.Fatalf("recorded fingerprint = %q, want the offered key's", recorded.Fingerprint)
	}

	// The same key now verifies.
	result, err = f.handlers.verifyHostKey(ctx, params(t, proto.VerifyHostKeyParams{
		Host: f.host, Algorithm: first.Type(), Key: first.Marshal(),
	}))
	if err != nil {
		t.Fatalf("verify after trust: %v", err)
	}
	verified, ok := result.(proto.VerifyHostKeyResult)
	if !ok {
		t.Fatalf("result type = %T, want proto.VerifyHostKeyResult", result)
	}
	if verified.Verdict != proto.HostKeyTrusted {
		t.Fatalf("verdict after trust = %q, want trusted", verified.Verdict)
	}

	// A DIFFERENT key for the same host is `changed`, and it carries the
	// recorded fingerprint so nobody is asked to accept a key they cannot
	// compare against anything.
	result, err = f.handlers.verifyHostKey(ctx, params(t, proto.VerifyHostKeyParams{
		Host: f.host, Algorithm: second.Type(), Key: second.Marshal(),
	}))
	if err != nil {
		t.Fatalf("verify a second key: %v", err)
	}
	changed, ok := result.(proto.VerifyHostKeyResult)
	if !ok {
		t.Fatalf("result type = %T, want proto.VerifyHostKeyResult", result)
	}
	if changed.Verdict != proto.HostKeyChanged {
		t.Fatalf("verdict = %q, want changed", changed.Verdict)
	}
	if changed.Expected != gossh.FingerprintSHA256(first) {
		t.Fatalf("expected = %q, want the recorded key's fingerprint", changed.Expected)
	}
	if changed.Fingerprint != gossh.FingerprintSHA256(second) {
		t.Fatalf("fingerprint = %q, want the offered key's", changed.Fingerprint)
	}
}

func TestTheHostKeyHandlersRefuseWhatTheyCannotJudge(t *testing.T) {
	f := newReverseFixture(t, nil)
	key := newTestHostKey(t)

	cases := []struct {
		name   string
		params proto.VerifyHostKeyParams
	}{
		{"no host", proto.VerifyHostKeyParams{Algorithm: key.Type(), Key: key.Marshal()}},
		{"no key", proto.VerifyHostKeyParams{Host: f.host, Algorithm: key.Type()}},
		{
			"an algorithm the key is not",
			proto.VerifyHostKeyParams{Host: f.host, Algorithm: "ssh-rsa", Key: key.Marshal()},
		},
		{
			"key bytes that are not a public key",
			proto.VerifyHostKeyParams{Host: f.host, Algorithm: key.Type(), Key: []byte("not a key")},
		},
		{
			// The regression this case exists for: a storage identity the trust
			// store cannot split used to reach knownhosts, whose check splits
			// the PEER address first — with a nil one the coordinator did not
			// refuse, it panicked. A refusal is what a caller can act on.
			"a host that is not host:port",
			proto.VerifyHostKeyParams{Host: "example.test", Algorithm: key.Type(), Key: key.Marshal()},
		},
	}
	for _, tc := range cases {
		_, err := f.handlers.verifyHostKey(context.Background(), params(t, tc.params))
		if err == nil {
			t.Errorf("%s was answered", tc.name)
			continue
		}
		if code := reverseRefusalCode(t, err); code != proto.ErrCodeBadParams {
			t.Errorf("%s: code = %q, want %q", tc.name, code, proto.ErrCodeBadParams)
		}
	}
}

func newTestHostKey(t *testing.T) gossh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	key, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return key
}

func bytesContainsAny(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == string(needle) {
			return true
		}
	}
	return false
}
