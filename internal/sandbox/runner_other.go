//go:build !linux && !darwin

package sandbox

// RunRunner refuses platforms without the implemented native boundary. The
// helper must treat this result as unsupported Enforce, never ordinary launch.
func RunRunner() int {
	return 125
}
