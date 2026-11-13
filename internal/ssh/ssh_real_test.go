//go:build nocx_local_ssh

package ssh

// Resolution and pool behaviour on the seam the local helper uses: the
// destination resolves through ~/.ssh/config with explicit options winning,
// and two acquisitions of one destination share one connection (AD-4).

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	gossh "golang.org/x/crypto/ssh"
)

// hostAndPort splits a dial address into the resolved pair a PooledSpec
// carries.
func hostAndPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	return host, port
}

// TestSSHConfig_AliasResolution proves an alias resolves to the address, the
// port and the account ~/.ssh/config names for it — the values the helper is
// handed and dials, so a wrong answer here is a connection to the wrong
// machine rather than a bad display.
func TestSSHConfig_AliasResolution(t *testing.T) {
	stub := NewStubConfigResolver()
	stub.AddEntry("myalias", HostConfig{HostName: "target.example.com", User: "testuser", Port: 2222})

	client, err := NewReal(log.NewSlogAdapter(nil), WithConfigResolver(stub))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	target, err := client.ResolveTarget(context.Background(), "myalias",
		WithUser("testuser"),
		WithCredentials(&credStub{pw: "x"}, "test-id"),
		// Bound to the alias the profile names: the credential is authorized
		// for what that alias resolves to.
		withBinding("myalias", 0))
	if err != nil {
		t.Fatalf("ResolveTarget via alias: %v", err)
	}
	if target.Host != "target.example.com" || target.Port != 2222 || target.User != "testuser" {
		t.Fatalf("alias resolved to %s@%s:%d, want testuser@target.example.com:2222",
			target.User, target.Host, target.Port)
	}
}

// TestSSHConfig_ExplicitOptionBeatsConfig: an explicit option outranks the
// config, which is OpenSSH's own rule and the one a caller relies on when it
// resolved a profile's effective account from the saved connection.
func TestSSHConfig_ExplicitOptionBeatsConfig(t *testing.T) {
	stub := NewStubConfigResolver()
	stub.AddEntry("target.example.com", HostConfig{User: "configuser"})

	client, err := NewReal(log.NewSlogAdapter(nil), WithConfigResolver(stub))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	target, err := client.ResolveTarget(context.Background(), "target.example.com",
		WithUser("explicituser"),
		WithCredentials(&credStub{pw: "x"}, "test-id"),
		withBinding("target.example.com", 0))
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if target.User != "explicituser" {
		t.Fatalf("resolved user = %q, want explicituser (an explicit option beats the config)", target.User)
	}
}

// TestPoolConnectionSharing: two acquisitions of one destination share one
// connection, and the connection closes when the last reference goes. That is
// AD-4's whole contract, and the helper's services acquire per pane, per
// forward and per probe.
func TestPoolConnectionSharing(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	client, err := NewReal(log.NewSlogAdapter(nil))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	host, port := hostAndPort(t, srv.addr)
	spec := PooledSpec{
		Host:     host,
		Port:     port,
		User:     "test",
		Identity: "sharing-fixture",
		Config: &gossh.ClientConfig{
			User:            "test",
			Auth:            []gossh.AuthMethod{gossh.PublicKeys(srv.userSigner)},
			HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // fixture server started by this test
		},
	}

	first, err := client.AcquirePooled(context.Background(), spec)
	if err != nil {
		t.Fatalf("AcquirePooled 1: %v", err)
	}
	second, err := client.AcquirePooled(context.Background(), spec)
	if err != nil {
		t.Fatalf("AcquirePooled 2: %v", err)
	}
	if first.Client() != second.Client() {
		t.Fatal("two acquisitions of one destination got two connections; the pool did not share")
	}
	if got := client.dial.pool.Count(); got != 1 {
		t.Fatalf("after 2 acquisitions of one destination, pool.Count()=%d, want 1", got)
	}

	// Close one — the connection stays for the other reference.
	if err := first.Close(); err != nil {
		t.Fatalf("first.Close: %v", err)
	}
	if got := client.dial.pool.Count(); got != 1 {
		t.Fatalf("after 1 close, pool.Count()=%d, want 1 (still shared)", got)
	}

	// Close the last — the connection is released.
	if err := second.Close(); err != nil {
		t.Fatalf("second.Close: %v", err)
	}
	if got := client.dial.pool.Count(); got != 0 {
		t.Fatalf("after all closed, pool.Count()=%d, want 0", got)
	}
}
