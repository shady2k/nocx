package assistant

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/log/logtest"
)

func TestEngineSelectorPinsOperationsAndNeverChecksDisabledRuntime(t *testing.T) {
	ctx, _ := logtest.New(t)
	enabled := false
	checks := 0
	eino, experimental := &client{}, &client{}
	selector := NewEngineSelector(
		Engine{ID: EngineEino, RuntimeVersion: "eino-version", Client: eino},
		Engine{ID: EnginePydantic, Client: experimental},
		func(context.Context) (bool, error) { return enabled, nil },
		func(context.Context) (string, error) { checks++; return "manifest-digest", nil },
	)
	first, err := selector.Selected(ctx)
	if err != nil || checks != 0 || first.ID != EngineEino {
		t.Fatalf("disabled selection = %+v, %v; runtime checks=%d", first, err, checks)
	}
	enabled = true
	second, err := selector.Selected(ctx)
	if err != nil || checks != 1 || second.ID != EnginePydantic || second.RuntimeVersion != "manifest-digest" {
		t.Fatalf("experimental selection = %+v, %v; runtime checks=%d", second, err, checks)
	}
	enabled = false
	third, err := selector.Selected(ctx)
	if err != nil || checks != 1 || third.ID != EngineEino || second.Client != experimental || second.ID != EnginePydantic {
		t.Fatalf("new selection changed an admitted operation: first=%+v second=%+v third=%+v err=%v", first, second, third, err)
	}
}

func TestEngineSelectorUnavailableRuntimeDoesNotFallBack(t *testing.T) {
	ctx, _ := logtest.New(t)
	selector := NewEngineSelector(
		Engine{ID: EngineEino, Client: &client{}}, Engine{ID: EnginePydantic, Client: &client{}},
		func(context.Context) (bool, error) { return true, nil },
		func(context.Context) (string, error) { return "", errors.New("manifest missing") },
	)
	selected, err := selector.Selected(ctx)
	if !errors.Is(err, ErrRuntimeUnavailable) || err.Error() != RuntimeUnavailableSentence || selected.ID != EnginePydantic || selected.RuntimeVersion != "" {
		t.Fatalf("unavailable runtime = %+v, %v; want explicit experimental repair", selected, err)
	}
}

func TestEngineSelectorSettingFailureCannotGuessAnEngine(t *testing.T) {
	ctx, _ := logtest.New(t)
	settingErr := errors.New("settings cannot be read")
	selector := NewEngineSelector(
		Engine{ID: EngineEino, Client: &client{}}, Engine{ID: EnginePydantic, Client: &client{}},
		func(context.Context) (bool, error) { return false, settingErr },
		func(context.Context) (string, error) {
			t.Fatal("runtime checked after setting failure")
			return "", nil
		},
	)
	selected, err := selector.Selected(ctx)
	if !errors.Is(err, settingErr) || selected.ID != "" || selected.Client != nil {
		t.Fatalf("setting failure guessed an engine: %+v, %v", selected, err)
	}
}
