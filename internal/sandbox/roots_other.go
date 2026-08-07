//go:build !linux && !darwin

package sandbox

// systemReadOnlyRoots is empty on unsupported platforms: the unsupported
// Service never reaches policy construction.
func systemReadOnlyRoots() []string {
	return nil
}
