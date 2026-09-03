package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/content"
)

// RunLeaseBound names a lease bound whose enforcement depends on the
// authenticated shell-integration lifecycle.
type RunLeaseBound string

const (
	RunLeaseBoundInactivity RunLeaseBound = "inactivity"
	RunLeaseBoundOutput     RunLeaseBound = "output"
)

// RunLeaseUnavailableBounds maps the configured integration-dependent bounds
// to their wire names. The transport still uses needsShellIntegration as the
// single gate for checking lifecycle availability.
func RunLeaseUnavailableBounds(inactivity, output bool) []RunLeaseBound {
	var bounds []RunLeaseBound
	if inactivity {
		bounds = append(bounds, RunLeaseBoundInactivity)
	}
	if output {
		bounds = append(bounds, RunLeaseBoundOutput)
	}
	return bounds
}

// RunLeaseDegradation carries the bounds that could not be armed for one
// assistant run. It is mutable because the renderer requester discovers the
// availability while the stream is executing, before terminalize publishes
// the final run state.
type RunLeaseDegradation struct {
	mu     sync.Mutex
	bounds []RunLeaseBound
}

func NewRunLeaseDegradation() *RunLeaseDegradation {
	return &RunLeaseDegradation{}
}

func (d *RunLeaseDegradation) Add(bounds ...RunLeaseBound) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, bound := range bounds {
		if bound == "" {
			continue
		}
		seen := false
		for _, existing := range d.bounds {
			if existing == bound {
				seen = true
				break
			}
		}
		if !seen {
			d.bounds = append(d.bounds, bound)
		}
	}
}

func (d *RunLeaseDegradation) Bounds() []RunLeaseBound {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]RunLeaseBound(nil), d.bounds...)
}

type runLeaseDegradationContextKey struct{}

func WithRunLeaseDegradation(ctx context.Context, degradation *RunLeaseDegradation) context.Context {
	return context.WithValue(ctx, runLeaseDegradationContextKey{}, degradation)
}

func RunLeaseDegradationFromContext(ctx context.Context) *RunLeaseDegradation {
	if ctx == nil {
		return nil
	}
	degradation, _ := ctx.Value(runLeaseDegradationContextKey{}).(*RunLeaseDegradation)
	return degradation
}

// FrameRegion is an absolute buffer row span [Start, End) of a session's
// screen. The renderer interprets it against ITS grid; the backend never
// re-derives terminal geometry (AD-6). Nil means "the visible screen".
type FrameRegion struct {
	Start int
	End   int
}

// RendererRequester is the seam a renderer-executed tool (design §2.2,
// §6.6 — Executes: InRenderer) asks the renderer through. The transport
// adapts its request broker to this interface at the run: the broker mints
// the request id, correlates the resolution over the same socket, and
// returns the frame body as it crossed the wire, validated (bounded,
// shape-checked) before the executor ever decodes it.
//
// The frame body is deliberately opaque here: the frame wire vocabulary
// (cells, attributes, capture identity) is owned by the transport's
// captureFrame validation, and this seam consumes it rather than recreating
// it — the executor decodes only the fields its return contract needs
// (design §4.4's window), never the full frame type.
type RendererRequester interface {
	// RequestScreen asks the renderer to capture sessionID's screen — the
	// same frame shape the renderer pushes for agent.captureFrame, pulled —
	// and returns the validated frame body (rows, cursor, capture identity).
	// A session the run cannot read must be refused HERE or before: the
	// capability check happens before this call, and a failed capture is a
	// returned error, never a hang.
	RequestScreen(ctx context.Context, sessionID string, region *FrameRegion) (json.RawMessage, error)
	// RequestRun asks the renderer to submit command to sessionID's lane
	// through the same submit path a person uses — the renderer's ordinary
	// orchestration (block, ledger entry, attempt, output artifact, all
	// minted at submit; design §4.1) — to wait for the completion, and to
	// resolve with the run body: the entry id, the exit status and a window
	// of the output (design §4.4). The backend never writes to the PTY
	// (design §2.1 — rejected, not open for re-litigation). A session the
	// run cannot use must be refused HERE or before: the capability check
	// happens before this call, and a refused or failed submission is a
	// returned error, never a hang.
	RequestRun(ctx context.Context, sessionID string, command string) (json.RawMessage, error)
}

// RunDecision is what a continuation says about a run the quiet bound asked
// about: keep waiting, or stop it. Two values and no third — "do nothing" is
// not an answer a party who was asked may give, and leaving a command
// running with nobody watching is what the bound exists to prevent.
type RunDecision string

const (
	// RunKeepWaiting re-attaches to the SAME execution. It never re-submits
	// the command: the renderer was never told to cancel and the process has
	// been running the whole time.
	RunKeepWaiting RunDecision = "continue"
	// RunStop terminalizes the execution — INT → TERM → KILL against its own
	// process group — and the run reports the stop, not a timeout.
	RunStop RunDecision = "stop"
)

// ErrRunNotWaiting is the answer to a continuation that names a run which is
// no longer waiting: it finished on its own, it was already stopped, or the
// id was never real.
//
// IT IS AN ANSWER AND NOT A FAILED RUN, and the distinction is the whole
// reason it is a named value. A command can finish in the moment between
// nocx asking about its silence and the model answering — that is a RACE THE
// MODEL CANNOT WIN and did nothing wrong by losing. Killing the turn for it
// would punish the model for answering a question we asked. The kernel turns
// this into a sentence the model reads and carries on from, the way it
// already does for a refusal and for a lease bound.
var ErrRunNotWaiting = errors.New("no command is waiting under that run id — it has already finished or been stopped")

// RunWaiter is the continuation seam of the run lease's quiet bound
// (ADR-0020 decision 2, nocx-6dzxq). It is deliberately SEPARATE from
// RendererRequester rather than a fifth method on it: a requester with no
// parked run to continue implements nothing, and session.wait says so
// honestly instead of every fake in the tree growing a method it has no
// answer for. The transport implements both.
//
// The runID is the one the still-running answer named. It is not a session,
// not a command and not a ledger entry: it is the exact execution the model
// was asked about, so an answer can never land on a different command that
// happens to be running in the same pane.
type RunWaiter interface {
	RequestRunWait(ctx context.Context, runID string, decision RunDecision) (json.RawMessage, error)
}

// runQuietBoundContextKey carries the model's own quiet bound for ONE call
// from the tool executor down to the transport that arms the lease.
//
// It travels in the context rather than in RequestRun's signature for the
// reason RunLeaseDegradation does: the bound is per-call wiring that only
// the transport consumes, the interface is implemented by every fake in the
// tree, and a fifth parameter on a seam four packages deep buys nothing that
// a context value does not.
type runQuietBoundContextKey struct{}

// WithRunQuietBound states the quiet bound this ONE call asked for. Zero (or
// absent) means the call asked for nothing and the person's setting applies
// unchanged. It is an ASK: the transport clamps it to the person's ceiling
// and never widens past it (ADR-0047 — a program may ask; it never chooses).
func WithRunQuietBound(ctx context.Context, d time.Duration) context.Context {
	if d <= 0 {
		return ctx
	}
	return context.WithValue(ctx, runQuietBoundContextKey{}, d)
}

// RunQuietBoundFromContext reads the quiet bound this call asked for.
func RunQuietBoundFromContext(ctx context.Context) time.Duration {
	if ctx == nil {
		return 0
	}
	d, _ := ctx.Value(runQuietBoundContextKey{}).(time.Duration)
	return d
}

// RunStillRunningError is the QUIET BOUND'S QUESTION, and it is not a
// failure: nothing was terminalized, the command is still executing, and the
// renderer was never told to cancel. It exists because the model is reachable
// only through a tool result, so the attempt has to END in order to ask —
// see internal/transport/run_park.go for why the alternative (a new
// mid-run push) was refused.
//
// It carries what the model needs to answer: which run to answer about, the
// bound that fired, how much of the person's ceiling is left, and how many
// times this same question has already been answered on this execution.
type RunStillRunningError struct {
	// RunID is the handle session.wait continues or stops the execution
	// with.
	RunID string
	// Quiet is the silence bound that fired.
	Quiet time.Duration
	// Remaining is what is left of the person's wall-clock ceiling. It is
	// the number that makes "keep waiting" a decision rather than a reflex:
	// the model can see that the command cannot outlive it.
	Remaining time.Duration
	// Renewals is how many times "keep waiting" has already been answered
	// for this execution. Zero on the first ask.
	Renewals int
	// ClampedFrom is what this call asked for when it asked for MORE than
	// the person allows, and zero otherwise. A clamp is stated, never
	// silent.
	ClampedFrom time.Duration
	// EntryID is the ledger entry the renderer minted for the command. It
	// is here for the same reason RunLeaseError carries one: the block
	// EXISTS — a person is looking at it — and the command→turn edge must be
	// written even though this call is ending without the command's result.
	// Empty when the store wrote no row.
	EntryID string
}

func (e *RunStillRunningError) Error() string {
	return "run still running: " + RunStillRunningSentence("", e)
}

// RunStillRunningSentence is what the model reads. It names the bound that
// fired, its value, what is left of the ceiling, and the continuation — in
// words, because the model acts on the sentence and not on a reason code.
//
// The person is deliberately not mentioned as somebody to wake: the ladder
// is "the lease asks the model; the model may ask the person if it needs
// to", and which of those the model does is its own judgement, taken through
// the paths it already has.
func RunStillRunningSentence(tool string, e *RunStillRunningError) string {
	if e == nil {
		return ""
	}
	call := "the command"
	if tool != "" {
		call = "your call to " + tool
	}
	s := "STILL RUNNING: " + call + " has printed nothing for " +
		humanDuration(e.Quiet) + " and has NOT been stopped — it is still executing. " +
		"Judge whether that silence is expected for this command: a stuck mount, a compile with no progress output and a wedged process all look like this. " +
		"Answer with session.wait on run id " + e.RunID +
		": \"continue\" keeps waiting on the SAME execution (it is not restarted), \"stop\" ends it. " +
		"It will be stopped anyway in " + humanDuration(e.Remaining) + ", which is the limit the person set and which you cannot extend."
	if e.Renewals > 0 {
		s += " You have already chosen to keep waiting " + plural(e.Renewals, "time", "times") + " on this command."
	}
	if e.ClampedFrom > 0 {
		s += " You asked for a quiet bound of " + humanDuration(e.ClampedFrom) +
			"; the person's limit is " + humanDuration(e.Quiet) + ", so that is what was used."
	}
	return s
}

// humanDuration writes a lease bound in words, always — "10 minutes",
// "90 seconds" as "1 minute 30 seconds" — so the number the model is told is
// spelled the way the settings screen spells it and the way the rest of the
// sentence around it is spelled. Go's own "1m30s" was the one token in that
// paragraph a reader had to decode, and a bound the model misreads is a
// bound it plans badly against.
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "no time at all"
	}
	d = d.Round(time.Second)
	if d < time.Minute {
		s := int(d / time.Second)
		if s == 0 {
			return "under a second"
		}
		return plural(s, "second", "seconds")
	}
	minutes := int(d / time.Minute)
	seconds := int((d % time.Minute) / time.Second)
	if seconds == 0 {
		return plural(minutes, "minute", "minutes")
	}
	return plural(minutes, "minute", "minutes") + " " + plural(seconds, "second", "seconds")
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func plural(n int, one, many string) string {
	return strconv.Itoa(n) + " " + pluralWord(n, one, many)
}

// RunLeaseError is the terminal failure of a run whose lease bound fired
// (ADR-0020 decision 2). When SubmissionExpired is false, the execution was
// terminalized by its wall-clock deadline (TermTimeout), its inactivity
// deadline (TermInactivity) or its output budget (TermOutputBudget), after
// cancellation escalated INT → TERM → KILL against the execution's process
// group. When SubmissionExpired is true, the bound fired before the broker
// delivered the run request, so no execution existed to terminalize and no
// escalation is attempted. The transport's RequestRun returns this once any
// required escalation has completed; EntryID carries the renderer-minted
// lifecycle/ledger entry when submission reached that point, so the assistant
// can preserve the command→turn cause edge even on abandonment. The policy
// middleware records Reason on the attempt when one exists, and the run driver
// turns it into the failure sentence the block shows. Err is the underlying
// broker terminalization (usually context.Canceled — the request was cancelled
// so a late resolution could not win the race and report the run completed).
type RunLeaseError struct {
	Reason            content.TerminationReason
	Err               error
	EntryID           string
	SubmissionExpired bool
}

func (e *RunLeaseError) Error() string {
	return fmt.Sprintf("run lease: %s: %v", e.Reason, e.Err)
}

func (e *RunLeaseError) Unwrap() error { return e.Err }

// RunLeaseSentence names the outcome of the lease bound. Both the model-facing
// tool result and the transport runState use this vocabulary.
func RunLeaseSentence(reason content.TerminationReason, submissionExpired bool) string {
	if submissionExpired {
		return "the run submission expired before execution started"
	}
	switch reason {
	case content.TermTimeout:
		return "the command did not finish within its wall-clock deadline and was terminalized"
	case content.TermInactivity:
		// Reachable only for a lease with no quiet-bound answerer at all.
		// The ordinary path no longer terminalizes on silence: it parks and
		// asks (RunStillRunningSentence).
		return "the command was terminalized for inactivity: it produced no output for too long"
	case content.TermAgentDeclined:
		return "you were asked whether to keep waiting for this command and answered stop, so it was ended"
	case content.TermOutputBudget:
		return "the command was terminalized: its output exceeded the budget, and was bounded rather than truncated"
	default:
		return "the command was terminalized by its lease"
	}
}

// RunLeaseUnavailableSentence names a bound that could not be armed because
// shell integration was unavailable. This is separate from RunLeaseSentence:
// one describes a bound that ended a command, while the other describes a
// bound that never applied and therefore must not become a termination reason.
func RunLeaseUnavailableSentence(bound RunLeaseBound) string {
	switch bound {
	case RunLeaseBoundInactivity:
		return "the inactivity bound is not active because shell integration is unavailable"
	case RunLeaseBoundOutput:
		return "the output bound is not active because shell integration is unavailable"
	default:
		return "this lease bound is not active because shell integration is unavailable"
	}
}
