package app

// The coordinator's use of THIS MACHINE'S HELPER as its ssh transport
// (nocx-50w7p.3, plan §3).
//
// # What moves, and what does not
//
// The owner's invariant (2026-09-13) is that there is no ssh connection without
// a helper: the LOCAL helper dials, the coordinator holds no ssh client once
// the consumers have moved. What moves for a consumer is the TRANSPORT
// underneath it and nothing else — the deploy code, the publish rules, the
// lease semantics and every test of them stay where they are — and this file is
// where the transport is swapped.
//
// # Which consumers are here, and which are still the coordinator's
//
// The install/uninstall lease, the git lane and the settings probe (the last
// two since nocx-50w7p.10). The other consumers of the same epic (files and the
// bundle publish, forwards and the remote lifecycle tunnel, discovery's probes)
// still dial from this process and are moved one per task; a consumer is moved
// WHOLLY or not at all, which is why this file's leaseRoutes names each of them
// out loud rather than pretending the migration is finished.
//
// The two that moved here are the two whose carrier was not a channel anybody
// else wanted: a lane is an exec session on the pooled connection, and a probe
// is a dial that leaves nothing behind. Both are one helper op each
// (internal/helper/sshsvc), and neither leaves a coordinator dial behind — the
// probe's old path was RealClient.ProbeConfig, which is deleted with the lane's
// ssh.HelperConn.
//
// # Why the destination is resolved here and not by the helper
//
// Resolution — an alias through ~/.ssh/config, and the credential's own
// authorization against the endpoint its profile names — belongs to the party
// that reads the config and holds the binding, which is this process
// (ssh.RealClient.ResolveTarget). The helper is handed an address, an account
// and a REFERENCE to material, and it decides nothing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/vault"
)

// sshOverHelper opens ssh channels on this machine's helper.
type sshOverHelper struct {
	// local is the connection to this machine's daemon — the same one every
	// local pane rides (helper_local.go's "one connection, every pane"), which
	// is what makes an install and a file listing to one host share one
	// transport rather than raising a second daemon connection per consumer.
	local *localHelperOpener
	// resolve is the coordinator's own resolver and NOT its dialer: this type
	// never calls Connect, FSConn, TunnelConn or any sibling, and the only
	// methods it touches are the resolution seam and the error vocabulary
	// (ssh.RealClient.ResolveTarget).
	resolve *ssh.RealClient
	log     *slog.Logger
}

// installLeaseProvider is the narrow surface the install path needs. It is
// narrower than helperInstallProvider on purpose: the composite at the
// composition root hands the lane and the platform probe to the coordinator's
// own dials and this one lease to the helper, so the two halves of the factory
// can move independently without either pretending to be the other.
type installLeaseProvider interface {
	HelperInstallConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.HelperInstallConn, error)
}

// laneProvider is the exec lane a helper rides to a remote helper's bridge
// (design D19), and since nocx-50w7p.10 it is THIS MACHINE'S HELPER that owns
// it: the coordinator no longer opens an ssh exec session at all, so the
// interface is a seam over the helper op rather than over RealClient.HelperConn
// (deleted).
type laneProvider interface {
	// LaneConn opens one exec lane on the helper's pooled connection for host,
	// running the installed helper of generation under machine's install
	// identity in its bridge subcommand, and answers the carrier the helper
	// client rides.
	LaneConn(ctx context.Context, host string, machine proto.Machine, generation proto.GenerationID, opts ...ssh.ConnectOption) (helperclient.HelperConn, error)
}

// LaneConn opens the exec lane a remote helper's bridge rides.
//
// The command is not built here and does not cross: the helper derives it from
// the machine and the generation (proto.LaneParams, D3), which is also why this
// method takes those two facts rather than the command path the install knows.
func (h *sshOverHelper) LaneConn(ctx context.Context, host string, machine proto.Machine, generation proto.GenerationID, opts ...ssh.ConnectOption) (helperclient.HelperConn, error) {
	local, _, err := h.local.connect(ctx)
	if err != nil {
		return nil, err
	}
	target, err := h.resolve.ResolveTarget(ctx, host, opts...)
	if err != nil {
		return nil, err
	}
	lane, err := local.OpenLane(ctx, proto.LaneParams{
		Destination: destinationOf(target),
		Machine:     machine,
		Generation:  generation,
	})
	if err != nil {
		return nil, h.translate(host, err)
	}
	return lane, nil
}

// ProbeWithResult asks this machine's helper whether a credential works
// against a host, and answers the host-key fingerprint the handshake saw
// (empty when it never reached one).
//
// It is the settings surface's connection test, and it is a DIAL: under the
// owner's invariant dials are the helper's, so this is the same question
// RealClient.ProbeConfig used to answer from this process (deleted with the
// lane's HelperConn). What is NOT the helper's is the host-key DECISION: the
// helper asks, this coordinator answers from its own known_hosts through the
// reverse registry (helper_reverse.go), and the verdict travels back
// (internal/helper/sshsvc).
func (h *sshOverHelper) ProbeWithResult(ctx context.Context, host string, cfg *ssh.ConnectConfig) (string, error) {
	local, _, err := h.local.connect(ctx)
	if err != nil {
		return "", err
	}
	// The resolved profile becomes connect options and is resolved again by the
	// SAME resolver the coordinator's own dial used (ResolveTarget) — alias
	// through ~/.ssh/config, the credential's authorization against the
	// endpoint its profile names, and the destination the helper is handed.
	// The options are the resolver's own conversion (session.SSHOptionsFromConfig),
	// so a field this path forgets is a field that path forgot.
	target, err := h.resolve.ResolveTarget(ctx, host, session.SSHOptionsFromConfig(cfg)...)
	if err != nil {
		return "", fmt.Errorf("probe config: %w", err)
	}
	var result proto.ProbeResult
	if err := local.Call(ctx, proto.ServiceSSH, proto.OpProbe, proto.ProbeParams{
		Host: target.Host, Port: target.Port, User: target.User,
		Identity: identityOf(target),
		// FALSE, and it is the same decision the coordinator's own probe made:
		// it never accepted a key on trust. First contact is answered as
		// host-key-unknown with the evidence, the accept sheet is raised from
		// that evidence, and the WRITE happens in the separate
		// connections.trustHostKey act.
		AcceptOnTrust: false,
	}, &result); err != nil {
		return "", h.translateProbe(target, err)
	}
	return probeAnswer(target, result)
}

// translateProbe re-types a helper refusal into the error this coordinator's
// probe callers already classify.
//
// It translates a REFUSAL and never an outcome, and the line between the two is
// the whole of it: a refusal means the probe did not happen (bad params, no
// coordinator to ask, this helper's own machinery), while a probe that DID
// happen answers a classified result — rejected, unreachable, needs-interactive
// — which probeAnswer reconstructs. Rebuilding those from a refusal would be a
// second classifier beside the one that already exists (AD-8).
//
// The two host-key codes are here because they are the SERVICE's vocabulary for
// one fact rather than the probe's: a refused channel ends in them with the same
// evidence, and an unknown or changed key has to reach the accept sheet as the
// type that sheet switches on whichever op met it.
func (h *sshOverHelper) translateProbe(target ssh.DialTarget, err error) error {
	var refusal *helperclient.RefusalError
	if !errors.As(err, &refusal) {
		return fmt.Errorf("probe: %w", err)
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
	case proto.ErrCodeVaultSealed:
		// The material could not be read because the vault is sealed. The
		// helper's code is its own spelling; what every surface above this one
		// switches on is this package's vault sentinel — rpcErrorFor builds the
		// `data.reason` the renderer turns into an unlock dialog out of it — so
		// it is REBUILT rather than left to a message a normalizer would have to
		// fingerprint.
		return fmt.Errorf("probe %s: %w", target.User+"@"+target.Host, vault.ErrVaultSealed)
	}
	// Everything else keeps the helper's own code and sentence, prefixed with
	// the act and the destination: a sealed vault reaches the renderer as the
	// reason it can act on (the message the normalizer fingerprints carries the
	// vault's own sentence), and a protocol mistake stays this coordinator's
	// failure rather than being folded into a probe outcome.
	return fmt.Errorf("probe %s: %w", target.User+"@"+target.Host, err)
}

// probeAnswer turns the helper's classified result into the pair the transport
// classifies and the fingerprint it stores.
//
// The result is RECONSTRUCTED as the typed error the coordinator's own probe
// would have returned, field for field, and that is not ceremony: the transport
// classifies an ERROR (ssh.ClassifyProbeError), the renderer's host-key sheet is
// built from the two host-key error TYPES, and a classified outcome carried
// across as a value would be a second classifier beside the one that already
// exists (AD-8).
func probeAnswer(target ssh.DialTarget, result proto.ProbeResult) (string, error) {
	switch result.Outcome {
	case proto.ProbeAccepted:
		return result.Fingerprint, nil
	case proto.ProbeHostKeyUnknown:
		if ev := result.HostKey; ev != nil {
			return result.Fingerprint, &ssh.ErrUnknownHostKey{
				Addr: ev.Addr, KnownHostsAddr: ev.KnownHostsAddr, KeyAlgo: ev.Algorithm,
				Fingerprint: ev.Fingerprint, Key: ev.Key,
			}
		}
		return result.Fingerprint, fmt.Errorf("probe: %s", result.Detail)
	case proto.ProbeHostKeyChanged:
		if ev := result.HostKey; ev != nil {
			return result.Fingerprint, &ssh.ErrHostKeyMismatch{
				Addr: ev.Addr, KnownHostsAddr: ev.KnownHostsAddr, KeyAlgo: ev.Algorithm,
				Fingerprint: ev.Fingerprint, Expected: ev.Expected, Key: ev.Key,
			}
		}
		return result.Fingerprint, fmt.Errorf("probe: %s", result.Detail)
	case proto.ProbeRejected:
		// A refused credential is an ANSWER (the server said no), so it is
		// rebuilt as the type ssh.ClassifyProbeError reads as `rejected` rather
		// than being reported as the probe not having run.
		return result.Fingerprint, &ssh.ErrAuthFailed{User: target.User, Host: target.Host, Err: errors.New(result.Detail)}
	case proto.ProbeNeedsInteractive:
		return result.Fingerprint, &ssh.ErrEncryptedKey{Path: result.Detail}
	case proto.ProbeUnreachable:
		return result.Fingerprint, unreachableError(target.Host, result.Detail)
	}
	// An outcome no generation has defined: refused rather than folded into
	// `rejected`, for the reason the helper refuses an unknown verdict.
	return result.Fingerprint, fmt.Errorf("probe: outcome %q is not one this coordinator knows", string(result.Outcome))
}

// unreachableError rebuilds the error the transport classifies as
// `unreachable` — a *net.OpError — around the sentence the helper's own dial
// produced.
//
// The type is what matters and it is the honest one: *net.OpError is what the
// coordinator's own probe returned when the host did not answer, and the
// sentence inside it is the helper's, verbatim, so a person reading the detail
// sees what the dial said rather than a description of it.
func unreachableError(host, detail string) error {
	return &net.OpError{
		Op:   "dial",
		Net:  "tcp",
		Addr: addrString(host),
		Err:  errors.New(detail),
	}
}

// addrString is a net.Addr for an address that arrived as a string.
type addrString string

func (a addrString) Network() string { return "tcp" }
func (a addrString) String() string  { return string(a) }

// destinationOf is the ONE conversion from a resolved target to the wire's
// destination: alias resolution and authorization are the resolver's
// (ResolveTarget), and everything that opens something on a host names what it
// resolved the same way.
func destinationOf(t ssh.DialTarget) proto.SSHDestination {
	return proto.SSHDestination{
		Host:     t.Host,
		Port:     t.Port,
		User:     t.User,
		Identity: identityOf(t),
	}
}

// identityOf is the credential half of the same conversion.
func identityOf(t ssh.DialTarget) proto.SSHIdentity {
	return proto.SSHIdentity{
		Credential: proto.SSHCredential{
			Ref:           string(t.Credential),
			PassphraseRef: string(t.Passphrase),
		},
		Auth:      proto.SSHAuthKind(t.Auth),
		PublicKey: t.PublicKey,
	}
}

// filesLeaseProvider is the narrow surface the file panel's factory needs, the
// same way installLeaseProvider is the installer's. *ssh.RealClient used to
// satisfy it; this machine's helper does now, which is the whole of
// nocx-50w7p.12's Files half.
type filesLeaseProvider interface {
	FSConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.FSConn, error)
}

// HelperInstallConn acquires the write-capable install lease over an SFTP
// stream this machine's helper opened.
//
// The channel is opened ONCE per lease and closed with it, which is the whole
// of the resource story: `ssh.open` takes a pooled reference on the helper
// side, `ssh.close` releases it, and the connection itself lives on in the pool
// for whatever asks next (AD-4).
func (h *sshOverHelper) HelperInstallConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.HelperInstallConn, error) {
	stream, err := h.openChannel(ctx, proto.ChannelSFTP, host, opts)
	if err != nil {
		return nil, err
	}
	conn, err := ssh.NewHelperInstallConn(ctx, stream)
	if err != nil {
		// The stream is released whichever way this fails: an open channel
		// nobody holds is a pooled reference the helper keeps for a caller
		// that has given up.
		_ = stream.Close()
		return nil, fmt.Errorf("helper install lease for %s: %w", host, err)
	}
	return conn, nil
}

// FSConn acquires the file manager's SFTP lease over a channel this machine's
// helper opened (nocx-50w7p.12).
//
// The lease is the SAME lease the panel has always held — one bounded lane, a
// hard timeout, close-to-cancel, the typed error ladder (ssh.NewFSConn) — and
// that is deliberate: what moved is the transport, not the consumer. What the
// helper adds is the connection it rides. One pooled connection per
// (destination, identity) carries the pane's shell channel AND this sftp
// channel, so the file panel and the terminal it belongs to authenticate once
// (AD-4), and the reference is the helper's: closing this lease closes the
// channel (`ssh.close`), and the connection lives on for whoever asks next.
func (h *sshOverHelper) FSConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.FSConn, error) {
	stream, err := h.openChannel(ctx, proto.ChannelSFTP, host, opts)
	if err != nil {
		return nil, err
	}
	lease, err := ssh.NewFSConn(ctx, stream)
	if err != nil {
		// The stream is released whichever way this fails: an open channel
		// nobody holds is a pooled reference the helper keeps for a caller
		// that has given up.
		_ = stream.Close()
		return nil, fmt.Errorf("sftp provider for %s: %w", host, err)
	}
	return lease, nil
}

// UninstallHelper removes the helper install tree from a host, over the same
// lease the installer uses. It is the transport.RemoteHelperUninstaller
// capability the composition root wires (D25).
//
// D25's ordering is unchanged and still the caller's: every live helper channel
// on the machine is closed BEFORE this runs, because no helper may be running
// out of a directory being deleted.
func (h *sshOverHelper) UninstallHelper(ctx context.Context, host string, opts ...ssh.ConnectOption) (bool, error) {
	conn, err := h.HelperInstallConn(ctx, host, opts...)
	if err != nil {
		return false, fmt.Errorf("ssh: helper uninstall %s: %w", host, err)
	}
	defer func() { _ = conn.Close() }()
	removed, err := ssh.UninstallHelperTree(ctx, conn)
	if err != nil {
		return removed, fmt.Errorf("ssh: helper uninstall %s: %w", host, err)
	}
	return removed, nil
}

// openChannel resolves the destination, asks this machine's helper to open one
// channel of the given kind, and answers the stream.
func (h *sshOverHelper) openChannel(ctx context.Context, kind proto.ChannelKind, host string, opts []ssh.ConnectOption) (*helperclient.ChannelStream, error) {
	client, _, err := h.local.connect(ctx)
	if err != nil {
		return nil, err
	}
	target, err := h.resolve.ResolveTarget(ctx, host, opts...)
	if err != nil {
		return nil, err
	}
	stream, err := client.OpenChannel(ctx, proto.OpenChannelParams{
		Destination: destinationOf(target),
		Kind:        kind,
		// FALSE, and it is not a default: a channel open has no caller that
		// can answer the accept flow (that flow belongs to a pane open, where
		// a person is watching). An unknown host key therefore comes back as
		// ErrUnknownHostKey and the existing accept path raises exactly the
		// sheet it always did — which is also why the helper's `open` reports
		// it as its own refusal code rather than as an internal failure.
		AcceptOnTrust: false,
	})
	if err != nil {
		return nil, h.translate(host, err)
	}
	return stream, nil
}

// translate re-types a helper refusal into the error this coordinator's callers
// already switch on.
//
// It is the one place the migration has to be careful, and the reason is that
// Go values do not cross a process boundary: the helper raises the SAME typed
// errors this package's ssh client raises — ssh.ErrUnknownHostKey,
// ssh.ErrHostKeyMismatch — and the classifier that reads them is one process
// away (sshsvc.classifyChannelError). What arrives here is a code and the
// evidence, so the typed error is REBUILT, field for field. Without this the
// accept sheet, the mismatch warning and the transport's hostKeyInfoFromError
// would all see an opaque helper failure where they used to see evidence.
func (h *sshOverHelper) translate(host string, err error) error {
	var refusal *helperclient.RefusalError
	if !errors.As(err, &refusal) {
		return fmt.Errorf("ssh: open a channel to %s: %w", host, err)
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
		// reports these as *ErrAuthFailed and as the network error underneath,
		// and the caller that acts on the distinction is a person reading the
		// sentence, which is preserved verbatim.
		return fmt.Errorf("ssh: open a channel to %s: %s", host, refusal.Message)
	case proto.ErrCodeChannelRefused:
		// The far side will not serve this channel. It is typed as the
		// coordinator's own subsystem refusal because that is what the file
		// and install paths report for the same fact, and because the sftp
		// subsystem is the only channel kind this generation opens: a refusal
		// at the session and a refusal at the subsystem are one answer to a
		// caller that cannot act differently on them (proto's own note on the
		// code).
		return fmt.Errorf("%w: %s", ssh.ErrFSSubsystemRefused, refusal.Message)
	}
	// Anything else keeps the helper's own sentence and its code, prefixed
	// with what this process was doing: a caller reading the log should see
	// the act that failed and the refusal that stopped it, in that order.
	return fmt.Errorf("ssh: open a channel to %s: %w", host, err)
}

// decodeHostKeyEvidence reads the structured half of a host-key refusal. A
// payload that will not decode yields no error of its own: the refusal is what
// the caller acts on, and an evidence-less one degrades to the generic branch
// rather than to no answer at all.
func decodeHostKeyEvidence(raw json.RawMessage) (proto.HostKeyEvidence, bool) {
	if len(raw) == 0 {
		return proto.HostKeyEvidence{}, false
	}
	var ev proto.HostKeyEvidence
	if err := json.Unmarshal(raw, &ev); err != nil || ev.Addr == "" {
		return proto.HostKeyEvidence{}, false
	}
	return ev, true
}

// installLeaseRoutes is the composition root's answer to "who serves this
// lease", for the factory that needs three of them.
//
// It is a DISPATCH and not a policy, exactly like hostedOpeners: each method is
// one lease and each lease has exactly one owner. Every lease this factory
// needs is this machine's helper's: the exec lane a remote helper's bridge
// rides (nocx-50w7p.10), the write-capable install lease (nocx-50w7p.3) and the
// file panel's sftp lease (nocx-50w7p.12) through `viaLocal`, and the platform
// probe (nocx-50w7p.9) through `probes` — which is the same helper, reached as
// a named op on a probe lease rather than as a channel. There is no `direct`
// field and no coordinator dial left in this dispatch, and that is the state
// the epic's invariant asks for rather than a step on the way to it.
type installLeaseRoutes struct {
	viaLocal helperLeases  // the leases this machine's helper answers
	probes   *helperProbes // the platform probe: a named op on a lease
}

// helperLeases is what this machine's helper answers through this dispatch: the
// exec lane the git factory rides, the install lease and the file panel's,
// each acquired as one channel on the pooled connection it names. It is a
// composite of the narrow surfaces rather than a wider method set, so a
// consumer still declares exactly the one it needs (and the lane's own
// interface stays where its implementation is, next to the op it names).
type helperLeases interface {
	laneProvider
	installLeaseProvider
	filesLeaseProvider
}

func (r installLeaseRoutes) LaneConn(ctx context.Context, host string, machine proto.Machine, generation proto.GenerationID, opts ...ssh.ConnectOption) (helperclient.HelperConn, error) {
	return r.viaLocal.LaneConn(ctx, host, machine, generation, opts...)
}

// DiscoveryConn is the platform probe (D20), answered by this machine's helper
// as the typed `ssh.uname` op.
//
// It keeps the ssh.DiscoveryConn SHAPE — and that shape is one command wide,
// which `platformLease.Exec` refuses anything outside of — because its caller
// is helper_git.go's probeExec, a conversion into the deploy package's own
// one-command seam. What crosses to the helper is a lease and an op name: no
// command reaches the wire (nocx-50w7p.9).
func (r installLeaseRoutes) DiscoveryConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.DiscoveryConn, error) {
	return r.probes.HelperPlatformProbe(ctx, host, opts...)
}

func (r installLeaseRoutes) HelperInstallConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.HelperInstallConn, error) {
	return r.viaLocal.HelperInstallConn(ctx, host, opts...)
}

// FSConn is the file panel's lease, and it is the helper's now (nocx-50w7p.12) —
// the same `viaLocal` half HelperInstallConn and LaneConn use, because one value
// answering for three leases of the same owner is one answer rather than three
// that agree today.
func (r installLeaseRoutes) FSConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.FSConn, error) {
	return r.viaLocal.FSConn(ctx, host, opts...)
}

// The two assertions worth writing here: the dispatch must satisfy the
// interface the git factory takes and the one the file-panel factory takes, or
// a wiring line stops compiling — which is the check, and it is why there is no
// assertion for the uninstaller (that one is satisfied at its own wiring line,
// where transport names the interface).
var (
	_ helperInstallProvider = installLeaseRoutes{}
	_ filesLeaseProvider    = installLeaseRoutes{}
)
