package app

import (
	"context"
	"errors"
	"sync"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/wave"
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

// waveAuthParticipants answers whether the admitted session is a WORKER's.
//
// The record is the only thing that can answer it. A worker calling in knows
// its session and nothing else — its participant id is backend-owned (A9) and
// never travels to the agent — and the outside route to the same answer,
// HeldBy, needs the COORDINATOR's session, which a worker has no business
// holding. Unwired, every admitted caller is a coordinator, which is what the
// endpoint did before nocx-rowqt.9 and is why wave.say had no reader.
type waveAuthParticipants interface {
	ParticipantOf(ctx context.Context, sessionID string) (wave.Participant, error)
}

// The slot is the coordinator seat, not a conversation gate. M1 makes talk
// mesh from day one; A1 says membership makes a participant addressable while
// delegation makes it controllable. Each participant has its own session, so
// its own calls use its own slot. The session-keyed coordinator slot therefore
// must never mute a participant's conversation.
//
// D11 is intentionally not implemented here: a lost mutation response stays
// unknown until wave.holdings reports the existing record; there is no response
// cache or idempotency promise.
type waveCallerSlots struct {
	mu   sync.Mutex
	held map[session.ID]*waveCallerSlot
}

type waveCallerSlot struct {
	once  sync.Once
	slots *waveCallerSlots
	sid   session.ID
}

func (s *waveCallerSlots) acquire(sid session.ID) (func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held == nil {
		s.held = make(map[session.ID]*waveCallerSlot)
	}
	if _, ok := s.held[sid]; ok {
		return nil, false
	}
	slot := &waveCallerSlot{slots: s, sid: sid}
	s.held[sid] = slot
	return slot.release, true
}

func (s *waveCallerSlots) release(slot *waveCallerSlot) {
	s.mu.Lock()
	if s.held[slot.sid] == slot {
		delete(s.held, slot.sid)
	}
	s.mu.Unlock()
}

func (s *waveCallerSlot) release() {
	s.once.Do(func() { s.slots.release(s) })
}

type waveAuthorizer struct {
	pinner       wavepin.Pinner
	sessions     waveAuthSessions
	enrolments   waveAuthEnrolments
	participants waveAuthParticipants
	workspace    string
	slots        waveCallerSlots
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
func newWaveAuthorizer(
	pinner wavepin.Pinner,
	sessions waveAuthSessions,
	enrolments waveAuthEnrolments,
	participants waveAuthParticipants,
	workspace string,
) waveendpoint.Authorizer {
	return &waveAuthorizer{
		pinner: pinner, sessions: sessions, enrolments: enrolments,
		participants: participants, workspace: workspace,
	}
}

func (a *waveAuthorizer) Admit(peer waveendpoint.Peer) (assistant.WaveInvocation, func(), error) {
	if a == nil || a.pinner == nil || a.sessions == nil || a.enrolments == nil || peer.PID <= 0 {
		return assistant.WaveInvocation{}, nil, waveendpoint.ErrNotEnrolled
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
			return assistant.WaveInvocation{}, nil, waveendpoint.ErrNotEnrolled
		}
		admitted = sid
	}
	if admitted == "" {
		return assistant.WaveInvocation{}, nil, waveendpoint.ErrNotEnrolled
	}

	release, acquired := a.slots.acquire(admitted)
	if !acquired {
		return assistant.WaveInvocation{}, nil, waveendpoint.ErrSessionCallerActive
	}
	// Which of the two callers this is, decided by the RECORD and never by
	// anything the peer sent. A session the record holds a live participant
	// for is a worker calling about itself; every other admitted session is a
	// coordinator calling about its wave. The two get disjoint grants, so the
	// separation is what Registry.ForGrant offers rather than a check inside
	// a shared tool.
	if participant, err := a.participantOf(admitted); err == nil {
		return assistant.WaveInvocation{
			Context: context.Background(),
			RunContext: agenttools.RunContext{
				Session:     string(admitted),
				Participant: string(participant.ID),
				Workspace:   a.workspace,
			},
			Grant: waveParticipantGrant(a.workspace),
		}, release, nil
	}
	// The workspace travels on BOTH invocations, and it is not an authority:
	// the coordinator's pane is in this workspace too, and naming it is what
	// lets a participant call resolve its resource and then be refused by the
	// GRANT, which carries no workspace scope. Leaving it empty here would
	// make the same refusal arrive as "invalid params" from a resolver that
	// could not name a resource — true of the resolver, misleading about the
	// call, and it would put an authority answer in the parameter layer.
	return assistant.WaveInvocation{
		Context: context.Background(),
		RunContext: agenttools.RunContext{
			Session:   string(admitted),
			Workspace: a.workspace,
		},
		Grant: waveCallerGrant(admitted),
	}, release, nil
}

// participantOf asks the record whether this session is a worker's. An
// unwired record answers "no", which leaves every caller a coordinator — the
// behaviour before nocx-rowqt.9, and a degrade that is at least the one that
// refuses rather than the one that over-grants.
func (a *waveAuthorizer) participantOf(sid session.ID) (wave.Participant, error) {
	if a.participants == nil {
		return wave.Participant{}, errors.New("app: no wave record to resolve a participant")
	}
	return a.participants.ParticipantOf(context.Background(), string(sid))
}

// waveParticipantGrant is what a WORKER gets, and it is a different authority
// rather than a smaller version of the coordinator's. Observe alone: reading
// your own mailbox exercises no authority over anything but your own reading
// position. Delegate is refused, so a worker cannot spawn; mutate-destructive
// is refused, so it cannot close anything, its own siblings included.
//
// It names a WORKSPACE scope and no session and no environment. That is what
// makes the two offer sets disjoint by construction (A11, and see
// agenttools.resourceParticipantWorkspace): the coordinator's four calls all
// declare session or environment kinds, which this grant does not carry, and
// wave.inbox declares the workspace kind, which no other grant in the tree
// mints. Neither caller is ever OFFERED the other's calls, so nothing has to
// refuse them.
func waveParticipantGrant(workspace string) content.Grant {
	permit := content.EffectRow{Decision: content.DecisionPermit}
	refuse := content.EffectRow{Decision: content.DecisionRefuse}
	return content.EffectPolicy{
		Observe:           permit,
		MutateReversible:  refuse,
		MutateDestructive: refuse,
		PrivilegeChange:   refuse,
		Disclose:          refuse,
		CrossBoundary:     refuse,
		Delegate:          refuse,
	}.AsGrant([]content.GrantScope{
		{Kind: content.ResourceWorkspace, ID: agenttools.ParticipantWorkspaceScopeID(workspace)},
	})
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
