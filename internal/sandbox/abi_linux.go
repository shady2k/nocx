//go:build linux

package sandbox

import (
	"fmt"

	landlock "github.com/landlock-lsm/go-landlock/landlock"
	ll "github.com/landlock-lsm/go-landlock/landlock/syscall"
)

// Landlock ABI policy (design spec §8.1): floor 3 so truncation cannot
// escape the policy (ABI < 3 cannot deny truncation at all), semantic cap 8
// so the declared filesystem-only contract stays stable on ABI 9+ kernels.
const (
	minLandlockABI = 3
	capLandlockABI = 8
)

// landlockABIQuery is a seam for tests; the production implementation is the
// raw kernel query.
var landlockABIQuery = func() (int, error) { return ll.LandlockGetABIVersion() }

// detectABI returns the raw kernel Landlock ABI. An error means the kernel
// cannot run Landlock at all (too old, or the LSM is not enabled).
func detectABI() (int, error) {
	return landlockABIQuery()
}

// strictConfigABI caps the detected ABI at the semantic cap.
func strictConfigABI(abi int) int {
	if abi > capLandlockABI {
		return capLandlockABI
	}
	return abi
}

// strictConfig returns the go-landlock preset for min(abi, cap). These
// presets carry no BestEffort and are used with RestrictPaths only —
// RestrictNet and RestrictScoped are never called (design spec §8.1).
func strictConfig(abi int) landlock.Config {
	switch strictConfigABI(abi) {
	case 1:
		return landlock.V1
	case 2:
		return landlock.V2
	case 3:
		return landlock.V3
	case 4:
		return landlock.V4
	case 5:
		return landlock.V5
	case 6:
		return landlock.V6
	case 7:
		return landlock.V7
	default:
		return landlock.V8
	}
}

// statusForABI maps a detection result onto the stable Status contract.
func statusForABI(abi int, err error) Status {
	if err != nil {
		return Status{Available: false, Backend: BackendLandlock, Reason: ReasonLandlockUnavailable, Detail: fmt.Sprintf("ABI probe: %v", err)}
	}
	if abi < 1 {
		return Status{Available: false, Backend: BackendLandlock, Reason: ReasonLandlockUnavailable, Detail: "ABI probe returned no usable version"}
	}
	if abi < minLandlockABI {
		return Status{Available: false, Backend: BackendLandlock, Reason: ReasonLandlockABITooOld, ABI: abi, Detail: fmt.Sprintf("kernel Landlock ABI %d is below the required floor of %d", abi, minLandlockABI)}
	}
	return Status{Available: true, Backend: BackendLandlock, ABI: abi}
}
