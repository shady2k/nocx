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

// OpLane is the forward op that opens one pty-less EXEC LANE on a pooled
// connection: the installed helper of a named GENERATION, run in its bridge
// subcommand, with the lane's bytes carried as an ordinary channel.
//
// It is what the git-over-a-remote-helper bridge rides once the coordinator
// holds no ssh client (plan §3's `ssh.lane(machine, generation)`), and the
// reason it is an op of its own rather than a `kind` of `open` is D3. The
// command a lane runs is the one thing a caller may never name: an argv
// reaching a command line on somebody else's machine is the capability this
// whole level exists to not hand out. So the caller names FACTS — which
// machine's install, which generation of it — and the helper turns them into
// the single command it is allowed to run (deploy.InstalledBinary +
// endpoint.BridgeInvocation), which is also why a lane carries an exit status
// where every other channel carries none.
//
// The remote helper's own ABI is unchanged by this: a lane ends at the bridge
// subcommand, exactly as the coordinator's own exec lane did, and the frame
// protocol on it is the one that was always there.
const OpLane = "lane"

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
	// OpPrompt asks the coordinator's UI for the answers a server's
	// keyboard-interactive challenge needs, and answers with them.
	//
	// It is the ONE reverse op whose subject is a PERSON rather than a stored
	// secret, and the separation is the point: `secret` returns material the
	// coordinator already holds, while this returns something nobody holds
	// until the question is asked. It carries the server's own prompts — their
	// text and whether the answer is echoed — because that is what makes the
	// ask answerable by somebody who is not looking at the far host (a
	// "Password:" and a "Verification code:" are different questions), and
	// because inventing a question of nocx's own would leave the person
	// answering something the server did not ask.
	//
	// A coordinator with no UI attached refuses it by name (ErrCodeNoAuthChannel
	// when no connection is there to ask, ErrCodePromptCancelled when a person
	// dismissed the question), and neither refusal is a fallback: the helper
	// has nothing to present and says so rather than guessing at an empty
	// answer, which a server would read as a wrong password.
	OpPrompt = "prompt"
	// OpPasswordPrompt asks the coordinator's OWN connection-password ask
	// for the one live person who is this connection's credential, when the
	// server's challenge is a bare `password` request rather than a
	// keyboard-interactive one (nocx-y6fh7 item 4, round 3).
	//
	// It is a DIFFERENT reverse op from OpPrompt, deliberately, rather than
	// a second spelling of the same relay: a password ask CORRELATES to the
	// connection it belongs to (Connection, ProfileID — both echoed back
	// from proto.SSHDestination, which the coordinator itself resolved and
	// therefore already knows), so it reaches the person through the SAME
	// requester a direct dial's own prompt rung uses — the "Password for
	// {profile}" dialog with its remember checkbox (ADR-0017) — one owner
	// for "ask this connection's password" whichever path asks. OpPrompt
	// stays the server's own keyboard-interactive questions, which travel
	// with no profile and have no remember concept: a verification code is
	// never a thing to bind to a connection.
	OpPasswordPrompt = "password-prompt"
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
	// SSHAuthInteractive answers the SERVER's own keyboard-interactive
	// prompts, and it is a kind of its own rather than a spelling of
	// SSHAuthPassword because the method is not the same one: `password`
	// carries no question and is answered by a callback that produces the
	// stored secret, while this one carries the far side's prompts — their
	// text and whether an answer is echoed — to a PERSON, through the
	// coordinator's own ask (proto.OpPrompt).
	//
	// It has no credential reference and no public key, and that absence is
	// the honest shape: there is nothing stored to name and nothing to offer
	// before the server speaks. The coordinator resolves this rung only when
	// the connection has a password requester wired (ssh.ConnectConfig
	// .PasswordRequester), which is exactly the boundary a PROBE crosses the
	// other way — a probe dials without one, so it cannot raise a prompt.
	SSHAuthInteractive SSHAuthKind = "interactive"
)

// SSHIdentity is what a dial authenticates with: a password's material, or the
// KEY QUEUE the helper must declare before it is asked to sign.
//
// Which field a kind uses is a decision rather than a convention — a password
// names material, a key names keys, and the interactive rung names neither —
// so `Credential` is absent for key auth and `Keys` is absent for the others,
// and the schema enforces both directions. That is what makes an identity
// self-describing without a reader having to know which fields "usually" go
// together.
type SSHIdentity struct {
	// Credential is the material a PASSWORD presents. It is a POINTER so that
	// "this identity carries no credential" is a fact on the wire rather than a
	// zero-valued struct: encoding/json's omitempty does not omit a struct, so
	// an interactive rung would otherwise carry
	// {"credential":{"ref":""}} — which the frozen schema must either accept as
	// noise (making "a password identity names material" unenforced) or reject,
	// while a nil credential is simply not there.
	Credential *SSHCredential `json:"credential,omitempty"`
	Auth       SSHAuthKind    `json:"auth"`
	// Keys is the queue a KEY identity offers, in the order the helper must
	// declare it: one entry for a credential the coordinator named, and several
	// where OpenSSH itself would offer several (an agent holding more than one
	// key, a home directory with more than one default key in it).
	//
	// It is a list rather than a single key because ssh's `publickey` method IS
	// a query per key — the helper declares each public half and the far side
	// answers whether it would accept a signature with it — so a connection
	// whose key is not the first one offered is an ordinary connection, not a
	// failed authentication. The private halves never cross: each entry carries
	// a reference to material that stays in the coordinator, and `sign` is what
	// turns a challenge into a signature.
	Keys []SSHKeyOffer `json:"keys,omitempty"`
}

// SSHKeyOffer is ONE key in an identity's queue: the public half the helper
// declares, and the reference it signs through when the far side asks.
//
// The two travel together because they are one fact at two moments of the same
// handshake — an entry whose reference did not belong to its public half would
// be a signature with a key nobody asked about, which is the shape a mismatch
// between them would take.
type SSHKeyOffer struct {
	// Credential names where the private half lives. Opaque to the helper, and
	// the same reference grammar every other credential on this wire uses
	// (`vault:`, `file:`, `agent:`) — the three places a key lives differ only
	// in what the coordinator does when this reference comes back on a `sign`.
	Credential SSHCredential `json:"credential"`
	// PublicKey is the wire-format public key blob (RFC 4253 §6.6) as base64 in
	// JSON. Public material: it is what the peer learns on the first packet.
	PublicKey []byte `json:"publicKey"`
}

// CredentialOf answers the credential this identity carries, as a value: the
// interactive rung carries none, and every caller that reads a field rather
// than a pointer reads it through here instead of testing for nil itself.
//
// For a KEY identity it is the FIRST key of the queue — the one the coordinator
// would offer first, and the only one a caller that needs a single principal
// (a session's launch record, for instance) can be given. The full queue is
// Keys, and a caller that needs to offer keys reads that.
func (id SSHIdentity) CredentialOf() SSHCredential {
	if id.Auth == SSHAuthKey {
		if len(id.Keys) == 0 {
			return SSHCredential{}
		}
		return id.Keys[0].Credential
	}
	if id.Credential == nil {
		return SSHCredential{}
	}
	return *id.Credential
}

// ProbeParams is one credential test against one host.
//
// It carries the SAME destination every other op takes (proto.SSHDestination)
// rather than its own copy of the address, the account and the credential, for
// the reason this whole package is one file per subject: a probe, a channel, a
// forward and a lane all ask the same question — who is this and how does one
// dial it — and four declarations of it would be four places for a hop or a
// storage identity to be forgotten. A route reached through jump hosts is
// therefore testable by the settings surface exactly as it is openable by a
// pane, which is the difference between "does this credential work" being
// answered about the host the person named and about a host they cannot reach.
type ProbeParams struct {
	// Destination is the resolved address, account, credential and route.
	Destination SSHDestination `json:"destination"`
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
	// Fingerprint is the offered host key's SHA256 fingerprint, present
	// whenever the handshake reached a key at all.
	//
	// It is here because the coordinator's own probe returned it beside its
	// error (ProbeConfigWithResult) and the settings surface STORES it — the
	// `host-key-unknown` outcome is first contact with a machine, and a probe
	// that could not say which key it saw would make the user's next
	// connections indistinguishable from each other (internal/transport's
	// ProbeResultIdentity.HostKeyFingerprint).
	Fingerprint string `json:"fingerprint,omitempty"`
	// HostKey is the evidence for the two host-key outcomes: the offered
	// key's wire bytes, its algorithm, and the recorded fingerprint when the
	// answer is `changed`.
	//
	// It is the SAME $def the channel plane's refusals carry (HostKeyEvidence)
	// — one shape for one fact — and it exists for the same reason: nothing
	// carries a Go value across this socket, so the coordinator REBUILDS
	// ssh.ErrUnknownHostKey / ssh.ErrHostKeyMismatch from it, which is what
	// keeps the accept sheet and the mismatch warning working unchanged.
	HostKey *HostKeyEvidence `json:"hostKey,omitempty"`
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

// PromptParams asks the coordinator's UI the questions a server's
// keyboard-interactive challenge carries.
//
// Host, Port and User are what the ask is ABOUT, and they are here so the
// question can name the connection it belongs to — the same discipline
// ssh.PasswordRequest applies on the coordinator's own prompt rung, where a
// bare "enter password" box is how the wrong password ends up in the wrong
// connection. They are the RESOLVED values the destination carried, never
// re-derived here.
type PromptParams struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	User string `json:"user"`
	// Prompts are the server's questions, in the order it asked them. An
	// empty list is refused rather than answered with nothing: a
	// keyboard-interactive challenge with no prompts is a protocol event this
	// helper has no question to relay for.
	Prompts []Prompt `json:"prompts"`
}

// Prompt is ONE question a server asked during a keyboard-interactive
// challenge: its own text, and whether the answer is echoed.
//
// Both fields are the SERVER's, passed through unchanged. Echo is load-bearing
// in a way that is easy to lose: a UI that does not know a question is a
// secret would print the answer on a screen, and one that assumes every
// question is a secret would hide a "Verification code:" the person needs to
// see as they type it.
type Prompt struct {
	Prompt string `json:"prompt"`
	Echo   bool   `json:"echo"`
}

// PromptResult carries the answers, positionally matched to PromptParams
// .Prompts. Position rather than a key or a label, because that is what
// keyboard-interactive IS: the protocol answers a challenge's list in order,
// and a mapping would be a second spelling of an order the protocol already
// fixes.
type PromptResult struct {
	Answers []string `json:"answers"`
}

// PasswordPromptParams asks the coordinator's own connection-password ask
// for the ONE live person who is this connection's credential, over the
// interactive rung's bare `password` method (nocx-y6fh7 item 4, round 3).
//
// Host, Port and User are what the ask is about, exactly as PromptParams
// carries them for the same reason. Connection and ProfileID are what makes
// this op different from PromptParams: they correlate the ask to the
// connection it belongs to, so the coordinator answers through the SAME
// requester a direct dial's own prompt rung uses rather than a bare "enter
// password" box with nothing to name.
type PasswordPromptParams struct {
	// Connection is the saved profile's display name, echoed from
	// SSHDestination.ConnectionName. Empty for a destination with no saved
	// profile.
	Connection string `json:"connection,omitempty"`
	// ProfileID is the saved profile's id, echoed from
	// SSHDestination.ProfileID. Empty for a destination with no saved
	// profile — the ask then falls back to the bare wire requester, which
	// has no profile to bind a remember to.
	ProfileID string `json:"profileId,omitempty"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	User      string `json:"user"`
}

// PasswordPromptResult carries the password a person typed.
type PasswordPromptResult struct {
	Password string `json:"password"`
}

// VerifyHostKeyParams asks what to make of a host key the helper was just
// offered during a handshake.
type VerifyHostKeyParams struct {
	// Host is the address the key was OFFERED for: the address this helper
	// dialed. It is what the resulting error and its evidence name, and it is
	// what a person is shown.
	Host string `json:"host"`
	// KnownHostsAddr is the address the coordinator looks the key up under —
	// its STORAGE identity, which is the offered address for a direct route
	// and a route digest for a destination reached through a jump host
	// (ssh_real.go's knownHostsTargetAddr). It is the coordinator's value and
	// not something the helper derives: the helper echoes what the
	// destination carried (proto.SSHDestination.KnownHostsAddr), and a helper
	// that guessed the digest would look for a line the coordinator's next
	// connection would not write.
	//
	// Empty means the offered address, which is the direct route's answer.
	KnownHostsAddr string `json:"knownHostsAddr,omitempty"`
	Algorithm      string `json:"algorithm"`
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
	// Jumps are the hosts this destination is reached THROUGH, in dial order:
	// Jumps[0] is dialed from this machine, Jumps[1] through Jumps[0], and the
	// destination through the last of them. Each hop is a destination in its
	// own right — its own address, its own account, its own credential
	// reference and its own host key to verify — because a route is a chain of
	// independent handshakes and not one connection with a longer address.
	//
	// It is a flat, ordered list rather than a nesting of destinations, and
	// that is a decision rather than a simplification: a hop that carried its
	// own route would be a second way to say the same thing, and the two would
	// have to agree about traversal order. The coordinator flattens its own
	// recursive jump chain into this list; the helper walks it from the front.
	Jumps []SSHHop `json:"jumps,omitempty"`
	// KnownHostsAddr is the address this destination's host key is STORED
	// under — the coordinator's own storage identity for it, which is not the
	// dial address when the destination is reached through a jump host
	// (ssh.RealClient's route record: a key accepted through one bastion must
	// never vouch for the same host dialed directly, or for the same host
	// through a different one).
	//
	// It travels WITH the destination because the derivation is the
	// coordinator's: known_hosts is its file, the route hash is its rule, and a
	// helper that re-derived either would be a second answer to "which machine
	// is this" (AD-8). Empty means the dial address is the storage identity,
	// which is the direct route's answer and the only one a hop can have.
	KnownHostsAddr string `json:"knownHostsAddr,omitempty"`
	// ConnectionName is the saved profile's display name, and ProfileID its
	// id — both the COORDINATOR's own values, echoed back unchanged on
	// OpPasswordPrompt so a person-asked password correlates to the
	// connection it belongs to without a second lookup (nocx-y6fh7 item 4,
	// round 3: the coordinator that resolved this destination is the same
	// one that will answer the ask, so it puts what it already knows on the
	// request rather than asking the helper to look anything up). Both
	// empty for a destination with no saved profile — a jump hop, or a
	// direct host this connection names inline — which is a real state and
	// not a gap: such an ask falls back to the bare wire prompt with no
	// profile to bind a remember to.
	ConnectionName string `json:"connectionName,omitempty"`
	ProfileID      string `json:"profileId,omitempty"`
}

// SSHHop is one INTERMEDIATE host a destination is reached through: a
// destination in every respect except that it carries no route of its own,
// because a route is the flat list above and a hop inside it cannot be
// re-routed without a second way to say what order to walk.
//
// Its fields are the destination's, spelled once more rather than shared by
// embedding, for a reason the schema can enforce: a hop must not be able to
// carry `jumps` at all, and `additionalProperties: false` says that — while a
// shared declaration would let a document nest a route inside a route and
// leave the helper deciding which one it meant.
type SSHHop struct {
	Host     string      `json:"host"`
	Port     int         `json:"port"`
	User     string      `json:"user"`
	Identity SSHIdentity `json:"identity"`
	// KnownHostsAddr is the hop's own storage identity, resolved exactly as
	// the destination's is (a hop that is itself reached through a later hop
	// has a route-hashed one). Empty means the hop's dial address, which is
	// the ordinary case.
	KnownHostsAddr string `json:"knownHostsAddr,omitempty"`
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

// Machine is the identity of the installed helper a lane runs, and it is the
// "machine" half of a lane's name.
//
// It is the install DIRECTORY and nothing else, and that is the narrowest
// honest shape rather than a path somebody let through. A helper install is
// content-addressed and immutable (D7): deploy.Ensure writes
// <home>/.nocx/helper/<version>-<goos>-<goarch>-<hash>/nocx-helper, the
// coordinator records that directory as the install's own identity
// (consent.Install.Path, and the route a reopened session already carries), and
// what a lane needs is exactly which of those to run.
//
// The alternative — home and platform, so the helper re-derives the path — was
// rejected for a concrete reason rather than for taste: a session re-adopted
// after a coordinator restart has the recorded DIRECTORY and no home or
// platform to speak of, and recovering them would be a second derivation of the
// install layout, which is the regression AD-8 names. The directory is the fact
// both paths already hold.
//
// What a caller still cannot do is name a COMMAND: the helper appends the
// binary's name and the bridge subcommand itself, from its own install layout,
// so the only thing a lane can be pointed at is a directory holding a helper.
type Machine struct {
	// Dir is the absolute install directory the coordinator installed into —
	// the directory deploy.Ensure keys by generation and writes the
	// .install-complete marker in.
	Dir string `json:"dir"`
}

// LaneParams asks this machine's helper for one exec lane to a remote helper's
// bridge: WHICH destination (resolved exactly as every other op's), WHICH
// installed helper of it (Machine), and WHICH build (the generation).
//
// There is no command and no argv in it, and that absence is the op's point
// rather than a tidy omission — see OpLane.
type LaneParams struct {
	Destination SSHDestination `json:"destination"`
	Machine     Machine        `json:"machine"`
	// Generation is the content hash the installer wrote (D7, D21): the
	// install is content-addressed and the generation IS the build, so naming
	// it is what stops a lane from reaching a DIFFERENT generation's sessions
	// and what lets two generations coexist on one host while an old one still
	// holds somebody's shell (D4).
	Generation GenerationID `json:"generation"`
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
	// Exit is the status the far end's PROCESS exited with, present exactly
	// when the channel WAS one: a lane (OpLane) whose remote helper ended.
	//
	// It crosses because the coordinator's own exec lane read it, and what it
	// classifies is not a detail — a bridge that ends before the handshake's
	// sentinel is read by its exit status, where `exitNoEndpoint` means "no
	// helper is serving that generation" and any other code means "the host
	// did not answer with our helper" (internal/helper/client's pump). Losing
	// it would collapse two facts a person acts on differently into one.
	//
	// A POINTER so absence is a fact rather than a zero: an sftp subsystem and
	// a direct-tcpip connection have no exit status at all, and `0` is a
	// perfectly ordinary way for a process to end.
	Exit *int32 `json:"exit,omitempty"`
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

// OpToolSocket asks the far side for a TOOL SOCKET: a unix socket bound at a
// path the caller names, on the connection the destination names.
//
// # Why it is its own op, and why the far side is a SOCKET and not a channel
//
// Every other listener on this wire hands its accepted connections to the
// COORDINATOR as channels (`forward`, and the announcements that carry them).
// This one does not, and the difference is the whole reason it exists: what
// arrives on a pane's tool socket is an AGENT, and the endpoint that admits an
// agent must be told WHICH PANE the connection belongs to before a single byte
// of it is read — a record only the party holding the listener can write
// (internal/toolendpoint's panebind, whose reader refuses a connection without
// one). A helper that announced the connection instead would hand the
// coordinator a stream with no pane on it.
//
// So the op takes the two ends AND the pane: `path` is bound on the far host
// by its own sshd (a streamlocal forward — the same request the coordinator's
// own -R would make, which is why a path that must not exist yet is the
// caller's business), `target` is the socket on THIS machine each accepted
// connection is piped into, and `session` is what is written first.
//
// It answers the same ForwardID `forward` answers, and it is ended by the same
// `unforward`: a tool socket is a listener, and one op that ends listeners is
// what keeps "which id is ended by which call" answerable at the type level.
//
// The reason the far side is a PATH rather than the loopback port `forward`
// binds is D12's: a loopback port on a machine anybody can log into is
// reachable by every account on it, while a unix socket is bound with the login
// account's own permissions — the same boundary the local endpoint is (0700
// directory, 0600 socket).
const OpToolSocket = "tool-socket"

// ToolSocketParams is one pane's far-side tool socket, as the caller resolves
// it.
//
// The two ends are the CALLER's, and the helper may not invent either: it
// cannot read the far host's filesystem, so a far path it chose would be a
// guess about somebody else's machine, and it does not hold the coordinator's
// endpoint either — that is a path on this machine which the pane's owner
// names (nocx-50w7p.18's rule, one op over). What the helper owns is the
// listener, the record written on every connection through it, and the pump.
type ToolSocketParams struct {
	Destination   SSHDestination `json:"destination"`
	AcceptOnTrust bool           `json:"acceptOnTrust"`
	// HostKeyFingerprint is the caller's statement about the key it expects,
	// carried for the reason `forward` and `open` carry it: this op may be the
	// first contact with a host.
	HostKeyFingerprint string `json:"hostKeyFingerprint,omitempty"`
	// Path is the socket path ON THE FAR HOST, as that host will see it. It
	// must not exist when the request arrives — OpenSSH refuses to bind over
	// an existing name rather than replacing it — and the directory holding it
	// must already be there, because neither sshd nor this helper creates one.
	Path string `json:"path"`
	// Target is the socket on THIS machine every accepted connection is piped
	// into: the tool endpoint of the coordinator that asked for the pane.
	Target string `json:"target"`
	// Session is the coordinator's session id for the pane these connections
	// belong to, and it is written before any far byte (panebind). Required:
	// a tool socket whose connections name no pane is a socket the coordinator
	// can only refuse, and it is refused by name before anything is dialed.
	Session string `json:"session"`
}

// ToolSocketResult names the listener, so the caller can end it with the same
// `unforward` every other listener is ended by. Path is echoed back rather than
// derived there: the caller named it, and the answer says what was actually
// asked for rather than what somebody re-derived.
type ToolSocketResult struct {
	Forward ForwardID `json:"forward"`
	Path    string    `json:"path"`
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
	// ErrCodePromptCancelled means the coordinator asked a person and the
	// person dismissed the question. It is its own code rather than a
	// `needs_interactive` because the two are different states of the world:
	// `needs_interactive` says the material exists and nobody can supply it,
	// while this says somebody was asked and declined — which is a decision,
	// and one a caller must not answer by retrying.
	ErrCodePromptCancelled = "prompt_cancelled"
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
