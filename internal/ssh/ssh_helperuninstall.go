package ssh

// The helper-uninstall capability (remote-helper design D25): the way back
// out of an installed helper. It is the removal half of the same lease the
// installer uses — the write-capable SFTP subsystem over the pooled
// connection — and it removes the WHOLE ~/.nocx/helper tree: every version
// and any directory an interrupted install left incomplete, which is
// exactly the kind a user cannot otherwise get rid of. Nothing else under
// the home is touched (the shell bundle's files belong to the publisher).
//
// D25's ordering is the CALLER's contract, stated here because the whole
// point of the rule is that no helper may be running out of a directory
// being deleted: the composition root closes every live helper channel on
// the machine BEFORE this capability runs. This capability owns only the
// dial-and-remove — acquire the lease, ask the SFTP server for the remote
// home, remove the tree — and the raw *gossh.Client never leaves
// internal/ssh, exactly as with UninstallIntegration.

import (
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path"

	"github.com/shady2k/nocx/internal/helper/deploy"
)

// helperUninstallFS adapts a HelperInstallConn to the deploy package's
// RemoteFS seam: the same shape, with each package's own File type name.
// Create is the one method whose return type differs; the rest pass
// through, and Uninstall never reaches Create in practice.
type helperUninstallFS struct{ conn HelperInstallConn }

func (a helperUninstallFS) Lstat(p string) (iofs.FileInfo, error) { return a.conn.Lstat(p) }
func (a helperUninstallFS) Mkdir(p string, m os.FileMode) error   { return a.conn.Mkdir(p, m) }

func (a helperUninstallFS) Create(p string, m os.FileMode) (deploy.File, error) {
	f, err := a.conn.Create(p, m)
	if err != nil {
		return nil, err
	}
	return uninstallFile{f}, nil
}

func (a helperUninstallFS) SyncDir(p string) error                    { return a.conn.SyncDir(p) }
func (a helperUninstallFS) Rename(s, d string) error                  { return a.conn.Rename(s, d) }
func (a helperUninstallFS) Remove(p string) error                     { return a.conn.Remove(p) }
func (a helperUninstallFS) ReadDir(p string) ([]iofs.FileInfo, error) { return a.conn.ReadDir(p) }
func (a helperUninstallFS) ReadFile(p string) ([]byte, error)         { return a.conn.ReadFile(p) }

// uninstallFile promotes the lease's File (io.Writer + Sync + Close) to
// deploy.File.
type uninstallFile struct{ File }

var _ deploy.File = uninstallFile{}

// UninstallHelper removes the helper install tree from a remote host
// (remote-helper design D25): the whole ~/.nocx/helper tree, including
// directories left incomplete by interrupted installs. removed reports
// whether a helper tree existed at all: a host with nothing installed
// uninstalls cleanly — a no-op that succeeds — so a user clicking remove
// twice never sees a failure.
//
// The caller must have closed every live helper channel on this machine
// BEFORE calling (D25): no helper may be running out of a directory being
// deleted. A helper running from a DIFFERENT nocx instance sharing the same
// $HOME is out of this caller's reach and stated as such — the design
// accepts it because the backend can only know about its own channels.
func (rc *RealClient) UninstallHelper(ctx context.Context, host string, opts ...ConnectOption) (removed bool, err error) {
	conn, err := rc.HelperInstallConn(ctx, host, opts...)
	if err != nil {
		return false, fmt.Errorf("ssh: helper uninstall %s: %w", host, err)
	}
	defer func() { _ = conn.Close() }()

	home, err := conn.Home()
	if err != nil {
		return false, fmt.Errorf("ssh: helper uninstall %s: remote home: %w", host, err)
	}
	root := path.Join(home, ".nocx", deploy.HelperRootName)
	if _, err := conn.Lstat(root); err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return false, nil // nothing installed — a clean no-op
		}
		return false, fmt.Errorf("ssh: helper uninstall %s: %w", host, err)
	}
	if err := deploy.Uninstall(ctx, helperUninstallFS{conn}, home); err != nil {
		return false, fmt.Errorf("ssh: helper uninstall %s: %w", host, err)
	}
	return true, nil
}

// compile-time check: the capability is satisfied by *RealClient, which the
// composition root wires without an adapter.
var _ interface {
	UninstallHelper(ctx context.Context, host string, opts ...ConnectOption) (removed bool, err error)
} = (*RealClient)(nil)
