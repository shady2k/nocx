//go:build nocx_local_ssh

package main

import (
	"log/slog"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/sshdial"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
)

// holdSSHClient opens the ssh client this daemon holds and answers the seam
// that serves it, for a build that has one: nocx_local_ssh is the tag
// `make helper-local` passes, and it is what the client's own package is gated
// on (internal/helper/sshdial) and what the service is gated on
// (internal/helper/sshsvc).
//
// The client is opened here, beside the sessions and out of the accept loop,
// for the same reason they are: one pool serves every host this machine hosts
// for the daemon's whole life (AD-4), and the service that dials through it is
// process-scoped for the same reason — a connection ending releases that
// connection's protocol engine, and every credential it asked about survives
// it.
//
// This is the widening nocx-50w7p.1 deliberately left to the bead that
// implemented the service: the seam used to answer a release and nothing else,
// because nothing dialed. It now also answers how the service reaches a
// connection, which is the same two-part shape sessions uses (construct once,
// Bind per connection).
func holdSSHClient(log *slog.Logger) (sshSeam, error) {
	client, err := sshdial.New(log)
	if err != nil {
		return sshSeam{}, err
	}
	svc := sshsvc.New(client, log)
	log.Info("ssh service: this helper dials; the artifact it came from was built with nocx_local_ssh")
	return sshSeam{
		register: func(h *host.Host) { h.Register(svc) },
		release:  func() { _ = client.Close() },
	}, nil
}
