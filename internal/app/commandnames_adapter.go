package app

import (
	"context"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/commandnames"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/shellintegration"
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
	probes *helperProbes
}

// CommandNames implements transport.CommandNamesResolver.
func (r *commandNamesRouter) CommandNames(ctx context.Context, target capability.SessionTarget) commandnames.Result {
	gen := shellintegration.ScriptVersion()
	switch target.Kind {
	case session.KindLocal:
		return r.svc.Names(ctx, commandnames.NewLocalSource(gen, nil))
	case session.KindRemote:
		if r.probes == nil {
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
			r.probes.HelperCommandNamesProvider(target.Host, target.SSHOptions...)))
	default:
		return commandnames.Result{
			State:  commandnames.StateFailed,
			Reason: "command discovery does not know this session kind",
		}
	}
}

// The enumeration's lease and the session's own connect options: forwarding
// them is what makes a jump route resolve to the connection the session itself
// reached instead of silently reaching past it — the same contract the
// completion adapter documents.
//
// There is no adapter between the two any more: the enumeration names a PHASE
// and passes its nonce, and the helper's own command builder owns the script
// (D3), so what is wired here is a provider rather than a translation.
