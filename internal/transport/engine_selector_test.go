package transport

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log/logtest"
)

func fixedEinoSelector(client assistant.Client) assistant.EngineSelector {
	return assistant.NewEngineSelector(
		assistant.Engine{ID: assistant.EngineEino, Client: client}, assistant.Engine{},
		func(context.Context) (bool, error) { return false, nil }, nil,
	)
}

func newEinoTestClient(t *testing.T) (assistant.Client, agenttools.Registry, error) {
	t.Helper()
	_, logger := logtest.New(t)
	return assistant.NewClientAndRegistry(logger, assistant.NewModelHTTPFactory(logger, nil), content.Floor{}, nil)
}
