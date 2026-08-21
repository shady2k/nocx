package agenttools

import "github.com/shady2k/nocx/internal/content"

// Runner is the narrowed capability a run tool executes through (design
// §4.1: the agent runs a command in a session — its lane — through the same
// submit path a person uses, executed by the renderer). It holds EXACTLY
// the grant's ResourceSession scopes and nothing else: the tool can ask to
// run only in the sessions the grant names, because it never holds the
// identity of any other session (ADR-0028 decision 4 — the dispatcher
// narrows, it does not check).
//
// The shape deliberately mirrors ScreenReader: the middleware's
// executeInRenderer dispatches by capability type, so each renderer-executed
// tool gets its own type and the branch's type switch is the exhaustiveness
// proof (a third InRenderer tool extends the switch or it does not compile).
// The session set is the authority; how a run is performed (the renderer
// request) is wired separately at the run, so this type is pure authority
// and stays trivially testable. Out-of-grant sessions are refused here,
// before any request could name them — criterion 4 of nocx-tjppv.
type Runner struct {
	sessions map[string]struct{}
}

// NewRunner builds the narrowed capability from the grant's session scopes.
// A grant with no session scope builds a capability that refuses every run —
// the tool can never exceed the grant because it never holds more than the
// grant's sessions.
func NewRunner(scopes []content.GrantScope) *Runner {
	r := &Runner{sessions: make(map[string]struct{})}
	for _, sc := range scopes {
		if sc.Kind == content.ResourceSession && sc.ID != "" {
			r.sessions[sc.ID] = struct{}{}
		}
	}
	return r
}

// Allows reports whether sessionID is inside the grant. The executor checks
// this BEFORE any renderer request: a request naming a session outside the
// grant never leaves the process (criterion 4 — asserted by trying).
func (r *Runner) Allows(sessionID string) bool {
	if r == nil || sessionID == "" {
		return false
	}
	_, ok := r.sessions[sessionID]
	return ok
}
