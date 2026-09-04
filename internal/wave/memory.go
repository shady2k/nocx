package wave

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/google/uuid"
)

// MemoryStore is the record, held in memory for the life of the backend.
//
// # Why it is not durable
//
// The 2026-08-15 D5 makes a stage-1 worker die with the backend: nocx owns
// the PTY, and the process that owns the PTYs exits when its last session
// does. So the record's lifetime and its participants' lifetime coincide BY
// CONSTRUCTION, and a row that outlived them would describe nothing. The
// spawn-and-register design says exactly that and decides "no journal";
// ordering is the rollback within the lifetime, and across a restart there is
// nothing left to roll back.
//
// A durable participant would not merely be spare, it would be misleading:
// something read back after a restart is a claim about a process, and the
// only honest thing to say about every one of them is that it is gone.
//
// If D5 is ever repealed — workers surviving the backend, which is what the
// helper epic is for — a participant becomes a session-class record with a
// carry-over set, and that is where the durability question is answered. It
// is not answered here by keeping rows nobody could interpret.
//
// # What replaces the store's guards
//
// The SQLite rows carried their invariants in WHERE clauses and CHECK
// constraints. Every one of them is a method here, and each is stated where
// it is enforced: a terminal state is never overwritten, a participant only
// reaches live from prepared, and a cursor never moves backwards.
//
// # Concurrency
//
// The supervisor's exit, the enrolment rendezvous and the coordinator's own
// calls are on three different goroutines, so every method takes the one
// mutex, and every value that leaves is a COPY: Participant and Delegation
// carry pointers and slices, and handing out the stored ones would let any
// reader rewrite a fact only the two admitted sources may write.
type MemoryStore struct {
	mu      sync.Mutex
	waves   map[ID]string
	parts   map[ParticipantID]Participant
	dels    map[ParticipantID]Delegation
	mail    map[ReaderID][]Message
	cursors map[cursorKey]Cursor
}

// cursorKey is one reader's position in one mailbox. Two fields and not a
// concatenated string, because a mailbox id and a reader id are both free
// text and a separator would be a value one of them could contain.
type cursorKey struct {
	mailbox ReaderID
	reader  ReaderID
}

// NewMemoryStore returns an empty record. Empty is the only correct starting
// state: nothing this backend supervises existed before this backend did.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		waves:   map[ID]string{},
		parts:   map[ParticipantID]Participant{},
		dels:    map[ParticipantID]Delegation{},
		mail:    map[ReaderID][]Message{},
		cursors: map[cursorKey]Cursor{},
	}
}

var _ Store = (*MemoryStore)(nil)

// EnsureWave records a wave and does NOTHING if it is already there. It never
// reassigns: the coordinator session of a wave is its identity, and a second
// call moving it would hand every participant to a controller that never
// spawned them.
func (s *MemoryStore) EnsureWave(_ context.Context, id ID, coordinatorSession string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.waves[id]; ok {
		return nil
	}
	s.waves[id] = coordinatorSession
	return nil
}

func (s *MemoryStore) CommitPrepared(_ context.Context, p Participant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parts[p.ID] = copyParticipant(p)
	return nil
}

// MarkLive moves a prepared participant to live, and only a prepared one: a
// participant that is no longer prepared has been terminalized by a
// compensation, and marking it live would resurrect a record something else
// already closed.
func (s *MemoryStore) MarkLive(_ context.Context, id ParticipantID, l Liveness) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.parts[id]
	if !ok || p.State != StatePrepared {
		return fmt.Errorf("wave: mark live %q: %w", id, ErrNoSuchParticipant)
	}
	p.State = StateLive
	p.Liveness = l
	s.parts[id] = p
	return nil
}

// Terminalize writes a terminal state over a non-terminal one, and over
// nothing else.
//
// Already terminal is not an error, it is the whole point: a compensation is
// retried, and a second pass must complete what the first could not without
// turning a completed participant into an interrupted one. A state that is
// not terminal is refused by name — the type admits it, this method does not.
func (s *MemoryStore) Terminalize(_ context.Context, id ParticipantID, st State) error {
	if !st.Terminal() {
		return fmt.Errorf("wave: terminalize %q: %q is not a terminal state", id, st)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.parts[id]
	if !ok {
		return fmt.Errorf("wave: terminalize %q: %w", id, ErrNoSuchParticipant)
	}
	if p.State.Terminal() {
		return nil
	}
	p.State = st
	s.parts[id] = p
	return nil
}

// RecordDeclaration stores the participant's own terminal fact and returns
// the participant as it then stands, so the caller reduces from stored state
// rather than from what it believed was stored.
func (s *MemoryStore) RecordDeclaration(_ context.Context, id ParticipantID, d Declaration) (Participant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.parts[id]
	if !ok {
		return Participant{}, fmt.Errorf("wave: record declaration %q: %w", id, ErrNoSuchParticipant)
	}
	stored := d
	p.Declared = &stored
	s.parts[id] = p
	return copyParticipant(p), nil
}

// RecordExit stores the process fact and returns the participant as it then
// stands.
func (s *MemoryStore) RecordExit(_ context.Context, id ParticipantID, e Exit) (Participant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.parts[id]
	if !ok {
		return Participant{}, fmt.Errorf("wave: record exit %q: %w", id, ErrNoSuchParticipant)
	}
	stored := e
	p.Exited = &stored
	s.parts[id] = p
	return copyParticipant(p), nil
}

// PutDelegation commits the controller session's authority, REPLACING what
// was there. A delegation is one statement of what a session may currently
// do, so a second put is that statement again and not an addition to it.
func (s *MemoryStore) PutDelegation(_ context.Context, d Delegation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dels[d.Participant] = copyDelegation(d)
	return nil
}

// Delegation reads back what a controller session may do to a participant.
//
// A participant with no delegation is not an empty answer: it is ADDRESSABLE
// AND NOT CONTROLLABLE, which is a real state — a registration that failed at
// step 5, or a delegation that was revoked — and a zero delegation would let
// a caller read "no effects" as "no row" and refuse for the wrong reason.
func (s *MemoryStore) Delegation(_ context.Context, id ParticipantID) (Delegation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.dels[id]
	if !ok {
		return Delegation{}, fmt.Errorf("wave: no delegation over participant %q: %w", id, ErrNotDelegated)
	}
	return copyDelegation(d), nil
}

func (s *MemoryStore) Participant(_ context.Context, id ParticipantID) (Participant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.parts[id]
	if !ok {
		return Participant{}, fmt.Errorf("wave: participant %q: %w", id, ErrNoSuchParticipant)
	}
	return copyParticipant(p), nil
}

// CoordinatorSession answers who must judge a fact about this wave. A wave
// the record does not hold is refused rather than answered with the empty
// string, which is what stops a wake being addressed to nobody.
func (s *MemoryStore) CoordinatorSession(_ context.Context, id ID) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	coord, ok := s.waves[id]
	if !ok {
		return "", fmt.Errorf("wave: wave %q: %w", id, ErrNoSuchParticipant)
	}
	return coord, nil
}

func (s *MemoryStore) NonTerminal(_ context.Context, id ID) ([]Participant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectParticipants(func(p Participant) bool {
		return p.Wave == id && !p.State.Terminal()
	}), nil
}

func (s *MemoryStore) AllNonTerminal(_ context.Context) ([]Participant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectParticipants(func(p Participant) bool { return !p.State.Terminal() }), nil
}

// HeldBy answers D3 through the WAVE and never through the delegation, and
// the difference is the point. A delegation is revocable and suspendable;
// membership is not. A coordinator that has just restarted must be told about
// every worker it is responsible for, including one whose control it has
// temporarily lost to a human takeover — otherwise a takeover would make a
// worker disappear from its own coordinator's account of the wave.
//
// A coordinator does not hold itself: its own participant row is a
// membership, not a holding.
func (s *MemoryStore) HeldBy(_ context.Context, coordinatorSession string) ([]Participant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectParticipants(func(p Participant) bool {
		return s.waves[p.Wave] == coordinatorSession && p.Role != RoleCoordinator
	}), nil
}

// selectParticipants collects in one order — registration, then id — so two
// reads of an unchanged record answer the same way. Map iteration is random,
// and a caller shown a different order each time would be reading noise. The
// caller holds the lock.
func (s *MemoryStore) selectParticipants(keep func(Participant) bool) []Participant {
	var out []Participant
	for _, p := range s.parts {
		if keep(p) {
			out = append(out, copyParticipant(p))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].RegisteredAt.Equal(out[j].RegisteredAt) {
			return out[i].RegisteredAt.Before(out[j].RegisteredAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// copyParticipant deep-copies the two terminal facts, which are the only
// pointers a participant carries.
func copyParticipant(p Participant) Participant {
	if p.Declared != nil {
		d := *p.Declared
		p.Declared = &d
	}
	if p.Exited != nil {
		e := *p.Exited
		p.Exited = &e
	}
	return p
}

// copyDelegation copies the effect list, which is the only slice a delegation
// carries.
func copyDelegation(d Delegation) Delegation {
	d.Effects = append([]Effect(nil), d.Effects...)
	return d
}

// ── the mailbox ───────────────────────────────────────────────────────────

// Commit writes one message and mints its position in that mailbox.
//
// The sequence is minted from the mailbox's own current length under the same
// lock the append takes, because two coordinators writing to one worker at
// the same moment must not be given the same number: the order of a mailbox
// is the only thing a cursor can point at.
func (s *MemoryStore) Commit(_ context.Context, m Message) (Message, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Message{}, fmt.Errorf("wave: mint a message id: %w", err)
	}
	m.ID = MessageID(id.String())
	s.mu.Lock()
	defer s.mu.Unlock()
	box := s.mail[m.Recipient]
	m.Seq = int64(len(box)) + 1
	s.mail[m.Recipient] = append(box, m)
	return m, nil
}

// Since reads a page of one mailbox and MODIFIES NOTHING. That is the whole
// property: two readers get the same messages, and neither read is a take.
func (s *MemoryStore) Since(_ context.Context, mailbox ReaderID, after int64, limit int) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Message
	for _, m := range s.mail[mailbox] {
		if m.Seq <= after {
			continue
		}
		out = append(out, m)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Cursor reads one reader's position. A reader that has never looked gets a
// ZERO cursor rather than an error: "I have seen nothing" is the ordinary
// starting state of every reader that ever existed.
func (s *MemoryStore) Cursor(_ context.Context, mailbox, reader ReaderID) (Cursor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cursors[cursorKey{mailbox, reader}]
	if !ok {
		return Cursor{Mailbox: mailbox, Reader: reader}, nil
	}
	return c, nil
}

// AdvanceCursor moves a reader's marks and never moves either backwards.
//
// The maximum is taken here rather than in the caller because the caller
// reads and writes in two steps: a second reader for the same mailbox — a
// retry, a reconnect — can interleave between them, and a cursor going
// backwards hands out a message that was already acted on, which is the
// duplicated-effect failure §7.2 names.
func (s *MemoryStore) AdvanceCursor(_ context.Context, c Cursor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := cursorKey{c.Mailbox, c.Reader}
	if had, ok := s.cursors[key]; ok {
		if had.Fetched > c.Fetched {
			c.Fetched = had.Fetched
		}
		if had.Acted > c.Acted {
			c.Acted = had.Acted
		}
	}
	s.cursors[key] = c
	return nil
}

// Undelivered lists what a wave's mailboxes hold that their OWN RECIPIENTS
// have not fetched.
//
// It asks the recipient's own cursor and nobody else's, because a message is
// delivered when the participant it was addressed to has taken it — not when
// somebody has looked. A mailbox nobody has ever read has no cursor at all,
// and that is exactly where undelivered mail piles up.
func (s *MemoryStore) Undelivered(_ context.Context, id ID) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Message
	for mailbox, msgs := range s.mail {
		fetched := s.cursors[cursorKey{mailbox, mailbox}].Fetched
		for _, m := range msgs {
			if m.Wave == id && m.Seq > fetched {
				out = append(out, m)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Recipient != out[j].Recipient {
			return out[i].Recipient < out[j].Recipient
		}
		return out[i].Seq < out[j].Seq
	})
	return out, nil
}
