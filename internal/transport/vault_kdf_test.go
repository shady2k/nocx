package transport

import "github.com/shady2k/nocx/internal/vault"

// testCheapKDF is the passphrase cost every vault this package builds uses.
//
// SEVERAL TESTS STAND UP A REAL VAULT, and the production derivation (64 MiB,
// three passes, four threads) is right for a person setting a passphrase once
// and wrong for a test binary that does it hundreds of times: measured here it
// was 27% of this package's CPU, most of why its suite needed the whole
// default ten-minute go test timeout (nocx-ouru5.1.16).
//
// It is a VALUE handed to that one constructor, not a process-wide switch:
// nothing can change what another package's vault writes, and the production
// composition root passes nothing and gets vault.DefaultKDFParams.
func testCheapKDF() vault.KDFParams {
	return vault.KDFParams{MemoryKiB: 1, Time: 1, Threads: 1}
}
