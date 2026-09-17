package app

import (
	"context"
	"fmt"
	"io"
	"os"

	pkgsftp "github.com/pkg/sftp"

	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
)

// remoteInstallerAdapter is the composition-root carrier for the remote
// shell-integration publisher. shellintegration owns the publish protocol and
// accepts FS; this adapter owns the transport, and both halves of it are now
// this machine's helper's:
//
//   - the PUBLISH has been since nocx-50w7p.15. `bundle` is the helper-backed
//     route (helper_publish.go), which asks the account's home on a pooled
//     lease and opens the sftp channel the bundle rides on the same
//     connection. This adapter's own job is to hand the destination over
//     unchanged.
//   - the REMOVAL joined it in nocx-50w7p.5, and that is what closed the epic:
//     it used to ride the connection internal/ssh acquired for this process
//     (`UninstallIntegration`), holding a raw *gossh.Client and asking the home
//     over it — the last dial and the last client the coordinator had. It is
//     the same method signature on this carrier now, so the transport and its
//     handler were untouched; only the party behind it changed.
//
// A GAP REMAINS AND IS NAMED: EnsureInstalledOverPipe (the script-mode
// carrier's half) still takes a stream from its caller rather than acquiring
// one, so it is a seam for a transport that is not this file's. Nothing in
// this file holds a client any more.
//
// It is absent from binaries such as cmd/nocx-helper: the publish protocol
// rides the coordinator, and pkg/sftp with it.
type remoteInstallerAdapter struct {
	inner *shellintegration.Impl
	// bundle is the helper-backed publish, assigned at the composition root
	// once the helper halves exist (app.go). Nil in a build or a test that
	// wired no helper, which is a refusal at the act rather than a fallback:
	// there is no second transport to publish over.
	bundle *helperBundlePublisher
}

var _ ssh.RemoteInstaller = (*remoteInstallerAdapter)(nil)

type shellIntegrationSFTPFS struct {
	*ssh.SFTPFS
}

func (f shellIntegrationSFTPFS) Create(path string, mode os.FileMode) (shellintegration.File, error) {
	fh, err := f.SFTPFS.Create(path, mode)
	if err != nil {
		return nil, err
	}
	return fh, nil
}

// EnsureInstalledRemote publishes the bundle through this machine's helper.
//
// The destination is passed on exactly as the caller named it — the address
// and the options the pane's own open resolved with — because the helper's
// pool is keyed by what those resolve to and a second spelling of one
// destination would be a second authentication.
func (a *remoteInstallerAdapter) EnsureInstalledRemote(ctx context.Context, host string, opts ...ssh.ConnectOption) error {
	if a.bundle == nil {
		return fmt.Errorf("ssh: no helper is wired to publish the shell integration bundle for %s", host)
	}
	return a.bundle.PublishBundle(ctx, host, opts...)
}

// UninstallIntegration removes the committed bundle through this machine's
// helper — the transport shell.footprint.uninstall rides (nocx-50w7p.5).
//
// It is the SAME method the capability has always called, on a different party.
// internal/ssh used to own the dial-and-call and called back into this carrier
// with a live *gossh.Client, which made the removal the last connection the
// coordinator made and the last raw client it held. The carrier now owns the
// route — the helper's probe lease and its sftp channel — so the signature is
// what moved and every caller is untouched, which is why the transport needs no
// change to keep the capability it already had.
//
// A build or a test that wired no helper is a REFUSAL at the act rather than a
// fallback, the same shape EnsureInstalledRemote has: there is no second
// transport to remove over, and quietly removing nothing would report success
// for a bundle still sitting in somebody's home.
func (a *remoteInstallerAdapter) UninstallIntegration(ctx context.Context, host string, opts ...ssh.ConnectOption) (removed, conflicts []string, err error) {
	if a.bundle == nil {
		return nil, nil, fmt.Errorf("ssh: no helper is wired to remove the shell integration bundle from %s", host)
	}
	return a.bundle.UninstallBundle(ctx, host, opts...)
}

func (a *remoteInstallerAdapter) EnsureInstalledOverPipe(ctx context.Context, rw io.ReadWriteCloser, home string) error {
	clientSFTP, err := pkgsftp.NewClientPipe(rw, rw)
	if err != nil {
		return fmt.Errorf("shellintegration: sftp over the auxiliary channel: %w", err)
	}
	defer func() { _ = clientSFTP.Close() }()
	return a.inner.EnsureInstalledOverPipe(ctx, shellIntegrationSFTPFS{SFTPFS: ssh.NewSFTPFS(clientSFTP)}, home)
}

// The home question is no longer asked here. It used to be: this type held the
// *gossh.Client the coordinator had dialed and ran internal/remoteprobe's
// commands over it, which is exactly the arrangement nocx-50w7p.5 deletes. The
// helper asks it now, over the lease its own connection already holds
// (helperBundlePublisher.PublishBundle and .UninstallBundle), so this file
// composes no command and holds no client.
