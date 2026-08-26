package transport

// The per-session policy overlay — what a person answered "allow in this
// session" (or "deny in this session") to, and where it dies.
//
// This is the first per-session store on the assistant path. ApprovalStore is
// process-lifetime and keyed by the exact proposal, so it cannot host this: an
// answer that covers the NEXT proposal is a different fact from an answer that
// covered one, and it must expire on a different event.
//
// The store and the drop live in one file on purpose. The permission's whole
// promise is that it does not outlive its session, and that promise is kept by
// a call at every teardown path — three of them today, the three sites in
// ws.go that call gitSessionClosed. A store defined somewhere else is a store
// whose drop gets forgotten when a fourth appears.
//
// Nothing persists it: a session-scoped permission that survived a restart
// would be one in name only (spec, "The session layer").

import (
	"sync"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/session"
)

type sessionPolicyStore struct {
	mu sync.RWMutex
	by map[session.ID]content.SessionOverrides
}

func newSessionPolicyStore() *sessionPolicyStore {
	return &sessionPolicyStore{by: make(map[session.ID]content.SessionOverrides)}
}

// Set records one answer for one session.
func (s *sessionPolicyStore) Set(sid session.ID, e content.Effect, d content.Decision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.by[sid] == nil {
		s.by[sid] = make(content.SessionOverrides, 1)
	}
	s.by[sid][e] = d
}

// For returns a COPY of one session's overlay. A copy because the resolver
// reads it without the lock, and a map handed out under a read lock is a map
// read after the lock is gone. Never nil: an unknown session is an empty
// overlay, which is "no override" and therefore asks — an absent answer is
// not a permit.
func (s *sessionPolicyStore) For(sid session.ID) content.SessionOverrides {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.by[sid]
	out := make(content.SessionOverrides, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// Drop forgets every answer of one session. Called from every session
// teardown path; see the file comment.
func (s *sessionPolicyStore) Drop(sid session.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.by, sid)
}
