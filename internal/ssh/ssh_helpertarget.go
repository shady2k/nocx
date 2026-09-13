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
// # What it deliberately does NOT resolve
//
// A route through a jump host. The helper's dial is direct (Req 1 of this
// bead's ABI: one address, one client configuration), and a bastioned route
// would need the helper to dial the bastion with a SECOND credential and a
// second host-key answer. Refusing it by name is the honest state of that
// work — proto.ProbeParams has the same hole today — and it is a refusal and
// not a silent direct dial, because a direct dial to a host that is only
// reachable through a bastion fails as an unreachable host, which reads as a
// network problem rather than as the missing feature it is.

import (
	"context"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/credential"
)

// DialAuthKind is how a resolved target authenticates, in the closed set the
// helper's wire carries (proto.SSHAuthKind, spelled the same and held to it by
// the coordinator's own mapping).
type DialAuthKind string

const (
	// DialAuthPassword presents a stored password.
	DialAuthPassword DialAuthKind = "password"
	// DialAuthKey proves possession of a stored private key by signing.
	DialAuthKey DialAuthKind = "key"
)

// DialTarget is everything a helper needs to open a connection, and nothing
// it may decide for itself.
type DialTarget struct {
	Host string
	Port int
	User string
	Auth DialAuthKind
	// Credential names the material to present: the password for
	// DialAuthPassword, the private key for DialAuthKey. It is the
	// COORDINATOR's reference (a credential.SecretID) and is opaque to the
	// helper, which echoes it back when it asks for what it names.
	Credential credential.SecretID
	// Passphrase names the passphrase of an encrypted key. Empty for a
	// password and for an unencrypted key.
	Passphrase credential.SecretID
	// PublicKey is the wire-format public half of a key credential (RFC 4253
	// §6.6). Required for DialAuthKey: x/crypto/ssh asks a signer for
	// PublicKey() BEFORE it asks for a signature, so the party that dials must
	// be able to declare which key it is offering without holding it.
	PublicKey []byte
}

// ErrNoHelperIdentity is raised when the connection's credential is something
// a helper cannot be handed: an inline key file, the agent, or the prompt rung.
//
// It is a refusal by NAME and not a fallback, and the reason is the invariant
// rather than convenience: falling back to dialing here would be the
// coordinator holding an ssh client, which is the state this whole level
// exists to end. The sentence names what is missing so the next person knows
// which of the three to implement.
var ErrNoHelperIdentity = errors.New(
	"ssh: this connection's credential cannot be offered by a helper yet")

// ErrRoutedDial is raised for a destination reached through a jump host (see
// the file header).
var ErrRoutedDial = errors.New(
	"ssh: a destination reached through a jump host is not yet opened by a helper")

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
	resolved, err := rc.resolveConfig(ctx, host, cfg)
	if err != nil {
		return DialTarget{}, fmt.Errorf("resolve config for %s: %w", host, err)
	}
	if cfg.JumpHost != "" || cfg.JumpConfig != nil {
		return DialTarget{}, fmt.Errorf("%w: %s", ErrRoutedDial, host)
	}
	// The same check acquirePooled runs before its own dial, called through
	// the same function: a helper-opened connection must be bound by exactly
	// the authorization a coordinator-opened one was, and the way to guarantee
	// that is to run the check rather than to describe it.
	if cfg.Secrets != nil {
		resolvedAuthz := rc.resolveAuthzEndpoint(ctx, cfg.AuthorizedEndpoint)
		if authErr := checkAuthorization(resolvedAuthz, resolved, string(cfg.SecretID), false); authErr != nil {
			return DialTarget{}, authErr
		}
	}

	target := DialTarget{
		Host: resolved.hostName,
		Port: resolved.port,
		User: resolved.user,
	}
	switch {
	case cfg.KeySecretID != "":
		// The signer is built here for its PUBLIC half only — the private
		// half never leaves the call, and the read is stanced so a sealed
		// vault raises the unlock a person answers (ADR-0032) rather than
		// being reported as an unusable credential.
		signer, err := rc.storedKeySigner(ctx, cfg)
		if err != nil {
			return DialTarget{}, err
		}
		target.Auth = DialAuthKey
		target.Credential = cfg.KeySecretID
		target.Passphrase = cfg.PassphraseSecretID
		target.PublicKey = signer.PublicKey().Marshal()
	case cfg.SecretID != "" && (cfg.AuthMode == "" || cfg.AuthMode == "password"):
		// "" is the auto ladder, which offers the stored password as a
		// PASSWORD method; "password" names that bucket explicitly. The
		// keyboard-interactive bucket is NOT folded in here even though it
		// presents the same secret: it is a different ssh method, the profile
		// that selected it selected that method, and sending a password
		// instead would be the coordinator overriding a mode the user chose.
		// It falls to the refusal below until the wire carries a method of
		// its own.
		target.Auth = DialAuthPassword
		target.Credential = cfg.SecretID
	default:
		// Everything else: an inline key file (the helper has no file), the
		// agent (it has no socket), the prompt rung (nobody can answer for a
		// helper), or a stored password on a connection whose mode excludes
		// the password buckets. Named as one refusal because the caller's next
		// act is the same for all of them: report which credential the helper
		// cannot be handed.
		return DialTarget{}, fmt.Errorf("%w: host %s, user %s, mode %q", ErrNoHelperIdentity, resolved.hostName, resolved.user, cfg.AuthMode)
	}
	return target, nil
}
