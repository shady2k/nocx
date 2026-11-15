package harness

import (
	"context"
	"errors"
	"testing"
	"time"
)

type admissionProtocol struct {
	shellProtocol
	models chan string
}

func (p admissionProtocol) NewCodec(SessionInfo) Codec { return p }
func (p admissionProtocol) BeginTurn(spec TurnSpec) []Step {
	p.models <- spec.Model
	return []Step{{Command: Command{Name: "barrier"}, Barrier: true, Model: func() string { return "native" }}, {Command: Command{Name: "prompt"}, Prompt: true}}
}

func TestModelPinAdmissionBeforePromptAndQueuedTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	models := make(chan string, 3)
	m := NewSessionManager(Base{}, admissionProtocol{models: models})
	defer func() { _ = m.Close(ctx) }()
	first, err := m.Run(ctx, TurnSpec{ConversationID: "pin", ModelSelected: func(context.Context, string) error { return errors.New("pin write failed") }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = first.Wait(ctx); err == nil {
		t.Fatal("prompt ran despite pin write failure")
	}
}

func TestModelPinOverridesQueuedRole(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	models := make(chan string, 3)
	m := NewSessionManager(Base{}, admissionProtocol{models: models})
	defer func() { _ = m.Close(ctx) }()
	first, err := m.Run(ctx, TurnSpec{ConversationID: "pin"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Run(ctx, TurnSpec{ConversationID: "pin", Model: "changed-role"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = first.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = second.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	<-models
	if got := <-models; got != "native" {
		t.Fatalf("queued model = %q, want native pin", got)
	}
}
