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

// Conversations returns a stub ConversationRepository.
func (s *Stub) Conversations() ConversationRepository {
	s.log.Info("content stub: Conversations called (no-op)")
	return &convStub{log: s.log}
}

// Ledger returns a stub LedgerRepository.
func (s *Stub) Ledger() LedgerRepository {
	s.log.Info("content stub: Ledger called (no-op)")
	return &ledgerStub{log: s.log}
}

// Layout returns a stub LayoutRepository.
func (s *Stub) Layout() LayoutRepository {
	s.log.Info("content stub: Layout called (no-op)")
	return &layoutStub{log: s.log}
}

// APIRuns returns a stub API-run repository.
func (s *Stub) APIRuns() APIRunRepository {
	s.log.Info("content stub: APIRuns called (no-op)")
	return &apiRunStub{log: s.log}
}

type apiRunStub struct {
	log log.Logger
}

func (s *apiRunStub) Begin(_ context.Context, start APIRunStart) (APIRun, error) {
	s.log.Info("content stub: APIRunRepository.Begin", "collection", start.CollectionPath, "request", start.RequestRelPath)
	return APIRun{}, ErrNotImplemented
}

func (s *apiRunStub) Complete(_ context.Context, id int64, _ APIRunResult) (APIRun, error) {
	s.log.Info("content stub: APIRunRepository.Complete", "id", id)
	return APIRun{}, ErrNotImplemented
}

func (s *apiRunStub) Get(_ context.Context, id int64) (APIRun, error) {
	s.log.Info("content stub: APIRunRepository.Get", "id", id)
	return APIRun{}, ErrNotImplemented
}

func (s *apiRunStub) List(_ context.Context, collectionPath, requestRelPath string) ([]APIRun, error) {
	s.log.Info("content stub: APIRunRepository.List", "collection", collectionPath, "request", requestRelPath)
	return nil, ErrNotImplemented
}

func (s *apiRunStub) Delete(_ context.Context, id int64) error {
	s.log.Info("content stub: APIRunRepository.Delete", "id", id)
	return ErrNotImplemented
}

var _ APIRunRepository = (*apiRunStub)(nil)

// Backup returns ErrNotImplemented: the stub has nothing to snapshot.
func (s *Stub) Backup(_ context.Context, destPath string) error {
	s.log.Info("content stub: Backup called (no-op)", "dest", destPath)
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

func (s *ledgerStub) RecordCompleted(_ context.Context, in CompletedCommand) (string, error) {
	s.log.Info("content stub: LedgerRepository.RecordCompleted", "intent", in.Intent)
	return "", ErrNotImplemented
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

func (s *ledgerStub) QueryEntries(_ context.Context, q LedgerQuery) (LedgerPage, error) {
	s.log.Info("content stub: LedgerRepository.QueryEntries", "scope", q.Scope, "limit", q.Limit)
	return LedgerPage{Entries: []LedgerEntrySummary{}}, ErrNotImplemented
}

func (s *ledgerStub) RewriteRedaction(_ context.Context, entryID string, span Redaction, reference string) error {
	s.log.Info("content stub: LedgerRepository.RewriteRedaction", "entry", entryID, "span", span, "reference", reference)
	return ErrNotImplemented
}

func (s *ledgerStub) DeleteEntry(_ context.Context, id string) error {
	s.log.Info("content stub: LedgerRepository.DeleteEntry", "id", id)
	return ErrNotImplemented
}

func (s *ledgerStub) EvictEntries(_ context.Context, req EvictionRequest) (EvictionResult, error) {
	s.log.Info("content stub: LedgerRepository.EvictEntries", "before", req.Before, "max", req.Max)
	return EvictionResult{}, ErrNotImplemented
}

func (s *ledgerStub) Watermark(_ context.Context) (RetentionWatermark, error) {
	s.log.Info("content stub: LedgerRepository.Watermark")
	return RetentionWatermark{}, ErrNotImplemented
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

func (s *ledgerStub) CaptureOutput(_ context.Context, in CaptureOutput) (bool, error) {
	s.log.Info("content stub: LedgerRepository.CaptureOutput",
		"artifact", in.ArtifactID, "seq", in.Seq, "bytes", len(in.Body))
	return false, nil
}

func (s *ledgerStub) AppendChunk(_ context.Context, artifactID string, seq int, body []byte) error {
	s.log.Info("content stub: LedgerRepository.AppendChunk",
		"artifact", artifactID, "seq", seq, "bytes", len(body))
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

var _ LayoutRepository = (*layoutStub)(nil)

// layoutStub implements LayoutRepository for the stub.
type layoutStub struct {
	log log.Logger
}

func (s *layoutStub) CreateWorkspace(_ context.Context, ws Workspace, firstTab Tab, firstPane Pane) (Created[NewWorkspace], error) {
	s.log.Info("content stub: LayoutRepository.CreateWorkspace",
		"id", ws.ID, "first_tab", firstTab.ID, "first_pane", firstPane.ID)
	return Created[NewWorkspace]{}, ErrNotImplemented
}

func (s *layoutStub) Snapshot(_ context.Context) (LayoutSnapshot, error) {
	s.log.Info("content stub: LayoutRepository.Snapshot")
	return LayoutSnapshot{}, ErrNotImplemented
}

func (s *layoutStub) Workspaces(_ context.Context) ([]Workspace, error) {
	s.log.Info("content stub: LayoutRepository.Workspaces")
	return nil, ErrNotImplemented
}

func (s *layoutStub) RenameWorkspace(_ context.Context, id, name string) (Workspace, error) {
	s.log.Info("content stub: LayoutRepository.RenameWorkspace", "id", id, "name", name)
	return Workspace{}, ErrNotImplemented
}

func (s *layoutStub) RecolourWorkspace(_ context.Context, id string, colour *string) (Workspace, error) {
	s.log.Info("content stub: LayoutRepository.RecolourWorkspace", "id", id, "set", colour != nil)
	return Workspace{}, ErrNotImplemented
}

func (s *layoutStub) ReorderWorkspaces(_ context.Context, ids []string) ([]Workspace, error) {
	s.log.Info("content stub: LayoutRepository.ReorderWorkspaces", "count", len(ids))
	return nil, ErrNotImplemented
}

func (s *layoutStub) DeleteWorkspace(_ context.Context, id string, next Replacement) error {
	s.log.Info("content stub: LayoutRepository.DeleteWorkspace", "id", id, "replacement_tab", next.TabID)
	return ErrNotImplemented
}

func (s *layoutStub) CreateTab(_ context.Context, tab Tab, firstPane Pane) (Created[NewTab], error) {
	s.log.Info("content stub: LayoutRepository.CreateTab",
		"id", tab.ID, "workspace", tab.WorkspaceID, "first_pane", firstPane.ID)
	return Created[NewTab]{}, ErrNotImplemented
}

func (s *layoutStub) Tabs(_ context.Context, workspaceID string) ([]Tab, error) {
	s.log.Info("content stub: LayoutRepository.Tabs", "workspace", workspaceID)
	return nil, ErrNotImplemented
}

func (s *layoutStub) RenameTab(_ context.Context, id string, _ *string) (Tab, error) {
	s.log.Info("content stub: LayoutRepository.RenameTab", "id", id)
	return Tab{}, ErrNotImplemented
}

func (s *layoutStub) RecolourTab(_ context.Context, id string, _ *string) (Tab, error) {
	s.log.Info("content stub: LayoutRepository.RecolourTab", "id", id)
	return Tab{}, ErrNotImplemented
}

func (s *layoutStub) PinTab(_ context.Context, id string, pinned bool) (Tab, error) {
	s.log.Info("content stub: LayoutRepository.PinTab", "id", id, "pinned", pinned)
	return Tab{}, ErrNotImplemented
}

func (s *layoutStub) ReorderTabs(_ context.Context, workspaceID string, ids []string) ([]Tab, error) {
	s.log.Info("content stub: LayoutRepository.ReorderTabs", "workspace", workspaceID, "count", len(ids))
	return nil, ErrNotImplemented
}

func (s *layoutStub) DeleteTab(_ context.Context, id string, next Replacement) error {
	s.log.Info("content stub: LayoutRepository.DeleteTab", "id", id, "replacement_tab", next.TabID)
	return ErrNotImplemented
}

func (s *layoutStub) CreatePane(_ context.Context, pane Pane) (Created[Pane], error) {
	s.log.Info("content stub: LayoutRepository.CreatePane", "id", pane.ID, "tab", pane.TabID)
	return Created[Pane]{}, ErrNotImplemented
}

func (s *layoutStub) Panes(_ context.Context, tabID string) ([]Pane, error) {
	s.log.Info("content stub: LayoutRepository.Panes", "tab", tabID)
	return nil, ErrNotImplemented
}

func (s *layoutStub) DeletePane(_ context.Context, id string, next Replacement) error {
	s.log.Info("content stub: LayoutRepository.DeletePane", "id", id, "replacement_tab", next.TabID)
	return ErrNotImplemented
}

func (s *layoutStub) SetPaneCwd(_ context.Context, paneID, cwd string) (Pane, error) {
	s.log.Info("content stub: LayoutRepository.SetPaneCwd", "pane", paneID, "cwd", cwd)
	return Pane{}, ErrNotImplemented
}

func (s *layoutStub) MovePane(_ context.Context, paneID, tabID string) (Pane, error) {
	s.log.Info("content stub: LayoutRepository.MovePane", "pane", paneID, "tab", tabID)
	return Pane{}, ErrNotImplemented
}

func (s *layoutStub) WorkspaceForPane(_ context.Context, paneID string) (string, error) {
	s.log.Info("content stub: LayoutRepository.WorkspaceForPane", "pane", paneID)
	return "", ErrNotImplemented
}

func (s *layoutStub) ClearWindow(_ context.Context) error {
	s.log.Info("content stub: LayoutRepository.ClearWindow")
	return ErrNotImplemented
}
