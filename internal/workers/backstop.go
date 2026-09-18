package workers

// The fifth thing the record holds: THE FACTS THAT NEED JUDGEMENT AND HAVE NOT
// BEEN DISPATCHED (§6 of the 2026-08-24 orchestration mechanism design).
//
// # What is left of it, and what left
//
// This used to be a backstop in two halves: a wake per fact (D14) and a
// per-fact DEADLINE that reached the human when nobody dispatched it (D2).
// Both are gone with nocx-luqz9.3, and for one reason: two mechanisms that can
// both call the human will, and the fact that reaches a person is not "this
// worker declared" but "this coordinator has not read anything in a while".
// The line typed at the coordinator, the retry and the human notice are now
// wake.go's, which is where a fact about TIME belongs. What this file keeps is
// the fact SET and the one event that closes a fact — the coordinator's own
// fetch.
//
// # The wake is unacknowledged, so it closes nothing
//
// Seeing our text echo in a pane's input region is evidence it was typed,
// never that it was acted on. A fact therefore leaves this set only when the
// coordinator FETCHES it — D8 keeps four acknowledgements distinct and
// advances the cursor on the second — and the fetch is the coordinator's own
// call (Registrar.HeldBy).
//
// # The set is the record's answer to "what do I still owe judgement on"
//
// It is read by workers.holdings, which reports each held participant's
// NeedsJudgement from it, and by the routing tests that decide which facts
// enter. That is why the set survived the mechanism it was built for: the
// routing table is a statement about the WORK and not about timers, and nothing
// else in the record answers it. What it no longer has is a waiter: the wait
// that used to hold a channel on it is gone with workers.wait (ADR-0070), so
// the one event that closes a fact is the coordinator's own fetch.
//
// # The set lives exactly as long as the backend
//
// So does everything else the record holds, and for one reason: a participant
// dies with the backend that spawned it (D5), so a fact that outlived the
// restart would need judgement about a participant that is gone, and the
// coordinator it would be dispatched to is gone with its run.

import (
	"context"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// FactKind names which fact entered the set. There is exactly one left — the
// process exit — because the participant's own account of its work left with
// the declaration (ADR-0070 decision 3). Nothing read off a screen enters
// here, and the grid cannot reach this file at all.
type FactKind string

const (
	// FactExited is the process fact.
	FactExited FactKind = "exited"
)

// Fact is one thing that needs judgement and has not been dispatched.
type Fact struct {
	Participant ParticipantID
	Group       ID
	// CoordinatorSession is who must judge it. It is carried ON the fact
	// rather than looked up when a wake is attempted: by then the worker may
	// hold nothing non-terminal, and the answer would be gone exactly when it
	// is needed.
	CoordinatorSession string
	Kind               FactKind
	// State is what the participant's record became. It is the record's own
	// word rather than a second vocabulary.
	State State
	// Task is what the participant was given, so anything that names the fact
	// can say what it is about without reading anything back.
	Task      string
	EnteredAt time.Time
}

// Stats is what the mechanism costs, counted rather than assumed.
//
// §12 of the design names the number it is judged by: what fraction of facts
// reaches the HUMAN rather than the coordinator. If most escalate, the
// mechanism moved the work to a person and should say so out loud instead of
// being described as orchestration. That number cannot be argued from a
// design, so the record counts it.
//
// THREE OF THESE ARE THE SET'S AND TWO ARE THE WAKE'S, and they are one struct
// because they are one question read at two moments: what the record owes
// judgement on, and what it cost to have it read. A reader shown only the
// first could not tell a mechanism that is working from one that has stopped.
type Stats struct {
	// Routine is facts recorded that woke nobody, because the coordinator
	// had nothing to decide about them yet.
	Routine int
	// Judgement is facts that entered the undispatched set.
	Judgement int
	// Dispatched is facts the coordinator fetched for itself.
	Dispatched int
	// Lines is lines TYPED AND DELIVERED at a coordinator's pane (wake.go).
	// A refused attempt is not one: it reached nobody.
	Lines int
	// Notices is how many times a person was told that coordination has
	// stopped. It is the wake's counterpart to Judgement, and the pair is the
	// fraction §12 asks for — one measures whether the mechanism is doing the
	// work, the other measures what it costs somebody's attention.
	Notices int
}

// Facts is every fact the record has routed, which is the denominator of the
// fraction.
func (s Stats) Facts() int { return s.Routine + s.Judgement }

// factKey is (participant, kind): one participant produces at most one of
// each fact, and a repeat of one it already owes judgement on is the same
// fact rather than a second one.
type factKey struct {
	participant ParticipantID
	kind        FactKind
}

// Backstop holds the undispatched facts: what the record still owes judgement
// on, and the one event that clears it.
type Backstop struct {
	log log.Logger

	mu    sync.Mutex
	open  map[factKey]*Fact
	stats Stats
}

// NewBackstop builds an empty set.
func NewBackstop(lg log.Logger) *Backstop {
	if lg == nil {
		lg = log.NewSlogAdapter(nil)
	}
	return &Backstop{
		log:  lg,
		open: make(map[factKey]*Fact),
	}
}

// Routine records a fact the coordinator has nothing to decide about yet.
//
// It is one half of the routing table and it is deliberately a METHOD rather
// than an early return inside Entered: a fact that woke nobody is still a
// fact, and it is the denominator of the number §12 judges this design by. A
// routing table whose routine branch left no trace could not report what
// fraction of facts reached anybody.
func (b *Backstop) Routine(f Fact) {
	b.mu.Lock()
	b.stats.Routine++
	b.mu.Unlock()
	b.log.Debug("worker: a fact was recorded and nobody was woken",
		"participant", string(f.Participant), "worker", string(f.Group),
		"kind", string(f.Kind), "state", string(f.State))
}

// Entered admits a fact that needs judgement.
//
// Nothing is armed and nobody is typed at: the wake is the coordinator's
// unread MAIL and not this fact (wake.go), and this only records what the
// record owes. The same fact about the same participant twice is one fact —
// coalesced rather than re-entered, because a second entry would be counted
// twice in the denominator of §12's fraction.
func (b *Backstop) Entered(_ context.Context, f Fact) {
	key := factKey{participant: f.Participant, kind: f.Kind}
	f.EnteredAt = time.Now()

	b.mu.Lock()
	defer b.mu.Unlock()
	if _, already := b.open[key]; already {
		return
	}
	b.open[key] = &f
	b.stats.Judgement++
}

// Dispatched removes every open fact about these participants.
//
// This is the FETCH — the coordinator has been told — and it is the only thing
// that closes a fact. A wake does not, because delivery is unacknowledged.
func (b *Backstop) Dispatched(ids ...ParticipantID) {
	if len(ids) == 0 {
		return
	}
	told := make(map[ParticipantID]bool, len(ids))
	for _, id := range ids {
		told[id] = true
	}
	settled := make(map[ID]bool)
	b.mu.Lock()
	for key, open := range b.open {
		if !told[key.participant] {
			continue
		}
		delete(b.open, key)
		b.stats.Dispatched++
		settled[open.Group] = true
	}
	stats := b.stats
	b.mu.Unlock()

	for id := range settled {
		b.log.Info("worker: the coordinator has judged everything this worker owed",
			"worker", string(id),
			"facts", stats.Facts(), "routine", stats.Routine,
			"dispatched", stats.Dispatched)
	}
}

// Stats is what the set has cost so far, in its own half of Stats' own doc.
func (b *Backstop) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats
}

// Open is what the record still owes judgement on. It is a snapshot and holds
// no pointers into the set.
func (b *Backstop) Open() []Fact {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Fact, 0, len(b.open))
	for _, f := range b.open {
		out = append(out, *f)
	}
	return out
}
