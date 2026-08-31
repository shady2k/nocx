//go:build unix

package endpoint

import (
	"io/fs"
	"os"
	"syscall"
)

// ownedByThisAccount reports whether the directory belongs to the account this
// process runs as. It is the trust boundary stated as a check: D12 grants
// same-UID trust, so a directory owned by a DIFFERENT uid grants nothing and
// must not be used. Root is the one exception that would be wrong to make: a
// helper running as root has no business trusting a directory it does not own
// either.
func ownedByThisAccount(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		// The stat had no uid to read. Refusing here would strand a platform
		// that answers this way; the 0700 mode is still enforced above.
		return true
	}
	return int(st.Uid) == os.Geteuid()
}
