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
}

func newAgentApprovalService(sessions workerAuthSessions, store *agentapproval.Store, scope string) *agentApprovalService {
	return &agentApprovalService{
		sessions: sessions, store: store,
		scope:     agentToolEndpointScopePrefix + scope,
		workspace: scope,
		enrolled:  map[session.ID]agentapproval.Executable{},
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
	// One fact per field. The path, the digest and the workspace used to
	// travel as one composed string and a durable scope key, so the renderer
	// could not give them a row each and printed the key at a person
	// ("tool-endpoint:workspace:default"). The key's grammar has one owner and
	// it is here; what crosses is the workspace's NAME (nocx-fu18z).
	response, err := s.requester.RequestHost(ctx, transport.HostAsk{
		Capability: transport.HostCapAgentApproval,
		Executable: executable.Path,
		Digest:     executable.SHA256,
		Workspace:  s.workspace,
	})
	if err != nil {
		return fmt.Errorf("agent approval: %w", err)
	}
	decision := agentapproval.Denied
	if response.Approved {
		decision = agentapproval.Granted
	}
	if err := s.store.Record(executable, s.scope, decision); err != nil {
		return err
	}
	if decision != agentapproval.Granted {
		return fmt.Errorf("agent approval was denied for %s", agent)
	}
	s.remember(sid, executable)
	return nil
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

func (s *agentApprovalService) remember(sid session.ID, executable agentapproval.Executable) {
	s.mu.Lock()
	s.enrolled[sid] = executable
	s.mu.Unlock()
}

var _ agentApproval = (*agentApprovalService)(nil)
