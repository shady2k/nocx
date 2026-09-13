package ssh

// The destination a helper dials, resolved by the coordinator (nocx-50w7p.3).
//
// This is the seam that keeps the owner's invariant honest in both directions:
// the helper is handed an address and a REFERENCE, and the coordinator's own
// rules — alias resolution, the authorization of a linked credential against
// the endpoint its profile names — still run. A test that only proved the happy
// path would leave the two refusals (a route it cannot dial, a credential it
// cannot hand over) as sentences nobody has ever seen produced.

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
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
	if target.Credential != credential.SecretID("secret-7") {
		t.Fatalf("credential = %q, want the reference the caller named", target.Credential)
	}
	if len(target.PublicKey) != 0 {
		t.Fatalf("a password target carries a public key (%d bytes)", len(target.PublicKey))
	}
}

// TestResolveTargetRefusesARouteItCannotDial: a bastioned destination is a
// refusal BY NAME and never a direct dial. The difference matters because a
// direct dial to a host that is only reachable through a bastion fails as an
// unreachable host — which reads as a network problem rather than as the
// missing feature it is.
func TestResolveTargetRefusesARouteItCannotDial(t *testing.T) {
	rc := targetTestClient(t)

	_, err := rc.ResolveTarget(context.Background(), "prod.example.com",
		WithUser("deploy"),
		WithCredentials(nil, credential.SecretID("secret-7")),
		WithJumpHost("bastion.example.com", 22, "jump", "publicKey"),
	)
	if !errors.Is(err, ErrRoutedDial) {
		t.Fatalf("ResolveTarget through a jump host = %v, want ErrRoutedDial", err)
	}
}

// TestResolveTargetRefusesACredentialItCannotHandOver covers the three shapes a
// helper cannot be given today — an inline key file, the agent, and the prompt
// rung — in one test because the caller's next act is the same for all three:
// report which credential could not be offered.
//
// It is the assertion that stops the silent fallback: if any of these started
// answering a target, this process would have kept a dial path for exactly the
// connections nobody was looking at.
func TestResolveTargetRefusesACredentialItCannotHandOver(t *testing.T) {
	cases := []struct {
		name string
		opts []ConnectOption
	}{
		{
			name: "an inline key file",
			opts: []ConnectOption{WithUser("deploy"), WithKeyFile("/home/u/.ssh/id_ed25519")},
		},
		{
			name: "the agent",
			opts: []ConnectOption{WithUser("deploy"), WithAgent()},
		},
		{
			name: "the prompt rung",
			opts: []ConnectOption{WithUser("deploy"), WithAuthMode("keyboardInteractive")},
		},
		{
			name: "no credential at all",
			opts: []ConnectOption{WithUser("deploy")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rc := targetTestClient(t)
			_, err := rc.ResolveTarget(context.Background(), "prod.example.com", tc.opts...)
			if !errors.Is(err, ErrNoHelperIdentity) {
				t.Fatalf("ResolveTarget with %s = %v, want ErrNoHelperIdentity", tc.name, err)
			}
		})
	}
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
	if target.Credential != key.secretID {
		t.Fatalf("credential = %q, want the key's reference", target.Credential)
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
