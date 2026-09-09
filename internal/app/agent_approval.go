package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/shady2k/nocx/internal/agentapproval"
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
	// Identities with a question already on screen. Starting the agent again
	// while the person is still reading must not put a second copy of the
	// same question in the queue: it is the same decision, and a stack of
	// identical dialogs is how a person clicks one without reading it.
	asking map[string]bool
}

func newAgentApprovalService(sessions workerAuthSessions, store *agentapproval.Store, scope string) *agentApprovalService {
	return &agentApprovalService{
		sessions: sessions, store: store,
		scope:     agentToolEndpointScopePrefix + scope,
		workspace: scope,
		enrolled:  map[session.ID]agentapproval.Executable{},
		asking:    map[string]bool{},
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
func (s *agentApprovalService) Forget(sid session.ID) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.enrolled, sid)
	s.mu.Unlock()
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
	answer, found := s.store.Lookup(executable, s.scope)
	if found {
		if answer == agentapproval.Granted {
			s.remember(sid, executable)
			return nil
		}
		return fmt.Errorf("agent approval was denied for %s", agent)
	}
	if s.requester == nil {
		return errors.New("nocx has no client to ask for agent approval")
	}
	// THE QUESTION IS RAISED, AND THIS RETURNS WITHOUT IT (nocx-t7xds).
	//
	// The two deadlines are irreconcilable and both are right. The shell gives
	// an enrolment five seconds (nocx.bash __nocx_lc_grant_timeout_s) because
	// a handshake between two programs must be bounded. The approval question
	// has no deadline at all (transport.hostTimeoutFor) because it waits on
	// somebody reading it. Waiting for the second inside the first meant a
	// person could never answer in time: the pane printed "nocx did not
	// answer", the agent started without tools, the dialog stayed up, and the
	// click that eventually came was recorded against an enrolment that no
	// longer existed. It worked on the NEXT start, which made a rule look like
	// a fluke.
	//
	// So the ask goes out on its own and the enrolment refuses now, saying
	// what is happening. D4 holds — no enrolment, no orchestration, and the
	// pane says so — and the sentence is an instruction rather than a report
	// about a timeout. The answer is durable, so starting the agent again
	// after answering enrols with no question at all.
	if s.beginAsking(executable) {
		go s.ask(executable, agent)
	}
	return fmt.Errorf("nocx is asking you whether %s may use its tools; answer that, then start it again", agent)
}

// ask puts the question and records what comes back. It outlives the
// enrolment that raised it, deliberately: a person reading a consent dialog
// is not on the shell's clock.
//
// Its own context, for the same reason. The enrolment's is finished by the
// time anybody clicks, and cancelling the question with it would take the
// dialog off the screen mid-read and leave the person's next start refusing
// for ever with nothing to answer.
func (s *agentApprovalService) ask(executable agentapproval.Executable, agent string) {
	defer s.doneAsking(executable)
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
		// permanent (nocx-6jbad).
		return
	}
	decision := agentapproval.Denied
	if response.Approved {
		decision = agentapproval.Granted
	}
	_ = s.store.Record(executable, s.scope, decision)
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

// ForgetAgentAccess unmakes one answer. It touches only the document: a pane
// already running is not reached and does not need to be, because Approved
// reads the store on every admit, so that agent's next tool call is refused
// and it goes on running without nocx's tools — the state a denial produces.
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
	return s.store.Forget(agentapproval.Executable{Path: executable, SHA256: digest}, s.scope)
}

// beginAsking claims the question for one identity, and answers false when
// somebody else already holds it. The claim is released when the ask returns,
// answered or not: a question nobody could deliver must be askable again.
func (s *agentApprovalService) beginAsking(executable agentapproval.Executable) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := executable.Path + "\x00" + executable.SHA256
	if s.asking[k] {
		return false
	}
	s.asking[k] = true
	return true
}

func (s *agentApprovalService) doneAsking(executable agentapproval.Executable) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.asking, executable.Path+"\x00"+executable.SHA256)
}

func (s *agentApprovalService) remember(sid session.ID, executable agentapproval.Executable) {
	s.mu.Lock()
	s.enrolled[sid] = executable
	s.mu.Unlock()
}

var _ agentApproval = (*agentApprovalService)(nil)
