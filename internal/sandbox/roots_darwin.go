//go:build darwin

package sandbox

// systemReadOnlyRoots is the documented macOS read-only set (design spec
// §5.5). Missing roots are skipped at policy build time.
func systemReadOnlyRoots() []string {
	return []string{
		"/usr",
		"/bin",
		"/sbin",
		"/System/Library",
		"/System/Volumes/Preboot/Cryptexes",
		"/Library/Developer/CommandLineTools",
		"/etc",
		"/dev",
		"/private/etc",
		"/private/var/db",
	}
}
