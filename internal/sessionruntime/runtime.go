package sessionruntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

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
	// RendezvousExpiry is the bounded missing-fence wait (design §6.4): how
	// long a rendezvous left with one half missing stays pending before the
	// runtime calls its own [Session.ExpireRendezvous]. The number is free
	// to change; the POLICY is not, and zero means no timer — the contract
	// drives [Session.ExpireRendezvous] itself, as a call, and the
	// composition root (internal/helper/session) states the production
	// interval out loud rather than a default assuming it.
	RendezvousExpiry time.Duration
	// ExpireAfter schedules the wait. Nil means time.AfterFunc; a test
	// hands its own and fires the trigger itself, because a test may not
	// depend on timing (AGENTS.md) — the wait must be observable as a
	// STATE, never as a duration.
	ExpireAfter func(d time.Duration, f func()) (stop func() bool)
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

	// expireDur and expireAfter are the bounded rendezvous waits' wiring
	// (Config.RendezvousExpiry): the seam a test injects its own trigger
	// through, because a wait must be observable as a STATE, never a
	// duration. Each pending meeting arms its OWN wait (rendezvousEntry.stop
	// holds the cancel handle); both fields are guarded by mu, and a timer's
	// callback takes mu again by entering [Session.expireBy].
	expireDur   time.Duration
	expireAfter func(d time.Duration, f func()) (stop func() bool)
	// expireSeq is the generation space for every armed wait: it is bumped
	// each time a timer is actually scheduled, and a callback whose
	// generation its entry no longer carries does nothing. A one-shot timer
	// can fire while its stop is being replaced — Stop returns false and the
	// callback runs anyway — so the disarm alone cannot make this safe; the
	// check must be under mu at the moment of transitioning.
	expireSeq uint64

	allowance *Allowance
	// consumers are the subscribers attached to this session, in attach order.
	consumers []*subscriber
	// nextEffect mints the identity an effect's duplicate policy is stated
	// over. The runtime mints it, never the consumer.
	nextEffect EffectID

	// ingestWork is the work the runtime's OWN ingest path has spent, one unit
	// per byte it examined, and ingestLost is what it discarded. Both are
	// numbers so that "bounded" is checkable rather than asserted.
	ingestWork uint64
	ingestLost uint64

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
}

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
	after := cfg.ExpireAfter
	if after == nil {
		after = defaultExpireAfter
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
		expireDur:    cfg.RendezvousExpiry,
		expireAfter:  after,
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

	replies, err := s.emulator.Ingest(b)
	if err != nil {
		// The emulator refused bytes it was handed, so what it holds is not
		// the whole stream and no attach may be told otherwise: the session
		// says so, in the same breath as reporting the failure.
		s.completeness = CompletenessLostIngest
		return err
	}
	// The program's own answer is handed to the reply sink on the ordered
	// path. A failure here is reported, and it does NOT swallow what the
	// stream produced: the bytes reached the emulator, so the effects the
	// program asked for and the frame the screen moved to are owed to the
	// consumer either way. An effect dropped because a reply could not be
	// delivered is a bell that rings nowhere and is never reported as lost.
	replyErr := s.deliverReplyLocked(replies)
	for _, e := range s.emulator.Effects() {
		if e.Kind == emulator.EffectFence {
			// The join: a fence the emulator drained LOCATES the
			// authenticated half of the rendezvous (ADR-0024 decision 1)
			// and is not a consumer payload, so it never reaches
			// effectKindOf or the delivery path below — a kind the
			// delivery vocabulary does not know is refused there, and a
			// fenced command must not fail the ingest that carried it.
			s.sightDrainedFenceLocked(e)
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
	if err := s.deliverLocked(frameDelivery(s.tick())); err != nil {
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
	s.completeness = CompletenessLostIngest
	s.tick()
	return nil
}

// --------------------------------------------------------------- rendezvous

// rendezvousEntry is one tracked meeting: its record, and the bounded wait
// armed while it is pending. The wait is observable as a STATE (the entry's
// own trigger through [Config.ExpireAfter], fired into [Session.expireBy]),
// never as a duration; its generation ([Session.expireSeq]) is what makes a
// stale trigger harmless.
type rendezvousEntry struct {
	Rendezvous
	// stop cancels this entry's own wait. Nil once the entry is settled or
	// when no expiry policy is configured.
	stop func() bool
	// seq is the generation the wait was armed under.
	seq uint64
}

// pending reports whether the meeting is still waiting for one of its halves.
func (e *rendezvousEntry) pending() bool {
	return e.State == RendezvousAwaitingSighting || e.State == RendezvousAwaitingAuthenticated
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
	if e := s.rendezvous[nonce]; e != nil {
		// The meeting is already tracked. Only a parked sighting with THIS
		// nonce is the half this completion closes; a duplicate completion,
		// or one arriving after the meeting settled, is the same event
		// again and changes nothing — a sighting may authorise nothing, and
		// neither may a late second half reopen what a bounded wait
		// honestly closed.
		if e.State == RendezvousAwaitingAuthenticated {
			s.disarmEntryLocked(e)
			e.State = RendezvousComplete
			s.rendezvousLatest, s.rendezvousHasLatest = nonce, true
			s.tick()
		}
		return
	}
	s.admitRendezvousLocked(&rendezvousEntry{Rendezvous: Rendezvous{
		State: RendezvousAwaitingSighting,
		Nonce: nonce,
		At:    at,
	}}, true)
}

// admitRendezvousLocked inserts a new meeting into the set, keeping it within
// [MaxPendingRendezvous], and answers whether it was admitted.
//
// authenticated says an authenticated half is behind the insert. At the bound
// the oldest SETTLED meeting gives up its slot first — it is record, not
// authority — and then, for a completion only, the oldest parked SIGHTING:
// a fence with nothing authenticated behind it authorised nothing, so
// evicting one spends nothing, while a pending authenticated half is evicted
// by nothing but its own expiry or its completion. A sighting that finds no
// room is refused.
func (s *Session) admitRendezvousLocked(e *rendezvousEntry, authenticated bool) bool {
	if len(s.rendezvous) >= MaxPendingRendezvous {
		if !s.evictRendezvousLocked(func(e *rendezvousEntry) bool { return !e.pending() }) &&
			!(authenticated && s.evictRendezvousLocked(func(e *rendezvousEntry) bool {
				return e.State == RendezvousAwaitingAuthenticated
			})) {
			return false
		}
	}
	s.rendezvous[e.Nonce] = e
	s.rendezvousOrder = append(s.rendezvousOrder, e.Nonce)
	s.rendezvousLatest, s.rendezvousHasLatest = e.Nonce, true
	s.armExpiryLocked(e)
	s.tick()
	return true
}

// evictRendezvousLocked removes the OLDEST meeting satisfying keep, stopping
// its wait first, and answers whether one was found.
//
// It is called only from [Session.admitRendezvousLocked], and only to make
// room for a meeting that is inserted immediately afterwards — so if the
// evicted meeting was the one rendezvousLatest named, the insert re-points
// the window before the lock is released. The window can never dangle here,
// and Rendezvous can never answer idle about a set that is not empty.
func (s *Session) evictRendezvousLocked(keep func(*rendezvousEntry) bool) bool {
	for i, nonce := range s.rendezvousOrder {
		if e := s.rendezvous[nonce]; e != nil && keep(e) {
			s.disarmEntryLocked(e)
			delete(s.rendezvous, nonce)
			s.rendezvousOrder = append(s.rendezvousOrder[:i], s.rendezvousOrder[i+1:]...)
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
	if e := s.rendezvous[nonce]; e != nil {
		switch e.State {
		case RendezvousAwaitingSighting:
			// The matching sighting is the half that was missing.
			s.disarmEntryLocked(e)
			e.State = RendezvousComplete
			e.SightedAt = s.rev
			e.PinnedSource = append([]byte(nil), source...)
			s.rendezvousLatest, s.rendezvousHasLatest = nonce, true
			s.tick()
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
	// anybody: it authorises nothing, so refusing it costs nothing.
	if !s.admitRendezvousLocked(&rendezvousEntry{Rendezvous: Rendezvous{
		State:        RendezvousAwaitingAuthenticated,
		Nonce:        nonce,
		At:           s.inc,
		SightedAt:    s.rev,
		PinnedSource: append([]byte(nil), source...),
	}}, false) {
		return ErrRendezvousFull
	}
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

// fenceNonceOf decodes a fence body: exactly 64 hex characters. The
// emulator's adapter only reports a fence whose stream bytes matched that
// shape exactly (internal/emulator/ghostty/fence.go), so a mismatch here is
// defensive, and refusing it is the safe answer — a zero-filled nonce could
// otherwise MATCH a zero completion and close a meeting on bytes that never
// carried a fence at all.
func fenceNonceOf(body []byte) (FenceNonce, bool) {
	var nonce FenceNonce
	raw, err := hex.DecodeString(string(body))
	if err != nil || len(raw) != len(nonce) {
		return nonce, false
	}
	copy(nonce[:], raw)
	return nonce, true
}

// defaultExpireAfter is the production scheduler: time.AfterFunc behind the
// seam [Config.ExpireAfter] names.
func defaultExpireAfter(d time.Duration, f func()) (stop func() bool) {
	t := time.AfterFunc(d, f)
	return t.Stop
}

// armExpiryLocked arms ONE entry's bounded wait: a timer exists exactly while
// its meeting is pending, and the moment the meeting settles its own timer is
// disarmed. It runs under mu; the trigger it schedules does NOT hold mu,
// because its whole body is [Session.expireBy], which takes the lock itself.
func (s *Session) armExpiryLocked(e *rendezvousEntry) {
	if s.expireDur <= 0 || !e.pending() {
		return
	}
	s.expireSeq++
	e.seq = s.expireSeq
	nonce, seq := e.Nonce, e.seq
	e.stop = s.expireAfter(s.expireDur, func() { s.expireBy(nonce, seq) })
}

// disarmEntryLocked cancels one entry's wait, if one is armed.
func (s *Session) disarmEntryLocked(e *rendezvousEntry) {
	if e.stop != nil {
		e.stop()
		e.stop = nil
	}
}

// expireBy is a timer callback's entry. Stop cannot retract a callback that
// is already running or already dequeued, so the callback re-checks, under
// mu, that the entry still exists and still carries the generation the wait
// was armed under: a superseded or settled meeting expires nothing. (After
// [Fail] the same guard holds through [live]: a session that is gone expires
// nothing.)
func (s *Session) expireBy(nonce FenceNonce, seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.rendezvous[nonce]
	if e == nil || e.seq != seq {
		return
	}
	_ = s.expireRendezvousLocked(nonce)
}

// ExpireRendezvous is the bounded wait elapsing for the meeting the nonce
// names. It is a call and not a timer, so the contract can exercise it
// without depending on a duration. An AUTHENTICATED interval whose fence
// never arrived may still be worth keeping; it may not be described as the
// command's complete output. A sighted fence with nothing authenticated
// behind it expires into nothing at all.
func (s *Session) ExpireRendezvous(nonce FenceNonce) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expireRendezvousLocked(nonce)
}

// expireRendezvousLocked is [Session.ExpireRendezvous] with the lock held.
//
// The two pending states are not the same thing and do not expire the same
// way. AwaitingSighting is an AUTHENTICATED completion whose fence never
// arrived: the interval really has no trustworthy boundary, so completeness
// becomes [CompletenessNoFence] and the write gate refuses. AwaitingAuthenticated
// is a sighting with NOTHING authenticated behind it: it authorised nothing
// while it waited (ADR-0024 decision 1), so expiring it drops the pinned
// source and changes NOTHING ELSE — not completeness, not write authority.
// Unauthenticated output can therefore never revoke the person's ability to
// type, for however long the session lives.
func (s *Session) expireRendezvousLocked(nonce FenceNonce) error {
	if err := s.live(); err != nil {
		return err
	}
	e := s.rendezvous[nonce]
	if e == nil || !e.pending() {
		return ErrNoRendezvous
	}
	awaitingSighting := e.State == RendezvousAwaitingSighting
	s.disarmEntryLocked(e)
	e.State = RendezvousExpired
	e.PinnedSource = nil
	if awaitingSighting && s.completeness == CompletenessComplete {
		s.completeness = CompletenessNoFence
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
	for _, e := range s.rendezvous {
		s.disarmEntryLocked(e)
	}
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

// frameDelivery is one coalescable frame: the revision of the cells a consumer
// is owed, and NOT a copy of them. A frame supersedes the one before it, the
// consumer reads the cells at the revision it last saw, and a queue holding
// eight copies of an 80x24 screen would be the one kind of memory the bounds
// exist to refuse. The frame protocol itself is the client epic's (nocx-zg3k3).
func frameDelivery(rev Revision) queued {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(rev))
	return queued{class: DeliveryCoalescable, rev: rev, bytes: encoded[:]}
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

// Attach joins a consumer. It is handed nothing until the runtime emits
// something, which is why a consumer that never reads is the ordinary case and
// not the hostile one.
func (s *Session) Attach() Consumer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attachLocked()
}

func (s *Session) attachLocked() *subscriber {
	c := &subscriber{owner: &s.mu}
	s.consumers = append(s.consumers, c)
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
	return s.deliverLocked(frameDelivery(s.rev))
}

// Lost is a consumer going away. It cancels no admitted input and revokes no
// control: losing a watcher is not losing the terminal.
func (s *Session) Lost() {}

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
