package harness

import "context"

// TurnAuthority is the run's authority bound to one turn — the assistant's
// kernel for that run, supplied by the caller that starts the turn. The harness
// carries it to the ToolSession and never looks inside it; the session never
// rebinds authority already admitted by the endpoint.
type TurnAuthority any

// ToolSession owns a process generation's bearer and the settlement boundary
// between its turns. EndTurn closes admission before waiting for accepted work.
// Retire additionally revokes the bearer and cancels all work for the session.
// An EndTurn error requires retirement before settlement can be confirmed. A
// Retire error leaves work unconfirmed and the generation must never be reused.
type ToolSession interface {
	BeginTurn(TurnAuthority) error
	EndTurn(context.Context) error
	Pending() bool
	Retire(context.Context) error
}

// ToolSessionFactory mints the private bridge once for each process generation.
// A nil factory means the harness has no endpoint-backed tools.
type ToolSessionFactory func(context.Context, SessionInfo) (ToolSession, ToolBridge, error)
