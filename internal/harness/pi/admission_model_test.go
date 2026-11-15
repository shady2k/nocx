package pi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/harness"
	fakemodel "github.com/shady2k/nocx/internal/harness/fakemodeltest"
)

func TestRealPiAdmissionPinsDefaultBeforeCancelledFirstAnswer(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	c, fake := configuredPi(t, fakemodel.Response{Block: gate}, fakemodel.Response{Text: []string{"continued"}})
	defer func() { _ = c.Close(context.Background()) }()
	selected := make(chan string, 1)
	spec := harness.TurnSpec{ConversationID: "pin-before-answer", Text: "first question", Dir: t.TempDir(), ModelSelected: func(_ context.Context, model string) error { selected <- model; return nil }}
	turn := piTurn(t, c, spec)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := fake.WaitRequests(ctx, 1); err != nil {
		t.Fatal(err)
	}
	select {
	case model := <-selected:
		if model != "nocx-test/test-model" {
			t.Fatalf("admitted model: %q", model)
		}
	default:
		t.Fatal("provider request preceded model persistence")
	}
	if err := turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	piCollect(t, turn)
	if _, err := turn.Wait(ctx); !errors.Is(err, harness.ErrCancelled) {
		t.Fatalf("cancel: %v", err)
	}
	spec.Model = "nocx-test/changed-role-model"
	spec.ModelSelected = nil
	spec.Text = "continue question"
	piSuccess(t, c, spec)
	requests := fake.Requests()
	if len(requests) != 2 || requests[1].Model != requests[0].Model {
		t.Fatalf("model changed after cancellation: %+v", requests)
	}
}

func TestRealPiModelPersistenceFailurePreventsProviderRequest(t *testing.T) {
	c, fake := configuredPi(t, fakemodel.Response{Text: []string{"must not answer"}})
	defer func() { _ = c.Close(context.Background()) }()
	spec := harness.TurnSpec{ConversationID: "pin-write-failure", Text: "question", Dir: t.TempDir(), ModelSelected: func(context.Context, string) error { return errors.New("pin storage unavailable") }}
	turn := piTurn(t, c, spec)
	events := piCollect(t, turn)
	if _, err := turn.Wait(context.Background()); err == nil {
		t.Fatalf("persistence failure accepted: %+v", events)
	}
	if requests := fake.Requests(); len(requests) != 0 {
		t.Fatalf("unpersisted model reached provider: %+v", requests)
	}
}
