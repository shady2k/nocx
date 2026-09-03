package agenttools

import "github.com/shady2k/nocx/internal/content"

// RunWatcher is the narrowed capability session.wait executes through: the
// continuation of a command whose quiet bound asked the model whether to
// keep waiting (ADR-0020 decision 2's renewable clause, nocx-6dzxq).
//
// WHY THIS IS A SECOND TYPE AND NOT Runner. The middleware's
// executeInRenderer dispatches by capability type, and that type switch is
// the exhaustiveness proof — a new renderer-executed tool extends it or it
// does not compile. Two tools sharing one capability type would silently
// route both to the same executor, which is exactly the failure the switch
// exists to make impossible. It holds the same authority Runner holds (the
// grant's ResourceSession scopes and nothing else), because the right to
// wait on a command travels with the right to have started it.
//
// AND WHY session.wait IS A SECOND TOOL AND NOT AN ARGUMENT ON session.run,
// which is the first thing AGENTS.md tells you to check. It cannot be one:
// session.run is a COMMAND CARRIER, and the pipeline classifies its call
// effect from the command text (kernel.classifyCall). A continuation carries
// no command, so it would classify as the declaration's worst reachable
// effect — mutate-destructive — and every "keep waiting" would raise an
// approval and wake the person. Which is the one thing this whole mechanism
// exists to avoid: the model was handed the question precisely so that
// nobody would be woken for it.
type RunWatcher struct {
	sessions  map[string]struct{}
	sessionID string
}

// NewRunWatcher builds the narrowed capability from the grant's session
// scopes. A grant with no session scope builds a capability that refuses
// every continuation, for the reason NewRunner's does: the tool can never
// exceed the grant because it never holds more than the grant.
func NewRunWatcher(scopes []content.GrantScope) *RunWatcher {
	w := &RunWatcher{sessions: make(map[string]struct{})}
	for _, sc := range scopes {
		if sc.Kind == content.ResourceSession && sc.ID != "" {
			w.sessions[sc.ID] = struct{}{}
		}
	}
	if len(w.sessions) == 1 {
		for id := range w.sessions {
			w.sessionID = id
		}
	}
	return w
}

// Allows reports whether sessionID is inside the grant.
func (w *RunWatcher) Allows(sessionID string) bool {
	if w == nil || sessionID == "" {
		return false
	}
	_, ok := w.sessions[sessionID]
	return ok
}

// SessionID returns the sole session resolved for this call.
func (w *RunWatcher) SessionID() string {
	if w == nil {
		return ""
	}
	return w.sessionID
}
