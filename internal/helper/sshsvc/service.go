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
	// DialAuth dials one RESOLVED destination — through its route when it has
	// one — and answers a borrowed connection the caller must close. It is the
	// probe's dial: nothing is pooled, so closing it closes the hops too.
	DialAuth(ctx context.Context, spec ssh.PooledSpec) (*ssh.PooledConn, error)
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

	// mu guards channels and forwards: the proxied channels and remote
	// listeners this daemon holds, across every connection it serves. The
	// service is process-scoped (one per daemon, beside the sessions) while
	// the ssh connections are per-COORDINATOR, so the registry is one map and
	// the identity of a channel is its random 128-bit id rather than anything
	// derived from a connection.
	mu       sync.Mutex
	channels map[proto.ChannelID]*openChannel
	forwards map[proto.ForwardID]*openForward
	// toolSockets are the far-side tool sockets this helper serves: one per
	// pane whose shell runs on a far host (nocx-e2bws). They live in the same
	// registry as the forwards and for the same reason — the service is
	// process-scoped while the connections are per-coordinator, so identity is
	// a random id and not anything derived from a connection — and they are a
	// SECOND table rather than entries in the first because what they carry is
	// not the same thing: a forward hands its connections to the coordinator
	// as channels, while a tool socket pipes them into an endpoint itself,
	// with a pane record written first.
	toolSockets map[proto.ForwardID]*toolSocket
	// leases are the probe leases: pooled references held for a coordinator
	// that wants its probes answered on one transport. They live in the same
	// registry as the channels and for the same reason — the service is
	// process-scoped while the connections are per-coordinator, so identity is
	// a random id and not anything derived from a connection (lease_id.go).
	leases map[proto.LeaseID]*probeLease
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
	ops := append(append(append([]string{proto.OpProbe}, s.channelOps()...), s.forwardOps()...), s.probeOps()...)
	ops = append(ops, s.laneOps()...)
	return append(ops, s.toolSocketOps()...)
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
	case proto.OpForward:
		return host.SchemaFor(proto.ForwardParams{})
	case proto.OpUnforward:
		return host.SchemaFor(proto.UnforwardParams{})
	case proto.OpLease:
		return host.SchemaFor(proto.LeaseParams{})
	case proto.OpUnlease:
		return host.SchemaFor(proto.UnleaseParams{})
	case proto.OpUname:
		return host.SchemaFor(proto.UnameParams{})
	case proto.OpHome:
		return host.SchemaFor(proto.HomeParams{})
	case proto.OpSamplePorts:
		return host.SchemaFor(proto.SamplePortsParams{})
	case proto.OpCompletion:
		return host.SchemaFor(proto.CompletionParams{})
	case proto.OpCommandNames:
		return host.SchemaFor(proto.CommandNamesParams{})
	case proto.OpLane:
		return host.SchemaFor(proto.LaneParams{})
	case proto.OpToolSocket:
		return host.SchemaFor(proto.ToolSocketParams{})
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
	case errors.Is(err, errBadProbeParams), errors.Is(err, errBadChannelParams),
		errors.Is(err, errBadLeaseParams), errors.Is(err, errBadLaneParams),
		errors.Is(err, errBadToolSocketParams):
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
	case proto.OpForward:
		var p proto.ForwardParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("%w: %w", errBadChannelParams, err)
			}
		}
		return s.forward(ctx, p)
	case proto.OpUnforward:
		var p proto.UnforwardParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("%w: %w", errBadChannelParams, err)
			}
		}
		return s.unforward(p.Forward)
	case proto.OpLease:
		var p proto.LeaseParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("%w: %w", errBadLeaseParams, err)
			}
		}
		return s.lease(ctx, p)
	case proto.OpUnlease:
		var p proto.UnleaseParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("%w: %w", errBadLeaseParams, err)
			}
		}
		return s.unlease(p.Lease)
	case proto.OpUname:
		var p proto.UnameParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("%w: %w", errBadLeaseParams, err)
			}
		}
		return s.uname(ctx, p)
	case proto.OpHome:
		var p proto.HomeParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("%w: %w", errBadLeaseParams, err)
			}
		}
		return s.home(ctx, p)
	case proto.OpSamplePorts:
		var p proto.SamplePortsParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("%w: %w", errBadLeaseParams, err)
			}
		}
		return s.samplePorts(ctx, p)
	case proto.OpCompletion:
		var p proto.CompletionParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("%w: %w", errBadLeaseParams, err)
			}
		}
		return s.completion(ctx, p)
	case proto.OpCommandNames:
		var p proto.CommandNamesParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("%w: %w", errBadLeaseParams, err)
			}
		}
		return s.commandNames(ctx, p)
	case proto.OpLane:
		var p proto.LaneParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("%w: %w", errBadLaneParams, err)
			}
		}
		return s.openLane(ctx, p)
	case proto.OpToolSocket:
		var p proto.ToolSocketParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("%w: %w", errBadToolSocketParams, err)
			}
		}
		return s.toolSocket(ctx, p)
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
	ep := endpointOf(p.Destination)
	route, err := s.routeOf(ctx, conn, p.Destination, p.AcceptOnTrust)
	if err != nil {
		return proto.ProbeResult{}, err
	}
	gcfg, err := s.clientConfig(ctx, conn, ep, p.AcceptOnTrust)
	if err != nil {
		return proto.ProbeResult{}, err
	}
	// The probe is the ONE op that must report WHICH host key it met, because
	// the coordinator stores it: `host-key-unknown` is first contact with a
	// machine, and a probe that could not say which key it saw would make two
	// machines indistinguishable in the settings surface's own record. So the
	// callback is wrapped — the verdict is still the coordinator's, and this
	// only watches what the verdict was about.
	seen := &seenHostKey{}
	gcfg.HostKeyCallback = observeHostKey(gcfg.HostKeyCallback, seen)

	// The probe dials the whole ROUTE — every hop and the destination — and
	// pools none of it: what it answers is whether this credential works here,
	// and a connection it left behind (a bastion's included) would be one whose
	// lifetime nothing owns. The hops' keys are verified by the coordinator
	// through the same asks the destination's is.
	pool, err := s.client.DialAuth(ctx, ssh.PooledSpec{
		Host: ep.Host, Port: ep.Port, User: ep.User,
		Identity: identityKey(p.Destination.Identity),
		Config:   gcfg,
		Route:    route,
	})
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
		return proto.ProbeResult{
			Outcome:     proto.ProbeOutcome(outcome),
			Detail:      detail,
			Fingerprint: seen.fingerprint,
			HostKey:     hostKeyEvidenceOf(err),
		}, nil
	}
	_ = pool.Close()
	return proto.ProbeResult{Outcome: proto.ProbeAccepted, Detail: "ok", Fingerprint: seen.fingerprint}, nil
}

// seenHostKey is what one handshake saw of the key it was offered, whether the
// verdict accepted it or refused it.
type seenHostKey struct {
	addr        string
	algorithm   string
	fingerprint string
	key         []byte
}

// observeHostKey wraps a host-key callback so the offered key is recorded
// before the verdict is decided.
//
// It watches and does not decide: the callback underneath is still the one
// that asks the coordinator, and an offer this process never answers still
// leaves the evidence behind — which is the case that matters, because a
// refused key is exactly the one a person is shown.
func observeHostKey(inner gossh.HostKeyCallback, seen *seenHostKey) gossh.HostKeyCallback {
	return func(addr string, remote net.Addr, key gossh.PublicKey) error {
		seen.addr = addr
		seen.algorithm = key.Type()
		seen.fingerprint = gossh.FingerprintSHA256(key)
		seen.key = key.Marshal()
		return inner(addr, remote, key)
	}
}

// hostKeyEvidenceOf is the evidence a host-key outcome carries, taken from the
// TYPED error the callback raised rather than rebuilt from what this function
// happened to see: the error is where the coordinator's verdict landed, and it
// is the only place the recorded fingerprint of a `changed` key exists.
//
// Nil for every other failure, and nil for success: an `accepted` probe carries
// a fingerprint and no evidence, because there is nothing to decide.
func hostKeyEvidenceOf(err error) *proto.HostKeyEvidence {
	var unknown *ssh.ErrUnknownHostKey
	if errors.As(err, &unknown) {
		return &proto.HostKeyEvidence{
			Addr: unknown.Addr, KnownHostsAddr: unknown.KnownHostsAddr,
			Algorithm: unknown.KeyAlgo, Key: unknown.Key, Fingerprint: unknown.Fingerprint,
		}
	}
	var changed *ssh.ErrHostKeyMismatch
	if errors.As(err, &changed) {
		return &proto.HostKeyEvidence{
			Addr: changed.Addr, KnownHostsAddr: changed.KnownHostsAddr,
			Algorithm: changed.KeyAlgo, Key: changed.Key,
			Fingerprint: changed.Fingerprint, Expected: changed.Expected,
		}
	}
	return nil
}

// validateProbe refuses a probe that cannot be dialed before anything is
// dialed. The declared schema is what the contract layer checks; this is what
// the running helper checks, and the reason both exist is that a schema is a
// document and this is a decision.
func validateProbe(p proto.ProbeParams) error {
	if err := validateDestinationAddress(p.Destination); err != nil {
		return fmt.Errorf("%w (probe)", err)
	}
	return nil
}

// validateIdentity refuses an identity this helper could not offer.
//
// It is one switch over the CLOSED SET of auth kinds, and each arm states what
// its kind needs rather than sharing a rule the kinds do not share: a password
// names material, a key names the QUEUE of keys it will declare — each with the
// public half it must present and a reference to sign through — and an
// interactive rung names NOTHING, because the person is the credential and a
// reference there would name something that does not exist.
//
// Each arm also refuses what belongs to another kind, in both directions. That
// is not defensive noise: `additionalProperties: false` means the schema
// decides what may be SENT, and this decides what may be DONE with it, so a
// payload the contract layer would accept as shaped correctly must still be
// refused here if it says two different things about how to authenticate.
func validateIdentity(id proto.SSHIdentity) error {
	switch id.Auth {
	case proto.SSHAuthInteractive:
		if id.Credential != nil || len(id.Keys) != 0 {
			// An interactive rung that carries material is a caller that
			// believes it is sending a credential, and answering it as a
			// prompt would ask a person for something already in hand.
			return fmt.Errorf("%w: interactive auth carries no credential", errBadProbeParams)
		}
		return nil
	case proto.SSHAuthPassword:
		if id.Credential == nil || id.Credential.Ref == "" {
			return fmt.Errorf("%w: no credential reference", errBadProbeParams)
		}
		if len(id.Keys) != 0 {
			return fmt.Errorf("%w: password auth carries keys", errBadProbeParams)
		}
		return nil
	case proto.SSHAuthKey:
		if id.Credential != nil {
			// A key identity carries its material in the queue, one reference
			// per key: a bare credential beside it would name a key with no
			// public half to declare, which is the shape that cannot be
			// offered at all.
			return fmt.Errorf("%w: key auth names its keys, not a credential", errBadProbeParams)
		}
		if len(id.Keys) == 0 {
			// A key identity with no key cannot be offered: the helper would
			// have to learn the key from somewhere, and the only somewhere is
			// the coordinator, whose answer to "which key" is the queue the
			// caller already built.
			return fmt.Errorf("%w: key auth with no public key", errBadProbeParams)
		}
		for i, key := range id.Keys {
			if key.Credential.Ref == "" {
				return fmt.Errorf("%w: key %d names no credential", errBadProbeParams, i)
			}
			if len(key.PublicKey) == 0 {
				return fmt.Errorf("%w: key %d carries no public key", errBadProbeParams, i)
			}
			if _, err := gossh.ParsePublicKey(key.PublicKey); err != nil {
				return fmt.Errorf("%w: key %d is not a public key: %w", errBadProbeParams, i, err)
			}
		}
		return nil
	}
	return fmt.Errorf("%w: auth %q is not one this helper knows", errBadProbeParams, id.Auth)
}

// dialEndpoint is one endpoint this helper is about to dial, in the shape the
// client configuration and the coordinator's asks need it: the address, the
// account, the identity, and the address this endpoint's host key is stored
// under.
//
// It exists because a destination and a hop are the same thing to every caller
// below this line — each is dialed, each is authenticated through the reverse
// channel and each has its key verified by the coordinator — and two parameter
// lists that agree by convention would eventually agree about the wrong one.
type dialEndpoint struct {
	Host string
	Port int
	User string
	// Identity is what to authenticate with here: this hop's own credential,
	// never the destination's.
	Identity proto.SSHIdentity
	// KnownHostsAddr is the storage identity the coordinator gave for this
	// endpoint; empty means the dial address.
	KnownHostsAddr string
	// ConnectionName and ProfileID correlate a password ask back to the
	// connection it is for (nocx-y6fh7 item 4, round 3), echoed unchanged
	// from proto.SSHDestination. Both empty for a hop, which carries no
	// such fields at all — a jump host is not a saved connection a person's
	// remember checkbox could bind to.
	ConnectionName string
	ProfileID      string
}

// addr is the dial address of one endpoint.
func (e dialEndpoint) addr() string { return net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) }

// storageAddr is the address the coordinator looks this endpoint's host key up
// under: what it sent, or the dial address when it sent nothing — which is what
// a direct connection's storage identity is, and the only one a hop can have.
func (e dialEndpoint) storageAddr() string {
	if e.KnownHostsAddr != "" {
		return e.KnownHostsAddr
	}
	return e.addr()
}

// endpointOf is the destination half of the conversion.
func endpointOf(d proto.SSHDestination) dialEndpoint {
	return dialEndpoint{
		Host: d.Host, Port: d.Port, User: d.User,
		Identity: d.Identity, KnownHostsAddr: d.KnownHostsAddr,
		ConnectionName: d.ConnectionName, ProfileID: d.ProfileID,
	}
}

// hopEndpointOf is the hop half. A hop carries the same five facts, because a
// route is a chain of ordinary handshakes rather than one longer address.
func hopEndpointOf(h proto.SSHHop) dialEndpoint {
	return dialEndpoint{
		Host: h.Host, Port: h.Port, User: h.User,
		Identity: h.Identity, KnownHostsAddr: h.KnownHostsAddr,
	}
}

// routeOf builds the hops a dial passes through, each with the client
// configuration the coordinator's own answers produce: the identity's one auth
// method, and the host-key callback that asks the coordinator about THIS hop's
// key.
//
// The order is the wire's, which is the coordinator's: Jumps[0] is dialed from
// this machine and each next hop through the one before it. Nothing is
// re-ordered or re-derived here — a helper that sorted or de-duplicated a route
// would be deciding which machines a connection passes through.
func (s *Service) routeOf(ctx context.Context, conn *host.Host, d proto.SSHDestination, acceptOnTrust bool) ([]ssh.PooledHop, error) {
	if len(d.Jumps) == 0 {
		return nil, nil
	}
	route := make([]ssh.PooledHop, 0, len(d.Jumps))
	for i, hop := range d.Jumps {
		ep := hopEndpointOf(hop)
		cfg, err := s.clientConfig(ctx, conn, ep, acceptOnTrust)
		if err != nil {
			return nil, fmt.Errorf("route hop %d: %w", i, err)
		}
		route = append(route, ssh.PooledHop{
			Host: hop.Host, Port: hop.Port, User: hop.User,
			Identity: identityKey(hop.Identity),
			Config:   cfg,
		})
	}
	return route, nil
}

// clientConfig builds the ONE client configuration this helper dials with.
//
// Shared by the probe and by every channel op, and by the forward op, because a
// second builder would be a second answer to "how does this helper
// authenticate" — and the two would first disagree about the arm that matters:
// a config offering more than one method for a STORED secret is password
// spraying against somebody else's host, and a config with no host-key
// callback is one that trusts whatever answers.
//
// A STORED secret (password or key) still offers exactly one method — that
// half of the rule is unchanged. The interactive rung is the one exception,
// and it is not spraying: nothing stored is repeated under a second name,
// every method asks the SAME live person the SAME question, and the server
// only ever sees an answer once a person has given one. Offering both is what
// "ask a person, whichever method the server speaks" means when the server's
// own supported set (`password`, `keyboard-interactive`) is unknown until the
// handshake states it. Which RELAY a method asks through differs, and it is
// the point rather than an inconsistency: `keyboard-interactive` carries the
// server's own questions with no connection behind them (OpPrompt), while a
// bare `password` challenge is a question about THIS connection's own
// credential and is asked through the coordinator's connection-password
// requester instead (OpPasswordPrompt, round 3) — one relay per subject, not
// two relays for one.
func (s *Service) clientConfig(ctx context.Context, conn *host.Host, ep dialEndpoint, acceptOnTrust bool) (*gossh.ClientConfig, error) {
	auth, err := s.authMethods(ctx, conn, ep)
	if err != nil {
		return nil, err
	}
	return &gossh.ClientConfig{
		User:            ep.User,
		Auth:            auth,
		HostKeyCallback: s.hostKeyCallback(ctx, conn, acceptOnTrust, ep.storageAddr()),
		Timeout:         ProbeTimeout,
	}, nil
}

// authMethods builds the method(s) this dial will send.
//
// Every arm but one answers with exactly one method — a STORED secret is
// never repeated under a second name (clientConfig's own comment). The one
// exception is SSHAuthInteractive: nothing is stored to spray, so it offers
// BOTH `password` and `keyboard-interactive`, whichever the server actually
// speaks, and each is the SAME live person answering the SAME question
// through the SAME relay (askInteractive) — a password challenge is that
// relay asked with one synthetic, non-echoed "Password:" question rather
// than the server's own list, because a plain gossh.PasswordCallback carries
// no question for a person to be asked and this rung has no stored secret to
// hand it silently.
//
// The password and key arms are LAZY on purpose. A password is asked for at
// the moment the server challenges for it rather than when the probe is
// built, so it lives in this process for the duration of a callback and not
// for the duration of a dial — the same discipline the coordinator's own
// chain keeps, one hop out.
func (s *Service) authMethods(ctx context.Context, conn *host.Host, ep dialEndpoint) ([]gossh.AuthMethod, error) {
	switch ep.Identity.Auth {
	case proto.SSHAuthPassword:
		return []gossh.AuthMethod{gossh.PasswordCallback(func() (string, error) {
			var out proto.SecretResult
			err := conn.Ask(ctx, proto.ServiceSSH, proto.OpSecret, proto.SecretParams{
				Credential: ep.Identity.CredentialOf(),
				Purpose:    proto.PurposePassword,
			}, &out)
			if err != nil {
				return "", askRefusal(err)
			}
			return string(out.Secret), nil
		})}, nil

	case proto.SSHAuthInteractive:
		// The rung a PERSON answers, on whichever of the server's two
		// password-shaped methods it actually offers — each through its OWN
		// relay, because the two are different questions with different
		// owners (clientConfig's own comment): the server's own
		// keyboard-interactive questions travel to the coordinator verbatim
		// through OpPrompt, and a bare `password` challenge — which carries
		// no question of its own, RFC 4252 §8's challenge is bare — is asked
		// through the coordinator's own connection-password ask
		// (askPassword, OpPasswordPrompt), the same one a direct dial's
		// prompt rung uses, so it reaches the "Password for {profile}"
		// dialog with its remember checkbox rather than a bare box with no
		// connection to name (nocx-y6fh7 item 4, round 3).
		return []gossh.AuthMethod{
			gossh.KeyboardInteractive(func(_, _ string, questions []string, echos []bool) ([]string, error) {
				return s.askInteractive(ctx, conn, ep, questions, echos)
			}),
			gossh.PasswordCallback(func() (string, error) {
				return s.askPassword(ctx, conn, ep)
			}),
		}, nil

	case proto.SSHAuthKey:
		// ONE method, every key in the queue: `gossh.PublicKeys` sends a
		// `publickey` QUERY per signer and the server answers each in turn, so a
		// host that accepts only the third key a person's agent holds is
		// authenticated by the same handshake that offers the other two. The
		// private halves stay where they are — each signer is a reference to
		// the coordinator (reverseSigner), and what crosses per challenge is a
		// signature.
		signers := make([]gossh.Signer, 0, len(ep.Identity.Keys))
		for i, offer := range ep.Identity.Keys {
			pub, err := gossh.ParsePublicKey(offer.PublicKey)
			if err != nil {
				// Unreachable through validateIdentity, which parses every
				// entry before anything is dialed; kept because the alternative
				// is a nil signer inside the method, which the handshake would
				// answer for with a panic rather than a refusal.
				return nil, fmt.Errorf("%w: key %d: public key: %w", errBadProbeParams, i, err)
			}
			signers = append(signers, &reverseSigner{ctx: ctx, conn: conn, cred: offer.Credential, pub: pub})
		}
		return []gossh.AuthMethod{gossh.PublicKeys(signers...)}, nil
	}
	// validateIdentity has already refused every other value; this arm exists
	// so a kind added to the wire without a method here is a refusal and not a
	// nil method the handshake would send.
	return nil, fmt.Errorf("%w: auth %q is not one this helper knows", errBadProbeParams, ep.Identity.Auth)
}

// askInteractive relays one keyboard-interactive challenge to the coordinator
// and answers what a person said.
//
// The order and the count are the protocol's: the answers go back in the order
// the questions arrived, one per question. A coordinator that answers a
// different number of them has answered a different question, which is a
// refusal and not something to pad — a handshake that sent a short list would
// have the library answer the missing ones with empty strings, i.e. try an
// empty password.
func (s *Service) askInteractive(ctx context.Context, conn *host.Host, ep dialEndpoint, questions []string, echos []bool) ([]string, error) {
	if len(questions) == 0 {
		return nil, internalRefusal("the server asked a keyboard-interactive challenge with no questions")
	}
	if len(echos) != len(questions) {
		// The protocol pairs them — x/crypto builds both lists from the
		// server's own request — so a mismatch is this process's bug rather
		// than a hostile peer's. It is refused as this helper's own failure
		// rather than answered with a default, because "which questions are
		// secret" is exactly the fact a default gets silently wrong: it either
		// hides an answer a person needs to read, or prints one they do not.
		return nil, internalRefusal(
			"the server asked %d question(s) with %d echo flag(s)", len(questions), len(echos))
	}
	prompts := make([]proto.Prompt, len(questions))
	for i, q := range questions {
		prompts[i] = proto.Prompt{Prompt: q, Echo: echos[i]}
	}
	var out proto.PromptResult
	if err := conn.Ask(ctx, proto.ServiceSSH, proto.OpPrompt, proto.PromptParams{
		Host: ep.Host, Port: ep.Port, User: ep.User,
		Prompts: prompts,
	}, &out); err != nil {
		return nil, askRefusal(err)
	}
	if len(out.Answers) != len(prompts) {
		return nil, internalRefusal(
			"the coordinator answered %d of %d prompts", len(out.Answers), len(prompts))
	}
	return out.Answers, nil
}

// askPassword asks the coordinator's OWN connection-password ask for the one
// live person who is this connection's credential, over the interactive
// rung's bare `password` method (nocx-y6fh7 item 4, round 3).
//
// It is a DIFFERENT reverse op from askInteractive/OpPrompt on purpose: a
// password ask correlates to the connection it belongs to (ep.ConnectionName,
// ep.ProfileID, both echoed from the destination the coordinator itself
// resolved), so the coordinator answers it through the SAME requester a
// direct dial's own prompt rung uses — the "Password for {profile}" dialog
// with its remember checkbox (ADR-0017) — rather than inventing a second,
// profile-blind box for one question. OpPrompt stays the server's own
// keyboard-interactive questions, which carry no profile and no remember
// concept at all.
func (s *Service) askPassword(ctx context.Context, conn *host.Host, ep dialEndpoint) (string, error) {
	var out proto.PasswordPromptResult
	if err := conn.Ask(ctx, proto.ServiceSSH, proto.OpPasswordPrompt, proto.PasswordPromptParams{
		Connection: ep.ConnectionName,
		ProfileID:  ep.ProfileID,
		Host:       ep.Host, Port: ep.Port, User: ep.User,
	}, &out); err != nil {
		return "", askRefusal(err)
	}
	return out.Password, nil
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
//
// knownHostsAddr is the STORAGE identity the coordinator resolved for this
// endpoint, and it is passed through rather than derived: for a host reached
// through a jump it is a route digest only the coordinator can compute, and a
// helper that used the dial address instead would look for a line nobody ever
// wrote.
func (s *Service) hostKeyCallback(ctx context.Context, conn *host.Host, acceptOnTrust bool, knownHostsAddr string) gossh.HostKeyCallback {
	return func(addr string, _ net.Addr, key gossh.PublicKey) error {
		return s.verifyHostKey(ctx, conn, acceptOnTrust, addr, knownHostsAddr, key)
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
func (s *Service) verifyHostKey(ctx context.Context, conn *host.Host, acceptOnTrust bool, addr, knownHostsAddr string, key gossh.PublicKey) error {
	blob := key.Marshal()
	var out proto.VerifyHostKeyResult
	err := conn.Ask(ctx, proto.ServiceSSH, proto.OpVerifyHostKey, proto.VerifyHostKeyParams{
		Host:           addr,
		KnownHostsAddr: knownHostsAddr,
		Algorithm:      key.Type(),
		Key:            blob,
	}, &out)
	if err != nil {
		return err
	}

	switch out.Verdict {
	case proto.HostKeyTrusted:
		return nil
	case proto.HostKeyChanged:
		return &ssh.ErrHostKeyMismatch{
			Addr: addr, KnownHostsAddr: knownHostsAddr, KeyAlgo: key.Type(),
			Fingerprint: out.Fingerprint, Expected: out.Expected, Key: blob,
		}
	case proto.HostKeyUnknown:
		if !acceptOnTrust {
			return &ssh.ErrUnknownHostKey{
				Addr: addr, KnownHostsAddr: knownHostsAddr, KeyAlgo: key.Type(),
				Fingerprint: out.Fingerprint, Key: blob,
			}
		}
		// The coordinator records it — known_hosts is the coordinator's file
		// and the coordinator is the party that decided this may happen — and
		// its answer IS the trust: the key is recorded, so the handshake
		// proceeds. Asking again would be a second round trip whose only
		// possible outcome is the answer just given.
		//
		// The write names the STORAGE identity, not the offered address: that
		// is the line the next connection will look for, and writing the dial
		// address instead would record a host nobody verifies against.
		var trusted proto.TrustHostKeyResult
		if terr := conn.Ask(ctx, proto.ServiceSSH, proto.OpTrustHostKey, proto.TrustHostKeyParams{
			Host:      knownHostsAddr,
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
