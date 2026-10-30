package assistant

import (
	"context"
	"io/fs"

	tools "github.com/shady2k/nocx/contracts/tools"
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
)

// InvocationKind keeps host policy messages distinct from untrusted tool output.
type InvocationKind string

const (
	InvocationToolOutput  InvocationKind = "tool_output"
	InvocationNocxMessage InvocationKind = "nocx_message"
)

type InvocationResult struct {
	Text string
	Kind InvocationKind
}

// RunHost exposes the existing kernel without exporting its authority objects.
// InvokeClassified returns unframed text; only tool_output is framed for a model.
type RunHost interface {
	Tools() []agenttools.Tool
	ModelTools() []agenttools.Tool
	InvokeClassified(ctx context.Context, name, callID, rawArgs string) (InvocationResult, error)
	UnknownTool(name, rawArgs string) (string, error)
	FrameForModel(tool, result string) string
}

type RunHostFactory interface {
	NewRunHost(ctx context.Context, p AskParams, onCall func(ToolCall) error) (RunHost, error)
	Discard(runID string)
}

type runHostFactory struct {
	registry     agenttools.Registry
	floor        content.Floor
	generator    TextGenerator
	approvals    *ApprovalStore
	snapshots    *runSnapshots
	searchSchema []byte
}

// NewRunHostFactory owns the same process-lifetime approvals and snapshots
// previously held by the Eino client. Resume creates a fresh host over them.
func NewRunHostFactory(registry agenttools.Registry, floor content.Floor, generator TextGenerator) RunHostFactory {
	searchSchema, _ := fs.ReadFile(tools.Schemas, "search.schema.json")
	return newRunHostFactory(registry, floor, generator, searchSchema)
}

func newRunHostFactory(registry agenttools.Registry, floor content.Floor, generator TextGenerator, searchSchema []byte) *runHostFactory {
	return &runHostFactory{
		registry: registry, floor: floor, generator: generator,
		approvals: NewApprovalStore(), snapshots: newRunSnapshots(), searchSchema: searchSchema,
	}
}

func (f *runHostFactory) NewRunHost(ctx context.Context, p AskParams, onCall func(ToolCall) error) (RunHost, error) {
	if p.Grant == nil {
		return &runHost{}, nil
	}
	grant := *p.Grant
	grant.Policy = grant.Policy.WithFloor(f.floor)
	approvals := p.Approvals
	if approvals == nil {
		approvals = f.approvals
	}
	var classifier CallClassifier
	if p.Classifier != nil {
		classifier = newClassifierEngine(f.generator, p.Classifier)
	}
	kernel, err := newEffectKernel(log.From(ctx), grant, f.registry, p.AttemptLedger, approvals, p.KnownMaterial, p.RunID, p.SessionID, p.Attempt, p.TurnEntryID, p.Requester, Attachments{AutomaticItems: p.AutomaticSessionItems, MarkedWindows: p.MarkedSessionWindows}, classifier, onCall, toolSeams{
		modelToolName:     p.ModelToolName,
		sessions:          p.SessionSource,
		screenCapture:     p.ScreenCapture,
		noteOperation:     p.NoteOperation,
		snippetOperation:  p.SnippetOperation,
		skills:            p.Skills,
		skillDraft:        p.SkillDraft,
		generator:         f.generator,
		fetcher:           p.Fetcher,
		snapshots:         f.snapshots,
		runID:             p.RunID,
		expansions:        p.Expansions,
		workerStore:       p.Workers,
		workerEnvironment: p.WorkerEnvironment,
		scripts:           p.Scripts,
		cwd:               p.Cwd,
		paneAccessBinder:  p.PaneAccessBinder,
	})
	if err != nil {
		return nil, err
	}
	host := newRunHost(kernel)
	if p.Presentation != nil {
		host.presentation = *p.Presentation
		host.presentationState = newPresentationState(p.Presentation.Loaded)
		host.searchSchema = f.searchSchema
	}
	return host, nil
}

func (f *runHostFactory) Discard(runID string) { f.snapshots.Discard(runID) }

type runHost struct {
	kernel            *effectKernel
	grantProvider     func() content.Grant
	presentation      agenttools.PresentationConfig
	presentationState *presentationState
	searchSchema      []byte
}

func newRunHost(kernel *effectKernel) *runHost {
	return &runHost{
		kernel:            kernel,
		grantProvider:     func() content.Grant { return kernel.grant },
		presentationState: newPresentationState(nil),
	}
}

// Tools is the full executable catalog for a drive. Hidden eligible tools stay
// registered in the SDK; hiding their schemas must not make them unknown.
func (h *runHost) Tools() []agenttools.Tool {
	if h.kernel == nil {
		return nil
	}
	tools := h.kernel.registry.ForGrant(h.kernel.grant)
	if h.presentation.Lazy && len(h.searchSchema) > 0 {
		tools = append(tools, h.searchDeclaration())
	}
	return tools
}

// ModelTools is refreshed before every model request. Discovery updates this
// projection, not the SDK's executable catalog or the kernel's authority.
func (h *runHost) ModelTools() []agenttools.Tool {
	if h.kernel == nil {
		return nil
	}
	projection := h.presentationProjection()
	if projection.Lazy {
		return append(projection.Visible, h.searchDeclaration())
	}
	return projection.Visible
}

func (h *runHost) searchDeclaration() agenttools.Tool {
	return agenttools.Tool{
		Declaration:  agenttools.Declaration{Name: toolsSearchName, Description: toolsSearchDescription},
		ParamsSchema: h.searchSchema,
	}
}

func (h *runHost) InvokeClassified(ctx context.Context, name, callID, rawArgs string) (InvocationResult, error) {
	if err := ValidateToolCallID(callID); err != nil {
		return InvocationResult{}, err
	}
	if h.kernel == nil {
		text, err := h.UnknownTool(name, rawArgs)
		return InvocationResult{Text: text, Kind: InvocationNocxMessage}, err
	}
	if name == toolsSearchName && h.presentation.Lazy && len(h.searchSchema) > 0 {
		text, err := h.searchTools(ctx, rawArgs)
		return InvocationResult{Text: text, Kind: InvocationNocxMessage}, err
	}
	out, err := h.kernel.invokeClassified(ctx, name, callID, rawArgs)
	kind := InvocationToolOutput
	if out.kind == modelNocxMessage {
		kind = InvocationNocxMessage
	}
	return InvocationResult{Text: out.text, Kind: kind}, err
}

func (h *runHost) UnknownTool(name, _ string) (string, error) {
	if h.kernel == nil {
		return unknownToolResult(agenttools.Registry{}, content.Grant{}, name, nil), nil
	}
	return unknownToolResult(h.kernel.registry, h.kernel.grant, name, h.kernel.runSeams.modelToolName), nil
}

func (h *runHost) FrameForModel(tool, result string) string {
	if h.kernel == nil {
		return result
	}
	return h.kernel.FrameForModel(tool, result)
}
