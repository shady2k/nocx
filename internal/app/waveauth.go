package app

import (
	"context"
	"sync"

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
	pinner     wavepin.Pinner
	sessions   waveAuthSessions
	enrolments waveAuthEnrolments
	slots      waveCallerSlots
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
	return assistant.WaveInvocation{
		Context:    context.Background(),
		RunContext: agenttools.RunContext{Session: string(admitted)},
		Grant:      waveCallerGrant(admitted),
	}, release, nil
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
