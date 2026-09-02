package proto

// The FROZEN half of the helper protocol: the identities a host session is
// addressed by, and the envelopes that attach a reader to one, advance its
// cursor and let go of it again.
//
// # Why this is frozen and not merely designed
//
// A helper install is content-addressed and immutable, two generations are
// resident at once, and a generation lingers for exactly as long as it holds a
// session — months, in the case this whole level exists for. So a generation
// whose ABI assumes a single unnamed client can never later be made to serve
// two observers correctly, and an opaque identifier can never later become
// authorization. Everything below is decided now, deliberately, rather than
// discovered after deployment (level-1 design D8).
//
// # Three types, and no call conflates them (D2)
//
//	HostSessionID   the PTY and its process group, minted by the helper and
//	                qualified by the generation that minted it. It SURVIVES
//	                coordinator replacement, and it is what the ledger's
//	                entries.session_id references.
//	AttachmentID    one coordinator↔helper connection and its lease.
//	                Disposable, and it appears NOWHERE in the ledger.
//	StreamOffset    the monotonic output coordinate of a host session. It
//	                survives attachments; a new reader does not restart it.
//
// A new attachment is a new READER of the same stream, never a new stream.
// The correction that produced these three is worth keeping: before it, "the
// session" meant the coordinator-owned PTY channel, so the channel's death was
// the session's death — which is what made a replacing coordinator delete live
// work. They are defined types rather than three spellings of `string`
// precisely so that conflation is a compile error somewhere, rather than a
// wrong answer everywhere.
//
// # How a reader resumes: extended, not minted
//
// AD-9 already owns "how a reader resumes a stream", and it owns it in two
// places that agree: internal/transport's replay ring (offsets, acks, replay,
// reset-to-base) and the `attach`/`ack` control methods the renderer speaks
// (contracts/attach.schema.json's resumed/reset/from, contracts/ack.params).
// This ABI speaks that vocabulary unchanged and adds only what D8 requires and
// AD-9 has no need for, because the coordinator's ring has exactly one reader:
//
//   - a SubscriberID on attach, on data and on write, so several readers of
//     one stream are a wire fact rather than an implementation question;
//   - a LeaseEpoch, because exactly one of those readers may also write;
//   - `fresh` as an explicit flag, never inferred from the offset;
//   - a gap alongside a reset, because the helper's window is capacity-
//     reclaimed (D8 of the execution-host design) and a reset is now a fact
//     about the stream rather than only a fact about a slow client.
//
// The DECISION rule — is the offset still in the window, and if not where does
// the reader restart — was deliberately NOT written here, so that it would land
// with the window it decides for. It landed with nocx-k6p18.3 as ResumeAt in
// session_service.go, beside the Resume shape that states its answer, and
// internal/transport's outputRing.snapshot now takes its VERDICT from there
// rather than keeping a second derivation. What that ring keeps is only where a
// reset restarts, because the two windows lose bytes for different reasons —
// the comment on ResumeAt says which.
//
// # The trust boundary, and that it cannot be retrofitted (D12)
//
// Any nocx running under that Unix account may connect to this helper.
// Same-UID trust; NO session capability is reserved, and the consequence is
// stated rather than left to be discovered: any process running as you on that
// machine can attach to your sessions and write to them. On a machine you own
// that is the same bar as your ssh keys, your shell history and your files.
//
// This is the one decision here that is irreversible in the strongest sense. A
// generation deployed without a capability accepts any same-UID peer for the
// whole of its life, whatever a later document decides, so if independent
// same-UID nocx servers must ever be isolated from one another the capability
// is owed BEFORE the next generation ships — never afterwards. The owner
// decided on 2026-08-31 that they need not be. TestNoEnvelopeReservesACapability
// is that decision made enforceable: reversing it starts by changing that test.
//
// # Reserved here, unused here, and required to stay (D15)
//
// An opaque WorkspaceID belonged in the inventory and spawn envelopes when
// those landed, and it is in both (session_service.go). It was named here first
// so a later optimisation would not quietly drop it: it is opaque and never a
// display name, because human names bring rename, collision, normalisation and
// guessability into execution-host policy, and the helper owns no
// human-authored name (D3).

// ServiceSession is the name of the helper service that owns PTYs. The name was
// RESERVED here while its service did not exist (D15) and internal/helper/host
// refused to register anything under it; nocx-k6p18.3 cashed that reservation
// in, and internal/helper/session is what answers to it now. This constant is
// still where the name is spelled, so it has one owner rather than a literal in
// each package that cares — and host.Register still refuses a SECOND service
// claiming any name, which is what the reservation was protecting.
const ServiceSession = "session"

// The operations of the session service. Frozen with the envelopes below; an
// op renamed later is an op an older generation does not answer.
const (
	// OpAttach makes one subscriber a reader of one host session.
	OpAttach = "attach"
	// OpAck advances that subscriber's read cursor.
	OpAck = "ack"
	// OpDetach drops one attachment. The process survives it, and the
	// session stays in the inventory and reattachable (D9) — detach is not
	// close-session, and neither is closing a tab.
	OpDetach = "detach"
)

// GenerationID is one content-addressed helper install. It qualifies every
// host session that install minted, so a durable handle addresses its
// generation rather than needing a lookup service (D10).
type GenerationID string

// HostSessionID is the durable handle of one PTY and its process group. It is
// a struct rather than a string because the qualification is load-bearing:
// there is no way to spell a session without the generation that minted it,
// and the generation's endpoint is derived from it.
type HostSessionID struct {
	Generation GenerationID `json:"generation"`
	// Session is 32 lowercase hex characters — 16 raw bytes, the same width
	// and the same spelling the coordinator's own session id already uses, so
	// the data frame's header below can carry it raw exactly as
	// internal/transport's does.
	Session string `json:"session"`
}

// AttachmentID names one coordinator↔helper connection and its lease. It is
// disposable: it dies with the connection, it is what `detach` names, and it
// may never reach the ledger — a row keyed by it would tie durable work to a
// pipe (D2).
type AttachmentID string

// SubscriberID names a reader of a host session's output. Several may read one
// stream at once, each on its own cursor, and one of them may hold the write
// capability. It is opaque to the helper and confers nothing: it addresses, it
// does not authorize (D12).
type SubscriberID string

// StreamOffset is the monotonic output coordinate of a host session, in bytes,
// counted from the first byte the session ever produced. It is 64-bit
// deliberately: 32 bits wrap after 4 GiB, which one long build reaches.
type StreamOffset uint64

// LeaseEpoch numbers the grants of a session's one write capability. It is
// minted from 1, so zero names no grant, and it rises on every grant so a
// frame written by a displaced holder is rejected rather than applied late.
type LeaseEpoch uint64

// AttachParams makes Subscriber a reader of Session from Offset.
type AttachParams struct {
	Subscriber SubscriberID  `json:"subscriber"`
	Session    HostSessionID `json:"session"`
	// Offset is where this subscriber resumes. A reconnect names the offset
	// it last received; a reader with no position of its own names the base
	// the inventory reported, exactly as a fresh renderer attaches at
	// sessions.live's replayFrom today. It is never inferred from Fresh.
	Offset StreamOffset `json:"offset"`
	// Fresh says the caller has no render state, and it is a flag rather
	// than an inference because only the caller knows: a fresh renderer can
	// attach at a non-zero offset, and a renderer can hold an offset after
	// losing its screen. It has no omitempty for the same reason — `false`
	// and `absent` must not be the same bytes.
	Fresh bool `json:"fresh"`
	// LifecycleOffset and LifecycleFresh describe the separate raw lifecycle
	// stream carried by this attachment.
	LifecycleOffset StreamOffset `json:"lifecycleOffset"`
	LifecycleFresh  bool         `json:"lifecycleFresh"`
	// RequestWrite asks for the session's one write capability. A second
	// request is refused and names the holder (see WriteGrant); it is never
	// silently promoted, and it never displaces — displacement is a product
	// decision the coordinator already makes for its own clients.
	RequestWrite bool `json:"requestWrite"`
}

// AttachResult is the attachment, where both readers stand, and whether it
// may write. The two Resume fields are intentionally the same vocabulary for
// the two independent bounded streams.
type AttachResult struct {
	Attachment      AttachmentID `json:"attachment"`
	Resume          Resume       `json:"resume"`
	LifecycleResume Resume       `json:"lifecycleResume"`
	Write           WriteGrant   `json:"write"`
}

// Resume says where a reader stands in the stream. It is the coordinator's own
// attach vocabulary (contracts/attach.schema.json), unchanged: both booleans
// are always present rather than one being omitted, because the contract is
// exact only when a reader can tell "the helper said no reset" from "the
// helper did not mention reset", and exactly one of them is true.
//
// One shape, two carriers: an attach answers with it, and so does the
// mid-stream SessionReset notification. A reader must decode "where am I and
// what did I lose" the same way whether it learned it at attach or while
// attached, and two shapes would be two decoders that eventually disagree.
type Resume struct {
	// Resumed means the requested offset was still in the window and the
	// stream continues from it: nothing was lost.
	Resumed bool `json:"resumed"`
	// Reset means the requested offset is older than the window's base. The
	// reader must clear its decoder and its screen and resync from From —
	// replay cannot begin inside a UTF-8 sequence spliced onto a different
	// stream position.
	Reset bool `json:"reset"`
	// From is the offset the stream resumes at: the requested offset when
	// resumed, the window's base when reset.
	From StreamOffset `json:"from"`
	// Gap is what was lost, present exactly when Reset and absent otherwise.
	Gap *Gap `json:"gap,omitempty"`
}

// Gap is one range of output this reader will never receive, in the ledger's
// own Gap shape — "what is missing" is said the same way here as on an
// artifact and in a recording.
type Gap struct {
	Start StreamOffset `json:"start"`
	End   StreamOffset `json:"end"`
	// Reason is why. It is a second value and not `cap` deliberately: telling
	// a person the cap dropped bytes nobody ever had is a false statement in
	// the product. A gap is a caller defect when the caller had the bytes,
	// and a fact about the stream when nobody did — and only the second kind
	// can come from here. See GapReasonWindow.
	Reason string `json:"reason"`
}

// GapReasonWindow is the only reason this generation sends: the helper's
// bounded output window was reclaimed past this range while the reader was
// behind it, so nobody ever held these bytes. It is distinct from the
// recording's own `cap`, which names bytes the recorder had and dropped.
//
// A coordinator meeting a reason it does not recognise must treat it as an
// unqualified loss and must not refuse the frame: generations coexist, and a
// newer one may name a loss this one cannot.
const GapReasonWindow = "window"

// WriteGrant is the answer to RequestWrite. Exactly one attachment holds a
// session's write capability at a time.
type WriteGrant struct {
	// Granted is whether this attachment now holds it.
	Granted bool `json:"granted"`
	// Epoch is the lease this grant carries, minted from 1. Every input
	// frame carries it, and a frame carrying a stale one is rejected rather
	// than applied — which is what makes "exactly one writer" survive a
	// carrier that delivers a displaced holder's bytes late. Zero when not
	// granted.
	Epoch LeaseEpoch `json:"epoch"`
	// Holder names who has it when this request was refused, and is nil
	// otherwise. A refusal that does not say who holds it leaves the caller
	// with nothing to do about it.
	Holder *SubscriberID `json:"holder"`
}

// AckParams advances one subscriber's read cursor. It is keyed by subscriber
// and session, NOT by attachment: the cursor is the reader's and outlives the
// connection that carried it (D2), so a reconnect resumes rather than
// restarting.
type AckParams struct {
	Subscriber SubscriberID  `json:"subscriber"`
	Session    HostSessionID `json:"session"`
	Offset     StreamOffset  `json:"offset"`
	// LifecycleOffset, when present, advances the separate lifecycle reader.
	// A pointer keeps conventional clients' existing acknowledgement shape
	// unchanged while allowing zero as a legitimate initial offset.
	LifecycleOffset *StreamOffset `json:"lifecycleOffset,omitempty"`
}

// DetachParams drops one attachment (D9). The process survives; the session
// stays in the inventory and is reattachable. In level 1 closing a tab is this
// verb, not close-session.
type DetachParams struct {
	Attachment AttachmentID `json:"attachment"`
}

// DetachResult reports whether this attachment was holding the session's write
// capability when it went, because that is the fact the next caller acts on: a
// replacing coordinator can take the capability without arbitration precisely
// when the previous holder's connection released it.
type DetachResult struct {
	ReleasedWrite bool `json:"releasedWrite"`
}

// SessionReset is the LIVE reset: an attached reader whose cursor fell behind
// the window's base is told so explicitly, mid-stream, and resumes at the
// base. Without it a slow reader would simply go quiet, which is the silent
// degrade the product must never show — the loss is stated, never only logged.
//
// It rides as a TypeNotify frame on the same wire as the data frames, so it is
// ordered with respect to them: the reader sees exactly which bytes the hole
// sits between.
type SessionReset struct {
	Subscriber SubscriberID  `json:"subscriber"`
	Session    HostSessionID `json:"session"`
	Resume     Resume        `json:"resume"`
	// Stream distinguishes the independent bounded carriers. Empty means the
	// original PTY output stream for compatibility with older helpers.
	Stream string `json:"stream,omitempty"`
}
