package app

import (
	"context"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waveendpoint"
	"github.com/shady2k/nocx/internal/wavepin"
)

// waveAuthSessions is the launch-record view needed by the authorizer. The
// session registry is the only source of a root pid: a peer's pid is used only
// as the child to check after the backend-owned root has been pinned.
type waveAuthSessions interface {
	List() []session.Session
	OwnedProcessPID(session.ID) (int, bool)
}

// waveAuthEnrolments is the live interval opened by agent_enrol and closed by
// agent_withdraw. The pane grid is lifecycle-owned, so a session that is no
// longer watched cannot remain an admitting principal.
type waveAuthEnrolments interface {
	Enrolled(paneID string) bool
}

type waveAuthorizer struct {
	pinner     wavepin.Pinner
	sessions   waveAuthSessions
	enrolments waveAuthEnrolments
}

// newWaveAuthorizer builds the one external caller authorizer. It binds a
// peer only to a session whose pane is currently enrolled and whose root pid
// came from a process nocx opened itself. The peer's uid and pid never supply
// the root identity.
//
// This is not D13's human approval. It admits the caller whose tree root is
// the session that enrolled through the lifecycle channel: a real act tied to
// a pane a person opened, and exactly the A12 ceiling recorded by
// nocx-rowqt.12. The interval has two ends: agent_enrol opens it and
// agent_withdraw closes it; a call from that tree after withdraw is refused.
func newWaveAuthorizer(pinner wavepin.Pinner, sessions waveAuthSessions, enrolments waveAuthEnrolments) waveendpoint.Authorizer {
	return &waveAuthorizer{pinner: pinner, sessions: sessions, enrolments: enrolments}
}

func (a *waveAuthorizer) Admit(peer waveendpoint.Peer) (assistant.WaveInvocation, error) {
	if a == nil || a.pinner == nil || a.sessions == nil || a.enrolments == nil || peer.PID <= 0 {
		return assistant.WaveInvocation{}, waveendpoint.ErrNotEnrolled
	}

	var admitted session.ID
	for _, sess := range a.sessions.List() {
		sid := sess.ID()
		if sid == "" || !a.enrolments.Enrolled(string(sid)) {
			continue
		}
		rootPID, known := a.sessions.OwnedProcessPID(sid)
		if !known {
			// A false second result is a refusal, never a zero pid to pin:
			// SSH sessions and sessions the helper did not launch are not a
			// backend-owned process tree.
			continue
		}
		root, err := a.pinner.Pin(rootPID)
		if err != nil {
			continue
		}
		member, err := a.pinner.Member(peer.PID, root)
		if err != nil || !member {
			continue
		}
		if admitted != "" {
			// A peer matching two live enrolled roots has no unambiguous
			// session authority. Refuse rather than selecting map order.
			return assistant.WaveInvocation{}, waveendpoint.ErrNotEnrolled
		}
		admitted = sid
	}
	if admitted == "" {
		return assistant.WaveInvocation{}, waveendpoint.ErrNotEnrolled
	}

	return assistant.WaveInvocation{
		Context:    context.Background(),
		RunContext: agenttools.RunContext{Session: string(admitted)},
		Grant:      waveCallerGrant(admitted),
	}, nil
}

func waveCallerGrant(sid session.ID) content.Grant {
	permit := content.EffectRow{Decision: content.DecisionPermit}
	refuse := content.EffectRow{Decision: content.DecisionRefuse}
	return content.EffectPolicy{
		Observe:           permit,
		MutateReversible:  refuse,
		MutateDestructive: permit,
		PrivilegeChange:   refuse,
		Disclose:          refuse,
		CrossBoundary:     refuse,
		Delegate:          permit,
	}.AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: string(sid)},
		{Kind: content.ResourceEnvironment, ID: content.EnvironmentIDFor(content.EnvLocal, "")},
	})
}

var _ waveendpoint.Authorizer = (*waveAuthorizer)(nil)
