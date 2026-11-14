package harness

import (
	"context"

	"github.com/shady2k/nocx/internal/assistant"
)

// TurnAuthority is the assistant kernel bound to one turn. The session never
// rebinds authority already admitted by the endpoint.
type TurnAuthority = assistant.TurnTools

// ToolSession owns a process generation's bearer and the settlement boundary
// between its turns. EndTurn closes admission before waiting for accepted work.
// Retire additionally revokes the bearer. Both may report a caller deadline;
// that does not make the generation reusable.
type ToolSession interface {
	BeginTurn(TurnAuthority) error
	EndTurn(context.Context) error
	Pending() bool
	Retire(context.Context) error
}

// ToolSessionFactory mints the private bridge once for each process generation.
// A nil factory means the harness has no endpoint-backed tools.
type ToolSessionFactory func(context.Context, SessionInfo) (ToolSession, ToolBridge, error)
