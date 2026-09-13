//go:build nocx_local_ssh

package main

import (
	"log/slog"

	"github.com/shady2k/nocx/internal/helper/sshdial"
)

// holdSSHClient opens the ssh client this daemon holds and answers how to
// release it, for a build that has one: nocx_local_ssh is the tag
// `make helper-local` passes, and it is what the client's own package is gated
// on (internal/helper/sshdial).
//
// Nothing dials through the client yet — this bead links it and stops there —
// which is why "release it" is the whole of the seam's contract today. Widening
// it is a later bead's, and it will be widened where the service that uses the
// client is written rather than here.
func holdSSHClient(log *slog.Logger) (release func(), err error) {
	client, err := sshdial.New(log)
	if err != nil {
		return nil, err
	}
	log.Info("ssh client: this helper can dial; the artifact it came from was built with nocx_local_ssh")
	return func() { _ = client.Close() }, nil
}
