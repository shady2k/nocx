package lifecycle

import "time"

// AttemptState is the terminal-or-not status of one execution attempt.
type AttemptState uint8

const (
	AttemptOpen AttemptState = iota + 1
	AttemptCompleted
	AttemptUnknown
)

// AttemptOrigin records where the attempt came from (decision 5: both origins
// are legitimate — an authenticated start is exactly as attributable as an
// authenticated complete).
type AttemptOrigin uint8

const (
	// OriginApp: created synchronously at editor submit, before the bytes
	// that could cause the shell's own start are written to the pty.
	OriginApp AttemptOrigin = iota + 1
	// OriginShell: created by an authenticated start with nothing pending.
	OriginShell
)

// ExecutionAttempt is one command execution. It belongs to exactly one domain
// and cannot cross an activation boundary. The app-owned Command text and the
// shell's view are kept distinct: on attachment the shell's text is ignored
// outright (the wire line may carry vault-resolved secrets while the app's
// text carries references — decision 5's privacy rule).
type ExecutionAttempt struct {
	ID        AttemptID
	Domain    DomainID
	Lane      LaneID
	Command   string // app-owned text; for shell-originated attempts, the shell's line
	Cwd       string
	Host      string
	StartedAt time.Time
	Origin    AttemptOrigin
	// SubmitID is the correlation token the renderer minted for the submit
	// that created this attempt, echoed back so the renderer can bind the
	// ledger record it opened to this attempt by equality. Empty for a
	// shell-originated attempt, which has no submit behind it. It is a
	// correlation token and never an identity: ID is the backend's
	// (decision 5), and nothing may be looked up by SubmitID here.
	SubmitID    string
	Started     bool // true once an authenticated start attached or created it
	State       AttemptState
	ExitCode    *int // set exactly once, only by an authenticated completion
	CompletedAt *time.Time
	Fence       FenceNonce // the completion's render fence
	// shellID is the id the shell itself mints for this attempt and names in
	// its start and snapshot envelopes (the shell never learns the app-minted
	// id — protocol §8 — so its own id is the only name it can report). It is
	// set exactly once, only when an authenticated start attaches to a
	// pending app attempt, and is deliberately unexported: it resolves TO the
	// app id and must never become the attempt's identity, never appear in a
	// published fact, and never cross JSON-RPC (ADR-0024 constraint b).
	shellID AttemptID
}
