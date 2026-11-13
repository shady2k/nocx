//go:build nocx_local_ssh

package ssh

// CREDENTIAL BINDING: a linked credential may be spent only on the endpoint
// its profile names.
//
// It is decided in RESOLUTION (resolveDialEndpoint), which is the entry the
// coordinator uses before a helper is handed a destination: the same check a
// coordinator-side dial used to run before its own dial, called through the
// same function (checkAuthorization). These tests therefore drive
// ResolveTarget and assert the DECISION — they dial nothing, so a passing test
// cannot be a connection that happened to work.

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
)

// credStub is a minimal SecretStore for binding tests: it reports a stored
// password so Secrets != nil (the gate for the binding check) without
// depending on the keyring.
type credStub struct{ pw string }

func (s *credStub) Create(_ context.Context, value credential.Secret) (credential.SecretID, error) {
	_ = value // binding tests don't persist; the store always returns s.pw
	return credential.SecretID("test-id"), nil
}

func (s *credStub) Get(_ context.Context, _ credential.SecretID) (credential.Secret, error) {
	return credential.NewSecret(s.pw), nil
}

func (s *credStub) Resolve(ctx context.Context, id credential.SecretID, _ credential.Stance) (credential.Secret, error) {
	return s.Get(ctx, id)
}
func (s *credStub) Delete(_ context.Context, _ credential.SecretID) error         { return nil }
func (s *credStub) Exists(_ context.Context, _ credential.SecretID) (bool, error) { return true, nil }

// newBindingClient builds a RealClient with an empty stub resolver so
// resolution is deterministic (alias lookups use the StubConfigResolver).
func newBindingClient(t *testing.T) *RealClient {
	t.Helper()
	c, err := NewReal(
		log.NewSlogAdapter(nil),
		WithConfigResolver(NewStubConfigResolver()),
		WithKnownHostsFile(writeSSHConfig(t, "")),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// unreachableHost names a host the resolver can parse. Nothing dials it — that
// is the point of testing resolution: the binding verdict cannot be a
// connection error wearing a different type.
const unreachableHost = "127.0.0.1:1"

// TestBinding_RefusesMismatchedHost proves the attack is stopped: a
// credential bound to host A, aimed at host B, is refused.
func TestBinding_RefusesMismatchedHost(t *testing.T) {
	c := newBindingClient(t)
	store := &credStub{pw: "victim-secret"}

	_, err := c.ResolveTarget(
		context.Background(), unreachableHost,
		WithUser("victim"),
		WithCredentials(store, credential.SecretID("test-id")),
		// Credential is bound to a different host than the one resolved.
		withBinding("good.example.com", 0),
	)

	var authErr *ErrCredentialAuthorizationFailed
	if !errors.As(err, &authErr) {
		t.Fatalf("want ErrCredentialAuthorizationFailed, got %T: %v", err, err)
	}
	if authErr.ResolvedHost != "127.0.0.1" {
		t.Errorf("ResolvedHost = %q, want 127.0.0.1 (the resolved host, not the alias)", authErr.ResolvedHost)
	}
	if authErr.Expected != "good.example.com" {
		t.Errorf("Expected = %q, want good.example.com", authErr.Expected)
	}
	if authErr.Jump {
		t.Error("Jump flag should be false for the target binding")
	}
}

// TestBinding_AliasResolves proves that a credential bound to an SSH alias is
// accepted when the alias resolves through ~/.ssh/config to the target the
// binding names. Under the computed-authorization redesign the identity is the
// profile's Host, which is an alias the user chose — the credential is
// authorized for the canonical hostname, so an alias resolves correctly.
func TestBinding_AliasResolves(t *testing.T) {
	stub := NewStubConfigResolver()
	stub.AddEntry("victim", HostConfig{HostName: "target.example.com", Port: 2222})

	client, err := NewReal(log.NewSlogAdapter(nil), WithConfigResolver(stub),
		WithKnownHostsFile(writeSSHConfig(t, "")))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	store := &credStub{pw: "x"}

	// Bound to the ALIAS — accepted. resolveAuthzEndpoint("victim") resolves
	// through the stub and resolveConfig("victim") resolves to the same
	// endpoint, so both sides match.
	if _, err := client.ResolveTarget(
		context.Background(), "victim",
		WithUser("test"),
		WithCredentials(store, credential.SecretID("test-id")),
		withBinding("victim", 0),
	); err != nil {
		t.Fatalf("alias-bound resolution: %v", err)
	}
}

// TestBinding_AliasDriftRefused proves that when ~/.ssh/config changes the
// HostName of an alias after the authorized endpoint was resolved, the
// connection is refused (drift detection). The accepted endpoint is the
// canonical hostname from the OLD resolution; the destination resolves through
// the NEW config, which yields a different host — the mismatch is detected and
// the credential is never offered.
func TestBinding_AliasDriftRefused(t *testing.T) {
	store := &credStub{pw: "x"}

	driftStub := NewStubConfigResolver()
	driftStub.AddEntry("victim", HostConfig{HostName: "evil.example.com", Port: 2222})
	client, err := NewReal(log.NewSlogAdapter(nil), WithConfigResolver(driftStub),
		WithKnownHostsFile(writeSSHConfig(t, "")))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	// The authorized endpoint is the value the OLD resolution produced; the
	// alias now resolves elsewhere, so the credential is not spent.
	_, err = client.ResolveTarget(
		context.Background(), "victim",
		WithUser("test"),
		WithCredentials(store, credential.SecretID("test-id")),
		func(c *ConnectConfig) { c.AuthorizedEndpoint = "target.example.com" },
	)
	var authErr *ErrCredentialAuthorizationFailed
	if !errors.As(err, &authErr) {
		t.Fatalf("drift: want ErrCredentialAuthorizationFailed, got %T: %v", err, err)
	}
	if authErr.Expected != "target.example.com" {
		t.Errorf("Expected = %q, want target.example.com (the old resolved endpoint)", authErr.Expected)
	}
	if authErr.ResolvedHost != "evil.example.com" {
		t.Errorf("ResolvedHost = %q, want evil.example.com (the new HostName after drift)", authErr.ResolvedHost)
	}
}

// TestBinding_JumpHostRefused pins the easier-to-miss path: a jump host's
// credential is resolved and enforced against the jump host's own resolved
// name, independently of the target. A jump credential bound to host A must be
// refused when the jump host resolves to host B.
func TestBinding_JumpHostRefused(t *testing.T) {
	stub := NewStubConfigResolver()
	stub.AddEntry("jumphost", HostConfig{HostName: "bastion.example.com", Port: 22})
	client, err := NewReal(log.NewSlogAdapter(nil), WithConfigResolver(stub),
		WithKnownHostsFile(writeSSHConfig(t, "")))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	store := &credStub{pw: "x"}

	_, err = client.ResolveTarget(
		context.Background(), "target.example.com",
		WithUser("test"),
		WithCredentials(store, credential.SecretID("test-id")),
		withBinding("target.example.com", 0),
		WithJumpHost("jumphost", 0, "test", "publicKey"),
		WithJumpCredentials(store, credential.SecretID("test-id")),
		func(c *ConnectConfig) {
			c.JumpAuthorizedEndpoint = "other-bastion.example.com"
		},
	)
	var authErr *ErrCredentialAuthorizationFailed
	if !errors.As(err, &authErr) {
		t.Fatalf("want ErrCredentialAuthorizationFailed for jump, got %T: %v", err, err)
	}
	if !authErr.Jump {
		t.Error("Jump flag should be true for a jump-credential binding failure")
	}
	if authErr.ResolvedHost != "bastion.example.com" {
		t.Errorf("ResolvedHost = %q, want bastion.example.com (the jump alias's resolved HostName)", authErr.ResolvedHost)
	}
	if authErr.Expected != "other-bastion.example.com" {
		t.Errorf("Expected = %q, want other-bastion.example.com", authErr.Expected)
	}
}

func TestBinding_PortFromAlias(t *testing.T) {
	stub := NewStubConfigResolver()
	stub.AddEntry("portalias", HostConfig{HostName: "target.example.com", Port: 2222})
	client, err := NewReal(log.NewSlogAdapter(nil), WithConfigResolver(stub),
		WithKnownHostsFile(writeSSHConfig(t, "")))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	store := &credStub{pw: "x"}

	// Bound to the right host but port 22 — the alias resolves to 2222. A
	// binding that names a port is a stricter claim than the endpoint, and it
	// must be refused.
	_, err = client.ResolveTarget(
		context.Background(), "portalias",
		WithUser("test"),
		WithCredentials(store, credential.SecretID("test-id")),
		withBinding("target.example.com", 22),
	)
	var authErr *ErrCredentialAuthorizationFailed
	if !errors.As(err, &authErr) {
		t.Fatalf("port mismatch: want ErrCredentialAuthorizationFailed, got %T: %v", err, err)
	}
	if authErr.Expected != "target.example.com:22" {
		t.Errorf("Expected = %q, want target.example.com:22", authErr.Expected)
	}
	if authErr.ResolvedPort == 22 {
		t.Error("ResolvedPort = 22, but the alias overrides Port to 2222")
	}
}

func TestBinding_UnboundRefused(t *testing.T) {
	c := newBindingClient(t)
	store := &credStub{pw: "x"}
	secretID := credential.SecretID("test-id")

	_, err := c.ResolveTarget(
		context.Background(), unreachableHost,
		WithUser("victim"),
		WithCredentials(store, secretID),
	)
	var authErr *ErrCredentialAuthorizationFailed
	if !errors.As(err, &authErr) {
		t.Fatalf("want ErrCredentialAuthorizationFailed, got %T: %v", err, err)
	}
	if authErr.Expected != "<none>" {
		t.Errorf("Expected = %q, want \"<none>\" for unbound credential", authErr.Expected)
	}
	if authErr.ResolvedHost != "127.0.0.1" {
		t.Errorf("ResolvedHost = %q, want 127.0.0.1", authErr.ResolvedHost)
	}
	if authErr.CredentialID != string(secretID) {
		t.Errorf("CredentialID = %q, want %q", authErr.CredentialID, secretID)
	}
}

func TestBinding_HostAnyPortWhenPortUnset(t *testing.T) {
	client, err := NewReal(log.NewSlogAdapter(nil), WithConfigResolver(NewStubConfigResolver()),
		WithKnownHostsFile(writeSSHConfig(t, "")))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	store := &credStub{pw: "x"}

	// A binding that names no port covers every port of that host: the profile
	// said "this machine", and an ephemeral test port is still that machine.
	if _, err := client.ResolveTarget(
		context.Background(), "127.0.0.1:2222",
		WithUser("test"),
		WithCredentials(store, credential.SecretID("test-id")),
		withBinding("127.0.0.1", 0),
	); err != nil {
		t.Fatalf("host-only binding: %v", err)
	}
}

func TestBinding_InlineAuthNotChecked(t *testing.T) {
	client, err := NewReal(log.NewSlogAdapter(nil), WithConfigResolver(NewStubConfigResolver()),
		WithKnownHostsFile(writeSSHConfig(t, "")))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Inline auth has no linked credential and so nothing to bind: only a
	// stored secret names an endpoint a credential may be spent on. The key is
	// a real one, because resolution reads and parses every inline key it is
	// given (it never hands the bytes on).
	keyPEM, _ := generateTestKey(t)
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if _, err := client.ResolveTarget(
		context.Background(), "127.0.0.1:2222",
		WithUser("test"),
		WithKeyFile(keyPath),
	); err != nil {
		t.Fatalf("inline resolution: %v", err)
	}
}

// withBinding sets the credential binding on a ConnectConfig. It is a test
// helper rather than a public With option because binding is resolver-owned
// data, not something callers set directly in production.
func withBinding(host string, port int) ConnectOption {
	return func(c *ConnectConfig) {
		if port == 0 {
			c.AuthorizedEndpoint = host
		} else {
			c.AuthorizedEndpoint = net.JoinHostPort(host, strconv.Itoa(port))
		}
	}
}
