package ssh

// The destination a helper dials, resolved by the coordinator (nocx-50w7p.3,
// extended by nocx-50w7p.11).
//
// This is the seam that keeps the owner's invariant honest in both directions:
// the helper is handed an address and a REFERENCE, and the coordinator's own
// rules — alias resolution, the authorization of a linked credential against
// the endpoint its profile names — still run. A test that only proved the happy
// path would leave the refusals as sentences nobody has ever seen produced, so
// every case below that can fail is paired with the one that succeeds: an agent
// that is there beside one that is not, a key file that opens beside one that
// cannot, a prompt that somebody can answer beside one nobody can.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// targetTestClient is a RealClient with a stub resolver and no network: the
// resolution path is what is under test, and it never dials.
func targetTestClient(t *testing.T) *RealClient {
	t.Helper()
	rc, err := NewReal(log.NewSlogAdapter(nil), WithConfigResolver(NewStubConfigResolver()))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })
	return rc
}

// TestResolveTargetAnswersTheResolvedAddressAndTheReference: the helper gets
// what it dials and what to ask for, and it gets no material.
func TestResolveTargetAnswersTheResolvedAddressAndTheReference(t *testing.T) {
	rc := targetTestClient(t)

	target, err := rc.ResolveTarget(context.Background(), "prod.example.com:2222",
		WithUser("deploy"),
		WithCredentials(nil, credential.SecretID("secret-7")),
	)
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if target.Host != "prod.example.com" || target.Port != 2222 || target.User != "deploy" {
		t.Fatalf("resolved %s@%s:%d, want deploy@prod.example.com:2222", target.User, target.Host, target.Port)
	}
	if target.Auth != DialAuthPassword {
		t.Fatalf("auth = %q, want %q", target.Auth, DialAuthPassword)
	}
	if got, want := target.Credential.String(), "vault:secret-7"; got != want {
		t.Fatalf("credential = %q, want %q", got, want)
	}
	if len(target.PublicKey) != 0 {
		t.Fatalf("a password target carries a public key (%d bytes)", len(target.PublicKey))
	}
	if len(target.Route) != 0 {
		t.Fatalf("a direct destination resolved %d hops", len(target.Route))
	}
	if target.KnownHostsAddr != "prod.example.com:2222" {
		t.Fatalf("known-hosts identity = %q, want the dial address for a direct route", target.KnownHostsAddr)
	}
}

// TestResolveTargetResolvesARouteIntoOrderedHops is the jump-route half: the
// destination a helper dials carries the hops it passes through, in dial order,
// each with its own account and its own credential reference.
//
// It replaces a test that asserted the OPPOSITE (a routed destination was
// refused by name, ssh.ErrRoutedDial), and the shape of the replacement is the
// point: a route is no longer a refusal, it is a list of endpoints, and what a
// helper must be able to see is which account and which credential belongs to
// each one.
func TestResolveTargetResolvesARouteIntoOrderedHops(t *testing.T) {
	rc := targetTestClient(t)

	bastionKey := writeTestKeyFile(t)

	target, err := rc.ResolveTarget(context.Background(), "prod.example.com",
		WithUser("deploy"),
		WithCredentials(nil, credential.SecretID("secret-7")),
		WithJumpHost("bastion.example.com", 2222, "jump", "publicKey"),
		WithJumpConfig(&ConnectConfig{User: "jump", Port: 2222, AuthMode: "publicKey", KeyFile: bastionKey}),
	)
	if err != nil {
		t.Fatalf("ResolveTarget through a jump host: %v", err)
	}
	if len(target.Route) != 1 {
		t.Fatalf("route has %d hops, want 1", len(target.Route))
	}
	hop := target.Route[0]
	if hop.Host != "bastion.example.com" || hop.Port != 2222 || hop.User != "jump" {
		t.Fatalf("hop = %s@%s:%d, want jump@bastion.example.com:2222", hop.User, hop.Host, hop.Port)
	}
	if hop.KnownHostsAddr != "bastion.example.com:2222" {
		t.Fatalf("hop storage identity = %q, want its own dial address", hop.KnownHostsAddr)
	}
	// The destination's storage identity is NOT its dial address once there is
	// a route: a key accepted through a bastion must never vouch for the same
	// host dialed directly (the coordinator's route record).
	if target.KnownHostsAddr == net.JoinHostPort(target.Host, "22") {
		t.Fatalf("routed destination stored under its dial address %q", target.KnownHostsAddr)
	}
	if target.KnownHostsAddr == "" {
		t.Fatal("routed destination carries no storage identity, so the helper would look up a line nobody wrote")
	}
}

// TestResolveTargetWalksAMultiHopChainInDialOrder: a chain of two bastions
// resolves to two hops in the order they are dialed, and the second hop's
// storage identity is route-derived for the same reason the destination's is.
func TestResolveTargetWalksAMultiHopChainInDialOrder(t *testing.T) {
	rc := targetTestClient(t)

	inner := &ConnectConfig{
		User:     "inner",
		JumpHost: "outer.example.com", JumpPort: 22, JumpUser: "outer",
		AuthMode: "publicKey", KeyFile: writeTestKeyFile(t),
	}
	outer := &ConnectConfig{User: "outer", AuthMode: "publicKey", KeyFile: writeTestKeyFile(t)}
	inner.JumpConfig = outer

	target, err := rc.ResolveTarget(context.Background(), "prod.example.com",
		WithUser("deploy"),
		WithCredentials(nil, credential.SecretID("secret-7")),
		WithJumpHost("bastion.example.com", 22, "jump", "publicKey"),
		WithJumpConfig(inner),
	)
	if err != nil {
		t.Fatalf("ResolveTarget through two jumps: %v", err)
	}
	if len(target.Route) != 2 {
		t.Fatalf("route has %d hops, want 2", len(target.Route))
	}
	if got := target.Route[0].Host; got != "bastion.example.com" {
		t.Fatalf("first hop = %q, want the nearest bastion", got)
	}
	if got := target.Route[1].Host; got != "outer.example.com" {
		t.Fatalf("second hop = %q, want the far bastion", got)
	}
	if got := target.Route[1].User; got != "outer" {
		t.Fatalf("second hop user = %q, want the account its own profile names", got)
	}
	if target.Route[0].KnownHostsAddr == "" || target.Route[1].KnownHostsAddr == "" {
		t.Fatal("a hop carries no storage identity")
	}
}

// TestResolveTargetRefusesAHopThatNamesNoHost: a chain whose parent names no
// host is malformed, and resolving it against some other field would dial an
// address nobody chose.
func TestResolveTargetRefusesAHopThatNamesNoHost(t *testing.T) {
	rc := targetTestClient(t)

	_, err := rc.ResolveTarget(context.Background(), "prod.example.com",
		WithUser("deploy"),
		WithCredentials(nil, credential.SecretID("secret-7")),
		WithJumpConfig(&ConnectConfig{User: "jump"}),
	)
	if err == nil {
		t.Fatal("a jump config with no host resolved a route")
	}
}

// TestResolveTargetHandsOverAKeyFileWithoutItsBytes: an inline key file is read
// and parsed HERE, and what crosses is a reference and the public half — the
// private bytes never leave this process, which is the whole reason a key can be
// offered at all now that the dial is one process away.
func TestResolveTargetHandsOverAKeyFileWithoutItsBytes(t *testing.T) {
	rc := targetTestClient(t)
	keyPEM, signer := generateTestKey(t)
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	target, err := rc.ResolveTarget(context.Background(), "prod.example.com", WithUser("deploy"), WithKeyFile(path))
	if err != nil {
		t.Fatalf("ResolveTarget with an inline key file: %v", err)
	}
	if target.Auth != DialAuthKey {
		t.Fatalf("auth = %q, want %q", target.Auth, DialAuthKey)
	}
	if got, want := target.Credential.String(), "file:"+path; got != want {
		t.Fatalf("credential = %q, want %q", got, want)
	}
	if string(target.PublicKey) != string(signer.PublicKey().Marshal()) {
		t.Fatalf("public half = %x, want the key's own wire blob", target.PublicKey)
	}
	if string(target.PublicKey) == string(keyPEM) {
		t.Fatal("the PRIVATE key was handed over as the public half")
	}
}

// TestResolveTargetRefusesAKeyFileItCannotUnlock is the paired refusal for the
// case above: an encrypted key with no passphrase to read is ErrEncryptedKey,
// which the probe reports as `needs-interactive` rather than as a bad
// credential — the key is fine, it is only locked.
func TestResolveTargetRefusesAKeyFileItCannotUnlock(t *testing.T) {
	rc := targetTestClient(t)
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, encryptedTestKey(t), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	_, err := rc.ResolveTarget(context.Background(), "prod.example.com", WithUser("deploy"), WithKeyFile(path))
	var locked *ErrEncryptedKey
	if !errors.As(err, &locked) {
		t.Fatalf("ResolveTarget with a locked key = %v, want *ErrEncryptedKey", err)
	}
}

// agentSocket starts a real ssh-agent holding one real key on a unix socket,
// points SSH_AUTH_SOCK at it, and answers that key's public half — so the
// resolution under test enumerates a real agent rather than a stub of one.
func agentSocket(t *testing.T) gossh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate agent key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("agent signer: %v", err)
	}
	keyring := agent.NewKeyring()
	if addErr := keyring.Add(agent.AddedKey{PrivateKey: priv, Comment: "nocx-test"}); addErr != nil {
		t.Fatalf("add key to the agent: %v", addErr)
	}

	dir := t.TempDir()
	sock := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_ = agent.ServeAgent(keyring, conn)
			}()
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)
	return signer.PublicKey()
}

// TestResolveTargetOffersTheAgentKeyTheAgentHolds: a connection whose mode is
// the agent resolves to a KEY identity carrying the agent key's public half and
// a reference the coordinator can sign with — the agent socket itself never
// crosses, because what crosses is a fingerprint.
func TestResolveTargetOffersTheAgentKeyTheAgentHolds(t *testing.T) {
	held := agentSocket(t)

	rc := targetTestClient(t)
	target, err := rc.ResolveTarget(context.Background(), "prod.example.com", WithUser("deploy"), WithAuthMode("agent"))
	if err != nil {
		t.Fatalf("ResolveTarget with an agent: %v", err)
	}
	if target.Auth != DialAuthKey {
		t.Fatalf("auth = %q, want %q", target.Auth, DialAuthKey)
	}
	if got := target.Credential.Kind(); got != CredentialAgent {
		t.Fatalf("credential kind = %q, want %q", got, CredentialAgent)
	}
	if len(target.PublicKey) == 0 {
		t.Fatal("an agent identity carries no public half, so the helper could not declare which key it offers")
	}
	if want := gossh.FingerprintSHA256(held); target.Credential.ID() != want {
		t.Fatalf("agent reference = %q, want the fingerprint of the key the agent holds (%q)", target.Credential.ID(), want)
	}
	if string(target.PublicKey) != string(held.Marshal()) {
		t.Fatalf("public half = %x, want the agent key's own wire blob", target.PublicKey)
	}
}

// TestResolveTargetRefusesAnAgentThatIsNotThere is the paired refusal, and it
// is named: ErrNoAuthMethod with Mode "agent" is the type the coordinator's own
// ladder has always answered with, and the sentence says what is wrong (a
// desktop session) rather than sending a person to look at the host.
func TestResolveTargetRefusesAnAgentThatIsNotThere(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")

	rc := targetTestClient(t)
	_, err := rc.ResolveTarget(context.Background(), "prod.example.com", WithUser("deploy"), WithAuthMode("agent"))
	var noMethod *ErrNoAuthMethod
	if !errors.As(err, &noMethod) {
		t.Fatalf("ResolveTarget with no agent = %v, want *ErrNoAuthMethod", err)
	}
	if noMethod.Mode != "agent" {
		t.Fatalf("refused mode = %q, want agent", noMethod.Mode)
	}
}

// TestResolveTargetAsksForAPersonOnlyWhenSomebodyCanAnswer is the prompt rung:
// a connection that can raise a dialog resolves to the interactive kind, and the
// same connection with no requester wired — a PROBE's options — declines by name
// instead of raising an ask nobody asked for.
func TestResolveTargetAsksForAPersonOnlyWhenSomebodyCanAnswer(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	rc := targetTestClient(t)

	target, err := rc.ResolveTarget(context.Background(), "prod.example.com",
		WithUser("deploy"), WithAuthMode("keyboardInteractive"), WithPasswordRequester(&recordingAsker{}))
	if err != nil {
		t.Fatalf("ResolveTarget with a requester: %v", err)
	}
	if target.Auth != DialAuthInteractive {
		t.Fatalf("auth = %q, want %q", target.Auth, DialAuthInteractive)
	}
	if !target.Credential.IsZero() || len(target.PublicKey) != 0 {
		t.Fatal("the interactive rung carries material; the person IS the credential")
	}

	_, err = rc.ResolveTarget(context.Background(), "prod.example.com",
		WithUser("deploy"), WithAuthMode("keyboardInteractive"), WithoutPasswordPrompt())
	var noMethod *ErrNoAuthMethod
	if !errors.As(err, &noMethod) {
		t.Fatalf("ResolveTarget with the prompt rung un-wired = %v, want *ErrNoAuthMethod", err)
	}
}

// TestResolveTargetRefusesAModeWithNothingBehindIt: a profile that filters to
// one method and holds no material for it is a configuration answer, not a
// server one — and it must not silently fall through to another rung.
func TestResolveTargetRefusesAModeWithNothingBehindIt(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	rc := targetTestClient(t)

	_, err := rc.ResolveTarget(context.Background(), "prod.example.com",
		WithUser("deploy"),
		WithAuthMode("publicKey"),
		WithCredentials(nil, credential.SecretID("stored-password")),
	)
	var noMethod *ErrNoAuthMethod
	if !errors.As(err, &noMethod) {
		t.Fatalf("ResolveTarget = %v, want *ErrNoAuthMethod", err)
	}
}

// recordingAsker is the prompt seam a connection carries when a person can be
// asked. It answers nothing: resolution never asks, and a test that did would
// be asserting on a dialog rather than on the destination.
type recordingAsker struct{}

func (r *recordingAsker) RequestConnectionPassword(context.Context, PasswordRequest) (PasswordAnswer, error) {
	return PasswordAnswer{}, errors.New("no answer in this test")
}

// encryptedTestKey is one private key with a passphrase nobody here has: what
// matters is that parsing it without one fails, which is the state a locked key
// is in.
func encryptedTestKey(t *testing.T) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := gossh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("a passphrase"))
	if err != nil {
		t.Fatalf("marshal encrypted key: %v", err)
	}
	return pem.EncodeToMemory(block)
}

// writeTestKeyFile writes one fresh private key to a temp file and answers the
// path: a hop's credential has to be something the resolution can actually
// read, because a hop is resolved exactly as the destination is.
func writeTestKeyFile(t *testing.T) string {
	t.Helper()
	keyPEM, _ := generateTestKey(t)
	path := filepath.Join(t.TempDir(), "hop_key")
	if err := os.WriteFile(path, keyPEM, 0o600); err != nil {
		t.Fatalf("write hop key: %v", err)
	}
	return path
}

// storedKeyFixture is a real credential store holding one real private key: the
// point of the test below is that the PUBLIC half comes back and the private
// one does not, and a fake that answered a canned blob could not tell those
// apart.
type storedKeyFixture struct {
	store      credential.Resolver
	secretID   credential.SecretID
	publicBlob []byte
	privatePEM []byte
}

func newStoredKeyFixture(t *testing.T) storedKeyFixture {
	t.Helper()
	store := newTestStore()
	keyPEM, signer := generateTestKey(t)
	id, err := store.Create(context.Background(), credential.NewSecretBytes(keyPEM))
	if err != nil {
		t.Fatalf("store the key: %v", err)
	}
	return storedKeyFixture{
		store:      store,
		secretID:   id,
		publicBlob: signer.PublicKey().Marshal(),
		privatePEM: keyPEM,
	}
}

// TestResolveTargetReadsThePublicHalfOfAStoredKey: key auth is the case the
// wire can actually carry, and the public half is what makes it possible — the
// helper must be able to say which key it is offering before it is asked to
// sign, and it must never hold the private half.
func TestResolveTargetReadsThePublicHalfOfAStoredKey(t *testing.T) {
	rc := targetTestClient(t)
	key := newStoredKeyFixture(t)

	target, err := rc.ResolveTarget(context.Background(), "prod.example.com:22",
		WithUser("deploy"),
		WithKeySecretID(key.secretID),
		WithCredentials(key.store, key.secretID),
		// The profile's endpoint binding, which is the whole reason this
		// resolution is the coordinator's and not the helper's: a key linked
		// to one host may not be spent on another.
		WithAuthorizedEndpoint("prod.example.com"),
	)
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if target.Auth != DialAuthKey {
		t.Fatalf("auth = %q, want %q", target.Auth, DialAuthKey)
	}
	if target.Credential.Kind() != CredentialVault || target.Credential.ID() != string(key.secretID) {
		t.Fatalf("credential = %q, want the key's vault reference", target.Credential.String())
	}
	if len(target.PublicKey) == 0 {
		t.Fatal("a key target carries no public half, so the helper could not declare which key it offers")
	}
	if string(target.PublicKey) == string(key.privatePEM) {
		t.Fatal("the PRIVATE key was handed over as the public half")
	}
	if string(target.PublicKey) != string(key.publicBlob) {
		t.Fatalf("public half = %x, want the key's own wire blob %x", target.PublicKey, key.publicBlob)
	}
}
