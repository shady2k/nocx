package assistant

// The suspended-run store (ADR-0028 decision 2's "approval resumes FROM THE
// CHECKPOINT as a new attempt with a new grant", and its consequence
// "checkpoints are process-lifetime state, not records").
//
// Two facts about one suspended run live here, under one key and one
// lifetime:
//
//   - the eino checkpoint itself — the framework's own bytes, written by
//     adk.Runner when a tool call interrupts and read back when it resumes;
//   - the interrupt the person is answering — adk's InterruptCtx.ID of the
//     branch whose Info is the ApprovalRequest or the EgressRequest, which
//     is the only thing ResumeWithParams needs to say WHICH suspended
//     branch continues.
//
// They are one store rather than two because they are born together, die
// together, and are keyed by the same run: a checkpoint whose target has
// been forgotten cannot be resumed, and a target whose checkpoint is gone
// names nothing. ADR-0019 is intact — nothing here is the record of what
// happened; the ledger is. Nothing is persisted, and a restart legitimately
// loses every suspension, which is already what the recovery rule says.

import (
	"context"
	"sync"
)

// runCheckpoints is the client's process-lifetime checkpoint store. It
// implements eino's adk.CheckPointStore (Get/Set) and its optional
// CheckPointDeleter (Delete), so the framework's own bookkeeping and ours
// use one map.
//
// Concurrency-safe: one store serves every run on the server, and two runs
// suspend and resume on unrelated goroutines.
type runCheckpoints struct {
	mu     sync.Mutex
	blob   map[string][]byte
	target map[string]string
}

func newRunCheckpoints() *runCheckpoints {
	return &runCheckpoints{
		blob:   make(map[string][]byte),
		target: make(map[string]string),
	}
}

// Get implements adk.CheckPointStore.
func (s *runCheckpoints) Get(_ context.Context, id string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.blob[id]
	return b, ok, nil
}

// Set implements adk.CheckPointStore. The bytes are the framework's; we
// hold them and never read them.
func (s *runCheckpoints) Set(_ context.Context, id string, checkpoint []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blob[id] = checkpoint
	return nil
}

// Delete implements adk.CheckPointDeleter and is also how a terminal run
// drops its suspension. BOTH facts go: a run that has terminalized has no
// branch left to continue. v0.9.13 never calls this itself on a completed
// run — the store owner is responsible for the lifecycle, which the
// framework says in as many words — so every terminal path calls it.
func (s *runCheckpoints) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.blob, id)
	delete(s.target, id)
	return nil
}

// suspend records the interrupt a person is being asked about, against the
// checkpoint eino has just written for this run. Called with the id off the
// interrupt event, so the pair is complete before Ask returns the
// suspension the surface renders.
func (s *runCheckpoints) suspend(id, interruptID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.target[id] = interruptID
}

// resumable answers the ONE question a drive of a run asks: is this run
// suspended at a checkpoint, and if so which branch continues? A run with
// both facts is resumed; a run with neither is started. There is no third
// state — the two are written together and deleted together — and a
// checkpoint whose interrupt we never saw is not resumable, because
// ResumeWithParams would have nothing to target.
func (s *runCheckpoints) resumable(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.blob[id]; !ok {
		return "", false
	}
	t, ok := s.target[id]
	return t, ok && t != ""
}
