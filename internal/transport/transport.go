package transport

import (
	"context"
	"fmt"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

type Transport interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

type Stub struct {
	log      log.Logger
	sessions session.Registry
	started  bool
}

func NewStub(logger log.Logger, reg session.Registry) *Stub {
	return &Stub{
		log:      logger,
		sessions: reg,
	}
}

func (t *Stub) Start(ctx context.Context) error {
	if t.started {
		return fmt.Errorf("transport already started")
	}
	t.started = true
	t.log.Info("transport stub: started")
	return nil
}

func (t *Stub) Stop(ctx context.Context) error {
	if !t.started {
		return nil
	}
	t.started = false
	t.log.Info("transport stub: stopped")
	for _, s := range t.sessions.List() {
		t.log.Debug("transport stub: closing session", "id", string(s.ID()))
		if err := s.Close(); err != nil {
			t.log.Warn("transport stub: error closing session", "id", string(s.ID()), "error", err)
		}
	}
	return nil
}
