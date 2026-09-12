package sessionruntime

import "errors"

// ---------------------------------------------------------------------------
// 1. Session incarnation
// ---------------------------------------------------------------------------

// SessionID names a session for as long as a person would call it the same
// terminal. It is stable across a coordinator restart, which is the whole point
// of the runtime living beside the PTY (nocx-ygxjv.3).
type SessionID string

// Generation counts the PTYs that SessionID has had. It rises when a new
// process is started for the session and never otherwise; a coordinator dying
// and coming back does NOT advance it, because nothing about the terminal
// changed. Minted from 1, so the zero Incarnation names nothing.
type Generation uint64

// Incarnation is the identity every event is judged against. Evidence naming a
// dead incarnation is REFUSED rather than applied late: session.Identity's
// SameIncarnation exists for the same reason and this is its runtime-side
// counterpart.
type Incarnation struct {
	Session    SessionID
	Generation Generation
}

// ---------------------------------------------------------------------------
// 2. Control epoch — who is directing the session
// ---------------------------------------------------------------------------

// PrincipalKind separates the two subjects that can direct a session. It is not
// a permission level: what each may do is ADR-0064's and internal/workers', and
// nothing here widens either.
type PrincipalKind int

const (
	// PrincipalNone is the zero value and names nobody. It exists so that "no
	// holder" is a state rather than an empty string somebody has to remember
	// to check.
	PrincipalNone PrincipalKind = iota
	// PrincipalPerson is a human at a client.
	PrincipalPerson
	// PrincipalAgent is an automated caller — a coordinator, or nocx's own
	// assistant — acting under a delegation.
	PrincipalAgent
)

// Principal is who is acting. ID distinguishes two agents, or a person at two
// clients; it is opaque to the runtime.
type Principal struct {
	Kind PrincipalKind
	ID   string
}

// ControlEpoch numbers the grants of the session's one control authority.
// Minted from 1 so zero names no grant, and RISING on every grant and every
// revocation — the same shape as proto.LeaseEpoch and for the same reason: an
// intent carrying a superseded epoch is refused rather than applied late.
//
// Its SUBJECT is different from proto.LeaseEpoch's. That one answers "may this
// coordinator attachment write at all"; this one answers "is this principal
// still the one directing the session". Both are checked, and neither replaces
// the other — see the package doc.
type ControlEpoch uint64

// Control is the authority as a whole. Holder is PrincipalNone exactly when
// Epoch names a revocation rather than a grant, and a runtime with no holder
// executes no input at all.
type Control struct {
	Holder Principal
	Epoch  ControlEpoch
}

// ---------------------------------------------------------------------------
// 3. Input admission and its order
// ---------------------------------------------------------------------------

// IntentKind is what a client asked for. It is INTENT and not bytes: a frame of
// cells conveys nothing about DECCKM or bracketed paste, so a client that
// encoded its own keys would send differently encoded input while the screen
// looked correct (ADR-0066, AD-1 as amended).
type IntentKind int

const (
	IntentKindNone IntentKind = iota
	IntentKindKey
	IntentKindText
	IntentKindPaste
	IntentKindMouse
	IntentKindFocus
)

// IntentID orders intents within one incarnation. It is assigned by the runtime
// at admission, not by the client, so "the order they were admitted in" has one
// owner.
type IntentID uint64

// Intent is one thing a principal asked the terminal to do.
//
// It carries what realSession.writeJob does not, and that absence is the defect
// this type exists to close: writeJob holds a payload and a result channel, so
// once a keystroke is in the queue nothing can say whose it was or under what
// authority it was taken. An intent that cannot name its author cannot be
// cancelled when that author's authority is revoked.
type Intent struct {
	ID    IntentID
	At    Incarnation
	Under ControlEpoch
	By    Principal
	Kind  IntentKind
	// Payload is the intent's argument: the key, the text, the pasted body.
	// Encoding it against the terminal's modes is the runtime's, at execution.
	Payload []byte
	// Precondition, when set, is what the acting caller established before
	// asking. It is REVALIDATED between admission and execution, which is
	// ADR-0064's rule generalised: the identification that authorises a write
	// is the one taken immediately before it, never the one a caller saw.
	Precondition *Precondition
}

// Precondition is evidence the acting caller obtained, to be checked again at
// execution. ScreenRevision names what they read; Digest is what they read, so
// that a screen which changed underneath them is a refusal rather than a write
// into a different dialog.
type Precondition struct {
	ScreenRevision Revision
	Digest         [32]byte
}

// IntentState is where one intent got to. The states are deliberately not a
// boolean: "accepted" and "reached the PTY" are different facts, and reporting
// the first as the second is how input that never arrived looks identical to
// input that did.
type IntentState int

const (
	// IntentStateNone is the zero value: no such intent.
	IntentStateNone IntentState = iota
	// IntentStateAdmitted means the runtime took it and placed it in order. It
	// has NOT reached the PTY.
	IntentStateAdmitted
	// IntentStateExecuted means the bytes were written. It is terminal and
	// irreversible: bytes on a PTY cannot be recalled, so no later revocation
	// may report an executed intent as cancelled.
	IntentStateExecuted
	// IntentStateCancelled means it was admitted and will never execute,
	// because the authority it was admitted under was revoked first.
	IntentStateCancelled
	// IntentStateRefused means it was never admitted.
	IntentStateRefused
	// IntentStateFailed means execution was attempted and the write did not
	// complete. It is NOT IntentStateCancelled: nobody may claim a failed
	// write definitely did not reach the program.
	IntentStateFailed
)

// ---------------------------------------------------------------------------
// 4. Geometry commits
// ---------------------------------------------------------------------------

// Geometry is a terminal size. The client REPORTS one; the runtime decides.
type Geometry struct {
	Cols, Rows     uint16
	XPixel, YPixel uint16
}

// Revision is the runtime's monotonic clock. Everything a client is handed —
// the live frame, the cards, the committed geometry, the availability of an
// artifact — is relative to one, because a card sealed between two independent
// reads is otherwise in neither of them (design §5).
type Revision uint64

// GeometryCommit is a size the PTY and the emulator BOTH took. The interval it
// describes has two ends, which is the only way to state it honestly: it opens
// when both accepted the size and closes when the next commit replaces it. A
// resize that fails on either side commits neither and leaves the previous
// commit standing — the session then describes what it is still running at
// rather than what was asked for.
type GeometryCommit struct {
	Geometry Geometry
	Revision Revision
}

// ---------------------------------------------------------------------------
// 5. The authenticated lifecycle and the fence rendezvous
// ---------------------------------------------------------------------------

// FenceNonce is the unpredictable render fence a completion carries. It is
// lifecycle.FenceNonce's counterpart here and deliberately the same width; the
// runtime never mints one and never authenticates one.
type FenceNonce [32]byte

// RendezvousState is how far the meeting of the two halves has got.
//
// Both arrival orders are real and neither is the error case: ADR-0024 decision
// 7 records that SSH orders the two channels independently, so an authenticated
// completion can arrive before the last output bytes it describes. Moving both
// consumers into one process does not order their arrivals.
type RendezvousState int

const (
	// RendezvousIdle is no rendezvous in flight.
	RendezvousIdle RendezvousState = iota
	// RendezvousAwaitingSighting means the authenticated half arrived first
	// and the runtime is waiting for the emulator to see the fence.
	RendezvousAwaitingSighting
	// RendezvousAwaitingAuthenticated means the emulator saw a fence first. It
	// AUTHORISES NOTHING while it waits: a sighted marker may only LOCATE an
	// already-authenticated event (ADR-0024 decision 1), so a program printing
	// a forged one must not be able to close a block or choose an endpoint.
	RendezvousAwaitingAuthenticated
	// RendezvousComplete means both halves arrived and their nonces matched.
	RendezvousComplete
	// RendezvousExpired means the bounded wait elapsed with one half missing.
	// It is an outcome with a name, not a silent fall-through, and it forces
	// CompletenessNoFence rather than letting a capture claim to be whole.
	RendezvousExpired
)

// Rendezvous is one meeting in flight.
//
// PinnedSource is what makes it survivable and is the reason a row number is
// not enough: between the sighting and the authenticated event, output can
// overwrite or trim the rows the fence was seen on. So the sighting pins the
// CONTENT, and the pin is released only when the rendezvous leaves the pending
// states.
type Rendezvous struct {
	State        RendezvousState
	Nonce        FenceNonce
	At           Incarnation
	SightedAt    Revision
	PinnedSource []byte
}

// AuthenticatedEvents is the port through which internal/lifecycle's already
// authenticated facts reach the runtime. The runtime consumes; it never
// authenticates, never mints a nonce and never moves an attempt.
type AuthenticatedEvents interface {
	// Completed reports an authenticated completion carrying its fence. The
	// caller has already validated protocol version, domain liveness,
	// transport binding, epoch, capability and the sequence rule — that is
	// lifecycle.Kernel's, and this method is not a second gate on any of it.
	Completed(at Incarnation, nonce FenceNonce, exitCode int)
}

// ---------------------------------------------------------------------------
// 6. Capture completeness
// ---------------------------------------------------------------------------

// Completeness is what a capture can honestly claim. It is an enumeration and
// never a bool, because "not complete" has several causes that a reader must be
// able to tell apart, and because UNKNOWN has to be representable: while it is
// unknown the write gate refuses.
type Completeness int

const (
	// CompletenessUnknown is the zero value and the state a runtime is in
	// before it has established anything. Writes are refused here.
	CompletenessUnknown Completeness = iota
	// CompletenessComplete means every byte of the interval reached the
	// emulator and the capture body is the whole of it.
	CompletenessComplete
	// CompletenessLostIngest means output was lost before the emulator saw it
	// — the helper's bounded output window discarded its oldest bytes, or a
	// re-attachment reported a hole (internal/transport/ws_readopt.go:100). An
	// emulator fed only the surviving suffix is not authoritative.
	CompletenessLostIngest
	// CompletenessNoFence means ingest was whole but the rendezvous expired,
	// so the interval has no authenticated boundary. The body may still be
	// worth keeping; it may not be described as the command's complete output.
	CompletenessNoFence
	// CompletenessEvicted means retention deliberately kept less than the
	// whole. It is a DIFFERENT available artifact, never an empty body and
	// never silently described as complete.
	CompletenessEvicted
)

// ---------------------------------------------------------------------------
// 7. Runtime failure
// ---------------------------------------------------------------------------

// Availability is whether the session can be used at all.
//
// The honest first answer to a runtime that failed is that its session becomes
// unavailable and writes are revoked — NOT that a surviving process is adopted
// under an invented terminal state. Transparent recovery needs complete
// emulator checkpoints including pending parser state and is a separate
// durability feature (nocx-ygxjv.3 says so in the same words).
type Availability int

const (
	// AvailabilityUnknown is the zero value: nothing has been established.
	AvailabilityUnknown Availability = iota
	// AvailabilityAvailable means the runtime holds the terminal and may act.
	AvailabilityAvailable
	// AvailabilityUnavailable is terminal. The incarnation is over, every
	// admitted intent is cancelled, control is revoked and no later evidence
	// about this incarnation is accepted.
	AvailabilityUnavailable
)

// ---------------------------------------------------------------------------
// 8. Delivery: three classes, and the bounds that make them keepable
// ---------------------------------------------------------------------------

// DeliveryClass is what a consumer may do with one payload, and the PRODUCER
// decides it — the runtime classifies what it emits, so that a consumer reading
// a gap never has to guess whether it missed a frame that was coalesced or a
// byte that was dropped.
//
// Three classes, exhaustive, and every payload belongs to exactly one. That is
// the only form of AD-10's promise that can be kept: "lossless and ordered"
// described raw PTY bytes, the emulator moved to the backend (ADR-0066), and
// the data plane now carries FRAMES outbound and effects beside them. So the
// statement splits, per class:
//
//   - [DeliveryLossless] — the payloads that must never be lost, in order: the
//     output the runtime INGESTS into the emulator, and the ledger. Losing a
//     byte here is losing what the program said, so no bound licenses
//     discarding one: the way to keep the promise is to throttle the SOURCE
//     (AD-10's credit, which is the carrier's and the helper's), never to drop.
//   - [DeliveryCoalescable] — what FRAMES are. An intermediate visual state
//     nobody was shown is explicitly LOSSY, because that is what frame-rate
//     coalescing IS: the consumer is shown a later revision of the same cells
//     instead of every revision that existed. Calling this class lossless
//     would be a promise the design cannot keep, so it is not called that, and
//     the loss is REPORTED to the consumer rather than discovered by it.
//   - [DeliveryAtMostOnce] — the payloads that change no cell: a bell, a
//     notification request, an OSC 52 clipboard write, a title, a cwd report
//     (design §6.2). Each carries an [EffectID], and the duplicate policy is
//     stated over that identity: a consumer that has already been given an
//     effect with that identity is not given it again, and a FULL FRAME — a
//     snapshot, a resend, a re-attachment — carries cells and never an effect.
//     A resend of state must not resend an effect.
//
// [DeliveryUnclassified] is the zero value and is NOT a class: a payload whose
// class nobody decided is one the runtime refuses ([ErrUnclassifiedDelivery])
// rather than one it delivers as though the zero value were a policy.
type DeliveryClass int

const (
	// DeliveryUnclassified is the absence of a classification, and nothing may
	// be delivered carrying it.
	DeliveryUnclassified DeliveryClass = iota
	// DeliveryLossless is a payload that must never be lost, in order: the
	// output ingested into the emulator, and the ledger.
	DeliveryLossless
	// DeliveryCoalescable is a payload a later one supersedes: a frame whose
	// cells have been redrawn does not have to reach the consumer, and what
	// the consumer lost is reported to it.
	DeliveryCoalescable
	// DeliveryAtMostOnce is a payload that changes no cell and must not be
	// applied twice: a bell, a notification request, a clipboard write, a
	// title, a cwd report.
	DeliveryAtMostOnce
)

// EffectKind is which non-visual effect a program asked for, in the spellings
// the renderer handles today (frontend/src/renderers/xterm.ts): BEL for a bell,
// OSC 9 and OSC 777 for one notification request (two spellings of one thing,
// so nothing downstream may depend on which was sent), OSC 52 for a clipboard
// write, OSC 0 and OSC 2 for a title, OSC 7 for a cwd report.
//
// A fence is deliberately NOT here. OSC 133/1337 ride the rendezvous above,
// which is a meeting of two authenticated halves and not a delivery; a program
// printing a forged one must not be able to close a block or choose a capture
// endpoint (ADR-0024 decision 1).
type EffectKind int

const (
	// EffectNone is the zero value and names no effect: an OSC number that
	// carries none — a fence, a progress hint — yields no effect at all rather
	// than one of this kind.
	EffectNone EffectKind = iota
	// EffectBell is BEL.
	EffectBell
	// EffectNotification is OSC 9 or OSC 777: the program asked nocx to
	// present a message (ADR-0047). It is a REQUEST, never a grant — what the
	// router does with it is internal/notify's and nothing here widens it.
	EffectNotification
	// EffectClipboard is OSC 52: the program asked for its payload to be put
	// on the clipboard. Decoding it is the surface's; delivering it once is
	// this contract's.
	EffectClipboard
	// EffectTitle is OSC 0 or OSC 2.
	EffectTitle
	// EffectCwdReport is OSC 7.
	EffectCwdReport
)

// EffectID identifies one effect for as long as its incarnation lives. The
// runtime mints it when the program's output produced the effect — not the
// consumer, and not the client, because an identity a consumer could choose is
// an identity it could choose twice.
type EffectID uint64

// Effect is one non-visual effect with the identity its duplicate policy is
// stated over: two deliveries carrying the same [EffectID] are the same bell,
// the same clipboard write, the same notification request, and the second one
// is not applied again. Identity is the whole of what makes that implementable
// — without it, "sent twice" and "two identical bells" are the same bytes.
type Effect struct {
	ID   EffectID
	At   Incarnation
	Kind EffectKind
	// Body is the effect's argument: the notification text, the clipboard
	// payload, the title, the reported directory. It is untrusted bytes from
	// whatever the user ran, and nothing here interprets them.
	Body []byte
}

// The bounds. Each is a NUMBER rather than a policy statement, so that
// "bounded" is a claim somebody can check, and each belongs to the runtime
// rather than to the emulator it feeds — the emulator's own bounds are the
// emulator's and are named where the choice of emulator is (ADR-0065,
// nocx-ygxjv.2).
const (
	// MaxIngestBytes is the most output one ingest call carries. A larger call
	// is REFUSED ([ErrIngestTooLarge]) and changes nothing, rather than
	// silently truncated: the class stays lossless because nothing was taken
	// and then discarded — the bytes are still the CALLER's, and the caller is
	// the carrier, whose obligation is then to hand them over again in windows
	// of at most this (its own reads are bounded by the credit AD-10 gives it —
	// internal/transport/ring.go's CreditLimit, 64 KiB per subscriber, which is
	// what this number's order matches and not what it decides: that one bounds
	// transport buffering for a reader, this one bounds the work the runtime
	// does on its own account in one call). A call this large therefore means a
	// caller that has stopped honoring its own window, and the runtime says so
	// instead of doing unbounded work on its account.
	MaxIngestBytes = 64 << 10
	// MaxPendingSequence is the most of an UNTERMINATED escape sequence the
	// runtime holds. A remote program can open an OSC or a DCS and never close
	// it; the bytes past this bound are the oldest of that sequence and are
	// discarded, with the loss reported (Design §6.7: an emulator fed a
	// partial stream is not authoritative, and the attach must say so).
	MaxPendingSequence = 4 << 10
	// MaxPendingFrames is the most payloads one session's consumer queue may
	// hold. Past it the runtime coalesces — the oldest coalescable payload
	// goes, its loss is reported to the consumer — and it never grows, because
	// the consumer is whoever is reading or not reading the session's screen.
	// The allowance is PER SESSION and is never transferable: one session
	// reading slowly must not spend another session's, which is what per-session
	// fairness in AD-10 means over frames.
	MaxPendingFrames = 8
)

// ---------------------------------------------------------------------------
// The interface an implementation must satisfy
// ---------------------------------------------------------------------------

// Runtime is the session runtime's whole surface to the contract. nocx-ygxjv.2
// implements it over a real PTY and a real emulator; the model in this
// package's tests implements it with neither, and the same schedules judge
// both.
//
// Every method is a TRANSITION. An invalid event mutates nothing and returns a
// sentinel error from this file — the rule internal/lifecycle's kernel is built
// on, and the reason its invalid events are testable at all.
type Runtime interface {
	// Incarnation is the identity every other call is judged against.
	Incarnation() Incarnation
	// Availability is whether the runtime holds the terminal.
	Availability() Availability
	// Revision is the runtime's monotonic clock, and the number a snapshot and
	// the changes after it are related by.
	Revision() Revision

	// GrantControl makes p the directing principal and mints the next epoch.
	// It is a TAKEOVER and it is explicit: it revokes the previous holder's
	// authority from the boundary it returns, and every intent admitted under
	// the previous epoch and not yet executed is cancelled. Already executed
	// intents are untouched, because bytes on a PTY cannot be recalled.
	GrantControl(p Principal) (Control, error)
	// RevokeControl ends the current grant without making anybody else the
	// holder. The epoch still rises.
	RevokeControl() (Control, error)
	// Control is the authority as it stands.
	Control() Control

	// Admit takes an intent into the ordered queue, or refuses it. Accepted is
	// NOT delivered: the returned id names something that has not reached the
	// PTY, and no caller may report otherwise.
	Admit(i Intent) (IntentID, error)
	// Execute performs the next admitted intent: revalidates its incarnation,
	// its control epoch and its precondition, encodes it against the modes the
	// program set, and writes. Revalidation at CONSUMPTION rather than at
	// admission is the point — see Precondition.
	Execute() (IntentID, IntentState, error)
	// IntentState reports where one intent got to.
	IntentState(id IntentID) IntentState

	// ReportGeometry is the client saying what it can show. It is a report,
	// not an instruction: the runtime decides, and the frame follows.
	ReportGeometry(g Geometry) error
	// CommitGeometry applies a decided size to the PTY and the emulator
	// together, or to neither.
	CommitGeometry(g Geometry) (GeometryCommit, error)
	// Geometry is the commit in force.
	Geometry() GeometryCommit

	// Ingest feeds output into the emulator. The same bytes must produce the
	// same screen wherever the writes were split, which is a property of the
	// emulator (ADR-0065) and an obligation of this method.
	Ingest(b []byte) error
	// ReportHole says bytes were lost before the runtime saw them.
	ReportHole(lost uint64) error
	// SightFence reports that the emulator drew a fence, and pins the content
	// it was drawn over.
	SightFence(nonce FenceNonce, source []byte) error
	// ExpireRendezvous is the bounded wait elapsing. It is a call rather than a
	// timer so the contract can exercise it without depending on duration:
	// a test may not depend on timing (AGENTS.md).
	ExpireRendezvous() error
	// Rendezvous is the meeting in flight.
	Rendezvous() Rendezvous
	// Completeness is what the capture may honestly claim.
	Completeness() Completeness

	// Fail ends the runtime. Its session becomes unavailable and writes are
	// revoked; nothing is adopted.
	Fail(cause string) error

	// Snapshot is what an attaching or resynchronising observer receives: the
	// state AT a revision, so that changes after it compose. Taking one
	// changes no incarnation, no control, no terminal state, and replays no
	// intent and no effect.
	Snapshot() Snapshot
}

// Snapshot is one consistent read. Every field belongs to Revision, which is
// what makes "the cards through R and the live frame at R, then the changes
// after R" expressible at all.
type Snapshot struct {
	Revision     Revision
	At           Incarnation
	Availability Availability
	Control      Control
	Geometry     GeometryCommit
	Screen       []byte
	Rendezvous   RendezvousState
	Completeness Completeness
}

// ---------------------------------------------------------------------------
// Sentinel errors. An invalid event mutates nothing and returns one of these.
// ---------------------------------------------------------------------------

var (
	// ErrStaleIncarnation names evidence about a terminal that is gone.
	ErrStaleIncarnation = errors.New("sessionruntime: evidence names another incarnation")
	// ErrStaleControlEpoch names an intent from a principal that no longer
	// directs the session.
	ErrStaleControlEpoch = errors.New("sessionruntime: intent carries a superseded control epoch")
	// ErrNoController names input with nobody holding control.
	ErrNoController = errors.New("sessionruntime: no principal holds control")
	// ErrUnavailable names any call to a runtime that has failed.
	ErrUnavailable = errors.New("sessionruntime: the runtime is unavailable")
	// ErrCompletenessUnknown is the write gate refusing while it cannot say
	// whether what it holds is the whole stream.
	ErrCompletenessUnknown = errors.New("sessionruntime: completeness is unknown, writes are refused")
	// ErrPreconditionStale names a write whose evidence no longer describes
	// the screen. ADR-0064's rule, at consumption.
	ErrPreconditionStale = errors.New("sessionruntime: the screen changed since the caller read it")
	// ErrNothingAdmitted names Execute with an empty queue.
	ErrNothingAdmitted = errors.New("sessionruntime: nothing is admitted")
	// ErrNonceMismatch names two halves of a rendezvous that are not the same
	// event.
	ErrNonceMismatch = errors.New("sessionruntime: fence nonce does not match the pending rendezvous")
	// ErrNoRendezvous names an expiry or a sighting with nothing in flight.
	ErrNoRendezvous = errors.New("sessionruntime: no rendezvous is in flight")
	// ErrGeometryInvalid names a size no terminal can run at.
	ErrGeometryInvalid = errors.New("sessionruntime: geometry is not valid")
	// ErrIngestTooLarge is one ingest call over [MaxIngestBytes]. It is a
	// REFUSAL and not a truncation: nothing was ingested, and the caller knows.
	ErrIngestTooLarge = errors.New("sessionruntime: the ingest call is larger than the runtime will take")
	// ErrUnclassifiedDelivery names a payload whose [DeliveryClass] nobody
	// decided — the class no payload belongs to. The runtime refuses it rather
	// than inventing a policy for it, which is what makes "every payload
	// belongs to exactly one class" a promise instead of a habit.
	ErrUnclassifiedDelivery = errors.New("sessionruntime: the payload has no delivery class")
)
