package session

import (
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sandbox"
)

type nativeDiagnosticProcess interface {
	nativeDiagnostics() *nativeInbox
}

func (s *Service) nativeDiagnosticSession(id proto.HostSessionID) (*nativeInbox, string, error) {
	hs, err := s.find(id)
	if err != nil {
		return nil, "", err
	}
	hs.mu.Lock()
	binding := hs.sandbox
	process := hs.proc
	if binding == nil || binding.Mode != proto.SandboxEnforce {
		hs.mu.Unlock()
		return nil, "", errSandboxUnsupported
	}
	launchID := binding.LaunchID
	hs.mu.Unlock()
	diagnostic, ok := process.(nativeDiagnosticProcess)
	if !ok || diagnostic.nativeDiagnostics() == nil {
		return nil, "", errSandboxUnsupported
	}
	return diagnostic.nativeDiagnostics(), launchID, nil
}

func (s *Service) sandboxAccessList(params proto.SandboxAccessListParams) (proto.SandboxAccessListResult, error) {
	if params.Limit > nativeInboxPageLimit {
		return proto.SandboxAccessListResult{}, errSandboxParams
	}
	inbox, launchID, err := s.nativeDiagnosticSession(params.Session)
	if err != nil {
		return proto.SandboxAccessListResult{}, err
	}
	return proto.SandboxAccessListResult{Session: params.Session, LaunchID: launchID, Inbox: inbox.page(params.Cursor, params.Limit)}, nil
}

func (s *Service) sandboxAccessReserve(params proto.SandboxAccessReserveParams) (proto.SandboxAccessReserveResult, error) {
	if params.EventID == "" || len(params.EventID) > 256 || params.Revision == 0 || (params.Decision != sandbox.DecisionDismiss && params.Decision != sandbox.DecisionAllowRO && params.Decision != sandbox.DecisionAllowRW) {
		return proto.SandboxAccessReserveResult{}, errSandboxParams
	}
	inbox, launchID, err := s.nativeDiagnosticSession(params.Session)
	if err != nil {
		return proto.SandboxAccessReserveResult{}, err
	}
	reservation, record, err := inbox.reserve(params.EventID, params.Revision, params.Decision)
	if err != nil {
		return proto.SandboxAccessReserveResult{}, err
	}
	return proto.SandboxAccessReserveResult{Session: params.Session, LaunchID: launchID, Reservation: reservation, Record: record}, nil
}

func (s *Service) sandboxAccessFinish(params proto.SandboxAccessFinishParams) (proto.SandboxAccessFinishResult, error) {
	if params.EventID == "" || len(params.EventID) > 256 || params.Reservation == "" || len(params.Reservation) > 64 || (params.Uncertain && params.Committed) {
		return proto.SandboxAccessFinishResult{}, errSandboxParams
	}
	inbox, launchID, err := s.nativeDiagnosticSession(params.Session)
	if err != nil {
		return proto.SandboxAccessFinishResult{}, err
	}
	var record sandbox.DiagnosticRecord
	if params.Uncertain {
		record, err = inbox.markUncertain(params.EventID, params.Reservation)
	} else {
		record, err = inbox.finish(params.EventID, params.Reservation, params.Committed, params.ProfileRevision)
	}
	if err != nil {
		return proto.SandboxAccessFinishResult{}, err
	}
	return proto.SandboxAccessFinishResult{Session: params.Session, LaunchID: launchID, Record: record}, nil
}
