package transport

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	agentharness "github.com/shady2k/nocx/internal/harness"
)

type reorderedToolHarness struct {
	agentharness.Harness
	results chan error
}

type reorderedToolTurn struct {
	deliveryTurn
	cancel context.CancelFunc
}

func (t *reorderedToolTurn) Cancel(context.Context) error {
	t.cancel()
	return nil
}

func (h *reorderedToolHarness) Run(ctx context.Context, spec agentharness.TurnSpec) (agentharness.Turn, error) {
	tools, ok := spec.Authority.(*assistant.RunTools)
	if !ok {
		return nil, errors.New("missing run tools")
	}
	turnCtx, cancel := context.WithCancel(ctx)
	turn := &reorderedToolTurn{deliveryTurn: deliveryTurn{events: make(chan agentharness.Event), done: make(chan struct{})}, cancel: cancel}
	go func() {
		defer close(turn.done)
		defer close(turn.events)
		defer cancel()
		send := func(kind agentharness.EventKind, id string) {
			select {
			case turn.events <- agentharness.Event{Kind: kind, Tool: &agentharness.ToolCall{ID: id}}:
			case <-turnCtx.Done():
			}
		}
		send(agentharness.EventToolStarted, "model-first")
		send(agentharness.EventToolStarted, "model-second")

		// Reserve in the reverse order, as two MCP requests can arrive on the socket.
		request := assistant.ToolInvocation{Context: turnCtx, Method: "fetch.url", AgentCallKey: "2:model-second", RawParams: []byte(`{"url":"https://example.test/second"}`)}
		second, releaseSecond := tools.ReserveInvocation(request)
		defer releaseSecond()
		request.AgentCallKey = "1:model-first"
		request.RawParams = []byte(`{"url":"https://example.test/first"}`)
		first, releaseFirst := tools.ReserveInvocation(request)
		defer releaseFirst()

		// The harness's finish can be read while an accepted request's details
		// are still pending. The second request owns the first execution slot.
		send(agentharness.EventToolFinished, "model-first")
		firstResult := make(chan error, 1)
		go func() {
			_, err := tools.Dispatch(first)
			firstResult <- err
		}()
		_, err := tools.Dispatch(second)
		h.results <- err
		releaseSecond()
		h.results <- <-firstResult
		releaseFirst()

		send(agentharness.EventToolFinished, "model-second")
		send(agentharness.EventToolStarted, "model-first")
		request.AgentCallKey = "3:model-first"
		request.RawParams = []byte(`{"url":"https://example.test/later"}`)
		_, err = tools.Dispatch(request)
		h.results <- err
	}()
	return turn, nil
}

func TestAgentHarnessCorrelatesReorderedCallsAcrossFinish(t *testing.T) {
	backend := &reorderedToolHarness{results: make(chan error, 3)}
	fetcher := transportFetchTextFetcher{result: apifetch.TextDocument{
		URL: "https://example.test/page", ContentType: "text/html", Text: "hello from the page",
	}}
	h := newAskHarnessVault(t, answeringScript(), WithAgentHarness("test", backend), WithAgentPolicy(autonomousPolicyStore(t)))
	grant := h.ws.runGrantFor("test")
	var details []string
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := h.ws.agentTerminalizer(nil).askHarness(ctx, askRunContext{}, assistant.AskParams{
		Fetcher: fetcher, Grant: grant, FreshGrant: func() *content.Grant { return grant }, AttemptLedger: &deliveryLedger{}, KnownMaterial: adapterKnownMaterial(h.v),
	}, func(ev agentAskEvent) error {
		if ev.Kind == agentAskToolCall {
			url, ok := ev.Call.Args["url"].(string)
			if !ok {
				return errors.New("tool details are missing the URL")
			}
			details = append(details, ev.Call.CallID+":"+url)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if dispatchErr := <-backend.results; dispatchErr != nil {
			t.Fatal(dispatchErr)
		}
	}
	want := []string{"model-second:https://example.test/second", "model-first:https://example.test/first", "model-first:https://example.test/later"}
	if !slices.Equal(details, want) {
		t.Fatalf("tool details = %v, want %v", details, want)
	}
}
