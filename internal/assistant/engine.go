package assistant

// The eino wiring (ADR-0028 decision 1, design §4.1): adk.ChatModelAgent
// with the OpenAI-compatible adapter from eino-ext. We do NOT write a
// tool-calling loop, an SSE client, or a provider adapter set — the
// framework's, all of it.
//
// Explain mode (design §4.2): terminate after the first completed response,
// context is question + referenced frames. The engine declares to the model
// exactly the tools the run's grant permits (design §5, nocx-pgtrh) —
// computed from the agenttools registry, never a static list. Today every
// caller has no grant, so the set is empty and the agent takes the no-tools
// path: with zero tools ADK builds buildNoToolsRunFunc, a direct model chain
// with no tools node, which is what the existing explain runs rely on. The
// policy middleware, the narrowed capability and the execution of a tool are
// nocx-lndv and deliberately do not live here.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	openai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"

	"github.com/shady2k/nocx/internal/agenttools"
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
func streamModelAnswer(ctx context.Context, logger log.Logger, httpClient *http.Client, key credential.Secret, baseURL, model string, headers []Header, msgs []*schema.Message, tools []tool.BaseTool, handlers []adk.ChatModelAgentMiddleware, onDelta func(string) error) error {
	cm, err := buildModel(httpClient, key, baseURL, model)
	if err != nil {
		return err
	}
	cfg := &adk.ChatModelAgentConfig{Model: cm}
	if len(tools) > 0 {
		// A non-empty tool set is what takes the agent off the no-tools path.
		// With zero tools ADK builds buildNoToolsRunFunc — a direct model
		// chain with no tools node — and the request carries no tools array;
		// that is the shape every existing explain run depends on, and the
		// probe below always passes nil.
		//
		// ExecuteSequentially is MANDATORY, not a preference: the policy
		// middleware's batch latch (nocx-lndv) stops the calls AFTER a
		// refused or escalated one, and it can only do that if the calls
		// actually run in order — the ADK's default is PARALLEL, where a
		// later call races the earlier refusal and can run before the latch
		// trips. The framework's own doc names the contract: sequential
		// means in order, NOT stop on failure — sequentialRunToolCall loops
		// every task and never inspects tasks[i].err, so the latch is what
		// makes a refused call stop the ones after it.
		cfg.ToolsConfig = adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools, ExecuteSequentially: true},
		}
	}
	// The policy middleware (nocx-lndv): the run's grant decides what the
	// model may call. It rides the agent's Handlers — the pipeline at the
	// framework's own seam — and BeforeAgent mints the batch latch for the
	// run. Zero handlers (every explain-mode run today) changes nothing.
	if len(handlers) > 0 {
		cfg.Handlers = handlers
	}
	agent, err := adk.NewChatModelAgent(ctx, cfg)
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
		// A suspension: the policy escalated (design §7.2) and the run is
		// awaiting approval — the call that asked has not run. This is not a
		// failure; it is the state the approval surface renders and the
		// resume re-validates. Without this check the interrupt event would
		// fall through the Output==nil guard and the suspension would be
		// silently reported as a completed run.
		if ev.Action != nil && ev.Action.Interrupted != nil {
			if req := approvalRequestFrom(ev.Action.Interrupted); req != nil {
				return &ApprovalRequestedError{Request: req}
			}
			// The egress gate's ask (design §7.1): a tool result contained
			// secret-shaped material and the run suspended before the bytes
			// left for the provider. Same shape as the inbound ask — NOT a
			// failure, the state the approval surface renders.
			if req := egressRequestFrom(ev.Action.Interrupted); req != nil {
				return &EgressRequestedError{Request: req}
			}
			return &ApprovalRequestedError{}
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
	var declared []tool.BaseTool
	var handlers []adk.ChatModelAgentMiddleware
	if p.Grant != nil {
		permitted := c.tools.ForGrant(*p.Grant)
		if len(permitted) > 0 {
			var err error
			declared, err = declaredTools(permitted)
			if err != nil {
				return err
			}
			// The approval store is the client's own (process-lifetime, one
			// per client, keyed by run id — ADR-0028: checkpoints are
			// process-lifetime state); a caller may pass one explicitly.
			approvals := p.Approvals
			if approvals == nil {
				approvals = c.approvals
			}
			// The classifier (bead nocx-kpy23): the middleware consults a
			// SECOND model for every permitted proposal. The engine builds
			// the classifier over ITS guarded client and the caller's role
			// resolver — the model call is the engine's (design §6: usage
			// has an owner); nil without a resolver is the feature off.
			var classifier CallClassifier
			if p.Classifier != nil {
				classifier = newClassifierEngine(c.log, c.http, p.Classifier)
			}
			mw, err := newPolicyMiddleware(*p.Grant, c.tools, p.AttemptLedger, approvals, p.KnownMaterial, p.RunID, p.Attempt, p.Requester, classifier)
			if err != nil {
				return err
			}
			handlers = append(handlers, mw)
		}
	}

	var text strings.Builder
	err := streamModelAnswer(ctx, c.log, c.http, p.Key, p.BaseURL, p.Model, p.Headers, msgs, declared, handlers, func(delta string) error {
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

// declaredTools converts the registry's assembled tools into the ADK tools
// the model is offered. The params schema each tool carries in the registry
// becomes the schema the model is shown, byte for byte: the model's
// arguments will be validated against the same file (design §6.2) by the
// middleware that owns validation.
func declaredTools(permitted []agenttools.Tool) ([]tool.BaseTool, error) {
	out := make([]tool.BaseTool, 0, len(permitted))
	for _, t := range permitted {
		var params jsonschema.Schema
		if err := json.Unmarshal(t.ParamsSchema, &params); err != nil {
			return nil, fmt.Errorf("tool %s: params schema: %w", t.Name, err)
		}
		out = append(out, &declaredTool{
			info: &schema.ToolInfo{
				Name:        t.Name,
				Desc:        toolDescription(t),
				ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&params),
			},
		})
	}
	return out, nil
}

// toolDescription derives the model-facing description from the declaration
// itself — one vocabulary, never a second one that can drift from the row.
func toolDescription(t agenttools.Tool) string {
	kinds := make([]string, 0, len(t.Resources))
	for _, k := range t.Resources {
		kinds = append(kinds, string(k))
	}
	return fmt.Sprintf("%s: effect %s over %s, executes %s", t.Name, t.Effect, strings.Join(kinds, ","), t.Executes)
}

// declaredTool is a tool the engine declares to the model but does not
// execute — this slice is declaration-only (design §5): the narrowed
// capability and the execution are nocx-lndv's. A model that calls one gets
// an honest "not executable yet", never a silent no-op; no grant exists in
// production yet, so no model is ever offered one of these.
type declaredTool struct {
	info *schema.ToolInfo
}

func (d *declaredTool) Info(context.Context) (*schema.ToolInfo, error) {
	return d.info, nil
}

func (d *declaredTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	return "", fmt.Errorf("tool %q is declared but not executable: nocx-lndv wires execution", d.info.Name)
}
