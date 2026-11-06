//go:build !unix

package git

// mountDomain has no device id to read off this platform; each checkout is
// therefore its own failure domain.
func mountDomain(path string) string { return "checkout:" + path }
