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
const Version = "4"
