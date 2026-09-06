package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

// ToolInvocation is the caller-neutral input to the worker dispatcher. The
// session and grant are already bound by the caller adapter; method params
// cannot add authority to either one.
type ToolInvocation struct {
	Context    context.Context
	Method     string
	RunContext agenttools.RunContext
	Grant      content.Grant
	RawParams  json.RawMessage
}

// ToolDispatcher is the common operation used by the in-process assistant and
// the future external endpoint. Both callers enter the same declaration,
// validation, resource, capability, executor and result-contract pipeline.
type ToolDispatcher interface {
	Dispatch(ToolInvocation) (string, error)
}

// ToolCatalogue projects the executable declarations admitted by a grant.
// Callers use it to offer only methods the same dispatcher can reach.
type ToolCatalogue interface {
	Catalogue(content.Grant) []agenttools.Tool
}

var (
	// ErrUnknownMethod means no assembled declaration has this method name.
	ErrUnknownMethod = errors.New("assistant dispatch: unknown method")
	// ErrInvalidParams means raw params failed the declaration's own schema.
	ErrInvalidParams = errors.New("assistant dispatch: invalid params")
	// ErrUnreachableMethod means the grant projection does not expose the
	// declaration, including a missing resource kind or effect.
	ErrUnreachableMethod = errors.New("assistant dispatch: method unreachable for grant")
	// ErrInvalidResult means the executor returned a value outside its declared
	// result contract.
	ErrInvalidResult = errors.New("assistant dispatch: invalid result")
)

// DispatchRefusalError preserves the method and refusal answer when a model
// adapter aborts before execution. Callers can classify it without parsing the
// rendered reason text.
type DispatchRefusalError struct {
	Method string
	Reason string
}

func (e *DispatchRefusalError) Error() string {
	return fmt.Sprintf("assistant dispatch: %s: %s", e.Method, e.Reason)
}

func dispatchAbortedError(invocation ToolInvocation, result *modelResult) error {
	reason := ""
	if result != nil {
		reason = result.text
	}
	return &DispatchRefusalError{Method: invocation.Method, Reason: reason}
}

type (
	dispatchExecutor  func(context.Context, agenttools.Tool, agenttools.Capability, []byte) (string, error)
	dispatchTransform func(*preparedInvocation) error
	dispatchGate      func(*preparedInvocation) (bool, *modelResult, error)
)

type preparedInvocation struct {
	request             ToolInvocation
	decl                agenttools.Tool
	args                map[string]any
	invocation          content.Invocation
	resources           []agenttools.ResourceRef
	resourceDeclaration bool
}

type dispatchOutcome struct {
	prepared *preparedInvocation
	output   string
	runErr   error
	aborted  *modelResult
}

type dispatchOperation struct {
	registry   agenttools.Registry
	validators map[string]*jsonschema.Schema
	results    map[string]*jsonschema.Schema
	executor   dispatchExecutor
	allowed    map[string]struct{}
}

func compileDispatchSchemas(registry agenttools.Registry) (map[string]*jsonschema.Schema, map[string]*jsonschema.Schema, error) {
	validators := make(map[string]*jsonschema.Schema, len(registry.All()))
	results := make(map[string]*jsonschema.Schema, len(registry.All()))
	for _, tool := range registry.All() {
		params, err := compileToolSchema(tool)
		if err != nil {
			return nil, nil, err
		}
		validators[tool.Name] = params
		if len(tool.ResultSchema) == 0 {
			continue
		}
		result, err := compileResultSchema(tool)
		if err != nil {
			return nil, nil, err
		}
		results[tool.Name] = result
	}
	return validators, results, nil
}

// NewToolDispatcher builds the worker-only adapter over the common operation.
// Its public surface accepts only the assembled worker declarations; the
// record and environment are infrastructure seams, never caller parameters.
func NewToolDispatcher(registry agenttools.Registry, workerStore WorkerRecord, environment string) (ToolDispatcher, error) {
	validators, results, err := compileDispatchSchemas(registry)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{}, len(workerMethodNames))
	for _, name := range workerMethodNames {
		allowed[name] = struct{}{}
	}
	return &dispatchOperation{
		registry:   registry,
		validators: validators,
		results:    results,
		allowed:    allowed,
		executor: func(ctx context.Context, decl agenttools.Tool, cap agenttools.Capability, raw []byte) (string, error) {
			return runDeclaredTool(ctx, decl, cap, raw, toolSeams{
				workerStore:       workerStore,
				workerEnvironment: environment,
			})
		},
	}, nil
}

// workerMethodNames describes the current worker surface, not a general authority
// allowlist. Applying the reachability gate to every tool was measured to make
// 11 existing tests fail: git.status and skill mutations deliberately record
// the execution attempt before capability refusal so the refusal stays
// auditable, while worker methods have no such requirement today. The general
// case remains undecided; this list must not become a second effect/name policy.
var workerMethodNames = [...]string{
	"workers.spawn",
	"workers.say",
	"workers.wait",
	"workers.holdings",
	"workers.close",
	// The participant's one call (nocx-rowqt.9). It belongs on this list for
	// the same reason the other five do — it is part of the worker surface the
	// endpoint exposes — and NOT because it shares their authority: it
	// narrows to the other capability entirely, and no grant that reaches
	// those five reaches this one.
	"workers.inbox",
}

// isWorkerMethod selects the current worker surface for the reachability behavior;
// it does not decide authority, which remains declaration- and grant-owned.
func isWorkerMethod(name string) bool {
	for _, workerName := range workerMethodNames {
		if name == workerName {
			return true
		}
	}
	return false
}

func (d *dispatchOperation) Dispatch(invocation ToolInvocation) (string, error) {
	outcome, err := d.dispatch(invocation, nil, nil, nil, nil)
	if err != nil {
		return "", err
	}
	if outcome.aborted != nil {
		return "", dispatchAbortedError(invocation, outcome.aborted)
	}
	if outcome.runErr != nil {
		return "", outcome.runErr
	}
	return outcome.output, nil
}

// Catalogue returns the executable tools admitted by grant. It is kept beside
// Dispatch so an external caller can ask the same registry projection that
// the dispatcher enforces, without sending authority in request params.
func (d *dispatchOperation) Catalogue(grant content.Grant) []agenttools.Tool {
	return d.registry.ForGrant(grant)
}

func (d *dispatchOperation) dispatch(invocation ToolInvocation, transform dispatchTransform, gate dispatchGate, beforeExecute func(*preparedInvocation, agenttools.Capability) error, executor dispatchExecutor) (dispatchOutcome, error) {
	prepared, err := d.prepare(invocation, transform)
	if err != nil {
		return dispatchOutcome{}, err
	}
	if gate != nil {
		continueExecution, result, gateErr := gate(&prepared)
		if gateErr != nil {
			return dispatchOutcome{}, gateErr
		}
		if !continueExecution {
			return dispatchOutcome{prepared: &prepared, aborted: result}, nil
		}
	}
	if prepared.decl.Narrow == nil {
		return dispatchOutcome{prepared: &prepared}, fmt.Errorf("%w: %q has no capability constructor", ErrUnreachableMethod, prepared.decl.Name)
	}
	capability, err := prepared.decl.Narrow(prepared.request.Grant, prepared.resources, prepared.request.RunContext)
	if err != nil {
		return dispatchOutcome{prepared: &prepared}, fmt.Errorf("tool %q: construct capability: %w", prepared.decl.Name, err)
	}
	if beforeExecute != nil {
		if err := beforeExecute(&prepared, capability); err != nil {
			return dispatchOutcome{prepared: &prepared}, err
		}
	}
	if executor == nil {
		executor = d.executor
	}
	if executor == nil {
		return dispatchOutcome{prepared: &prepared}, errors.New("assistant dispatch: no executor wired")
	}
	output, runErr := executor(prepared.request.Context, prepared.decl, capability, prepared.request.RawParams)
	if runErr == nil {
		if err := validateToolResult(d.results, prepared.decl.Name, output); err != nil {
			return dispatchOutcome{prepared: &prepared}, err
		}
	}
	return dispatchOutcome{prepared: &prepared, output: output, runErr: runErr}, nil
}

func (d *dispatchOperation) prepare(invocation ToolInvocation, transform dispatchTransform) (preparedInvocation, error) {
	decl, ok := d.registry.Lookup(invocation.Method)
	if !ok {
		return preparedInvocation{}, fmt.Errorf("%w: %q", ErrUnknownMethod, invocation.Method)
	}
	if d.allowed != nil {
		if _, ok := d.allowed[decl.Name]; !ok {
			return preparedInvocation{}, fmt.Errorf("%w: %q", ErrUnreachableMethod, decl.Name)
		}
	}
	if len(invocation.RawParams) > maxArgsBytes {
		return preparedInvocation{}, fmt.Errorf("%w: tool %q arguments exceed the %d-byte bound", ErrInvalidParams, decl.Name, maxArgsBytes)
	}
	args, err := validateToolArgs(d.validators, decl, string(invocation.RawParams))
	if err != nil {
		return preparedInvocation{}, fmt.Errorf("%w: tool %q: %v", ErrInvalidParams, decl.Name, err)
	}
	prepared := preparedInvocation{request: invocation, decl: decl, args: args}
	if transform != nil {
		if transformErr := transform(&prepared); transformErr != nil {
			return preparedInvocation{}, transformErr
		}
	}
	resources, resourceDeclaration, err := resolveToolResources(prepared.decl, prepared.args, invocation.RunContext)
	if err != nil {
		return preparedInvocation{}, fmt.Errorf("%w: tool %q: resolve resources: %v", ErrInvalidParams, prepared.decl.Name, err)
	}
	prepared.resources = resources
	prepared.resourceDeclaration = resourceDeclaration
	// The external worker adapter and in-process worker calls require the
	// declaration projection here. Non-worker model calls intentionally keep
	// their historical attempt-before-capability behavior (notably
	// git.status and skill mutations), so this shared operation only applies
	// the reachability gate to the worker surface in this refactor.
	if isWorkerMethod(prepared.decl.Name) && !methodReachable(d.registry, invocation.Grant, prepared.decl.Name) {
		return preparedInvocation{}, fmt.Errorf("%w: %q", ErrUnreachableMethod, prepared.decl.Name)
	}
	return prepared, nil
}

func methodReachable(registry agenttools.Registry, grant content.Grant, name string) bool {
	for _, tool := range registry.ForGrant(grant) {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func validateToolArgs(validators map[string]*jsonschema.Schema, decl agenttools.Tool, raw string) (map[string]any, error) {
	validator := validators[decl.Name]
	if validator == nil {
		return nil, errors.New("no validator compiled for this tool")
	}
	var document any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("arguments are not JSON: %w", err)
	}
	if err := validator.Validate(document); err != nil {
		return nil, fmt.Errorf("arguments do not match the schema the model was shown: %w", err)
	}
	object, ok := document.(map[string]any)
	if !ok {
		return nil, errors.New("arguments are not an object")
	}
	return object, nil
}

func validateToolResult(results map[string]*jsonschema.Schema, tool, output string) error {
	validator := results[tool]
	if validator == nil {
		return nil
	}
	var document any
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("%w: agent tool %q returned something that is not JSON: %v", ErrInvalidResult, tool, err)
	}
	if err := validator.Validate(document); err != nil {
		return fmt.Errorf("%w: agent tool %q returned a result its own contract does not allow: %v", ErrInvalidResult, tool, err)
	}
	return nil
}

func resolveToolResources(decl agenttools.Tool, args map[string]any, runContext agenttools.RunContext) ([]agenttools.ResourceRef, bool, error) {
	if decl.ResolveResources == nil {
		return nil, false, nil
	}
	resources, err := decl.ResolveResources(args, runContext)
	if err != nil {
		return nil, true, err
	}
	for _, resource := range resources {
		if resource.Kind == "" || resource.ID == "" {
			return nil, true, errors.New("resource resolver returned an incomplete resource")
		}
		declared := false
		for _, kind := range decl.ResourceKinds {
			if resource.Kind == kind {
				declared = true
				break
			}
		}
		if !declared {
			return nil, true, fmt.Errorf("resource resolver returned undeclared kind %q", resource.Kind)
		}
	}
	return resources, true, nil
}

func runDeclaredTool(ctx context.Context, decl agenttools.Tool, capability agenttools.Capability, raw []byte, seams toolSeams) (string, error) {
	runContext := ctx
	cancel := func() {}
	if decl.Deadline > 0 {
		runContext, cancel = context.WithTimeout(ctx, decl.Deadline)
	}
	defer cancel()
	runContext = withToolBound(runContext, decl.ResultBound)
	executor, ok := executors[decl.Name]
	if !ok {
		return "", fmt.Errorf("tool %q has a capability constructor but no executor", decl.Name)
	}
	return executor(runContext, capability, raw, seams)
}

var _ ToolDispatcher = (*dispatchOperation)(nil)
