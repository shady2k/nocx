package ghostty

import "github.com/shady2k/nocx/internal/emulator"

// Recovery marker is written by the authenticated shell at its restored prompt.
// It is a locator only; the private nonce is checked by sessionruntime.
const (
	recoveryFixed = "\x1b]1337;NOCX_RECOVERY;"
	recoveryLen   = len(recoveryFixed) + 64 + 1
)

func recoveryMatches(idx int, b byte) bool {
	switch {
	case idx < len(recoveryFixed):
		return b == recoveryFixed[idx]
	case idx < recoveryLen-1:
		return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f')
	default:
		return b == 0x07
	}
}

var _ emulator.EffectKind = emulator.EffectRecoverySighting
