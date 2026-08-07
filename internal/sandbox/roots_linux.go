//go:build linux

package sandbox

// systemReadOnlyRoots is the documented Linux read-only set (design spec
// §5.5). Missing roots are skipped at policy build time.
func systemReadOnlyRoots() []string {
	return []string{
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
}
