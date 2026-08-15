package content

import (
	"context"

	"github.com/shady2k/nocx/internal/log"
)

// Stub is the no-op implementation of ContentDB. Every repository method logs
// the call and returns ErrNotImplemented. It exists so the seam compiles and
// can be injected before the SQLite implementation lands.
type Stub struct {
	log log.Logger
}

// NewStub creates a Stub that logs calls through logger.
func NewStub(logger log.Logger) *Stub {
	return &Stub{log: logger}
}

// convStub implements ConversationRepository for the stub.
type convStub struct {
	log log.Logger
}

func (s *convStub) Save(_ context.Context, conv Conversation) error {
	s.log.Info("content stub: ConversationRepository.Save", "id", conv.ID)
	return ErrNotImplemented
}

func (s *convStub) GetByID(_ context.Context, id string) (*Conversation, error) {
	s.log.Info("content stub: ConversationRepository.GetByID", "id", id)
	return nil, ErrNotImplemented
}

func (s *convStub) List(_ context.Context, limit int) ([]Conversation, error) {
	s.log.Info("content stub: ConversationRepository.List", "limit", limit)
	return nil, ErrNotImplemented
}

// histStub implements CommandHistoryRepository for the stub.
type histStub struct {
	log log.Logger
}

func (s *histStub) Add(_ context.Context, record CommandRecord) (int64, error) {
	s.log.Info("content stub: CommandHistoryRepository.Add", "command", record.Command)
	return 0, ErrNotImplemented
}

func (s *histStub) List(_ context.Context, limit int) ([]CommandRecord, error) {
	s.log.Info("content stub: CommandHistoryRepository.List", "limit", limit)
	return nil, ErrNotImplemented
}

func (s *histStub) GetByID(_ context.Context, id int64) (*CommandRecord, error) {
	s.log.Info("content stub: CommandHistoryRepository.GetByID", "id", id)
	return nil, ErrNotImplemented
}

func (s *histStub) FindByPrefix(_ context.Context, prefix string, limit int) ([]CommandRecord, error) {
	s.log.Info("content stub: CommandHistoryRepository.FindByPrefix", "prefix", prefix, "limit", limit)
	return nil, ErrNotImplemented
}

func (s *histStub) RewriteRedaction(_ context.Context, id int64, span Redaction, reference string) error {
	s.log.Info("content stub: CommandHistoryRepository.RewriteRedaction", "id", id, "span", span, "reference", reference)
	return ErrNotImplemented
}

func (s *histStub) Query(_ context.Context, scope Scope, cwd, host string, limit int, _ *int64, text string) (HistoryPage, error) {
	s.log.Info("content stub: CommandHistoryRepository.Query", "scope", scope, "cwd", cwd, "host", host, "limit", limit, "text", text)
	return HistoryPage{}, ErrNotImplemented
}

// Conversations returns a stub ConversationRepository.
func (s *Stub) Conversations() ConversationRepository {
	s.log.Info("content stub: Conversations called (no-op)")
	return &convStub{log: s.log}
}

// CommandHistory returns a stub CommandHistoryRepository.
func (s *Stub) CommandHistory() CommandHistoryRepository {
	s.log.Info("content stub: CommandHistory called (no-op)")
	return &histStub{log: s.log}
}

// Ledger returns a stub LedgerRepository.
func (s *Stub) Ledger() LedgerRepository {
	s.log.Info("content stub: Ledger called (no-op)")
	return &ledgerStub{log: s.log}
}

// Backup returns ErrNotImplemented: the stub has nothing to snapshot.
func (s *Stub) Backup(_ context.Context, destPath string) error {
	s.log.Info("content stub: Backup called (no-op)", "dest", destPath)
	return ErrNotImplemented
}

// RestorePrivate returns ErrNotImplemented: the stub stores nothing.
func (s *Stub) RestorePrivate(_ context.Context, conversations []Conversation, history []CommandRecord) error {
	s.log.Info("content stub: RestorePrivate called (no-op)", "conversations", len(conversations), "history", len(history))
	return ErrNotImplemented
}

// Close is a no-op.
func (s *Stub) Close() error {
	s.log.Info("content stub: Close called (no-op)")
	return nil
}

var _ LedgerRepository = (*ledgerStub)(nil)

// ledgerStub implements LedgerRepository for the stub.
type ledgerStub struct {
	log log.Logger
}

func (s *ledgerStub) CreateWorkspace(_ context.Context, ws Workspace) error {
	s.log.Info("content stub: LedgerRepository.CreateWorkspace", "id", ws.ID)
	return ErrNotImplemented
}

func (s *ledgerStub) CreateSession(_ context.Context, sess Session) error {
	s.log.Info("content stub: LedgerRepository.CreateSession", "id", sess.ID)
	return ErrNotImplemented
}

func (s *ledgerStub) DeleteSession(_ context.Context, id string) error {
	s.log.Info("content stub: LedgerRepository.DeleteSession", "id", id)
	return ErrNotImplemented
}

func (s *ledgerStub) EnsureEnvironment(_ context.Context, env Environment) error {
	s.log.Info("content stub: LedgerRepository.EnsureEnvironment", "id", env.ID)
	return ErrNotImplemented
}

func (s *ledgerStub) RecordObservation(_ context.Context, obs Observation) (int64, error) {
	s.log.Info("content stub: LedgerRepository.RecordObservation", "environment", obs.EnvironmentID)
	return 0, ErrNotImplemented
}

func (s *ledgerStub) Submit(_ context.Context, in SubmitEntry) (SubmitResult, error) {
	s.log.Info("content stub: LedgerRepository.Submit", "id", in.ID, "intent", in.Intent)
	return SubmitResult{}, ErrNotImplemented
}

func (s *ledgerStub) Entry(_ context.Context, id string) (*LedgerEntry, error) {
	s.log.Info("content stub: LedgerRepository.Entry", "id", id)
	return nil, ErrNotImplemented
}

func (s *ledgerStub) ListEntries(_ context.Context, limit int) ([]LedgerEntrySummary, error) {
	s.log.Info("content stub: LedgerRepository.ListEntries", "limit", limit)
	return nil, ErrNotImplemented
}

func (s *ledgerStub) DeleteEntry(_ context.Context, id string) error {
	s.log.Info("content stub: LedgerRepository.DeleteEntry", "id", id)
	return ErrNotImplemented
}

func (s *ledgerStub) StartExecution(_ context.Context, in StartExecution) (int64, error) {
	s.log.Info("content stub: LedgerRepository.StartExecution", "entry", in.EntryID)
	return 0, ErrNotImplemented
}

func (s *ledgerStub) FinishExecution(_ context.Context, executionID int64, end FinishExecution) error {
	s.log.Info("content stub: LedgerRepository.FinishExecution", "execution", executionID)
	return ErrNotImplemented
}

func (s *ledgerStub) AppendArtifact(_ context.Context, in AppendArtifact) (string, error) {
	s.log.Info("content stub: LedgerRepository.AppendArtifact", "id", in.ID)
	return "", ErrNotImplemented
}

func (s *ledgerStub) AppendChunk(_ context.Context, artifactID string, body []byte) error {
	s.log.Info("content stub: LedgerRepository.AppendChunk", "artifact", artifactID, "bytes", len(body))
	return ErrNotImplemented
}

func (s *ledgerStub) Artifact(_ context.Context, id string) (*Artifact, error) {
	s.log.Info("content stub: LedgerRepository.Artifact", "id", id)
	return nil, ErrNotImplemented
}

func (s *ledgerStub) AddEdge(_ context.Context, e Edge) error {
	s.log.Info("content stub: LedgerRepository.AddEdge", "from", e.From, "to", e.To, "rel", string(e.Rel))
	return ErrNotImplemented
}

func (s *ledgerStub) Edges(_ context.Context, entryID string) ([]Edge, error) {
	s.log.Info("content stub: LedgerRepository.Edges", "entry", entryID)
	return nil, ErrNotImplemented
}

func (s *ledgerStub) CaptureFrame(_ context.Context, in CaptureFrame) (CaptureFrameResult, error) {
	s.log.Info("content stub: LedgerRepository.CaptureFrame", "capture", in.CaptureID, "source", string(in.Source))
	return CaptureFrameResult{}, ErrNotImplemented
}

func (s *ledgerStub) SubmitAgentAsk(_ context.Context, in AgentAsk) (AgentAskResult, error) {
	s.log.Info("content stub: LedgerRepository.SubmitAgentAsk", "id", in.ID)
	return AgentAskResult{}, ErrNotImplemented
}

func (s *ledgerStub) RunState(_ context.Context, executionID int64) (*RunState, error) {
	s.log.Info("content stub: LedgerRepository.RunState", "execution", executionID)
	return nil, ErrNotImplemented
}

func (s *ledgerStub) TransitionRun(_ context.Context, runID int64, to RunState) error {
	s.log.Info("content stub: LedgerRepository.TransitionRun", "run", runID, "to", string(to))
	return ErrNotImplemented
}

func (s *ledgerStub) FinishAgentRun(_ context.Context, runID int64, in FinishAgentRun) error {
	s.log.Info("content stub: LedgerRepository.FinishAgentRun", "run", runID, "state", string(in.State))
	return ErrNotImplemented
}
