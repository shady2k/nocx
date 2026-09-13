package ssh

import (
	"context"
	"fmt"
)

// UninstallIntegration removes nocx's shell integration from a remote host,
// owning the dial-and-call end to end (P10). It acquires the pooled
// connection the way Connect does — same resolution, authorization and pool
// key — and hands it to the carrier, which asks the far side where its home is
// over that live connection and removes the bundle. The raw *gossh.Client
// never leaves internal/ssh; callers (the transport) hold only this
// capability, and every future caller that needs to reach a remote host's
// filesystem goes through a capability of the same shape instead of reaching
// for the client.
//
// The HOME QUESTION is the carrier's, and it has to be: internal/ssh must not
// compose shell text (the commands are internal/remoteprobe's, which every
// helper links), and the question is the same one the publish asks. Leaving it
// here was the shape that put an `echo $HOME` in this package's vocabulary and
// in the carrier's at once.
//
// The carrier comes from the ConnectConfig the resolver built — a saved
// connection carries the SFTP installer, so uninstall is available exactly
// where nocx owns credentials. A config without one is refused: nothing is
// removed on a guess.
func (rc *RealClient) UninstallIntegration(ctx context.Context, host string, opts ...ConnectOption) (removed, conflicts []string, err error) {
	cfg := &ConnectConfig{}
	for _, o := range opts {
		o(cfg)
	}

	acq, err := rc.acquirePooled(ctx, host, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("ssh: uninstall %s: %w", host, err)
	}
	defer rc.pool.Release(acq.handle)

	if cfg.RemoteInstaller == nil {
		return nil, nil, fmt.Errorf("ssh: uninstall %s: no remote installer wired; nothing can be removed", host)
	}

	return cfg.RemoteInstaller.UninstallRemote(ctx, acq.client)
}

// compile-time check: the capability is satisfied by *RealClient, which the
// transport wires without an adapter (the signatures are identical).
var _ interface {
	UninstallIntegration(ctx context.Context, host string, opts ...ConnectOption) (removed, conflicts []string, err error)
} = (*RealClient)(nil)
