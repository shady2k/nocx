package capability

// The agent domain (nocx-f4s5): agent.captureFrame and agent.ask, the ask
// transaction over the ledger. One operation, one gate — the content domain
// (the ledger is content's schema v1; ADR-0019). Reads and writes both
// participate in the content gate: the ask transaction and the recall read
// are one database.

import (
	"context"
	"strings"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/transport/control"
)

// AgentService is the agent domain surface: the ledger's ask transaction.
// What a handler may touch is exactly these two methods — nothing reaches a
// store the operation was not constructed with.
type AgentService interface {
	// CaptureFrame ingests one renderer-minted frame and returns the
	// backend-minted frame id.
	CaptureFrame(ctx context.Context, in content.CaptureFrame) (content.CaptureFrameResult, error)
	// SubmitAsk records one ask transaction atomically (question + answer
	// entry + pending run + references/caused-by edges) and returns the
	// backend-minted run id.
	SubmitAsk(ctx context.Context, in content.AgentAsk) (content.AgentAskResult, error)
	// TransitionRun moves the run to a non-terminal state (prepared →
	// streaming) — the gate deltas may not pass before.
	TransitionRun(ctx context.Context, runID int64, to content.RunState) error
	// FinishAgentRun closes the run and its entries in one transaction.
	FinishAgentRun(ctx context.Context, runID int64, in content.FinishAgentRun) error
	// AppendRunDelta appends one streamed chunk to the answer artifact,
	// maintaining its byte_len. The delta is persisted BEFORE it is emitted
	// over the wire — the ledger is the record.
	AppendRunDelta(ctx context.Context, artifactID string, body []byte) error
	// FrameText returns the referenced frame's durable text (its artifact
	// body) — the context assembly for the ask (question + referenced
	// frames, design §4.2).
	FrameText(ctx context.Context, frameID string) (string, error)
}

// AgentOperation is the typed operation for the agent domain. Its gate is
// [content].
type AgentOperation interface {
	Run(context.Context, func(context.Context, AgentService) error) error
}

// NewAgentOperation builds an AgentOperation that acquires the content gate
// before the execution lane.
func NewAgentOperation(contentGate, lane control.Admission, db content.ContentDB) AgentOperation {
	g := &guard{}
	return newOperation[AgentService](control.NewComposite(contentGate, lane), g, newAgentService(g, db))
}

func newAgentService(g *guard, db content.ContentDB) *agentService {
	return &agentService{guard: g, ledger: db.Ledger()}
}

type agentService struct {
	guard  *guard
	ledger content.LedgerRepository
}

func (s *agentService) CaptureFrame(ctx context.Context, in content.CaptureFrame) (content.CaptureFrameResult, error) {
	if err := s.guard.check(); err != nil {
		return content.CaptureFrameResult{}, err
	}
	return s.ledger.CaptureFrame(ctx, in)
}

func (s *agentService) SubmitAsk(ctx context.Context, in content.AgentAsk) (content.AgentAskResult, error) {
	if err := s.guard.check(); err != nil {
		return content.AgentAskResult{}, err
	}
	return s.ledger.SubmitAgentAsk(ctx, in)
}

func (s *agentService) TransitionRun(ctx context.Context, runID int64, to content.RunState) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.ledger.TransitionRun(ctx, runID, to)
}

func (s *agentService) FinishAgentRun(ctx context.Context, runID int64, in content.FinishAgentRun) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.ledger.FinishAgentRun(ctx, runID, in)
}

func (s *agentService) AppendRunDelta(ctx context.Context, artifactID string, body []byte) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.ledger.AppendChunk(ctx, artifactID, body)
}

// FrameText reads one frame's durable text: the frame entry's artifact
// bodies joined in order. The recall read never hauls bytes (Entry returns
// metadata only), so the artifact fetch is the second, deliberate read.
func (s *agentService) FrameText(ctx context.Context, frameID string) (string, error) {
	if err := s.guard.check(); err != nil {
		return "", err
	}
	e, err := s.ledger.Entry(ctx, frameID)
	if err != nil {
		return "", err
	}
	if e == nil {
		return "", content.ErrFrameNotFound
	}
	for _, ex := range e.Executions {
		for _, a := range ex.Artifacts {
			if a.State != content.ArtifactOpen && a.State != content.ArtifactSealed {
				continue
			}
			art, err := s.ledger.Artifact(ctx, a.ID)
			if err != nil {
				return "", err
			}
			var sb strings.Builder
			for _, c := range art.Chunks {
				sb.Write(c)
			}
			return sb.String(), nil
		}
	}
	return "", content.ErrFrameNotFound
}
