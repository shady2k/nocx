package agenttools

import "github.com/shady2k/nocx/internal/content"

// SessionReader is the narrowed capability shared by session.list and
// session.read. It carries only the session ids named by the run's grant;
// the ledger and renderer seams are supplied separately by the assistant.
type SessionReader struct {
	sessions       map[string]struct{}
	sessionID      string
	automaticItems map[string]struct{}
	markedWindows  map[string]MarkedSessionWindow
}

// NewSessionReader adds renderer-owned item ids whose session.read calls must
// use the current renderer screen rather than a durable ledger row. These ids
// are validated as part of the ask envelope and remain scoped to this run's
// session grant. Pass nil for an ordinary session reader.
func NewSessionReader(
	scopes []content.GrantScope,
	automaticItems []string,
	markedWindows []MarkedSessionWindow,
) *SessionReader {
	r := &SessionReader{
		sessions:       make(map[string]struct{}),
		automaticItems: make(map[string]struct{}, len(automaticItems)),
		markedWindows:  make(map[string]MarkedSessionWindow, len(markedWindows)),
	}
	for _, mark := range markedWindows {
		if mark.ItemID != "" && mark.Count > 0 {
			r.markedWindows[mark.ItemID] = mark
		}
	}
	for _, item := range automaticItems {
		if item != "" {
			r.automaticItems[item] = struct{}{}
		}
	}
	for _, scope := range scopes {
		if scope.Kind == content.ResourceSession && scope.ID != "" {
			r.sessions[scope.ID] = struct{}{}
		}
	}
	if len(r.sessions) == 1 {
		for id := range r.sessions {
			r.sessionID = id
		}
	}
	return r
}

// IsAutomaticItem reports whether id is a renderer-owned screen attachment
// for this run. The caller must still enforce the session grant separately.
func (r *SessionReader) IsAutomaticItem(id string) bool {
	if r == nil || id == "" {
		return false
	}
	_, ok := r.automaticItems[id]
	return ok
}

func (r *SessionReader) Allows(sessionID string) bool {
	if r == nil || sessionID == "" {
		return false
	}
	_, ok := r.sessions[sessionID]
	return ok
}

// SessionID returns the sole session resolved for this call. A run grant
// normally names one pane; multiple scopes deliberately do not choose one.
func (r *SessionReader) SessionID() string {
	if r == nil {
		return ""
	}
	return r.sessionID
}

// MarkedWindow reports the row span a person marked on this item, when they
// marked one. The run carries it from the ask envelope, so it is authority
// about what the question is about — not a hint the model may improve on.
func (r *SessionReader) MarkedWindow(id string) (MarkedSessionWindow, bool) {
	if r == nil || id == "" {
		return MarkedSessionWindow{}, false
	}
	mark, ok := r.markedWindows[id]
	return mark, ok
}
