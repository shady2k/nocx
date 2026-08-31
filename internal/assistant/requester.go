package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

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

// RunLeaseError is the terminal failure of a run whose lease bound fired
// (ADR-0020 decision 2): the execution was terminalized by its wall-clock
// deadline (TermTimeout), its inactivity deadline (TermInactivity) or its
// output budget (TermOutputBudget), after cancellation escalated
// INT → TERM → KILL against the execution's process group. The transport's
// RequestRun returns it once the escalation has completed; EntryID carries the
// renderer-minted lifecycle/ledger entry when submission reached that point,
// so the assistant can preserve the command→turn cause edge even on
// abandonment. The policy middleware records Reason on the attempt so the
// ledger says WHICH bound ended the run, and the run driver turns it into the
// failure sentence the block shows. Err is the underlying broker
// terminalization (usually context.Canceled — the request was cancelled so a
// late resolution could not win the race and report the run completed).
type RunLeaseError struct {
	Reason  content.TerminationReason
	Err     error
	EntryID string
}

func (e *RunLeaseError) Error() string {
	return fmt.Sprintf("run lease: %s: %v", e.Reason, e.Err)
}

func (e *RunLeaseError) Unwrap() error { return e.Err }

// RunLeaseSentence names the bound that terminalized a command. Both the
// model-facing tool result and the transport runState use this vocabulary.
func RunLeaseSentence(reason content.TerminationReason) string {
	switch reason {
	case content.TermTimeout:
		return "the command did not finish within its wall-clock deadline and was terminalized"
	case content.TermInactivity:
		return "the command was terminalized for inactivity: it produced no output for too long"
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
