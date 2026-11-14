package assistant

import (
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
)

func newPolicyMiddleware(logger log.Logger, grant content.Grant, registry agenttools.Registry, ledger AttemptLedger, approvals *ApprovalStore, known KnownMaterial, runID, sessionID string, attempt int, turnEntryID string, requester RendererRequester, attached Attachments, onCall func(ToolCall) error, seams ...toolSeams) (*policyMiddleware, error) {
	k, err := newEffectKernel(logger, grant, registry, ledger, approvals, known, runID, sessionID, attempt, turnEntryID, requester, attached, onCall, seams...)
	if err != nil {
		return nil, err
	}
	return middlewareForKernel(k), nil
}
