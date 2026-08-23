package app

import (
	"context"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/commandnames"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
)

// commandNamesRouter is the composition root's half of command discovery: it
// decides, from a live session's immutable target facts, WHICH source the
// shared cache should ask — this machine under a supervised process group,
// or one resolved SSH route over the same pooled discovery lane completion
// and port discovery use.
//
// The routing lives here and not in internal/commandnames for the reason
// completion's does not either: the package would otherwise import
// internal/ssh, and a cache that knows about connect options is a cache with
// two jobs.
type commandNamesRouter struct {
	svc    *commandnames.Service
	client discoveryLeaseProvider
}

// CommandNames implements transport.CommandNamesResolver.
func (r *commandNamesRouter) CommandNames(ctx context.Context, target capability.SessionTarget) commandnames.Result {
	gen := shellintegration.ScriptVersion()
	switch target.Kind {
	case session.KindLocal:
		return r.svc.Names(ctx, commandnames.NewLocalSource(gen, nil))
	case session.KindRemote:
		if r.client == nil {
			return commandnames.Result{
				State:  commandnames.StateFailed,
				Reason: "command discovery has no connection to this host",
			}
		}
		// The route identity is the host the session actually reached, the
		// same string the completion adapter leases on — so two aliases for
		// one host share one scan rather than scanning twice. The remote
		// user and the effective PATH are not guessed from here: the probe
		// reports them from the far side and they are part of the key, so a
		// route reached as two different users never shares one name set.
		return r.svc.Names(ctx, commandnames.NewRemoteSource("ssh:"+target.Host, gen,
			sshCommandNamesProvider(r.client, target.Host, target.SSHOptions...)))
	default:
		return commandnames.Result{
			State:  commandnames.StateFailed,
			Reason: "command discovery does not know this session kind",
		}
	}
}

// commandNamesConnAdapter adapts ssh.DiscoveryConn to commandnames.ExecConn.
// It is the twin of discoveryConnAdapter and exists for the same reason: the
// feature package declares the one method it needs rather than importing
// internal/ssh.
type commandNamesConnAdapter struct {
	inner ssh.DiscoveryConn
}

func (a *commandNamesConnAdapter) Exec(ctx context.Context, cmd string) (*commandnames.ExecResult, error) {
	r, err := a.inner.Exec(ctx, cmd)
	if err != nil {
		return nil, err
	}
	return &commandnames.ExecResult{
		Stdout:     r.Stdout,
		ExitStatus: r.ExitStatus,
		Truncated:  r.Truncated,
	}, nil
}

func (a *commandNamesConnAdapter) Close() error { return a.inner.Close() }

// sshCommandNamesProvider leases the pooled connection with the terminal
// session's own connect options. Forwarding them is what makes a jump route
// reuse the session's connection instead of silently dialing the target
// directly — the same contract the completion adapter documents.
func sshCommandNamesProvider(client discoveryLeaseProvider, host string, opts ...ssh.ConnectOption) commandnames.ExecConnProvider {
	return func(ctx context.Context) (commandnames.ExecConn, error) {
		dc, err := client.DiscoveryConn(ctx, host, opts...)
		if err != nil {
			return nil, err
		}
		return &commandNamesConnAdapter{inner: dc}, nil
	}
}
