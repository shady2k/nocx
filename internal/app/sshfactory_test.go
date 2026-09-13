//go:build nocx_local_ssh

package app

// The session.SSHFactory the app's dialing stands wire, and the composition
// root no longer does.
//
// It used to live in the composition root as sshFactoryAdapter, and
// nocx-50w7p.5 deleted it from there: `sess.WithSSHFactory(...)` was the last
// thing in the coordinator that could have dialed a far host, so the root wires
// no factory at all now, and a coordinator build has no Connect to adapt
// (internal/ssh/ssh_real.go is the half both builds carry; the dial is
// ssh_real_dial.go, tagged).
//
// The stands that drove the coordinator's own open path still need one, which
// is why this exists here rather than beside the root. A test may dial — this
// file is compiled only into the tagged build, the one that links the client at
// all (the same tag internal/app's *_live_test.go files carry) — and what the
// stands are asserting is the auth ladder of an open, not the transport
// underneath it. Reworking them onto the helper's spawn-ssh seam is the route
// commit of plan §7, not this split.

import (
	"context"

	"github.com/shady2k/nocx/internal/ssh"
)

// coordinatorDialFactory adapts a *ssh.RealClient to session.SSHFactory for a
// stand: one method, and the same one the deleted adapter had.
type coordinatorDialFactory struct {
	client *ssh.RealClient
}

func (f *coordinatorDialFactory) Connect(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.Channel, error) {
	return f.client.Connect(ctx, host, opts...)
}
