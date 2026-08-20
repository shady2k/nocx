package discovery

// The SSH half of the exec seam: an owned lease on a pooled SSH connection
// satisfies the seam through this adapter, which converts the lease's result
// shape into the transport-neutral ExecResult and classifies its sentinel
// failures into ExecError kinds — so the ladder itself never names the
// transport. The lease's connection-loss surface (Done/LostErr) stays with
// the scheduler, which owns the SSH lifecycle; the seam is just "run a
// command".
import (
	"context"
	"errors"

	"github.com/shady2k/nocx/internal/ssh"
)

// adaptSSH wraps an ssh.DiscoveryConn lease in the exec seam. The caller
// keeps the raw lease for the connection-loss watcher and closes it through
// the adapter (or the lease releases itself on loss).
func adaptSSH(conn ssh.DiscoveryConn) ExecConn {
	return sshAdapter{conn: conn}
}

type sshAdapter struct {
	conn ssh.DiscoveryConn
}

func (a sshAdapter) Exec(ctx context.Context, cmd string) (*ExecResult, error) {
	res, err := a.conn.Exec(ctx, cmd)
	if err != nil {
		return nil, classifySSHError(err)
	}
	return &ExecResult{
		Stdout:     res.Stdout,
		Stderr:     res.Stderr,
		ExitStatus: res.ExitStatus,
		Truncated:  res.Truncated,
	}, nil
}

func (a sshAdapter) Close() error { return a.conn.Close() }

// classifySSHError maps the SSH lease's sentinel failures onto the
// transport-neutral kinds. Anything unrecognized passes through untouched —
// the ladder's default is a transient failure, never a guess about the host.
func classifySSHError(err error) error {
	switch {
	case errors.Is(err, ssh.ErrExecSessionRefused):
		return &ExecError{Kind: ExecErrSessionRefused, Err: err}
	case errors.Is(err, ssh.ErrExecProhibited):
		return &ExecError{Kind: ExecErrExecProhibited, Err: err}
	case errors.Is(err, ssh.ErrExecLost):
		return &ExecError{Kind: ExecErrConnectionLost, Err: err}
	case errors.Is(err, ssh.ErrExecClosed):
		return &ExecError{Kind: ExecErrLeaseClosed, Err: err}
	case errors.Is(err, ssh.ErrCommandTooLong):
		return &ExecError{Kind: ExecErrCommandTooLong, Err: err}
	default:
		return err
	}
}
