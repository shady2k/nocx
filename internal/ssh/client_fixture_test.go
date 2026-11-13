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
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// sshHome gives this test its own $HOME and answers where the product's client
// reads its host keys: $HOME/.ssh/known_hosts.
//
// That is the ONLY seam there is, and it is deliberate rather than a
// convenience: the client resolves its own home, production has no need to
// name a path, and an option for it would be a test hook living in the product
// (nocx-oalo1.3.2). A test that wants its own store gives the client its own
// home, exactly as a person has one. The directory is created; the file is the
// caller's to write, or TrustHostKey's.
func sshHome(t *testing.T) string {
	t.Helper()
	home := sshTestHome(t)
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("prepare %s: %v", dir, err)
	}
	return filepath.Join(dir, "known_hosts")
}

// sshTestHome gives this test its own $HOME, for a caller that needs only a
// home and never reads a host key: a client whose bindings refuse before any
// dial, a client that is only constructed.
//
// It is the one place a test in this package moves HOME, and it moves it
// through storagetest.SetHome so that the go command's module cache stays in
// the caller's directories. `go build` under a moved HOME writes GOPATH and
// GOMODCACHE into the test's home and makes them read-only, which is a home
// nothing can remove and a failure a test did not ask for (nocx-oalo1.3.2).
func sshTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	storagetest.SetHome(t, home)
	return home
}

// newTrustClient builds a RealClient over the $HOME sshHome gave this test.
//
// It registers no cleanup, and the reason is the split rather than an
// oversight: RealClient.Close is the TAGGED half's (it closes the pool), so a
// fixture the untagged pass compiles cannot call it, and there is nothing here
// for it to close — the coordinator's client holds no connection. The tagged
// tests that dial through this client close what they dialed (probeOnce closes
// the one connection it opens), so nothing survives them either.
func newTrustClient(t *testing.T) *RealClient {
	t.Helper()
	client, err := NewReal(log.NewSlogAdapter(nil))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	return client
}
