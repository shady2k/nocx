package transport

// The lane's interactivity state (ADR-0020 decision 3): a program that
// takes the alternate screen puts the lane in awaiting-takeover, where the
// agent is demoted, not evicted — it loses write authority and keeps the
// right to read the screen and advise — and the human takes over, answers,
// detaches or kills the program.
//
// The backend cannot see the alternate screen: AD-6 forbids it sniffing the
// byte stream, and the renderer already owns that fact (capture identity
// carries the buffer kind). So the FACT travels up from the renderer
// (agent.laneInteractivity carries the buffer kind the renderer observed)
// and the TRANSITION is decided here, in Go — the lane's state machine is
// backend-owned, exactly as the design's split requires ("interactivity is
// a fact travelling up from the renderer, and the lane's transition is
// decided in Go from it").
//
// The state is per-session, in-memory, and re-reported by the renderer on
// every buffer change (including the replay after a reattach). A consumer
// that must react to the transition — the run lease — registers a WATCH
// callback: note invokes every watcher synchronously on the reporting
// goroutine (the read loop), so the lease suspends without spawning a
// goroutine of its own (ADR-0026 enforcement).

import (
	"sync"

	"github.com/shady2k/nocx/internal/session"
)

type laneRecord struct {
	// awaiting is true while the lane's buffer is the alternate screen:
	// a program owns the terminal and the human owns the execution.
	awaiting bool
	// watchers are the registered transition callbacks, invoked outside
	// the lock by note. nextID mints their identities (a func value cannot
	// be compared, so identity is a map key, not a func).
	nextID   int
	watchers map[int]func()
}

// laneState is the transport's per-session lane interactivity map.
type laneState struct {
	mu    sync.Mutex
	lanes map[session.ID]*laneRecord
}

func newLaneState() *laneState {
	return &laneState{lanes: make(map[session.ID]*laneRecord)}
}

// note records the renderer's report of the lane's buffer kind and invokes
// every watcher. "normal" clears awaiting-takeover; "alternate" enters it.
// Watchers run OUTSIDE the lock (a watcher may re-enter the state to
// unregister) and must not block — the lease's callback is a mutex-guarded
// disarm, microseconds.
func (ls *laneState) note(sid session.ID, bufferKind string) {
	ls.mu.Lock()
	rec := ls.lanes[sid]
	if rec == nil {
		rec = &laneRecord{watchers: make(map[int]func())}
		ls.lanes[sid] = rec
	}
	rec.awaiting = bufferKind == "alternate"
	watchers := make([]func(), 0, len(rec.watchers))
	for _, w := range rec.watchers {
		watchers = append(watchers, w)
	}
	ls.mu.Unlock()
	for _, w := range watchers {
		w()
	}
}

// awaitingTakeover reports whether the lane is awaiting takeover right now.
func (ls *laneState) awaitingTakeover(sid session.ID) bool {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	rec := ls.lanes[sid]
	return rec != nil && rec.awaiting
}

// watch registers cb to be invoked synchronously on the reporting goroutine
// for every transition of sid, and returns a stop func (idempotent). The
// record is created on first watch so a later note cannot be missed by a
// waiter that arrived first. The lease registers its suspension callback
// here — the callback replaces the select arm a channel-based API would
// need, so the lease needs no goroutine of its own.
func (ls *laneState) watch(sid session.ID, cb func()) func() {
	ls.mu.Lock()
	rec := ls.lanes[sid]
	if rec == nil {
		rec = &laneRecord{watchers: make(map[int]func())}
		ls.lanes[sid] = rec
	}
	id := rec.nextID
	rec.nextID++
	rec.watchers[id] = cb
	ls.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			ls.mu.Lock()
			if rec := ls.lanes[sid]; rec != nil {
				delete(rec.watchers, id)
			}
			ls.mu.Unlock()
		})
	}
}

// remove drops the lane's record when its session closes — a re-opened
// session of the same id is a different incarnation and must not inherit a
// stale transition. A watcher's stop after remove is a no-op.
func (ls *laneState) remove(sid session.ID) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	delete(ls.lanes, sid)
}
