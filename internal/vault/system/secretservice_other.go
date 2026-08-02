//go:build !linux

package system

import "github.com/shady2k/nocx/internal/vault"

// SecretServiceAvailable reports whether a usable freedesktop.org Secret
// Service is present. Off Linux there is no such thing, so this is always
// false — macOS and Windows reach their keychains through their own APIs, not
// through org.freedesktop.secrets.
//
// This stub exists so the symbol is present on every platform. Without it the
// Linux-only definition would compile fine here and break the darwin build the
// moment any cross-platform code called it, which is a failure nobody would
// see until the release job ran.
func SecretServiceAvailable() bool { return false }

// platformReason abstains off Linux. The macOS Keychain and the Windows
// Credential Manager expose no equivalent of the Secret Service's Locked
// property, so there is nothing here to observe, and inventing an answer would
// be the same guess this indirection exists to remove. When the probe abstains
// the provider keeps its historical default.
func platformReason() vault.Reason { return "" }
