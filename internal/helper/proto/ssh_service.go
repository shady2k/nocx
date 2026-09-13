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

// OpOpen is the forward op that opens one PROXIED CHANNEL on the pooled
// connection for a destination, and answers the id its bytes will be keyed by.
//
// It is a separate op from `probe` for a reason that is not stylistic. A probe
// deliberately leaves nothing behind — it dials, it authenticates, it closes —
// while this one must leave exactly one thing behind: a live stream whose
// lifetime the CALLER owns, on a connection the helper keeps pooled for the
// next channel. Folding them into one op with a flag would make "hold this
// open" a mode of "answer and close", and the two would then have to agree
// about pool semantics that only one of them has.
//
// The destination rides in full (SSHDestination): host, port and user are
// RESOLVED values and the identity is the same one `probe` takes, so the
// helper neither reads ~/.ssh/config nor decides which credential to offer.
const OpOpen = "open"

// OpClose ends one proxied channel and releases the pooled reference the
// helper took for it. It is idempotent at the helper: an id it does not hold
// is answered as closed rather than refused, because the ordinary caller is a
// process shutting down and a second close is not a disagreement about state.
const OpClose = "close"

// OpForward asks the helper for a LISTENER on the far side: the tcpip-forward
// request of a remote forward (-R), and the transport the remote lifecycle
// channel rides (ADR-0024). It answers the identity the listener will be
// announced under and the address the server actually bound.
//
// It is its own op rather than a `kind` of `open` because the two leave
// different things behind and are ended by different ops: a channel is one
// stream a caller holds, and a listener is a stream FACTORY whose accepted
// connections arrive as channels nobody asked for by name. Folding them
// together would make "accept on the far side" a mode of "open one stream",
// and the two would then have to agree about who announces what.
const OpForward = "forward"

// OpUnforward ends one listener and the accepted channels still riding it.
// Like `close` it is idempotent: an id this helper does not hold is answered
// as done, because the ordinary caller is a lease being released and a second
// release is not a disagreement about state.
const OpUnforward = "unforward"

// EventChannelClosed is the notification a helper sends when a channel's
// REMOTE end is gone: the server closed it, the stream errored, or the
// connection died under it.
//
// It is a notification rather than an end-of-stream frame because a
// zero-length channel frame is a legitimate write of no bytes
// (channel_frame.go says why that distinction is load-bearing). Ordering is
// what makes it sufficient: TypeNotify rides the same wire as the data frames,
// so every byte the helper wrote before the close has already been written to
// it, and a reader that sees this event has seen all of them.
const EventChannelClosed = "channel-closed"

// EventForwardedTCPIP is the notification a helper sends when a connection
// arrives on a listener a `forward` op created: the far side accepted
// somebody, and the bytes of that connection are a channel like any other.
//
// It is an announcement rather than a result because nobody asked: the
// coordinator holds a listener, and the connection is the far side's act.
// Ordering is the same edge the channel plane relies on — a notification
// rides the same wire as the data frames — so the id is on the wire before
// the first byte of the stream it names is.
const EventForwardedTCPIP = "forwarded-tcpip"

// EventForwardClosed is the notification that a listener is gone: the
// coordinator asked (unforward), the far side's connection died under it, or
// the listener was closed by the server. The cause is carried, because a
// coordinator holding an Accept that will never return has to be able to say
// whether it stopped waiting because it asked to or because the transport did.
const EventForwardClosed = "forward-closed"

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

// SSHDestination is one RESOLVED destination a connection is acquired for:
// the address, the account, and what to authenticate as.
//
// Host, Port and User are resolved values, for the reason ProbeParams states at
// length — alias resolution, ~/.ssh/config merging and the credential's own
// authorization against an endpoint stay in the coordinator — and the fields
// are not folded into a string ("user@host:port") because a destination is
// three typed facts and a spelling of them would have to be parsed again on
// the far side by something that is not allowed to guess.
type SSHDestination struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	User string `json:"user"`
	// Identity is what to authenticate with and, for a key, the public half
	// the helper must declare before it is asked to sign. Required: a channel
	// with no credential is not a question this op can answer.
	Identity SSHIdentity `json:"identity"`
}

// ChannelKind is the closed set of proxied channels this generation opens.
//
// It is a KIND and not a command, and that is D3 rather than convenience: the
// helper refuses a free-form argv (host.Schema refuses a bare []string) because
// an argument list reaching a command line on somebody else's machine is the
// capability this whole level exists to not hand out. A kind names a protocol
// the helper itself implements; a caller that wants a new one adds a member
// here, in a generation, with a test — which is the point.
type ChannelKind string

const (
	// ChannelSFTP is a session channel carrying the `sftp` subsystem. The
	// helper opens it and moves bytes; it does not speak the SFTP protocol,
	// and pkg/sftp stays in the coordinator, where the file code that
	// consumes it already lives.
	ChannelSFTP ChannelKind = "sftp"
	// ChannelDirectTCPIP is a `direct-tcpip` channel to the Target the params
	// name: the far side connects to that address on ITS network and the
	// helper carries the bytes. It is the outbound half of a forward (-L, and
	// every SOCKS CONNECT), and the address resolves at the far end — which is
	// the whole point of both, since a target that only exists behind the
	// remote host is what a forward is for.
	ChannelDirectTCPIP ChannelKind = "direct-tcpip"
)

// ChannelTarget is one address a channel is opened to, TYPED.
//
// Host and Port are two fields and never one "host:port" string, for the
// reason SSHDestination is three: a spelling would have to be parsed again on
// the far side by something that is not allowed to guess, and the one caller
// that legitimately carries a name rather than an address — a SOCKS CONNECT —
// still carries a NAME and a PORT, not a sentence. An IPv6 literal therefore
// crosses without brackets and is joined there, rather than being assembled
// here in a form the far side would have to un-pick.
type ChannelTarget struct {
	// Host is the address or name to connect to, resolved on the FAR side.
	Host string `json:"host"`
	// Port is the port, 1-65535. It is never 0: a caller asking for an
	// ephemeral port on the far side has asked for something that is not a
	// forward (the forwarded LISTENER is the one that may request 0).
	Port int `json:"port"`
}

// OpenChannelParams asks the helper for one proxied channel.
type OpenChannelParams struct {
	Destination SSHDestination `json:"destination"`
	Kind        ChannelKind    `json:"kind"`
	// Target is where a direct-tcpip channel connects, on the far side's
	// network. Required for ChannelDirectTCPIP and absent for every other
	// kind, which the helper checks rather than tolerates: a target on an
	// sftp open is a caller that believes it is getting something else.
	//
	// It is a POINTER so that "absent" is a fact on the wire and not a
	// zero-valued struct: encoding/json's omitempty does not omit a struct,
	// and an sftp open would then carry {"target":{"host":"","port":0}} —
	// which the frozen schema for that op must either accept (as noise) or
	// reject, while a nil target is simply not there. The schema requires it
	// exactly when the kind does (ssh.open.params, if/then).
	Target *ChannelTarget `json:"target,omitempty"`
	// AcceptOnTrust is the same caller's decision ProbeParams carries, and it
	// is here for the same reason: a channel's handshake may meet a host key
	// nobody has recorded, and the helper may not decide that for itself.
	AcceptOnTrust bool `json:"acceptOnTrust"`
}

// OpenChannelResult names the stream the caller's bytes will be keyed by. The
// HELPER mints it — it is the end that owns the channel — and the coordinator
// echoes it back verbatim on every frame and on the close.
type OpenChannelResult struct {
	Channel ChannelID `json:"channel"`
}

// CloseChannelParams ends one proxied channel.
type CloseChannelParams struct {
	Channel ChannelID `json:"channel"`
}

// CloseChannelResult is the empty answer an idempotent close gives. It is a
// named struct rather than `any` so the wire shape is declared once and can be
// frozen; an empty JSON object is its whole content.
type CloseChannelResult struct{}

// ChannelClosedEvent is the notification payload for EventChannelClosed.
//
// Error is the helper's own sentence for a close it did not ask for, empty on
// an ordinary end. It is TEXT and not a refusal code, deliberately: this is
// reporting, not dispatch — the caller's next read is what it acts on, and
// giving a notification a code vocabulary would invite somebody to switch on
// it for control flow that belongs to the read path.
type ChannelClosedEvent struct {
	Channel ChannelID `json:"channel"`
	Error   string    `json:"error,omitempty"`
}

// ForwardID identifies one listener the helper holds for the life of the
// connection that asked for it. Like ChannelID it is minted by the HELPER —
// the end that owns the listener — echoed verbatim by the coordinator, and
// opaque in both directions.
//
// It is its own type rather than a second use of ChannelID because the two
// name different things and are ended by different ops: a channel is a stream
// somebody holds, a listener is the thing streams arrive on, and an id that
// could be either would make `close` and `unforward` interchangeable at the
// type level while meaning different things on the wire.
//
// The type and its codec live in forward_id.go: it has no data plane of its
// own (the connections it produces are ordinary channels), so it does not
// belong in the file that lays out a channel frame.

// ForwardParams asks for a listener on the far side.
//
// The destination is the same resolved triple `open` takes — the connection
// the listener is requested on — and the bind is the address to listen on
// THERE. Port 0 asks the server to allocate, which is the ordinary request:
// a fixed port is a policy question (PermitListen) and an allocation is not.
type ForwardParams struct {
	Destination SSHDestination `json:"destination"`
	// Bind is the requested listen address on the far side. The HOST is the
	// server's business as much as the client's — a non-loopback bind is
	// rebindable by policy (GatewayPorts) without a word to the client — so
	// it is reported back rather than promised, exactly as the coordinator's
	// own -R strategy already treats it.
	Bind ChannelTarget `json:"bind"`
	// AcceptOnTrust is the caller's host-key decision, carried for the reason
	// `open` carries it: this op may be the first contact with a host.
	AcceptOnTrust bool `json:"acceptOnTrust"`
}

// ForwardResult names the listener and the address the server bound.
//
// Bind is the transport's answer: the PORT is the one the server allocated (a
// requested 0 is resolved there and never reported as 0), and the HOST is
// whatever the reply carried — which for a hostname bind is an all-interfaces
// answer rather than proof of what was bound. The coordinator's own remote
// strategy has always disclosed exactly this; the wire does not improve on it.
type ForwardResult struct {
	Forward ForwardID     `json:"forward"`
	Bind    ChannelTarget `json:"bind"`
}

// UnforwardParams ends one listener.
type UnforwardParams struct {
	Forward ForwardID `json:"forward"`
}

// UnforwardResult is the empty answer an idempotent unforward gives.
type UnforwardResult struct{}

// ForwardedTCPIPEvent is the notification payload for EventForwardedTCPIP: a
// connection arrived on a listener, and this is the channel its bytes will be
// keyed by.
//
// Peer is the accepted connection's remote address as the FAR side reported
// it — a fact about somebody else's machine, passed through for the log and
// for the local end's RemoteAddr. It is not authenticated and nothing acts on
// it: the capability of a lifecycle candidate is what authenticates, and this
// is deliberately not a second, weaker way to.
type ForwardedTCPIPEvent struct {
	Forward ForwardID `json:"forward"`
	Channel ChannelID `json:"channel"`
	Peer    string    `json:"peer,omitempty"`
}

// ForwardClosedEvent is the notification payload for EventForwardClosed.
//
// Error is empty when the coordinator itself asked (its own `unforward`), and
// the helper's sentence when the listener ended for a reason nobody here
// chose — the far side's connection dying under it is the case a -R forward
// and the remote lifecycle channel both have to hear about, because both hold
// an Accept that would otherwise wait for ever.
type ForwardClosedEvent struct {
	Forward ForwardID `json:"forward"`
	Error   string    `json:"error,omitempty"`
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
	// ErrCodeChannelRefused means the far side would not give the helper the
	// channel it asked for: the server refused the session, or it refused the
	// subsystem request on the session it did open. ONE code for the two,
	// deliberately: a caller cannot act differently on them — either way this
	// host will not serve that channel — and the sentence the helper fails
	// with keeps the distinction for whoever reads the error.
	ErrCodeChannelRefused = "channel_refused"
)

// # The codes an OPEN ends in, and why they are the probe's own spellings
//
// A channel open has no result to carry an outcome — the channel was not
// opened, so the caller has a failure either way — yet the classes of that
// failure are exactly the ones a probe already reports: unreachable, rejected,
// needs-interactive, host-key-unknown, host-key-changed. So the refusal CODE
// is ProbeOutcome's own spelling, character for character, rather than a
// second enum naming the same states (AD-8). The coordinator already switches
// on that vocabulary — it is what it maps a helper's probe into — and a second
// set of names for one fact is the defect that mapping exists to avoid.
//
// The two host-key codes carry EVIDENCE in Details (HostKeyEvidence), because
// the coordinator rebuilds its own typed error from it: the accept sheet and
// the mismatch warning switch on ssh.ErrUnknownHostKey and ssh.ErrHostKeyMismatch,
// and a fingerprint that stays inside the helper is a sheet nobody can raise.

// HostKeyEvidence is what a refused channel's handshake knows about the key it
// refused: the same facts internal/ssh's own error types carry, in a shape that
// can cross a process boundary.
//
// It exists because the classification is preserved in ONE direction only. An
// error raised inside the helper cannot arrive as the coordinator's typed error
// — nothing carries Go values across this wire — so the coordinator rebuilds
// its own error from these fields, and every path that switched on the type
// (the accept sheet, the mismatch warning, the transport's hostKeyInfoFromError)
// goes on working unchanged. Without it those paths would see an opaque failure
// where they used to see evidence.
type HostKeyEvidence struct {
	// Addr is the address the handshake was against, and KnownHostsAddr is
	// the STORAGE identity the key was looked up under — the same value for a
	// direct route, and the field the accept path must write back.
	Addr           string `json:"addr"`
	KnownHostsAddr string `json:"knownHostsAddr"`
	Algorithm      string `json:"algorithm"`
	// Key is the offered public key's wire bytes, so the accept path can
	// record it without a second handshake.
	Key []byte `json:"key"`
	// Fingerprint is the offered key's SHA256 fingerprint, and Expected the
	// recorded one(s) — present exactly when the answer is `changed`, so a
	// changed key can never be rendered without the value it changed from.
	Fingerprint string `json:"fingerprint"`
	Expected    string `json:"expected,omitempty"`
}
