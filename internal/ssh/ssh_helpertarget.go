package ssh

// The destination a HELPER needs, resolved by the coordinator.
//
// # Why the coordinator resolves it and the helper only dials
//
// The owner's invariant (plan §1-§3) is that every ssh connection is made by
// the local helper and the coordinator holds no ssh client. That moves the
// DIAL, and it cannot move the two things a dial needs decided:
//
//   - which host, port and account this is. An alias is resolved through
//     ~/.ssh/config, and the party that reads that file is the coordinator.
//   - what to authenticate with, and whether this credential is authorized
//     for that endpoint at all. A credential is bound to an endpoint by the
//     profile that names it, and an authorization check the helper could run
//     would be a check the helper could also skip — the same-UID boundary
//     (D12) means it has no way to know it was asked to.
//
// So ResolveTarget answers exactly the facts a helper needs and nothing it
// could act on by itself: an address, an account, and a REFERENCE to the
// credential — never the material. The material crosses only when the helper
// asks for it at the moment it has to present it (proto's reverse ops), which
// is what keeps a helper holding no secret at rest.
//
// # What each credential resolves to, and why the wire can carry all of them
//
// A stored key, a key FILE this process reads and an ssh-agent key all resolve
// to the same wire shape — a reference plus the public half — because the only
// thing a handshake needs from a key is a SIGNATURE, and a signature is exactly
// what a reference lets the helper ask for. Which of the three a reference
// names is the coordinator's business alone (ssh_helpercred.go), and an
// encrypted key is unlocked HERE, where its passphrase lives, so nothing a
// helper holds is ever a key.
//
// The prompt rung is the one that resolves to no reference at all: a person is
// the credential, and the helper asks for them the way it asks for anything
// else (proto.OpPrompt). It resolves only when the connection carries a
// password requester, which is the boundary a PROBE crosses — a probe answers a
// question the product asked itself and may not stop to ask a person.
//
// # The route
//
// A destination reached through jump hosts resolves to a flat, ordered list of
// hops (DialEndpoint, the same shape as the target itself): each hop is dialed
// from the one before it, each has its own account and credential, and each
// one's host key is verified by the coordinator — the helper has no
// known_hosts to verify anything against. What a hop's key is STORED under is
// the coordinator's own derivation and travels with the hop
// (KnownHostsAddr), because a target reached through one bastion is a different
// machine as far as the key store is concerned from the same target dialed
// directly.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/helper/proto"
	gossh "golang.org/x/crypto/ssh"
)

// DialAuthKind is how a resolved target authenticates, in the closed set the
// helper's wire carries (proto.SSHAuthKind, spelled the same and held to it by
// the coordinator's own mapping).
type DialAuthKind string

const (
	// DialAuthPassword presents a stored password.
	DialAuthPassword DialAuthKind = "password"
	// DialAuthKey proves possession of a private key by signing: the stored
	// key of a linked credential, an inline key file, or a key the ssh-agent
	// holds. The three are one kind on this wire because they are one METHOD —
	// what differs is which coordinator-side resolver answers `sign`.
	DialAuthKey DialAuthKind = "key"
	// DialAuthInteractive answers the server's own keyboard-interactive
	// prompts from the coordinator's UI. It carries no credential: the person
	// is the credential, and the reference a person would have would name
	// nothing (see the file header).
	DialAuthInteractive DialAuthKind = "interactive"
)

// DialEndpoint is one resolved ssh endpoint: the address, the account, the
// credential to present there, and the address its host key is stored under. It
// is a destination and a hop at once, because they are the same three facts and
// a hop is a destination that is not routed any further.
type DialEndpoint struct {
	Host string
	Port int
	User string
	Auth DialAuthKind
	// Credential names the material to present: the password for
	// DialAuthPassword, the private key for DialAuthKey. It is the
	// COORDINATOR's reference — vault, file or agent — and is opaque to the
	// helper, which echoes it back when it asks for what it names. Empty for
	// DialAuthInteractive, which names no material at all.
	Credential CredentialRef
	// Passphrase names the passphrase of an encrypted key. Empty for a
	// password, for an unencrypted key, and for the agent (whose keys this
	// process never parses).
	Passphrase CredentialRef
	// PublicKey is the wire-format public half of a key credential (RFC 4253
	// §6.6). Required for DialAuthKey: x/crypto/ssh asks a signer for
	// PublicKey() BEFORE it asks for a signature, so the party that dials must
	// be able to declare which key it is offering without holding it.
	PublicKey []byte
	// KnownHostsAddr is the address this endpoint's host key is stored under,
	// which is the dial address for a direct route and a route-derived digest
	// for a host reached through a jump (see knownHostsTargetAddr). It is
	// resolved HERE because known_hosts is this process's file and the digest
	// is this package's rule: a helper that re-derived either would be a second
	// answer to "which machine is this".
	KnownHostsAddr string
}

// DialTarget is everything a helper needs to open a connection, and nothing it
// may decide for itself: the destination, and the hops it is reached through in
// dial order.
type DialTarget struct {
	DialEndpoint
	// Route is the intermediate hosts, in dial order: Route[0] is dialed from
	// this machine, each next hop through the one before it, and the
	// destination through the last. Empty for a direct connection.
	Route []DialEndpoint
}

// ResolveTarget resolves a host and its connect options into the destination a
// helper dials.
//
// The authorization check runs HERE and not there, which is the one thing this
// function does that the helper could not: a linked credential may only be
// spent on the endpoint its profile identifies, and the coordinator is the
// party holding that binding.
func (rc *RealClient) ResolveTarget(ctx context.Context, host string, opts ...ConnectOption) (DialTarget, error) {
	cfg := &ConnectConfig{}
	for _, o := range opts {
		o(cfg)
	}
	endpoint, err := rc.resolveDialEndpoint(ctx, host, cfg, cfg.AuthorizedEndpoint, cfg.SecretID)
	if err != nil {
		return DialTarget{}, err
	}
	route, err := rc.resolveRoute(ctx, cfg)
	if err != nil {
		return DialTarget{}, err
	}
	return DialTarget{DialEndpoint: endpoint, Route: route}, nil
}

// resolveDialEndpoint resolves ONE endpoint: its address and account through
// ~/.ssh/config, its credential's authorization against that address, and the
// credential itself as a reference plus whatever public half the helper needs to
// ask for signatures.
//
// authorizedEndpoint and secretID are the parent hop's binding when this is a
// hop: a jump host's credential is bound by the profile that names it, and the
// flat Jump* fields carry that binding for the one-hop case.
func (rc *RealClient) resolveDialEndpoint(
	ctx context.Context, host string, cfg *ConnectConfig,
	authorizedEndpoint string, secretID credential.SecretID,
) (DialEndpoint, error) {
	resolved, err := rc.resolveConfig(ctx, host, cfg)
	if err != nil {
		return DialEndpoint{}, fmt.Errorf("resolve config for %s: %w", host, err)
	}
	// The same check acquirePooled runs before its own dial, called through
	// the same function: a helper-opened connection must be bound by exactly
	// the authorization a coordinator-opened one was, and the way to guarantee
	// that is to run the check rather than to describe it.
	if cfg.Secrets != nil {
		resolvedAuthz := rc.resolveAuthzEndpoint(ctx, authorizedEndpoint)
		if authErr := checkAuthorization(resolvedAuthz, resolved, string(secretID), false); authErr != nil {
			return DialEndpoint{}, authErr
		}
	}

	endpoint := DialEndpoint{
		Host: resolved.hostName,
		Port: resolved.port,
		User: resolved.user,
	}
	if err := rc.resolveCredential(ctx, resolved, cfg, &endpoint); err != nil {
		return DialEndpoint{}, err
	}
	// The storage identity is the coordinator's own derivation, run over the
	// same inputs its dial path runs it over (dialForConnect and
	// dialJumpForConnect both call knownHostsTargetAddr), so the key a helper's
	// handshake is checked against is the key a coordinator's own handshake
	// would have been checked against.
	endpoint.KnownHostsAddr = knownHostsTargetAddr(
		net.JoinHostPort(resolved.hostName, strconv.Itoa(resolved.port)), cfg)
	return endpoint, nil
}

// resolveCredential fills in WHAT this endpoint authenticates with.
//
// # The order, and why it is the profile's before the ladder's
//
// The coordinator's own auth chain tries several rungs in a fixed order, and
// this wire carries exactly ONE method per dial — deliberately, because a
// second attempt against one host is indistinguishable from password spraying
// and MaxAuthTries is finite. So the ladder has to be collapsed, and the rule
// for collapsing it is: what the PROFILE declared wins, and the chain's own
// order decides only among things nobody declared.
//
// That is what keeps a connection whose profile binds a stored password from
// silently being dialed with an agent key that happens to be loaded on this
// machine, while still letting a profile that declares no credential at all
// fall back the way the ladder does — a running agent first, then the prompt.
//
// # What this deliberately does NOT replicate
//
// The ladder's DEFAULT KEY DISCOVERY — ~/.ssh/id_ed25519, id_rsa, id_ecdsa —
// is not consulted here, and the reason is the ordering above. Those paths are
// the ladder's last-resort discovery rather than a credential a profile names,
// and they come BEFORE the stored password in the chain: resolving them here
// would silently re-key every connection whose profile binds a password to
// whichever default key happens to sit in the person's home, which is a change
// nobody asked for. A profile that means "use this key" names it (an inline
// file, or a vault key), and a profile that means "use my agent" says so.
func (rc *RealClient) resolveCredential(ctx context.Context, resolved *resolvedConfig, cfg *ConnectConfig, endpoint *DialEndpoint) error {
	mode := cfg.AuthMode
	passwordCapable := mode == "" || mode == "password"

	switch {
	case cfg.KeySecretID != "":
		// The signer is built here for its PUBLIC half only — the private
		// half never leaves the call, and the read is stanced so a sealed
		// vault raises the unlock a person answers (ADR-0032) rather than
		// being reported as an unusable credential.
		signer, err := rc.storedKeySigner(ctx, cfg)
		if err != nil {
			return err
		}
		endpoint.Auth = DialAuthKey
		endpoint.Credential = VaultRef(cfg.KeySecretID)
		endpoint.PublicKey = signer.PublicKey().Marshal()
		if cfg.PassphraseSecretID != "" {
			endpoint.Passphrase = VaultRef(cfg.PassphraseSecretID)
		}
		return nil

	case resolved.identityFile != "":
		// An inline key FILE. The bytes are read and parsed HERE — parse and
		// sign, never hand over — and the passphrase, when the profile binds
		// one, is read from the same store it has always been read from. A key
		// this process cannot unlock is ErrEncryptedKey, which the probe
		// reports as `needs-interactive`: the key is fine, it is only locked.
		signer, err := rc.loadKey(ctx, resolved.identityFile, cfg)
		if err != nil {
			return err
		}
		endpoint.Auth = DialAuthKey
		endpoint.Credential = FileRef(resolved.identityFile)
		endpoint.PublicKey = signer.PublicKey().Marshal()
		if cfg.PassphraseSecretID != "" {
			endpoint.Passphrase = VaultRef(cfg.PassphraseSecretID)
		}
		return nil

	case mode == "agent":
		return rc.resolveAgentCredential(resolved, endpoint)

	case cfg.SecretID != "" && passwordCapable:
		// "" is the auto ladder, which offers the stored password as a
		// PASSWORD method. The keyboard-interactive bucket is NOT folded in
		// here even though it presents the same secret: it is a different ssh
		// method, and sending a password instead would be this process
		// overriding a mode the user chose.
		endpoint.Auth = DialAuthPassword
		endpoint.Credential = VaultRef(cfg.SecretID)
		return nil

	case mode == "keyboardInteractive":
		return rc.resolveInteractiveCredential(resolved, cfg, endpoint)

	case mode == "" && rc.agentAvailable():
		// Auto, nothing declared, an agent loaded: the ladder's own next rung
		// after keys. It is consulted only where nothing was declared, so a
		// profile that binds a password is not quietly dialed with an agent
		// key.
		return rc.resolveAgentCredential(resolved, endpoint)

	case passwordCapable && cfg.PasswordRequester != nil:
		// The prompt rung — the ladder's last password-capable rung, and the
		// one a person answers. Wired only when the connection carries a
		// requester: a probe takes that wiring off (ssh.WithoutPasswordPrompt),
		// so a probe of such a profile declines here instead of raising a
		// dialog nobody asked for.
		endpoint.Auth = DialAuthInteractive
		return nil
	}

	// Everything else: a mode that filters out what the profile has, or a
	// connection with nothing declared and nothing to fall back to. The
	// sentence names the mode, because the fix is a profile edit and not a
	// host to go and look at.
	return &ErrNoAuthMethod{User: resolved.user, Host: resolved.hostName, Mode: mode}
}

// resolveAgentCredential offers the first key the running ssh-agent holds.
//
// # Why one key and not all of them
//
// The coordinator's own ladder offers every key the agent has, and lets the
// server try them in turn. This wire carries one identity, and that is the
// deliberate narrowing: a destination's identity is what the helper DECLARES
// before a signature is asked for, and a list would make every hop's handshake
// try keys the profile never named. Which key it picks is the agent's own
// order, which is the order `ssh` itself would try them in — and a profile
// that needs a later one can bind that key as an inline file, or load only it
// into the agent.
//
// The failure is ErrNoAuthMethod with Mode "agent", which is the type the app's
// own ladder has answered with since it had this rung: an agent is a fact about
// the person's desktop session, and the sentence says so.
func (rc *RealClient) resolveAgentCredential(resolved *resolvedConfig, endpoint *DialEndpoint) error {
	keys, err := rc.agentKeys()
	if err != nil {
		rc.log.Debug("no agent key to offer", "host", resolved.hostName, "error", err)
		return &ErrNoAuthMethod{User: resolved.user, Host: resolved.hostName, Mode: "agent"}
	}
	endpoint.Auth = DialAuthKey
	endpoint.Credential = AgentRef(gossh.FingerprintSHA256(keys[0]))
	endpoint.PublicKey = keys[0].Marshal()
	return nil
}

// resolveInteractiveCredential resolves the keyboard-interactive rung, which
// exists only where a person can be asked.
func (rc *RealClient) resolveInteractiveCredential(resolved *resolvedConfig, cfg *ConnectConfig, endpoint *DialEndpoint) error {
	if cfg.PasswordRequester == nil {
		// The rung is what this connection is set to, and there is nowhere to
		// raise it — a probe, or a backend with no renderer. Reported as "no
		// method" rather than as a failed authentication, because the server
		// was never asked anything.
		return &ErrNoAuthMethod{User: resolved.user, Host: resolved.hostName, Mode: cfg.AuthMode}
	}
	endpoint.Auth = DialAuthInteractive
	return nil
}

// WireDestination converts a resolved target into the destination the helper's
// wire carries: the address, the account, the credential reference, the public
// half a key needs, the storage identity each endpoint's key is verified
// under, and the route.
//
// It is the ONE conversion, and it lives here rather than in each caller
// because there are two of them (the coordinator's own helper adapters and the
// tunnel transport) and because the conversion is no longer a field-by-field
// copy: a route is a list of hops and a storage identity is a value only this
// package computes. Two copies of that would be two places for a hop to be
// dropped, and a dropped hop is a connection that dials DIRECTLY to a host the
// profile said is only reachable through a bastion — which fails as an
// unreachable host and reads as a network problem.
//
// This is the ONE place this package knows the helper's wire vocabulary, and
// the direction is deliberate: proto declares the shapes, sshsvc and the
// coordinator's adapters use them, and the alternative — every caller doing its
// own conversion — is what the paragraph above refuses.
func WireDestination(t DialTarget) proto.SSHDestination {
	d := proto.SSHDestination{
		Host:           t.Host,
		Port:           t.Port,
		User:           t.User,
		Identity:       WireIdentity(t.DialEndpoint),
		KnownHostsAddr: t.KnownHostsAddr,
	}
	if len(t.Route) == 0 {
		return d
	}
	d.Jumps = make([]proto.SSHHop, 0, len(t.Route))
	for _, hop := range t.Route {
		d.Jumps = append(d.Jumps, proto.SSHHop{
			Host:           hop.Host,
			Port:           hop.Port,
			User:           hop.User,
			Identity:       WireIdentity(hop),
			KnownHostsAddr: hop.KnownHostsAddr,
		})
	}
	return d
}

// WireIdentity is the credential half of the same conversion.
//
// The credential is present exactly when this endpoint HAS one: the interactive
// rung names a person, and an identity that carried an empty credential for it
// would make "a password identity names material" a rule the wire states and
// does not enforce.
func WireIdentity(ep DialEndpoint) proto.SSHIdentity {
	id := proto.SSHIdentity{
		Auth:      proto.SSHAuthKind(ep.Auth),
		PublicKey: ep.PublicKey,
	}
	if !ep.Credential.IsZero() || !ep.Passphrase.IsZero() {
		id.Credential = &proto.SSHCredential{
			Ref:           ep.Credential.String(),
			PassphraseRef: ep.Passphrase.String(),
		}
	}
	return id
}

// resolveRoute flattens the jump chain into the ordered hops a helper dials.
//
// It walks the same chain acquireJumpHost walks, in the same order and with the
// same reading of the two shapes a route can arrive in: the recursive
// JumpConfig the connection resolver builds, and the flat Jump* fields a
// single-hop profile sets. Each hop is resolved by resolveDialEndpoint, so a
// hop's credential goes through exactly the resolution the destination's does —
// a bastion key is read, parsed and signed for here, and never crosses.
func (rc *RealClient) resolveRoute(ctx context.Context, cfg *ConnectConfig) ([]DialEndpoint, error) {
	var route []DialEndpoint
	for parent := cfg; parent.JumpHost != "" || parent.JumpConfig != nil; {
		hopCfg := jumpConnectConfig(parent)
		// A hop's host is the PARENT's JumpHost, in both shapes a route
		// arrives in: the resolver sets it from the hop profile's own Host
		// (connection.buildConfig) and the connection path reads it from the
		// same field (acquireJumpHost resolves cfg.JumpHost with jumpCfg's
		// options). A chain whose parent names no host is malformed, and
		// resolving it against some other field would dial an address nobody
		// chose.
		if parent.JumpHost == "" {
			return nil, errors.New("ssh: a jump hop names no host")
		}
		// The binding the hop's credential is checked against, with
		// acquireJumpHost's own precedence: the hop's own reference, and the
		// parent's flat JumpSecretID only where the hop names none.
		hopSecretID := hopCfg.SecretID
		if hopSecretID == "" {
			hopSecretID = parent.JumpSecretID
		}
		hop, err := rc.resolveDialEndpoint(ctx, parent.JumpHost, hopCfg, parent.JumpAuthorizedEndpoint, hopSecretID)
		if err != nil {
			return nil, fmt.Errorf("resolve jump host %s: %w", parent.JumpHost, err)
		}
		route = append(route, hop)
		parent = hopCfg
	}
	return route, nil
}
