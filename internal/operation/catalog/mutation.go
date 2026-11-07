package catalog

// A mutation's identity, and the bounded record of what became of it
// (ADR-8eej3e66 §4).
//
// A mutation may commit an effect the caller cannot see: the response is lost,
// the renderer gave up, the socket dropped, or the operation ran past its
// deadline. Nothing in that situation may replay it, because a replay of a
// committed write is a second write. So the renderer mints an identity and
// carries it with the call, and the backend retains the answer to one
// question — did it start, is it done, or is it still unknown — for a bounded
// time, in memory.
//
// The answer is the catalogue's own `expired` field read backwards: an
// operation declared `outcome-unknown` is one whose effect may exist by the
// deadline, and that is the operation that needs an identity. Nothing else
// does.
//
// Nothing here runs on a clock of its own. The store has no goroutine, no
// timer and no log line: a record expires when somebody asks for it after the
// bound, which is the only moment the answer is worth anything. That keeps the
// promise the scope makes — nothing is written after settlement — and keeps a
// routine call silent.
//
// The record is deliberately about the INVOCATION and not about the effect's
// payload. "done" means the backend stopped working on it and answered; it
// does not claim the write committed. That is the conservative direction for
// the rule this exists for: it can never talk a client into a replay.

import (
	"sync"
	"time"
)

// Answer is what operation.outcome retains about a mutation. The first three
// are ADR-8eej3e66 §8's "did not start", "done" and "checking what happened";
// NoRecordAnswer is where checking ends — nothing is retained, so nothing can
// be said.
type Answer string

const (
	// AnswerNotStarted: no work ran, so a retry cannot double an effect.
	AnswerNotStarted Answer = "not-started"
	// AnswerDone: the work finished. It may have answered an error; either
	// way it is not still running and must not be replayed.
	AnswerDone Answer = "done"
	// AnswerUnknown: the work has not finished. The person is "checking what
	// happened" and may ask again while the record is retained.
	AnswerUnknown Answer = "outcome-unknown"
	// AnswerNoRecord: there is nothing to say — the identity was never seen,
	// its retention ran out, or the backend restarted.
	AnswerNoRecord Answer = "unknown"
)

// AnsweredKind is the response a mutation's handler produced, when it produced
// one. The responder is the only source of this: a handler that returns
// without answering is read off the invocation's settlement instead.
type AnsweredKind string

const (
	// AnsweredResult: the handler answered with a result.
	AnsweredResult AnsweredKind = "result"
	// AnsweredError: the handler answered with an error.
	AnsweredError AnsweredKind = "error"
	// AnsweredSealed: the handler was refused because the vault is sealed.
	// That refusal is emitted before the operation touched anything, which is
	// exactly what ADR-0032's unlock replay relies on, so it is the one
	// error answer that means "not started".
	AnsweredSealed AnsweredKind = "sealed"
)

// RetentionMillis is how long a settled mutation's answer is kept. It is not a
// deadline on work: it is how long the backend can say what became of a
// mutation after the caller stopped waiting.
//
// No present behaviour to read: the backend keeps no record at all today, so
// this stage chooses the bound and states it here. Five minutes outlives a
// reconnection and a vault unlock prompt, and bounds the answer a person can
// still get; anything older answers AnswerNoRecord and names what to look at
// instead.
const RetentionMillis int64 = 300_000

// Retention is the same bound with the provenance every deadline in this
// catalogue carries, so a reader cannot mistake it for a zero or unlimited
// value and the validation refuses one.
var Retention = Deadline{
	Millis: RetentionMillis,
	Basis:  BasisRead,
	Note:   "no present behaviour: the backend retains no outcome at all; five minutes is this stage's chosen bound, long enough for a reconnect or an unlock and short enough to bound the answer",
}

// MaxRetained bounds the store's size in records. A settled record is the one
// evicted first; a running one only when nothing is settled, which needs more
// than a thousand mutations in flight at once.
const MaxRetained = 1024

// MutationAnswer is what operation.outcome answers: the retained state and the
// operation it was recorded for.
type MutationAnswer struct {
	Answer    Answer
	Operation ID
}

type mutationState uint8

const (
	// mutationRunning: the invocation started and has not finished.
	mutationRunning mutationState = iota
	// mutationNotStarted: nothing ran.
	mutationNotStarted
	// mutationDone: the invocation finished.
	mutationDone
	// mutationUnknown: it stopped without an answer (withdrawn, cancelled or
	// expired), so whether the effect happened is not knowable here.
	mutationUnknown
)

// MutationRecord is one mutation's retained state. Every method is nil-safe:
// a call that carries no identity has no record, and dispatch must not branch
// on it.
type MutationRecord struct {
	store  *MutationStore
	key    string
	op     ID
	state  mutationState
	answer bool // a terminal fact was recorded; the state must not be overwritten
	began  time.Time
	ended  time.Time // zero while running
}

func (r *MutationRecord) notStarted() {
	if r == nil {
		return
	}
	// TERMINAL. A task that never began cannot acquire a handler answer
	// afterwards, so the fact is recorded with the same finality as a
	// response. Without it the transport's own expiry answer — an error
	// written after this call — classified the record as "done", and the one
	// retry a not-started identity sanctions was refused as a replay of work
	// that never ran (nocx-3an1s.2.17).
	r.store.record(r, mutationNotStarted, true)
}

func (r *MutationRecord) answered(kind AnsweredKind) {
	if r == nil {
		return
	}
	if kind == AnsweredSealed {
		r.store.record(r, mutationNotStarted, true)
		return
	}
	r.store.record(r, mutationDone, true)
}

// settled records how the invocation ended. A response already recorded wins:
// the handler answered, and the answer is the better fact.
func (r *MutationRecord) settled(outcome Outcome) {
	if r == nil {
		return
	}
	switch outcome {
	case OutcomeCompleted:
		r.store.record(r, mutationDone, false)
	case OutcomeNotStarted, OutcomeRefused:
		r.store.record(r, mutationNotStarted, false)
	default:
		// Cancelled or expired: the work stopped (or is still stopping) and
		// nobody answered, so the effect is not knowable.
		r.store.record(r, mutationUnknown, false)
	}
}

// NotStarted marks a mutation whose worker never began. It is the one record
// write the transport makes outside settlement, because a withdrawn queued
// task never reaches the handler that would have settled it.
func (r *MutationRecord) NotStarted() { r.notStarted() }

// Answered marks the response the handler produced.
func (r *MutationRecord) Answered(kind AnsweredKind) { r.answered(kind) }

// Settled marks how the invocation ended.
func (r *MutationRecord) Settled(outcome Outcome) { r.settled(outcome) }

// MutationStore retains mutation answers for Retention.
type MutationStore struct {
	mu    sync.Mutex
	now   func() time.Time
	byKey map[string]*MutationRecord
	order []string
}

// NewMutationStore returns an empty store on the wall clock.
func NewMutationStore() *MutationStore { return newMutationStore(time.Now) }

func newMutationStore(now func() time.Time) *MutationStore {
	return &MutationStore{now: now, byKey: make(map[string]*MutationRecord)}
}

// Begin registers a mutation, or reports the answer that forbids it.
//
// A record that says the work never started may be retried: that is ADR-0032's
// unlock replay, and the renderer is allowed exactly that one. Anything else is
// a replay of work that may have happened, and is refused. The caller answers
// the request with mutation-replayed and the record's answer.
func (s *MutationStore) Begin(key string, op ID) (*MutationRecord, MutationAnswer) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, seen := s.byKey[key]
	if seen {
		if s.expired(existing, now) {
			s.removeLocked(key, existing)
		} else if existing.state != mutationNotStarted {
			return nil, MutationAnswer{Answer: answerOf(existing.state), Operation: existing.op}
		}
		// WHATEVER RUNS NEXT GETS ITS OWN RECORD, and the record is the
		// ATTEMPT's handle rather than the identity's (nocx-3an1s.2.17). A
		// retry the record sanctions is admitted because nothing ran the first
		// time, but the first attempt is not necessarily finished: an
		// invocation that expired while queued reaches its start gate AFTER the
		// retry was admitted, and it used to write "not started" onto the very
		// record the retry was running under — because Begin handed the retry
		// that record. The retry's effect then read as work that never ran, and
		// a third call under the identity was admitted beside it. Every write
		// is checked against the record the map now holds (record), so a
		// replaced attempt's settlement can change nothing.
	}
	s.evictLocked()
	record := &MutationRecord{store: s, key: key, op: op, state: mutationRunning, began: now}
	s.byKey[key] = record
	// THE PRUNE RUNS WITH THE RECORD IN THE MAP, so a key that was here before
	// — a retry, or an identity whose expired record was just removed — keeps
	// the one place it already has. Compacting first dropped that place,
	// because the map no longer held the key, and the record inserted
	// afterwards was never appended: it lived in the map and in no order entry,
	// where evictLocked cannot pick it (nocx-3an1s.2.17). A key the order does
	// not hold yet gets its one entry here.
	s.pruneOrderLocked()
	if !seen {
		s.order = append(s.order, key)
	}
	return record, MutationAnswer{}
}

// Answer reports what became of a mutation.
func (s *MutationStore) Answer(key string) MutationAnswer {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.byKey[key]
	if !ok {
		return MutationAnswer{Answer: AnswerNoRecord}
	}
	if s.expired(record, now) {
		s.removeLocked(key, record)
		return MutationAnswer{Answer: AnswerNoRecord}
	}
	return MutationAnswer{Answer: answerOf(record.state), Operation: record.op}
}

func (s *MutationStore) record(r *MutationRecord, state mutationState, answered bool) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	// ONLY THE ATTEMPT THE MAP HOLDS MAY WRITE IT. An identity that was
	// retried — or whose record expired and started over — has a different
	// record now, and the attempt it replaced may still reach its own end: its
	// settlement belongs to its own caller and to nothing else
	// (nocx-3an1s.2.17).
	if current, ok := s.byKey[r.key]; !ok || current != r {
		return
	}
	if r.answer {
		return
	}
	r.state = state
	r.answer = answered
	if state != mutationRunning {
		r.ended = now
	}
}

// expired reports whether a settled record has outlived its retention. A
// running record never expires by time: its own max bounds it, and eviction
// bounds the map.
func (s *MutationStore) expired(r *MutationRecord, now time.Time) bool {
	if r.state == mutationRunning || r.ended.IsZero() {
		return false
	}
	return now.Sub(r.ended) >= time.Duration(RetentionMillis)*time.Millisecond
}

func (s *MutationStore) removeLocked(key string, r *MutationRecord) {
	if s.byKey[key] != r {
		return
	}
	delete(s.byKey, key)
}

// pruneOrderLocked drops insertion-order entries whose record is gone. The
// order list is the only thing that grows without a record beside it, so it is
// compacted before it can, in its own words, remember every mutation a session
// ever made.
func (s *MutationStore) pruneOrderLocked() {
	if len(s.order) <= 2*len(s.byKey)+8 {
		return
	}
	live := s.order[:0]
	for _, key := range s.order {
		if _, ok := s.byKey[key]; ok {
			live = append(live, key)
		}
	}
	s.order = live
}

// evictLocked drops the oldest settled record when the store is full, and the
// oldest record of any kind when nothing is settled.
func (s *MutationStore) evictLocked() {
	for len(s.byKey) >= MaxRetained && len(s.order) > 0 {
		victim := -1
		for i, key := range s.order {
			r, ok := s.byKey[key]
			if !ok {
				continue
			}
			if r.state != mutationRunning {
				victim = i
				break
			}
			if victim < 0 {
				victim = i
			}
		}
		if victim < 0 {
			s.order = s.order[:0]
			return
		}
		key := s.order[victim]
		s.order = append(s.order[:victim], s.order[victim+1:]...)
		delete(s.byKey, key)
	}
}

func answerOf(state mutationState) Answer {
	switch state {
	case mutationNotStarted:
		return AnswerNotStarted
	case mutationDone:
		return AnswerDone
	default:
		return AnswerUnknown
	}
}
