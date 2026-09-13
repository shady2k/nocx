//go:build nocx_local_ssh

// Package sshsvc is the helper's `ssh` service: the ONE place an ssh
// connection is dialed on this machine.
//
// # Why it is build-tagged, and why that is the point rather than a detail
//
// The helper runs on every machine, including hosts nobody here controls, and
// the SAME BYTES are both deployed there and installed here (D11). An ssh
// client is the one thing the deployed copy must not carry, so the two are
// different builds and only a build flag can tell them apart: `make helpers`
// builds the deployable artifacts untagged, and nocx_local_ssh — what
// `make helper-local` passes — links a client into the copy installed here.
//
// A helper built without the tag therefore has NO ssh service at all, and this
// is deliberate rather than a gap: it answers `unknown_service` to every op,
// which is an answer a coordinator can act on and the honest one for a machine
// that cannot dial (nocx-50w7p.1, plan §1).
//
// # What the helper can and cannot do here
//
// It dials, and that is all. It holds no secret at rest, writes no
// known_hosts, links no `ssh/knownhosts` (a forbidden import in this binary,
// enforced by internal/helper/deploy's dependency test), and cannot decide
// anything a person decides:
//
//   - the password to present is asked for over the connection that asked for
//     the probe, one challenge at a time;
//   - a signature is asked for the same way, with the private key never
//     leaving the coordinator;
//   - a host key it has never seen is ASKED about, and the answer it acts on
//     is the coordinator's verdict rather than a rule it could apply itself.
//
// # Which connection, when there is more than one
//
// A helper daemon serves several coordinators at once (D12's same-UID trust),
// so "the coordinator" is not a property of the process — it is a property of
// the REQUEST. The connection a request arrived on travels with it
// (host.WithConnection), and that is the one this service asks: a reader
// attached by coordinator A must never have its credentials answered by
// coordinator B, which is the defect the request-scoped connection exists to
// make impossible. A request that arrives with no connection on it cannot be
// answered at all, and is refused by name rather than guessed at.
package sshsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// Client is the daemon's ssh client, as this service uses it: dial a
// throwaway connection for a probe, and acquire a REF-COUNTED POOLED
// connection for a channel.
//
// Two methods rather than one because the two ops genuinely differ — a probe
// leaves nothing behind by design, a channel holds a pooled reference for its
// whole life — and one seam rather than two because both are the same
// question ("this machine's ssh client") asked of the same object. A helper
// given two seams could be wired with two clients, which is two pools for one
// host and the state AD-4 exists to prevent.
//
// It is an interface rather than the concrete *ssh.RealClient for the reason
// every seam in this repository is one — a test can then drive the service
// without a network — but NOT to hide the ssh package: the classification of a
// probe failure is ssh.ClassifyProbeError's, and the pool is ssh.RealClient's,
// on purpose, because a second vocabulary or a second pool for one fact is
// what AD-8 exists to prevent.
type Client interface {
	DialAuth(ctx context.Context, addr, host, user string, cfg *gossh.ClientConfig) (*gossh.Client, error)
	// AcquirePooled borrows a reference to the pooled connection for a
	// resolved destination, dialing it if this is the first reference. The
	// caller owns the reference and must Close it.
	AcquirePooled(ctx context.Context, spec ssh.PooledSpec) (*ssh.PooledConn, error)
}

// ProbeTimeout bounds one probe's handshake. It is the same number the
// coordinator's own probe path uses when a profile names none (ReadyTimeout's
// default), so a probe does not silently change its budget by moving to the
// helper. The caller's context bounds it too: the dial and the handshake are
// both context-aware, and the tighter of the two wins.
const ProbeTimeout = 30 * time.Second

// The named refusals this service raises itself.
var (
	// errNoAuthChannel is the state the owner's plan calls by this name (plan
	// §2): a helper with no coordinator connection to ask. An open is always
	// coordinator-initiated, so a dial here never lacks one; a RE-DIAL after a
	// coordinator replacement can, and the answer is this refusal — never a
	// hang, never a stored fallback, never a retry loop.
	errNoAuthChannel = errors.New("this helper has no coordinator connected to ask for credentials")
	// errBadProbeParams is a probe the helper will not send: a request that
	// does not name a host, a port, a user and material to authenticate with.
	// It is refused before anything is dialed, because a dial that cannot
	// authenticate would report the server's refusal as if it were an answer
	// about the credential.
	errBadProbeParams = errors.New("probe params are incomplete")
)

// Service answers the `ssh` service's ops on this helper.
//
// One instance serves every connection (the daemon constructs it once, beside
// the sessions), which is why it holds no per-connection state: what a request
// needs from its connection is carried on the request's context, not here.
type Service struct {
	client Client
	log    *slog.Logger

	// mu guards channels: the proxied channels this daemon holds, across every
	// connection it serves. The service is process-scoped (one per daemon,
	// beside the sessions) while the ssh connections are per-COORDINATOR, so
	// the registry is one map and the identity of a channel is its random
	// 128-bit id rather than anything derived from a connection.
	mu       sync.Mutex
	channels map[proto.ChannelID]*openChannel
}

// Compile-time proof that this satisfies the host's registerable service and
// the refusal coder that puts its codes on the wire.
var (
	_ host.Service      = (*Service)(nil)
	_ host.RefusalCoder = (*Service)(nil)
)

// New builds the service over the daemon's ssh client.
func New(client Client, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{client: client, log: log}
}

// Name is the service name the coordinator addresses (proto.ServiceSSH).
func (s *Service) Name() string { return proto.ServiceSSH }

// Ops lists the ops THIS side answers. The reverse ops of the same service are
// answered by the coordinator, not here, and they are deliberately absent:
// host.Register refuses a service that declares an op with no params type, and
// a helper that claimed to answer `sign` would be claiming to hold key
// material it does not have.
func (s *Service) Ops() []string {
	return append([]string{proto.OpProbe}, s.channelOps()...)
}

// ParamsSchema declares the shape of each op. D3 is enforced off this table:
// no field here may be a free-form string list, and none is — a probe takes a
// host, a port, a user and a credential reference.
func (s *Service) ParamsSchema(op string) *host.Schema {
	switch op {
	case proto.OpProbe:
		return host.SchemaFor(proto.ProbeParams{})
	case proto.OpOpen:
		return host.SchemaFor(proto.OpenChannelParams{})
	case proto.OpClose:
		return host.SchemaFor(proto.CloseChannelParams{})
	}
	return nil
}

// RefusesCancel: no. A probe is short, it half-applies nothing — it dials, it
// authenticates, it closes — and a probe that hangs is exactly the request a
// person wants to be able to give up on.
func (s *Service) RefusesCancel(string) bool { return false }

// Refusal names this service's refusals for the wire. The important one is a
// refusal the COORDINATOR sent back: the helper asked for material and the
// coordinator's vault is sealed, so the answer must return to the coordinator's
// caller as the state it is — the vault error the renderer already turns into
// an unlock — rather than as an opaque helper failure.
func (s *Service) Refusal(err error) (string, json.RawMessage) {
	var refusal *proto.Refusal
	if errors.As(err, &refusal) {
		return refusal.Code, refusal.Details
	}
	switch {
	case errors.Is(err, errNoAuthChannel):
		return proto.ErrCodeNoAuthChannel, nil
	case errors.Is(err, errBadProbeParams), errors.Is(err, errBadChannelParams):
		return proto.ErrCodeBadParams, nil
	}
	return "", nil
}

// Call dispatches one op.
func (s *Service) Call(ctx context.Context, op string, params json.RawMessage) (any, error) {
	switch op {
	case proto.OpProbe:
		var p proto.ProbeParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("%w: %w", errBadProbeParams, err)
			}
		}
		return s.probe(ctx, p)
	case proto.OpOpen:
		var p proto.OpenChannelParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("%w: %w", errBadChannelParams, err)
			}
		}
		return s.openChannel(ctx, p)
	case proto.OpClose:
		var p proto.CloseChannelParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("%w: %w", errBadChannelParams, err)
			}
		}
		return s.closeChannel(p.Channel)
	}
	return nil, fmt.Errorf("ssh: no op %q on this service", op)
}

// probe dials one host, authenticates with the credential the coordinator
// named, reports how far it got, and closes. It leaves nothing behind: no
// pooled connection, no session, no shell — the same discipline the
// coordinator's own pool-bypassing probe has, and for the same reason. A probe
// answers "does this credential work here", and a connection held open to
// answer it would be a resource whose lifetime nothing owns.
func (s *Service) probe(ctx context.Context, p proto.ProbeParams) (proto.ProbeResult, error) {
	conn, _ := host.ConnectionFrom(ctx).(*host.Host)
	if conn == nil {
		return proto.ProbeResult{}, errNoAuthChannel
	}
	if err := validateProbe(p); err != nil {
		return proto.ProbeResult{}, err
	}

	auth, err := s.authMethod(ctx, conn, p.Identity)
	if err != nil {
		return proto.ProbeResult{}, err
	}

	addr := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	gcfg := &gossh.ClientConfig{
		User: p.User,
		// Exactly one method, as the coordinator's own probe path insists: a
		// second attempt against one host is indistinguishable from password
		// spraying, and MaxAuthTries is finite.
		Auth:            []gossh.AuthMethod{auth},
		HostKeyCallback: s.hostKeyCallback(ctx, conn, p.AcceptOnTrust),
		Timeout:         ProbeTimeout,
	}

	client, err := s.client.DialAuth(ctx, addr, p.Host, p.User, gcfg)
	if err != nil {
		// A refusal the COORDINATOR answered is separated FIRST, and that order
		// is the whole point of this branch rather than a detail of it.
		//
		// The refusal arrives inside the handshake — the password callback
		// returns it, x/crypto wraps what a callback returned as "handshake
		// failed", and the dial seam wraps that as an authentication failure —
		// so by the time it is here it LOOKS exactly like a rejected
		// credential. Classifying it as one is the defect this arm exists to
		// prevent: a sealed vault would reach a person as "wrong password",
		// and they would go and check the host while the sheet they needed was
		// never raised. The chain is preserved (`%w` the whole way), so the
		// refusal can be recovered rather than inferred from its text.
		var refusal *proto.Refusal
		if errors.As(err, &refusal) {
			if refusal.Code == proto.ErrCodeNeedsInteractive {
				// The one refusal that is really an ANSWER: the credential
				// exists and cannot be used without a person, which is what the
				// coordinator's own probe reports when it meets an encrypted key
				// with no passphrase (ssh.ErrEncryptedKey).
				return proto.ProbeResult{Outcome: proto.ProbeNeedsInteractive, Detail: refusal.Message}, nil
			}
			return proto.ProbeResult{}, refusal
		}
		outcome, detail, unclassified := ssh.ClassifyProbeError(err)
		if unclassified != nil {
			// Never collapsed into `rejected`: a failure nobody can classify
			// is not the server refusing a credential, and reporting it as one
			// would send a person to look at the host.
			return proto.ProbeResult{}, fmt.Errorf("probe: %w", unclassified)
		}
		return proto.ProbeResult{Outcome: proto.ProbeOutcome(outcome), Detail: detail}, nil
	}
	_ = client.Close()
	return proto.ProbeResult{Outcome: proto.ProbeAccepted, Detail: "ok"}, nil
}

// validateProbe refuses a probe that cannot be dialed before anything is
// dialed. The declared schema is what the contract layer checks; this is what
// the running helper checks, and the reason both exist is that a schema is a
// document and this is a decision.
func validateProbe(p proto.ProbeParams) error {
	switch {
	case p.Host == "":
		return fmt.Errorf("%w: no host", errBadProbeParams)
	case p.Port <= 0 || p.Port > 65535:
		return fmt.Errorf("%w: port %d", errBadProbeParams, p.Port)
	case p.User == "":
		return fmt.Errorf("%w: no user", errBadProbeParams)
	}
	return validateIdentity(p.Identity)
}

func validateIdentity(id proto.SSHIdentity) error {
	if id.Credential.Ref == "" {
		return fmt.Errorf("%w: no credential reference", errBadProbeParams)
	}
	switch id.Auth {
	case proto.SSHAuthPassword:
		return nil
	case proto.SSHAuthKey:
		if len(id.PublicKey) == 0 {
			// A key credential with no public half cannot be offered: the
			// helper would have to learn the key from somewhere, and the only
			// somewhere is the coordinator, whose answer to "which key" is the
			// identity the caller already built.
			return fmt.Errorf("%w: key auth with no public key", errBadProbeParams)
		}
		return nil
	}
	return fmt.Errorf("%w: auth %q is not one this helper knows", errBadProbeParams, id.Auth)
}

// authMethod builds the ONE method this probe will send.
//
// Both arms are LAZY on purpose. A password is asked for at the moment the
// server challenges for it rather than when the probe is built, so it lives in
// this process for the duration of a callback and not for the duration of a
// dial — the same discipline the coordinator's own chain keeps, one hop out.
func (s *Service) authMethod(ctx context.Context, conn *host.Host, id proto.SSHIdentity) (gossh.AuthMethod, error) {
	switch id.Auth {
	case proto.SSHAuthPassword:
		return gossh.PasswordCallback(func() (string, error) {
			var out proto.SecretResult
			err := conn.Ask(ctx, proto.ServiceSSH, proto.OpSecret, proto.SecretParams{
				Credential: id.Credential,
				Purpose:    proto.PurposePassword,
			}, &out)
			if err != nil {
				return "", askRefusal(err)
			}
			return string(out.Secret), nil
		}), nil

	case proto.SSHAuthKey:
		pub, err := gossh.ParsePublicKey(id.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("%w: public key: %w", errBadProbeParams, err)
		}
		return gossh.PublicKeys(&reverseSigner{ctx: ctx, conn: conn, cred: id.Credential, pub: pub}), nil
	}
	// validateIdentity has already refused every other value; this arm exists
	// so a kind added to the wire without a method here is a refusal and not a
	// nil method the handshake would send.
	return nil, fmt.Errorf("%w: auth %q is not one this helper knows", errBadProbeParams, id.Auth)
}

// reverseSigner is a gossh.Signer whose private half is the coordinator's.
//
// It is the whole reason `sign` exists, and the reason the public key travels
// in the probe identity rather than being discovered: x/crypto/ssh asks
// PublicKey() BEFORE it asks for a signature, so the helper must be able to
// declare which key it is offering without ever possessing it. Signing is then
// one round trip per challenge, and the bytes that cross are the challenge and
// a signature.
type reverseSigner struct {
	ctx  context.Context
	conn *host.Host
	cred proto.SSHCredential
	pub  gossh.PublicKey
}

// PublicKey is the public half the coordinator PUT in the identity. It is not
// fetched and not derived: the party that holds both halves said which one it
// is about to sign with, and a helper that invented a different answer here
// would offer a key the coordinator would then refuse to sign for.
func (s *reverseSigner) PublicKey() gossh.PublicKey { return s.pub }

// Sign asks the coordinator for one signature over data.
//
// The algorithm travels with the request and the coordinator checks it against
// the key it resolves, so a mismatch is refused where the key is rather than
// producing a signature the peer would reject with a message about algorithms.
func (s *reverseSigner) Sign(_ io.Reader, data []byte) (*gossh.Signature, error) {
	var out proto.SignResult
	err := s.conn.Ask(s.ctx, proto.ServiceSSH, proto.OpSign, proto.SignParams{
		Credential: s.cred,
		Challenge:  data,
		Algorithm:  s.pub.Type(),
	}, &out)
	if err != nil {
		return nil, askRefusal(err)
	}
	var sig gossh.Signature
	if err := gossh.Unmarshal(out.Signature, &sig); err != nil {
		return nil, internalRefusal("the coordinator's signature is not an ssh signature: %v", err)
	}
	return &sig, nil
}

// hostKeyCallback answers x/crypto/ssh's host-key question with the
// coordinator's verdict.
//
// It returns THIS PACKAGE's error types — ssh.ErrUnknownHostKey,
// ssh.ErrHostKeyMismatch — rather than something of its own, and that is the
// whole reason the classification still works one process away: ssh's own
// ClassifyProbeError reads those two types, so the outcome a coordinator sees
// from a helper's probe is the outcome it saw when it dialed itself.
func (s *Service) hostKeyCallback(ctx context.Context, conn *host.Host, acceptOnTrust bool) gossh.HostKeyCallback {
	return func(addr string, _ net.Addr, key gossh.PublicKey) error {
		return s.verifyHostKey(ctx, conn, acceptOnTrust, addr, key)
	}
}

// verifyHostKey asks what to make of one offered key and acts on the answer.
//
// The accept-on-trust path is BOUNDED BY CONSTRUCTION: one verification, at
// most one trust request, and no re-verification — there is no loop here to
// bound because there is no branch that returns to the top. The trust request
// is only ever sent when the coordinator's own verdict says the key is unknown
// AND the caller that started this probe said it may be accepted; a `changed`
// verdict never reaches it, whatever the caller asked for, because a changed
// key is the one signature of a machine in the middle.
func (s *Service) verifyHostKey(ctx context.Context, conn *host.Host, acceptOnTrust bool, addr string, key gossh.PublicKey) error {
	blob := key.Marshal()
	var out proto.VerifyHostKeyResult
	err := conn.Ask(ctx, proto.ServiceSSH, proto.OpVerifyHostKey, proto.VerifyHostKeyParams{
		Host:      addr,
		Algorithm: key.Type(),
		Key:       blob,
	}, &out)
	if err != nil {
		return err
	}

	switch out.Verdict {
	case proto.HostKeyTrusted:
		return nil
	case proto.HostKeyChanged:
		return &ssh.ErrHostKeyMismatch{
			Addr: addr, KnownHostsAddr: addr, KeyAlgo: key.Type(),
			Fingerprint: out.Fingerprint, Expected: out.Expected, Key: blob,
		}
	case proto.HostKeyUnknown:
		if !acceptOnTrust {
			return &ssh.ErrUnknownHostKey{
				Addr: addr, KnownHostsAddr: addr, KeyAlgo: key.Type(),
				Fingerprint: out.Fingerprint, Key: blob,
			}
		}
		// The coordinator records it — known_hosts is the coordinator's file
		// and the coordinator is the party that decided this may happen — and
		// its answer IS the trust: the key is recorded, so the handshake
		// proceeds. Asking again would be a second round trip whose only
		// possible outcome is the answer just given.
		var trusted proto.TrustHostKeyResult
		if terr := conn.Ask(ctx, proto.ServiceSSH, proto.OpTrustHostKey, proto.TrustHostKeyParams{
			Host:      addr,
			Algorithm: key.Type(),
			Key:       blob,
		}, &trusted); terr != nil {
			return terr
		}
		return nil
	default:
		// A verdict this helper does not know is refused rather than treated
		// as one of the three: the value that arrived is whatever the peer
		// wrote, and a default arm that accepted it would accept a spelling
		// invented by a generation nobody has written yet.
		//
		// It is a TYPED refusal and not a plain error, and that is not
		// decoration: x/crypto wraps whatever a callback returned as
		// "handshake failed", the dial seam classifies that as an
		// authentication failure, and a plain error here would reach a person
		// as "the server refused this credential" — about a protocol mistake
		// this process made. See internalRefusal.
		return internalRefusal("host key verdict %q is not one this helper knows", out.Verdict)
	}
}

// internalRefusal names a failure of THIS helper's own machinery rather than
// anything the peer said, on the wire and in the vocabulary the caller already
// switches on.
//
// The need for it is exact. Every error a callback raises during a handshake
// comes back wrapped as "ssh: handshake failed", which the dial seam's own
// classifier reads as an authentication failure — so an unrecognised verdict, a
// signature that is not an ssh signature, or a coordinator that could not
// answer at all would all be reported as a rejected credential. That is the
// same conflation the sealed-vault arm above exists to prevent, one class
// wider, and typing these is how it stops.
func internalRefusal(format string, args ...any) error {
	return &proto.Refusal{Code: proto.ErrCodeInternal, Message: fmt.Sprintf(format, args...)}
}

// askRefusal keeps a coordinator's named refusal as itself and types anything
// else as this helper's own failure. The two must not be merged: the first is
// an answer the caller acts on (a sealed vault), the second is a bug in this
// process that nobody can act on.
func askRefusal(err error) error {
	var refusal *proto.Refusal
	if errors.As(err, &refusal) {
		return refusal
	}
	return internalRefusal("the coordinator could not answer: %v", err)
}
