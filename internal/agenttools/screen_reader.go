package agenttools

import "github.com/shady2k/nocx/internal/content"

// ScreenReader is the narrowed capability a readScreen tool executes through
// (design §4.1: the agent reads the screen through the renderer, because the
// renderer owns the grid — AD-6). It holds EXACTLY the grant's ResourceSession
// scopes and nothing else: the tool can ask to read only the sessions the
// grant names, because it never holds the identity of any other session
// (ADR-0028 decision 4 — the dispatcher narrows, it does not check).
//
// The session set is the authority; how a read is performed (the renderer
// request) is wired separately at the run, so this type is pure authority and
// stays trivially testable. Out-of-grant sessions are refused here, before
// any request could name them.
type ScreenReader struct {
	sessions map[string]struct{}
}

// NewScreenReader builds the narrowed capability from the grant's session
// scopes. A grant with no session scope builds a capability that refuses
// every read — the tool can never exceed the grant because it never holds
// more than the grant's sessions.
func NewScreenReader(scopes []content.GrantScope) *ScreenReader {
	s := &ScreenReader{sessions: make(map[string]struct{})}
	for _, sc := range scopes {
		if sc.Kind == content.ResourceSession && sc.ID != "" {
			s.sessions[sc.ID] = struct{}{}
		}
	}
	return s
}

// Allows reports whether sessionID is inside the grant. The executor checks
// this BEFORE any renderer request: a request naming a session outside the
// grant never leaves the process (criterion 2 — asserted by trying).
func (s *ScreenReader) Allows(sessionID string) bool {
	if s == nil || sessionID == "" {
		return false
	}
	_, ok := s.sessions[sessionID]
	return ok
}
