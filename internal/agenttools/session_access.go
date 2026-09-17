package agenttools

// session.read's capability constructor for the surface that can now reach
// a DESCENDANT's pane, not only the run's own session (design §4.1, §7.1,
// Task 8).

import "github.com/shady2k/nocx/internal/content"

// SessionDescendantCapability is session.read's, session.keys' and
// session.message's shared capability: the run's own session reader —
// unchanged, still grant-scoped exactly as narrowSession always built it,
// for the ledger/renderer path (design §11 "kept, deliberately") — plus the
// run's PaneAccess, SessionReads, SessionKeys and SessionMessages, carried
// through untouched from RunContext.
//
// PaneAccess, SessionReads, SessionKeys and SessionMessages travel as `any`
// because this package sits below internal/app (which binds the concrete
// DescendantPaneAccess, PaneReader, PaneKeys and PaneMessages) and below
// internal/assistant (whose executeSessionRead/executeSessionKeys/
// executeSessionMessage are the places that type-assert them back). Narrow
// does not need to know any of them concretely: it only carries them from
// RunContext to the executor, which is the "point of use"
// RunContext.PaneAccess's own doc already names.
type SessionDescendantCapability struct {
	*SessionReader
	PaneAccess      any
	SessionReads    any
	SessionKeys     any
	SessionMessages any
}

// narrowDescendants is session.read's and session.keys' shared Narrow
// (registry.go's declaration rows). The own-session reader is built exactly
// as narrowSession already does — resources still narrow it to the grant's
// session scopes, for the run's own pane — and PaneAccess/SessionReads/
// SessionKeys pass through from runCtx untouched: a sessionId naming a
// descendant is authorized by PaneAccess alone, never by the grant's
// session scopes, which only ever name the run's own session (§7.1).
func narrowDescendants(grant content.Grant, resources []ResourceRef, runCtx RunContext) (Capability, error) {
	own, err := narrowSession(grant, resources, runCtx)
	if err != nil {
		return nil, err
	}
	reader, _ := own.(*SessionReader)
	return &SessionDescendantCapability{
		SessionReader:   reader,
		PaneAccess:      runCtx.PaneAccess,
		SessionReads:    runCtx.SessionReads,
		SessionKeys:     runCtx.SessionKeys,
		SessionMessages: runCtx.SessionMessages,
	}, nil
}
