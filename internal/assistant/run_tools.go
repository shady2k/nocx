package assistant

import (
	"errors"

	"github.com/google/uuid"
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
)

// TurnTools is the bound authority of one assistant turn, independent of its carrier.
type TurnTools interface {
	RunContext() agenttools.RunContext
	Admit() (ToolInvocation, error)
}

// RunTools exposes the existing assistant kernel to the tool endpoint.
// Its state, ordering, approvals and screens belong to one run.
type RunTools struct{ kernel *effectKernel }

// BindRunTools binds the same run inputs the in-process assistant uses.
// FreshGrant is required: authority is minted at admission, not at binding.
func BindRunTools(c Client, p AskParams, onCall func(ToolCall) error) (*RunTools, error) {
	owner, ok := c.(*client)
	if !ok {
		return nil, errors.New("assistant tools: client cannot bind run tools")
	}
	if p.FreshGrant == nil {
		return nil, errors.New("assistant tools: admission requires a grant mint")
	}
	k, err := owner.bindRunKernel(nil, p, onCall)
	if err != nil {
		return nil, err
	}
	return &RunTools{kernel: k}, nil
}

// RunContext returns the complete context bound once for the run.
func (r *RunTools) RunContext() agenttools.RunContext { return r.kernel.runCtx }

// Admit mints authority from the run's existing refresh owner, including its floor.
func (r *RunTools) Admit() (ToolInvocation, error) {
	grant, ok := r.kernel.mintGrantAfterAnswer()
	if !ok {
		return ToolInvocation{}, errors.New("assistant tools: admission grant unavailable")
	}
	return ToolInvocation{RunContext: r.RunContext(), Grant: grant, Dispatcher: r}, nil
}

// Dispatch uses the kernel's own per-turn order and complete authority chain.
func (r *RunTools) Dispatch(invocation ToolInvocation) (string, error) {
	invocation.RunContext = r.RunContext()
	out, err := r.kernel.invokeInvocation(invocation, uuid.NewString())
	if err == nil && out.kind == modelNocxMessage {
		return "", &DispatchRefusalError{Method: invocation.Method, Reason: out.text}
	}
	return out.text, err
}

// Catalogue projects the same declarations and grant as the run's dispatcher.
func (r *RunTools) Catalogue(grant content.Grant) []agenttools.Tool {
	return r.kernel.dispatcher.Catalogue(grant)
}

func (c *client) bindRunKernel(logger log.Logger, p AskParams, onCall func(ToolCall) error) (*effectKernel, error) {
	if p.Grant == nil {
		return nil, errors.New("assistant tools: run requires authority")
	}
	grant := *p.Grant
	grant.Policy = grant.Policy.WithFloor(c.floor)
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
	return newEffectKernel(logger, grant, c.tools, p.AttemptLedger, approvals, p.KnownMaterial, p.RunID, p.SessionID, p.Attempt, p.TurnEntryID, p.Requester, Attachments{AutomaticItems: p.AutomaticSessionItems, MarkedWindows: p.MarkedSessionWindows}, onCall, toolSeams{
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
}
