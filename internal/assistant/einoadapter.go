package assistant

// The FRAMEWORK ADAPTER (nocx-d6gn4.12): everything eino knows about the
// tool-call pipeline is in this file, and it is deliberately thin — unpack the
// framework's call context, hand what the model reached for to the run's
// carrier, and translate what comes back into the framework's vocabulary. It
// decides nothing, and it knows which carrier it is holding no more than the
// framework does.
//
// Nothing here suspends. A person's question blocks the very call that asked
// (ADR-anntnxdj §3), so this adapter has no interrupt to raise, no checkpoint to
// name and no batch to latch: it unpacks the framework's call context, hands
// the proposal to the kernel, and translates what comes back into the
// framework's vocabulary.

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
)

// policyMiddleware is the retained declared-call carrier wearing eino's
// middleware interface. It holds no state of its own: the run's facts belong
// to the kernel, and two owners of them is the thing this split exists to
// prevent.
//
// It is the carrier rather than the kernel because the framework's seam is
// "the model reached for a tool"; the kernel is still underneath it.
type policyMiddleware struct {
	adk.BaseChatModelAgentMiddleware
	carrier
	kernel            *effectKernel
	grantProvider     func() content.Grant
	presentation      agenttools.PresentationConfig
	presentationState *presentationState
	searchSchema      []byte
}

// newPolicyMiddleware builds the kernel for one run, wraps the declared-call
// carrier in eino's middleware interface, and dresses the pair as eino's
// middleware. Every argument is the kernel's; see newEffectKernel for what
// each one is and which may be nil.
func newPolicyMiddleware(logger log.Logger, grant content.Grant, registry agenttools.Registry, ledger AttemptLedger, approvals *ApprovalStore, known KnownMaterial, runID, sessionID string, attempt int, turnEntryID string, requester RendererRequester, attached Attachments, classifier CallClassifier, onCall func(ToolCall) error, seams ...toolSeams) (*policyMiddleware, error) {
	k, err := newEffectKernel(logger, grant, registry, ledger, approvals, known, runID, sessionID, attempt, turnEntryID, requester, attached, classifier, onCall, seams...)
	if err != nil {
		return nil, err
	}
	return &policyMiddleware{
		carrier:           &callsCarrier{effectKernel: k},
		kernel:            k,
		grantProvider:     func() content.Grant { return grant },
		presentationState: newPresentationState(nil),
	}, nil
}

// WrapInvokableToolCall installs the pipeline on one tool call.
func (m *policyMiddleware) WrapInvokableToolCall(ctx context.Context, endpoint adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	if tCtx != nil && tCtx.Name == unknownToolAnchorName {
		return func(_ context.Context, rawArgs string, _ ...tool.Option) (string, error) {
			// The anchor keeps ADK on its ToolsNode path when every real
			// tool is refused. It is never offered to the model and must
			// not execute if a model guesses its internal name.
			return m.UnknownTool(tCtx.Name, rawArgs)
		}, nil
	}
	if tCtx != nil && tCtx.Name == toolsSearchName {
		return func(ctx context.Context, rawArgs string, _ ...tool.Option) (string, error) {
			return m.searchTools(ctx, rawArgs)
		}, nil
	}
	return func(ctx context.Context, rawArgs string, _ ...tool.Option) (string, error) {
		out, err := m.Invoke(ctx, tCtx.Name, tCtx.CallID, rawArgs)
		if err != nil {
			// A refusal is the call's answer and rides its own slot as text;
			// only a real failure reaches here, and it fails the run.
			return "", err
		}
		// The model-facing frame belongs to a carrier that faces a model.
		return out.forModel(func(result string) string {
			return m.FrameForModel(tCtx.Name, result)
		}), nil
	}, nil
}

// BeforeModelRewriteState rebuilds the model-facing projection from the
// canonical registry on every model request. The ToolsNode still owns every
// currently eligible executable tool, so a hidden name reaches the ordinary
// middleware and kernel path; only ToolInfos is presentation state.
func (m *policyMiddleware) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil {
		return ctx, state, nil
	}
	projection := m.presentationProjection()
	infos, err := declaredToolInfos(projection.Visible)
	if err != nil {
		return ctx, state, err
	}
	if projection.Lazy {
		searchInfo, err := searchToolInfo(m.searchSchema)
		if err != nil {
			return ctx, state, err
		}
		infos = append(infos, searchInfo)
	}
	state.ToolInfos = infos
	return ctx, state, nil
}
