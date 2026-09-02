package app

import (
	"context"
	"fmt"
	"io"
	"os"

	pkgsftp "github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"

	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
)

// remoteInstallerAdapter is the composition-root carrier for the remote
// shell-integration publisher. shellintegration owns the publish protocol and
// accepts FS; this adapter owns the SSH/SFTP transport and is therefore absent
// from binaries such as cmd/nocx-helper.
type remoteInstallerAdapter struct {
	inner *shellintegration.Impl
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

func (a *remoteInstallerAdapter) EnsureInstalledRemote(ctx context.Context, client *gossh.Client, home string) error {
	clientSFTP, err := pkgsftp.NewClient(client)
	if err != nil {
		return fmt.Errorf("shellintegration: sftp client: %w", err)
	}
	defer func() { _ = clientSFTP.Close() }()
	return a.inner.EnsureInstalledRemote(ctx, shellIntegrationSFTPFS{SFTPFS: ssh.NewSFTPFS(clientSFTP)}, home)
}

func (a *remoteInstallerAdapter) UninstallRemote(ctx context.Context, client *gossh.Client, home string) ([]string, []string, error) {
	clientSFTP, err := pkgsftp.NewClient(client)
	if err != nil {
		return nil, nil, fmt.Errorf("shellintegration: sftp client: %w", err)
	}
	defer func() { _ = clientSFTP.Close() }()
	return a.inner.UninstallRemote(ctx, shellIntegrationSFTPFS{SFTPFS: ssh.NewSFTPFS(clientSFTP)}, home)
}

func (a *remoteInstallerAdapter) GetRemoteHome(client *gossh.Client) (string, error) {
	return a.inner.GetRemoteHome(remoteCommandRunner{client: client})
}

func (a *remoteInstallerAdapter) EnsureInstalledOverPipe(ctx context.Context, rw io.ReadWriteCloser, home string) error {
	clientSFTP, err := pkgsftp.NewClientPipe(rw, rw)
	if err != nil {
		return fmt.Errorf("shellintegration: sftp over the auxiliary channel: %w", err)
	}
	defer func() { _ = clientSFTP.Close() }()
	return a.inner.EnsureInstalledOverPipe(ctx, shellIntegrationSFTPFS{SFTPFS: ssh.NewSFTPFS(clientSFTP)}, home)
}

type remoteCommandRunner struct {
	client *gossh.Client
}

func (r remoteCommandRunner) Output(command string) ([]byte, error) {
	sess, err := r.client.NewSession()
	if err != nil {
		return nil, err
	}
	defer func() { _ = sess.Close() }()
	return sess.Output(command)
}
