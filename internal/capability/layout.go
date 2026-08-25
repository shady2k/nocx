package capability

// The layout domain (nocx-isoph.2): the workspaces.*, tabs.* and panes.*
// methods by which the frontend ASKS the backend to create, move and destroy
// the objects it used to own itself (design
// .internal/specs/2026-08-16-tabs-panes-and-blocks-design.md §4.1).
//
// One operation, one gate: the CONTENT gate, because the layout chain is
// three tables in content's schema v1 and a reorder is a read-modify-write
// over rows the ledger's own writes sit beside. Sharing the gate with the
// ledger and the ask transaction is not an approximation — it is the same
// database and the same single writer goroutine underneath.
//
// It is a separate SERVICE from LedgerService for the reason the ledger is
// separate from the agent's: what a handler may touch is exactly what its own
// surface declares. The layout handler has no business reaching the entry
// phase machine, and the ledger handler none moving a pane.

import (
	"context"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/transport/control"
)

// LayoutService is the layout domain surface. It is deliberately the whole of
// LayoutRepository's write path plus the reads those writes are checked
// against — a create must be able to answer with the row that is already
// there, which is a read — and nothing else.
type LayoutService interface {
	// The creates take their first member with them, because a container
	// with none may not exist even for the length of a statement
	// (nocx-isoph.3, design §4.1): the create IS where the content goes.
	// Snapshot is the whole chain in one answer, and the only read here that
	// no write is checked against: it exists for the renderer, which draws
	// itself from it (nocx-isoph.4).
	Snapshot(ctx context.Context) (content.LayoutSnapshot, error)
	SandboxGrantedPaneIDs(ctx context.Context) (map[string]struct{}, error)

	CreateWorkspace(ctx context.Context, ws content.Workspace, firstTab content.Tab, firstPane content.Pane) (content.Created[content.NewWorkspace], error)
	RenameWorkspace(ctx context.Context, id, name string) (content.Workspace, error)
	RecolourWorkspace(ctx context.Context, id string, colour *string) (content.Workspace, error)
	ReorderWorkspaces(ctx context.Context, ids []string) ([]content.Workspace, error)
	// The closes take the identity of the tab that replaces the application's
	// last one, for the same §7 reason: it is a durable id, so it is the
	// frontend's to mint and never the backend's.
	DeleteWorkspace(ctx context.Context, id string, next content.Replacement) error

	CreateTab(ctx context.Context, tab content.Tab, firstPane content.Pane) (content.Created[content.NewTab], error)
	RenameTab(ctx context.Context, id string, name *string) (content.Tab, error)
	RecolourTab(ctx context.Context, id string, colour *string) (content.Tab, error)
	PinTab(ctx context.Context, id string, pinned bool) (content.Tab, error)
	ReorderTabs(ctx context.Context, workspaceID string, ids []string) ([]content.Tab, error)
	DeleteTab(ctx context.Context, id string, next content.Replacement) error

	CreatePane(ctx context.Context, pane content.Pane) (content.Created[content.Pane], error)
	MovePane(ctx context.Context, id, tabID string) (content.Pane, error)
	// SetPaneCwd records where a pane's shell IS — what a restore reopens
	// it in. The caller must have verified the cwd (AD-5).
	SetPaneCwd(ctx context.Context, id, cwd string) (content.Pane, error)
	// DeletePane takes the replacement for the same reason the other two
	// closes do: removing the last pane can empty the application, and the
	// tab that appears then has a durable id, so it is the frontend's.
	DeletePane(ctx context.Context, id string, next content.Replacement) error
}

// LayoutOperation is the typed operation for the layout domain. Its gate is
// [content].
type LayoutOperation interface {
	Run(context.Context, func(context.Context, LayoutService) error) error
}

// NewLayoutOperation builds a LayoutOperation that acquires the content gate
// before the execution lane.
func NewLayoutOperation(contentGate, lane control.Admission, db content.ContentDB) LayoutOperation {
	g := &guard{}
	return newOperation[LayoutService](control.NewComposite(contentGate, lane), g, newLayoutService(g, db))
}

func newLayoutService(g *guard, db content.ContentDB) *layoutService {
	return &layoutService{guard: g, layout: db.Layout()}
}

type layoutService struct {
	guard  *guard
	layout content.LayoutRepository
}

func (s *layoutService) CreateWorkspace(ctx context.Context, ws content.Workspace, firstTab content.Tab, firstPane content.Pane) (content.Created[content.NewWorkspace], error) {
	if err := s.guard.check(); err != nil {
		return content.Created[content.NewWorkspace]{}, err
	}
	return s.layout.CreateWorkspace(ctx, ws, firstTab, firstPane)
}

func (s *layoutService) RenameWorkspace(ctx context.Context, id, name string) (content.Workspace, error) {
	if err := s.guard.check(); err != nil {
		return content.Workspace{}, err
	}
	return s.layout.RenameWorkspace(ctx, id, name)
}

func (s *layoutService) RecolourWorkspace(ctx context.Context, id string, colour *string) (content.Workspace, error) {
	if err := s.guard.check(); err != nil {
		return content.Workspace{}, err
	}
	return s.layout.RecolourWorkspace(ctx, id, colour)
}

func (s *layoutService) ReorderWorkspaces(ctx context.Context, ids []string) ([]content.Workspace, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.layout.ReorderWorkspaces(ctx, ids)
}

func (s *layoutService) DeleteWorkspace(ctx context.Context, id string, next content.Replacement) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.layout.DeleteWorkspace(ctx, id, next)
}

func (s *layoutService) CreateTab(ctx context.Context, tab content.Tab, firstPane content.Pane) (content.Created[content.NewTab], error) {
	if err := s.guard.check(); err != nil {
		return content.Created[content.NewTab]{}, err
	}
	return s.layout.CreateTab(ctx, tab, firstPane)
}

func (s *layoutService) RenameTab(ctx context.Context, id string, name *string) (content.Tab, error) {
	if err := s.guard.check(); err != nil {
		return content.Tab{}, err
	}
	return s.layout.RenameTab(ctx, id, name)
}

func (s *layoutService) RecolourTab(ctx context.Context, id string, colour *string) (content.Tab, error) {
	if err := s.guard.check(); err != nil {
		return content.Tab{}, err
	}
	return s.layout.RecolourTab(ctx, id, colour)
}

func (s *layoutService) PinTab(ctx context.Context, id string, pinned bool) (content.Tab, error) {
	if err := s.guard.check(); err != nil {
		return content.Tab{}, err
	}
	return s.layout.PinTab(ctx, id, pinned)
}

func (s *layoutService) ReorderTabs(ctx context.Context, workspaceID string, ids []string) ([]content.Tab, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.layout.ReorderTabs(ctx, workspaceID, ids)
}

func (s *layoutService) DeleteTab(ctx context.Context, id string, next content.Replacement) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.layout.DeleteTab(ctx, id, next)
}

func (s *layoutService) CreatePane(ctx context.Context, pane content.Pane) (content.Created[content.Pane], error) {
	if err := s.guard.check(); err != nil {
		return content.Created[content.Pane]{}, err
	}
	return s.layout.CreatePane(ctx, pane)
}

func (s *layoutService) SetPaneCwd(ctx context.Context, id, cwd string) (content.Pane, error) {
	if err := s.guard.check(); err != nil {
		return content.Pane{}, err
	}
	return s.layout.SetPaneCwd(ctx, id, cwd)
}

func (s *layoutService) MovePane(ctx context.Context, id, tabID string) (content.Pane, error) {
	if err := s.guard.check(); err != nil {
		return content.Pane{}, err
	}
	return s.layout.MovePane(ctx, id, tabID)
}

func (s *layoutService) DeletePane(ctx context.Context, id string, next content.Replacement) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.layout.DeletePane(ctx, id, next)
}

func (s *layoutService) Snapshot(ctx context.Context) (content.LayoutSnapshot, error) {
	if err := s.guard.check(); err != nil {
		return content.LayoutSnapshot{}, err
	}
	return s.layout.Snapshot(ctx)
}

func (s *layoutService) SandboxGrantedPaneIDs(ctx context.Context) (map[string]struct{}, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.layout.SandboxGrantedPaneIDs(ctx)
}
