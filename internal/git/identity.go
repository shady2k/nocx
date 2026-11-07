package git

import "path/filepath"

// FailureDomain names what fails together for work on localPath, before a
// repository exists: the mount the path is on. git.open uses it because a
// checkout's own identity is only known after the open, and the open already
// draws on the disk.
//
// This reads the disk — one stat, to learn the device — so it must be called
// where a stuck syscall is bounded. PathFailureDomain is the same question
// asked without reading anything.
func FailureDomain(localPath string) string {
	return mountDomain(filepath.Clean(localPath))
}

// PathFailureDomain names the domain a path has while its mount is unknown:
// the path itself, in the one spelling mountDomain falls back to when its
// stat fails.
//
// It exists for the order of operations, not for an answer: a caller that
// wants the mount must stat, a stat on a hung mount never returns, and the
// permit that bounds that worker has to be taken BEFORE the question is
// asked. So this names a domain that can be held while FailureDomain runs,
// and the two agree wherever FailureDomain cannot answer.
func PathFailureDomain(localPath string) string {
	return "checkout:" + filepath.Clean(localPath)
}

// CheckoutIdentity is a local checkout's ordering and failure-domain
// identity (ADR-8eej3e66 §5 and §6).
//
// checkout is the canonical working-tree root: the path a person reaches
// through a symlink and the path git reports are the same resource, and on
// macOS a temporary directory is always behind one, so the two spellings must
// not order separately. domain names what fails together for git processes on
// this checkout — its mount, by device id — so a hung mount holds only its own
// bulkhead permits.
//
// A checkout whose stat fails is its own domain: the mount is unknown, and
// grouping it with anything else would be a claim nobody measured.
func CheckoutIdentity(toplevel string) (checkout, domain string) {
	if toplevel == "" {
		return "", ""
	}
	canonical := filepath.Clean(toplevel)
	if resolved, err := filepath.EvalSymlinks(canonical); err == nil {
		canonical = resolved
	}
	return canonical, mountDomain(canonical)
}
