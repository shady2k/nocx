//go:build !nocx_dev_bind

package transport

import (
	"time"

	"github.com/shady2k/nocx/internal/session"
)

// THE SHIPPED HALF, and the default: the listener takes its port from the OS
// and from nothing else.
//
// The pair of files is D10's shape rather than a new one. A stance the process
// must not be argued out of is a property of the BUILD, because the process it
// governs lives for days and any process of the user can set an environment
// variable (keystore_build_login.go says the same thing about the keychain).
// So the variable named in dev_bind_dev.go is not read here — not defaulted,
// not validated, not logged. There is no code in this build that could bind
// anywhere the OS did not choose.
const (
	devBindEnabled = false
	// Named here so the shipped build can assert what it does NOT read:
	// TestDevBindPortIsIgnoredWithoutTheTag sets this variable and requires
	// the listener to land somewhere else.
	devBindPortEnv = "NOCX_DEV_WS_PORT"

	// The stand's startup-bound pair, named for the same reason: this build
	// reads neither, so no environment can lengthen a session's `starting`
	// interval in a shipped binary.
	devStartupBoundEnv     = "NOCX_DEV_INTEGRATION_STARTUP_BOUND"
	devStartupBoundUserEnv = "NOCX_DEV_INTEGRATION_BOUND_USER"
)

// devBindPort is the port to ask the OS for. Zero means "you pick", and it is
// the only answer this build has.
func devBindPort() (int, error) { return 0, nil }

// devNoteStartupBound records nothing: a shipped build has one startup bound,
// and it is the product's own.
func devNoteStartupBound(session.ID, string) {}

// devTakeStartupBound answers nothing, for the same reason.
func devTakeStartupBound(session.ID) (time.Duration, bool) { return 0, false }
