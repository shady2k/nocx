//go:build nocx_local_ssh

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
	if len(target.Keys) != 0 {
		t.Fatalf("a password target offers %d key(s); its material is the credential", len(target.Keys))
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
// It replaces a test that asserted the OPPOSITE — that a routed destination was
// REFUSED, by a sentinel this change deleted — and the shape of the replacement
// is the point: a route is no longer a refusal, it is a list of endpoints, and
// what a helper must be able to see is which account and which credential
// belongs to each one.
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
	if len(target.Keys) != 1 {
		t.Fatalf("a named key file offers %d key(s), want exactly the one the profile named", len(target.Keys))
	}
	if got, want := target.Keys[0].Credential.String(), "file:"+path; got != want {
		t.Fatalf("credential = %q, want %q", got, want)
	}
	if string(target.Keys[0].PublicKey) != string(signer.PublicKey().Marshal()) {
		t.Fatalf("public half = %x, want the key's own wire blob", target.Keys[0].PublicKey)
	}
	if string(target.Keys[0].PublicKey) == string(keyPEM) {
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
	if len(target.Keys) != 1 {
		t.Fatalf("an agent holding one key offers %d key(s)", len(target.Keys))
	}
	offer := target.Keys[0]
	if got := offer.Credential.Kind(); got != CredentialAgent {
		t.Fatalf("credential kind = %q, want %q", got, CredentialAgent)
	}
	if len(offer.PublicKey) == 0 {
		t.Fatal("an agent identity carries no public half, so the helper could not declare which key it offers")
	}
	if want := gossh.FingerprintSHA256(held); offer.Credential.ID() != want {
		t.Fatalf("agent reference = %q, want the fingerprint of the key the agent holds (%q)", offer.Credential.ID(), want)
	}
	if string(offer.PublicKey) != string(held.Marshal()) {
		t.Fatalf("public half = %x, want the agent key's own wire blob", offer.PublicKey)
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
		WithUser("deploy"), WithAuthMode("keyboardInteractive"), WithPasswordRequester(&countingAsker{}))
	if err != nil {
		t.Fatalf("ResolveTarget with a requester: %v", err)
	}
	if target.Auth != DialAuthInteractive {
		t.Fatalf("auth = %q, want %q", target.Auth, DialAuthInteractive)
	}
	if !target.Credential.IsZero() || len(target.Keys) != 0 {
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

// countingAsker is the prompt seam a connection carries when a person can be
// asked, and it counts the asks. It answers nothing — resolution never asks, and
// a test that did would be asserting on a dialog rather than on a destination —
// which is why the count is the observable: "a locked default key did not ask
// for a passphrase" is a claim about a question that was never put to anybody.
type countingAsker struct{ asks int }

func (a *countingAsker) RequestConnectionPassword(context.Context, PasswordRequest) (PasswordAnswer, error) {
	a.asks++
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
	if len(target.Keys) != 1 || target.Keys[0].Credential.Kind() != CredentialVault ||
		target.Keys[0].Credential.ID() != string(key.secretID) {
		t.Fatalf("credential = %q, want the key's vault reference", target.Keys)
	}
	if len(target.Keys[0].PublicKey) == 0 {
		t.Fatal("a key target carries no public half, so the helper could not declare which key it offers")
	}
	if string(target.Keys[0].PublicKey) == string(key.privatePEM) {
		t.Fatal("the PRIVATE key was handed over as the public half")
	}
	if string(target.Keys[0].PublicKey) != string(key.publicBlob) {
		t.Fatalf("public half = %x, want the key's own wire blob %x", target.Keys[0].PublicKey, key.publicBlob)
	}
}

// ── default key discovery and an agent's several keys (nocx-50w7p.19) ──────
//
// Two ordinary setups were refusals through the helper before these cases. A
// profile that names NO credential is "connect to this host with my keys", and
// OpenSSH answers it by offering what it finds: the agent's keys and the
// identity files its configuration lists — ssh's own default list when the
// configuration lists none. An agent holding SEVERAL keys is the same shape
// underneath, because only one of them is the key a given host accepts, and not
// necessarily the first.
//
// Both are asserted on the resolved destination, because that is the seam where
// this process decides what a helper may offer and in what order. Everything
// below is about the QUEUE, its order, and the refusals that pair with it.

// resolvingHostFor answers a client whose resolver knows exactly ONE host: the
// identity files ssh's configuration would list for it, in that order.
//
// The list is the resolver's answer rather than a field of the client, which is
// the point of the fixture — discovery reads the same oracle every other
// directive comes from, so what a test hands in here is what ssh -G answered.
func resolvingHostFor(t *testing.T, host string, files ...string) *RealClient {
	t.Helper()
	resolver := NewStubConfigResolver()
	resolver.AddEntry(host, HostConfig{
		HostName: host, User: "deploy", Port: 22, IdentityFiles: files,
	})
	rc, err := NewReal(log.NewSlogAdapter(nil), WithConfigResolver(resolver))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })
	return rc
}

// identityFileKey writes one real private key into a file with the given name —
// the name ssh itself looks for — and answers its path and signer.
func identityFileKey(t *testing.T, name string) (string, gossh.Signer) {
	t.Helper()
	keyPEM, signer := generateTestKey(t)
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, keyPEM, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path, signer
}

// lockedKey is an encrypted private key file WITH the `.pub` ssh-keygen would
// write beside it: the state a default key is in when it can be OFFERED and
// cannot be signed with here, because `ssh-add` loaded it into an agent once and
// the file on disk stayed locked.
//
// The private half is kept because the fixtures below need to put exactly this
// key into an agent, and the public half because that is what the coordinator
// must still be able to declare.
type lockedKey struct {
	path    string
	public  gossh.PublicKey
	private ed25519.PrivateKey
}

func lockedIdentityFile(t *testing.T, name string) lockedKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	block, err := gossh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("a passphrase"))
	if err != nil {
		t.Fatalf("marshal encrypted key: %v", err)
	}
	path := filepath.Join(t.TempDir(), name)
	if wErr := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); wErr != nil {
		t.Fatalf("write %s: %v", name, wErr)
	}
	if wErr := os.WriteFile(path+".pub", gossh.MarshalAuthorizedKey(signer.PublicKey()), 0o600); wErr != nil {
		t.Fatalf("write %s.pub: %v", name, wErr)
	}
	return lockedKey{path: path, public: signer.PublicKey(), private: priv}
}

// freshAgentKeys generates n private keys in the order they will be added to an
// agent, which is the order the agent will list them in.
func freshAgentKeys(t *testing.T, n int) []any {
	t.Helper()
	keys := make([]any, 0, n)
	for i := range n {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("generate key %d: %v", i, err)
		}
		keys = append(keys, priv)
	}
	return keys
}

// keyringAgent starts a real ssh-agent holding the given keys, in that order,
// and points SSH_AUTH_SOCK at it — so what is enumerated below is a real agent's
// own listing rather than a slice this test built.
func keyringAgent(t *testing.T, keys ...any) []gossh.PublicKey {
	t.Helper()
	keyring := agent.NewKeyring()
	public := make([]gossh.PublicKey, 0, len(keys))
	for i, key := range keys {
		signer, err := gossh.NewSignerFromKey(key)
		if err != nil {
			t.Fatalf("signer %d: %v", i, err)
		}
		if addErr := keyring.Add(agent.AddedKey{PrivateKey: key, Comment: "nocx-test"}); addErr != nil {
			t.Fatalf("add key %d to the agent: %v", i, addErr)
		}
		public = append(public, signer.PublicKey())
	}
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_ = agent.ServeAgent(keyring, conn)
			}()
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)
	return public
}

// assertOffered compares a destination's key queue against the expected
// description, entry for entry.
//
// The comparison is by ORDER and not by set: a queue in the wrong order is a
// connection that offers the host's key after offering others, which is not the
// same connection. Each entry is decoded into a public key on the way in, so a
// value that is not a public half cannot pass this check at all — which is how
// "the private bytes never cross" is asserted here rather than by a substring
// search.
func assertOffered(t *testing.T, what string, target DialTarget, want []string) {
	t.Helper()
	got := make([]string, 0, len(target.Keys))
	for i, key := range target.Keys {
		pub, err := gossh.ParsePublicKey(key.PublicKey)
		if err != nil {
			t.Fatalf("%s: key %d carries a public half that does not parse: %v", what, i, err)
		}
		got = append(got, string(key.Credential.Kind())+":"+key.Credential.ID()+"="+gossh.FingerprintSHA256(pub))
	}
	if len(got) != len(want) {
		t.Fatalf("%s offers %d key(s) %v, want %d %v", what, len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s offers %v, want %v (first difference at %d)", what, got, want, i)
		}
	}
}

// agentOffer is the description of one agent key: the reference and the public
// half are the same fingerprint, because that IS the reference for an agent key.
func agentOffer(pub gossh.PublicKey) string {
	fp := gossh.FingerprintSHA256(pub)
	return "agent:" + fp + "=" + fp
}

// fileOffer is the same for an identity file.
func fileOffer(path string, pub gossh.PublicKey) string {
	return "file:" + path + "=" + gossh.FingerprintSHA256(pub)
}

// TestResolveTargetDiscoversTheDefaultKeysInTheResolversOrder is the first half
// of the criterion: a profile that names no credential resolves to the identity
// files ssh's own configuration lists, in that order, each carrying a reference
// this process signs through and the public half the helper must declare.
//
// The MISSING file in the middle is the other half of the same rule: ssh offers
// the next key rather than failing, and a home directory holding one default key
// and not the others is the ordinary case rather than a broken one.
func TestResolveTargetDiscoversTheDefaultKeysInTheResolversOrder(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")

	rsaPath, rsaSigner := identityFileKey(t, "id_rsa")
	absent := filepath.Join(t.TempDir(), "id_ecdsa")
	edPath, edSigner := identityFileKey(t, "id_ed25519")

	rc := resolvingHostFor(t, "prod.example.com", rsaPath, absent, edPath)
	target, err := rc.ResolveTarget(context.Background(), "prod.example.com",
		WithUser("deploy"), WithoutPasswordPrompt())
	if err != nil {
		t.Fatalf("ResolveTarget with no credential named: %v", err)
	}
	if target.Auth != DialAuthKey {
		t.Fatalf("auth = %q, want %q: a profile naming nothing offers the keys this machine has", target.Auth, DialAuthKey)
	}
	assertOffered(t, "the discovered destination", target, []string{
		fileOffer(rsaPath, rsaSigner.PublicKey()),
		fileOffer(edPath, edSigner.PublicKey()),
	})
}

// TestResolveTargetFallsThroughALockedDefaultKey is the paired case for the one
// above, and it is where a locked key belongs: ssh offers the next key and does
// NOT stop to ask for a passphrase a profile never named. So a default key that
// is encrypted resolves PAST it — the connection carries a requester and is
// still never asked anything.
func TestResolveTargetFallsThroughALockedDefaultKey(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")

	locked := lockedIdentityFile(t, "id_rsa")
	openPath, openSigner := identityFileKey(t, "id_ed25519")

	rc := resolvingHostFor(t, "prod.example.com", locked.path, openPath)
	asker := &countingAsker{}
	target, err := rc.ResolveTarget(context.Background(), "prod.example.com",
		WithUser("deploy"), WithPasswordRequester(asker))
	if err != nil {
		t.Fatalf("ResolveTarget with a locked first key: %v", err)
	}
	assertOffered(t, "the destination behind a locked key", target, []string{
		fileOffer(openPath, openSigner.PublicKey()),
	})
	if asker.asks != 0 {
		t.Fatalf("a locked default key raised %d prompt(s); a profile that never named a passphrase must not ask for one", asker.asks)
	}
}

// TestResolveTargetRefusesALockedKeyWhenItIsTheOnlyOne takes the same rule to its
// end: a home whose only default key is encrypted, with no agent to sign for it,
// has NOTHING to offer — and the locked key itself asks nobody anything.
//
// A probe therefore declines by NAME, and a connection that can ask a person
// reaches the ladder's ordinary prompt rung instead: what it may then ask is the
// SERVER's question, not a passphrase this process invented for a key the profile
// never named. Asking for that passphrase would be a credential nobody described,
// and it is the state a person is in after `ssh-add -D` on an encrypted key.
func TestResolveTargetRefusesALockedKeyWhenItIsTheOnlyOne(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")

	locked := lockedIdentityFile(t, "id_ed25519")
	rc := resolvingHostFor(t, "prod.example.com", locked.path)

	_, err := rc.ResolveTarget(context.Background(), "prod.example.com",
		WithUser("deploy"), WithoutPasswordPrompt())
	var noMethod *ErrNoAuthMethod
	if !errors.As(err, &noMethod) {
		t.Fatalf("a probe whose only key is locked = %v (%T), want *ErrNoAuthMethod", err, err)
	}

	asker := &countingAsker{}
	target, err := rc.ResolveTarget(context.Background(), "prod.example.com",
		WithUser("deploy"), WithPasswordRequester(asker))
	if err != nil {
		t.Fatalf("ResolveTarget with a locked key and a person available: %v", err)
	}
	if target.Auth != DialAuthInteractive {
		t.Fatalf("auth = %q, want %q", target.Auth, DialAuthInteractive)
	}
	if asker.asks != 0 {
		t.Fatalf("the locked key raised %d ask(s) during resolution; a passphrase no profile named is not asked for", asker.asks)
	}
}

// TestResolveTargetOffersEveryKeyTheAgentHolds is the multi-key half of the
// criterion at the resolution seam: an agent holding three keys resolves to three
// offered keys, in the agent's own order, each naming its own fingerprint.
//
// The end-to-end half — that a host accepting only a LATER key authenticates — is
// the helper's own test against a real ssh server. This one proves the queue
// leaves here whole and in order, which is the half that has to be true first.
func TestResolveTargetOffersEveryKeyTheAgentHolds(t *testing.T) {
	held := keyringAgent(t, freshAgentKeys(t, 3)...)
	if len(held) != 3 {
		t.Fatalf("the fixture's agent holds %d key(s), want 3", len(held))
	}

	rc := targetTestClient(t)
	target, err := rc.ResolveTarget(context.Background(), "prod.example.com",
		WithUser("deploy"), WithAuthMode("agent"))
	if err != nil {
		t.Fatalf("ResolveTarget with an agent holding several keys: %v", err)
	}
	assertOffered(t, "an agent with three keys", target, []string{
		agentOffer(held[0]), agentOffer(held[1]), agentOffer(held[2]),
	})
}

// TestResolveTargetOffersTheAgentsKeysBeforeTheFiles is OpenSSH's own queue,
// collapsed to the two arms this package has: a key the agent holds is offered
// through the agent, and the identity files the configuration lists follow it.
func TestResolveTargetOffersTheAgentsKeysBeforeTheFiles(t *testing.T) {
	held := keyringAgent(t, freshAgentKeys(t, 1)...)
	filePath, fileSigner := identityFileKey(t, "id_ed25519")

	rc := resolvingHostFor(t, "prod.example.com", filePath)
	target, err := rc.ResolveTarget(context.Background(), "prod.example.com", WithUser("deploy"))
	if err != nil {
		t.Fatalf("ResolveTarget with an agent and a default key file: %v", err)
	}
	assertOffered(t, "an agent key beside an identity file", target, []string{
		agentOffer(held[0]),
		fileOffer(filePath, fileSigner.PublicKey()),
	})
}

// TestResolveTargetHonoursIdentitiesOnly is the directive's own case, and it is
// the one that must not be implemented by switching the agent off.
//
// The setup is why the directive exists: an agent holding two keys, of which the
// configuration names one — and that one is ENCRYPTED on disk, so the only party
// that can sign for it is the agent. Dropping the agent's keys wholesale would
// refuse this connection; the answer is the agent's copy of the key the
// configuration names, and nothing else.
func TestResolveTargetHonoursIdentitiesOnly(t *testing.T) {
	named := lockedIdentityFile(t, "id_ed25519")
	unnamed, err := gossh.NewSignerFromKey(freshAgentKeys(t, 1)[0])
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	held := keyringAgent(t, named.private, freshAgentKeys(t, 1)[0])
	if gossh.FingerprintSHA256(held[0]) != gossh.FingerprintSHA256(named.public) {
		t.Fatalf("the agent's first key is not the one the locked file names")
	}

	resolver := NewStubConfigResolver()
	resolver.AddEntry("prod.example.com", HostConfig{
		HostName: "prod.example.com", User: "deploy", Port: 22,
		IdentityFiles: []string{named.path}, IdentitiesOnly: true,
	})
	strict, err := NewReal(log.NewSlogAdapter(nil), WithConfigResolver(resolver))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	t.Cleanup(func() { _ = strict.Close() })

	target, err := strict.ResolveTarget(context.Background(), "prod.example.com", WithUser("deploy"))
	if err != nil {
		t.Fatalf("ResolveTarget under IdentitiesOnly: %v", err)
	}
	assertOffered(t, "IdentitiesOnly with one named key", target, []string{
		agentOffer(held[0]),
	})
	if gossh.FingerprintSHA256(unnamed.PublicKey()) == gossh.FingerprintSHA256(held[1]) {
		t.Fatal("the fixture built one key twice; the suppression below would prove nothing")
	}

	// The same destination WITHOUT the directive offers the agent's other key
	// too, which is exactly what the directive exists to suppress.
	loose := resolvingHostFor(t, "prod.example.com", named.path)
	target, err = loose.ResolveTarget(context.Background(), "prod.example.com", WithUser("deploy"))
	if err != nil {
		t.Fatalf("ResolveTarget without IdentitiesOnly: %v", err)
	}
	assertOffered(t, "the same destination without IdentitiesOnly", target, []string{
		agentOffer(held[0]), agentOffer(held[1]),
	})
}

// TestResolveTargetRefusesWhenNoDefaultKeyIsPresent is the paired refusal: a home
// directory with no key ssh would offer, and no agent, is ErrNoAuthMethod with no
// mode — nothing was named and nothing was found — and it is NOT a dial with no
// method. The same connection with a person who can be asked still reaches the
// prompt rung, which is the ladder's last resort rather than a second attempt at
// the key.
func TestResolveTargetRefusesWhenNoDefaultKeyIsPresent(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")

	rc := resolvingHostFor(t, "prod.example.com",
		filepath.Join(t.TempDir(), "id_rsa"),
		filepath.Join(t.TempDir(), "id_ed25519"),
	)

	_, err := rc.ResolveTarget(context.Background(), "prod.example.com",
		WithUser("deploy"), WithoutPasswordPrompt())
	var noMethod *ErrNoAuthMethod
	if !errors.As(err, &noMethod) {
		t.Fatalf("a destination with nothing to offer = %v (%T), want *ErrNoAuthMethod", err, err)
	}

	asked, err := rc.ResolveTarget(context.Background(), "prod.example.com",
		WithUser("deploy"), WithPasswordRequester(&countingAsker{}))
	if err != nil {
		t.Fatalf("a destination with nothing to offer but a person to ask: %v", err)
	}
	if asked.Auth != DialAuthInteractive {
		t.Fatalf("auth = %q, want %q: the prompt rung is the ladder's last resort", asked.Auth, DialAuthInteractive)
	}
}

// TestResolveTargetDoesNotRekeyAProfileThatBindsAPassword guards the discovery
// arm's boundary: a connection whose profile binds a stored password resolves to
// that PASSWORD even with a default key on disk and a key in the agent.
//
// "Nothing was declared" is what the discovery arm means, and this is what
// "nothing" has to keep meaning. Those default key paths come before the password
// rung in the ladder, so a discovery that ran one arm earlier would silently
// re-key every profile that binds a password to whichever key happens to sit in
// the person's home.
func TestResolveTargetDoesNotRekeyAProfileThatBindsAPassword(t *testing.T) {
	keyringAgent(t, freshAgentKeys(t, 1)...)
	filePath, _ := identityFileKey(t, "id_ed25519")

	rc := resolvingHostFor(t, "prod.example.com", filePath)
	target, err := rc.ResolveTarget(context.Background(), "prod.example.com",
		WithUser("deploy"),
		WithCredentials(nil, credential.SecretID("written-down-password")),
	)
	if err != nil {
		t.Fatalf("ResolveTarget with a stored password: %v", err)
	}
	if target.Auth != DialAuthPassword {
		t.Fatalf("auth = %q, want %q: a stored password is what the profile declared", target.Auth, DialAuthPassword)
	}
	if len(target.Keys) != 0 {
		t.Fatalf("the destination offers %v; a profile that binds a password is not dialed with a key", target.Keys)
	}
	if got, want := target.Credential.String(), "vault:written-down-password"; got != want {
		t.Fatalf("credential = %q, want %q", got, want)
	}
}
