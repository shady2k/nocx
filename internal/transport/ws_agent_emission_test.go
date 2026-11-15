package transport

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	agentharness "github.com/shady2k/nocx/internal/harness"
)

// Keep a real tool executor open after cancellation so Cancel cannot be
// mistaken for the settlement that Wait promises.
type emissionFetcher struct {
	started   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
}

func (f *emissionFetcher) FetchText(ctx context.Context, _ apifetch.TextRequest) (apifetch.TextDocument, error) {
	close(f.started)
	<-ctx.Done()
	close(f.cancelled)
	<-f.release
	return apifetch.TextDocument{}, ctx.Err()
}

type emissionHarness struct {
	agentharness.Harness
	fetcher *emissionFetcher
	turn    *emissionTurn
}

func (*emissionHarness) Readiness(context.Context) agentharness.Readiness {
	return agentharness.Readiness{State: agentharness.Ready}
}
func (*emissionHarness) Facts() agentharness.Facts { return agentharness.Facts{} }

func (h *emissionHarness) Run(ctx context.Context, spec agentharness.TurnSpec) (agentharness.Turn, error) {
	toolCtx, cancel := context.WithCancel(ctx)
	turn := &emissionTurn{events: make(chan agentharness.Event), done: make(chan struct{}), waiting: make(chan struct{}), cancel: cancel}
	h.turn = turn
	tools, ok := spec.Authority.(*assistant.RunTools)
	if !ok {
		return nil, errors.New("missing run tools")
	}
	go func() {
		defer close(turn.done)
		invocation, err := tools.Admit(toolCtx)
		if err != nil {
			return
		}
		invocation.Context = toolCtx
		invocation.Method = "fetch.url"
		invocation.RawParams = json.RawMessage(`{"url":"https://example.test/page"}`)
		_, _ = tools.Dispatch(invocation)
	}()
	go func() {
		defer close(turn.events)
		turn.events <- agentharness.Event{Kind: agentharness.EventToolStarted, Tool: &agentharness.ToolCall{ID: "fetch-call"}}
		<-h.fetcher.started
		turn.events <- agentharness.Event{Kind: agentharness.EventAnswerDelta, Text: "partial"}
	}()
	return turn, nil
}

type emissionTurn struct {
	events  chan agentharness.Event
	done    chan struct{}
	waiting chan struct{}
	cancel  context.CancelFunc
}

func (t *emissionTurn) Events() <-chan agentharness.Event { return t.events }
func (t *emissionTurn) Cancel(context.Context) error      { t.cancel(); return nil }
func (t *emissionTurn) Wait(context.Context) (agentharness.Outcome, error) {
	close(t.waiting)
	<-t.done
	return agentharness.Outcome{}, agentharness.ErrCancelled
}

func TestAgentHarnessEmissionErrorWaitsForBlockedToolSettlement(t *testing.T) {
	fetcher := &emissionFetcher{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	backend := &emissionHarness{fetcher: fetcher}
	h := newAskHarnessWithOpts(t, answeringScript(), WithAgentHarness("test", backend), WithAgentPolicy(autonomousPolicyStore(t)), WithAgentFetcher(fetcher))
	handler := h.ws.agentTerminalizer(nil)
	grant := h.ws.runGrantFor("test")
	emitErr := errors.New("answer persistence failed")
	returned := make(chan error, 1)
	go func() {
		returned <- handler.askHarness(t.Context(), askRunContext{}, assistant.AskParams{
			Fetcher: fetcher, Grant: grant, FreshGrant: func() *content.Grant { return grant }, AttemptLedger: &deliveryLedger{}, KnownMaterial: adapterKnownMaterial(h.v),
		}, func(ev agentAskEvent) error {
			if ev.Kind == agentAskAnswer {
				return emitErr
			}
			return nil
		})
	}()
	defer close(fetcher.release)
	select {
	case <-fetcher.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("emission failure did not cancel the blocked executor")
	}
	select {
	case err := <-returned:
		t.Fatalf("askHarness returned before the blocked tool settled: %v", err)
	case <-backend.turn.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("emission failure did not wait for turn settlement")
	}
	select {
	case err := <-returned:
		t.Fatalf("askHarness returned while the executor was still blocked: %v", err)
	default:
	}
	fetcher.release <- struct{}{}
	select {
	case err := <-returned:
		if !errors.Is(err, emitErr) {
			t.Fatalf("error = %v, want original emission error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("settled turn did not return")
	}
}
