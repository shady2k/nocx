package app

// The coordinator's consumers of the helper's NAMED PROBES (nocx-50w7p.9).
//
// # What this file is
//
// Every shell question this process used to answer by dialing ssh itself — what
// platform is this host, where is its home, which ports are listening, what
// does its shell think the line should be, which commands are on its PATH — is
// now a named op this machine's helper answers (`ssh.uname`, `ssh.home`,
// `ssh.sample-ports`, `ssh.completion`, `ssh.command-names`, on a lease the
// coordinator holds). This is the adapter layer that turns each consumer's own
// narrow seam into that op, and it exists for the reason every adapter in this
// package does: the feature packages must not import the helper client, and the
// resolution the helper must not do (an alias through ~/.ssh/config, a
// credential's authorization against the endpoint it names) happens here.
//
// # The consumer keeps its seam; only the transport underneath moves
//
// internal/discovery still owns its ladder, its framing rule and its five
// states; internal/completion still owns its parser and its soft failures;
// internal/commandnames still owns its cache and its deadlines. What changed is
// what answers `Sample`, `Complete` and `Enumerate`: a lease on the helper's
// pooled connection, and no command composed here at all — the text lives in
// internal/remoteprobe, which the helper links too.
//
// # The platform probe's seam, and why it looks like the old one
//
// `probeExec` in helper_git.go maps a DiscoveryConn to the deploy package's
// one-command seam, and that caller is out of this task's bounds. It is served
// here by `platformLease`, which implements the same interface and sends NO
// command: the only argument deploy's probe ever passes is its own fixed uname
// command, and this adapter answers exactly that one from the helper's typed
// op and refuses anything else locally, before it could reach a wire. See
// `platformExec.Exec` for the refusal and `TestTheOnlyExecutableSeamIsBounded`
// for the proof that nothing else is accepted.

import (
	"context"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/commandnames"
	"github.com/shady2k/nocx/internal/completion"
	"github.com/shady2k/nocx/internal/discovery"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/remoteprobe"
	"github.com/shady2k/nocx/internal/ssh"
)

// helperProbes answers the coordinator's probes on this machine's helper.
//
// It holds the same two collaborators sshOverHelper does, and for the same
// reasons: the connection to this machine's daemon (one daemon connection per
// coordinator, not one per consumer) and the coordinator's own resolver, which
// is the party that reads ~/.ssh/config and holds the credential binding.
//
// Both are interfaces rather than the concrete types the composite is built
// from, so the one thing this file must not get wrong — that the destination
// handed to the helper is the RESOLVED one and carries the session's own route
// — is assertable without a daemon. A composition root that could only be tested
// by running the product is how a jump route silently becomes a direct dial.
type helperProbes struct {
	local   probeHelperSource
	resolve probeTargetResolver
}

// probeHelperSource is this machine's daemon, as the probes need it.
type probeHelperSource interface {
	probeHelper(ctx context.Context) (probeHelper, error)
}

// probeHelper is a helper connection that can acquire one probe lease.
type probeHelper interface {
	AcquireProbeLease(ctx context.Context, params proto.LeaseParams) (probeCommands, error)
}

// probeTargetResolver is the coordinator's own resolution: an alias through
// ~/.ssh/config, and a credential's authorization against the endpoint it names.
type probeTargetResolver interface {
	ResolveTarget(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.DialTarget, error)
}

// probeHelperFor reaches this machine's daemon and returns the lease acquirer.
func (o *localHelperOpener) probeHelper(ctx context.Context) (probeHelper, error) {
	c, _, err := o.connect(ctx)
	if err != nil {
		return nil, err
	}
	return helperLeaseAcquirer{client: c}, nil
}

// helperLeaseAcquirer adapts the helper client's own lease type to the interface
// this package's adapters consume. It exists because Go requires an exact method
// signature to satisfy an interface, and the client returns its concrete lease.
type helperLeaseAcquirer struct {
	client *helperclient.Client
}

func (a helperLeaseAcquirer) AcquireProbeLease(ctx context.Context, params proto.LeaseParams) (probeCommands, error) {
	lease, err := a.client.AcquireProbeLease(ctx, params)
	if err != nil {
		return nil, err
	}
	return lease, nil
}

// lease acquires one probe lease for a destination and returns the adapter a
// consumer's seam is satisfied by.
//
// The destination is RESOLVED here and travels as an address, an account and a
// credential reference: the helper reads no config, holds no secret at rest and
// decides nothing about what an address means.
func (p *helperProbes) acquire(ctx context.Context, host string, opts ...ssh.ConnectOption) (*helperProbeLease, error) {
	if p == nil {
		// A composition that did not wire the helper's probes cannot serve
		// them. It is refused by NAME rather than left to a nil dereference,
		// and it is not a fallback: the coordinator has no dialer to fall
		// back to any more, which is the invariant this whole level exists to
		// hold.
		return nil, errors.New("ssh: the helper's probes are not wired at this composition root")
	}
	target, err := p.resolve.ResolveTarget(ctx, host, opts...)
	if err != nil {
		return nil, err
	}
	// Resolution FIRST, then the daemon: the destination the helper is handed
	// must be resolved before anything is dialed, and an unresolvable one is
	// refused without waking the helper at all.
	helper, err := p.local.probeHelper(ctx)
	if err != nil {
		return nil, err
	}
	lease, err := helper.AcquireProbeLease(ctx, proto.LeaseParams{
		Destination: proto.SSHDestination{
			Host: target.Host,
			Port: target.Port,
			User: target.User,
			Identity: proto.SSHIdentity{
				Credential: proto.SSHCredential{
					Ref:           string(target.Credential),
					PassphraseRef: string(target.Passphrase),
				},
				Auth:      proto.SSHAuthKind(target.Auth),
				PublicKey: target.PublicKey,
			},
		},
		// FALSE, and it is not a default: a probe can raise no accept sheet —
		// the question "may this host key be recorded" belongs to an act a
		// person is watching, and discovery, completion and the install
		// platform probe are questions the product asked itself. An unknown
		// key therefore comes back as ssh.ErrUnknownHostKey and follows the
		// path it always did, rather than being recorded behind a user's back.
		AcceptOnTrust: false,
	})
	if err != nil {
		return nil, translateProbeRefusal(host, err)
	}
	return &helperProbeLease{lease: lease}, nil
}

// Lease answers discovery's Connector: one lease per target, held for as long
// as the target's detector lives.
//
// It is the interface method itself rather than a wrapper around one, because
// what discovery acquires and what this holds are the same thing: a reference to
// a pooled connection that reports its own death.
func (p *helperProbes) Lease(ctx context.Context, host string, opts ...ssh.ConnectOption) (discovery.Lease, error) {
	return p.acquire(ctx, host, opts...)
}

// HelperCompletionProvider answers completion's ProbeConnProvider: one lease per
// completion request, released with it.
func (p *helperProbes) HelperCompletionProvider(opts ...ssh.ConnectOption) completion.ProbeConnProvider {
	return func(ctx context.Context, host string) (completion.ProbeConn, error) {
		l, err := p.acquire(ctx, host, opts...)
		if err != nil {
			return nil, fmt.Errorf("completion lease: %w", err)
		}
		return l, nil
	}
}

// HelperCommandNamesProvider answers commandnames' ExecConnProvider: one lease
// per enumeration half, released with it.
func (p *helperProbes) HelperCommandNamesProvider(host string, opts ...ssh.ConnectOption) commandnames.ExecConnProvider {
	return func(ctx context.Context) (commandnames.ExecConn, error) {
		l, err := p.acquire(ctx, host, opts...)
		if err != nil {
			return nil, fmt.Errorf("commandnames lease: %w", err)
		}
		return l, nil
	}
}

// HelperPlatformProbe answers the install path's lane: a lease on the helper,
// as the ssh.DiscoveryConn the deploy package's probe is handed through.
func (p *helperProbes) HelperPlatformProbe(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.DiscoveryConn, error) {
	l, err := p.acquire(ctx, host, opts...)
	if err != nil {
		return nil, fmt.Errorf("probe lease for %s: %w", host, err)
	}
	return &platformLease{helperProbeLease: l, host: host}, nil
}

// probeCommands is the lease as everything in this package uses it: the named
// probes, the identity the install path keys consent by, and the lifetime
// signals. It is an interface so the adapters above can be driven without a
// helper — the client lease is the only production implementation, and a test
// that had to stand up a daemon to ask "does an unknown command reach the wire"
// is a test nobody writes.
type probeCommands interface {
	Uname(ctx context.Context) (*remoteprobe.Result, error)
	Home(ctx context.Context) (string, error)
	SamplePorts(ctx context.Context, probe remoteprobe.PortProbe) (*remoteprobe.Result, error)
	Completion(ctx context.Context, cwd, line string, pos, limit int, nonce string) (*remoteprobe.Result, error)
	CommandNames(ctx context.Context, phase remoteprobe.CommandNamesPhase, nonce string) (*remoteprobe.Result, error)
	Fingerprint() string
	Done() <-chan struct{}
	LostErr() error
	Close() error
}

// helperProbeLease is one probe lease, as every consumer seam this package
// serves.
//
// One type for four interfaces and not four types, because the thing behind
// them is one: a reference to a pooled connection on which named probes run.
// Four wrappers would be four places to translate the same refusal.
type helperProbeLease struct {
	lease probeCommands
}

// Home answers where the far account's home directory is, for the one caller
// that is not one of those four seams: the bundle publish, which holds a lease
// for the lifetime of the publish so that its sftp channel lands on the same
// pooled connection (helper_publish.go).
func (l *helperProbeLease) Home(ctx context.Context) (string, error) { return l.lease.Home(ctx) }

// Sample answers discovery's ExecConn: one rung of the port ladder, named.
func (l *helperProbeLease) Sample(ctx context.Context, probe discovery.ProbeName) (*discovery.ExecResult, error) {
	return l.lease.SamplePorts(ctx, probe)
}

// Done and LostErr are discovery's connection-loss signal, which the scheduler
// watches. They are the client lease's own, unchanged: the helper's pooled
// connection is what dies, and the lease learns it either when the coordinator's
// connection to the helper drops or when a probe comes back saying the far
// transport is gone.
func (l *helperProbeLease) Done() <-chan struct{} { return l.lease.Done() }
func (l *helperProbeLease) LostErr() error        { return l.lease.LostErr() }

// Complete answers completion's ProbeConn: one completion probe, with the typed
// arguments the fixed script takes.
func (l *helperProbeLease) Complete(ctx context.Context, probe completion.CompletionProbe) (*completion.ExecResult, error) {
	return l.lease.Completion(ctx, probe.Cwd, probe.Line, probe.Pos, probe.Limit, probe.Nonce)
}

// Enumerate answers commandnames' ExecConn: one half of the PATH enumeration.
func (l *helperProbeLease) Enumerate(ctx context.Context, phase commandnames.Phase, nonce string) (*commandnames.ExecResult, error) {
	return l.lease.CommandNames(ctx, phase, nonce)
}

// Close releases the lease: the helper drops its pooled reference and this
// process forgets the id. The consumers call it exactly as they called their
// own lease's.
func (l *helperProbeLease) Close() error { return l.lease.Close() }

// platformLease is the install path's view of a probe lease: the ssh.DiscoveryConn
// shape `probeExec` in helper_git.go converts to the deploy package's
// one-command seam.
type platformLease struct {
	*helperProbeLease
	host string
}

// HostKeyFingerprint is the target host's host-key fingerprint as observed at
// dial time — the value the install path keys a consent decision by (ADR-0023),
// read off the lease exactly as it was before the dial moved.
func (l *platformLease) HostKeyFingerprint() string { return l.lease.Fingerprint() }

// Exec is the ONE exec-shaped method left in this coordinator, and it is closed
// over a single command.
//
// deploy's platform probe is the only caller, it passes its own fixed command
// (`uname -s -m`, spelled once in internal/remoteprobe), and this answers it
// from the helper's typed `ssh.uname` op. Anything else is refused HERE, before
// it could reach a wire: the value of this method is precisely that a caller
// cannot use it to run something else on somebody's machine.
func (l *platformLease) Exec(_ context.Context, cmd string) (*ssh.ExecResult, error) {
	if cmd != remoteprobe.UnameCommand {
		return nil, fmt.Errorf("%w: the platform probe is the only command this lease runs", errUnnamedProbeCommand)
	}
	result, err := l.lease.Uname(context.Background())
	if err != nil {
		return nil, err
	}
	return &ssh.ExecResult{
		Stdout:     result.Stdout,
		Stderr:     result.Stderr,
		ExitStatus: result.ExitStatus,
		Truncated:  result.Truncated,
	}, nil
}

// errUnnamedProbeCommand is a command this coordinator will not run on a remote
// host: the probe seam is one command wide, and anything else reaching it is a
// programming mistake rather than a host fact.
var errUnnamedProbeCommand = errors.New("ssh: this probe seam runs one named command")

// translateProbeRefusal re-types a lease's refusal in the vocabulary this
// process already switches on.
//
// It is the same discipline sshOverHelper.translate applies to a channel open,
// and it is needed for the same reason: the helper's dial raises the SAME typed
// errors this package's ssh client raises — ssh.ErrUnknownHostKey,
// ssh.ErrHostKeyMismatch — and nothing carries a Go value across the wire, so
// the typed error is rebuilt from the code and its evidence. An order that did
// not do this would turn the accept sheet and the mismatch warning into "the
// helper refused", which is the failure mode the channel plane's own translation
// exists to prevent.
func translateProbeRefusal(host string, err error) error {
	var refusal *helperclient.RefusalError
	if !errors.As(err, &refusal) {
		return fmt.Errorf("ssh: probe lease for %s: %w", host, err)
	}
	switch refusal.Code {
	case string(proto.ProbeHostKeyUnknown):
		if ev, ok := decodeHostKeyEvidence(refusal.Details); ok {
			return &ssh.ErrUnknownHostKey{
				Addr: ev.Addr, KnownHostsAddr: ev.KnownHostsAddr, KeyAlgo: ev.Algorithm,
				Fingerprint: ev.Fingerprint, Key: ev.Key,
			}
		}
	case string(proto.ProbeHostKeyChanged):
		if ev, ok := decodeHostKeyEvidence(refusal.Details); ok {
			return &ssh.ErrHostKeyMismatch{
				Addr: ev.Addr, KnownHostsAddr: ev.KnownHostsAddr, KeyAlgo: ev.Algorithm,
				Fingerprint: ev.Fingerprint, Expected: ev.Expected, Key: ev.Key,
			}
		}
	case string(proto.ProbeRejected), string(proto.ProbeUnreachable):
		// A credential the server refused, or a host that did not answer.
		// Neither is a typed error here — the coordinator's own dial path
		// reports these as *ErrAuthFailed and as the network error underneath
		// — and the caller that acts on the distinction is a person reading
		// the sentence, which is preserved verbatim.
		return fmt.Errorf("ssh: probe lease for %s: %s", host, refusal.Message)
	}
	return fmt.Errorf("ssh: probe lease for %s: %s", host, refusal.Message)
}
