package proto

// The `ssh` service: the ONE place an ssh connection is dialed, and the two
// directions it speaks.
//
// # Why this is a helper service and not the coordinator's own client
//
// The owner's invariant of 2026-09-13 is that there is no ssh connection
// without a helper: every dial is made by the LOCAL helper and the coordinator
// holds no ssh client once the consumers have moved (epic nocx-50w7p, plan
// §1-§3). A helper is a different process on the same machine, so a dial it
// makes needs two things the coordinator used to have in-process — the
// credential material, and the decision about a host key it has never seen.
// Neither may be moved into the helper: it holds no secret at rest, it writes
// no known_hosts, and `ssh/knownhosts` is a forbidden import there
// (internal/helper/deploy/dependency_test.go). So the helper ASKS, and this is
// the shape of the asking.
//
// # Two directions, one service name
//
// `probe` is a FORWARD op: the coordinator asks the helper, and the helper
// answers. `secret`, `sign`, `verify-host-key` and `trust-host-key` are
// REVERSE ops: the helper asks the coordinator over the connection the
// request arrived on (host.Host.Ask), and the coordinator answers through a
// handler it registered at its composition root (client.ReverseRegistry).
//
// They share one name because they are one subject — this machine's ssh
// client — and the op names are disjoint, so the two dispatchers (the host's,
// which answers forward ops, and the client's, which answers reverse ones)
// cannot collide. A service name per direction would be two names for one
// answer, which is what AD-8 refuses.
//
// # The credential reference, and why the helper cannot read it
//
// SSHCredential is OPAQUE to the helper. The coordinator mints it, the helper
// echoes it back verbatim in every ask, and the coordinator is the only party
// that knows what it names. That is the whole authorization story on this
// wire: a helper cannot ask for material the coordinator did not name, and
// the coordinator resolves what it is asked for through the same stanced
// credential resolver its own dial path uses (internal/credential).
//
// # The public key is NOT a discovery question
//
// Key authentication needs the helper to declare WHICH key it is offering
// before the peer asks for a signature — x/crypto/ssh's Signer interface
// answers PublicKey() first — and the private half must never cross. So the
// public half is public material carried in the identity, put there by the
// coordinator that already holds the key, and `sign` stays exactly what its
// name says: one challenge in, one signature out. There is deliberately no
// "identify this credential" op and no empty-challenge sentinel, because a
// magic value inside a cryptographic request is a behaviour nobody can see
// and nobody can test.
//
// # Why a password crosses and a key does not
//
// A signature is the only way to prove a key without handing it over, and ssh
// has supported that since the beginning. A password has no such protocol: the
// helper must PRESENT it, so it crosses, over a socket the helper and the
// coordinator both own (D12's same-UID trust boundary — any nocx running as
// that account may connect). A passphrase never crosses at all: the party that
// parses a private key is the coordinator, which is why `secret`'s passphrase
// purpose exists for a helper that needs material the coordinator cannot use
// on its behalf, and why this generation's probe asks only for a password.

// ServiceSSH is the name of the helper service that owns this machine's ssh
// client. It is registered only by a helper built with nocx_local_ssh — the
// tag that links the ssh client and is absent from the artifact deployed to a
// host we do not own (D11) — so a deployed helper answers `unknown_service`
// to every op below, which is an answer and not a failure.
const ServiceSSH = "ssh"

// OpProbe is the forward op: dial one host, authenticate with the credential
// the coordinator named, report the outcome, close.
//
// It exists because the settings surface has to answer "does this credential
// work against this host" WITHOUT opening a session — the coordinator's own
// path for that is the pool-bypassing Probe (internal/ssh, ssh_real.go), and
// once the dial moves to the helper so does the question.
const OpProbe = "probe"

// The REVERSE ops. Each is asked by the helper and answered by the coordinator.
const (
	// OpSecret returns credential material the helper must PRESENT: a
	// password for a password rung, a passphrase for a helper that has to
	// unlock key material itself. The coordinator resolves the reference
	// through the stance its own dial path declares (credential.Operation),
	// so a sealed vault raises the unlock it already owns rather than
	// answering with a wrong-but-plausible "no such secret".
	OpSecret = "secret"
	// OpSign returns one signature over one challenge, made with the private
	// key the reference names. The key itself never crosses.
	OpSign = "sign"
	// OpVerifyHostKey asks what to make of a host key the helper was just
	// offered. Unlike the coordinator's own accept flow, the helper has no
	// other option: `knownhosts` is a forbidden import there, so its host-key
	// callback cannot decide anything by itself (internal/helper/deploy).
	OpVerifyHostKey = "verify-host-key"
	// OpTrustHostKey records a key the coordinator has already decided to
	// accept. The DECISION is not made here: it travels in ProbeParams
	// .AcceptOnTrust, set by the caller that has asked whatever it asks before
	// a first connection — so a helper cannot turn "ask the user" into "trust
	// it", and the write stays where known_hosts lives.
	OpTrustHostKey = "trust-host-key"
)

// SSHCredential is an opaque handle on one stored credential, minted by the
// coordinator. The helper never interprets either field; it echoes them.
//
// PassphraseRef is the second half of a key credential and it is optional
// because most keys are not encrypted. It is a REFERENCE, never material: the
// coordinator is what parses a private key, so the passphrase is resolved by
// the same party the key lives in and never reaches this wire.
type SSHCredential struct {
	// Ref names the material the credential's auth kind presents: the
	// password for password auth, the private key for key auth.
	Ref string `json:"ref"`
	// PassphraseRef names the passphrase of an encrypted key. Empty for a
	// password credential and for an unencrypted key.
	PassphraseRef string `json:"passphraseRef,omitempty"`
}

// SSHAuthKind is how a probe authenticates, in a closed set. It is the
// coordinator's decision and not the helper's: the coordinator knows which
// material it stored, and a helper that guessed would send the wrong rung.
type SSHAuthKind string

const (
	// SSHAuthPassword presents the credential's password.
	SSHAuthPassword SSHAuthKind = "password"
	// SSHAuthKey proves possession of the credential's private key by
	// signing the handshake's challenge.
	SSHAuthKey SSHAuthKind = "key"
)

// SSHIdentity is the credential a probe authenticates with, plus the public
// half of a key credential the helper must declare before it is asked to sign.
//
// PublicKey is the wire-format public key blob (RFC 4253 §6.6, what
// gossh.Marshal on a PublicKey produces). It is required for SSHAuthKey and
// absent for SSHAuthPassword; a helper that is asked to sign with a key it was
// not given will refuse rather than invent one.
type SSHIdentity struct {
	Credential SSHCredential `json:"credential"`
	Auth       SSHAuthKind   `json:"auth"`
	// PublicKey is base64 in JSON (encoding/json's []byte). Public material:
	// it is what the peer learns anyway on the first packet.
	PublicKey []byte `json:"publicKey,omitempty"`
}

// ProbeParams is one credential test against one host.
//
// Host, Port and User are RESOLVED values. Alias resolution, ~/.ssh/config
// merging and the credential's own authorization against the endpoint stay in
// the coordinator — it is the party that reads the config and holds the
// binding, and moving that into the helper would be a second answer to "which
// host is this" (plan §3). The helper dials exactly what it is told.
type ProbeParams struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	User string `json:"user"`
	// Identity is what to authenticate with. It is required: a probe with no
	// credential is not a question this op can answer, and letting it fall
	// through to "try nothing" would report the host's refusal as the
	// credential's.
	Identity SSHIdentity `json:"identity"`
	// AcceptOnTrust is the coordinator's answer to "may a key this host has
	// never presented be recorded". It is the CALLER's decision travelling
	// with the request: the coordinator sets it only after the accept flow it
	// already owns has run, so a helper cannot trust a host on its own
	// initiative. False is the ordinary value and produces the
	// `host-key-unknown` outcome below.
	AcceptOnTrust bool `json:"acceptOnTrust"`
}

// ProbeOutcome is the closed set of answers a probe can give. The spellings
// are ssh.ProbeOutcome's own, character for character, because the
// coordinator classifies its own probes with ssh.ClassifyProbeError and a
// second vocabulary for one fact is what AD-8 exists to prevent.
//
// The two halves are spelled here rather than imported because this package
// is the wire's leaf: internal/ssh imports x/crypto/ssh and pkg/sftp, and
// proto is linked by every helper — including the untagged artifact deployed
// to somebody else's host, which must not reach either
// (internal/helper/deploy/dependency_test.go).
// TestProbeOutcomeSpellingsMatchTheSSHVocabulary is the guard that keeps the
// two in step, and it lives with the code that converts between them
// (internal/helper/sshsvc).
type ProbeOutcome string

const (
	// ProbeAccepted means the host key was accepted AND an authentication
	// method succeeded.
	ProbeAccepted ProbeOutcome = "accepted"
	// ProbeRejected means the server refused the credential offered.
	ProbeRejected ProbeOutcome = "rejected"
	// ProbeUnreachable means no connection was established: refused, timed
	// out, or cancelled.
	ProbeUnreachable ProbeOutcome = "unreachable"
	// ProbeHostKeyUnknown means the host presented a key nobody has recorded.
	// It is routine first contact rather than a failure, which is why it is
	// not spelled `rejected`.
	ProbeHostKeyUnknown ProbeOutcome = "host-key-unknown"
	// ProbeHostKeyChanged means a recorded host presented a DIFFERENT key.
	// It is never conflated with the routine case above: this is the one
	// signature of a machine in the middle.
	ProbeHostKeyChanged ProbeOutcome = "host-key-changed"
	// ProbeNeedsInteractive means the credential needs something a machine
	// cannot supply on its own — a passphrase nobody stored — and the
	// coordinator must ask.
	ProbeNeedsInteractive ProbeOutcome = "needs-interactive"
)

// ProbeResult is the probe's answer. Outcome is always present; Detail is the
// classifier's own sentence, for the log and for the evidence a settings
// surface shows next to the outcome.
//
// A probe that cannot be classified at all is NOT a result: it is a refusal
// from the op (`internal`), because "the dial failed and we do not know why"
// must remain distinguishable from a classified rejection.
type ProbeResult struct {
	Outcome ProbeOutcome `json:"outcome"`
	Detail  string       `json:"detail"`
}

// SecretPurpose is what the asked-for material is FOR, in a closed set.
//
// The purpose is not decoration: the read is stanced (internal/credential), so
// the coordinator resolves a password and a passphrase through different
// declarations, different reasons in the vault's audit, and — where a profile
// carries its own policy — different answers.
type SecretPurpose string

const (
	// PurposePassword asks for the secret a password rung presents to the
	// server.
	PurposePassword SecretPurpose = "password"
	// PurposePassphrase asks for the passphrase of an encrypted private key,
	// for a caller that has to unlock key material itself.
	PurposePassphrase SecretPurpose = "passphrase"
)

// SecretParams asks the coordinator for material by reference and purpose.
type SecretParams struct {
	Credential SSHCredential `json:"credentialRef"`
	Purpose    SecretPurpose `json:"purpose"`
}

// SecretResult carries the material. It is base64 in JSON ([]byte) and it is
// the ONE field on this wire that is a secret in the ordinary sense: a
// password has to be presented by the party holding the connection, and the
// helper is that party. It is never logged, never stored, and never returned
// by the helper — see the package comment above for why this widening is
// stated rather than hidden.
type SecretResult struct {
	Secret []byte `json:"secret"`
}

// SignParams asks for one signature. The shape is deliberately two fields: a
// credential to sign with and a challenge to sign, with the key itself staying
// where it is.
type SignParams struct {
	Credential SSHCredential `json:"credentialRef"`
	// Challenge is the bytes the peer asked to be signed, exactly as
	// x/crypto/ssh handed them to the signer — the session id and the
	// userauth request, already hashed by nothing and hashed by the signer.
	// It is required and must be non-empty: signing nothing is a request
	// nobody makes, so it is refused rather than treated as a discovery
	// question (see the package comment).
	Challenge []byte `json:"challenge"`
	// Algorithm is the signature algorithm the caller expects, as the public
	// key's own type string (`ssh-ed25519`, `rsa-sha2-256`, …). It is
	// checked against the key the credential resolves to, so a mismatch is
	// refused here instead of producing a signature the peer will not accept.
	Algorithm string `json:"algorithm"`
}

// SignResult is the signature, in the wire encoding of an ssh signature
// (string format followed by string blob, RFC 4253 §6.6) — the form
// x/crypto/ssh's own Unmarshal reads back into a Signature, which is how the
// helper hands it to the library unchanged.
type SignResult struct {
	Signature []byte `json:"signature"`
}

// VerifyHostKeyParams asks what to make of a host key the helper was just
// offered during a handshake.
type VerifyHostKeyParams struct {
	// Host is the storage identity the coordinator looks the key up under —
	// an address for a direct route. It is the coordinator's value and not
	// something the helper derives: the identity of a route through a jump
	// host is a digest only the coordinator can compute (ssh_real.go's
	// knownHostsTargetAddr), and a helper that guessed it would write a line
	// the next connection would not find.
	Host      string `json:"host"`
	Algorithm string `json:"algorithm"`
	// Key is the wire-format public key blob the peer presented.
	Key []byte `json:"key"`
}

// HostKeyVerdict is the closed set of answers to "what is this key".
//
// There is no `trusted` value in internal/ssh, and that is not a gap: a
// trusted key is the ABSENCE of a host-key error there (nil from the callback,
// ssh_real.go's hostKeyCallbackFor). Naming the three outcomes here is what
// turns that absence into something a helper — which has no callback to
// consult — can carry.
type HostKeyVerdict string

const (
	// HostKeyTrusted means the key is the one recorded for this host, or the
	// coordinator has just recorded it.
	HostKeyTrusted HostKeyVerdict = "trusted"
	// HostKeyChanged means a key IS recorded and this is a different one.
	HostKeyChanged HostKeyVerdict = "changed"
	// HostKeyUnknown means nothing is recorded for this host.
	HostKeyUnknown HostKeyVerdict = "unknown"
)

// VerifyHostKeyResult is the verdict plus the evidence a person is shown when
// they are asked about it — the offered fingerprint always, and the recorded
// one exactly when the answer is `changed`, so a changed key can never be
// rendered without the value it changed from.
type VerifyHostKeyResult struct {
	Verdict HostKeyVerdict `json:"verdict"`
	// Fingerprint is the SHA256 fingerprint of the offered key, in the
	// `SHA256:base64` form ssh.FingerprintSHA256 produces.
	Fingerprint string `json:"fingerprint"`
	// Expected is the recorded fingerprint(s), comma-joined, and it is
	// present exactly when Verdict is `changed`. It is spelled the way the
	// coordinator's own host-key error spells it (ErrHostKeyMismatch
	// .Expected), so the sentence a person reads is the same one the probe
	// path has always shown.
	Expected string `json:"expected,omitempty"`
}

// TrustHostKeyParams names a key to record for a host. The key is public
// material the peer presented.
type TrustHostKeyParams struct {
	// Host is the storage identity, as in VerifyHostKeyParams.
	Host      string `json:"host"`
	Algorithm string `json:"algorithm"`
	Key       []byte `json:"key"`
}

// TrustHostKeyResult reports what the write produced: the fingerprint of the
// key now recorded for that host. It is the answer the coordinator's own
// accept path already returns (RealClient.TrustHostKey), unchanged.
type TrustHostKeyResult struct {
	Fingerprint string `json:"fingerprint"`
}

// The ssh service's wire refusals. Two, and each names a state the caller acts
// on differently — which is the test a new code has to pass before it earns a
// place here.
const (
	// ErrCodeNoAuthChannel means this helper has nowhere to ask: the request
	// arrived without a coordinator connection on it. An open is always
	// coordinator-initiated, so this is the RE-DIAL case (a replacement
	// coordinator that has not reconnected yet), and the honest answer is a
	// refusal naming it — never a hang, never a retry loop, never a stored
	// fallback.
	ErrCodeNoAuthChannel = "no_auth_channel"
	// ErrCodeVaultSealed means the coordinator could not read the material
	// because its vault is not open. It crosses as its own code rather than
	// as `internal` because the coordinator's caller turns exactly this into
	// the unlock it already owns (the renderer's vault sheet), and a generic
	// refusal would send the user to look at the host instead.
	ErrCodeVaultSealed = "vault_sealed"
	// ErrCodeNeedsInteractive means the material exists but cannot be used
	// without a person: an encrypted private key whose passphrase the
	// coordinator has nowhere to read. It is the key half of the
	// `needs-interactive` outcome the probe reports.
	ErrCodeNeedsInteractive = "needs_interactive"
)
