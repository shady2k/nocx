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
	// invocationEffects is the effect set THIS call reaches, as the
	// declaration's InvocationRelation derived it from the validated
	// arguments — nil when the declaration derives nothing and the
	// declared set stands. The relation half rides on decl.EffectRelation,
	// already a per-call field; this carries the set half, so the endpoint
	// gate reads one derivation instead of deriving again.
	invocationEffects []content.Effect
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
	allowed := make(map[string]struct{}, len(orchestrationMethodNames))
	for _, name := range orchestrationMethodNames {
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

// orchestrationMethodNames describes the current worker/orchestration
// surface, not a general authority allowlist. Applying the reachability gate
// to every tool was measured to make 11 existing tests fail: git.status and
// skill mutations deliberately record the execution attempt before
// capability refusal so the refusal stays auditable, while these methods
// have no such requirement today. The general case remains undecided; this
// list must not become a second effect/name policy.
//
// Renamed from workerMethodNames (nocx-6q1uh.8, design §4.1): session.read
// is not a worker's own call, but the tool endpoint's dispatcher (built by
// NewToolDispatcher below) is the one place that needs an allowlist at all,
// and it is the SAME allowlist both the coordinator's and a worker's calls
// go through — "worker" undersold what it was already gating. session.keys
// and session.message are listed here too, ahead of their own tasks (9,
// 10): this is the one place their names belong, and the registry simply
// has no row for them yet, exactly as it had none for session.read before
// this task — a name here with no matching declaration is inert, never
// reachable through Lookup.
var orchestrationMethodNames = [...]string{
	"workers.spawn",
	"workers.say",
	"workers.holdings",
	"workers.close",
	// The coordinator's explicit removal of a checkout a closed worker left
	// (nocx-xn63t.1.5). It is on this list for the same reason the four above
	// are — it is part of the worker surface the endpoint exposes — and the
	// list is what makes the declaration reachable at all: without the name
	// here the tool is declared, offered by the catalogue and refused by
	// dispatch, which is exactly the gap session.read's comment below records.
	"workers.removeCheckout",
	// The participant's one call (nocx-rowqt.9). It belongs on this list for
	// the same reason the other four do — it is part of the worker surface the
	// endpoint exposes — and NOT because it shares their authority: it
	// narrows to the other capability entirely, and no grant that reaches
	// those four reaches this one.
	"workers.inbox",
	// The worker's OWN report (nocx-luqz9.4). It is here for workers.inbox's
	// reason and it is the one name on this list whose holder must be a
	// PARTICIPANT: its narrow refuses every run that is not one, so the
	// endpoint's dispatcher has to accept the name for the refusal to arrive —
	// a name absent from this list answers ErrUnreachableMethod before any
	// constructor runs, which would tell an ordinary agent its tool is not
	// offered rather than telling a coordinator to read its mailbox instead.
	"workers.report",
	// The session surface (nocx-6q1uh, design §4.1): a descendant's pane,
	// read, written to with one step under a target, or sent a message.
	// Both callers reach these through DescendantPaneAccess; the endpoint's
	// catalogue already offered session.read via ForGrant before this
	// allowlist did (nocx-6q1uh.8's own finding — catalogue offers what
	// dispatch refuses), which is the gap this addition closes.
	"session.read",
	"session.keys",
	"session.message",
}

// isOrchestrationMethod selects the current worker/orchestration surface for
// the reachability behavior; it does not decide authority, which remains
// declaration- and grant-owned.
func isOrchestrationMethod(name string) bool {
	for _, orchestrationName := range orchestrationMethodNames {
		if name == orchestrationName {
			return true
		}
	}
	return false
}

func (d *dispatchOperation) Dispatch(invocation ToolInvocation) (string, error) {
	outcome, err := d.dispatch(invocation, nil, d.endpointEffectGate, nil, nil)
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

// endpointEffectGate is the tool endpoint's half of the per-call effect
// question. The kernel's own dispatcher answers it through the verdict
// machinery, where an ask can become a question to a person; this dispatcher
// serves the product's minted coordinators, whose grants decide permit or
// refuse and carry no ask machinery at all — so a row the grant refuses
// refuses the call here, before any capability is constructed and before any
// side effect exists. The relation's own question applies (ADR-0053 as
// amended): one refusal refuses a conjunction whole, because there is nothing
// to move to execution, while an alternative set survives on any row that is
// not refused — its offer-time answer carried to execution. The declared set
// stands when the declaration derives no per-call shape.
func (d *dispatchOperation) endpointEffectGate(prepared *preparedInvocation) (bool, *modelResult, error) {
	effects := prepared.invocationEffects
	if effects == nil {
		effects = prepared.decl.Declaration.Effect
	}
	if len(effects) == 0 {
		return true, nil, nil
	}
	refused := make([]content.Effect, 0, len(effects))
	for _, effect := range effects {
		if prepared.request.Grant.Policy.DecisionFor(effect) == content.DecisionRefuse {
			refused = append(refused, effect)
		}
	}
	if len(refused) == 0 {
		return true, nil, nil
	}
	if prepared.decl.EffectRelation != agenttools.EffectsConjunctive && len(refused) < len(effects) {
		return true, nil, nil
	}
	return false, &modelResult{
		text: fmt.Sprintf("%s reaches %s on this call, and this run's grant refuses %s — the call is refused before anything runs.",
			prepared.decl.Name, effectNames(effects), effectNames(refused)),
	}, nil
}

// effectNames renders an effect set for a refusal sentence: the row ids are
// the words the policy's own surface uses ("delegate", "mutate-reversible").
func effectNames(effects []content.Effect) string {
	parts := make([]string, len(effects))
	for i, effect := range effects {
		parts[i] = string(effect)
	}
	return strings.Join(parts, ", ")
}

// Catalogue returns the executable tools admitted by grant, filtered to the
// ones THIS dispatcher would actually accept — the same two admission rules
// prepare applies below, before it ever asks a grant anything (nocx-6q1uh.16).
// registry.ForGrant alone answers "does the registry projection cover this
// resource and effect"; it says nothing about a dispatcher's own name
// allowlist. That gap is exactly what let the tool endpoint's dispatcher
// (NewToolDispatcher, allowed=orchestrationMethodNames) hand an external
// coordinator session.list, session.run and session.wait through
// tools.catalogue while Dispatch refused every one of them: those three are
// not on that allowlist, and effectsPermitted's alternative relation only
// needs ONE of session.run's declared effects permitted for ForGrant to
// offer it, which a caller grant permitting Observe/MutateDestructive/
// Delegate satisfies. acceptsMethod is prepare's own predicate, reused
// rather than re-derived, so a name this filter admits is never one Dispatch
// then refuses on name or reachability grounds alone — one function, two
// consumers, rather than a second list of what a caller may call (AGENTS.md,
// "look for the existing answer before you write a second one").
func (d *dispatchOperation) Catalogue(grant content.Grant) []agenttools.Tool {
	projected := d.registry.ForGrant(grant)
	out := make([]agenttools.Tool, 0, len(projected))
	for _, tool := range projected {
		if d.acceptsMethod(tool.Name, grant) {
			out = append(out, tool)
		}
	}
	return out
}

// methodAllowed reports whether name passes this dispatcher's own name
// allowlist — prepare's first admission rule. nil for the kernel's own
// dispatcher (every declared name is a candidate); set to
// orchestrationMethodNames for the tool endpoint's (NewToolDispatcher).
func (d *dispatchOperation) methodAllowed(name string) bool {
	if d.allowed == nil {
		return true
	}
	_, ok := d.allowed[name]
	return ok
}

// reachableForGrant reports whether the registry's grant projection admits
// name for grant — prepare's second admission rule, applied only to the
// orchestration surface (isOrchestrationMethod) for the reason prepare's own
// comment gives: every other name keeps its historical attempt-before-
// capability behavior, refused later by Narrow rather than here.
func (d *dispatchOperation) reachableForGrant(name string, grant content.Grant) bool {
	if !isOrchestrationMethod(name) {
		return true
	}
	return methodReachable(d.registry, grant, name)
}

// acceptsMethod combines both of prepare's name-level admission rules into
// the one predicate Catalogue filters ForGrant's output through. It says
// nothing about a specific call's parsed arguments or resolved resources —
// that authority stays per-call, decided inside prepare and Narrow, never
// projected here.
func (d *dispatchOperation) acceptsMethod(name string, grant content.Grant) bool {
	return d.methodAllowed(name) && d.reachableForGrant(name, grant)
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
	if !d.methodAllowed(decl.Name) {
		return preparedInvocation{}, fmt.Errorf("%w: %q", ErrUnreachableMethod, decl.Name)
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
	// The per-call shape (ADR-0053 as amended): arguments may raise a call
	// past the declared reading — workers.spawn's worktree ask reaches
	// delegate AND mutate-reversible where a plain spawn is delegation
	// alone. Derived once, here, because two consumers read it: the
	// kernel's verdict machinery, which the relation arms to take the
	// conjunctive strictest-across-rows branch, and the tool endpoint's
	// refusal gate below, which has no ask machinery and refuses what the
	// grant refuses before any capability is constructed.
	if prepared.decl.InvocationRelation != nil {
		if relation, effects, ok := prepared.decl.InvocationRelation(prepared.args); ok {
			prepared.decl.EffectRelation = relation
			prepared.invocationEffects = effects
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
	if !d.reachableForGrant(prepared.decl.Name, invocation.Grant) {
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
