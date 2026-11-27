package filesystem

import (
	"crypto/sha256"
	"encoding/hex"
)

// ContentDigest is the hex SHA-256 of content bytes — the field a Version's
// fence is built on, and the one a lost commit reply is settled by: a caller
// that meant to install these bytes can re-read the file and compare this
// digest, which is a rewrite of the same length inside one filesystem
// timestamp granule's only honest answer.
//
// It is one function in one place because two implementations of "the digest
// of these bytes" agree everywhere anyone looks and disagree at the length at
// which the trailing newline is or is not included — which is the whole
// question here.
func ContentDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
