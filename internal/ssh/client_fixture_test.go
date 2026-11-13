package ssh

// Fixtures that construct a RealClient, shared by test files on BOTH sides of
// the nocx_local_ssh split — which is why this file carries no build tag.
//
// A tagged test file is compiled by both passes (the tagged pass compiles the
// whole package, untagged files included), but an untagged one is not compiled
// by the tagged pass alone: it is compiled by the untagged pass, which is the
// COORDINATOR's build — the one where the dial half does not exist. So a
// fixture that only needs the constructor and the options belongs here, and one
// that needs the pool belongs in a tagged file. The two helpers below are the
// first kind: NewReal, a known_hosts path and a stub resolver are all the
// untagged half (ssh_real.go).

import (
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

// newTrustClient builds a RealClient whose known_hosts lives at the given
// path (which may not exist yet).
//
// It registers no cleanup, and the reason is the split rather than an
// oversight: RealClient.Close is the TAGGED half's (it closes the pool), so a
// fixture the untagged pass compiles cannot call it, and there is nothing here
// for it to close — the coordinator's client holds no connection. The tagged
// tests that dial through this client close what they dialed (probeOnce closes
// the one connection it opens), so nothing survives them either.
func newTrustClient(t *testing.T, khPath string) *RealClient {
	t.Helper()
	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	return client
}
