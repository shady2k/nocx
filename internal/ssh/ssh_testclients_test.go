package ssh

// Two fixtures the lease's own tests used to carry, kept because OTHER tests in
// this package take a RealClient pointed at a test server and the two lines
// that build one were written once.
//
// They moved here when nocx-50w7p.8 deleted ssh_tunnel_test.go with the lease
// it tested (the lease is internal/helper/tunnelchan's now, and its own tests
// live with it). What stayed is the pair: the discovery and helper-conn tests
// open a lease-shaped connection to the same stand, and a second copy of these
// would be a second answer to "how does this package point a client at the
// test server".

import (
	"testing"

	"github.com/shady2k/nocx/internal/log"
	gossh "golang.org/x/crypto/ssh"
)

// tunnelTestClient builds a RealClient pointed at the test server, cleaned up
// with the test.
func tunnelTestClient(t *testing.T, srv *testSSHServer) *RealClient {
	t.Helper()
	khPath := writeKnownHosts(t, srv, srv.addr)
	client, err := NewReal(log.NewSlogAdapter(nil), WithKnownHostsFile(khPath))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func tunnelConnectOpts(srv *testSSHServer) []ConnectOption {
	return []ConnectOption{
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{gossh.PublicKeys(srv.userSigner)}),
	}
}
