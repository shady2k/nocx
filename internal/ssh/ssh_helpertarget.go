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

// DialKey is ONE private key this endpoint may offer, and the credential that
// signs with it.
//
// It is the pair and not two parallel lists, because the two halves are one
// fact: x/crypto/ssh asks a Signer for PublicKey() BEFORE it asks for a
// signature, so a key is "the public half the far side is shown, and the
// reference this process signs with when it is challenged" — and a list of
// keys whose order did not travel with their references would be two things to
// keep in step across the wire.
type DialKey struct {
	// Credential names where the private half lives: a vault secret, a file
	// this process reads, an agent key by fingerprint. Opaque to the helper.
	Credential CredentialRef
	// Passphrase names the passphrase of an encrypted key, when the profile
	// bound one. Empty for an unencrypted key and for an agent key, which this
	// process never parses.
	Passphrase CredentialRef
	// PublicKey is the wire-format public half (RFC 4253 §6.6).
	PublicKey []byte
}

// DialEndpoint is one resolved ssh endpoint: the address, the account, what to
// present there, and the address its host key is stored under. It is a
// destination and a hop at once, because they are the same three facts and a
// hop is a destination that is not routed any further.
type DialEndpoint struct {
	Host string
	Port int
	User string
	Auth DialAuthKind
	// Credential names the material a PASSWORD presents. It is the
	// COORDINATOR's reference and is opaque to the helper, which echoes it back
	// when it asks for what it names. Empty for DialAuthKey, whose material is
	// in Keys, and for DialAuthInteractive, which names no material at all.
	Credential CredentialRef
	// Keys are the private keys this endpoint offers, IN ORDER — one for a
	// credential the profile named, and several for the discovery and
	// agent cases, where OpenSSH itself offers a queue of keys and lets the
	// server answer the first one it accepts.
	//
	// It is a list rather than a single key because one key per dial is a
	// refusal for two ordinary setups: an agent holding several keys, and a
	// home directory with more than one default key in it. Both are the
	// handshake's own shape — `publickey` is a query per key, and the far side
	// answers each in turn — so what travels is the queue, and NOT a second
	// attempt after a failed authentication, which is what MaxAuthTries exists
	// to bound.
	Keys []DialKey
	// KnownHostsAddr is the address this endpoint's host key is stored under,
	// which is the dial address for a direct route and a route-derived digest
	// for a host reached through a jump (see knownHostsTargetAddr). It is
	// resolved HERE because known_hosts is this process's file and the digest
	// is this package's rule: a helper that re-derived either would be a second
	// answer to "which machine is this".
	KnownHostsAddr string
	// ConnectionName and ProfileID name the saved connection this endpoint
	// was resolved from — the coordinator's own values, carried through so
	// a helper-hosted dial's interactive rung can echo them back on a
	// password ask (nocx-y6fh7 item 4, round 3), whichever of this
	// package's several destination-resolving callers (a pane's own spawn,
	// the shell-integration publish, a probe lease, a proxied channel) built
	// this endpoint — ONE resolution, here, rather than a second stamping
	// at each call site. Both empty for a hop: a jump host is not a saved
	// connection a person's remember checkbox could bind to, and for a
	// direct-host open, which names no profile at all.
	ConnectionName string
	ProfileID      string
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
		// Read off cfg directly rather than a caller's claim: this IS the
		// connection.Resolver's own value when cfg is a profile's config
		// (buildConfig sets both), and the zero value for a hop built with
		// no such option (jumpConnectConfig's flat fallback) or a
		// direct-host open — both real states, not gaps (nocx-y6fh7 item 4,
		// round 3).
		ConnectionName: cfg.ConnectionName,
		ProfileID:      cfg.ProfileID,
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
// this wire carries ONE METHOD per dial for a STORED secret — deliberately,
// because repeating one against one host is indistinguishable from password
// spraying and MaxAuthTries is finite. So the ladder has to be collapsed, and
// the rule for collapsing it is: what the PROFILE declared wins, and the
// chain's own order decides only among things nobody declared.
//
// The interactive rung is the one exception, and it is not the same rule
// relaxed: nothing stored is repeated under a second name there, because
// nothing is stored. `DialAuthInteractive` offers the server BOTH
// password-shaped methods (`password` and `keyboard-interactive`) precisely
// because it carries no material of its own to pick one in advance — each is
// the SAME live person answering the SAME question through the coordinator's
// one ask (sshsvc.authMethods), so a server that speaks either is answered
// once, not sprayed twice.
//
// That is what keeps a connection whose profile binds a stored password from
// silently being dialed with an agent key that happens to be loaded on this
// machine — and it is also why DEFAULT KEY DISCOVERY is the arm for "the
// profile named nothing", not an arm that outranks a stored password. Those
// paths are the ladder's last-resort discovery rather than a credential a
// profile names; putting them above the password rung would re-key every
// connection whose profile binds a password to whichever default key happens to
// sit in the person's home, which is a change nobody asked for.
//
// # What the discovery arm does, and why it is not optional
//
// A profile that names NOTHING is the ordinary local setup — "connect to this
// host with my keys" — and OpenSSH answers it by offering every key it finds:
// the agent's, and the identity files its configuration lists, which is ssh's
// own default list when the configuration lists none. Without this arm such a
// profile is REFUSED by name through the helper, which is the ordinary setup
// turned into a failure. The order, the deduplication and the fall-through past
// a locked key are discoveredKeys' own subject.
//
// One method, several KEYS: the queue above is a list of public keys inside a
// single `publickey` method, and each is a query the far side answers in turn.
// That is not a second attempt at authentication, it is what the method is —
// the same shape the agent rung has always had on the coordinator's own path.
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
		endpoint.Keys = []DialKey{{
			Credential: VaultRef(cfg.KeySecretID),
			Passphrase: passphraseRef(cfg.PassphraseSecretID),
			PublicKey:  signer.PublicKey().Marshal(),
		}}
		return nil

	case cfg.KeyFile != "":
		// An inline key FILE: what the profile named. The bytes are read and
		// parsed HERE — parse and sign, never hand over — and the passphrase,
		// when the profile binds one, is read from the same store it has always
		// been read from. A key this process cannot unlock is ErrEncryptedKey,
		// which the probe reports as `needs-interactive`: the key is fine, it
		// is only locked.
		signer, err := rc.loadKey(ctx, cfg.KeyFile, cfg)
		if err != nil {
			return err
		}
		endpoint.Auth = DialAuthKey
		endpoint.Keys = []DialKey{{
			Credential: FileRef(cfg.KeyFile),
			Passphrase: passphraseRef(cfg.PassphraseSecretID),
			PublicKey:  signer.PublicKey().Marshal(),
		}}
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

	case mode == "" || mode == "publicKey":
		// NOTHING declared, and the mode permits a key: OpenSSH's own
		// discovery, in OpenSSH's own order.
		if keys := rc.discoveredKeys(ctx, resolved, cfg); len(keys) > 0 {
			endpoint.Auth = DialAuthKey
			endpoint.Keys = keys
			return nil
		}
		// Nothing was found to offer, and that is a state with two different
		// endings rather than one: a connection that can ask a PERSON still has
		// the ladder's last rung, and one that cannot — a probe, which takes
		// the requester off — is refused below by name. "No default key is
		// present" therefore reads as the refusal it is, and never as a dial
		// with no method.
		if passwordCapable && cfg.PasswordRequester != nil {
			endpoint.Auth = DialAuthInteractive
			return nil
		}

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

// passphraseRef names a passphrase only when the profile bound one: an empty
// secret id is the zero reference, which is how "this key has no passphrase"
// crosses.
func passphraseRef(id credential.SecretID) CredentialRef {
	if id == "" {
		return CredentialRef{}
	}
	return VaultRef(id)
}

// resolveAgentCredential offers EVERY key the running ssh-agent holds, in the
// agent's own order.
//
// # Why the whole set and not the first one
//
// An agent is where a person's keys are: several of them, for several hosts,
// and the one a given host accepts is frequently not the one the agent lists
// first. Offering one was a refusal for that setup — and a refusal that reads
// as "the host rejected your credential", which sends somebody to look at a
// host that is fine.
//
// The list is the handshake's own shape rather than a second attempt: ssh's
// `publickey` method is a QUERY per key ("would you accept this one?") and the
// server answers each in turn, which is what `ssh` itself does with an agent
// holding several keys. `gossh.PublicKeys(signers...)` is that method, so the
// order the agent reports is the order the far side sees, and the first key it
// accepts is the one that signs.
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
	endpoint.Keys = agentDialKeys(keys)
	return nil
}

// agentDialKeys renders agent public keys into what the wire carries: a
// fingerprint reference and the public half, one pair per key, in order.
func agentDialKeys(keys []gossh.PublicKey) []DialKey {
	out := make([]DialKey, 0, len(keys))
	for _, key := range keys {
		out = append(out, DialKey{
			Credential: AgentRef(gossh.FingerprintSHA256(key)),
			PublicKey:  key.Marshal(),
		})
	}
	return out
}

// discoveredKeys is OpenSSH's own key discovery, for a connection whose profile
// names no credential at all: the keys the agent holds, and the identity files
// the resolved configuration lists.
//
// # Where the list comes from, and why not from here
//
// The identity files are the resolver's answer — every `identityfile` line of
// ssh -G's output, in its order, which is OpenSSH's own default list when the
// configuration names none. They are read through the ONE resolver this
// package already has (ADR-0015) rather than from a list kept here: the
// hard-coded `id_ed25519, id_rsa, id_ecdsa` that used to sit beside it was a
// second answer to "which keys does this machine offer", and it disagreed with
// ssh on the order, on the key types ssh 10 knows (id_ecdsa_sk, id_mldsa44…)
// and on every config that names its own files.
//
// # The order
//
// OpenSSH's own queue (`pubkey_prepare`) is: certificates, the identity files
// the agent also holds, the agent's other keys, PKCS#11 keys, and last the
// identity files read from disk. Two of those arms do not exist here — this
// package handles no certificate files and no PKCS#11 provider — and what is
// left collapses to what is built below: the AGENT's keys first, then the
// identity files, with the file whose key the agent already offered dropped.
// The collapse preserves the one thing the first two arms exist for: a key the
// agent holds is used through the agent rather than by reading the private key
// from disk, and the key is offered once.
//
// # IdentitiesOnly
//
// It suppresses the agent's keys the configuration does not name, and NOT the
// agent as a rung: a key that an `identityfile` names is still signed for by
// the agent, which is the case the directive exists to make work (a passphrase
// protected key, loaded once, while other keys on the agent stay unoffered).
// That is why the files are resolved BEFORE the agent is asked: whether a key
// the agent holds is admissible is a question about these files, and answering
// it by suppressing the agent entirely would refuse exactly that setup.
//
// A file that cannot be read, cannot be parsed, or is encrypted with a
// passphrase the profile did not bind is SKIPPED, not fatal — ssh offers the
// next key, and it does not stop to ask for a passphrase a profile never named.
// A `.pub` beside such a key is still read, because that public half is what
// lets the agent's copy of the key stay admissible under IdentitiesOnly.
func (rc *RealClient) discoveredKeys(ctx context.Context, resolved *resolvedConfig, cfg *ConnectConfig) []DialKey {
	type fileKey struct {
		path   string
		signer gossh.Signer
	}
	var (
		files []fileKey
		named = map[string]bool{}
	)
	for _, path := range resolved.identityFiles {
		signer, err := rc.loadKey(ctx, path, cfg)
		if err != nil {
			rc.log.Debug("skipping a key file in discovery", "path", path, "error", err)
			if resolved.identitiesOnly {
				if pub := publicHalfOf(path); pub != nil {
					named[gossh.FingerprintSHA256(pub)] = true
				}
			}
			continue
		}
		files = append(files, fileKey{path: path, signer: signer})
		named[gossh.FingerprintSHA256(signer.PublicKey())] = true
	}

	var keys []DialKey
	if rc.agentAvailable() {
		agentKeys, err := rc.agentKeys()
		if err != nil {
			rc.log.Debug("no agent key to offer", "host", resolved.hostName, "error", err)
		}
		for _, key := range agentKeys {
			if resolved.identitiesOnly && !named[gossh.FingerprintSHA256(key)] {
				continue
			}
			keys = append(keys, DialKey{
				Credential: AgentRef(gossh.FingerprintSHA256(key)),
				PublicKey:  key.Marshal(),
			})
		}
	}

	seen := map[string]bool{}
	for _, f := range keys {
		seen[string(f.PublicKey)] = true
	}
	for _, f := range files {
		pub := f.signer.PublicKey().Marshal()
		if seen[string(pub)] {
			// The agent already offered this key, and its copy signs for it.
			continue
		}
		seen[string(pub)] = true
		keys = append(keys, DialKey{
			Credential: FileRef(f.path),
			Passphrase: passphraseRef(cfg.PassphraseSecretID),
			PublicKey:  pub,
		})
	}
	return keys
}

// publicHalfOf answers the public key the `.pub` file beside a private key
// names, and nil when there is none to read.
//
// It exists for one case: an ENCRYPTED private key that the agent holds. This
// process cannot parse the file — that is what encrypted means — and it does not
// need to, because the signature comes from the agent; all it needs is the
// public half, to declare the key and to know that IdentitiesOnly admits it.
// OpenSSH reads exactly this file for exactly this reason
// (`sshkey_load_public` tries `<path>.pub`).
func publicHalfOf(path string) gossh.PublicKey {
	data, err := readFileFn(path + ".pub")
	if err != nil {
		return nil
	}
	key, _, _, _, err := gossh.ParseAuthorizedKey(data)
	if err != nil {
		return nil
	}
	return key
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
		ConnectionName: t.ConnectionName,
		ProfileID:      t.ProfileID,
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
// Each kind is spelled ONCE, in the field its kind owns: a password carries a
// credential, a key carries the ordered list of keys it offers, and the
// interactive rung — a person — carries neither, which is what makes "a
// password identity names material" and "a key identity names the keys it
// declares" rules the wire states rather than ones it merely implies.
func WireIdentity(ep DialEndpoint) proto.SSHIdentity {
	id := proto.SSHIdentity{Auth: proto.SSHAuthKind(ep.Auth)}
	switch ep.Auth {
	case DialAuthKey:
		id.Keys = make([]proto.SSHKeyOffer, 0, len(ep.Keys))
		for _, k := range ep.Keys {
			id.Keys = append(id.Keys, proto.SSHKeyOffer{
				Credential: proto.SSHCredential{
					Ref:           k.Credential.String(),
					PassphraseRef: k.Passphrase.String(),
				},
				PublicKey: k.PublicKey,
			})
		}
	default:
		if !ep.Credential.IsZero() {
			// A password has no second half: the passphrase belongs to a KEY,
			// and it now travels with the key it unlocks (DialKey.Passphrase).
			id.Credential = &proto.SSHCredential{Ref: ep.Credential.String()}
		}
	}
	return id
}

// jumpConnectConfig answers the ConnectConfig one hop is resolved and dialed
// with.
//
// A route reaches this package in two shapes and they mean the same thing: the
// recursive JumpConfig the connection resolver builds for a chain of profiles,
// and the flat Jump* fields a single-hop profile sets. This is the ONE place
// that reading happens — acquireJumpHost dials through it and ResolveTarget
// resolves through it — because two readings would eventually disagree about
// the one hop that matters, and a helper would then dial a different account
// or a different credential than the coordinator's own path would have.
func jumpConnectConfig(parent *ConnectConfig) *ConnectConfig {
	if parent.JumpConfig != nil {
		return parent.JumpConfig
	}
	return &ConnectConfig{
		User:               parent.JumpUser,
		Port:               parent.JumpPort,
		KeyFile:            parent.JumpKeyFile,
		AuthMode:           parent.JumpAuthMode,
		Secrets:            parent.JumpSecrets,
		SecretID:           parent.JumpSecretID,
		PassphraseSecretID: parent.JumpPassphraseSecretID,
	}
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
