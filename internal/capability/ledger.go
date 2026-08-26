package capability

// The ledger domain (nocx-rtg0.3): ledger.open, ledger.bind and ledger.close
// — the write path of schema v1 (ADR-0019, ADR-0020, design §6.2). One
// operation, one gate: the content gate, because the ledger IS content's
// schema v1 and the ask transaction, the recall read and this write path are
// one database.
//
// It is a separate service from AgentService deliberately. What a handler may
// touch is exactly the methods its own surface declares, and the ledger's
// lifecycle writes (Submit / StartExecution / FinishExecution) and the ask
// transaction are different jobs with different failure modes; widening
// AgentService would hand the ask handler a phase machine it has no business
// reaching, and this handler the frame capture.

import (
	"context"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/transport/control"
)

// LedgerService is the ledger domain surface: the entry lifecycle, plus the
// environment identity an entry's execution pins. Read and write both
// participate in the content gate — the phase decision is a read-modify-write
// over the same rows the write touches.
type LedgerService interface {
	// Entry reads one entry with its executions — how the handler learns the
	// row's current phase before deciding whether an event may be applied.
	// Nil when no row carries id.
	Entry(ctx context.Context, id string) (*content.LedgerEntry, error)
	// EnsureEnvironment records the durable identity of where work happens;
	// the first write wins.
	EnsureEnvironment(ctx context.Context, env content.Environment) error
	// RecordObservation appends one versioned snapshot of the environment's
	// mutable facts and returns its row identity — what an execution pins.
	// StartExecution refuses an environment with no observation, so this is
	// not optional bookkeeping: it is what makes a run pinnable.
	RecordObservation(ctx context.Context, obs content.Observation) (int64, error)
	// Submit accepts an intent as an open entry and returns the
	// backend-assigned ingest_seq — the ledger's only total order.
	Submit(ctx context.Context, in content.SubmitEntry) (content.SubmitResult, error)
	// StartExecution begins one run of an entry and moves the entry to bound.
	StartExecution(ctx context.Context, in content.StartExecution) (int64, error)
	// FinishExecution closes the run and the entry with it.
	FinishExecution(ctx context.Context, executionID int64, end content.FinishExecution) error
	// QueryEntries serves ledger.query: one page of the recall ladder's
	// rung, newest first by the ledger's own total order, with the page's
	// exhaustion, whether the ledger holds any row at all, and the
	// store-wide retention horizon.
	QueryEntries(ctx context.Context, q content.LedgerQuery) (content.LedgerPage, error)
	// Edges serves the detail read's relations: every edge touching an
	// entry, in either direction. It is what makes the ledger a memory
	// rather than a log (design §3.4).
	Edges(ctx context.Context, entryID string) ([]content.Edge, error)
	// Caused serves the detail read's causal flow (nocx-h1l4o): everything
	// this entry caused, in the causal order the turn assigned, each row
	// resolved into what a reader draws it with. The JOIN and the ORDER are
	// the ledger's — a reader given raw edges would own the arrangement a
	// second time, which is what AD-8 forbids.
	Caused(ctx context.Context, entryID string) ([]content.CausedEntry, error)
	// Artifact serves ledger.artifact: one body with its chunks, which is
	// what a restored block's output is drawn from. Nil when no artifact
	// carries the id — a body retention has evicted, which the caller must
	// render as a hole rather than as silence (ADR-0019 §7).
	Artifact(ctx context.Context, id string) (*content.Artifact, error)
	// CaptureOutput serves ledger.capture: one body of a frozen block,
	// against the entry's own execution. The bool is whether the body is
	// kept — false when output retention is off or the entry is sensitive,
	// which is an answer and not a failure.
	CaptureOutput(ctx context.Context, in content.CaptureOutput) (bool, error)
}

// LedgerOperation is the typed operation for the ledger domain. Its gate is
// [content].
type LedgerOperation interface {
	Run(context.Context, func(context.Context, LedgerService) error) error
}

// NewLedgerOperation builds a LedgerOperation that acquires the content gate
// before the execution lane.
func NewLedgerOperation(contentGate, lane control.Admission, db content.ContentDB) LedgerOperation {
	g := &guard{}
	return newOperation[LedgerService](control.NewComposite(contentGate, lane), g, newLedgerService(g, db))
}

func newLedgerService(g *guard, db content.ContentDB) *ledgerService {
	return &ledgerService{guard: g, ledger: db.Ledger()}
}

type ledgerService struct {
	guard  *guard
	ledger content.LedgerRepository
}

func (s *ledgerService) Entry(ctx context.Context, id string) (*content.LedgerEntry, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.ledger.Entry(ctx, id)
}

func (s *ledgerService) EnsureEnvironment(ctx context.Context, env content.Environment) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.ledger.EnsureEnvironment(ctx, env)
}

func (s *ledgerService) RecordObservation(ctx context.Context, obs content.Observation) (int64, error) {
	if err := s.guard.check(); err != nil {
		return 0, err
	}
	return s.ledger.RecordObservation(ctx, obs)
}

func (s *ledgerService) Submit(ctx context.Context, in content.SubmitEntry) (content.SubmitResult, error) {
	if err := s.guard.check(); err != nil {
		return content.SubmitResult{}, err
	}
	return s.ledger.Submit(ctx, in)
}

func (s *ledgerService) StartExecution(ctx context.Context, in content.StartExecution) (int64, error) {
	if err := s.guard.check(); err != nil {
		return 0, err
	}
	return s.ledger.StartExecution(ctx, in)
}

func (s *ledgerService) FinishExecution(ctx context.Context, executionID int64, end content.FinishExecution) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.ledger.FinishExecution(ctx, executionID, end)
}

func (s *ledgerService) QueryEntries(ctx context.Context, q content.LedgerQuery) (content.LedgerPage, error) {
	if err := s.guard.check(); err != nil {
		return content.LedgerPage{}, err
	}
	return s.ledger.QueryEntries(ctx, q)
}

func (s *ledgerService) Edges(ctx context.Context, entryID string) ([]content.Edge, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.ledger.Edges(ctx, entryID)
}

func (s *ledgerService) Caused(ctx context.Context, entryID string) ([]content.CausedEntry, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.ledger.Caused(ctx, entryID)
}

func (s *ledgerService) Artifact(ctx context.Context, id string) (*content.Artifact, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.ledger.Artifact(ctx, id)
}

func (s *ledgerService) CaptureOutput(ctx context.Context, in content.CaptureOutput) (bool, error) {
	if err := s.guard.check(); err != nil {
		return false, err
	}
	return s.ledger.CaptureOutput(ctx, in)
}
