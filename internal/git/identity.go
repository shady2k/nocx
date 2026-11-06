package git

import "path/filepath"

// FailureDomain names what fails together for work on localPath, before a
// repository exists: the mount the path is on. git.open uses it because a
// checkout's own identity is only known after the open, and the open already
// draws on the disk.
func FailureDomain(localPath string) string {
	return mountDomain(filepath.Clean(localPath))
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
