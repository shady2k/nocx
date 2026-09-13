//go:build nocx_local_ssh

// Package sshdial is the ssh client THIS MACHINE's helper dials with.
//
// # Why the build tag, and why it is absent by default
//
// The helper runs on every machine, including hosts nobody here controls, and
// one binary's bytes are both deployed to such a host and installed here (D11,
// D20). An ssh client is precisely the thing the deployed artifact must not
// carry, so the two are different BYTES for the same platform and only a build
// flag can tell them apart: `make helpers` builds the four deployable targets
// with no extra tags, and nocx_local_ssh — passed by `make helper-local` alone,
// which builds the host platform and nothing else — is what links a client into
// the copy installed on this machine. A forgotten flag therefore cannot ship an
// ssh client to somebody else's host; it costs a local helper that cannot dial,
// and that is the direction the mistake is allowed to point.
//
// # What this bead is, and what it is not
//
// nocx-50w7p.1 links the client and stops there: nothing here dials, and no
// pane, file, forward or probe reaches these packages yet. cmd/nocx-helper
// constructs this client at startup, holds it for the daemon's life and closes
// it at shutdown, so the link is real rather than a blank import, but the
// service that dials through it is a later bead (plan §2, §3) and so is the
// answer to what the helper may DO with it. That answer is not obvious and is
// deliberately not invented here: the plan has the helper ask the coordinator
// for credentials and host-key decisions (§2) and hold no secret at rest, so
// the helper's client will not simply be the coordinator's, and the shape of
// the difference belongs to the bead that implements the service.
package sshdial

import (
	"fmt"
	"log/slog"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
)

// New builds the ssh client for one helper daemon.
//
// It is opt-in by build tag (see the package doc), it constructs nothing that
// dials, and it answers the repository's ssh client — the one type that already
// owns the ref-counted connection pool AD-4 requires. A build without the tag
// has no such package at all, so the helper's own seam in cmd/nocx-helper is
// where "this build can dial" is stated for that build.
func New(logger *slog.Logger) (*ssh.RealClient, error) {
	client, err := ssh.NewReal(log.NewSlogAdapter(logger))
	if err != nil {
		return nil, fmt.Errorf("local helper: ssh client: %w", err)
	}
	return client, nil
}
