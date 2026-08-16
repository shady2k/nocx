package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
)

const OpenCodeIntentName = "opencode"

// IntentStatus is the advisory availability of the fixed backend-owned
// sandbox launch. Open repeats resolution authoritatively because PATH can
// change after sandbox.status returns.
type IntentStatus struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// OpenCodeStatus reports whether the fixed launch executable resolves now.
// It returns no path: executable authority never crosses to the renderer.
func OpenCodeStatus() IntentStatus {
	_, err := ResolveOpenCode()
	if err != nil {
		return IntentStatus{Name: OpenCodeIntentName, Reason: ReasonOpenCodeNotFound}
	}
	return IntentStatus{Name: OpenCodeIntentName, Available: true}
}

// ResolveOpenCode resolves the fixed sandbox launch intent from the backend's
// PATH, follows symlinks, and requires a regular executable. Its error is
// deliberately path-free because the transport may surface it.
func ResolveOpenCode() (string, error) {
	path, err := exec.LookPath(OpenCodeIntentName)
	if err != nil {
		return "", &LaunchError{Reason: ReasonOpenCodeNotFound}
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", &LaunchError{Reason: ReasonOpenCodeNotFound}
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", &LaunchError{Reason: ReasonOpenCodeNotFound}
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", &LaunchError{Reason: ReasonOpenCodeNotFound}
	}
	return canonical, nil
}
