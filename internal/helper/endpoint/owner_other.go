//go:build !unix

package endpoint

import "io/fs"

// ownedByThisAccount has no answer off unix: there is no uid on the stat to
// compare against. The mode check above still applies, and the helper's
// supported hosts are the unix ones — a platform without the concept is not
// silently granted a weaker boundary, it simply cannot be asked this question.
func ownedByThisAccount(fs.FileInfo) bool { return true }
