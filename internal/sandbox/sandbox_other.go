//go:build !linux && !darwin

package sandbox

import (
	"context"

	"github.com/shady2k/nocx/internal/log"
)

// unsupportedService is the final Windows-and-other stub: the V1 sandbox is
// unsupported outside Linux and macOS (design spec §9.4), so the composition
// root still wires a Service and every request fails closed.
type unsupportedService struct{}

// New returns the unsupported-platform Service for the current platform.
func New(logger log.Logger, _ string) Service {
	return unsupportedService{}
}

// NewWithAccess reports the observer as unavailable on unsupported platforms.
func NewWithAccess(logger log.Logger, cacheDir string, access *AccessInbox) Service {
	if access != nil {
		access.SetStatus(AccessMonitorStatus{Available: false, Platform: "unsupported", Reason: ReasonUnsupportedPlatform})
	}
	return New(logger, cacheDir)
}

// MaybeHelper is a no-op on unsupported platforms: the Linux Landlock helper
// and the macOS Seatbelt shim are platform-specific mechanisms.
func MaybeHelper() bool { return false }

func (unsupportedService) Status(_ context.Context) Status {
	return Status{Available: false, Backend: BackendUnsupported, Reason: ReasonUnsupportedPlatform}
}

func (unsupportedService) NewRuntimeRoot() (string, error) {
	return "", NewSetupErrorf("filesystem sandbox is unsupported on this platform")
}

func (unsupportedService) Prepare(_ context.Context, _ Request, _ CommandSpec) (*PreparedCommand, error) {
	return nil, NewSetupErrorf("filesystem sandbox is unsupported on this platform")
}
