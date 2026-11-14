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
	"sync"
	"sync/atomic"

	"github.com/shady2k/nocx/internal/operation/catalog"

	openai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
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

// streamModelAnswer drives one run through the adk agent and emits its
// ordered event stream to onEvent: the answer's chunks, the model's
// reasoning, and — through the policy middleware, which is the only place
// that knows a call is about to run — its tool calls.
//
// onEvent returns an error to ABORT the stream — the caller's write was
// refused, and the model must stop rather than keep producing chunks nobody
// can deliver (the probe's write-only callback cannot express that; the ask
// transaction needs it so a refused socket write terminalizes the run
// instead of wedging it). The abort error is returned as-is.
//
// Every error this returns is a stream failure the caller maps into a probe
// outcome or a terminal run state; a nil return means a response was
// received in full.
func streamModelAnswer(ctx context.Context, logger log.Logger, httpClient *http.Client, key credential.Secret, baseURL, model string, headers []Header, msgs []*schema.Message, tools []tool.BaseTool, unknownTool func(ctx context.Context, name, rawArgs string) (string, error), handlers []adk.ChatModelAgentMiddleware, onEvent func(AskEvent) error) error {
	scope := catalog.BeginInternal(ctx, catalog.OpInternalAssistantAskStream)
	defer scope.Complete()
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
		// ExecuteSequentially is MANDATORY, not a preference: the calls of
		// one turn run one at a time, in order (ADR-anntnxdj §2). The ADK's
		// default is PARALLEL, where a later call races the earlier one; the
		// pipeline's per-turn order enforces the same rule for a caller that
		// is not eino, and this is what keeps the framework from presenting
		// it with two calls at once.
		//
		// UnknownToolsHandler is the framework's seam for a name we never
		// declared, and without it the node FAILS: eino resolves the call
		// before any middleware of ours runs, so a model reaching for a tool
		// that does not exist ended the run with a framework error nothing
		// could name — over the most ordinary mistake a model makes. The
		// carrier answers it, because what to say depends on which one is
		// driving (carrier.go's UnknownTool).
		cfg.ToolsConfig = adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               tools,
				ExecuteSequentially: true,
				UnknownToolsHandler: unknownTool,
			},
		}
	}
	// The policy middleware (nocx-lndv): the run's grant decides what the
	// model may call. It rides the agent's Handlers — the pipeline at the
	// framework's own seam. Zero handlers (every explain-mode run today)
	// changes nothing.
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

	// One run, one drive, and no continuation: a person's question blocks the
	// call that asked it (ADR-anntnxdj §3), so there is no suspension for a
	// checkpoint to carry and nothing to resume. agent.Run builds a private,
	// temporary checkpoint store of its own (adk/chatmodel.go) and nothing
	// reads it.
	it := agent.Run(ctx, &adk.AgentInput{Messages: msgs, EnableStreaming: true}, runOpts...)
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
		// THE ROLE DECIDES WHETHER THIS IS THE ANSWER AT ALL (nocx-bshm2).
		// The loop used to emit any message that had content, and a tool
		// RESULT is a message — adk builds it with EventFromMessage(…,
		// schema.Tool, toolName) (adk/wrappers.go), and its content is the
		// tool's raw JSON return. So a readScreen result was rendered as
		// though the assistant had spoken it, in the middle of the answer.
		//
		// The check is an ALLOW-LIST rather than "not a tool result": the
		// answer is the ASSISTANT's own words and nothing else, so a role
		// this loop has never seen is skipped rather than spoken. Every
		// model-output event adk produces for *schema.Message carries
		// schema.Assistant (adk/interface.go typedModelOutputEvent), and
		// copies preserve it, so nothing the model says is lost by it.
		if mo.Role != schema.Assistant {
			continue
		}
		if mo.IsStreaming && mo.MessageStream != nil {
			stream := mo.MessageStream
			// Read-once, must close exactly once (schema/stream.go) —
			// drained to EOF or returned early, either way it closes.
			defer stream.Close()
			// With a tool set registered (handlers non-empty), the
			// answer-order middleware drained THIS response's stream on the
			// graph goroutine and emitted every chunk through the same
			// callback BEFORE the response reached the tools node
			// (answer_order.go). The event's stream is a second copy of the
			// same chunks, and draining it here would emit each one twice.
			// len(handlers) is the exact condition: the middleware is
			// registered in Ask exactly when handlers is built.
			if len(handlers) > 0 {
				continue
			}
			for {
				msg, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return err
				}
				if msg == nil {
					continue
				}
				// Reasoning FIRST, and separately: a chunk can carry both,
				// and concatenating them would put the thinking inside the
				// answer, which is the same defect in another shape
				// (nocx-s92so). eino's schema.Message.ReasoningContent is
				// "the thinking process of the model" and the
				// OpenAI-compatible adapter fills it from the wire's
				// `reasoning_content`; we used to decode it and drop it.
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
			continue
		}
		if mo.Message == nil {
			continue
		}
		if mo.Message.ReasoningContent != "" {
			if err := onEvent(AskEvent{Kind: AskReasoning, Text: mo.Message.ReasoningContent}); err != nil {
				return err
			}
		}
		if mo.Message.Content != "" {
			if err := onEvent(AskEvent{Kind: AskAnswer, Text: mo.Message.Content}); err != nil {
				return err
			}
		}
	}
}

// Ask implements Client. It streams the run through the same adk agent as
// the probe (streamModelAnswer is the one explain-mode run), over the same
// guarded HTTP client. Zero ANSWER text after a completed stream is a
// StreamError — the endpoint answered; it did not answer. Reasoning alone
// does not count: a model that only thought did not reply.
func (c *client) Ask(ctx context.Context, p AskParams, onEvent func(AskEvent) error) error {
	// The chain the transport put in the context becomes the chain
	// everything under this ask logs with — the kernel and the declared-call
	// carrier are both handed THIS logger. Binding it once here is what makes
	// an effect three layers down say which run and which wire frame it belongs
	// to without being told either.
	askLog := c.log
	if askLog != nil {
		askLog = askLog.WithContext(ctx)
	}
	if strings.TrimSpace(p.BaseURL) == "" {
		return fmt.Errorf("ask: base URL is required")
	}
	if strings.TrimSpace(p.Model) == "" {
		return fmt.Errorf("ask: model is required")
	}
	if p.RunID != "" && p.TurnEntryID != "" {
		// The HTTP recorder is below eino and can only learn which turn owns
		// these bytes through the context that crosses the model boundary.
		// Keep the unbound probe/test shape untouched.
		ctx = WithWireIdentity(ctx, p.RunID, p.TurnEntryID)
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
	// ONE ordered stream, and the mutex is what makes it one. The events
	// have two producers on two goroutines: this function's loop over the
	// agent's iterator, and the policy middleware, which runs inside eino's
	// tool node. The GRAPH orders them — a response's chunks are emitted by
	// the answer-order middleware on the graph goroutine before the tools
	// node can propose its calls (answer_order.go), and the answer written
	// FROM a result cannot be produced before the tool returns — but two
	// goroutines calling one callback would still interleave two writes.
	// This serializes them so the caller sees a stream, not a race.
	var emitMu sync.Mutex
	emit := func(e AskEvent) error {
		emitMu.Lock()
		defer emitMu.Unlock()
		return onEvent(e)
	}

	// The answer as it stands: accumulated for the "did this endpoint
	// actually answer" check below, and for nothing else. It carried a
	// UTF-16 LENGTH beside it until ADR-0040, which the middleware read to
	// anchor each cause in the prose; prose is a `text` entry with a seat of
	// its own now, so there is no offset left to take.
	//
	// Its own mutex rather than emitMu: writes arrive on the stream
	// goroutine while emit is not held, and taking emitMu here would make
	// the tally block the stream.
	var answerMu sync.Mutex
	var toolCalls atomic.Int64
	var text strings.Builder
	// The one callback both producers emit through: the engine's stream
	// drain, the answer-order middleware's drain (answer_order.go), and the
	// policy middleware's tool-call announcements. The accumulator lives
	// here so a chunk emitted from either drain is tallied the same way.
	sink := func(e AskEvent) error {
		if e.Kind == AskAnswer {
			answerMu.Lock()
			text.WriteString(e.Text)
			answerMu.Unlock()
		}
		if e.Kind == AskToolCall {
			toolCalls.Add(1)
		}
		return emit(e)
	}

	var declared []tool.BaseTool
	var runTools []tool.BaseTool
	var unknownTool func(ctx context.Context, name, rawArgs string) (string, error)
	var handlers []adk.ChatModelAgentMiddleware
	if p.Grant != nil {
		grant := *p.Grant
		grant.Policy = grant.Policy.WithFloor(c.floor)
		permitted := c.tools.ForGrant(grant)
		// The approval store is the client's own (process-lifetime, one
		// per client, keyed by the exact proposal); a caller may pass one
		// explicitly.
		approvals := p.Approvals
		if approvals == nil {
			approvals = c.approvals
		}
		// The fresh grant of ADR-anntnxdj §3: the caller's mint, wearing the
		// floor every grant this engine hands a run wears. The floor is the
		// ENGINE's (it is the build's), and a mint that came back without it
		// would be a second, weaker authority for the calls after an answer.
		var freshGrant func() *content.Grant
		if p.FreshGrant != nil {
			freshGrant = func() *content.Grant {
				minted := p.FreshGrant()
				if minted == nil {
					return nil
				}
				fresh := *minted
				fresh.Policy = fresh.Policy.WithFloor(c.floor)
				return &fresh
			}
		}
		mw, err := newPolicyMiddleware(askLog, grant, c.tools, p.AttemptLedger, approvals, p.KnownMaterial, p.RunID, p.SessionID, p.Attempt, p.TurnEntryID, p.Requester, Attachments{AutomaticItems: p.AutomaticSessionItems, MarkedWindows: p.MarkedSessionWindows}, func(call ToolCall) error {
			return sink(AskEvent{Kind: AskToolCall, Call: &call})
		}, toolSeams{
			sessions:          p.SessionSource,
			screenCapture:     p.ScreenCapture,
			noteOperation:     p.NoteOperation,
			snippetOperation:  p.SnippetOperation,
			fetcher:           p.Fetcher,
			snapshots:         c.snapshots,
			runID:             p.RunID,
			expansions:        p.Expansions,
			workerStore:       p.Workers,
			workerEnvironment: p.WorkerEnvironment,
			scripts:           p.Scripts,
			cwd:               p.Cwd,
			paneAccessBinder:  p.PaneAccessBinder,
			questioner:        p.Questioner,
			freshGrant:        freshGrant,
		})
		if err != nil {
			return err
		}
		if p.Presentation != nil {
			mw.presentation = *p.Presentation
			mw.presentationState = newPresentationState(p.Presentation.Loaded)
			mw.grantProvider = func() content.Grant {
				return grant
			}
			mw.searchSchema = append([]byte(nil), c.searchSchema...)
		}
		declared, err = mw.Declare(permitted)
		if err != nil {
			return err
		}
		// Keep ADK on its ToolsNode path even when every real tool is
		// refused. The policy middleware removes this internal anchor from
		// ToolInfos before every model call, so it is never declared.
		runTools = append(append([]tool.BaseTool(nil), declared...), &unknownToolAnchor{})
		if p.Presentation != nil && p.Presentation.Lazy && len(c.searchSchema) > 0 {
			search, err := mw.searchTool()
			if err != nil {
				return err
			}
			runTools = append(runTools, search)
		}
		unknownTool = func(_ context.Context, name, rawArgs string) (string, error) {
			return mw.UnknownTool(name, rawArgs)
		}
		// FIRST, so it is the outermost user wrapper: its Stream runs
		// before the policy middleware's and before the framework's
		// event sender forwards the response, which is what puts every
		// chunk of a response ahead of its tool-call event (the
		// transport's prose boundary depends on that order — see
		// answer_order.go). The policy middleware does not wrap the
		// model, so the two orders are equivalent; this one is stated.
		handlers = append(handlers, newOrderedAnswerMiddleware(sink), mw)
	}

	err := streamModelAnswer(ctx, askLog, c.http, p.Key, p.BaseURL, p.Model, p.Headers, msgs, runTools, unknownTool, handlers, sink)
	if err != nil {
		c.Discard(p.RunID)
		return err
	}
	c.Discard(p.RunID)
	if text.Len() == 0 {
		return &StreamError{Message: "the model returned no text"}
	}

	// A non-empty declared set means the model was offered tools. If no real
	// tool call crossed the policy seam and the complete answer is only a
	// textual envelope, the stream succeeded mechanically but not
	// semantically. Return a typed failure so the transport does not settle
	// the run as completed or replay the envelope as an answer.
	if len(declared) > 0 && toolCalls.Load() == 0 && IsUnexecutedToolCallEnvelope(text.String()) {
		return &UnexecutedToolCallError{}
	}
	return nil
}

// Discard implements Client: it drops whatever process-lifetime state the
// engine holds for one run — today the command snapshots a renderer request
// left behind. Ask calls it on every outcome of its own; the transport calls it
// for the outcomes Ask never returns from, so a run that ended by any path
// leaves nothing behind. Discarding a run that holds nothing is the ordinary
// case and does nothing.
func (c *client) Discard(runID string) {
	if runID == "" {
		return
	}
	if c.snapshots != nil {
		c.snapshots.Discard(runID)
	}
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

// toolDescription is the declaration's own sentence, unchanged. It used to
// render the ADR-0020 lattice — "run: effect mutate-destructive over
// session, executes InRenderer" — which is our vocabulary for AUTHORITY: it
// says who may do a thing and nothing whatever about what the thing does,
// and it was all the model had to go on. The row now carries a sentence
// written for the model, so there is nothing left to derive here; the
// effect and resource facts stay on the row for the consumers that decide
// on them (the policy, the middleware), which is the point of the split.
func toolDescription(t agenttools.Tool) string {
	return t.Description
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

const unknownToolAnchorName = "nocx.internal.unknown-tool-anchor"

// unknownToolAnchor is an internal, non-advertised tool that keeps ADK on its
// ToolsNode path when a grant permits no real tools. policyMiddleware removes
// its ToolInfo before every model call and handles a call naming it without
// invoking this method.
type unknownToolAnchor struct{}

func (*unknownToolAnchor) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: unknownToolAnchorName, Desc: "internal"}, nil
}

func (*unknownToolAnchor) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "", errors.New("internal unknown-tool anchor must not execute")
}
