package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

// attemptRecordingDispatcher is the external caller's ledger adapter. It
// records the action and opens its execution before entering the shared
// dispatcher, then closes that execution for every dispatch outcome. The
// invariant is: from before the dispatcher is entered until its outcome is
// recorded, exactly one attempt exists for each admitted call.
//
// The endpoint outcomes are deliberately asymmetric at the admission boundary:
// admitted and dispatched (success or failure) leave one attempt; admitted and
// refused by the dispatcher leave one attempt; admission refusal never creates
// an invocation and leaves none; an admitted unknown method leaves one attempt
// with its synthetic declaration before the dispatcher returns its refusal.
type attemptRecordingDispatcher struct {
	registry  agenttools.Registry
	inner     ToolDispatcher
	catalogue ToolCatalogue
	ledger    AttemptLedger
}

// NewAttemptRecordingDispatcher wraps the caller-neutral dispatcher with the
// same durable attempt record used by the in-process assistant. The ledger is
// a required composition seam: without it an external call could mutate the
// worker record while leaving no auditable refusal or outcome.
func NewAttemptRecordingDispatcher(registry agenttools.Registry, inner ToolDispatcher, ledger AttemptLedger) (ToolDispatcher, error) {
	if inner == nil || (reflect.ValueOf(inner).Kind() == reflect.Pointer && reflect.ValueOf(inner).IsNil()) {
		return nil, errors.New("assistant: no dispatcher to audit")
	}
	if ledger == nil || (reflect.ValueOf(ledger).Kind() == reflect.Pointer && reflect.ValueOf(ledger).IsNil()) {
		return nil, errors.New("assistant: no attempt ledger wired")
	}
	catalogue, ok := inner.(ToolCatalogue)
	if !ok {
		return nil, errors.New("assistant: audited dispatcher cannot enumerate its catalogue")
	}
	return &attemptRecordingDispatcher{registry: registry, inner: inner, catalogue: catalogue, ledger: ledger}, nil
}

func (d *attemptRecordingDispatcher) Dispatch(invocation ToolInvocation) (string, error) {
	decl := agenttools.Tool{Declaration: agenttools.Declaration{Name: invocation.Method}}
	if found, ok := d.registry.Lookup(invocation.Method); ok {
		decl = found
	}
	resources := attemptResources(decl, invocation)
	execID, _, err := recordAttempt(contextOrBackground(invocation.Context), d.ledger, invocation.Grant, decl, invocation.RunContext.RunID, string(invocation.RawParams), resources)
	if err != nil {
		return "", fmt.Errorf("assistant dispatch: record attempt: %w", err)
	}

	result, dispatchErr := d.inner.Dispatch(invocation)
	if dispatchErr != nil {
		// The outcome is already a refusal/failure owned by the inner
		// dispatcher. Preserve its typed error for the JSON-RPC mapper; a
		// close failure on this path must not hide the original answer.
		_ = finishAttempt(contextOrBackground(invocation.Context), d.ledger, execID, dispatchErr)
		return "", dispatchErr
	}
	if err := finishAttempt(contextOrBackground(invocation.Context), d.ledger, execID, nil); err != nil {
		return "", fmt.Errorf("assistant dispatch: record outcome: %w", err)
	}
	return result, nil
}

func (d *attemptRecordingDispatcher) Catalogue(grant content.Grant) []agenttools.Tool {
	return d.catalogue.Catalogue(grant)
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func attemptResources(decl agenttools.Tool, invocation ToolInvocation) []agenttools.ResourceRef {
	if decl.ResolveResources == nil {
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal(invocation.RawParams, &args); err != nil {
		return nil
	}
	resources, _, err := resolveToolResources(decl, args, invocation.RunContext)
	if err != nil {
		return nil
	}
	return resources
}

// recordAttempt writes the action entry and starts its first execution. It is
// shared with the in-process kernel so the two callers cannot drift in their
// environment, payload, grant, or execution metadata.
func recordAttempt(ctx context.Context, ledger AttemptLedger, grant content.Grant, decl agenttools.Tool, runID, rawArgs string, resources []agenttools.ResourceRef) (int64, string, error) {
	if err := prepareAttemptEnvironment(ctx, ledger); err != nil {
		return 0, "", err
	}
	entryID, err := submitAttemptEntry(ctx, ledger, grant, decl, selectedAttemptEffect(decl), runID, rawArgs, resources, nil)
	if err != nil {
		return 0, "", err
	}
	execID, err := startAttemptExecution(ctx, ledger, grant, entryID, 1)
	if err != nil {
		return 0, "", err
	}
	return execID, entryID, nil
}

func prepareAttemptEnvironment(ctx context.Context, ledger AttemptLedger) error {
	envID := content.EnvironmentIDFor(content.EnvLocal, "")
	if err := ledger.EnsureEnvironment(ctx, content.Environment{ID: envID, Kind: content.EnvLocal}); err != nil {
		return fmt.Errorf("environment: %w", err)
	}
	if _, err := ledger.RecordObservation(ctx, content.Observation{
		EnvironmentID: envID,
		Criticality:   content.CriticalityRoutine,
	}); err != nil {
		return fmt.Errorf("observation: %w", err)
	}
	return nil
}

func submitAttemptEntry(ctx context.Context, ledger AttemptLedger, grant content.Grant, decl agenttools.Tool, effect content.Effect, runID, rawArgs string, resources []agenttools.ResourceRef, fact *classifierFact) (string, error) {
	envID := content.EnvironmentIDFor(content.EnvLocal, "")
	payloadBody := map[string]any{
		"tool":   decl.Name,
		"effect": effect,
		"args":   attemptArgs(rawArgs),
	}
	if runID != "" {
		payloadBody["runId"] = runID
	}
	if len(resources) > 0 {
		payloadBody["resources"] = resources
		payloadBody["resource"] = matchedResource(resources)
	}
	if decl.OpensBlock {
		payloadBody["opensBlock"] = true
	}
	if fact != nil {
		payloadBody["classifier"] = fact
	}
	payload, err := json.Marshal(payloadBody)
	if err != nil {
		return "", fmt.Errorf("payload: %w", err)
	}
	res, err := ledger.Submit(ctx, content.SubmitEntry{
		ID:            uuid.NewString(),
		Client:        "agent",
		EnvironmentID: envID,
		Cwd:           "/",
		Kind:          content.EntryAction,
		Source:        content.SourceAssistant,
		Intent:        decl.Name,
		Payload:       string(payload),
	})
	if err != nil {
		return "", fmt.Errorf("submit: %w", err)
	}
	return res.ID, nil
}

func attemptArgs(rawArgs string) any {
	if json.Valid([]byte(rawArgs)) {
		return json.RawMessage(rawArgs)
	}
	return rawArgs
}

// selectedAttemptEffect uses the assembled tool's precomputed WorstEffect;
// only a lookup-miss synthetic declaration needs a declared-effect fallback.
func selectedAttemptEffect(decl agenttools.Tool) content.Effect {
	if len(decl.Declaration.Effect) == 0 {
		return ""
	}
	if decl.Effect != "" {
		return decl.Effect
	}
	return decl.Declaration.Effect[0]
}

func startAttemptExecution(ctx context.Context, ledger AttemptLedger, grant content.Grant, entryID string, attempt int) (int64, error) {
	executor := "agent"
	execID, err := ledger.StartExecution(ctx, content.StartExecution{
		EntryID:  entryID,
		Attempt:  attempt,
		Executor: &executor,
		Grant:    &grant,
	})
	if err != nil {
		return 0, fmt.Errorf("start execution: %w", err)
	}
	return execID, nil
}

func finishAttempt(ctx context.Context, ledger AttemptLedger, execID int64, dispatchErr error) error {
	end := content.FinishExecution{
		EndedAt: time.Now().UnixMilli(),
	}
	if dispatchErr == nil {
		end.TerminationReason = content.TermCompleted
		end.Status = content.EntrySuccess
	} else {
		end.TerminationReason = terminationReasonOf(dispatchErr)
		end.Status = content.EntryFailure
	}
	return ledger.FinishExecution(ctx, execID, end)
}

var (
	_ ToolDispatcher = (*attemptRecordingDispatcher)(nil)
	_ ToolCatalogue  = (*attemptRecordingDispatcher)(nil)
)
