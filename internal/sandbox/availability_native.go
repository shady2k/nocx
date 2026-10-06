//go:build linux || darwin

package sandbox

// NativeAvailable probes the same backend that prepares a launch. Success is
// capability, not proof that any existing process has a policy applied.
func NativeAvailable() error { return backendAvailable() }
