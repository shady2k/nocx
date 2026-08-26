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
func streamModelAnswer(ctx context.Context, logger log.Logger, httpClient *http.Client, key credential.Secret, baseURL, model string, headers []Header, msgs []*schema.Message, tools []tool.BaseTool, handlers []adk.ChatModelAgentMiddleware, checkpoints *runCheckpoints, checkpointID string, onEvent func(AskEvent) error) error {
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

	// The run goes through adk.Runner rather than agent.Run, and that is
	// what makes an approval resumable at all: ONLY the Runner carries a
	// CheckPointStore. agent.Run builds a private temporary bridge store of
	// its own (adk/chatmodel.go), so adding WithCheckPointID to it changes
	// nothing — the checkpoint dies with the call that wrote it.
	//
	// Under the Runner, ToolsNode checkpoints its INPUT message with its
	// tool calls and restores it before regenerating the tasks
	// (compose/tool_node.go). That is the whole fix: the resumed attempt
	// sees the SAME call id and the SAME arguments the person approved,
	// already-executed siblings are retained by call id, and no model call
	// is spent rebuilding a proposal the model would have re-rolled with a
	// fresh id — which is what left a person answering the same question
	// forever.
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		CheckPointStore: checkpoints,
	})
	var it *adk.AsyncIterator[*adk.AgentEvent]
	if target, ok := checkpoints.resumable(checkpointID); ok {
		// A resume, and it takes NO messages: the continuation is the
		// checkpoint, and the target names WHICH suspended branch goes on.
		// The decision itself is not passed here — it lives in the
		// ApprovalStore, which the middleware consults when the restored
		// call reaches the pipeline again; eino's resume data would be a
		// second place for one answer to live.
		var err error
		it, err = runner.ResumeWithParams(ctx, checkpointID, &adk.ResumeParams{
			Targets: map[string]any{target: nil},
		}, runOpts...)
		if err != nil {
			return fmt.Errorf("resume the suspended run: %w", err)
		}
	} else {
		// A first drive. Without a run id there is nothing to key a
		// checkpoint by — and nothing an approval could bind to either
		// (the binding starts with the run), so such a run cannot suspend
		// resumably and does not pretend to.
		if checkpointID != "" {
			runOpts = append(runOpts, adk.WithCheckPointID(checkpointID))
		}
		it = runner.Run(ctx, msgs, runOpts...)
	}
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
			// The Runner has already written the checkpoint by the time
			// this event reaches us (it saves, then sends). Recording the
			// interrupt's id against it is what completes the pair the
			// next drive reads: the bytes are the framework's, the branch
			// is ours to name.
			if req, interruptID := approvalRequestFrom(ev.Action.Interrupted); req != nil {
				checkpoints.suspend(checkpointID, interruptID)
				return &ApprovalRequestedError{Request: req}
			}
			// The egress gate's ask (design §7.1): a tool result contained
			// secret-shaped material and the run suspended before the bytes
			// left for the provider. Same shape as the inbound ask — NOT a
			// failure, the state the approval surface renders.
			if req, interruptID := egressRequestFrom(ev.Action.Interrupted); req != nil {
				checkpoints.suspend(checkpointID, interruptID)
				return &EgressRequestedError{Request: req}
			}
			return &ApprovalRequestedError{}
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
			mw, err := newPolicyMiddleware(c.log, *p.Grant, c.tools, p.AttemptLedger, approvals, p.KnownMaterial, p.RunID, p.Attempt, p.TurnEntryID, p.Requester, classifier, func(call ToolCall) error {
				return sink(AskEvent{Kind: AskToolCall, Call: &call})
			})
			if err != nil {
				return err
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
	}

	// The checkpoint is keyed by the run id, because that is what a drive
	// of this run already is: one run, one suspension, one continuation.
	// So an Ask whose run has a live checkpoint IS the resume of it —
	// there is no second flag to keep in step with the store, and no way
	// to ask for a resume of a run that is not suspended.
	err := streamModelAnswer(ctx, c.log, c.http, p.Key, p.BaseURL, p.Model, p.Headers, msgs, declared, handlers, c.checkpoints, p.RunID, sink)
	if err != nil {
		// A suspension is the ONE outcome that keeps the checkpoint: the
		// run is not over, and the checkpoint is the only thing that can
		// carry it past the person's answer. Every other error ends the
		// run — a refusal, a malformed proposal, a failed stream, a
		// cancelled context — and a run that has ended leaves nothing
		// behind (ADR-0028: checkpoints are deleted on terminalization).
		// v0.9.13 deletes nothing itself; the store's owner is
		// responsible, which the framework says in as many words.
		var apErr *ApprovalRequestedError
		var egErr *EgressRequestedError
		if !errors.As(err, &apErr) && !errors.As(err, &egErr) {
			c.Discard(p.RunID)
		}
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

// Discard implements Client: it drops whatever suspended state the engine
// holds for one run. Ask calls it on every terminal outcome of its own; the
// transport calls it for the terminal outcomes Ask never returns from — the
// person declined, or the run was closed while its question was still open.
// Discarding a run that holds nothing is the ordinary case and does
// nothing.
func (c *client) Discard(runID string) {
	if runID == "" {
		return
	}
	_ = c.checkpoints.Delete(context.Background(), runID)
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
