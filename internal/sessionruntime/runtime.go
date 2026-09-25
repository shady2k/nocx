package sessionruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/shady2k/nocx/internal/emulator"
)

// -------------------------------------------------------------- construction

// Config is everything a runtime must be handed to exist. Everything in it is
// a fact about a session that already exists — the incarnation it belongs to,
// the size its two sides were started at, and the two ends of the write
// boundary — because a runtime does not create a terminal, it directs one.
//
// The runtime NEVER resizes on its own account at construction: the pair it is
// handed is already running at Geometry (the composition root started both at
// that size), and [Session.CommitGeometry] is the only way a size moves
// afterwards.
type Config struct {
	// Incarnation is the session and generation this runtime is. It is minted
	// by whoever started the process (the composition root), never here: an
	// identity a runtime chose for itself could not be checked against the
	// evidence arriving from the other side.
	Incarnation Incarnation
	// Geometry is the size the terminal and the emulator are ALREADY running
	// at, and the first commit in force.
	Geometry Geometry
	// Terminal is the PTY side: the bytes the runtime decided, and the size it
	// asked for. internal/pty.Pty is the real one, behind this port.
	Terminal Terminal
	// Emulator is the screen: the parser, the modes, the answers to the
	// program's own questions and the cells a client is sent. It is the
	// full port because the runtime drives all of it; [Session.Emulator]
	// answers the contract's narrower seam, which the same value satisfies.
	Emulator emulator.Terminal
	// Completeness is what the CALLER has established about the stream before
	// this runtime existed. The zero value is [CompletenessUnknown], where the
	// write gate refuses — which is the honest state of a runtime attached to
	// a session whose earlier output it never saw, and why a fresh session
	// says [CompletenessComplete] out loud rather than being assumed.
	Completeness Completeness
	// Allowance is the delivery allowance this session's consumer queues draw
	// on. Nil gives this runtime an allowance of its own; supplying one is how
	// a caller can hold several sessions to ONE account, which is the
	// arrangement per-session fairness is checkable against.
	Allowance *Allowance
	// Replies is where Ingest, CommitGeometry and repairLocked hand the bytes
	// they used to write directly (spec §5.5): a reply to the program's own
	// question queues as an ordinary input item instead of being written
	// under this package's lock, which is the other half of nocx-6q1uh.1's
	// fix (Commit is the half at the write end of an intent; this is the
	// half at Ingest's).
	//
	// It may be nil at construction and bound afterward with [Session.SetReplies]:
	// the production sink — the session's I/O owner — is built OVER the
	// runtime a moment after the runtime is built (internal/helper/session's
	// newSessionRuntime has no owner yet to hand here), and nothing calls
	// Ingest before that binding completes. A runtime asked to deliver a
	// reply with none bound refuses loudly (see deliverReplyLocked) rather
	// than discarding the program's answer.
	Replies ReplySink
}

// Allowance is the delivery allowance consumer queues draw on, keyed by the
// session that spends it. It exists as a type rather than as a field of the
// runtime for the reason [MaxPendingFrames] gives: the allowance is PER
// SESSION, and a single account every session spent from would let the first
// wedged consumer starve the others.
type Allowance struct {
	mu        sync.Mutex
	remaining map[SessionID]int
}

// NewAllowance returns an allowance nothing has been spent from yet.
func NewAllowance() *Allowance {
	return &Allowance{remaining: map[SessionID]int{}}
}

// take spends one payload's worth of the account's allowance, opening the
// account at [MaxPendingFrames] the first time it is drawn on.
func (a *Allowance) take(account SessionID) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	left, ok := a.remaining[account]
	if !ok {
		left = MaxPendingFrames
	}
	if left <= 0 {
		a.remaining[account] = 0
		return false
	}
	a.remaining[account] = left - 1
	return true
}

// give refunds what n payloads spent, the hand-over side of take: a queue the
// consumer has drained is memory the session is no longer spending on it. It
// is called with the SESSION's lock held, the same order take is called under
// from the enqueue path, and only ever with a count take spent — the two stay
// symmetric, so an account never holds more than [MaxPendingFrames] worth of
// refunds it did not earn.
func (a *Allowance) give(account SessionID, n int) {
	if n <= 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.remaining[account] += n
}

// Session is the session runtime: an implementation of [Runtime] over a real
// terminal and a real emulator (nocx-ygxjv.9).
//
// # One mutex, held across validation and encoding — never across a write
//
// Every field is guarded by mu, and mu is held for exactly as long as
// deciding an intent's fate takes: revalidating its incarnation and control
// epoch, re-checking its precondition, running the caller's own check and
// encoding it against the modes the program set ([Commit]). It is NEVER held
// across the write those bytes then take — this package no longer performs
// that write at all (nocx-6q1uh.1, nocx-6q1uh.3, spec §5.1).
//
// Before this bead the two were one step: [Session] wrote to the terminal
// itself, under mu, the same way internal/helper/session.hostSession.write
// once held ITS mutex from validating the writer through the return of
// proc.Write. Both were the same defect stated twice — a lease or a control
// epoch cannot change between a check and a write only if nothing else may
// run meanwhile, and "nothing else may run" is exactly what stalls a pane
// whose program floods output and never reads its input: the pump needed
// this same lock to ingest, and a write blocked on that program held it.
// [Commit] answers the requirement (one indivisible validate-and-decide step)
// without answering it with a lock a slow write can hold forever: it returns
// the encoded bytes and writes nothing, so the one thing left to serialise
// against a takeover — deciding whose authority the bytes are decided under —
// finishes before mu is ever released, and the write that follows is the
// caller's, off this lock entirely.
//
// The remaining cost is unchanged in shape and smaller in practice: a read
// ([Snapshot], [IntentState]) still waits behind whichever of Ingest, Commit
// or CommitGeometry currently holds mu, but none of those calls blocks on a
// program's own pace any more — Reply through [ReplySink] and the return from
// Commit are both bounded, and the actual write is the owner's, elsewhere.
type Session struct {
	mu sync.Mutex

	// replies is where a reply to the program's own question goes once it can
	// no longer be written here (deliverReplyLocked). See [Config.Replies]
	// and [SetReplies].
	replies ReplySink

	inc   Incarnation
	avail Availability
	rev   Revision
	// cause is why the runtime failed, kept so a later reader of a failed
	// session can say what ended it rather than only that something did.
	cause string

	control Control

	queue   []IntentID
	intents map[IntentID]*intentRecord
	nextID  IntentID

	terminal Terminal
	emulator emulator.Terminal

	geom     GeometryCommit
	reported Geometry

	// The rendezvous is a SET of meetings keyed by nonce, not one slot. The
	// authenticated channel and the PTY are ordered independently (ADR-0024
	// decision 7, design §6.4), so two commands can legitimately be pending
	// at once, and one slot let the second command evict the first — losing
	// its pinned source or closing it on a nonce mismatch while completeness
	// still read complete. MaxPendingRendezvous bounds the set: an unbounded
	// map fed by forged fences is the memory exhaustion the single slot
	// accidentally prevented. All three fields are guarded by mu.
	rendezvous map[FenceNonce]*rendezvousEntry
	// rendezvousOrder is insertion order, so the bound's evictions and any
	// scan have a deterministic oldest first.
	rendezvousOrder []FenceNonce
	// rendezvousLatest names the meeting [Session.Rendezvous] answers — the
	// one most recently created, joined or expired. rendezvousHasLatest
	// separates "none yet" from a legitimate all-zero nonce.
	rendezvousLatest    FenceNonce
	rendezvousHasLatest bool

	completeness Completeness

	allowance *Allowance
	// consumers are the subscribers attached to this session, in attach order.
	consumers []*subscriber
	// latest is the frame the runtime last published: the bytes a
	// mid-session attacher is handed as its baseline and a resync is served
	// from. It is the screen AS READ at publishedFrame.rev — a later tick
	// that moves no cell (a rendezvous expiring, a control epoch turning
	// over) leaves it exact, and one that draws or reflows publishes a new
	// one. Guarded by mu, like everything else here.
	latest *publishedFrame
	// nextEffect mints the identity an effect's duplicate policy is stated
	// over. The runtime mints it, never the consumer.
	nextEffect EffectID

	// ingestWork is the work the runtime's OWN ingest path has spent, one unit
	// per byte it examined, and ingestLost is what it discarded. Both are
	// numbers so that "bounded" is checkable rather than asserted.
	ingestWork uint64
	ingestLost uint64

	// The observation record (nocx-zg3k3.5.2): observation is the interval
	// in flight — its opening screen and the losses it has counted (the
	// departed rows themselves leave through rowStream, nocx-2v80t.3.6;
	// the helper keeps no copy); observations are the records authenticated
	// boundaries have sealed, oldest first, bounded by [MaxObservations]
	// with the evictions counted in observationsEvicted. All three are
	// guarded by mu, like everything else here, and the reads that hand
	// records out live in observation.go.
	observation         *observationOpen
	observations        []ObservationRecord
	observationsEvicted uint64
	// rowStream is where departed rows leave the session as they leave the
	// screen, and IntervalEnd joins them. Nil is ordinary — nobody is
	// watching — and never loses the indices: departedRows keeps counting
	// (rowstream.go).
	rowStream RowStream
	// pendingScreen are the rows of the interval sealed last that were still
	// on the screen at its boundary. They leave the screen later — during the
	// next command, as its output pushes them off — and the emulator reports
	// them as departures when they do. The interval's block already holds
	// them as its closing screen, so the stream must not carry them a second
	// time: content against the head of this list is what tells a row
	// leaving the screen again from the next command's own output. Held only
	// until the first row that is not one of them (the screen has been
	// rewritten, so the boundary's rows are gone), and bounded by the
	// geometry. Nil after the first mismatch.
	//
	// Content alone is ambiguous by design (nocx-2v80t.3.10): a row a `clear`
	// destroyed and a row the NEXT command legitimately prints can read the
	// same, and content cannot tell them apart. Each entry therefore also
	// carries a [emulator.RowTrack] pinned to the row at capture time —
	// released the moment the entry is consumed, dropped, or the whole
	// window is replaced or cleared, so none outlives the window it names.
	pendingScreen []pendingBoundaryRow
	// pendingEntered is whether a row has matched that window yet. Before it
	// has, an arriving row that matches nothing is a row from ABOVE the
	// boundary's screen (history a geometry commit pulled back), not the next
	// command's own output: it leaves the window standing, and only once the
	// window has been entered does an unmatched row end it.
	pendingEntered bool
	// suppressedScreenRows counts the rows the stream declined to carry a
	// second time because they were the interval before's closing screen
	// leaving the screen again. A count, because a consumer must be able to
	// tell "nothing left the screen" from "rows were held back".
	suppressedScreenRows uint64
	// departedRows is how many departed rows this session has READ off the
	// emulator's report, in the order it read them, plus one index for every
	// struck feed (a loss spends the index it names, nocx-2v80t.3.26). It is
	// the absolute index space the stream's FromRow and endRow name rows by
	// — the session's, not any consumer's, so it advances whether or not a
	// row stream is bound.
	departedRows uint64
	// screenDepartedRows is how many rows have left the TOP of the screen,
	// as the emulator reported them — every row departedRows counts, plus
	// every row the suppression window declined to stream a second time
	// (suppressBoundaryScreenLocked). The two differ exactly by those
	// suppressed rows, and the difference matters to one question only:
	// how far a scroll has moved what sat on the screen at an instant, which
	// every departed row does whether or not it was streamed
	// (outputMarkSkipLocked, nocx-2v80t.3.24). departedRows stays the
	// stream's index space; this is never an index.
	screenDepartedRows uint64
	// obsCarried is how much of ingestLost some observation record already
	// carries: a hole reported before the first ingest, or in the gap
	// between one sealed interval and the next output, reaches no record at
	// the moment it is reported, and this is the marker that seeds it into
	// the record that opens next instead of letting the count die between
	// intervals (observation.go).
	obsCarried uint64

	// bufferInstance, bufferActive and bufferSeen are ScreenIdentity's own
	// bookkeeping (nocx-6q1uh.4, digest.go's ScreenIdentity doc): the
	// emulator's [emulator.Terminal.Screen] answers only which buffer is
	// active, never an identity for the buffer itself, so this package
	// derives one by counting every observed toggle between primary and
	// alternate. bufferSeen is false only before the first locked screen
	// read this incarnation has ever done; that first read counts as
	// observing an instance too (bufferInstance becomes 1, not 0), so a
	// token minted before any toggle still carries a real instance rather
	// than the zero value a bare ScreenIdentity{} also has.
	bufferInstance uint64
	bufferActive   bool
	bufferSeen     bool

	// entriesSealed are the environment entries this incarnation has already
	// sealed an interval for, oldest first, bounded by
	// [MaxRememberedEnvironmentEntries] (nocx-2v80t.3.28). It is what makes
	// [Session.SealEnvironmentEntry] idempotent: the coordinator's downlink
	// retries a delivery whose attempt timed out, and that attempt may
	// already have landed.
	entriesSealed []EnvironmentEntryID
}

// EnvironmentEntryID names one environment entry: the CHILD domain whose
// establishment took the lane (internal/lifecycle's DomainID, minted by the
// kernel as "dom-" and random hex, so it never recurs). It is the entry's
// identity, not the delivery's: every copy of one entry carries the same id,
// whichever attempt carried it, and two different child domains are two
// entries even when nothing ran between them.
type EnvironmentEntryID string

// MaxRememberedEnvironmentEntries bounds how many entries a session
// remembers having sealed. A duplicate is a RETRY of the downlink's queue
// head (internal/helper/client's CompletionDownlink sends nothing behind the
// head until the head lands), so it arrives before any other entry is sent
// and the most recent id alone would catch it; the bound keeps a few more so
// that an abandoned attempt the helper answers late, behind a later entry,
// is still recognised. A number, like every bound here, not an unbounded set
// fed by an unbounded session (AD-10).
const MaxRememberedEnvironmentEntries = 8

// intentRecord is one admitted intent and where it got to. The record outlives
// the queue: IntentState(id) has to answer long after an intent left it.
type intentRecord struct {
	intent Intent
	state  IntentState
}

var (
	_ Runtime = (*Session)(nil)
	// The emulator seam is satisfied BY the port's own terminal, and it is
	// checked here rather than asserted in prose: sessionruntime.Emulator is
	// the half of internal/emulator's Terminal a geometry commit needs, over
	// the one Geometry both packages name (contract.go, Geometry).
	_ Emulator = emulator.Terminal(nil)
)

// New builds a runtime over the terminal and the emulator it is handed. It
// touches neither: the pair arrives running at Config.Geometry, and the first
// commit in force is that size at revision 1.
func New(cfg Config) (*Session, error) {
	if cfg.Terminal == nil {
		return nil, errors.New("sessionruntime: a runtime needs a terminal to direct")
	}
	if cfg.Emulator == nil {
		return nil, errors.New("sessionruntime: a runtime needs an emulator to feed")
	}
	if !cfg.Geometry.Valid() {
		return nil, fmt.Errorf("%w: %+v", ErrGeometryInvalid, cfg.Geometry)
	}
	allowance := cfg.Allowance
	if allowance == nil {
		allowance = NewAllowance()
	}
	return &Session{
		inc:          cfg.Incarnation,
		avail:        AvailabilityAvailable,
		rev:          1,
		intents:      map[IntentID]*intentRecord{},
		rendezvous:   map[FenceNonce]*rendezvousEntry{},
		terminal:     cfg.Terminal,
		emulator:     cfg.Emulator,
		geom:         GeometryCommit{Geometry: cfg.Geometry, Revision: 1},
		completeness: cfg.Completeness,
		allowance:    allowance,
		replies:      cfg.Replies,
	}, nil
}

// SetReplies binds the sink Ingest, CommitGeometry and repairLocked hand
// their replies to, after construction. It exists for exactly one caller's
// shape: internal/helper/session's spawn builds the runtime before the
// session's I/O owner exists — the owner is built OVER the runtime a moment
// later and is itself the sink (spec §5) — so [Config.Replies] cannot name it
// at [New] time. Nothing may call Ingest, CommitGeometry or a resize between
// spawn and this binding (the owner starts the read loop that would trigger
// any of them), so there is no window in which a reply arrives with nothing
// bound.
func (s *Session) SetReplies(r ReplySink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies = r
}

// ---------------------------------------------------------------- the reads

func (s *Session) Incarnation() Incarnation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inc
}

func (s *Session) Availability() Availability {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.avail
}

func (s *Session) Revision() Revision {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rev
}

func (s *Session) Control() Control {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.control
}

func (s *Session) Geometry() GeometryCommit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.geom
}

// Rendezvous answers the meeting most recently touched — created, joined or
// expired — and the zero Rendezvous (idle) when nothing is tracked. It is a
// window on the set for a caller that observed a fence or a completion and
// knows nothing else about it; a caller that can name a nonce uses
// [Session.RendezvousFor].
func (s *Session) Rendezvous() Rendezvous {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.rendezvousHasLatest {
		return Rendezvous{}
	}
	e := s.rendezvous[s.rendezvousLatest]
	if e == nil {
		return Rendezvous{}
	}
	return cloneRendezvous(e.Rendezvous)
}

// RendezvousFor answers one meeting by nonce, or the zero Rendezvous (idle)
// when no meeting for that nonce is tracked. It is the keyed authority over
// the set; [Session.Rendezvous] is a window on the same store.
func (s *Session) RendezvousFor(nonce FenceNonce) Rendezvous {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.rendezvous[nonce]
	if e == nil {
		return Rendezvous{}
	}
	return cloneRendezvous(e.Rendezvous)
}

// cloneRendezvous copies a meeting's record for handing out. The pin is the
// sighting's CONTENT and it is handed out as a copy: a caller that wrote
// through it would be editing the evidence a rendezvous is judged against,
// which is the one thing the pin exists to keep.
func cloneRendezvous(r Rendezvous) Rendezvous {
	r.PinnedSource = bytes.Clone(r.PinnedSource)
	return r
}

// pendingRendezvousLocked counts the meetings in flight — Snapshot's
// PendingRendezvous, bounded by MaxPendingRendezvous and therefore checkable.
func (s *Session) pendingRendezvousLocked() int {
	n := 0
	for _, e := range s.rendezvous {
		if e.pending() {
			n++
		}
	}
	return n
}

func (s *Session) Completeness() Completeness {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completeness
}

func (s *Session) Terminal() Terminal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminal
}

func (s *Session) Emulator() Emulator {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.emulator
}

// AuthenticatedEvents is the session itself: it is what a completion is
// delivered TO, and the contract's read exists so that a schedule can deliver
// one without reaching past the interface (contract.go).
func (s *Session) AuthenticatedEvents() AuthenticatedEvents { return s }

// Consumers is the session itself for the reason the model's is: the delivery
// side is the queues, the allowance and the minted effect identities, and the
// session already holds all three.
func (s *Session) Consumers() Consumers { return s }

func (s *Session) ReportedGeometry() Geometry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reported
}

func (s *Session) Intents() []IntentRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.intentsLocked()
}

func (s *Session) intentsLocked() []IntentRecord {
	out := make([]IntentRecord, 0, len(s.intents))
	for id := IntentID(1); id <= s.nextID; id++ {
		if rec, ok := s.intents[id]; ok {
			out = append(out, IntentRecord{ID: id, State: rec.state})
		}
	}
	return out
}

func (s *Session) IngestState() IngestState {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Pending is always empty, and that is a FACT about this runtime rather
	// than an omission: it holds no part of an escape sequence, because the
	// parser it feeds does (ADR-0065, contract.go's Emulator). The contract's
	// [MaxPendingSequence] describes a runtime that buffers a trailing open
	// sequence itself, and the port it drives carries no such bytes to report
	// — see this bead's report for the rule that therefore cannot be honoured.
	return IngestState{Work: s.ingestWork, Lost: s.ingestLost}
}

func (s *Session) IntentState(id IntentID) IntentState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.intentStateLocked(id)
}

func (s *Session) intentStateLocked(id IntentID) IntentState {
	rec, ok := s.intents[id]
	if !ok {
		return IntentStateNone
	}
	return rec.state
}

// Snapshot is one consistent read at the revision it names, and it changes
// nothing: the clock, the incarnation, control, the terminal state and the
// consumers are all as they were, and no intent and no effect is replayed.
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

// snapshotLocked is Snapshot's body, split out for [Commit]: Commit already
// holds s.mu when it runs the caller's check, and Snapshot would deadlock
// taken from inside it.
func (s *Session) snapshotLocked() Snapshot {
	rows, cur, identity := s.screenStateLocked()
	return Snapshot{
		Revision:          s.rev,
		At:                s.inc,
		Availability:      s.avail,
		Control:           s.control,
		Geometry:          s.geom,
		Screen:            s.screenTextLocked(),
		PendingRendezvous: s.pendingRendezvousLocked(),
		Completeness:      s.completeness,
		Rows:              rows,
		Cursor:            cur,
		Identity:          identity,
	}
}

// screenStateLocked reads the rows (WITH style), the caret and the
// [ScreenIdentity] a target's digest is judged against (nocx-6q1uh.4, spec
// §6.2) — the same active screen [screenTextLocked] reduces to text, read a
// second way because a digest must notice a selection highlight or an
// attribute-only change that carries no text of its own.
//
// A screen that cannot be read answers no rows at all and an identity naming
// only the incarnation, the same honest silence [screenTextLocked] keeps for
// a closed emulator: a caller comparing Cols/Rows against a token's would see
// 0x0 and refuse `incomparable` rather than being handed a screen nobody
// read.
func (s *Session) screenStateLocked() ([]emulator.Row, emulator.Cursor, ScreenIdentity) {
	geom, err := s.emulator.Geometry()
	if err != nil {
		return nil, emulator.Cursor{}, ScreenIdentity{At: s.inc}
	}
	rows := make([]emulator.Row, 0, geom.Rows)
	for y := range geom.Rows {
		row, rowErr := s.emulator.Row(y)
		if rowErr != nil {
			return nil, emulator.Cursor{}, ScreenIdentity{At: s.inc}
		}
		rows = append(rows, row)
	}
	cur, err := s.emulator.Cursor()
	if err != nil {
		cur = emulator.Cursor{}
	}
	scr, err := s.emulator.Screen()
	alt := err == nil && scr == emulator.ScreenAlternate
	s.observeBufferLocked(alt)
	return rows, cur, ScreenIdentity{
		At:             s.inc,
		AltScreen:      alt,
		BufferInstance: s.bufferInstance,
		Cols:           geom.Cols,
		Rows:           geom.Rows,
	}
}

// observeBufferLocked advances bufferInstance the moment the active buffer
// differs from what the last locked screen read saw (see the field's own
// doc). Running it on every locked read rather than only from [Ingest] reads
// the identical count either way — the guard is idempotent, so a hundred
// reads between two toggles advance it exactly as many times as a hundred
// ingests would (once, at the toggle) — and it is done here because this is
// the one place both [Snapshot] and [Commit]'s check already read the live
// buffer, where [Ingest] would have to read it again for no additional
// accuracy.
func (s *Session) observeBufferLocked(alt bool) {
	if !s.bufferSeen || alt != s.bufferActive {
		s.bufferInstance++
		s.bufferActive = alt
		s.bufferSeen = true
	}
}

// ReadScreen lends the emulator this runtime directs for the duration of ONE
// read, and answers the revision and the completeness that read was taken at.
//
// It exists because a FRAME is several reads of one terminal — the geometry,
// which buffer is active, the caret, and every row — and the port orders none
// of them against an ingest ("an ingest concurrent with a read may or may not
// be reflected in it", emulator.Terminal). A caller composing those reads
// unguarded answers a screen the terminal was never in: the pump ingests on its
// own goroutine, so a chunk landing between the caret read and the rows leaves
// a frame whose rows carry text the caret it reports has not reached.
//
// Holding this runtime's lock for the whole read is what makes the frame an
// instant rather than a span. It costs what the contract already says a read
// costs — it waits behind an ingest in flight, which itself waits behind a
// reply write — and it is on the SESSION rather than on the Runtime contract
// because it lends state the contract deliberately does not expose: the
// contract's own consistent read is [Snapshot], whose Screen is text and
// carries neither cells nor a caret (design §6.11).
//
// A read that fails answers no revision and no completeness: the caller has
// nothing to compose and a value from either side would be a claim about a
// screen nobody read.
func (s *Session) ReadScreen(read func(emulator.Terminal) error) (Revision, Completeness, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := read(s.emulator); err != nil {
		return 0, CompletenessUnknown, err
	}
	return s.rev, s.completeness, nil
}

// screenTextLocked reads the ACTIVE screen out of the emulator, as text.
//
// The format is this runtime's stand-in until the client's frame protocol
// declares one (design §6.11, epic nocx-zg3k3): one line per row of the grid
// the cursor moves in, each line's trailing blank cells dropped, and trailing
// blank rows dropped, so the text ends at the last row that carries a cell.
// The screen itself is not lost by that: a consumer that needs the grid reads
// the emulator's own rows (design §6.11's renderer takes cells, not text).
//
// Two things are load-bearing and are why this is not bytes.TrimSpace on a
// buffer: a WIDE cluster occupies two columns and only the first of them holds
// it — the second is its spacer and is skipped, so a row's text is what a
// person sees rather than what the grid stores — and a cell with no text is a
// space rather than a hole, because a row is a rectangle.
//
// A screen that cannot be read — a closed emulator — is no screen at all, and
// answers nil rather than a stale or partial one.
func (s *Session) screenTextLocked() []byte {
	geom, err := s.emulator.Geometry()
	if err != nil {
		return nil
	}
	rows := make([][]byte, 0, geom.Rows)
	for y := range geom.Rows {
		row, err := s.emulator.Row(y)
		if err != nil {
			return nil
		}
		line := make([]byte, 0, len(row.Cells))
		for _, cell := range row.Cells {
			switch cell.Width {
			case emulator.WidthSpacerTail, emulator.WidthSpacerHead:
				continue
			}
			if !cell.HasText {
				line = append(line, ' ')
				continue
			}
			line = append(line, cell.Grapheme...)
		}
		rows = append(rows, bytes.TrimRight(line, " "))
	}
	return bytes.TrimRight(bytes.Join(rows, []byte("\n")), "\n")
}

func (s *Session) tick() Revision {
	s.rev++
	return s.rev
}

func (s *Session) live() error {
	if s.avail != AvailabilityAvailable {
		return ErrUnavailable
	}
	return nil
}

// deliverReplyLocked is Ingest's, CommitGeometry's and repairLocked's ONE
// path for a reply the runtime itself decided to send: it used to be a
// direct write here (writeLocked, deleted with Execute — nocx-6q1uh.3), and
// is now a hand-off to whatever [ReplySink] is bound, so that the SAME
// ordered queue an intent's bytes go through also carries the program's own
// answers, and this package never blocks on a program not reading them.
//
// A nil replies is a construction bug this reports rather than silently
// discarding the program's answer: see [Config.Replies]'s note on when it
// may legitimately be nil (never after [SetReplies] has run, which the
// composition root guarantees before the first byte is read).
//
// [ErrReplyReserveFull] is recognised by value: the reserve overflowing means
// the program kept asking questions without reading its input, and this
// incarnation's completeness becomes [CompletenessLostIngest] and stays so
// (spec §5.5) — every later intent is then refused [ErrCompletenessUnknown]
// by [Commit], visibly, rather than the pane silently falling behind.
func (s *Session) deliverReplyLocked(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	if s.replies == nil {
		return fmt.Errorf("sessionruntime: a reply arrived with no ReplySink bound")
	}
	err := s.replies.Reply(p)
	if errors.Is(err, ErrReplyReserveFull) {
		s.completeness = CompletenessLostIngest
	}
	return err
}

// ----------------------------------------------------------------- control

// GrantControl makes p the directing principal and mints the next epoch. It is
// a TAKEOVER: every intent admitted under the epoch it replaces, and not yet
// executed, is cancelled, because the authority it was admitted under is gone.
func (s *Session) GrantControl(p Principal) (Control, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.live(); err != nil {
		return s.control, err
	}
	s.control = Control{Holder: p, Epoch: s.control.Epoch + 1}
	s.cancelSupersededLocked()
	s.tick()
	return s.control, nil
}

// RevokeControl ends the current grant without making anybody else the holder.
// The epoch still rises, so an intent carrying the superseded one is refused
// rather than applied late.
func (s *Session) RevokeControl() (Control, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.live(); err != nil {
		return s.control, err
	}
	s.control = Control{Holder: Principal{}, Epoch: s.control.Epoch + 1}
	s.cancelSupersededLocked()
	s.tick()
	return s.control, nil
}

// cancelSupersededLocked is written against the RECORDS and not the queue: the
// records are what outlive it (IntentState answers long after an intent left
// the queue), and an executed intent must keep saying so — bytes on a PTY
// cannot be recalled.
func (s *Session) cancelSupersededLocked() {
	for id := IntentID(1); id <= s.nextID; id++ {
		rec, ok := s.intents[id]
		if !ok || rec.intent.Under == s.control.Epoch {
			continue
		}
		if rec.state != IntentStateAdmitted {
			continue
		}
		rec.state = IntentStateCancelled
	}
	kept := s.queue[:0]
	for _, id := range s.queue {
		if s.intents[id].intent.Under == s.control.Epoch {
			kept = append(kept, id)
		}
	}
	s.queue = kept
}

// ------------------------------------------------------------------- input

// Admit takes an intent into the ordered queue, or refuses it. What it returns
// names something that has NOT reached the PTY.
func (s *Session) Admit(i Intent) (IntentID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.live(); err != nil {
		return 0, err
	}
	if i.At != s.inc {
		return 0, ErrStaleIncarnation
	}
	if s.control.Holder.Kind == PrincipalNone {
		return 0, ErrNoController
	}
	if i.Under != s.control.Epoch {
		return 0, ErrStaleControlEpoch
	}
	s.nextID++
	i.ID = s.nextID
	s.intents[i.ID] = &intentRecord{intent: i, state: IntentStateAdmitted}
	s.queue = append(s.queue, i.ID)
	s.tick()
	return i.ID, nil
}

// Commit is Execute's replacement (nocx-6q1uh.3, spec §5.1, §5.3): it
// revalidates the incarnation, the control epoch and the precondition of the
// intent at the head of the admitted queue, runs check against a fresh
// snapshot — still under s.mu, which is where a token's digest and an access
// epoch are judged (internal/helper/session, nocx-6q1uh.4) — encodes the
// intent against the modes the PROGRAM set, and returns the encoded bytes.
//
// It writes NOTHING. The caller — the session's I/O owner — hands Encoded to
// its writer and assigns it a Fence; that hand-off, not this call, is the
// linearisation point spec §5.3 names. Marking the intent Executed here is
// therefore provisional, on the case that dominates: the write that follows
// succeeds. A caller for whom it did not calls [Session.ReportOutcome] to
// correct the record — see that method's doc for why Commit cannot make this
// distinction itself.
//
// i names which intent the caller believes is at the head, and a non-zero
// i.ID that does not match is refused: the owner's own queue is the one
// place ordering is decided, so a mismatch here is a defect in the CALLER's
// bookkeeping, not evidence about the terminal. i.ID == 0 skips the check —
// the shape a caller with no queue of its own (a schedule constructing a
// bare Intent) uses when it already knows only one thing can be at the head.
func (s *Session) Commit(i Intent, check func(Snapshot) error) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.live(); err != nil {
		return nil, err
	}
	// The gate is every state that is not [CompletenessComplete], not only
	// [CompletenessUnknown]: a digest taken against the screen is only as
	// good as the claim that the screen is the WHOLE of what the program
	// wrote, and three other states say it is not. LostIngest and Evicted
	// are the direct case — bytes this incarnation once had are gone, by
	// loss or by retention, and the emulator's screen was built from less
	// than the program actually sent. NoFence is subtler: ingest was whole,
	// but the interval has no authenticated boundary, so nothing here can
	// tell a legitimate reply from a coincidence at the same bytes — the
	// same "cannot validate it" reasoning spec §5.5 gives for the reserve
	// overflow. All four collapse to the one refusal a caller already
	// checks for (spec §5.5: overflow is reported as `completeness_unknown`
	// too), so this returns [ErrCompletenessUnknown] rather than minting a
	// second sentinel for the same decision.
	if s.completeness != CompletenessComplete {
		return nil, ErrCompletenessUnknown
	}
	if len(s.queue) == 0 {
		return nil, ErrNothingAdmitted
	}
	id := s.queue[0]
	rec := s.intents[id]
	if i.ID != 0 && i.ID != id {
		return nil, fmt.Errorf("sessionruntime: commit named intent %d, the head is %d", i.ID, id)
	}
	s.queue = s.queue[1:]

	if rec.intent.At != s.inc {
		rec.state = IntentStateRefused
		return nil, ErrStaleIncarnation
	}
	if rec.intent.Under != s.control.Epoch {
		rec.state = IntentStateCancelled
		return nil, ErrStaleControlEpoch
	}
	if p := rec.intent.Precondition; p != nil {
		if p.Digest != sha256.Sum256(s.screenTextLocked()) {
			rec.state = IntentStateRefused
			return nil, ErrPreconditionStale
		}
	}
	if check != nil {
		if err := check(s.snapshotLocked()); err != nil {
			rec.state = IntentStateRefused
			return nil, err
		}
	}

	encoded, err := s.encode(rec.intent)
	if err != nil {
		// An intent the terminal cannot encode is REFUSED and consumed: it was
		// never written, and leaving it queued would write it later against
		// evidence nobody has re-checked.
		rec.state = IntentStateRefused
		return nil, err
	}
	rec.state = IntentStateExecuted
	s.tick()
	return encoded, nil
}

// ReportOutcome corrects an intent's record with what its write actually did,
// once the caller has attempted it — [Commit] cannot know this, because it
// never writes (that is the whole of what moved to the caller). A nil err
// leaves the record exactly as Commit left it (Executed); any other value
// means the write did not land whole, and the record becomes
// [IntentStateFailed] — never Cancelled: a write can fail PART-WAY, and
// bytes it did take are beyond recall, which is the same distinction Execute
// used to draw at its own write site.
//
// It is a method of the concrete type rather than of [Runtime] because
// nothing generic over the contract needs it: a schedule that wants to
// exercise a failing write attaches its own terminal instrument and reads
// the failure off THAT, the way this package's own tests do (a real runtime
// has no test double standing between it and the terminal it was
// constructed over).
func (s *Session) ReportOutcome(id IntentID, writeErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if writeErr == nil {
		return
	}
	if rec, ok := s.intents[id]; ok && rec.state == IntentStateExecuted {
		rec.state = IntentStateFailed
	}
}

// encode is where an intent becomes bytes, against the modes the program set.
// Nothing here decides what a key is worth on its own account: the emulator
// owns the modes, so a program that turned on application cursor keys gets
// one sequence and a program that did not gets another, with nothing in the
// caller's intent saying which (ADR-0066, AD-1 as amended).
func (s *Session) encode(i Intent) ([]byte, error) {
	switch i.Kind {
	case IntentKindKey:
		ev, err := keyEvent(i.Payload)
		if err != nil {
			return nil, err
		}
		return s.emulator.EncodeKey(ev)
	case IntentKindText:
		return append([]byte(nil), i.Payload...), nil
	case IntentKindPaste:
		return s.emulator.Paste(i.Payload)
	case IntentKindMouse:
		ev, err := mouseEvent(i.Payload)
		if err != nil {
			return nil, err
		}
		return s.emulator.Mouse(ev)
	case IntentKindFocus:
		gained, err := focusEvent(i.Payload)
		if err != nil {
			return nil, err
		}
		return s.emulator.Focus(gained)
	default:
		return nil, fmt.Errorf("%w: intent kind %d", ErrIntentUnsupported, i.Kind)
	}
}

// ---------------------------------------------------------------- geometry

// ReportGeometry is the client saying what it can show. It is a report and not
// an instruction: the commit in force does not move, because a runtime that
// treated the report as the commit would have two owners for one size.
func (s *Session) ReportGeometry(g Geometry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.live(); err != nil {
		return err
	}
	if !g.Valid() {
		return ErrGeometryInvalid
	}
	s.reported = g
	return nil
}

// CommitGeometry applies a decided size to the terminal and the emulator, and
// PUBLISHES the commit only once both have taken it.
//
// The calls are not atomic and cannot be made so. A resize has effects outside
// them — the terminal is resized and the program receives SIGWINCH — and a
// signal already delivered cannot be recalled by anything this method does
// next. So the rule is stated where it can be kept: a refused attempt
// publishes nothing and the commit in force stands, and the side that took a
// size nobody committed is put BACK to the commit in force rather than left
// disagreeing with the other about every cell after a column.
//
// Replies are written as each side produces them, and that is not the same
// decision as the commit: a terminal that has taken a size answers the
// program's in-band size query (mode 2048) about the size it took, and a
// refusal by the emulator afterwards cannot un-tell the program what its
// terminal already did.
func (s *Session) CommitGeometry(g Geometry) (GeometryCommit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.live(); err != nil {
		return s.geom, err
	}
	if !g.Valid() {
		return s.geom, ErrGeometryInvalid
	}
	if err := s.terminal.Resize(g); err != nil {
		// The terminal's own refusal changed nothing, so there is nothing to
		// put back and the commit in force simply stands.
		return s.geom, err
	}
	replies, err := s.emulator.Resize(g)
	if err != nil {
		// The terminal already took a size that is not going to be committed.
		return s.geom, errors.Join(err, s.repairLocked())
	}
	if writeErr := s.deliverReplyLocked(replies); writeErr != nil {
		// Both sides took it, but the program's own report of the new size
		// never reached it: the attempt did not complete, so it does not
		// publish, and the sides go back to the commit that stands.
		return s.geom, errors.Join(writeErr, s.repairLocked())
	}
	s.geom = GeometryCommit{Geometry: g, Revision: s.tick()}
	// A resize moved the screen, so it publishes at the revision the commit
	// opened: consumers are painting, and a reflow they never hear about is
	// a screen held stale until the next byte arrives.
	if err := s.publishFrameLocked(); err != nil {
		return s.geom, err
	}
	return s.geom, nil
}

// repairLocked puts BOTH sides back to the commit in force, and it is the
// other half of the rule [GeometryCommit] states: no side may be left at a
// size nobody committed. It is called on every path where the attempt did not
// open a commit, and it asks both sides rather than the one believed to be
// wrong, because the call is idempotent and "which side moved" is exactly the
// reasoning that goes stale.
//
// What it cannot do is un-resize the PROGRAM: the size the terminal took is
// delivered as SIGWINCH, and a signal already delivered is a fact about the
// past. What it can do, and does, is make what is running and what the session
// describes one size again.
func (s *Session) repairLocked() error {
	var failed []error
	if err := s.terminal.Resize(s.geom.Geometry); err != nil {
		failed = append(failed, err)
	}
	if replies, err := s.emulator.Resize(s.geom.Geometry); err != nil {
		failed = append(failed, err)
	} else if writeErr := s.deliverReplyLocked(replies); writeErr != nil {
		failed = append(failed, writeErr)
	}
	return errors.Join(failed...)
}

// ------------------------------------------------------------------ output

// Ingest feeds the program's output into the emulator, answers whatever the
// program asked for on the same ordered write path, and emits what the stream
// produced: the non-visual effects the program asked for, and one frame at the
// revision the screen moved to.
//
// The runtime's own account for this call is linear in the BYTES it was handed
// — one work unit each — and never in a count inside them: `CSI 1000000000 b`
// costs what its sixteen bytes cost here, and the expansion (and its clamp) is
// the emulator's (ADR-0065).
//
// b is fed to the emulator in one or more sub-feeds, split at each complete
// fence marker's own end (fence_split.go, nocx-2v80t.3.12): a pty read often
// hands this call the fence AND the bytes the shell's own PROMPT_COMMAND
// writes right after it (133;D, 133;A, OSC 7, the visible PS1 text) in one
// feed, with nothing to flush in between. Feeding the whole thing before
// ever reading the screen would make the fence's own closing screen — the
// boundary window the next interval is judged against — read whatever the
// NEXT command's prompt had already drawn into it. Splitting costs nothing
// when no fence is present: the loop below runs its body exactly once, over
// the whole of b, and behaves exactly as it did before this split existed.
func (s *Session) Ingest(b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.live(); err != nil {
		return err
	}
	if len(b) > MaxIngestBytes {
		// A REFUSAL and not a truncation: nothing was ingested, no work was
		// charged, and the caller still holds every byte.
		return ErrIngestTooLarge
	}
	s.ingestWork += uint64(len(b))
	// The interval in flight opens BEFORE the feed that triggered it is
	// applied, so its opening screen is the screen the interval began on and
	// carries none of the output that opened it (observation.go).
	s.openObservationLocked()

	var replyErr error
	for rest := b; len(rest) > 0; {
		chunk := rest
		if end, ok := nextMarkerSplit(rest); ok {
			chunk = rest[:end]
		}
		rest = rest[len(chunk):]

		replies, err := s.emulator.Ingest(chunk)
		if err != nil {
			// The emulator refused bytes it was handed, so what it holds is
			// not the whole stream and no attach may be told otherwise: the
			// session says so, in the same breath as reporting the failure.
			s.completeness = CompletenessLostIngest
			return err
		}
		// The program's own answer is handed to the reply sink on the
		// ordered path. A failure here is reported, and it does NOT swallow
		// what the stream produced: the bytes reached the emulator, so the
		// effects the program asked for and the frame the screen moved to
		// are owed to the consumer either way. An effect dropped because a
		// reply could not be delivered is a bell that rings nowhere and is
		// never reported as lost. Later sub-feeds overwrite an earlier
		// reply failure the same way one combined call always would have —
		// this is one Ingest, and the caller reads one answer for it.
		if rerr := s.deliverReplyLocked(replies); rerr != nil {
			replyErr = rerr
		}
		// The departure report is drained BEFORE the effects are read,
		// because one of them may carry the fence that seals this record: a
		// feed's own departures belong to the interval that feed belongs to
		// (observation.go). Draining per sub-feed, in the same order the
		// bytes arrived, is what keeps this true when a fence split b: the
		// rows THIS sub-feed departed stream before the end marker its own
		// fence emits, exactly as they would have for one combined feed.
		s.drainObservationLocked(len(chunk))
		for _, e := range s.emulator.Effects() {
			if e.Kind == emulator.EffectFence {
				// The join: a fence the emulator drained LOCATES the
				// authenticated half of the rendezvous (ADR-0024 decision 1)
				// and is not a consumer payload, so it never reaches
				// effectKindOf or the delivery path below — a kind the
				// delivery vocabulary does not know is refused there, and a
				// fenced command must not fail the ingest that carried it.
				// Reached with nothing past the fence's own bytes fed yet
				// (the split above), so the screen read here is the screen
				// exactly as the fence left it.
				s.sightDrainedFenceLocked(e)
				continue
			}
			if e.Kind == emulator.EffectOutputMark {
				// Same reasoning as the fence: a sighted mark locates rather
				// than authorises (ADR-0024 decision 1), is not a consumer
				// payload, and reaching here with nothing past its own bytes
				// fed yet (the split above) is what lets the screen read
				// inside it be the screen exactly as THIS mark left it.
				s.sightOutputMarkLocked()
				continue
			}
			if e.Kind == emulator.EffectClearBoundary {
				// Unlike the fence and the output mark this is not a
				// rendezvous with an authenticated event — ED3 is a real VT
				// fact the emulator's own state already reflects (ADR-0066),
				// so there is nothing to authenticate and nothing to attach
				// to an attempt. It is not a consumer payload either (a
				// program cannot ring the notify bell or set a title this
				// way), so it never reaches effectKindOf: the vocabulary
				// there refuses a kind it does not know, and an erase must
				// not fail the ingest that carried it.
				s.sightClearBoundaryLocked()
				continue
			}
			s.nextEffect++
			effect := Effect{
				ID:   s.nextEffect,
				At:   s.inc,
				Kind: effectKindOf(e.Kind),
				Body: e.Body,
			}
			if err := s.deliverLocked(effectDelivery(effect)); err != nil {
				return err
			}
		}
	}
	s.tick()
	if err := s.publishFrameLocked(); err != nil {
		return err
	}
	return replyErr
}

// effectKindOf maps the emulator's vocabulary onto the contract's. The two
// exist because the port may not import this package (ADR-0065 point 3, and
// surface_test.go keeps it true), so the kinds are converted by NAME in the
// one place that owns delivery — and a kind this table does not know is
// [EffectNone], which the delivery path REFUSES rather than delivering under a
// guess.
func effectKindOf(k emulator.EffectKind) EffectKind {
	switch k {
	case emulator.EffectBell:
		return EffectBell
	case emulator.EffectNotification:
		return EffectNotification
	case emulator.EffectClipboard:
		return EffectClipboard
	case emulator.EffectTitle:
		return EffectTitle
	case emulator.EffectCwdReport:
		return EffectCwdReport
	default:
		return EffectNone
	}
}

// ReportHole says bytes were lost before the runtime saw them. An emulator fed
// only the surviving suffix is not authoritative, and the runtime says so
// rather than serving a screen that looks whole.
func (s *Session) ReportHole(lost uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.live(); err != nil {
		return err
	}
	if lost == 0 {
		return nil
	}
	// The count is the ingest path's own record of what it discarded, and a
	// hole the carrier reported is output this session never saw: it belongs
	// in the same number for the same reason.
	s.ingestLost += lost
	// The record in flight counts the hole beside the session: bytes that
	// never reached the emulator never became rows, so bytes are the honest
	// unit here and the session's completeness above is what the record's own
	// claim will fold down from (observation.go).
	if s.observation != nil {
		s.observation.Loss.IngestLostBytes += lost
		s.obsCarried += lost
	}
	s.completeness = CompletenessLostIngest
	s.tick()
	return nil
}

// --------------------------------------------------------------- rendezvous

// rendezvousEntry is one tracked meeting: its record, and — when a fence
// sighted it with nothing authenticated behind it — the boundary capture
// that sighting took. No wait is armed on it and no timer exists in the
// rendezvous at all (nocx-2v80t.3.9): a meeting left with one half missing
// is settled by an EVENT — the next interval's start, a second completion,
// or the session's end (observation.go) — or by the contract's own call to
// [Session.ExpireRendezvous], never by a duration elapsing.
type rendezvousEntry struct {
	Rendezvous
	// captured is what a PARKING sighting took at the fence's instant: the
	// interval record's content up to the fence, and the screen as the fence
	// sat on it. The boundary is where the fence sits in the byte stream, so
	// the capture detaches from the interval in flight — the output that
	// follows the fence starts the next record's content — and the join at
	// [Session.Completed] seals the capture rather than re-reading a screen
	// the stream has moved past. A sighting nobody authenticated returns its
	// capture to the interval in flight (observation.go). Nil on every other
	// entry; bounded by [MaxPendingRendezvous] with the set, since it lives
	// on the entry.
	captured *observationCapture
}

// pending reports whether the meeting is still waiting for one of its halves.
func (e *rendezvousEntry) pending() bool {
	return e.State == RendezvousAwaitingSighting || e.State == RendezvousAwaitingAuthenticated
}

// SealEnvironmentEntry seals the interval in flight at an authenticated
// environment entry (nocx-2v80t.3.21), or answers ctx's error and seals
// nothing when the caller gave up before the seal could be taken
// (nocx-2v80t.3.31): the coordinator's kernel accepted a
// confirmed environment change — a nested domain taking the lane, which
// abandons whatever local attempt was running under it (ADR-0024 §5,
// internal/lifecycle's applySuspend) — while this session's command was
// still open. The owner's decision is that this ends the local command's
// interval exactly as its own end marker would: the screen at the entry,
// bounded the same way as any other closing screen (closingRowsForStream,
// outputMarkSkipLocked), is appended and the block is sealed.
//
// There is no fence for this boundary — the shell that would have printed
// one is no longer the one holding the terminal — so the record seals with
// the ZERO nonce, which an ordinary command's fence never is (32
// cryptographically random bytes) and which the row stream's consumer reads
// as "no fence to match, close whichever interval is open" rather than
// hunting for a nonexistent authenticated completion.
//
// Like every other public entry point here this is an EVENT, never a timer
// (ADR-0074): it fires once, when the coordinator's kernel accepts the fact,
// and once per entry — a second delivery of the same entry, named by the
// child domain that made it, seals nothing (nocx-2v80t.3.28).
// at is judged exactly as [Session.Completed] judges it: a session that is
// not available, or a caller naming a generation this runtime is not, is
// refused rather than applied late — the same stale-sender guard, because a
// replaced coordinator asking a runtime to seal an incarnation it left
// behind is exactly what that guard exists for. An interval already parked
// on a completion whose own fence never arrived is settled here too, with no
// closing screen (ADR-0074's "the next event... seals it"), before the
// interval that follows — the one an environment entry actually ends — seals
// with its own.
func (s *Session) SealEnvironmentEntry(ctx context.Context, at Incarnation, entry EnvironmentEntryID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.avail != AvailabilityAvailable || at != s.inc {
		return nil
	}
	// A caller that has given up — its attempt timed out and was cancelled,
	// or its transport ended — has already sent, or will send, the retry
	// that stands for it; a handler scheduled only now must change nothing
	// (nocx-2v80t.3.31). Judged here, under the lock the seal itself takes,
	// so nothing can slip between the check and the seal, and before the
	// entry's id is spent, so the retry that stands for it still seals. The
	// dedupe below is what catches a retry of an attempt that DID land; it
	// no longer has to catch a late one, so its bounded memory is never the
	// thing standing between an abandoned delivery and a second seal.
	if err := ctx.Err(); err != nil {
		return err
	}
	// The same entry a second time is the same event again — a delivery
	// retried after an attempt that timed out but landed — and seals
	// nothing: the interval it ended is already sealed, and the one in
	// flight now is the child's, which no entry of this id ends
	// (nocx-2v80t.3.28). Keyed by the entry, never by elapsed time.
	if slices.Contains(s.entriesSealed, entry) {
		return nil
	}
	s.entriesSealed = append(s.entriesSealed, entry)
	if excess := len(s.entriesSealed) - MaxRememberedEnvironmentEntries; excess > 0 {
		s.entriesSealed = slices.Delete(s.entriesSealed, 0, excess)
	}
	s.settlePendingLocked(FenceNonce{})
	s.tick()
	s.sealObservationLocked(FenceNonce{})
	return nil
}

// Completed is the authenticated half arriving. It authenticates nothing: the
// caller has already validated the protocol version, the epoch, the capability
// and the sequence rule (internal/lifecycle's), and this method does not
// become a second gate on any of it.
func (s *Session) Completed(at Incarnation, nonce FenceNonce, _ int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.avail != AvailabilityAvailable || at != s.inc {
		return
	}
	// One of the events that settles a parked interval: a completion for a
	// DIFFERENT nonce means the interval before it is done and its fence's
	// sighting never arrived, so that record seals here, with no closing
	// screen and no fence (observation.go). A completion for the parked
	// nonce is that very interval's own join and settles nothing.
	s.settlePendingLocked(nonce)
	if e := s.rendezvous[nonce]; e != nil {
		// The meeting is already tracked. Only a parked sighting with THIS
		// nonce is the half this completion closes; a duplicate completion,
		// or one arriving after the meeting settled, is the same event
		// again and changes nothing — a sighting may authorise nothing, and
		// neither may a late second half reopen what the event settle
		// honestly closed.
		if e.State == RendezvousAwaitingAuthenticated {
			e.State = RendezvousComplete
			s.rendezvousLatest, s.rendezvousHasLatest = nonce, true
			s.tick()
			// The authenticated boundary just closed. In the fence-first
			// order the sighting captured the record AT the fence — the
			// boundary this completion is authenticating — so the capture
			// seals, and the interval in flight, rebased at the fence, is
			// already the next record. Under the same lock the join holds.
			//
			// e.captured is set at the ONE call site that ever puts an entry
			// into RendezvousAwaitingAuthenticated (sightDrainedFenceLocked,
			// via splitObservationAtFenceLocked, which always returns a
			// non-nil *observationCapture) and is consumed only here, so by
			// the time this branch runs it can never be nil — asserted
			// rather than silently routed around a nil that this codebase
			// had already proven impossible (stage review nocx-2v80t.3.15,
			// finding 2: the prior `else` branch was live code with no path
			// that could ever reach it).
			if e.captured == nil {
				panic("sessionruntime: rendezvous awaiting-authenticated with no captured observation")
			}
			s.sealObservationFromCaptureLocked(nonce, e.captured)
			e.captured = nil
		}
		return
	}
	// A completion with no meeting parked: admit it as the authenticated
	// half waiting for its sighting. The screen is deliberately not read
	// here — the next command may already have printed — so the interval
	// parks in the observation and its seal waits for the fence's sighting,
	// which is in the ordered stream and arrives in the ordinary case. It is
	// settled by an event, never a timer (nocx-2v80t.3.9).
	parked := &rendezvousEntry{Rendezvous: Rendezvous{
		State: RendezvousAwaitingSighting,
		Nonce: nonce,
		At:    at,
	}}
	if !s.admitRendezvousLocked(parked, true) {
		return
	}
	// An interval whose screen and row window were taken at ANOTHER fence's
	// sighting belongs to that fence's command. Parking this half on it would
	// give one record two overlapping spans, and the stale half's end marker
	// arrives behind rows the interval already streamed (nocx-2v80t.3.9). The
	// meeting is admitted and NOT parked: if its own sighting never arrives,
	// the settle degrades completeness and invents no record, which is the
	// honest answer for a boundary nobody saw.
	if o := s.observation; o != nil && o.Rebased != (FenceNonce{}) && o.Rebased != nonce {
		return
	}
	s.parkObservationLocked(nonce)
}

// admitRendezvousLocked inserts a new meeting into the set, keeping it within
// [MaxPendingRendezvous], and answers whether it was admitted.
//
// authenticated says an authenticated half is behind the insert, and the
// order the bound spends slots in is the order of what each one is worth.
// The oldest SETTLED meeting goes first — it is record, not authority, and
// evicting it costs nothing. Then, for a completion only, the oldest parked
// SIGHTING: a fence with nothing authenticated behind it authorised nothing
// (ADR-0024 decision 1), so a flood of them can never be the reason a real
// completion is refused. A SIGHTING that finds no room is refused there, and
// that refusal is free for the same reason.
//
// What is left is a set every slot of which holds an authenticated half
// still waiting for its fence, and another authenticated half arriving —
// nine commands completed before the emulator drew any of them. Something
// must give, and the one thing that may not is silence: the newest
// completion takes the OLDEST one's slot, because the oldest is the one
// whose fence is least likely still coming, and the boundary that left
// unmet makes completeness [CompletenessNoFence]. So an authenticated
// insert is never refused, and an authenticated boundary is either tracked
// or declared lost.
func (s *Session) admitRendezvousLocked(e *rendezvousEntry, authenticated bool) bool {
	if len(s.rendezvous) >= MaxPendingRendezvous {
		switch {
		case s.evictRendezvousLocked(func(e *rendezvousEntry) bool { return !e.pending() }):
		case !authenticated:
			return false
		case s.evictRendezvousLocked(func(e *rendezvousEntry) bool {
			return e.State == RendezvousAwaitingAuthenticated
		}):
		case s.evictRendezvousLocked(func(e *rendezvousEntry) bool {
			return e.State == RendezvousAwaitingSighting
		}):
			if s.completeness == CompletenessComplete {
				s.completeness = CompletenessNoFence
			}
		default:
			return false
		}
	}
	s.rendezvous[e.Nonce] = e
	s.rendezvousOrder = append(s.rendezvousOrder, e.Nonce)
	s.rendezvousLatest, s.rendezvousHasLatest = e.Nonce, true
	s.tick()
	return true
}

// evictRendezvousLocked removes the OLDEST meeting satisfying keep and
// answers whether one was found.
//
// It is called only from [Session.admitRendezvousLocked], and only to make
// room for a meeting that is inserted immediately afterwards — so if the
// evicted meeting was the one rendezvousLatest named, the insert re-points
// the window before the lock is released. The window can never dangle here,
// and Rendezvous can never answer idle about a set that is not empty.
//
// An eviction is an event like the ones that settle a parked interval
// (observation.go): an authenticated completion whose fence never arrived has
// an interval in flight PARKED on it, and dropping the meeting without
// settling that record would leak a record nothing could seal afterwards. So
// the settle runs here, after the meeting is out of the set, for the same
// reason the next interval's start and the session's end settle it.
func (s *Session) evictRendezvousLocked(keep func(*rendezvousEntry) bool) bool {
	for i, nonce := range s.rendezvousOrder {
		if e := s.rendezvous[nonce]; e != nil && keep(e) {
			if e.captured != nil {
				// Evicted before it authenticated: its capture was never a
				// boundary either, and goes back the same way (observation.go).
				s.returnObservationCaptureLocked(e.captured)
				e.captured = nil
			}
			delete(s.rendezvous, nonce)
			s.rendezvousOrder = append(s.rendezvousOrder[:i], s.rendezvousOrder[i+1:]...)
			s.settleParkedLocked(nonce)
			return true
		}
	}
	return false
}

// SightFence reports that the emulator drew a fence, and pins the content it
// was drawn over. A sighted marker LOCATES an already-authenticated event and
// never authorises one (ADR-0024 decision 1), so a fence the program printed
// itself parks and grants nothing.
func (s *Session) SightFence(nonce FenceNonce, source []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sightFenceLocked(nonce, source)
}

// sightFenceLocked is [Session.SightFence] with the lock already held: the
// ingest path joins a drained fence to the rendezvous from inside its own
// critical section, and a call to the exported form there would wait on the
// mutex it is holding.
func (s *Session) sightFenceLocked(nonce FenceNonce, source []byte) error {
	if err := s.live(); err != nil {
		return err
	}
	// A fence's sighting is an interval's start as much as a join: the fence
	// belongs to the command that just ended, so a sighting for a different
	// nonce than the parked one means the parked interval's own fence never
	// arrived and its record seals here (observation.go). A sighting for the
	// parked nonce IS that interval's join and settles nothing.
	s.settlePendingLocked(nonce)
	if e := s.rendezvous[nonce]; e != nil {
		switch e.State {
		case RendezvousAwaitingSighting:
			// The matching sighting is the half that was missing.
			e.State = RendezvousComplete
			e.SightedAt = s.rev
			e.PinnedSource = append([]byte(nil), source...)
			s.rendezvousLatest, s.rendezvousHasLatest = nonce, true
			s.tick()
			// The join at the sighting: same boundary, same seal, whichever
			// half arrived last (observation.go).
			s.sealObservationLocked(nonce)
			return nil
		case RendezvousAwaitingAuthenticated:
			// The same fence seen again refreshes the locate: the freshest
			// content is the capture source. The wait is deliberately NOT
			// re-armed — a flood of repeated fences must not extend a
			// meeting's life without bound.
			e.SightedAt = s.rev
			e.PinnedSource = append([]byte(nil), source...)
			s.rendezvousLatest, s.rendezvousHasLatest = nonce, true
			s.tick()
			return nil
		default:
			// A settled meeting stays settled. A sighting locates, never
			// authorises or reopens (ADR-0024 decision 1), so this fence
			// located nothing that was waiting and is dropped.
			return nil
		}
	}
	// A fence with nothing authenticated behind it parks and grants nothing
	// (ADR-0024 decision 1). At the bound it is REFUSED rather than evicting
	// anybody: it authorises nothing, so refusing it costs nothing. Once it
	// is admitted, the sighting takes the boundary's capture — the record
	// content up to here, the screen as the fence sits on it — and the
	// interval in flight is rebased to start its next record here, so the
	// output that follows the fence never lands in the record this boundary
	// will seal (observation.go).
	entry := &rendezvousEntry{Rendezvous: Rendezvous{
		State:        RendezvousAwaitingAuthenticated,
		Nonce:        nonce,
		At:           s.inc,
		SightedAt:    s.rev,
		PinnedSource: append([]byte(nil), source...),
	}}
	if !s.admitRendezvousLocked(entry, false) {
		return ErrRendezvousFull
	}
	entry.captured = s.splitObservationAtFenceLocked(nonce)
	return nil
}

// sightDrainedFenceLocked joins ONE fence effect the emulator drained to the
// rendezvous: the join the design names (§6.4), exercised on a real pty by
// realpty_rendezvous_test.go. The nonce the stream carried is 64 hex
// characters the emulator passed through undecoded; decoding it is the
// consumer's job, and a body that is not exactly a [FenceNonce] in hex cannot
// match any completion — sighting it would be manufacturing a rendezvous
// nobody authenticated, so the fence is dropped.
//
// A fence the set declines — undecodable, one for a meeting already settled,
// or one refused at the bound ([ErrRendezvousFull]) — is dropped: it located
// nothing and authorised nothing, the meetings already tracked keep waiting
// untouched, and neither is a reason the INGEST failed — the bytes reached
// the emulator and the screen moved, which is the account Ingest returns.
func (s *Session) sightDrainedFenceLocked(e emulator.Effect) {
	nonce, ok := fenceNonceOf(e.Body)
	if !ok {
		return
	}
	_ = s.sightFenceLocked(nonce, e.Source)
}

func fenceNonceOf(body []byte) (FenceNonce, bool) {
	var nonce FenceNonce
	raw, err := hex.DecodeString(string(body))
	if err != nil || len(raw) != len(nonce) {
		return nonce, false
	}
	copy(nonce[:], raw)
	return nonce, true
}

// ExpireRendezvous settles the meeting the nonce names, as the event that
// stands in for a fence's sighting that never arrived. It is a CALL and not
// a wait — no timer exists anywhere in the rendezvous (nocx-2v80t.3.9) — so
// the contract exercises the settle without depending on a duration. An
// AUTHENTICATED interval whose fence never arrived may still be worth
// keeping; it may not be described as the command's complete output. A
// sighted fence with nothing authenticated behind it settles into nothing at
// all.
//
// The two pending states are not the same thing and do not settle the same
// way. AwaitingSighting is an AUTHENTICATED completion whose fence never
// arrived: the interval in flight is parked on it, its record seals with NO
// closing screen at the count the completion measured
// (settleParkedLocked, observation.go), and completeness becomes
// [CompletenessNoFence] because an authenticated boundary went unmet.
// AwaitingAuthenticated is a sighting with NOTHING authenticated behind it:
// it authorised nothing while it waited (ADR-0024 decision 1), so settling it
// drops the pinned source, returns the capture it took, and changes NOTHING
// ELSE — not completeness, not write authority. Unauthenticated output can
// therefore never revoke the person's ability to type, for however long the
// session lives.
func (s *Session) ExpireRendezvous(nonce FenceNonce) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.live(); err != nil {
		return err
	}
	e := s.rendezvous[nonce]
	if e == nil || !e.pending() {
		return ErrNoRendezvous
	}
	if e.State == RendezvousAwaitingSighting {
		// The interval in flight may be PARKED on this meeting, and then the
		// settle is the record's too: the meeting reads expired, completeness
		// goes no-fence, and the record seals with no closing screen at the
		// count the completion measured (settleParkedLocked, observation.go).
		if s.settleParkedLocked(nonce) {
			s.tick()
			return nil
		}
		// Nothing is parked under this nonce: the settle is the state alone.
		// The authenticated boundary still went unmet, so the session's own
		// claim about its stream is no longer complete, and no record is
		// invented for an interval that was never parked.
		e.State = RendezvousExpired
		e.PinnedSource = nil
		if s.completeness == CompletenessComplete {
			s.completeness = CompletenessNoFence
		}
		s.rendezvousLatest, s.rendezvousHasLatest = nonce, true
		s.tick()
		return nil
	}
	// A sighting with NOTHING authenticated behind it authorised nothing
	// while it waited (ADR-0024 decision 1), so settling it drops the pinned
	// source and returns the capture it took — and changes NOTHING ELSE, not
	// completeness and not write authority. Unauthenticated output can never
	// revoke the person's ability to type, for however long the session lives.
	e.State = RendezvousExpired
	e.PinnedSource = nil
	if e.captured != nil {
		s.returnObservationCaptureLocked(e.captured)
		e.captured = nil
	}
	s.rendezvousLatest, s.rendezvousHasLatest = nonce, true
	s.tick()
	return nil
}

// ------------------------------------------------------------------ failure

// Fail ends the runtime. Its session becomes unavailable and writes are
// revoked, and NOTHING is adopted: a surviving process under an invented
// terminal state is not recovery, and transparent recovery needs complete
// emulator checkpoints including pending parser state (nocx-ygxjv.3).
func (s *Session) Fail(cause string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The session's end is one of the events that settles a parked interval
	// (observation.go): a completion whose fence never arrived leaves a
	// record nothing else could seal, and this is the last event there is.
	// It runs before the refusal below so that a second Fail is not the
	// reason a parked record leaks.
	s.settlePendingLocked(FenceNonce{})
	if s.avail == AvailabilityUnavailable {
		return ErrUnavailable
	}
	s.avail = AvailabilityUnavailable
	s.cause = cause
	for _, id := range s.queue {
		if rec := s.intents[id]; rec != nil && rec.state == IntentStateAdmitted {
			rec.state = IntentStateCancelled
		}
	}
	s.queue = nil
	s.control = Control{Holder: Principal{}, Epoch: s.control.Epoch + 1}
	s.tick()
	return nil
}

// ---------------------------------------------------------------- delivery

// queued is one thing the runtime hands a consumer, as the union its class
// types it. Its CLASS decides where it goes and the producer decides the
// class. (The reference model calls its own fixture by another name — this one
// is the runtime's, and the two never meet.)
type queued struct {
	class  DeliveryClass
	rev    Revision
	bytes  []byte
	effect Effect
}

// effectDelivery takes a COPY of the effect's body, for the same reason
// Ingest copies the emulator's: what the runtime holds must not be memory
// somebody else still has a name for. A producer that handed over a slice and
// then reused its buffer would otherwise rewrite a bell that has already been
// delivered, and the identity the duplicate policy is stated over would name
// different bytes at different times.
func effectDelivery(e Effect) queued {
	e.Body = bytes.Clone(e.Body)
	return queued{class: deliveryClassOf(e.Kind), effect: e}
}

// deliveryClassOf is the ONE place an effect's class is decided, exhaustively:
// a kind added to the vocabulary without being named here falls to
// [DeliveryUnclassified] and is REFUSED rather than delivered under a guess.
func deliveryClassOf(k EffectKind) DeliveryClass {
	switch k {
	case EffectBell, EffectNotification, EffectClipboard, EffectTitle, EffectCwdReport:
		return DeliveryAtMostOnce
	default:
		return DeliveryUnclassified
	}
}

// subscriber is one consumer: the queue the runtime keeps FOR it, and the
// counts that tell it what it lost. Every read is on the contract's Consumer
// interface; the queue itself is nobody's but the runtime's.
//
// # It reads under the SESSION's lock, and that is not decoration
//
// The runtime fills a queue from its ingest path and empties it from its
// delivery side, both under Session.mu, while the consumer's own reads are
// made by whoever holds the consumer — a client, a schedule — on another
// goroutine. So every exported read below takes the same lock the writer
// takes, and the runtime's own internal uses are the *Locked forms that assume
// it is already held. A reader that reached into the queue without it would be
// reading memory another goroutine appends to, which is a torn queue rather
// than a stale one.
//
// The lock order is one-way by construction: a subscriber never calls into its
// session, so nothing can invert it.
type subscriber struct {
	owner *sync.Mutex

	// account and allowance are the delivery budget this queue draws on,
	// named at attach because [Session.enqueueLocked] spends from the
	// session's account and [Take] must refund the very same one. The
	// session's incarnation does not change while a subscriber is attached.
	account   SessionID
	allowance *Allowance

	// ready is the hand-over signal: sent, never blocking, whenever a
	// payload lands in the queue. Buffered to one because the signal is a
	// LEVEL and not a count — a consumer that parks after the payload
	// landed must not miss it, and a consumer that drains in one Take must
	// not find two payloads worth of token waiting for one drain.
	ready chan struct{}

	queue       []queued
	coalesced   uint64
	effectsLost uint64
	stale       bool
}

var _ Consumer = (*subscriber)(nil)

func (c *subscriber) Pending() int {
	c.owner.Lock()
	defer c.owner.Unlock()
	return len(c.queue)
}

func (c *subscriber) HeldBytes() int {
	c.owner.Lock()
	defer c.owner.Unlock()
	return c.heldBytesLocked()
}

func (c *subscriber) heldBytesLocked() int {
	held := 0
	for _, p := range c.queue {
		held += len(p.bytes) + len(p.effect.Body)
	}
	return held
}

func (c *subscriber) Coalesced() uint64 {
	c.owner.Lock()
	defer c.owner.Unlock()
	return c.coalesced
}

func (c *subscriber) EffectsLost() uint64 {
	c.owner.Lock()
	defer c.owner.Unlock()
	return c.effectsLost
}

func (c *subscriber) Stale() bool {
	c.owner.Lock()
	defer c.owner.Unlock()
	return c.stale
}

func (c *subscriber) Effects() []Effect {
	c.owner.Lock()
	defer c.owner.Unlock()
	held := make([]Effect, 0, len(c.queue))
	for _, p := range c.queue {
		if p.class == DeliveryAtMostOnce {
			// The body is COPIED out: a caller that wrote through it would be
			// editing what the runtime holds, and the lock is released the
			// moment this returns.
			e := p.effect
			e.Body = bytes.Clone(e.Body)
			held = append(held, e)
		}
	}
	return held
}

// Ready is the receive half of the hand-over. The channel never changes over
// a subscriber's life, so the read takes no lock.
func (c *subscriber) Ready() <-chan struct{} {
	return c.ready
}

// Take hands over every frame the runtime is holding for this consumer,
// oldest first, and empties them from the queue. Three things ride along:
//
//   - The allowance the held frames were spending is refunded, so a consumer
//     that reads is never capped by what it has already taken away. The
//     refund names the account the enqueue spent, and it runs under the
//     session lock — the same order the spend runs under — so a take and a
//     concurrent enqueue cannot interleave an account into a state neither
//     of them wrote.
//   - Staleness is cleared: what the take hands over is the newest state at
//     each revision, and the frames are full snapshots, so what the consumer
//     now holds is current until the runtime moves past it. The revisions
//     the class coalesced on the way were reported through [Consumer.
//     Coalesced] when they were shed and are not unsaid here.
//   - Effects are not frames: they stay in the queue, and [Consumer.Effects]
//     remains the way to read them. A take over a queue holding only
//     effects hands over nothing, refunds nothing and unsets nothing.
func (c *subscriber) Take() []FrameDelivery {
	c.owner.Lock()
	frames := make([]FrameDelivery, 0, len(c.queue))
	kept := c.queue[:0]
	for _, p := range c.queue {
		if p.class == DeliveryCoalescable {
			frames = append(frames, FrameDelivery{Revision: p.rev, Bytes: p.bytes})
		} else {
			kept = append(kept, p)
		}
	}
	if len(frames) > 0 {
		c.queue = kept
		c.stale = false
		c.allowance.give(c.account, len(frames))
	}
	c.owner.Unlock()
	return frames
}

// signal wakes whoever parks on [subscriber.Ready], if anyone is parked or
// parks before the next payload lands. It never blocks: the channel is
// buffered to one and the signal is a level, so a second payload before the
// first is read changes nothing the reader would otherwise see — Take always
// hands over everything held.
func (c *subscriber) signal() {
	select {
	case c.ready <- struct{}{}:
	default:
	}
}

// hasEffectLocked reports whether this consumer already holds THIS effect. It
// assumes the session lock, like the rest of the delivery path. Identity is
// the pair and not the number: an EffectID names an effect for as long as its
// incarnation lives, so a later incarnation minting the same number again is a
// different effect.
func (c *subscriber) hasEffectLocked(e Effect) bool {
	for _, p := range c.queue {
		if p.class == DeliveryAtMostOnce && p.effect.At == e.At && p.effect.ID == e.ID {
			return true
		}
	}
	return false
}

// Attach joins a consumer. What it is handed at attach is the screen the
// session has already shown: a session that has published a frame hands the
// attacher that frame as its baseline, before anything later (design step 6),
// and a session that has published nothing hands nothing, because its first
// revision is the attacher's baseline whenever it comes. A consumer that
// never reads is the ordinary case and not the hostile one, which is why the
// queue is bounded whether or not anyone drains it.
func (s *Session) Attach() Consumer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attachLocked()
}

func (s *Session) attachLocked() *subscriber {
	c := &subscriber{
		owner:     &s.mu,
		account:   s.inc.Session,
		allowance: s.allowance,
		ready:     make(chan struct{}, 1),
	}
	s.consumers = append(s.consumers, c)
	if s.latest != nil {
		// The baseline: the mid-session attacher is handed the screen as
		// last read, before anything later, through the same bounded path
		// every frame takes. An attacher to a session that has published
		// nothing is handed nothing — its first frame is the first
		// revision.
		s.enqueueLocked(c, queued{class: DeliveryCoalescable, rev: s.latest.rev, bytes: s.latest.bytes})
	}
	return c
}

func (s *Session) Attached() []Consumer {
	s.mu.Lock()
	defer s.mu.Unlock()
	attached := make([]Consumer, 0, len(s.consumers))
	for _, c := range s.consumers {
		attached = append(attached, c)
	}
	return attached
}

// Offer is the producer's side of the duplicate policy: handing a consumer an
// effect it has already been given delivers nothing.
func (s *Session) Offer(e Effect) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deliverLocked(effectDelivery(e))
}

// Resend is a full frame — a snapshot, a re-attachment, a resync. It carries
// cells and NEVER an effect: a resend of state must not re-ring a bell or
// write the clipboard again (design §6.2).
func (s *Session) Resend() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest != nil {
		return s.deliverLocked(queued{class: DeliveryCoalescable, rev: s.latest.rev, bytes: s.latest.bytes})
	}
	// Nothing has been published yet, and the resync asked for state: the
	// current screen — blank or not — is the state. No tick here: a resend
	// is not a transition (contract, Consumers).
	return s.publishFrameLocked()
}

// Lost is a consumer going away. It cancels no admitted input and revokes no
// control: losing a watcher is not losing the terminal.
func (s *Session) Lost() {}

// Detach removes a consumer that has gone away — a subscriber whose pump
// ended, whose wire died, whose reader is gone. The queue it held is drained
// under the same lock the enqueue path holds, and what it held is refunded
// to the session's account: a departed reader never keeps spending the
// allowance a live one needs. The hand-over that Take performs is not
// performed — there is no reader to hand to — so this is Take's refund
// without Take's delivery, and a consumer's staleness dies with it.
func (s *Session) Detach(c Consumer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := c.(*subscriber)
	if !ok {
		return
	}
	for i, held := range s.consumers {
		if held == sub {
			s.consumers = append(s.consumers[:i], s.consumers[i+1:]...)
			break
		}
	}
	// Effects the departed reader held go with it: at-most-once permits zero
	// deliveries, and there is nobody left to tell. The refund counts every
	// payload released — the allowance counts payloads held, and these are
	// held by nobody now.
	removed := len(sub.queue)
	sub.queue = nil
	sub.stale = false
	s.allowance.give(sub.account, removed)
}

// publishedFrame is one encoded frame the runtime remembers: the revision
// its cells were read at, and the session.frame bytes that describe them.
// The bytes are shared with every queue they were delivered to and are never
// written again — a frame is content, and content does not change under a
// reader.
type publishedFrame struct {
	rev   Revision
	bytes []byte
}

// publishFrameLocked encodes the active screen at the current revision,
// hands it to every attached consumer, and remembers it as the frame an
// attacher or a resync is owed. The snapshot is taken HERE, at publication,
// under the lock the caller holds — never re-read from the mutable screen at
// some older queued revision. A screen that cannot be read publishes
// nothing ([ErrNoScreen]): skipping a revision is the honest answer, a frame
// nobody could paint is not.
func (s *Session) publishFrameLocked() error {
	snap := s.snapshotLocked()
	encoded, err := EncodeFrame(snap)
	if errors.Is(err, ErrNoScreen) {
		return nil
	}
	if err != nil {
		return err
	}
	s.latest = &publishedFrame{rev: snap.Revision, bytes: encoded}
	return s.deliverLocked(queued{class: DeliveryCoalescable, rev: snap.Revision, bytes: encoded})
}

// deliverLocked routes one payload to whatever its class says may hold it.
func (s *Session) deliverLocked(p queued) error {
	switch p.class {
	case DeliveryCoalescable, DeliveryAtMostOnce:
		for _, c := range s.consumers {
			s.enqueueLocked(c, p)
		}
		return nil
	case DeliveryUnclassified:
		return ErrUnclassifiedDelivery
	default:
		// DeliveryLossless is the emulator's and the ledger's, and it never
		// travels this path: losing a byte of it is losing what the program
		// said, so there is no queue here to lose it in.
		return ErrUnclassifiedDelivery
	}
}

func (s *Session) enqueueLocked(c *subscriber, p queued) {
	if p.class == DeliveryAtMostOnce && c.hasEffectLocked(p.effect) {
		return
	}
	if !s.allowance.take(s.inc.Session) {
		s.shedLocked(c, p)
		return
	}
	c.queue = append(c.queue, p)
	c.signal()
}

// shedLocked makes room in a queue whose session has spent its allowance. The
// oldest COALESCABLE payload goes first, because a later frame supersedes it;
// if the queue holds only effects, the oldest effect goes, which at-most-once
// permits — zero deliveries is inside that class — as long as it is REPORTED.
func (s *Session) shedLocked(c *subscriber, p queued) {
	for i, held := range c.queue {
		if held.class != DeliveryCoalescable {
			continue
		}
		c.queue = append(c.queue[:i], c.queue[i+1:]...)
		s.reportLoss(c, held)
		c.queue = append(c.queue, p)
		c.signal()
		return
	}
	if len(c.queue) == 0 {
		s.reportLoss(c, p)
		return
	}
	oldest := c.queue[0]
	c.queue = c.queue[1:]
	s.reportLoss(c, oldest)
	c.queue = append(c.queue, p)
	c.signal()
}

// reportLoss tells the consumer what it lost. Without this the payload is gone
// and nothing the consumer holds says so, which is how a client paints a
// screen it believes is current.
func (s *Session) reportLoss(c *subscriber, p queued) {
	if p.class == DeliveryAtMostOnce {
		c.effectsLost++
	} else {
		c.coalesced++
	}
	c.stale = true
}
