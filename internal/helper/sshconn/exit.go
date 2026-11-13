//go:build nocx_local_ssh

package sshconn

// The exit of a remote process, translated once.
//
// x/crypto spells a remote process's end as *ssh.ExitError, which answers
// ExitStatus() and Signal() (a NAME), while every consumer in this repository
// reads an exit CODE — os/exec's spelling, the one client.ExitStatus mirrors.
// The translation belongs to the carrier, because the cause of a process's end
// is a fact about the connection that carried it (design §5.1.2: "gossh
// ExitError moves into the x/crypto carrier, which maps it to a neutral exit
// cause").
//
// Before this file the same translation was written beside the carrier twice —
// in sshsvc's shell and lane — and a third question of the same shape (does
// this error carry a status at all) was written in probeops. That is three
// owners of one fact, which is what ADR-ba6xet1b forbids, and the reason the
// connection-owner ratchet baselines an ExitError read anywhere else.

import (
	"errors"

	gossh "golang.org/x/crypto/ssh"
)

// Exit answers how a remote process ended, from the error a channel's Wait
// returned.
//
// ok is false when the stream ended with no status the peer reported: the
// connection died, the server closed the channel, the call was abandoned. That
// is NOT an exit 0 — "the command exited zero" and "the wait returned nothing to
// read" must never be the same value — and the caller spells the absence.
//
// The signal NAME x/crypto also carries is dropped rather than translated into a
// number: a name-to-number table would be this package inventing a far kernel's
// numbering.
func Exit(err error) (status int, ok bool) {
	if err == nil {
		return 0, true
	}
	var exit *gossh.ExitError
	if errors.As(err, &exit) {
		return exit.ExitStatus(), true
	}
	return 0, false
}
