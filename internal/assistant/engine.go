package assistant

// The eino wiring (ADR-0028 decision 1, design §4.1): adk.ChatModelAgent
// with the OpenAI-compatible adapter from eino-ext, zero tools declared.
// We do NOT write a tool-calling loop, an SSE client, or a provider adapter
// set — the framework's, all of it.
//
// Explain mode only (design §4.2): zero tools, terminate after the first
// completed response, context is question + referenced frames. The tools,
// the policy middleware, the grant and the narrowed capability are nocx-lndv
// and deliberately do not live here. With zero tools ADK builds
// buildNoToolsRunFunc — a direct model chain with no tools node — so a
// hallucinated tool call cannot even reach a middleware; there is none.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	openai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
)

// buildModel constructs the OpenAI-compatible chat model for one
// endpoint+model over the guarded HTTP client. The key is a copy made
// deliberately inside Secret.Use, exactly as internal/capability does for
// the same boundary; it lives only in the model config, which dies with the
// function's scope.
func buildModel(httpClient *http.Client, key credential.Secret, baseURL, model string) (*openai.ChatModel, error) {
	var apiKey string
	if err := key.Use(func(b []byte) error {
		apiKey = string(b)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read API key: %w", err)
	}
	cm, err := openai.NewChatModel(context.Background(), &openai.ChatModelConfig{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		Model:      model,
		HTTPClient: httpClient,
	})
	if err != nil {
		return nil, fmt.Errorf("build model: %w", err)
	}
	return cm, nil
}

// streamModelAnswer streams the model's answer to msgs through the adk
// agent, calling onDelta for every content chunk. It is the explain-mode
// run: zero tools, terminate after the first completed response.
//
// onDelta returns an error to ABORT the stream — the caller's write was
// refused, and the model must stop rather than keep producing chunks nobody
// can deliver (the probe's write-only callback cannot express that; the ask
// transaction needs it so a refused socket write terminalizes the run
// instead of wedging it). The abort error is returned as-is.
//
// Every error this returns is a stream failure the caller maps into a probe
// outcome or a terminal run state; a nil return means a response was
// received in full.
func streamModelAnswer(ctx context.Context, logger log.Logger, httpClient *http.Client, key credential.Secret, baseURL, model string, headers []Header, msgs []*schema.Message, onDelta func(string) error) error {
	cm, err := buildModel(httpClient, key, baseURL, model)
	if err != nil {
		return err
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{Model: cm})
	if err != nil {
		return fmt.Errorf("build agent: %w", err)
	}

	// The endpoint's custom headers ride the model call as per-request extra
	// headers (eino's WithExtraHeader, applied when the adapter builds the
	// HTTP request), and their canonical names tag the context so the
	// guarded client's redirect rule drops exactly them on an origin change.
	var runOpts []adk.AgentRunOption
	if m, names := headerMap(headers); m != nil {
		runOpts = append(runOpts, adk.WithChatModelOptions([]einoModel.Option{openai.WithExtraHeader(m)}))
		ctx = withCustomHeaderNames(ctx, names)
	}

	it := agent.Run(ctx, &adk.AgentInput{
		Messages:        msgs,
		EnableStreaming: true,
	}, runOpts...)
	for {
		ev, ok := it.Next()
		if !ok {
			return nil
		}
		if ev.Err != nil {
			return ev.Err
		}
		if ev.Output == nil || ev.Output.MessageOutput == nil {
			continue
		}
		mo := ev.Output.MessageOutput
		if mo.IsStreaming && mo.MessageStream != nil {
			stream := mo.MessageStream
			// Read-once, must close exactly once (schema/stream.go) —
			// drained to EOF or returned early, either way it closes.
			defer stream.Close()
			for {
				msg, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return err
				}
				if msg != nil && msg.Content != "" {
					if err := onDelta(msg.Content); err != nil {
						return err
					}
				}
			}
			continue
		}
		if mo.Message != nil && mo.Message.Content != "" {
			if err := onDelta(mo.Message.Content); err != nil {
				return err
			}
		}
	}
}

// Ask implements Client. It streams the model's answer through the same adk
// agent as the probe (streamModelAnswer is the one explain-mode run), over
// the same guarded HTTP client. Zero content after a completed stream is a
// StreamError — the endpoint answered; it did not answer.
func (c *client) Ask(ctx context.Context, p AskParams, onDelta func(string) error) error {
	if strings.TrimSpace(p.BaseURL) == "" {
		return fmt.Errorf("ask: base URL is required")
	}
	if strings.TrimSpace(p.Model) == "" {
		return fmt.Errorf("ask: model is required")
	}
	msgs := make([]*schema.Message, 0, len(p.Messages))
	for _, m := range p.Messages {
		switch m.Role {
		case "user":
			msgs = append(msgs, schema.UserMessage(m.Content))
		case "assistant":
			msgs = append(msgs, schema.AssistantMessage(m.Content, nil))
		default:
			msgs = append(msgs, schema.SystemMessage(m.Content))
		}
	}
	var text strings.Builder
	err := streamModelAnswer(ctx, c.log, c.http, p.Key, p.BaseURL, p.Model, p.Headers, msgs, func(delta string) error {
		text.WriteString(delta)
		return onDelta(delta)
	})
	if err != nil {
		return err
	}
	if text.Len() == 0 {
		return &StreamError{Message: "the model returned no text"}
	}
	return nil
}
