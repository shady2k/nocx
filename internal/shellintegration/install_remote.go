package shellintegration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	gossh "golang.org/x/crypto/ssh"

	"github.com/pkg/sftp"
)

// sftpFS is the publisher's filesystem seam backed by an *sftp.Client
// (AD-8: the SFTP carrier and the self-installing launcher implement the
// same FS interface; the publisher holds no SFTP knowledge). Modes are set
// at creation and never left to the server's umask, exactly like osFS, and
// no path is followed through a symlink — lstat semantics throughout
// (design §4.1).
type sftpFS struct {
	client *sftp.Client
}

func (f sftpFS) Lstat(path string) (fs.FileInfo, error) { return f.client.Lstat(path) }

// Mkdir creates the directory and then chmods it: the SFTP server applies
// its own umask to the requested mode, which would silently widen 0700 into
// 0755 on a permissive host ("modes are set at creation, never left to
// umask"). The SFTP protocol answers EEXIST as a generic failure, so a path
// that already exists as a directory is translated to fs.ErrExist — the
// publisher's concurrent-create race tolerance reads it through that error.
func (f sftpFS) Mkdir(path string, mode os.FileMode) error {
	if err := f.client.Mkdir(path); err != nil {
		if isSFTPStatus(err, uint32(sftp.ErrSSHFxFailure)) {
			if info, lerr := f.client.Lstat(path); lerr == nil && info.IsDir() {
				return fs.ErrExist
			}
		}
		return err
	}
	return f.client.Chmod(path, mode)
}

// Create opens the file for writing (truncating if present) and then chmods
// it for the same umask reason. The returned File is the write boundary:
// Write, Sync and Close are separate fault-injectable steps.
func (f sftpFS) Create(path string, mode os.FileMode) (File, error) {
	fh, err := f.client.Create(path)
	if err != nil {
		return nil, err
	}
	if err := fh.Chmod(mode); err != nil {
		_ = fh.Close()
		return nil, err
	}
	return &sftpFile{File: fh}, nil
}

// SyncDir no-ops: SFTP has no directory fsync, and the seam's own carve-out
// says transports without it may no-op. Durability scope is stated, not
// assumed (design §4).
func (f sftpFS) SyncDir(string) error { return nil }

// Rename preserves the publisher's atomic-replacement contract on OpenSSH
// servers. SFTP v3's SSH_FXP_RENAME is only portable when dst is absent; an
// advertised posix-rename@openssh.com provides rename(2) replacement
// semantics for manifest upgrades.
//
// There is deliberately no remove-then-rename fallback when the extension is
// absent: that would create a window with no activation pointer, violating
// Publisher's fail-open invariant. A server without the extension can receive
// a first publish, but an existing destination is refused with the previous
// manifest untouched. Checking here also avoids relying on non-portable
// servers that happen to make SSH_FXP_RENAME replace.
func (f sftpFS) Rename(src, dst string) error {
	if _, ok := f.client.HasExtension("posix-rename@openssh.com"); ok {
		return f.client.PosixRename(src, dst)
	}
	if _, err := f.client.Lstat(dst); err == nil {
		return fmt.Errorf("atomic replacement unsupported: server does not advertise posix-rename@openssh.com")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check rename destination: %w", err)
	}
	return f.client.Rename(src, dst)
}

// Remove deletes a single file, retrying as a directory removal when
// SSH_FXP_REMOVE refuses (the publisher removes the empty lock directory
// this way). Absence is reported through fs.ErrNotExist, which the
// publisher tolerates.
func (f sftpFS) Remove(path string) error {
	err := f.client.Remove(path)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if rerr := f.client.RemoveDirectory(path); rerr != nil {
		return err
	}
	return nil
}

// ReadDir lists the entries of dir with lstat semantics: the SFTP server
// fills attrs from readdir(3), so a symlink entry is reported as a symlink,
// never followed.
func (f sftpFS) ReadDir(path string) ([]fs.FileInfo, error) { return f.client.ReadDir(path) }

func (f sftpFS) ReadFile(path string) ([]byte, error) {
	fh, err := f.client.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = fh.Close() }()
	return io.ReadAll(fh)
}

// sftpFile adapts *sftp.File to the publisher's File boundary.
type sftpFile struct {
	*sftp.File
}

// Sync requests a flush to stable storage when the server advertises the
// fsync@openssh.com extension. The SFTP protocol has no mandatory fsync: a
// server without the extension answers OP_UNSUPPORTED, and durability is
// then the server's promise — stated, not assumed (design §4). Every other
// failure is a real publish boundary.
func (f *sftpFile) Sync() error {
	err := f.File.Sync()
	if err == nil || isSFTPStatus(err, uint32(sftp.ErrSSHFxOpUnsupported)) {
		return nil
	}
	return err
}

// isSFTPStatus reports whether err is an SFTP status reply carrying the
// given protocol code. The SFTP protocol answers EEXIST and directory
// removal refusals as generic failures, so the carrier maps those to the fs
// semantics the publisher expects.
func isSFTPStatus(err error, code uint32) bool {
	var se *sftp.StatusError
	return errors.As(err, &se) && se.Code == code
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
	res, err := NewPublisher(s.log, sftpFS{client: sftpClient}, root).Publish(launchBundle())
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
	res, err := NewPublisher(s.log, sftpFS{client: sftpClient}, root).Uninstall()
	if err != nil {
		return nil, nil, fmt.Errorf("shellintegration: remote uninstall: %w", err)
	}
	s.log.Info("shellintegration: remote bundle uninstalled",
		"root", root, "removed", res.Removed, "conflicts", res.Conflicts)
	return res.Removed, res.Conflicts, nil
}

// EnsureInstalledOverPipe publishes the bundle over an SFTP subsystem that is
// already speaking on rw, through the same Publisher every other carrier
// uses. It is the typed path's publish (ADR-0035): there the connection is
// the user's own `ssh` process and nocx holds no SSH transport for it — what
// it holds is an AUXILIARY CHANNEL on that connection, opened over the
// multiplex master after ownership was proven, which is a pair of pipes and
// not a *gossh.Client.
//
// The seam is a pipe rather than a client for exactly that reason, and it is
// the same publish either way: one owner of the behaviour, and the transport
// is the variation (AD-8). The fail-open contract is unchanged — any failure
// leaves the previous activation untouched and the session still starts.
func (s *Impl) EnsureInstalledOverPipe(_ context.Context, rw io.ReadWriteCloser, remoteHome string) error {
	if remoteHome == "" {
		return fmt.Errorf("shellintegration: remote home directory is empty")
	}
	sftpClient, err := sftp.NewClientPipe(rw, rw)
	if err != nil {
		return fmt.Errorf("shellintegration: sftp over the auxiliary channel: %w", err)
	}
	defer func() { _ = sftpClient.Close() }()

	root := path.Join(remoteHome, dirName)
	res, err := NewPublisher(s.log, sftpFS{client: sftpClient}, root).Publish(launchBundle())
	if err != nil {
		s.log.Info("remote bundle publish refused", "root", root, "error", err)
		return fmt.Errorf("shellintegration: remote publish: %w", err)
	}
	s.log.Info("shellintegration: remote bundle published over the multiplex master",
		"root", root, "version", res.Version, "generation", res.Generation,
		"published", res.Published, "reason", res.Reason)
	return nil
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
