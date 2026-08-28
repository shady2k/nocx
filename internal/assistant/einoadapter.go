package assistant

// The FRAMEWORK ADAPTER (nocx-d6gn4.12): everything eino knows about the
// tool-call pipeline is in this file, and it is deliberately thin — unpack the
// framework's call context, hand what the model reached for to the run's
// carrier, and translate what comes back into the framework's vocabulary. It
// decides nothing, and it knows which carrier it is holding no more than the
// framework does.
//
// TWO THINGS LIVE HERE RATHER THAN IN THE KERNEL, and both for the same
// reason — they are facts about the FRAMEWORK'"'"'s seam, not about effects:
//
//   - The BATCH LATCH. "Every later call in this model response stops" is a
//     statement about a model response, which is a shape only a model
//     proposing calls in batches has. The retained declared-call path
//     proposes calls one at a time, so the latch has nothing to latch and
//     costs nothing.
//   - The INTERRUPT. compose.StatefulInterrupt is how eino suspends. A
//     carrier says "a person must answer this" by returning
//     *ApprovalRequestedError or *EgressRequestedError; turning that into a
//     framework interrupt is this adapter'"'"'s whole job.

import (
	"context"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"

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
func newPolicyMiddleware(logger log.Logger, grant content.Grant, registry agenttools.Registry, ledger AttemptLedger, approvals *ApprovalStore, known KnownMaterial, runID string, attempt int, turnEntryID string, requester RendererRequester, classifier CallClassifier, onCall func(ToolCall) error) (*policyMiddleware, error) {
	k, err := newEffectKernel(logger, grant, registry, ledger, approvals, known, runID, attempt, turnEntryID, requester, classifier, onCall)
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
			if l := latchFrom(ctx); l != nil {
				if reason := l.tripped(); reason != nil {
					return "", m.deferred(ctx, tCtx, reason)
				}
			}
			return m.searchTools(ctx, rawArgs)
		}, nil
	}
	return func(ctx context.Context, rawArgs string, _ ...tool.Option) (string, error) {
		// The batch latch, before anything else: once a call in this model
		// response refused or escalated, every later call returns immediately
		// without reaching the kernel. sequentialRunToolCall loops every task
		// and never inspects tasks[i].err, so without the latch a refused
		// call would not stop the next one from running.
		if l := latchFrom(ctx); l != nil {
			if reason := l.tripped(); reason != nil {
				return "", m.deferred(ctx, tCtx, reason)
			}
		}

		out, err := m.Invoke(ctx, tCtx.Name, tCtx.CallID, rawArgs)

		// A suspension is not a failure: the kernel has already recorded the
		// proposal and asked, and what remains is to stop THIS carrier in the
		// way it stops. The latch is tripped with the same error the kernel
		// returned, so the sentence a later deferred call reports is the one
		// that actually happened.
		var ask *ApprovalRequestedError
		if errors.As(err, &ask) {
			tripLatch(ctx, ask)
			return "", compose.StatefulInterrupt(ctx, ask.Request, ask.Request)
		}
		var egress *EgressRequestedError
		if errors.As(err, &egress) {
			tripLatch(ctx, egress)
			return "", compose.StatefulInterrupt(ctx, egress.Request, egress.Request)
		}
		if err != nil {
			return "", err
		}
		// The model-facing frame belongs to a carrier that faces a model.
		return out.forModel(func(result string) string {
			return m.FrameForModel(tCtx.Name, result)
		}), nil
	}, nil
}

// BeforeAgent mints the batch latch for this run and installs it in the run
// context (the batch latch is ours, not the framework's — ADR-0028 decision
// 2). It runs on every Run AND every Resume: a resumed attempt is a new
// attempt with a fresh latch.
func (m *policyMiddleware) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	return withLatch(ctx, &batchLatch{}), runCtx, nil
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

// deferred returns what a latched call returns: an interrupt error, so the
// batch still suspends cleanly when the trigger was an escalation (a plain
// error here would fail the run instead of suspending it). Only
// ESCALATIONS trip the latch now — a refusal is one call's answer, not the
// batch's (nocx-uvac6.1) — so the deferred shape is the interrupt and
// nothing else. Either way: next is not called, the tool does not run, and
// the human is told the truth about the call that asked.
func (m *policyMiddleware) deferred(ctx context.Context, tCtx *adk.ToolContext, reason error) error {
	info := fmt.Sprintf("call %q (%s) did not run: a prior call in this response %v", tCtx.Name, tCtx.CallID, reason)
	return compose.Interrupt(ctx, info)
}

// approvalRequestFrom finds the pipeline's own ask among an interrupt
// event's contexts: the asking call carries our *ApprovalRequest as its
// info; the latched, deferred calls carry a plain string ("a prior call
// ..."). The first ask is the one the human decides about.
//
// It returns the interrupt's ID with it — adk's fully-qualified address of
// THIS suspended branch — because that is what a resume must name
// (adk.ResumeParams.Targets). The two facts come off one context and are
// found by one walk: a request whose branch we could not name is a
// suspension nobody could ever answer.
func approvalRequestFrom(info *adk.InterruptInfo) (*ApprovalRequest, string) {
	if info == nil {
		return nil, ""
	}
	for _, ic := range info.InterruptContexts {
		if req, ok := ic.Info.(*ApprovalRequest); ok {
			return req, ic.ID
		}
	}
	return nil, ""
}
