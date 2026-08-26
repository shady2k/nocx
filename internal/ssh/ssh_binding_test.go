package ssh

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	gossh "golang.org/x/crypto/ssh"
)

// credStub is a minimal SecretStore for binding tests: it reports a
// stored password so Secrets != nil (the gate for the binding check)
// without depending on the keyring.
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

// unreachableHost is a host:port that refuses connections quickly, used to
// prove the binding check fires BEFORE any dial: a mismatched binding against
// this host returns a binding error, not a connection error.
const unreachableHost = "127.0.0.1:1"

// TestBinding_RefusesMismatchedHost proves the attack is stopped: a
// credential bound to host A, aimed at host B, is refused. The target here
// is unreachable, so the only way to get a binding error instead of a dial
// error is that the check runs before the dial.
func TestBinding_RefusesMismatchedHost(t *testing.T) {
	c := newBindingClient(t)
	store := &credStub{pw: "victim-secret"}

	_, err := c.Connect(
		context.Background(), unreachableHost,
		WithUser("victim"),
		WithCredentials(store, credential.SecretID("test-id")),
		// Credential is bound to a different host than the one we dial.
		withBinding("good.example.com", 0),
	)

	var authErr *ErrCredentialAuthorizationFailed
	if !errors.As(err, &authErr) {
		t.Fatalf("want ErrCredentialAuthorizationFailed, got %T: %v", err, err)
	}
	if authErr.ResolvedHost != "127.0.0.1" {
		t.Errorf("ResolvedHost = %q, want 127.0.0.1 (the dialed host, not the alias)", authErr.ResolvedHost)
	}
	if authErr.Expected != "good.example.com" {
		t.Errorf("Expected = %q, want good.example.com", authErr.Expected)
	}
	if authErr.Jump {
		t.Error("Jump flag should be false for the target binding")
	}
}

// TestBinding_AliasConnects proves that a credential bound to an SSH alias
// CONNECTS when the alias resolves through ~/.ssh/config to the same target
// as the dial endpoint. Under the computed-authorization redesign the identity
// is the profile's Host, which is an alias the user chose — the credential
// is authorized for the canonical hostname, so an alias resolves correctly.
func TestBinding_AliasConnects(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()
	_, portStr, _ := net.SplitHostPort(srv.addr)
	srvHost := hostPortOnly(srv.addr)
	srvPort, _ := strconv.Atoi(portStr)

	stub := NewStubConfigResolver()
	stub.AddEntry("victim", HostConfig{HostName: srvHost, Port: srvPort})
	khPath := writeKnownHosts(t, srv, srv.addr)

	client, err := NewReal(log.NewSlogAdapter(nil), WithConfigResolver(stub), WithKnownHostsFile(khPath))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	store := &credStub{pw: "x"}

	// Bound to the ALIAS — must CONNECT. resolveAuthzEndpoint("victim")
	// resolves through the stub to srvHost, and resolveConfig("victim")
	// also resolves to srvHost. Both sides match.
	ch, err := client.Connect(
		context.Background(), "victim",
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{gossh.PublicKeys(srv.userSigner)}),
		WithCredentials(store, credential.SecretID("test-id")),
		withBinding("victim", 0),
	)
	if err != nil {
		t.Fatalf("alias-bound: Connect: %v", err)
	}
	_ = ch.Close()
}

// TestBinding_AliasDriftRefused proves that when ~/.ssh/config changes the
// HostName of an alias after the authorized endpoint was resolved, the
// connection is refused (drift detection). The authorized endpoint is the
// canonical hostname from the OLD resolution; the dial target resolves
// through the NEW config, which yields a different host — the mismatch is
// detected and the credential is not submitted.
func TestBinding_AliasDriftRefused(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()
	_, portStr, _ := net.SplitHostPort(srv.addr)
	srvHost := hostPortOnly(srv.addr)
	srvPort, _ := strconv.Atoi(portStr)

	khPath := writeKnownHosts(t, srv, srv.addr)
	store := &credStub{pw: "x"}

	// Old config: alias "victim" → HostName srvHost (the test server).
	oldStub := NewStubConfigResolver()
	oldStub.AddEntry("victim", HostConfig{HostName: srvHost, Port: srvPort})
	client, err := NewReal(log.NewSlogAdapter(nil), WithConfigResolver(oldStub), WithKnownHostsFile(khPath))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Connect with AuthorizedEndpoint set to the OLD resolved value (srvHost).
	// resolveAuthzEndpoint("127.0.0.1") is a no-op (IP, not an alias).
	// resolveConfig("victim") → hostName = "127.0.0.1" (from old stub).
	// Match → connect.
	_, err = client.Connect(
		context.Background(), "victim",
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{gossh.PublicKeys(srv.userSigner)}),
		WithCredentials(store, credential.SecretID("test-id")),
		func(c *ConnectConfig) { c.AuthorizedEndpoint = srvHost },
	)
	if err != nil {
		t.Fatalf("connect with old resolved endpoint: %v", err)
	}

	// Now the config drifts: alias "victim" → HostName "evil.example.com".
	driftStub := NewStubConfigResolver()
	driftStub.AddEntry("victim", HostConfig{HostName: "evil.example.com", Port: srvPort})
	client2, err := NewReal(log.NewSlogAdapter(nil), WithConfigResolver(driftStub), WithKnownHostsFile(khPath))
	if err != nil {
		t.Fatalf("NewReal 2: %v", err)
	}
	defer func() { _ = client2.Close() }()

	// The authorized endpoint is still "127.0.0.1" (the old resolved value).
	// resolveAuthzEndpoint("127.0.0.1") → "127.0.0.1" (no-op).
	// resolveConfig("victim").hostName → "evil.example.com" (from drift stub).
	// "127.0.0.1" != "evil.example.com" → drifts → REFUSED.
	_, err = client2.Connect(
		context.Background(), "victim",
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{gossh.PublicKeys(srv.userSigner)}),
		WithCredentials(store, credential.SecretID("test-id")),
		func(c *ConnectConfig) { c.AuthorizedEndpoint = srvHost },
	)
	var authErr *ErrCredentialAuthorizationFailed
	if !errors.As(err, &authErr) {
		t.Fatalf("drift: want ErrCredentialAuthorizationFailed, got %T: %v", err, err)
	}
	if authErr.Expected != srvHost {
		t.Errorf("Expected = %q, want %q (the old resolved endpoint)", authErr.Expected, srvHost)
	}
	if authErr.ResolvedHost != "evil.example.com" {
		t.Errorf("ResolvedHost = %q, want evil.example.com (the new HostName after drift)", authErr.ResolvedHost)
	}
}

// TestBinding_JumpHostRefused pins the easier-to-miss path: JumpCredentials
// resolves separately and is enforced against the jump host's resolved name,
// independently of the target. A jump credential bound to host A must be
// refused when the jump host resolves to host B.
func TestBinding_JumpHostRefused(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()
	_, portStr, _ := net.SplitHostPort(srv.addr)
	srvPort, _ := strconv.Atoi(portStr)

	khPath := writeKnownHosts(t, srv, srv.addr)

	// Jump alias "jumphost" -> HostName 127.0.0.1 (the test server).
	stub := NewStubConfigResolver()
	stub.AddEntry("jumphost", HostConfig{HostName: hostPortOnly(srv.addr), Port: srvPort})
	client, err := NewReal(log.NewSlogAdapter(nil), WithConfigResolver(stub), WithKnownHostsFile(khPath))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	store := &credStub{pw: "x"}

	_, err = client.Connect(
		context.Background(), srv.addr,
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{gossh.PublicKeys(srv.userSigner)}),
		WithCredentials(store, credential.SecretID("test-id")),
		withBinding(hostPortOnly(srv.addr), 0),
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
	if authErr.ResolvedHost != hostPortOnly(srv.addr) {
		t.Errorf("ResolvedHost = %q, want %s (the jump alias's resolved HostName)", authErr.ResolvedHost, hostPortOnly(srv.addr))
	}
	if authErr.Expected != "other-bastion.example.com" {
		t.Errorf("Expected = %q, want other-bastion.example.com", authErr.Expected)
	}
}

func TestBinding_PortFromAlias(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()
	_, portStr, _ := net.SplitHostPort(srv.addr)
	srvPort, _ := strconv.Atoi(portStr)

	khPath := writeKnownHosts(t, srv, srv.addr)

	stub := NewStubConfigResolver()
	stub.AddEntry("portalias", HostConfig{HostName: hostPortOnly(srv.addr), Port: srvPort})
	client, err := NewReal(log.NewSlogAdapter(nil), WithConfigResolver(stub), WithKnownHostsFile(khPath))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	store := &credStub{pw: "x"}

	// Bound to the right host but port 22 — the alias resolves to srv port,
	// which is not 22 (it's an ephemeral port). Must be refused.
	_, err = client.Connect(
		context.Background(), "portalias",
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{gossh.PublicKeys(srv.userSigner)}),
		WithCredentials(store, credential.SecretID("test-id")),
		withBinding(hostPortOnly(srv.addr), 22),
	)
	var authErr *ErrCredentialAuthorizationFailed
	if !errors.As(err, &authErr) {
		t.Fatalf("port mismatch: want ErrCredentialAuthorizationFailed, got %T: %v", err, err)
	}
	if authErr.Expected != "127.0.0.1:22" {
		t.Errorf("Expected = %q, want 127.0.0.1:22", authErr.Expected)
	}
	if authErr.ResolvedPort == 22 {
		t.Error("ResolvedPort = 22, but the alias overrides Port to the test server's ephemeral port")
	}
}

func TestBinding_UnboundRefused(t *testing.T) {
	c := newBindingClient(t)
	store := &credStub{pw: "x"}
	secretID := credential.SecretID("test-id")

	_, err := c.Connect(
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
	srv := startTestSSHServer(t)
	defer srv.close()
	khPath := writeKnownHosts(t, srv, srv.addr)

	client, err := NewReal(log.NewSlogAdapter(nil), WithConfigResolver(NewStubConfigResolver()), WithKnownHostsFile(khPath))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	store := &credStub{pw: "x"}
	_, srvPort, _ := net.SplitHostPort(srv.addr)

	ch, err := client.Connect(
		context.Background(), srv.addr,
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{gossh.PublicKeys(srv.userSigner)}),
		WithCredentials(store, credential.SecretID("test-id")),
		withBinding(hostPortOnly(srv.addr), 0),
	)
	if err != nil {
		t.Fatalf("host-only binding on port %s: Connect: %v", srvPort, err)
	}
	defer func() { _ = ch.Close() }()
}

func TestBinding_InlineAuthNotChecked(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()
	khPath := writeKnownHosts(t, srv, srv.addr)

	client, err := NewReal(log.NewSlogAdapter(nil), WithConfigResolver(NewStubConfigResolver()), WithKnownHostsFile(khPath))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	ch, err := client.Connect(
		context.Background(), srv.addr,
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{gossh.PublicKeys(srv.userSigner)}),
	)
	if err != nil {
		t.Fatalf("inline Connect: %v", err)
	}
	defer func() { _ = ch.Close() }()
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
