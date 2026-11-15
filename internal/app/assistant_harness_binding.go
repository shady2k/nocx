package app

import (
	"context"
	"errors"
	"sync"

	"github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/turnbearer"
)

// assistantToolBinding bridges app construction and the endpoint published after
// Start. A session cannot mint tools until that endpoint has been bound.
type assistantToolBinding struct {
	mu     sync.RWMutex
	minter *turnbearer.Minter
}

func (b *assistantToolBinding) bind(endpoint turnbearer.Endpoint, helper string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.minter = turnbearer.NewMinter(endpoint, helper)
}

func (b *assistantToolBinding) Lookup(token string) (turnbearer.Authority, bool) {
	b.mu.RLock()
	minter := b.minter
	b.mu.RUnlock()
	if minter == nil {
		return turnbearer.Authority{}, false
	}
	return minter.Lookup(token)
}

func (b *assistantToolBinding) newSession(ctx context.Context, info harness.SessionInfo) (harness.ToolSession, harness.ToolBridge, error) {
	b.mu.RLock()
	minter := b.minter
	b.mu.RUnlock()
	if minter == nil {
		return nil, harness.ToolBridge{}, errors.New("assistant tools: endpoint is not available")
	}
	return minter.ToolSessions()(ctx, info)
}

// BindAssistantEndpoint supplies the endpoint started by the executable after
// App.Start installed the helper. The catalogue and ask share the same agent.
func (a *App) BindAssistantEndpoint(endpoint turnbearer.Endpoint) {
	a.assistantTools.bind(endpoint, a.localHelper.installedHelperBinary())
}
