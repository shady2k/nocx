package app

import (
	"context"

	"github.com/shady2k/nocx/internal/completion"
	"github.com/shady2k/nocx/internal/ssh"
)

// discoveryConnAdapter adapts ssh.DiscoveryConn to completion.ExecConn so
// the SSH completer can run its completion script through the same
// pooled connection lane the discovery ladder uses.
type discoveryConnAdapter struct {
	inner ssh.DiscoveryConn
}

func (a *discoveryConnAdapter) Exec(ctx context.Context, cmd string) (*completion.ExecResult, error) {
	r, err := a.inner.Exec(ctx, cmd)
	if err != nil {
		return nil, err
	}
	return &completion.ExecResult{
		Stdout:     r.Stdout,
		Stderr:     r.Stderr,
		ExitStatus: r.ExitStatus,
		Truncated:  r.Truncated,
	}, nil
}

func (a *discoveryConnAdapter) Close() error {
	return a.inner.Close()
}

type discoveryLeaseProvider interface {
	DiscoveryConn(context.Context, string, ...ssh.ConnectOption) (ssh.DiscoveryConn, error)
}

// routedSSHCompleter binds the completion engine to the SSH client without
// making internal/completion import internal/ssh. Every call receives the
// exact options captured from the live session.
type routedSSHCompleter struct {
	client discoveryLeaseProvider
}

func (c *routedSSHCompleter) Complete(ctx context.Context, req completion.Request, opts ...ssh.ConnectOption) (*completion.Response, error) {
	return completion.NewSSH(sshExecConnProvider(c.client, opts...)).Complete(ctx, req)
}

// sshExecConnProvider returns an ExecConnProvider backed by the SSH client's
// DiscoveryConn. opts are the terminal session's original connect options;
// forwarding them is what makes jump routes and the pool key identical.
func sshExecConnProvider(client discoveryLeaseProvider, opts ...ssh.ConnectOption) completion.ExecConnProvider {
	return func(ctx context.Context, host string) (completion.ExecConn, error) {
		dc, err := client.DiscoveryConn(ctx, host, opts...)
		if err != nil {
			return nil, err
		}
		return &discoveryConnAdapter{inner: dc}, nil
	}
}
