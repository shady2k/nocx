package turnbearer

import (
	"context"
	"fmt"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/harness"
)

// ToolSessions adapts the endpoint's session registry to the harness owner.
// Process generations receive distinct IDs even when a conversation restarts.
func (m *Minter) ToolSessions() harness.ToolSessionFactory {
	return func(ctx context.Context, info harness.SessionInfo) (harness.ToolSession, harness.ToolBridge, error) {
		if err := ctx.Err(); err != nil {
			return nil, harness.ToolBridge{}, err
		}
		id := fmt.Sprintf("%d:%s:%d", len(info.ConversationID), info.ConversationID, info.Generation)
		bridge, _, err := m.Mint(id)
		if err != nil {
			return nil, harness.ToolBridge{}, err
		}
		return &toolSession{minter: m, id: id}, harness.ToolBridge{Command: bridge.Command, Env: bridge.Env}, nil
	}
}

type toolSession struct {
	minter *Minter
	id     string
}

var _ harness.ToolSession = (*toolSession)(nil)

func (s *toolSession) BeginTurn(authority harness.TurnAuthority) error {
	tools, ok := authority.(assistant.TurnTools)
	if !ok {
		return fmt.Errorf("turn bearer: unsupported turn authority %T", authority)
	}
	return s.minter.BeginTurn(s.id, Authority{Tools: tools})
}

func (s *toolSession) EndTurn(ctx context.Context) error {
	return s.minter.EndTurnContext(ctx, s.id)
}
func (s *toolSession) Pending() bool { return s.minter.Pending(s.id) }
func (s *toolSession) Retire(ctx context.Context) error {
	return s.minter.RetireContext(ctx, s.id)
}
