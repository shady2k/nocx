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
// The install/uninstall lease, and only it. The other consumers of the same
// epic (files and the bundle publish, forwards and the remote lifecycle tunnel,
// discovery's probes, the git lane, the settings probe) still dial from this
// process and are moved one per task; a consumer is moved WHOLLY or not at all,
// which is why this file's leaseRoutes names each of them out loud rather than
// pretending the migration is finished.
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

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/ssh"
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
		Kind: kind,
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
// one lease and each lease has exactly one owner. The install lease is this
// machine's helper's; the git lane and the platform probe are still the
// coordinator's OWN dials, which is the state of the epic rather than a
// preference — they are separate consumers and each moves in its own task. A
// reader looking for "which leases still dial" gets the answer from this
// struct's two fields and nothing else.
type installLeaseRoutes struct {
	direct   directLanes          // the lanes whose dial is still the coordinator's
	viaLocal installLeaseProvider // the install lease, opened by this machine's helper
}

// directLanes is what the coordinator's own client still answers here. It is
// declared as its own interface rather than reusing helperInstallProvider so
// that the install method cannot be satisfied by BOTH halves: a type that
// filled in this field and implemented HelperInstallConn too would compile, and
// the second dial path would be invisible.
type directLanes interface {
	helperLaneProvider
	DiscoveryConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.DiscoveryConn, error)
}

func (r installLeaseRoutes) HelperConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.HelperConn, error) {
	return r.direct.HelperConn(ctx, host, opts...)
}

func (r installLeaseRoutes) DiscoveryConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.DiscoveryConn, error) {
	return r.direct.DiscoveryConn(ctx, host, opts...)
}

func (r installLeaseRoutes) HelperInstallConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.HelperInstallConn, error) {
	return r.viaLocal.HelperInstallConn(ctx, host, opts...)
}

// The one assertion worth writing here: the dispatch above must satisfy the
// interface the git factory takes, or the wiring line stops compiling — which
// is the check, and it is why there is no second assertion for the uninstaller
// (that one is satisfied at its own wiring line, where transport names the
// interface).
var _ helperInstallProvider = installLeaseRoutes{}
