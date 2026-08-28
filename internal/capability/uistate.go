package capability

import (
	"context"

	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/uistate"
)

// UIStateService is the domain surface of the UI-state document — a thin
// guard-bound wrapper over *uistate.Store, so a service that escapes its
// callback fails with ErrOperationInactive rather than reaching the store
// without the exclusion the operation exists to provide.
//
// Only the renderer's half is here. Window geometry is deliberately absent:
// the renderer can neither know it nor act on it, and putting it behind a
// method the transport could call would create a second potential owner of a
// fact the Wails side already owns (ADR-0048 §7).
type UIStateService interface {
	Layout(context.Context) uistate.Layout
	SetLayout(context.Context, uistate.Layout)
}

// UIStateOperation is the typed operation for the uistate.* methods. Its gate
// is [config], for the same reason the snippets and notes ones hold it: the
// document lives in the config directory, and a restore replacing that
// directory underneath a write is the two-writer race the config gate
// serialises. UI state never touches the vault, so the vault gate is
// deliberately not held.
type UIStateOperation interface {
	Run(context.Context, func(context.Context, UIStateService) error) error
}

// NewUIStateOperation builds a UIStateOperation that acquires configGate then
// the execution lane, and hands the callback a guard-bound UI-state service.
func NewUIStateOperation(configGate, lane control.Admission, store *uistate.Store) UIStateOperation {
	g := &guard{}
	return newOperation[UIStateService](control.NewComposite(configGate, lane), g, &uiStateService{guard: g, store: store})
}

type uiStateService struct {
	guard *guard
	store *uistate.Store
}

// Layout serves from memory. The zero Layout on a dead handle is correct: the
// caller is answering a read it should never have been able to make, and an
// empty layout is what an absent document yields anyway.
func (s *uiStateService) Layout(context.Context) uistate.Layout {
	if err := s.guard.check(); err != nil {
		return uistate.Layout{}
	}
	return s.store.Layout()
}

// SetLayout returns nothing because the store's write is deferred: the value
// is recorded now and reaches the disk when changes stop. There is no error a
// caller could act on — a failed write leaves the panel where the user put it
// and is retried by the next change (ADR-0048 §5).
func (s *uiStateService) SetLayout(_ context.Context, l uistate.Layout) {
	if err := s.guard.check(); err != nil {
		return
	}
	s.store.SetLayout(l)
}
