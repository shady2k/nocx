package app

import (
	"context"
	"errors"
	"fmt"

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
type agentApprovalService struct {
	sessions  workerAuthSessions
	store     *agentapproval.Store
	requester hostApprovalRequester
	scope     string
}

func newAgentApprovalService(sessions workerAuthSessions, store *agentapproval.Store, scope string) *agentApprovalService {
	return &agentApprovalService{sessions: sessions, store: store, scope: agentToolEndpointScopePrefix + scope}
}

func (s *agentApprovalService) SetRequester(requester hostApprovalRequester) {
	s.requester = requester
}

func (s *agentApprovalService) Approved(pid int, scope string) bool {
	executable, err := agentapproval.IdentityForPID(pid)
	if err != nil {
		return false
	}
	answer, ok := s.store.Lookup(executable, scope)
	return ok && answer == agentapproval.Granted
}

func (s *agentApprovalService) Approve(ctx context.Context, sid session.ID, agent string) error {
	if s == nil || s.sessions == nil || s.store == nil {
		return errors.New("nocx cannot ask for agent approval")
	}
	pid, ok := s.sessions.OwnedProcessPID(sid)
	if !ok {
		return errors.New("nocx cannot identify the enrolled agent executable")
	}
	executable, err := agentapproval.IdentityForPID(pid)
	if err != nil {
		return err
	}
	answer, found := s.store.Lookup(executable, s.scope)
	if found {
		if answer == agentapproval.Granted {
			return nil
		}
		return fmt.Errorf("agent approval was denied for %s", agent)
	}
	if s.requester == nil {
		return errors.New("nocx has no client to ask for agent approval")
	}
	wireExecutable := executable.Path + " (sha256:" + executable.SHA256 + ")"
	response, err := s.requester.RequestHost(ctx, transport.HostAsk{
		Capability: transport.HostCapAgentApproval,
		Executable: wireExecutable,
		Scope:      s.scope,
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
	return nil
}

var _ agentApproval = (*agentApprovalService)(nil)
