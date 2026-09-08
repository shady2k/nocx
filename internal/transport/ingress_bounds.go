package transport

import "github.com/shady2k/nocx/internal/ssh"

// Ingress bounds shared by more than one control domain.
//
// A bound lives here when two domains bound THE SAME THING. It does not live
// here merely because two domains happen to bound something: `maxHostRunes`
// (a DNS name that gets dialled, 253) and `maxDestinationRunes` (a
// user@host:port identity a renderer reports, 512) collided by NAME during
// the per-field validation sweep and are two different concepts, so they
// stayed where they are under names that say which is which. A path is the
// one that really was the same thing twice.

// maxPathRunes bounds one filesystem path — the settings key path read from
// disk at connect time and the files.* domain's paths alike. PATH_MAX is
// 4096 bytes on every platform this ships to, so a longer path cannot name a
// file that exists; the bound is the honest ceiling rather than a chosen one.
const maxPathRunes = 4_096

// maxHostRunes bounds an SSH host that gets DIALLED. 253 is the DNS name
// ceiling, and it also covers every bracketed IPv6 literal, so a longer
// string cannot name a host that resolves. Two domains had bounded this at
// 253 and at 300; the 300 was chosen, the 253 is derived, and nothing
// legitimate sits between them.
const maxHostRunes = 253

// maxUserRunes bounds an SSH user name. Both domains that bound it arrived
// at the same number independently, which is the clearest sign it is one
// concept rather than two.
const maxUserRunes = 256

// validateSSHHost owns the control-plane shape shared by direct opens and
// stored profiles. A leading dash would be parsed by ssh as an option, not
// as the positional destination the caller named.
func validateSSHHost(field, value string) string {
	if msg := validateStringBound(field, value, maxHostRunes); msg != "" {
		return msg
	}
	if hasControlChars(value) {
		return field + " must not contain control characters"
	}
	if ssh.IsOptionLikeHost(value) {
		return field + " must not begin with a dash"
	}
	return ""
}

// validateProfileID owns the renderer-supplied profile-id shape shared by the
// ports, tunnel, connections and shell.footprint seams: required, bounded,
// control-free. The stored id is a store key and a resolver input, so a
// control character in it is a hostile or broken caller.
//
// The bound is maxConfigIDRunes — profile.MaxIDRunes, the ceiling the backend
// mint guarantees — and NOT maxIDRunes, which bounds the agent surface's ask
// and attached-item ids. Both are 128 today; they are different concepts, and
// a profile id measured against the ask path's number agrees with the domain
// only until one of them moves.
func validateProfileID(id string) string {
	if id == "" {
		return "profileId is required"
	}
	return validateStringBound("profileId", id, maxConfigIDRunes)
}
