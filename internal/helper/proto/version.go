package proto

// Version is the protocol version the helper and the backend must agree on.
// A hello carrying another version is refused with exit code 42 before the
// helper writes anything to stdout (D5).
//
// It covers the whole wire contract, not the envelope alone: the frame types,
// the services, their ops, and the shape of every result. A reader that
// switches on any of those is reasoning about this number.
//
// # When it must be bumped (nocx-k6p18.11)
//
// Until the first generation is PUBLISHED, this number names what the
// protocol will turn out to be, and additions are part of its content rather
// than changes to it. nocx-k6p18.1 added a frame type and nocx-k6p18.3 added
// a whole service with three ops without bumping, and both said so: bumping
// would have refused every dev helper for no gain, because nothing was
// deployed to gain it against.
//
// From the first publish that argument is spent, and the rule is the strict
// one: ANY change to a frame type, a service, an op or a result shape gets a
// bump. Installs are content-addressed and immutable, two generations are
// resident at once, and one lingers as long as it holds a session — months.
// So a published number is a fact other binaries reason about, and anything
// added under it silently redefines what they think they agreed to.
//
// The bump is also what lets two protocol versions coexist on one host:
// internal/helper/deploy keys the install directory by this value (D7), so a
// new version installs beside the old rather than over it, and a generation
// still holding a session keeps the binary it was started from.
//
// This is 2 rather than 1 because the wire grew a frame type and a service
// after 1 was written, and the deploy work is where a published number starts
// to bind (nocx-6ojko).
//
// This is 3 rather than 2 because the wire grew the `ssh` service — a forward
// probe op, four reverse ops the helper asks the coordinator, and the three
// refusals a helper's dial can end in (no_auth_channel, vault_sealed,
// needs_interactive) — together with the frame direction they need: reverse
// requests, which a generation speaking 2 would answer as an unexpected frame
// and drop, hanging the helper that asked (nocx-50w7p.2). Two peers that
// disagree about this number refuse each other at hello in both directions,
// which is what makes the bump the whole of the compatibility story.
// This is 4 rather than 3 because the wire grew the PROXIED-CHANNEL plane:
// the `open` and `close` ops on the same `ssh` service, and the
// TypeChannelData frame type with the ChannelID identity its bytes are keyed
// by (nocx-50w7p.3). The frame type is the half that makes the bump
// unavoidable rather than tidy — the decoder treats an unknown type byte as
// garbage and rescans one byte at a time, so a generation speaking 3 would
// resync THROUGH a live sftp stream instead of dropping one frame — and the
// two ops are the half that makes it honest: a helper that cannot open a
// channel answers `unknown_op`, which a coordinator would have to read as
// "this machine's helper is older than this app" (nocx-50w7p.2's rationale,
// one generation on).
//
// This is 5 rather than 4 because the wire grew the FORWARD plane: the
// `direct-tcpip` channel kind (with the `target` a direct channel needs),
// the `forward` and `unforward` ops for a listener on the far side, and the
// two notifications that carry a forwarded connection and the end of the
// listener it arrived on (nocx-50w7p.8). One op added would have been
// answerable as `unknown_op`, which is a sentence a coordinator can act on;
// the `target` FIELD on `open` is not — an older helper builds its params
// schema from the same struct this one does, accepts the field, and ignores
// it, so a direct-tcpip open would be answered as a subsystem one and the
// caller would have a channel to the wrong thing. That is the half that makes
// this a bump rather than an addition, and the two new ops are the half that
// makes it honest.
//
// This is 6 rather than 5 because the wire grew the SSH PANE: the `spawn-ssh`
// op, and with it a launch record that is a discriminated union — a session
// whose process is a shell channel on a connection the helper dialed carries
// no pid, no pgid and no cwd, because this machine has no such facts about it
// (nocx-50w7p.4). The launch record is the half that makes the bump
// unavoidable rather than tidy: `sessionEntry` is `additionalProperties:
// false` on both sides, so a generation speaking 4 REJECTS an entry carrying
// `kind`, and one speaking 5 answers a `spawn` with a shape a 4-reader cannot
// read — the two cannot be told apart by anything less than the number. The op
// is the half that makes it honest: a 4-helper answers `unknown_op`, which a
// coordinator reads as "this machine's helper is older than this app"
// (nocx-50w7p.2's rationale, two generations on).
//
// This is 7 rather than 6 because the wire grew the NAMED PROBES and the lease
// they run on: `lease`, `unlease`, `uname`, `home`, `sample-ports`,
// `completion` and `command-names` on the same `ssh` service (nocx-50w7p.9).
// Seven ops added would each have been answerable as `unknown_op`, and the
// bump is still taken because of what the SEVEN MEAN: they are the ops by which
// every shell command the coordinator used to compose is now composed by the
// helper from internal/remoteprobe. A coordinator speaking 6 has no ops to ask
// these questions with and would fall back to its own dial — which is the state
// the owner's invariant exists to end — and a helper speaking 6 answers
// `unknown_op` to every one of them, which a coordinator reads correctly as
// "this machine's helper is older than this app" rather than as a host that
// refuses probes. The generation boundary is what makes that reading safe
// instead of a guess.
// This is 8 rather than 7 because the wire grew the EXEC LANE: the `lane` op,
// which is how the git-over-a-remote-helper bridge rides a connection the
// helper dialed (nocx-50w7p.10), and two shape changes that ride with it. The
// op is the half that degrades: a 7-helper answers `unknown_op`, which the
// coordinator reads as "this machine's helper is older than this app" — the
// same sentence, one generation on. The shapes are the half that does not: a
// lane's far end is a PROCESS, so `ssh.channel-closed` grew the `exit` field
// the coordinator's own exec lane read to tell "no helper is serving that
// generation" from "the host did not answer with our helper", and
// `ssh.probe`'s result grew the fingerprint and the host-key evidence the
// settings surface stores and the accept sheet renders — neither of which a
// 7-reader would reject politely, since `additionalProperties: false` means it
// accepts only what it was built with. Two peers that disagree refuse each
// other at hello in both directions, which is what makes the number the whole
// of the compatibility story.
//
// This is 9 rather than 8 because the wire grew the ROUTE and the PROMPT: a
// destination may now name the hosts it is reached through (`jumps`, each with
// its own credential reference and its own storage identity) and the address a
// host key is STORED under (`knownHostsAddr`), `authKind` grew the
// `interactive` member whose questions a helper relays to a person, and the
// `prompt` reverse op is how it asks (nocx-50w7p.11). The shapes are the half
// that makes this a bump rather than an addition: `additionalProperties: false`
// means an 8-helper REJECTS a destination carrying `jumps` — a jump-routed
// connection would not degrade into a direct dial, it would not dial at all,
// and a person would see a protocol failure where they asked for a host — and
// an 8-helper reads `auth: "interactive"` as a kind it does not know and
// refuses it, which is the right answer arriving one generation early. The op
// is the half that makes it honest: an 8-helper answers `unknown_op` to
// `prompt`, which a coordinator reads as "this machine's helper is older than
// this app". Two peers that disagree refuse each other at hello in both
// directions, which is what makes the number the whole of the compatibility
// story.
//
// This is 10 rather than 9 because the wire grew a FIELD on two frozen ops, and
// a field is the half that cannot be added: `session.spawn.params` and
// `session.spawn-ssh.params` gained `agentToolEndpoint` — the caller's own
// tool endpoint on this machine, which the pane's tool connections belong to
// (nocx-50w7p.18). It replaces a value the daemon read once from the
// environment of whichever coordinator started it, which was wrong the moment
// a second coordinator rode the same generation (D12): the daemon is keyed by
// generation, its callers are not, and a pane's tool connections belong to the
// coordinator that OPENED the pane.
//
// The reason is the freeze's own rule and not tidiness. Both shapes are
// `additionalProperties: false`, so a 9-helper builds its params schema from
// the same struct this one does, REJECTS the payload carrying the field, and
// answers `bad_params` — a sentence about the request, which is a lie: the
// request is well-formed and the helper is old. Bumping makes the two peers
// refuse each other at hello in both directions, which is the honest reading,
// and installs the new generation beside the old one (D7) so a session still
// holding the old binary keeps it.
//
// This is 11 rather than 10 because the wire grew the KEY QUEUE: `ssh.identity`
// no longer carries one `credential` plus one `publicKey` for key auth, and
// carries an ordered `keys` list instead, each entry a public half beside the
// reference the coordinator signs through (nocx-50w7p.19). The SHAPE is the
// half that makes this a bump rather than an addition: `additionalProperties:
// false` means a 10-helper REJECTS an identity carrying `keys` — a key-auth dial
// would not degrade into a single-key one, it would not dial at all, and the two
// ordinary setups the change is for (a profile naming no credential, an agent
// holding several keys) would be refusals arriving as protocol failures. The
// password half is unchanged in meaning, so an 11-helper reading a password
// identity behaves exactly as before; what no generation can do is read the
// other's key auth, and only the number tells them apart. Two peers that
// disagree refuse each other at hello in both directions, which is what makes
// the number the whole of the compatibility story.
const Version = "11"
