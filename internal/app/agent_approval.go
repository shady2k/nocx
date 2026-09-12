package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/shady2k/nocx/internal/agentapproval"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
)

const agentToolEndpointScopePrefix = "tool-endpoint:"

type hostApprovalRequester interface {
	RequestHost(context.Context, transport.HostAsk) (transport.HostAnswer, error)
}

// agentApprovalService joins durable human intent to the live process pin.
// The store answers whether this exact executable digest and scope was already
// decided; the requester is used only for an unanswered identity. A path-only
// identity would authorize a replaced binary, so the digest is displayed and
// keyed too. This does not defend against a same-UID process that can replace
// an approved executable and arrange the same digest; the kernel's (pid,
// start-time) pin remains the separate runtime defence.
//
// THE IDENTITY IS THE AGENT, NOT THE PANE'S SHELL (nocx-opiq5). It was the
// shell: the identity came from the session's owned process, which is the
// login shell nocx opened, so the key was /bin/bash's path and digest — the
// same in every pane on the machine. One yes therefore admitted every LATER
// agent silently (approve claude, and codex came in behind it), and one no
// refused every later agent with no question and no way to answer. The dialog
// meanwhile promised "this agent", and named a nix-store path to bash that no
// person could recognise as the thing they had typed. IdentityForExecutable
// existed for this and had no caller.
//
// What is now keyed is what the person was shown and typed: the executable the
// BACKEND resolves that agent name to. What this cannot verify is that the
// shell went on to exec that same file — the shell is inside the tree being
// granted, and a tree is the grant's unit by D13, so its claim about which
// agent it is launching is a claim. The live pin still proves the connecting
// peer belongs to a pane the person opened and enrolled; that is the fact this
// identity does not supply, and the reason both are required.
type agentApprovalService struct {
	sessions  workerAuthSessions
	store     *agentapproval.Store
	requester hostApprovalRequester
	scope     string
	workspace string

	// What was approved for a live enrolment, so the admit-time check reads
	// the same identity the person answered about. The ANSWER is still read
	// from the store on every call rather than cached here: a grant that was
	// withdrawn must stop admitting live panes, not only new ones.
	mu       sync.Mutex
	enrolled map[session.ID]agentapproval.Executable
	// Identities with a question on screen, and every enrolment waiting on
	// it. Starting the agent again while the person is still reading must not
	// put a second copy of the question in the queue — it is the same
	// decision, and a stack of identical dialogs is how a person clicks one
	// without reading it — so a second start joins the first one's wait.
	asking map[string][]chan string

	// authorityEnded is called whenever an answer stops holding — a revoke, a
	// withdrawn enrolment — so the tool endpoint can close the connections
	// admitted under it. Bound by the composition root; nil means no endpoint
	// was built, and nothing is admitted to close.
	authorityEnded func()
}

func newAgentApprovalService(sessions workerAuthSessions, store *agentapproval.Store, scope string) *agentApprovalService {
	return &agentApprovalService{
		sessions: sessions, store: store,
		scope:     agentToolEndpointScopePrefix + scope,
		workspace: scope,
		enrolled:  map[session.ID]agentapproval.Executable{},
		asking:    map[string][]chan string{},
	}
}

func (s *agentApprovalService) SetRequester(requester hostApprovalRequester) {
	s.requester = requester
}

// Approved answers for the AGENT this session enrolled, which is the identity
// the person was asked about. A session that never enrolled an agent has none,
// and is refused rather than falling back to some other identity the process
// tree happens to offer.
func (s *agentApprovalService) Approved(sid session.ID, scope string) bool {
	s.mu.Lock()
	executable, known := s.enrolled[sid]
	s.mu.Unlock()
	if !known {
		return false
	}
	answer, ok := s.store.Lookup(executable, scope)
	return ok && answer == agentapproval.Granted
}

// Forget drops what an enrolment approved when that enrolment ends, so the map
// follows the live intervals rather than growing for the life of the backend.
//
// THE INTERVAL ENDS WITH THE ANSWER. An enrolment that stops approving is also
// an admission that must stop being usable: the endpoint admits a connection
// once (ADR-0058), so a live one carries the approval it was let in under and
// nothing re-reads it per call. Ending the answer is therefore half the work,
// and the endpoint is told before this returns.
func (s *agentApprovalService) Forget(sid session.ID) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.enrolled, sid)
	s.mu.Unlock()
	s.endAuthority()
}

// BindAuthorityEnded takes what closes the tool-endpoint connections admitted
// under an answer that has stopped holding. newToolAuthorizer calls it while
// both are in hand; an approval seam that never gets one is one whose sessions
// nothing can admit, so there is nothing to close.
func (s *agentApprovalService) BindAuthorityEnded(ended func()) {
	if s == nil {
		return
	}
	s.authorityEnded = ended
}

// endAuthority tells the tool endpoint that an answer has stopped holding, so
// the connections admitted under it are closed. Nil before the authorizer binds
// it, and nil is safe: an endpoint that was never built admits nobody.
func (s *agentApprovalService) endAuthority() {
	if s == nil || s.authorityEnded == nil {
		return
	}
	s.authorityEnded()
}

func (s *agentApprovalService) Approve(ctx context.Context, sid session.ID, agent string) error {
	if s == nil || s.sessions == nil || s.store == nil {
		return errors.New("nocx cannot ask for agent approval")
	}
	// Not for the identity — for the provenance. A session with no
	// backend-owned process is not a tree nocx launched (an ssh pane, a
	// session the helper did not start), and the authorizer refuses one at
	// admit time; minting a durable workspace answer from it would record a
	// yes that nothing could ever use.
	if _, ok := s.sessions.OwnedProcessPID(sid); !ok {
		return errors.New("nocx cannot identify the enrolled agent executable")
	}
	executable, err := agentapproval.IdentityForExecutable(agent)
	if err != nil {
		return err
	}
	// THE QUESTION COMES FIRST, AND THE HANDSHAKE DOES NOT WAIT FOR IT.
	//
	// The two deadlines are irreconcilable and both are right. The shell gives
	// an enrolment five seconds (nocx.bash __nocx_lc_grant_timeout_s) because
	// a handshake between two programs must be bounded. The approval question
	// has no deadline at all (transport.hostTimeoutFor) because it waits on
	// somebody reading it. Waiting for the second inside the first meant a
	// person could never answer in time (nocx-t7xds). Refusing instead meant
	// the shell did what it does with a refusal — it started the agent without
	// tools while the dialog was still up, and told the person to start it
	// again after answering (nocx-cyhfw).
	//
	// So the verdict is a WAIT: returned now, inside the handshake's bound,
	// holding a channel that closes when the question does. The shell waits
	// on it outside the handshake and enrols again once it closes, and that
	// enrolment reads the stored answer here like any other.
	//
	// Decided under the lock, the store read included. An ask that finished
	// between a lookup and joining its wait would otherwise leave this
	// enrolment waiting on a question that had already closed, or put the
	// question the person had just answered back on screen. The ask records
	// before it settles, so under the lock the store is never behind.
	s.mu.Lock()
	answer, found := s.store.Lookup(executable, s.scope)
	switch {
	case found && answer == agentapproval.Granted:
		s.enrolled[sid] = executable
		s.mu.Unlock()
		return nil
	case found:
		s.mu.Unlock()
		return fmt.Errorf("agent approval was denied for %s", agent)
	case s.requester == nil:
		s.mu.Unlock()
		return errors.New("nocx has no client to ask for agent approval")
	}
	k := askingKey(executable)
	waiters, up := s.asking[k]
	settled := make(chan string, 1)
	s.asking[k] = append(waiters, settled)
	s.mu.Unlock()
	if !up {
		go s.ask(executable, agent)
	}
	return &lifecyclepub.EnrolmentPending{
		Reason:  fmt.Sprintf("nocx is asking whether %s may use its tools", agent),
		Settled: settled,
	}
}

// ask puts the question, records what comes back, and closes the question for
// everybody waiting on it. It outlives the enrolment that raised it,
// deliberately: a person reading a consent dialog is not on the shell's clock.
//
// Its own context, for the same reason. The enrolment's is finished by the
// time anybody clicks, and cancelling the question with it would take the
// dialog off the screen mid-read.
func (s *agentApprovalService) ask(executable agentapproval.Executable, agent string) {
	response, err := s.requester.RequestHost(context.Background(), transport.HostAsk{
		Capability: transport.HostCapAgentApproval,
		Executable: executable.Path,
		Digest:     executable.SHA256,
		Workspace:  s.workspace,
	})
	if err != nil {
		// Nothing is recorded. An unanswerable question — no client, a window
		// that went away — must not become a durable "no": the person never
		// said it, and a denial is never silently retried, so it would be
		// permanent (nocx-6jbad). It closes WITH A SENTENCE, so the pane
		// starts the agent without tools and says why, rather than enrolling
		// again into a question that cannot appear.
		s.settle(executable, fmt.Sprintf("nocx could not ask whether %s may use its tools: %v", agent, err))
		return
	}
	decision := agentapproval.Denied
	if response.Approved {
		decision = agentapproval.Granted
	}
	if err := s.store.Record(executable, s.scope, decision); err != nil {
		// A sentence here too. Closed as answered, the shell would enrol, find
		// nothing stored, and put the question the person just answered back
		// on screen.
		s.settle(executable, fmt.Sprintf("nocx could not keep your answer about %s: %v", agent, err))
		return
	}
	s.settle(executable, "")
}

// ListAgentAccess is the answers a person gave, in the terms the surface
// speaks: the three facts they were shown, and the store's own answer. The
// durable scope key does not travel — this is where it is composed, so this
// is where it is taken apart, and the transport never learns its grammar.
//
// Answers under some other scope are dropped rather than shown. This backend
// composes exactly one, and a row a person cannot address is a row they
// cannot act on.
func (s *agentApprovalService) ListAgentAccess() []transport.AgentAccessRecord {
	if s == nil || s.store == nil {
		return nil
	}
	records := []transport.AgentAccessRecord{}
	for _, record := range s.store.List() {
		if record.Scope != s.scope {
			continue
		}
		records = append(records, transport.AgentAccessRecord{
			Executable: record.Executable.Path,
			Digest:     record.Executable.SHA256,
			Workspace:  s.workspace,
			Answer:     string(record.Answer),
		})
	}
	return records
}

// ForgetAgentAccess unmakes one answer, and ends every admission that answer
// was holding.
//
// The DOCUMENT is only half of it now. A tool connection is admitted once
// (ADR-0058), so nothing re-reads this store per call, and revoking the answer
// alone left a live connection — and the grant inside it — working until the
// socket happened to end. Closing the connections admitted under an answer that
// has gone is what makes the revocation take effect on the next call rather
// than on the next session; the PANE still goes on running, which is the state
// a denial produces, because nocx does not reach into an agent's process.
//
// A workspace this backend does not answer for is refused rather than
// composed into a key that would match nothing: silently forgetting nothing
// and reporting success is how a person comes to believe a revocation landed.
func (s *agentApprovalService) ForgetAgentAccess(executable, digest, workspace string) (bool, error) {
	if s == nil || s.store == nil {
		return false, errors.New("nocx has no record of admitted agents")
	}
	if workspace != s.workspace {
		return false, fmt.Errorf("nocx does not hold answers for workspace %q", workspace)
	}
	forgotten, err := s.store.Forget(agentapproval.Executable{Path: executable, SHA256: digest}, s.scope)
	if err != nil {
		return forgotten, err
	}
	if forgotten {
		// An answer was actually unmade, so the admissions it held have to
		// end: asking Approved again for each live admitted session is what
		// closes the ones that no longer hold. AFTER the Forget and never
		// before — before it, the answer still reads as granted.
		//
		// Nothing forgotten means no answer ended, so there is nothing to
		// close; closing anyway would re-ask an unchanged set, and the answer
		// to "did this call change anything" should not be "it depends".
		s.endAuthority()
	}
	return forgotten, nil
}

// settle closes the question for every enrolment waiting on it — nothing when
// a person's answer was kept, a sentence when there is none — and releases the
// identity, answered or not: a question nobody could deliver must be askable
// again. Each channel is buffered and settled exactly once, so a pane that
// stopped waiting costs nothing.
func (s *agentApprovalService) settle(executable agentapproval.Executable, reason string) {
	s.mu.Lock()
	k := askingKey(executable)
	waiters := s.asking[k]
	delete(s.asking, k)
	s.mu.Unlock()
	for _, settled := range waiters {
		settled <- reason
	}
}

func askingKey(executable agentapproval.Executable) string {
	return executable.Path + "\x00" + executable.SHA256
}

var _ agentApproval = (*agentApprovalService)(nil)
