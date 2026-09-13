package app

// The script-mode bundle publish, over THIS MACHINE'S HELPER (nocx-50w7p.15).
//
// # What was wrong with the path this replaces
//
// The publish used to ride the pane's own connection: `GetRemoteHome` ran
// `echo $HOME` on an exec channel and the bundle was written under whatever
// came back. That path is gone (the helper owns every dial), and it had to go
// for a reason beyond ownership — the answer it collected was not the one the
// far side ACTIVATES with. Measured on the live-sshd fixture (nocx-50w7p.12):
// publishing to the sftp subsystem's own starting directory — the passwd
// home — made the far side refuse the generation, because a session's shell
// activates under the SESSION's `$HOME` (`sshd_config`'s SetEnv, which the
// session home and nothing else carries). Two answers to "where is this
// account's home" is one answer too many, and the party that knows which one a
// session uses is the one asking on the connection that session rides.
//
// # The route, and why it is two ops on ONE connection
//
// The home question is the `home` NAMED PROBE (nocx-50w7p.9): two fixed
// commands and the fallback rule between them, spelled once in
// internal/remoteprobe, asked on a probe lease. The bundle then rides an
// `ssh.open(kind=sftp)` channel. Both are the helper's, both name the same
// resolved destination, and so both land on the SAME pooled connection —
// AD-4's pool is keyed by host+port+user+identity, the lease is acquired
// first and released last, and the connection is ref-counted across the two.
// One authentication for one machine, which is what makes the Files panel, a
// pane and this publish all one login rather than three.
//
// # What stays in the coordinator, and why
//
// The publish protocol itself (the manifest, the generation, the atomic
// commit) and pkg/sftp. The helper may not link either — the artifact deployed
// to somebody else's host carries neither (internal/helper/deploy/
// dependency_test.go) — so it opens a stream, moves bytes, and is told nothing
// about what they mean. A home it cannot name is a refusal here rather than a
// guess there: an empty answer means the host could not say, and publishing
// into a directory nobody named is how a bundle lands somewhere no session
// will ever read.

import (
	"context"
	"fmt"
	"log/slog"

	pkgsftp "github.com/pkg/sftp"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
)

// helperBundlePublisher is the publish's transport: this machine's helper.
//
// It holds the same two collaborators every adapter in this package holds and
// for the same reasons — the connection to this machine's daemon (one daemon
// connection per coordinator, not one per consumer) and the coordinator's own
// resolver, which is the party that reads ~/.ssh/config and holds the
// credential binding. The destination travels as an address, an account and a
// credential REFERENCE: the helper reads no config, holds no secret at rest,
// and decides nothing about what an address means.
type helperBundlePublisher struct {
	probes   *helperProbes
	channels *sshOverHelper
	// publish is the protocol itself, over the filesystem carrier built from
	// the helper's stream. It is an interface so the route can be driven
	// without a real far side: the publish's own behaviour is
	// internal/shellintegration's and is tested there.
	publish bundlePublisher
	log     *slog.Logger
}

// bundlePublisher is the publish protocol as this file needs it: the bundle,
// into a named home, over a filesystem carrier — and its removal, over the
// same carrier and from the same home.
//
// The two are one interface because they are one route: both ride a probe lease
// and an sftp channel (nocx-50w7p.5 moved the removal here to join the
// publish), and a caller that could publish but not remove would be a second
// transport for one protocol.
type bundlePublisher interface {
	EnsureInstalledRemote(ctx context.Context, fs shellintegration.FS, remoteHome string) error
	UninstallRemote(ctx context.Context, fs shellintegration.FS, remoteHome string) (removed, conflicts []string, err error)
}

// PublishBundle writes the integration bundle into the remote account's home,
// through this machine's helper.
//
// THE ORDER IS THE CONNECTION'S: the lease is taken first so the pooled
// connection exists before the channel is opened on it, and it is released
// last so the channel never outlives the reference that keeps the transport
// alive. Reversed, the sftp open would dial for itself and the bundle would
// cost a second authentication — which is the one thing this route exists to
// not do.
func (p *helperBundlePublisher) PublishBundle(ctx context.Context, host string, opts ...ssh.ConnectOption) error {
	lease, err := p.probes.acquire(ctx, host, opts...)
	if err != nil {
		return fmt.Errorf("publish the shell integration bundle on %s: %w", host, err)
	}
	defer func() { _ = lease.Close() }()

	// The account's OWN home, as the far side reports it over a session of
	// its own — not the sftp subsystem's starting directory, which is the
	// passwd home and is the answer that made a far side refuse a generation
	// it had just been handed.
	home, err := lease.Home(ctx)
	if err != nil {
		return fmt.Errorf("publish the shell integration bundle on %s: the far side's home directory could not be read: %w", host, err)
	}
	if home == "" {
		return fmt.Errorf("publish the shell integration bundle on %s: the far side named no home directory", host)
	}

	stream, err := p.channels.openChannel(ctx, proto.ChannelSFTP, host, opts)
	if err != nil {
		return fmt.Errorf("publish the shell integration bundle on %s: %w", host, err)
	}
	// The stream is closed whatever the publish did: the helper holds a pooled
	// reference for it, and a channel nobody closes is a connection the helper
	// keeps for a caller that has given up.
	defer func() { _ = stream.Close() }()

	client, err := pkgsftp.NewClientPipe(stream, stream)
	if err != nil {
		return fmt.Errorf("publish the shell integration bundle on %s: sftp client: %w", host, err)
	}
	defer func() { _ = client.Close() }()

	if err := p.publish.EnsureInstalledRemote(ctx, shellIntegrationSFTPFS{SFTPFS: ssh.NewSFTPFS(client)}, home); err != nil {
		return fmt.Errorf("publish the shell integration bundle on %s: %w", host, err)
	}
	if p.log != nil {
		p.log.Info("ssh: shell integration bundle published through this machine's helper",
			"host", host, "home", home)
	}
	return nil
}

// UninstallBundle removes the integration bundle from the remote account's
// home, through this machine's helper — the mirror of PublishBundle, over the
// same lease and the same channel (nocx-50w7p.5).
//
// It exists because the removal had its own transport until now: it rode the
// connection internal/ssh acquired for the coordinator (`UninstallIntegration`),
// which is the last dial this process made and the one the epic exists to
// delete. Nothing about the protocol moves with it — internal/shellintegration
// still owns what a removal IS, and internal/remoteprobe still owns the
// commands — and what changes is only which process holds the connection.
//
// THE ORDER IS THE CONNECTION'S, for the reason PublishBundle gives: the lease
// first so the pooled connection exists before the channel opens on it, and
// released last so the channel never outlives the reference keeping it alive.
// The home is asked over the lease and not over the sftp subsystem, because the
// subsystem's starting directory is the passwd home, and a removal that used it
// would look in a different place from the publish that wrote.
func (p *helperBundlePublisher) UninstallBundle(ctx context.Context, host string, opts ...ssh.ConnectOption) (removed, conflicts []string, err error) {
	lease, err := p.probes.acquire(ctx, host, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("remove the shell integration bundle on %s: %w", host, err)
	}
	defer func() { _ = lease.Close() }()

	home, err := lease.Home(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("remove the shell integration bundle on %s: the far side's home directory could not be read: %w", host, err)
	}
	if home == "" {
		return nil, nil, fmt.Errorf("remove the shell integration bundle on %s: the far side named no home directory", host)
	}

	stream, err := p.channels.openChannel(ctx, proto.ChannelSFTP, host, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("remove the shell integration bundle on %s: %w", host, err)
	}
	defer func() { _ = stream.Close() }()

	client, err := pkgsftp.NewClientPipe(stream, stream)
	if err != nil {
		return nil, nil, fmt.Errorf("remove the shell integration bundle on %s: sftp client: %w", host, err)
	}
	defer func() { _ = client.Close() }()

	removed, conflicts, err = p.publish.UninstallRemote(ctx, shellIntegrationSFTPFS{SFTPFS: ssh.NewSFTPFS(client)}, home)
	if err != nil {
		return nil, nil, fmt.Errorf("remove the shell integration bundle on %s: %w", host, err)
	}
	if p.log != nil {
		p.log.Info("ssh: shell integration bundle removed through this machine's helper",
			"host", host, "home", home, "removed", len(removed), "conflicts", len(conflicts))
	}
	return removed, conflicts, nil
}
