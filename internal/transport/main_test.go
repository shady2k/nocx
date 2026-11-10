package transport

import (
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/vault"
)

// ONE PASS OF THE COST THIS PACKAGE PAYS PER TEST (nocx-ouru5.1.16).
//
// Several tests stand up a real vault, and a vault derives its passphrase key
// with argon2 at production cost — 64 MiB, three passes, four threads. That is
// right for a person setting a passphrase once and wrong for a test binary that
// does it hundreds of times: measured here, it was 27% of this package's CPU
// and most of why its suite needs the whole default ten-minute go test timeout
// (the gated run spent 599 s of test time against a 600 s bound, and the
// goroutine dump showed no test blocked at all).
//
// Lowering it once, for this process, keeps the package's suite inside the
// bound without moving the bound. The parameters are package vars read on every
// derive, so a per-test restore would race a parallel test; the KDF's own tests
// live in internal/vault and set what they need.
func TestMain(m *testing.M) {
	vault.UseCheapKDFForTest()
	os.Exit(m.Run())
}
