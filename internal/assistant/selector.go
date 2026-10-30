package assistant

import (
	"context"
	"errors"
	"fmt"
)

const (
	EngineEino                 = "eino"
	EnginePydantic             = "pydantic-deepagents"
	RuntimeUnavailableSentence = "The experimental agent runtime is unavailable. Turn off Pydantic agent (Experimental) in Settings → Assistant → Answers."
)

var ErrRuntimeUnavailable = errors.New(RuntimeUnavailableSentence)

// Engine is the immutable selection pinned to one admitted operation. ToolName
// renders trusted model-facing references; nil retains canonical registry names.
type Engine struct {
	ID             string
	RuntimeVersion string
	Client         Client
	ToolName       func(string) string
}

type EngineSelector interface {
	Selected(context.Context) (Engine, error)
}

type engineSelector struct {
	eino           Engine
	experimental   Engine
	enabled        func(context.Context) (bool, error)
	runtimeVersion func(context.Context) (string, error)
}

// NewEngineSelector selects outside both implementations. runtimeVersion only
// checks packaged files/manifest and is never called for a flag-off operation.
func NewEngineSelector(eino, experimental Engine, enabled func(context.Context) (bool, error), runtimeVersion func(context.Context) (string, error)) EngineSelector {
	return &engineSelector{eino: eino, experimental: experimental, enabled: enabled, runtimeVersion: runtimeVersion}
}

func (s *engineSelector) Selected(ctx context.Context) (Engine, error) {
	if s.enabled == nil {
		return Engine{}, errors.New("assistant engine setting is unavailable")
	}
	enabled, err := s.enabled(ctx)
	if err != nil {
		return Engine{}, fmt.Errorf("assistant engine setting: %w", err)
	}
	if !enabled {
		if s.eino.Client == nil || s.eino.ID != EngineEino {
			return Engine{}, errors.New("Eino assistant is unavailable")
		}
		return s.eino, nil
	}
	selected := s.experimental
	if selected.Client == nil || selected.ID != EnginePydantic || s.runtimeVersion == nil {
		return selected, ErrRuntimeUnavailable
	}
	version, err := s.runtimeVersion(ctx)
	if err != nil || version == "" {
		return selected, ErrRuntimeUnavailable
	}
	selected.RuntimeVersion = version
	return selected, nil
}
