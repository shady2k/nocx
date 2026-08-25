package assistant

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shady2k/nocx/internal/content"
)

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
// RequestRun returns it once the escalation has completed; the policy
// middleware records Reason on the attempt so the ledger says WHICH bound
// ended the run, and the run driver turns it into the failure sentence the
// block shows. Err is the underlying broker terminalization (usually
// context.Canceled — the request was cancelled so a late resolution could
// not win the race and report the run completed).
type RunLeaseError struct {
	Reason content.TerminationReason
	Err    error
}

func (e *RunLeaseError) Error() string {
	return fmt.Sprintf("run lease: %s: %v", e.Reason, e.Err)
}

func (e *RunLeaseError) Unwrap() error { return e.Err }
