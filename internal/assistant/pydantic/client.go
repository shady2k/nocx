package pydantic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
)

// Config injects the existing authority and HTTP owners. NewHosts is called
// exactly once with the fully constructed generator, avoiding cyclic wiring.
type Config struct {
	NewHosts func(assistant.TextGenerator) assistant.RunHostFactory
	HTTP     assistant.ModelHTTPFactory
	Registry agenttools.Registry
	Runtime  RuntimeLocator
}

const RuntimeLostSentence = "the experimental assistant runtime stopped unexpectedly. Ask again, or disable Pydantic agent (Experimental) in Settings"

type Client struct {
	hosts       assistant.RunHostFactory
	httpFactory assistant.ModelHTTPFactory
	http        *http.Client
	runtime     RuntimeLocator
	names       toolNames
	mu          sync.Mutex
	runs        map[string]*operation
}

var (
	_ assistant.Client        = (*Client)(nil)
	_ assistant.TextGenerator = (*Client)(nil)
)

func NewClient(cfg Config) (*Client, error) {
	if cfg.NewHosts == nil || cfg.HTTP == nil || cfg.Runtime == nil {
		return nil, errors.New("pydantic: host, HTTP and runtime dependencies are required")
	}
	names, err := newToolNames(cfg.Registry)
	if err != nil {
		return nil, err
	}
	c := &Client{httpFactory: cfg.HTTP, http: cfg.HTTP.NewModelHTTPClient(), runtime: cfg.Runtime, names: names, runs: make(map[string]*operation)}
	c.hosts = cfg.NewHosts(c)
	if c.hosts == nil || c.http == nil {
		return nil, errors.New("pydantic: host and HTTP factories returned nil")
	}
	return c, nil
}

// Readiness and RuntimeVersion inspect staged metadata, never start Python.
func (c *Client) Readiness(ctx context.Context) error { _, err := c.runtime.Locate(ctx); return err }

func (c *Client) RuntimeVersion(ctx context.Context) (string, error) {
	r, err := c.runtime.Locate(ctx)
	return r.Version, err
}

type (
	operationKey     struct{}
	generationResult struct {
		text string
		err  error
	}
)

type queuedFrame struct {
	frame envelope
	bytes int
}
type modelBinding struct {
	target  assistant.ModelTarget
	catalog catalogPayload
	ctx     context.Context
}

type operation struct {
	client       *Client
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	process      *process
	startID      string
	driving      bool
	host         assistant.RunHost
	pending      *envelope
	activeInvoke string
	failure      error
	intentional  bool
	bindings     map[string]modelBinding
	generations  map[string]chan generationResult
	streams      map[string]*httpStream
	httpFailures map[string]*assistant.EndpointHTTPError
	actions      chan queuedFrame
	queuedBytes  int
	queueSpace   chan struct{}
	lostOnce     sync.Once
	shutdownOnce sync.Once
	launchOnce   sync.Once
	launchErr    error
	onLost       func(error)
	batch        []proposal
	callIDs      map[string]bool
	response     uint64
	nextCall     int
	answer       strings.Builder
	toolCalls    int
}

func (c *Client) newOperation(ctx context.Context, onLost func(error)) *operation {
	life, cancel := context.WithCancel(context.WithoutCancel(ctx))
	o := &operation{client: c, cancel: cancel, bindings: make(map[string]modelBinding), generations: make(map[string]chan generationResult), streams: make(map[string]*httpStream), actions: make(chan queuedFrame, 128), queueSpace: make(chan struct{}, 1), onLost: onLost, callIDs: make(map[string]bool)}
	o.ctx = context.WithValue(life, operationKey{}, o)
	return o
}

func (o *operation) launch(ctx context.Context) error {
	o.launchOnce.Do(func() { o.launchErr = o.launchProcess(ctx) })
	return o.launchErr
}

func (o *operation) launchProcess(ctx context.Context) error {
	stop := context.AfterFunc(ctx, o.cancel)
	defer stop()
	r, err := o.client.runtime.Locate(ctx)
	if err != nil {
		return err
	}
	p, err := startProcess(o.ctx, r, o.accept, o.fail)
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.process = p
	cancelled := o.ctx.Err() != nil
	o.mu.Unlock()
	if cancelled {
		p.stop()
		return o.ctx.Err()
	}
	return nil
}

func (o *operation) fail(_ error) {
	o.mu.Lock()
	if o.intentional {
		o.mu.Unlock()
		return
	}
	if o.failure == nil {
		o.failure = &assistant.RuntimeLostError{StreamError: assistant.StreamError{Message: RuntimeLostSentence}}
	}
	err := o.failure
	o.mu.Unlock()
	o.cancel()
	o.lostOnce.Do(func() {
		if o.onLost != nil {
			go o.onLost(err)
		}
	})
	go o.shutdown()
}

func (o *operation) shutdown() {
	o.shutdownOnce.Do(func() {
		o.mu.Lock()
		o.intentional = true
		p := o.process
		for _, s := range o.streams {
			s.cancel()
		}
		o.mu.Unlock()
		o.cancel()
		if p != nil {
			p.stop()
		}
	})
}

func (c *Client) Discard(runID string) {
	c.mu.Lock()
	o := c.runs[runID]
	if o != nil {
		delete(c.runs, runID)
	}
	c.mu.Unlock()
	if o != nil {
		o.shutdown()
	}
	c.hosts.Discard(runID)
}

func (c *Client) Ask(ctx context.Context, p assistant.AskParams, onEvent func(assistant.AskEvent) error) (result error) {
	if strings.TrimSpace(p.BaseURL) == "" || strings.TrimSpace(p.Model) == "" {
		return errors.New("ask: base URL and model are required")
	}
	if onEvent == nil {
		return errors.New("ask: event callback is required")
	}
	p.ModelToolName = c.ModelToolName
	if p.RunID != "" && p.TurnEntryID != "" {
		ctx = assistant.WithWireIdentity(ctx, p.RunID, p.TurnEntryID)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	o := c.runs[p.RunID]
	if p.Resume && (p.RunID == "" || o == nil) {
		c.mu.Unlock()
		return &assistant.RuntimeLostError{StreamError: assistant.StreamError{Message: RuntimeLostSentence}}
	}
	if !p.Resume && o != nil {
		c.mu.Unlock()
		return errors.New("pydantic: the run is already admitted")
	}
	fresh := !p.Resume
	if fresh {
		o = c.newOperation(ctx, p.OnRuntimeLost)
		if p.RunID != "" {
			c.runs[p.RunID] = o
		}
	}
	o.mu.Lock()
	if o.driving {
		o.mu.Unlock()
		c.mu.Unlock()
		return errors.New("pydantic: the run is already being driven")
	}
	o.driving = true
	o.mu.Unlock()
	c.mu.Unlock()
	defer func() {
		if errors.Is(result, errProtocol) {
			o.fail(result)
		}
		o.mu.Lock()
		// fail publishes its cause before cancelling the operation. An active
		// host/event/launch path may return only cancellation; retain the same
		// cause the asynchronous callback carries before Discard reaps it.
		// A completed drive stays successful: a late losing callback must not
		// retroactively take ownership of its command leases.
		if result != nil && o.failure != nil {
			result = o.failure
		}
		o.driving = false
		o.host = nil
		o.mu.Unlock()
		if !suspension(result) {
			if p.RunID != "" {
				c.Discard(p.RunID)
			} else {
				o.shutdown()
			}
		}
	}()
	driveCtx, cancel := context.WithCancel(context.WithValue(ctx, operationKey{}, o))
	defer cancel()
	stopDrive := context.AfterFunc(ctx, o.cancel)
	defer stopDrive()
	stopOperation := context.AfterFunc(o.ctx, cancel)
	defer stopOperation()
	if err := o.ctx.Err(); err != nil {
		o.mu.Lock()
		failure := o.failure
		o.mu.Unlock()
		if failure != nil {
			return failure
		}
		return err
	}
	if fresh {
		if err := o.launch(driveCtx); err != nil {
			return err
		}
	}
	emit := func(e assistant.AskEvent) error {
		if err := o.ctx.Err(); err != nil {
			return err
		}
		if err := driveCtx.Err(); err != nil {
			return err
		}
		if e.Kind == assistant.AskAnswer {
			o.answer.WriteString(e.Text)
		}
		if e.Kind == assistant.AskToolCall {
			o.toolCalls++
		}
		return onEvent(e)
	}
	host, err := c.hosts.NewRunHost(driveCtx, p, func(call assistant.ToolCall) error {
		return emit(assistant.AskEvent{Kind: assistant.AskToolCall, Call: &call})
	})
	if err != nil {
		return err
	}
	catalog, err := c.names.catalog(host)
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.host = host
	o.mu.Unlock()
	if fresh {
		id := o.process.reserve("complete")
		o.mu.Lock()
		o.startID = id
		o.bindings[id] = modelBinding{assistant.ModelTarget{Key: p.Key, BaseURL: p.BaseURL, Model: p.Model, Headers: p.Headers}, catalog, o.ctx}
		o.mu.Unlock()
		if err = o.process.send("start", &id, startPayload{p.Model, c.names.messages(p.Messages), catalog.Tools, catalog.VisibleTools}); err != nil {
			return err
		}
	} else {
		o.mu.Lock()
		pending := o.pending
		o.pending = nil
		o.mu.Unlock()
		if pending == nil {
			return errors.New("pydantic: run has no suspended proposal")
		}
		if err = o.invoke(driveCtx, host, *pending, true); err != nil {
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-o.ctx.Done():
			o.mu.Lock()
			err = o.failure
			o.mu.Unlock()
			if err == nil {
				err = context.Canceled
			}
			return err
		case queued := <-o.actions:
			o.mu.Lock()
			o.queuedBytes -= queued.bytes
			o.mu.Unlock()
			select {
			case o.queueSpace <- struct{}{}:
			default:
			}
			e := queued.frame
			switch e.Kind {
			case "event":
				var event eventPayload
				if err = decodeStrict(e.Payload, &event); err != nil {
					return err
				}
				if event.OwnerRequestID != o.startID || (event.Kind != "answer" && event.Kind != "reasoning") {
					return errProtocol
				}
				if err = emit(assistant.AskEvent{Kind: assistant.AskEventKind(event.Kind), Text: event.Text}); err != nil {
					return err
				}
			case "catalog.request":
				var req catalogRequest
				if err = decodeStrict(e.Payload, &req); err != nil {
					return err
				}
				if req.OwnerRequestID != o.startID {
					return errProtocol
				}
				catalog, err = c.names.catalog(host)
				if err != nil {
					return err
				}
				o.mu.Lock()
				binding := o.bindings[o.startID]
				binding.catalog = catalog
				o.bindings[o.startID] = binding
				o.mu.Unlock()
				if err = o.process.send("catalog.result", e.RequestID, catalog); err != nil {
					return err
				}
			case "tool.invoke":
				if err = o.invoke(driveCtx, host, e, false); err != nil {
					return err
				}
			case "complete":
				var complete textPayload
				if err = decodeStrict(e.Payload, &complete); err != nil {
					return err
				}
				if e.RequestID == nil || *e.RequestID != o.startID {
					return errProtocol
				}
				if o.answer.Len() == 0 {
					return &assistant.StreamError{Message: "the model returned no text"}
				}
				if len(catalog.Tools) > 0 && o.toolCalls == 0 && assistant.IsUnexecutedToolCallEnvelope(o.answer.String()) {
					return &assistant.UnexecutedToolCallError{}
				}
				return nil
			case "error":
				return o.runtimeError(e)
			default:
				return errProtocol
			}
		}
	}
}

func suspension(err error) bool {
	var approval *assistant.ApprovalRequestedError
	var egress *assistant.EgressRequestedError
	return errors.As(err, &approval) || errors.As(err, &egress)
}

func (o *operation) invoke(ctx context.Context, host assistant.RunHost, e envelope, resume bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.ctx != nil {
		if err := o.ctx.Err(); err != nil {
			return err
		}
	}
	var p invokePayload
	if err := decodeStrict(e.Payload, &p); err != nil {
		return err
	}
	if p.OwnerRequestID != o.startID || p.Response == 0 || p.CallID == "" || len(p.RawArgs) > maxArguments || len(p.Batch) == 0 {
		return errProtocol
	}
	if !resume {
		if p.Response != o.response {
			if p.Response < o.response || o.batch != nil {
				return errProtocol
			}
			local := make(map[string]bool, len(p.Batch))
			for _, call := range p.Batch {
				if assistant.ValidateToolCallID(call.CallID) != nil || call.Name == "" || len(call.RawArgs) > maxArguments || local[call.CallID] || o.callIDs[call.CallID] {
					return errProtocol
				}
				local[call.CallID] = true
			}
			for id := range local {
				o.callIDs[id] = true
			}
			o.batch = p.Batch
			o.response = p.Response
			o.nextCall = 0
		} else if !slices.Equal(o.batch, p.Batch) {
			return errProtocol
		}
		if o.nextCall >= len(p.Batch) || p.Batch[o.nextCall] != (proposal{p.CallID, p.Name, p.RawArgs}) {
			return errProtocol
		}
	}
	canonical, recognized := o.client.names.toCanonical[p.Name]
	admitted := false
	if recognized {
		for _, t := range host.Tools() {
			if t.Name == canonical {
				admitted = true
				break
			}
		}
	} else {
		canonical = p.Name
	}
	var output assistant.InvocationResult
	var err error
	if admitted {
		output, err = host.InvokeClassified(ctx, canonical, p.CallID, p.RawArgs)
	} else {
		output.Text, err = host.UnknownTool(canonical, p.RawArgs)
		output.Kind = assistant.InvocationNocxMessage
	}
	if err != nil {
		if suspension(err) {
			o.mu.Lock()
			copy := e
			o.pending = &copy
			o.mu.Unlock()
			return err
		}
		_ = o.process.send("error", e.RequestID, errorPayload{Code: "host"})
		return err
	}
	if output.Kind == assistant.InvocationToolOutput {
		output.Text = host.FrameForModel(canonical, output.Text)
	}
	// Only the Go-owned name field is translated. Opaque result text and
	// user-derived refusal details remain byte-for-byte host output.
	o.mu.Lock()
	o.activeInvoke = ""
	o.mu.Unlock()
	if err = o.process.send("tool.result", e.RequestID, toolResult{output.Text, string(output.Kind)}); err != nil {
		return err
	}
	o.nextCall++
	if o.nextCall == len(o.batch) {
		o.batch = nil
		o.nextCall = 0
	}
	return nil
}

func (o *operation) accept(e envelope) error {
	switch e.Kind {
	case "model.open":
		return o.openHTTP(e)
	case "model.chunk":
		return o.ackHTTP(e)
	case "model.close":
		return o.closeHTTP(e)
	case "generated", "error":
		if e.RequestID == nil {
			return errProtocol
		}
		o.mu.Lock()
		ch := o.generations[*e.RequestID]
		o.mu.Unlock()
		if ch != nil {
			result := generationResult{}
			if e.Kind == "error" {
				result.err = o.runtimeError(e)
				if errors.Is(result.err, errProtocol) {
					return result.err
				}
			} else {
				var p textPayload
				if err := decodeStrict(e.Payload, &p); err != nil {
					return err
				}
				result.text = p.Text
			}
			o.mu.Lock()
			delete(o.bindings, *e.RequestID)
			delete(o.generations, *e.RequestID)
			delete(o.httpFailures, *e.RequestID)
			o.mu.Unlock()
			ch <- result
			return nil
		}
		if e.Kind == "generated" {
			return errProtocol
		}
	}
	o.mu.Lock()
	if o.pending != nil || o.activeInvoke != "" {
		o.mu.Unlock()
		return errProtocol
	}
	if e.Kind == "tool.invoke" {
		o.activeInvoke = *e.RequestID
	}
	o.mu.Unlock()
	// A committed proposal object may occupy the 4 MiB object allowance.
	// Prose uses the smaller stream window; its producer splits UTF-8 deltas.
	if e.Kind == "event" && len(e.Payload) > streamWindow {
		return errProtocol
	}
	cost := min(len(e.Payload), streamWindow)
	for {
		o.mu.Lock()
		fits := o.queuedBytes+cost <= streamWindow
		if fits {
			o.queuedBytes += cost
		}
		o.mu.Unlock()
		if fits {
			select {
			case o.actions <- queuedFrame{e, cost}:
				return nil
			case <-o.ctx.Done():
				return o.ctx.Err()
			}
		}
		select {
		case <-o.queueSpace:
		case <-o.ctx.Done():
			return o.ctx.Err()
		}
	}
}

func (o *operation) runtimeError(e envelope) error {
	var p errorPayload
	if err := decodeStrict(e.Payload, &p); err != nil {
		return err
	}
	switch p.Code {
	case "provider", "empty_answer", "cancelled", "host":
	case "protocol":
		return errProtocol
	default:
		return errProtocol
	}
	if p.Status != nil && (p.Code != "provider" || *p.Status < 100 || *p.Status > 599) {
		return errProtocol
	}
	o.mu.Lock()
	binding := o.bindings[*e.RequestID]
	failure := o.httpFailures[*e.RequestID]
	o.mu.Unlock()
	if failure == nil && p.Status != nil {
		failure = &assistant.EndpointHTTPError{StatusCode: *p.Status}
	}
	if failure != nil {
		if sentence, ok := assistant.EndpointErrorSentence(failure, binding.target.Model); ok {
			return &assistant.StreamError{Message: sentence}
		}
	}
	switch p.Code {
	case "empty_answer":
		return &assistant.StreamError{Message: "the model returned no text"}
	case "cancelled":
		return context.Canceled
	case "provider", "host":
		return &assistant.StreamError{Message: assistant.UnexplainedFailureSentence}
	case "protocol":
		return errProtocol
	default:
		return errProtocol
	}
}

func (c *Client) Generate(ctx context.Context, target assistant.ModelTarget, messages []assistant.Message) (string, error) {
	if strings.TrimSpace(target.BaseURL) == "" || strings.TrimSpace(target.Model) == "" {
		return "", errors.New("generate: base URL and model are required")
	}
	o, _ := ctx.Value(operationKey{}).(*operation)
	if o == nil || o.client != c {
		o = c.newOperation(ctx, nil)
		defer o.shutdown()
	}
	if err := o.launch(ctx); err != nil {
		return "", err
	}
	if err := o.ctx.Err(); err != nil {
		return "", err
	}
	id := o.process.reserve("generated")
	result := make(chan generationResult, 1)
	o.mu.Lock()
	o.generations[id] = result
	o.bindings[id] = modelBinding{target, catalogPayload{Tools: []toolDefinition{}, VisibleTools: []string{}}, ctx}
	o.mu.Unlock()
	input := make([]message, 0, len(messages))
	for _, m := range messages {
		input = append(input, message{m.Role, m.Content})
	}
	if err := o.process.send("generate", &id, generatePayload{target.Model, input}); err != nil {
		return "", err
	}
	select {
	case r := <-result:
		return r.text, r.err
	case <-ctx.Done():
		_ = o.process.send("cancel", nil, cancelPayload{&id})
		return "", ctx.Err()
	case <-o.ctx.Done():
		return "", o.ctx.Err()
	}
}

func (c *Client) Probe(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
	if strings.TrimSpace(p.BaseURL) == "" {
		return assistant.ProbeResult{}, errors.New("probe: base URL is required")
	}
	if strings.TrimSpace(p.Model) == "" {
		return assistant.CheckConnection(ctx, c.httpFactory, p)
	}
	start := time.Now()
	bounded, cancel := context.WithTimeout(ctx, assistant.ProbeTimeout)
	defer cancel()
	err := c.Ask(bounded, assistant.AskParams{Key: p.Key, BaseURL: p.BaseURL, Model: p.Model, Headers: p.Headers, Messages: []assistant.Message{{Role: "user", Content: "Reply with the single word: ok"}}}, func(assistant.AskEvent) error { return nil })
	res := assistant.ProbeResult{EndpointName: p.Name, Model: p.Model, Kind: assistant.ProbeModel, OK: err == nil, ElapsedMS: time.Since(start).Milliseconds(), At: time.Now()}
	if err != nil {
		var failure *assistant.StreamError
		if errors.As(err, &failure) {
			res.Error = failure.Message
		} else {
			res.Error = assistant.UnexplainedFailureSentence
		}
	}
	return res, nil
}

func (c *Client) AuditSkill(ctx context.Context, p assistant.SkillAuditParams) (assistant.SkillAuditResult, error) {
	o := c.newOperation(ctx, nil)
	defer o.shutdown()
	return assistant.AuditSkill(context.WithValue(ctx, operationKey{}, o), c, p)
}

func (o *operation) sendFailure(kind string, id *string, payload any) {
	if err := o.process.send(kind, id, payload); err != nil {
		o.fail(fmt.Errorf("pydantic: send: %w", err))
	}
}
