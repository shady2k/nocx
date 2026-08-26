package assistant

// The answer-order middleware: every chunk of one model response is emitted
// BEFORE the response's tool-call events, on the graph's own goroutine.
//
// WHY THIS EXISTS. The run's ordered stream has two producers: the engine's
// loop over the agent's iterator (which drains each model response's
// MessageStream and emits its chunks), and the policy middleware inside
// eino's tools node (which emits an AskToolCall when a call is about to
// run). A mutex serializes the two writes, but serialization is not order:
// the loop drains on the CALLER's goroutine while the tools node runs on
// the GRAPH's goroutine, so a response that both speaks and proposes a tool
// can deliver its call before the sentence that precedes it. That is not a
// cosmetic inversion — the transport seals the open prose block AT the call
// event (ADR-0040), so a call that arrives first finds no block to seal and
// the prose on both sides of it merge into one. The renderer used to paper
// over exactly this by cutting prose at the call announcement; the epic
// exists to remove the paper.
//
// The fix is to emit the chunks where the graph can see the order: the
// framework runs the event sender OUTSIDE all user WrapModel wrappers, so a
// wrapper's Stream drains the response and emits every chunk (reasoning and
// answer, in the same order the loop uses) BEFORE the wrapper returns and
// the response is handed to the tools node. The tools node cannot start
// until the model node completes, so "chunks first" stops being a race.
//
// The stream handed back is a SECOND copy of the same response, so the
// graph's output (the tools node's input and the next round's context) sees
// every chunk and every tool call unchanged. The engine's loop, knowing the
// wrapper drained this response, skips its own drain of the event stream —
// draining it again would emit each chunk twice.

import (
	"context"
	"errors"
	"io"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// orderedAnswerMiddleware is an adk.ChatModelAgentMiddleware whose WrapModel
// wraps the run's model so each response's stream is drained (and emitted)
// on the graph goroutine before the tools node can propose its calls.
// Registered for every run that carries a tool set — the only runs that
// have a tools node at all.
type orderedAnswerMiddleware struct {
	adk.BaseChatModelAgentMiddleware
	onEvent func(AskEvent) error
}

func newOrderedAnswerMiddleware(onEvent func(AskEvent) error) adk.ChatModelAgentMiddleware {
	return &orderedAnswerMiddleware{onEvent: onEvent}
}

func (m *orderedAnswerMiddleware) WrapModel(_ context.Context, base model.BaseModel[*schema.Message], _ *adk.TypedModelContext[*schema.Message]) (model.BaseModel[*schema.Message], error) {
	return &orderedAnswerModel{inner: base, onEvent: m.onEvent}, nil
}

type orderedAnswerModel struct {
	inner   model.BaseModel[*schema.Message]
	onEvent func(AskEvent) error
}

func (m *orderedAnswerModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.inner.Generate(ctx, input, opts...)
}

func (m *orderedAnswerModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	result, err := m.inner.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	if m.onEvent == nil {
		return result, nil
	}
	// One copy is drained and emitted HERE, on the graph's goroutine; the
	// other is what the agent consumes (the event stream and the tools
	// node's input are both derived from it downstream). Copy is the
	// framework's own fan-out (schema.StreamReader.Copy) — the same call
	// the event sender makes on the returned stream.
	streams := result.Copy(2)
	if err := emitAnswerStream(m.onEvent, streams[0]); err != nil {
		streams[0].Close()
		streams[1].Close()
		return nil, err
	}
	return streams[1], nil
}

// emitAnswerStream drains one model response stream, emitting reasoning and
// answer chunks through onEvent in the order they arrived. It is the exact
// mirror of the engine loop's drain (engine.go) — the two must never
// diverge, because one of them is skipped for every run that has a tools
// node, and a chunk the other one misses would be lost.
func emitAnswerStream(onEvent func(AskEvent) error, stream *schema.StreamReader[*schema.Message]) error {
	// Read-once, must close exactly once (schema/stream.go): drained to
	// EOF or returned early, either way it closes.
	defer stream.Close()
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if msg == nil {
			continue
		}
		// Reasoning first, and separately: a chunk can carry both, and
		// concatenating them would put the thinking inside the answer
		// (nocx-s92so) — the same rule the loop applies.
		if msg.ReasoningContent != "" {
			if err := onEvent(AskEvent{Kind: AskReasoning, Text: msg.ReasoningContent}); err != nil {
				return err
			}
		}
		if msg.Content != "" {
			if err := onEvent(AskEvent{Kind: AskAnswer, Text: msg.Content}); err != nil {
				return err
			}
		}
	}
}
