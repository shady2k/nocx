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
// Both halves of the document are here. The renderer's half (sidebar, active
// tab) is read and written by the renderer itself. The window half is RELAYED
// (ADR-0098): the shell owns the native window and the coordinator owns the
// document, and neither can reach the other except through this service — the
// renderer carries the saved geometry to the shell's restore binding and the
// shell's samples back here, deciding nothing on either leg.
type UIStateService interface {
	Layout(context.Context) uistate.Layout
	SetLayout(context.Context, uistate.Layout)
	// Window reports the recorded geometry, for the shell to restore at
	// bootstrap (ADR-0098).
	Window(context.Context) uistate.Window
	// ObserveWindow folds one live sample of the shell's window into the
	// recorded state. The folding rule — a maximised or full-screen sample
	// moves only its flags — is the store's, not the caller's.
	ObserveWindow(context.Context, uistate.Window)
}

// UIStateOperation is the typed operation for the uistate.* methods. Its gate
// is [config], for the same reason the snippets and notes ones hold it: the
// document lives in the config directory, and a restore replacing that
// directory underneath a write is the two-writer race the config gate
// serialises. UI state never touches the vault, so the vault gate is
// deliberately not held.
type UIStateOperation interface {
	AssistantOperation
	Run(context.Context, func(context.Context, UIStateService) error) error
}

// NewUIStateOperation builds a UIStateOperation that acquires configGate then
// the execution lane, and hands the callback a guard-bound UI-state service.
func NewUIStateOperation(configGate, lane control.Admission, store *uistate.Store) UIStateOperation {
	g := &guard{}
	return newOperation[UIStateService](Excluded("it persists renderer presentation state rather than performing a domain action"), control.NewComposite(configGate, lane), g, &uiStateService{guard: g, store: store})
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

// Window serves the recorded geometry. The zero Window on a dead handle is
// correct for the same reason the zero Layout is: the caller is answering a
// read it should never have been able to make, and a zero geometry is what an
// absent document yields anyway — the shell then leaves the window at the
// size it opened at (ADR-0098).
func (s *uiStateService) Window(context.Context) uistate.Window {
	if err := s.guard.check(); err != nil {
		return uistate.Window{}
	}
	return s.store.Window()
}

// ObserveWindow folds one live sample into the recorded state. The fold is
// uistate.Observe — one owner for the maximised rule — and the write itself is
// deferred exactly like SetLayout's, so a drag of any length is one write.
func (s *uiStateService) ObserveWindow(_ context.Context, live uistate.Window) {
	if err := s.guard.check(); err != nil {
		return
	}
	s.store.SetWindow(uistate.Observe(s.store.Window(), live))
}
