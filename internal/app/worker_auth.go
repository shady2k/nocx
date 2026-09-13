package app

import (
	"context"
	"errors"
	"sync"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/peerpin"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/workers"
)

// workerAuthSessions is the launch-record view needed by the authorizer. The
// session registry is the only source of a root pid: a peer's pid is used only
// as the child to check after the backend-owned root has been pinned.
type workerAuthSessions interface {
	List() []session.Session
	OwnedProcessPID(session.ID) (int, bool)
}

// workerAuthEnrolments is the live interval opened by agent_enrol and closed by
// agent_withdraw. The pane grid is lifecycle-owned, so a session that is no
// longer watched cannot remain an admitting principal.
type workerAuthEnrolments interface {
	Watched(paneID string) bool
}

// workerAuthParticipants answers whether the admitted session is a WORKER's.
//
// The record is the only thing that can answer it. A worker calling in knows
// its session and nothing else — its participant id is backend-owned (A9) and
// never travels to the agent — and the outside route to the same answer,
// HeldBy, needs the COORDINATOR's session, which a worker has no business
// holding. Unwired, every admitted caller is a coordinator, which is what the
// endpoint did before nocx-rowqt.9 and is why workers.say had no reader.
type workerAuthParticipants interface {
	ParticipantOf(ctx context.Context, sessionID string) (workers.Participant, error)
}

type workerAuthApproval interface {
	Approved(sid session.ID, scope string) bool
}

// workerAuthAuthorityEnding is implemented by an approval seam whose answers
// can stop holding while a connection admitted under one is still open. The
// authorizer binds its closer here, where both are in hand, rather than leaving
// a composition root — or a test that builds these two by hand — a second
// wiring step to remember.
type workerAuthAuthorityEnding interface {
	BindAuthorityEnded(ended func())
}

// The slot is the coordinator seat, not a conversation gate. M1 makes talk
// mesh from day one; A1 says membership makes a participant addressable while
// delegation makes it controllable. Each participant has its own session, so
// its own calls use its own slot. The session-keyed coordinator slot therefore
// must never mute a participant's conversation.
//
// D11 is intentionally not implemented here: a lost mutation response stays
// unknown until workers.holdings reports the existing record; there is no response
// cache or idempotency promise.
type workerCallerSlots struct {
	mu   sync.Mutex
	held map[session.ID]*workerCallerSlot
}

type workerCallerSlot struct {
	once  sync.Once
	slots *workerCallerSlots
	sid   session.ID
}

func (s *workerCallerSlots) acquire(sid session.ID) (func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held == nil {
		s.held = make(map[session.ID]*workerCallerSlot)
	}
	if _, ok := s.held[sid]; ok {
		return nil, false
	}
	slot := &workerCallerSlot{slots: s, sid: sid}
	s.held[sid] = slot
	return slot.release, true
}

func (s *workerCallerSlots) release(slot *workerCallerSlot) {
	s.mu.Lock()
	if s.held[slot.sid] == slot {
		delete(s.held, slot.sid)
	}
	s.mu.Unlock()
}

func (s *workerCallerSlot) release() {
	s.once.Do(func() { s.slots.release(s) })
}

type toolAuthorizer struct {
	pinner       peerpin.Pinner
	sessions     workerAuthSessions
	enrolments   workerAuthEnrolments
	participants workerAuthParticipants
	approval     workerAuthApproval
	workspace    string
	slots        workerCallerSlots
	// admissions is the endpoint's view of the connections it admitted, bound
	// by toolendpoint.New before the socket accepts anything. Nil when no
	// endpoint was built, and nil is the honest answer then: there are no
	// intervals to close.
	admissions toolendpoint.SessionAdmissions
}

// BindSessionAdmissions implements toolendpoint.SessionAdmissionBinder.
//
// THE ADMISSION INTERVAL IS NOT RECHECKED PER CALL. A shared connection is
// admitted once (ADR-0058), so the approval that let it in is the approval it
// keeps — which is the point of the interval, and also why the interval has to
// be CLOSED when the answer behind it ends. Without this, a person revoking an
// agent's access in Settings revoked a document while the coordinator's
// connection went on working, and the next tool call ran under a grant nobody
// still held.
func (a *toolAuthorizer) BindSessionAdmissions(admissions toolendpoint.SessionAdmissions) {
	if a == nil {
		return
	}
	a.admissions = admissions
}

// CloseUnapproved closes the admitted connections whose session no longer
// holds an approval, and reports how many it closed.
//
// It re-asks the approval rather than taking an instruction to close a
// particular session, because the two ways an answer ends reach this from
// different places and neither carries the connection's identity: a revoke
// names an executable and a digest, a withdraw names a lane. What they have in
// common is the fact that matters — the answer these connections were admitted
// under has stopped holding — and the approval seam is the only thing that can
// answer it. Approvals that still hold are left alone, so one person revoking
// one agent does not drop another's work in flight.
func (a *toolAuthorizer) CloseUnapproved() int {
	if a == nil || a.admissions == nil {
		return 0
	}
	closed := 0
	for _, sid := range a.admissions.AdmittedSessions() {
		if a.approval != nil && a.approval.Approved(session.ID(sid), agentToolEndpointScopePrefix+a.workspace) {
			continue
		}
		closed += a.admissions.CloseAdmitted(sid)
	}
	return closed
}

// newToolAuthorizer builds the one external caller authorizer. It binds a
// peer only to a session whose pane is currently enrolled and whose root pid
// came from a process nocx opened itself. The peer's uid and pid never supply
// the root identity. The durable executable/scope decision is required in
// addition to the live process-tree pin.
//
// It returns the CONCRETE type because the composition root asks it for one
// thing the Authorizer interface does not carry — closing the admitted
// connections whose approval has ended — and reaching that through an
// assertion at the call site would move a wiring fact into a runtime branch.
func newToolAuthorizer(
	pinner peerpin.Pinner,
	sessions workerAuthSessions,
	enrolments workerAuthEnrolments,
	participants workerAuthParticipants,
	workspace string,
	approval workerAuthApproval,
) (*toolAuthorizer, error) {
	if approval == nil {
		return nil, errors.New("tool authorizer: no agent approval")
	}
	authorizer := &toolAuthorizer{
		pinner: pinner, sessions: sessions, enrolments: enrolments,
		participants: participants, approval: approval, workspace: workspace,
	}
	if binder, ok := approval.(workerAuthAuthorityEnding); ok {
		// The count is for whoever wants to log it; this seam is a notice.
		binder.BindAuthorityEnded(func() { _ = authorizer.CloseUnapproved() })
	}
	return authorizer, nil
}

func (a *toolAuthorizer) admittedPeer(peer toolendpoint.Peer) (session.ID, session.Session, bool) {
	if a == nil || a.pinner == nil || a.sessions == nil || a.enrolments == nil || peer.PID <= 0 {
		return "", nil, false
	}
	var admitted session.ID
	var admittedSession session.Session
	for _, sess := range a.sessions.List() {
		sid := sess.ID()
		if sid == "" || !a.enrolments.Watched(string(sid)) {
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
		if !a.approval.Approved(sid, agentToolEndpointScopePrefix+a.workspace) {
			continue
		}
		if admitted != "" {
			// A peer matching two live enrolled roots has no unambiguous
			// session authority. Refuse rather than selecting map order.
			return "", nil, false
		}
		admitted = sid
		admittedSession = sess
	}
	return admitted, admittedSession, admitted != ""
}

func (a *toolAuthorizer) SessionForPeer(peer toolendpoint.Peer) (string, bool) {
	admitted, _, ok := a.admittedPeer(peer)
	return string(admitted), ok
}

func (a *toolAuthorizer) Admit(peer toolendpoint.Peer) (assistant.ToolInvocation, func(), error) {
	admitted, admittedSession, ok := a.admittedPeer(peer)
	if !ok {
		return assistant.ToolInvocation{}, nil, toolendpoint.ErrNotEnrolled
	}

	release, acquired := a.slots.acquire(admitted)
	if !acquired {
		return assistant.ToolInvocation{}, nil, toolendpoint.ErrSessionCallerActive
	}
	// Which of the two callers this is, decided by the RECORD and never by
	// anything the peer sent. A session the record holds a live participant
	// for is a worker calling about itself; every other admitted session is a
	// coordinator calling about its workers. The two get disjoint grants, so the
	// separation is what Registry.ForGrant offers rather than a check inside
	// a shared tool.
	if participant, err := a.participantOf(admitted); err == nil {
		return assistant.ToolInvocation{
			Context: context.Background(),
			RunContext: agenttools.RunContext{
				Session:     string(admitted),
				Participant: string(participant.ID),
				Workspace:   a.workspace,
			},
			Grant: participantGrant(a.workspace),
		}, release, nil
	}
	// The workspace travels on BOTH invocations, and it is not an authority:
	// the coordinator's pane is in this workspace too, and naming it is what
	// lets a participant call resolve its resource and then be refused by the
	// GRANT, which carries no workspace scope. Leaving it empty here would
	// make the same refusal arrive as "invalid params" from a resolver that
	// could not name a resource — true of the resolver, misleading about the
	// call, and it would put an authority answer in the parameter layer.
	return assistant.ToolInvocation{
		Context: context.Background(),
		RunContext: agenttools.RunContext{
			Session:   string(admitted),
			Workspace: a.workspace,
		},
		Grant: callerGrant(admitted, workerEnvironmentForSession(admittedSession)),
	}, release, nil
}

// participantOf asks the record whether this session is a worker's. An
// unwired record answers "no", which leaves every caller a coordinator — the
// behaviour before nocx-rowqt.9, and a degrade that is at least the one that
// refuses rather than the one that over-grants.
func (a *toolAuthorizer) participantOf(sid session.ID) (workers.Participant, error) {
	if a.participants == nil {
		return workers.Participant{}, errors.New("app: no worker record to resolve a participant")
	}
	return a.participants.ParticipantOf(context.Background(), string(sid))
}

// participantGrant is what a WORKER gets, and it is a different authority
// rather than a smaller version of the coordinator's. Observe alone: reading
// your own mailbox exercises no authority over anything but your own reading
// position. Delegate is refused, so a worker cannot spawn; mutate-destructive
// is refused, so it cannot close anything, its own siblings included.
//
// It names a WORKSPACE scope and no session and no environment. That is what
// makes the two offer sets disjoint by construction (A11, and see
// agenttools.resourceParticipantWorkspace): the coordinator's four calls all
// declare session or environment kinds, which this grant does not carry, and
// workers.inbox declares the workspace kind, which no other grant in the tree
// mints. Neither caller is ever OFFERED the other's calls, so nothing has to
// refuse them.
func participantGrant(workspace string) content.Grant {
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

func workerEnvironmentForSession(sess session.Session) string {
	// Admit currently refuses sessions whose root process the backend did not
	// launch, so every admitted session is local today; derive this identity
	// from the admitted session for when remote admission exists.
	environmentKind := content.EnvLocal
	if sess.Kind() == session.KindRemote {
		environmentKind = content.EnvSSH
	}
	return content.EnvironmentIDFor(environmentKind, sess.Host())
}

func callerGrant(sid session.ID, environmentID string) content.Grant {
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
		{Kind: content.ResourceEnvironment, ID: environmentID},
	})
}

var _ toolendpoint.Authorizer = (*toolAuthorizer)(nil)
