package ssh

// Shared helpers for this package's tests.
//
// readWithTimeout lived in ssh_discovery_test.go until that file's subject — the
// coordinator's own discovery lease — moved to the helper (nocx-50w7p.9), and it
// is used by the channel tests, not by the lease's. It is here on its own so the
// next deletion takes a test with its subject and leaves the helpers alone.

import (
	"testing"
	"time"
)

// readWithTimeout reads from ch with a bounded wait, so a regression that
// breaks the echo hangs the test instead of the whole suite.
func readWithTimeout(t *testing.T, ch Channel) string {
	t.Helper()
	type readOut struct {
		n   int
		err error
	}
	buf := make([]byte, 128)
	done := make(chan readOut, 1)
	go func() {
		n, err := ch.Read(buf)
		done <- readOut{n, err}
	}()
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("channel read: %v", out.err)
		}
		return string(buf[:out.n])
	case <-time.After(5 * time.Second):
		t.Fatal("channel read timed out")
		return ""
	}
}
