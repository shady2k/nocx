package transport

// The approval service (nocx-a62cl.3.1, ADR-anntnxdj §3): a person can block a
// tool call, and a service owns that.
//
// The kernel's chain asks its Approver about a call its policy says to ask
// about. On this path that approver is THIS service: it moves the run to
// awaiting_approval, publishes agent.approvalRequested, and WAITS — the call
// stays on the stack, inside the model's own tool invocation, holding no
// execution capacity and no resource chain. agent.approve on any connection
// settles the exact binding in the approval store and wakes the wait with the
// person's answer, which the pipeline then carries into the guards that follow
// it and, if they pass, the one execution of the tool.
//
// The wait is an `answer` phase of ADR-8eej3e66: internal.agent.run declares it
// (declarations_internal.go), with its own finite limit and no resources, and
// answerLimit below is that declaration read rather than restated. No answer by
// the limit, a cancelled call and a closed connection are all refusals — no
// answer is never a yes — and none of them leaves a question behind that a
// later agent.approve could settle.
//
// What this replaced: the question used to be published only after a
// suspension unwound the whole drive, and a yes started a NEW drive from eino's
// checkpoint. Both are gone (nocx-a62cl.3.4) — eino runs the loop and nothing
// suspends it, and a chain with no service wired has no approver at all, so an
// ask there is refused rather than suspended.

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/operation/catalog"
)

// errApprovalServiceNotWired is the answer a call gets when the transport has
// no approval surface to ask through. It is a refusal and not a panic: a run
// that cannot reach a person must not run the call.
var errApprovalServiceNotWired = errors.New("the approval service is not wired for this run")

// answerLimit is the declared bound on a person's answer — internal.agent.run's
// `answer` phase (ADR-8eej3e66 §2). It is READ from the catalogue, never
// restated, so the wait and the declaration cannot disagree; the declaration's
// own validation refuses a zero, which is why the missing case panics here
// rather than inventing a default.
var answerLimit = func() time.Duration {
	bound, ok := catalog.OpInternalAgentRun.Declaration().Answer.Deadline()
	if !ok {
		panic("transport: internal.agent.run declares no answer bound, so an approval wait would be unbounded")
	}
	return bound.Duration()
}()

// approvalService is the kernel's Approver on the transport's side of the seam.
// It holds the run it serves — its identity, its connection's responder and the
// server's stores — because a question is about one run, one call and one
// person's answer to it.
type approvalService struct {
	handlers agentHandlers
	rc       askRunContext
	clock    notify.Clock
	limit    time.Duration
}

// approvalService builds the approver for ONE drive of one run. The clock is
// the real one in the product and the test's in a test, which is the only way
// the declared ten-minute bound is exercised without sleeping it.
func (h agentHandlers) approvalService(rc askRunContext) approvalService {
	return approvalService{handlers: h, rc: rc, clock: notify.RealClock{}, limit: answerLimit}
}

// Ask puts one prepared policy question to the person and blocks until it is
// settled. It implements assistant.Questioner, so it is the value the kernel's
// chain calls from inside the model's tool invocation.
func (s approvalService) Ask(ctx context.Context, req *assistant.ApprovalRequest) assistant.Answer {
	proposal := approvalOf(req)
	// The two facts only the transport holds: the matrix row the answer is
	// about, and the bound a widening would have to cover. The kernel recorded
	// the proposal itself, with the ledger entry and the file versions.
	proposal.OutOfScope = req.OutOfScope
	s.handlers.approvals.NoteEffect(proposal)
	s.handlers.approvals.NoteOutOfScope(proposal)
	question := policyQuestion(req)
	invocation, hasInvocation, commandInvocation := s.handlers.approvals.InvocationFor(proposal)
	question.Standing = standingOffer(invocation, hasInvocation, commandInvocation, req.Effect)
	return s.wait(ctx, req.RunID, req.Tool, "policy", proposal, question)
}

// AskEgress puts the screen's question about a result that already exists to
// the person and blocks until it is settled. It is the same wait as Ask — one
// person, one surface, one open question at a time — and only the notification
// and the answers the surface offers differ: an egress decision covers this
// result only, so it is never a standing one.
func (s approvalService) AskEgress(ctx context.Context, req *assistant.EgressRequest) assistant.Answer {
	proposal := assistant.Approval{
		RunID: req.RunID, Attempt: req.Attempt, Tool: req.Tool, CallID: req.CallID,
		ArgHash: req.ArgHash, Effect: req.Effect,
	}
	s.handlers.approvals.NoteEffect(proposal)
	return s.wait(ctx, req.RunID, req.Tool, "egress", proposal, egressQuestion(req))
}

// wait is the one wait both questions go through: the run rests in
// awaiting_approval, the question is published, the call blocks, and the
// person's answer — or the declared limit, or the call's own cancellation —
// settles it.
func (s approvalService) wait(ctx context.Context, runIDText, tool, reason string, proposal assistant.Approval, question agentApprovalRequested) assistant.Answer {
	if s.handlers.op == nil || s.handlers.approvals == nil {
		return assistant.Unanswered{Err: errApprovalServiceNotWired}
	}
	runID, err := strconv.ParseInt(runIDText, 10, 64)
	if err != nil || runID <= 0 {
		return assistant.Unanswered{Err: errors.New("the question names no run, so nobody can be asked about it")}
	}
	// The run rests in awaiting_approval for as long as the question is open:
	// a reconnecting renderer reads that, and a run closed by another path is
	// refused here rather than asked about.
	if err := s.handlers.op.Run(ctx, func(ctx context.Context, svc capability.AgentService) error {
		return svc.TransitionRun(ctx, runID, content.RunAwaitingApproval)
	}); err != nil {
		return assistant.Unanswered{Err: err}
	}

	// The wait is recorded BEFORE the question is published: a person can
	// answer the instant the surface shows it, and an answer that arrives
	// first must wake this call rather than be told no question is open.
	wait := newAnswerWait(s.clock, tool, s.limit)
	wait.binding = proposal
	s.handlers.beginAnswerWait(runID, wait, reason)
	defer s.handlers.endAnswerWait(runID, wait)

	wait.begin()
	if err := s.handlers.r.TryNotify("agent.approvalRequested", mustMarshal(question)); err != nil {
		// Nobody can be asked on this connection: the question is withdrawn,
		// not left pending for an answer that can no longer reach the call.
		wait.stop()
		s.handlers.approvals.Withdraw(proposal)
		return assistant.Unanswered{Err: err}
	}

	answer := wait.result(ctx)
	wait.stop()
	// A wait that ended without a person's answer leaves nothing for a later
	// agent.approve to settle: a proposal still pending here is one nobody
	// answered. A person's answer already moved it out of pending.
	if s.handlers.approvals.IsPending(proposal) {
		s.handlers.approvals.Withdraw(proposal)
	}
	// The call goes on: the run is streaming again for the guards, the
	// attempt and the one execution that follow. A cancelled call fails this
	// and its own terminal path owns the outcome.
	_ = s.handlers.op.Run(ctx, func(ctx context.Context, svc capability.AgentService) error {
		return svc.TransitionRun(ctx, runID, content.RunStreaming)
	})
	return answer
}

// egressQuestion is the agent.approvalRequested notification a screen's
// question becomes: the findings and whether they were in the tool's error
// text, never the material, and no standing answer — sending secret-shaped
// material to the model provider is never a decision anyone makes by clicking
// a button beside five others.
func egressQuestion(req *assistant.EgressRequest) agentApprovalRequested {
	return agentApprovalRequested{
		RunID: req.RunID, Attempt: req.Attempt, Tool: req.Tool, CallID: req.CallID,
		ArgHash: req.ArgHash, Arguments: req.Arguments,
		Reason: "egress", WasError: req.WasError, Findings: req.Findings,
		// The surface ignores the effect on this arm, but the wire is not two
		// shapes and its schema requires it on both.
		Effect:   string(req.Effect),
		Resource: req.Resource,
		Standing: agentApprovalStanding{Reason: "standing answers are not offered for result disclosure"},
	}
}

// approvalOf is the store binding a question names. It is the SAME five fields
// agent.approve echoes back (approvals.go's key), plus the effect the matrix
// row is read from; the entry id, the invocation and the out-of-scope fact are
// carriers the pending record already holds.
func approvalOf(req *assistant.ApprovalRequest) assistant.Approval {
	return assistant.Approval{
		RunID: req.RunID, Attempt: req.Attempt, Tool: req.Tool, CallID: req.CallID,
		ArgHash: req.ArgHash, Effect: req.Effect,
	}
}

// policyQuestion is the agent.approvalRequested notification one policy
// question becomes. ONE builder, so the surface has one shape to render.
func policyQuestion(ap *assistant.ApprovalRequest) agentApprovalRequested {
	n := agentApprovalRequested{
		Reason: "policy",
		RunID:  ap.RunID, Attempt: ap.Attempt, Tool: ap.Tool, CallID: ap.CallID,
		ArgHash: ap.ArgHash, Arguments: ap.Arguments,
		Effect: string(ap.Effect), Resource: ap.Resource,
		Expansion: ap.Expansion, Scripts: ap.Scripts,
	}
	if ap.OutOfScope != nil {
		n.OutOfScope = &agentApprovalOutOfScope{
			Cause:    string(ap.OutOfScope.Cause),
			Resource: ap.OutOfScope.Resource,
			Widening: wideningOffer(ap.OutOfScope.Cause),
		}
	}
	return n
}

// ── the wait ──────────────────────────────────────────────────────────────

// answerWait is one question's wait: the person's answer, the declared limit,
// and the call's own cancellation. Exactly one of the three settles it — the
// claim is what makes that true — and a late answer decides nothing.
type answerWait struct {
	clock   notify.Clock
	tool    string
	limit   time.Duration
	binding assistant.Approval

	answers chan assistant.Answer
	expired chan struct{}
	timer   notify.Timer

	mu      sync.Mutex
	settled bool
}

func newAnswerWait(clock notify.Clock, tool string, limit time.Duration) *answerWait {
	return &answerWait{
		clock:   clock,
		tool:    tool,
		limit:   limit,
		answers: make(chan assistant.Answer, 1),
		expired: make(chan struct{}),
	}
}

// begin arms the declared limit. It is called before the question is published,
// so the bound covers the whole wait the person sees.
func (w *answerWait) begin() {
	w.timer = w.clock.AfterFunc(w.limit, func() { close(w.expired) })
}

// stop disarms the limit. It is safe after the timer fired.
func (w *answerWait) stop() {
	if w.timer != nil {
		w.timer.Stop()
	}
}

// settle claims the wait for one answer. Exactly one caller wins, and every
// later claim is dropped — which is what keeps a timeout that races a person's
// click from deciding anything.
func (w *answerWait) settle(a assistant.Answer) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.settled {
		return false
	}
	w.settled = true
	w.answers <- a
	return true
}

// answersCall reports whether ap is the exact proposal this wait is about. A
// stale answer for another call — or another attempt of this one — wakes
// nothing.
func (w *answerWait) answersCall(ap assistant.Approval) bool {
	return w.binding.RunID == ap.RunID &&
		w.binding.Attempt == ap.Attempt &&
		w.binding.Tool == ap.Tool &&
		w.binding.CallID == ap.CallID &&
		w.binding.ArgHash == ap.ArgHash
}

// result blocks until the answer is claimed or the wait is ended by its limit
// or by the call's cancellation. Every ending is an answer: the person's, a
// refusal at the limit, or an unanswered refusal for a cancelled call.
func (w *answerWait) result(ctx context.Context) assistant.Answer {
	select {
	case a := <-w.answers:
		return a
	case <-w.expired:
		w.settle(assistant.Declined{Reason: assistant.NoAnswerResult(w.tool, w.limit)})
		return <-w.answers
	case <-ctx.Done():
		w.settle(assistant.Unanswered{Err: ctx.Err()})
		return <-w.answers
	}
}

// ── the run's pending question ────────────────────────────────────────────

// beginAnswerWait records the question this run's call is blocked on. The run
// registry is the owner because the question belongs to the run: one run has at
// most one call in flight, and every connection's agent.approve finds the same
// record.
func (h agentHandlers) beginAnswerWait(runID int64, w *answerWait, reason string) {
	h.pendingRunsMu.Lock()
	defer h.pendingRunsMu.Unlock()
	rc, ok := h.pendingRuns[runID]
	if !ok {
		return
	}
	rc.approvalWait = w
	rc.pendingReason = reason
	h.pendingRuns[runID] = rc
}

// restoreAnswerWait puts a claimed wait back on the run: an answer the server
// REFUSED before it settled anything leaves the question exactly as it was, so
// the person answers it again rather than the call being refused for a store
// that would not take their decision.
func (h agentHandlers) restoreAnswerWait(runID int64, w *answerWait, reason string) {
	h.pendingRunsMu.Lock()
	defer h.pendingRunsMu.Unlock()
	rc, ok := h.pendingRuns[runID]
	if !ok {
		return
	}
	rc.approvalWait = w
	rc.pendingReason = reason
	h.pendingRuns[runID] = rc
}

// endAnswerWait drops the record once the wait is over, but only its own: a
// later question of the same run must not be cleared by an earlier wait's
// ending.
func (h agentHandlers) endAnswerWait(runID int64, w *answerWait) {
	h.pendingRunsMu.Lock()
	defer h.pendingRunsMu.Unlock()
	rc, ok := h.pendingRuns[runID]
	if !ok || rc.approvalWait != w {
		return
	}
	rc.approvalWait = nil
	rc.pendingReason = ""
	h.pendingRuns[runID] = rc
}

// takeAnswerWait claims the pending question agent.approve is answering. A nil
// result means no call is blocked on this binding: the answer names a question
// this run is not waiting on, and it is refused rather than settled.
func (h agentHandlers) takeAnswerWait(runID int64, ap assistant.Approval) *answerWait {
	h.pendingRunsMu.Lock()
	defer h.pendingRunsMu.Unlock()
	rc, ok := h.pendingRuns[runID]
	if !ok || rc.approvalWait == nil || !rc.approvalWait.answersCall(ap) {
		return nil
	}
	w := rc.approvalWait
	rc.approvalWait = nil
	rc.pendingReason = ""
	h.pendingRuns[runID] = rc
	return w
}
