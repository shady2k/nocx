//go:build linux

package sandbox

import "os"

// systemReadOnlyRoots is the documented Linux read-only set (design spec
// §5.5). Missing roots are skipped at policy build time.
//
// The FHS half of this list is not the whole answer on every Linux. A
// package-store distribution keeps the system in a content-addressed store
// and fills /etc, /bin and /usr with symlinks into it, and both Landlock and
// Seatbelt authorize the RESOLVED target — so granting /etc read-only there
// grants nothing at all. The enforcement smoke on such a host passed 34 of
// its 35 checks and failed exactly one, "read /etc/hosts", whose canonical
// path is a store entry (nocx-263da).
//
// The store is the same KIND of thing /usr is: system-owned, world-readable,
// immutable (mounted read-only on NixOS), and holding no user documents —
// which is why putting a secret in it is a known anti-pattern rather than a
// thing to defend against here. Naming it as a system read-only root is
// therefore the same statement as naming /usr, and it collapses at the source
// the per-library root explosion that made this feature refuse to start:
// every derived loader directory on such a host is a descendant of the store,
// so coalescing folds them into one rule.
func systemReadOnlyRoots() []string {
	roots := []string{
		"/usr",
		"/bin",
		"/sbin",
		"/lib",
		"/lib64",
		"/etc",
		"/dev",
		"/proc",
		"/sys",
	}
	for _, store := range linuxPackageStoreRoots {
		if fi, err := os.Stat(store); err == nil && fi.IsDir() {
			roots = append(roots, store)
		}
	}
	return roots
}

// linuxPackageStoreRoots names the content-addressed system stores this
// build knows about. Only Nix is listed because only Nix has been measured;
// Guix's /gnu/store is the same shape and belongs here the day somebody runs
// the enforcement smoke on it, not before. A var rather than a const so the
// tests can point it somewhere that is not the host.
var linuxPackageStoreRoots = []string{"/nix/store"}
