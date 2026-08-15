package shellintegration

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	gossh "golang.org/x/crypto/ssh"

	"github.com/pkg/sftp"

	"github.com/shady2k/nocx/internal/ssh"
)

// sftpFS is the publisher's filesystem seam backed by the shared SFTP
// adapter in internal/ssh (AD-8: the SFTP carrier and the self-installing
// launcher implement the same FS interface; the publisher holds no SFTP
// knowledge). The write primitives — mkdir+chmod, create+chmod, rename,
// removal, fsync tolerance and SFTP status translation — were moved out of
// this package so ONE implementation serves both the shell bundle's
// publisher and the remote helper's install lease (the remote-helper
// design, D7). Create is the one method that must delegate by hand: the
// shared adapter's File is internal/ssh's, and this seam names its own —
// the value is the same file handle, structurally identical.
type sftpFS struct {
	*ssh.SFTPFS
}

func (f sftpFS) Create(path string, mode os.FileMode) (File, error) {
	fh, err := f.SFTPFS.Create(path, mode)
	if err != nil {
		return nil, err
	}
	return fh, nil
}

// EnsureInstalledRemote publishes the integration bundle on a remote host
// over SFTP through the same Publisher the self-installing launcher uses
// (design §4: the SFTP carrier and the launcher hand the same descriptor to
// the same Publish). The rc-gate half of this function is retired (N4): no
// remote rc file is ever created or modified on any path.
//
// Fail-open contract: any publish failure leaves the previous activation
// untouched and the next connection converges with no manual cleanup; the
// session still starts (design §4.1: any publish failure leaves the current
// session usable).
func (s *Impl) EnsureInstalledRemote(ctx context.Context, sshClient *gossh.Client, remoteHome string) error {
	if remoteHome == "" {
		return fmt.Errorf("shellintegration: remote home directory is empty")
	}

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		return fmt.Errorf("shellintegration: sftp client: %w", err)
	}
	defer func() { _ = sftpClient.Close() }()

	root := path.Join(remoteHome, dirName)
	res, err := NewPublisher(s.log, sftpFS{SFTPFS: ssh.NewSFTPFS(sftpClient)}, root).Publish(launchBundle())
	if err != nil {
		// The publish outcome is a delivery decision, logged at INFO with
		// the refusal as a value — the fail-open side: the session still
		// runs transient-integrated, and the log says why nothing stuck.
		s.log.Info("remote bundle publish refused",
			"root", root, "error", err)
		return fmt.Errorf("shellintegration: remote publish: %w", err)
	}
	s.log.Info("shellintegration: remote bundle published",
		"root", root, "version", res.Version, "generation", res.Generation,
		"published", res.Published, "reason", res.Reason)
	return nil
}

// UninstallRemote removes the committed integration bundle on a remote host
// over SFTP through the same Publisher.Uninstall the product exposes (P10):
// only manifest-owned, unmodified files are removed; anything the user
// changed is reported as a conflict and left alone; ~/.nocx is never removed
// recursively — launch, tmp and the root stay in place. The two lists are
// root-relative paths, exactly as the publisher reports them, so the caller
// can render "these went, these did not" without re-deriving the semantics.
func (s *Impl) UninstallRemote(ctx context.Context, sshClient *gossh.Client, remoteHome string) (removed, conflicts []string, err error) {
	if remoteHome == "" {
		return nil, nil, fmt.Errorf("shellintegration: remote home directory is empty")
	}

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		return nil, nil, fmt.Errorf("shellintegration: sftp client: %w", err)
	}
	defer func() { _ = sftpClient.Close() }()

	root := path.Join(remoteHome, dirName)
	res, err := NewPublisher(s.log, sftpFS{SFTPFS: ssh.NewSFTPFS(sftpClient)}, root).Uninstall()
	if err != nil {
		return nil, nil, fmt.Errorf("shellintegration: remote uninstall: %w", err)
	}
	s.log.Info("shellintegration: remote bundle uninstalled",
		"root", root, "removed", res.Removed, "conflicts", res.Conflicts)
	return res.Removed, res.Conflicts, nil
}

// GetRemoteHome queries the remote host for the user's home directory.
func (s *Impl) GetRemoteHome(sshClient *gossh.Client) (string, error) {
	sess, err := sshClient.NewSession()
	if err != nil {
		return "", fmt.Errorf("shellintegration: new session for home: %w", err)
	}
	defer func() { _ = sess.Close() }()

	output, err := sess.Output("echo $HOME")
	if err != nil {
		return "", fmt.Errorf("shellintegration: get remote home: %w", err)
	}
	home := strings.TrimSpace(string(output))
	if home == "" {
		sess2, err := sshClient.NewSession()
		if err != nil {
			return "", fmt.Errorf("shellintegration: new session for ~: %w", err)
		}
		defer func() { _ = sess2.Close() }()
		output2, err := sess2.Output("cd ~ && pwd")
		if err != nil {
			return "", fmt.Errorf("shellintegration: get remote home via ~: %w", err)
		}
		home = strings.TrimSpace(string(output2))
	}
	if home == "" {
		return "", fmt.Errorf("shellintegration: could not determine remote home")
	}
	return home, nil
}

// RemoteStartCommand returns the installed-mode start command (design §3.3):
// exec the compact carrier when a generation is committed, else a native
// login shell. The guard travels to the far side because only that machine's
// ~/.nocx is the one in question; the plain-shell arm covers the one case
// the carrier cannot — its own absence. No passport is emitted from this
// command; production sessions reach the carrier through the launcher, which
// carries the environment id.
func (s *Impl) RemoteStartCommand() string {
	return `if [ -x "$HOME/.nocx/launch" ]; then exec "$HOME/.nocx/launch"; else exec "${SHELL:-/bin/sh}" -l; fi`
}
