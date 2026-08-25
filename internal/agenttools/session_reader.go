package agenttools

import "github.com/shady2k/nocx/internal/content"

// SessionReader is the narrowed capability shared by session.list and
// session.read. It carries only the session ids named by the run's grant;
// the ledger and renderer seams are supplied separately by the assistant.
type SessionReader struct {
	sessions map[string]struct{}
}

func NewSessionReader(scopes []content.GrantScope) *SessionReader {
	r := &SessionReader{sessions: make(map[string]struct{})}
	for _, scope := range scopes {
		if scope.Kind == content.ResourceSession && scope.ID != "" {
			r.sessions[scope.ID] = struct{}{}
		}
	}
	return r
}

func (r *SessionReader) Allows(sessionID string) bool {
	if r == nil || sessionID == "" {
		return false
	}
	_, ok := r.sessions[sessionID]
	return ok
}
