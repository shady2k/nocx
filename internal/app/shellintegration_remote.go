package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	pkgsftp "github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"

	"github.com/shady2k/nocx/internal/remoteprobe"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
)

// remoteInstallerAdapter is the composition-root carrier for the remote
// shell-integration publisher. shellintegration owns the publish protocol and
// accepts FS; this adapter owns the transport, and the transport of each half
// moved separately:
//
//   - the PUBLISH is this machine's helper's since nocx-50w7p.15. `bundle` is
//     the helper-backed route (helper_publish.go), which asks the account's
//     home on a pooled lease and opens the sftp channel the bundle rides on
//     the same connection. This adapter's own job is to hand the destination
//     over unchanged.
//   - the REMOVAL still rides the connection internal/ssh acquired
//     (UninstallIntegration), so it keeps the raw client and asks the home
//     over it. It moves the same way when its own task lands.
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

// UninstallRemote removes the committed bundle, asking the far side where its
// home is over the connection internal/ssh handed it.
//
// The home travels with the transport and not with the caller (see
// ssh.RemoteInstaller): the commands are internal/remoteprobe's, so this is
// the party that may name them, and the answer is the same `$HOME` the publish
// writes under.
func (a *remoteInstallerAdapter) UninstallRemote(ctx context.Context, client *gossh.Client) ([]string, []string, error) {
	home, err := a.inner.GetRemoteHome(remoteHomeOver{client: client})
	if err != nil {
		return nil, nil, err
	}
	clientSFTP, err := pkgsftp.NewClient(client)
	if err != nil {
		return nil, nil, fmt.Errorf("shellintegration: sftp client: %w", err)
	}
	defer func() { _ = clientSFTP.Close() }()
	return a.inner.UninstallRemote(ctx, shellIntegrationSFTPFS{SFTPFS: ssh.NewSFTPFS(clientSFTP)}, home)
}

func (a *remoteInstallerAdapter) EnsureInstalledOverPipe(ctx context.Context, rw io.ReadWriteCloser, home string) error {
	clientSFTP, err := pkgsftp.NewClientPipe(rw, rw)
	if err != nil {
		return fmt.Errorf("shellintegration: sftp over the auxiliary channel: %w", err)
	}
	defer func() { _ = clientSFTP.Close() }()
	return a.inner.EnsureInstalledOverPipe(ctx, shellIntegrationSFTPFS{SFTPFS: ssh.NewSFTPFS(clientSFTP)}, home)
}

// remoteHomeOver answers the home question over the client this path dials
// with.
//
// The transport is this path's own and moves with its own task (the script-mode
// carrier); what this type must not do is COMPOSE a command, and it does not:
// the commands are internal/remoteprobe's list, in its order, and the only
// decision here is "trim the answer and try the next one when it is empty" —
// the same rule the helper applies on the other transport.
type remoteHomeOver struct {
	client *gossh.Client
}

func (r remoteHomeOver) Home() (string, error) {
	for _, command := range remoteprobe.HomeCommands {
		sess, err := r.client.NewSession()
		if err != nil {
			return "", err
		}
		out, err := sess.Output(command)
		_ = sess.Close()
		if err != nil {
			return "", err
		}
		if home := strings.TrimSpace(string(out)); home != "" {
			return home, nil
		}
	}
	return "", nil
}
