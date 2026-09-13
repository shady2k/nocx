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
// as the child to check after the backend-owned root has been pinned. Get is
// here for the other fact the registry owns — the session itself, which is
// what an approval's trust domain is derived from (agent_approval.go's
// sessionDomain): the machine an answer is keyed to is the session's own
// route, and nothing but the registry holds it.
type workerAuthSessions interface {
	List() []session.Session
	Get(session.ID) (session.Session, error)
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
	// Interval answers whether the session's answer still holds, and names the
	// EPOCH of the interval it holds in. One call and not two, because the two
	// facts are one read: a verdict from one interval and an epoch from the
	// next is the gap the epoch exists to close.
	Interval(sid session.ID, scope string) (toolendpoint.AdmissionEpoch, bool)
}

// workerAuthAuthorityEnding is implemented by an approval seam whose answers
// can stop holding while a connection admitted under one is still open. The
// authorizer binds its closer here, where both are in hand, rather than leaving
// a composition root — or a test that builds these two by hand — a second
// wiring step to remember.
//
// It names the interval that ended. A revocation can end several sessions'
// intervals at once, and a withdrawal can race the enrolment that reopens the
// same session under a NEW epoch: a closer that took only the session would
// have to ask which interval had ended, and the answer a moment later is the
// one that replaced it.
type workerAuthAuthorityEnding interface {
	BindAuthorityEnded(ended func(session.ID, toolendpoint.AdmissionEpoch))
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
		binder.BindAuthorityEnded(authorizer.retire)
	}
	return authorizer, nil
}

// retire ends the authority one interval carried: nothing may be published into
// it again, and every connection already published into it is closed — which
// cancels the calls in flight on it.
//
// It takes the epoch and not only the session because a revocation can end
// several sessions' intervals and a withdrawal can race the re-enrolment that
// reopens the same session: a closer that knew only the session would have to
// ask which interval had ended, and the answer a moment later is the one that
// replaced it. With the epoch in hand, the connection admitted under the ended
// interval is closed and the one admitted under its successor is not
// (nocx-9mn6z).
func (a *toolAuthorizer) retire(sid session.ID, epoch toolendpoint.AdmissionEpoch) {
	if a == nil || a.admissions == nil || sid == "" || epoch == 0 {
		return
	}
	_ = a.admissions.RetireAdmissions(string(sid), epoch)
}

func (a *toolAuthorizer) admittedPeer(peer toolendpoint.Peer) (session.ID, session.Session, toolendpoint.AdmissionEpoch, bool) {
	if a == nil || a.pinner == nil || a.sessions == nil || a.enrolments == nil {
		return "", nil, 0, false
	}
	// THE ASSERTED PANE, and it is a different answer rather than a shortcut
	// through the local one (nocx-50w7p.16). A far agent has no pid here: what
	// its connection carries is the pane the helper accepted it on, and the
	// helper is the only party that knows it. The peer's pid is not consulted
	// on this arm at all — it is the HELPER's, and matching it against a
	// process tree would admit whatever tree the helper happens to be in.
	if peer.Pane != "" {
		return a.admittedPane(peer.Pane)
	}
	if peer.PID <= 0 {
		return "", nil, 0, false
	}
	var admitted session.ID
	var admittedSession session.Session
	var admittedEpoch toolendpoint.AdmissionEpoch
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
		epoch, live := a.approval.Interval(sid, agentToolEndpointScopePrefix+a.workspace)
		if !live {
			continue
		}
		if admitted != "" {
			// A peer matching two live enrolled roots has no unambiguous
			// session authority. Refuse rather than selecting map order.
			return "", nil, 0, false
		}
		admitted = sid
		admittedSession = sess
		admittedEpoch = epoch
	}
	return admitted, admittedSession, admittedEpoch, admitted != ""
}

// admittedPane decides authority for a connection this machine's helper
// forwarded, from the pane the helper named (nocx-50w7p.16).
//
// Three facts, and each refuses a different accident:
//
//   - the session is one THIS coordinator holds. A helper's record names a
//     session; a session this coordinator does not have is not a pane it may
//     answer for, so a name that belongs to another coordinator's pane
//     resolves to nothing here.
//   - it is a REMOTE session, and this is the fact that keeps the arm from
//     becoming a way to claim a LOCAL pane. An agent in a local pane can dial
//     the endpoint itself and prefix a record naming its own session; what
//     answers for it is the kernel pin, and what makes that unambiguous is
//     that a record is never honoured for a local pane. So the two rules never
//     both apply to one session.
//   - it is enrolled and its answer is live. The epoch comes from the same
//     Interval read the local arm makes, so a far agent's admission opens and
//     closes with the approval interval exactly as a local one's does — and
//     the publication below is the same publication, into the same record the
//     session's end retires.
func (a *toolAuthorizer) admittedPane(pane string) (session.ID, session.Session, toolendpoint.AdmissionEpoch, bool) {
	sid := session.ID(pane)
	if sid == "" {
		return "", nil, 0, false
	}
	sess, err := a.sessions.Get(sid)
	if err != nil || sess == nil {
		return "", nil, 0, false
	}
	if sess.Kind() != session.KindRemote {
		return "", nil, 0, false
	}
	if !a.enrolments.Watched(pane) {
		return "", nil, 0, false
	}
	epoch, live := a.approval.Interval(sid, agentToolEndpointScopePrefix+a.workspace)
	if !live {
		return "", nil, 0, false
	}
	return sid, sess, epoch, true
}

func (a *toolAuthorizer) SessionForPeer(peer toolendpoint.Peer) (string, bool) {
	admitted, _, _, ok := a.admittedPeer(peer)
	return string(admitted), ok
}

// Admit decides authority for one connection, and PUBLISHES the decision
// through the endpoint's callback before it returns.
//
// THE PUBLICATION IS THE LAST STEP OF THE DECISION and not a bookkeeping step
// after it (nocx-9mn6z). The epoch and the verdict are read together, the whole
// invocation is built from them, and only then is the connection recorded under
// that epoch — so the two events a withdrawal can interleave with are one: the
// interval's end either lands before this read, where the verdict comes back
// not-live and nothing is admitted, or after this publication, where the
// retirement finds the connection and closes it. There is no window in which a
// decision taken in an interval that has ended is recorded as a live admission.
//
// publish answering false is that race seen from this side: the interval ended
// while this decision was being taken. The connection was refused by the
// endpoint, so this grants nothing and the caller is told what a peer outside
// an enrolled pane is told — because that is now the fact.
func (a *toolAuthorizer) Admit(peer toolendpoint.Peer, publish func(session string, epoch toolendpoint.AdmissionEpoch) bool) (assistant.ToolInvocation, func(), error) {
	admitted, admittedSession, epoch, ok := a.admittedPeer(peer)
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
	//
	// The whole invocation is composed BEFORE the publication below, which is
	// the last thing this decision does: a connection is never recorded while
	// the grant inside it is still being built.
	participant, participantErr := a.participantOf(admitted)
	var invocation assistant.ToolInvocation
	if participantErr == nil {
		invocation = assistant.ToolInvocation{
			Context: context.Background(),
			RunContext: agenttools.RunContext{
				Session:     string(admitted),
				Participant: string(participant.ID),
				Workspace:   a.workspace,
			},
			Grant: participantGrant(a.workspace),
		}
	} else {
		// The workspace travels on BOTH invocations, and it is not an
		// authority: the coordinator's pane is in this workspace too, and
		// naming it is what lets a participant call resolve its resource and
		// then be refused by the GRANT, which carries no workspace scope.
		// Leaving it empty here would make the same refusal arrive as "invalid
		// params" from a resolver that could not name a resource — true of the
		// resolver, misleading about the call, and it would put an authority
		// answer in the parameter layer.
		invocation = assistant.ToolInvocation{
			Context: context.Background(),
			RunContext: agenttools.RunContext{
				Session:   string(admitted),
				Workspace: a.workspace,
			},
			Grant: callerGrant(admitted, workerEnvironmentForSession(admittedSession)),
		}
	}
	if !publish(string(admitted), epoch) {
		release()
		return assistant.ToolInvocation{}, nil, toolendpoint.ErrNotEnrolled
	}
	return invocation, release, nil
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
