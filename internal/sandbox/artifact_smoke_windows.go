//go:build windows

package sandbox

// ArtifactSmokeArg is reserved on unsupported platforms so the composition
// root remains buildable. Windows has no V1 sandbox backend.
const ArtifactSmokeArg = "__sandbox-artifact-smoke"

// MaybeArtifactSmoke is a no-op because V1 does not ship a Windows sandbox.
func MaybeArtifactSmoke() bool { return false }
