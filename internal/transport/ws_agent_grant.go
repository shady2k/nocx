package transport

import (
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/session"
)

// ── the run's grant ────────────────────────────────────────────────────────

// runGrantFor mints the default grant of ONE ask run (ADR-0020 decision 5:
// authority is granted per run, immutable once execution starts). The policy
// is the matrix of the amended §7, resolved by content.ResolvePolicy — the
// ONE place the order is stated: the workspace override when nocx-mp2vd
// lands, the global default now (the store the composition root wired).
// The mint supplies the run's own session, the whole accessible local
// filesystem, the content root, and direct network destinations as the RUN
// FENCE. The destination wildcard means "any destination reached through the
// direct route"; httppolicy remains the authority that refuses private
// redirects or unsafe addresses.
// EffectPolicy.AsGrant keeps these fence scopes distinct from each policy
// row's POLICY SELECTOR: a selector is intersected with the fence, while an
// empty selector applies across it. This is why a narrow path row remains
// narrow even though the run's outer path fence is "/"; the two lists are not
// unioned.
//
// This is the workspace's default grant: the workspace concept (which
// sessions read as one story) is not wired yet, so the single-session
// default is the honest first form, and the workspace bead rehosts the
// mint. Unset, the run carries no grant — the model is offered no tools,
// which is the state before assistant tools AND the deliberate production
// state until a policy is named: naming one is the one-line flip this seam
// makes, and session.read over-the-wire tests prove the grant path with the
// policy named at the harness.
func (s *WSServer) runGrantFor(sessionID string) *content.Grant {
	if s.agentPolicy == nil {
		return nil
	}
	return s.runGrantFrom(s.agentPolicy.Policy(), sessionID)
}

// runGrantFrom is the mint itself, over ONE stated global document.
//
// It is split out for a caller that has a document the store does not hold
// yet: a matrix write asks each live run whether it would decide differently
// once the write lands, and the only honest way to ask is to mint that run's
// authority AGAIN from the document the write leaves behind and compare the
// two (runsUnreachedByRowWrite). That question must cross the same overlay,
// the same session-selector rule and the same fence as the real mint, or it
// would answer about an authority nobody would ever hold — so it crosses THIS
// function, and there is no second mint to keep in step with.
func (s *WSServer) runGrantFrom(global content.EffectPolicy, sessionID string) *content.Grant {
	// The session's own answers overlay the global policy — an "allow in
	// this session" is in force from the answer until the session ends, and
	// the store (ws_sessionpolicy.go) is what ends it. The run grant's base
	// scope is already this session, so the overlay carries no scope of its
	// own: the run cannot reach outside its session anyway.
	p := content.ResolvePolicy(global, nil, s.sessionPolicy.For(session.ID(sessionID)))
	// The endpoint's session is the run-authoritative session resource. A
	// policy may narrow paths or content, but an operator session selector
	// names a different domain and must not erase the current session from
	// the run. Drop session selectors before the generic fence intersection;
	// the single-session run fence still prevents access to any other session.
	p = preserveRunSessionScope(p)
	scopes := []content.GrantScope{
		{Kind: content.ResourceSession, ID: sessionID},
		{Kind: content.ResourcePath, ID: "/"},
		{Kind: content.ResourceContent, ID: "note"},
		{Kind: content.ResourceContent, ID: "snippet"},
		{Kind: content.ResourceDestination, ID: "*"},
		// The environment a run may start a worker in (nocx-dkawo.8, A7 of
		// the worker authority model). It is the LOCAL machine and only that:
		// the spawner opens a local session, so a fence naming any other
		// environment would offer an authority nothing could deliver. A
		// remote participant needs the helper and is not this slice.
		//
		// Without this scope Registry.ForGrant's kind check silently omits
		// workers.spawn and it is never offered — which is why the scope is
		// minted here rather than assumed by the tool.
		{Kind: content.ResourceEnvironment, ID: content.EnvironmentIDFor(content.EnvLocal, "")},
	}
	if s.skillsEnabled() {
		scopes = append(scopes, content.GrantScope{Kind: content.ResourceContent, ID: "skill"})
	}
	g := p.AsGrant(scopes)
	return &g
}

// preserveRunSessionScope removes operator session selectors from a policy
// copy. The run mint owns exactly one session scope, so session authority is
// endpoint-derived while other resource selectors remain policy-controlled.
func preserveRunSessionScope(p content.EffectPolicy) content.EffectPolicy {
	rows := []*content.EffectRow{
		&p.Observe,
		&p.MutateReversible,
		&p.MutateDestructive,
		&p.PrivilegeChange,
		&p.Disclose,
		&p.CrossBoundary,
		&p.Delegate,
	}
	for _, row := range rows {
		scopes := make([]content.GrantScope, 0, len(row.Scopes))
		for _, scope := range row.Scopes {
			if scope.Kind != content.ResourceSession {
				scopes = append(scopes, scope)
			}
		}
		row.Scopes = scopes
	}
	return p
}
