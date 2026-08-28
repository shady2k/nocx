package capability

import (
	"context"

	"github.com/shady2k/nocx/internal/note"
	"github.com/shady2k/nocx/internal/transport/control"
)

// NoteService is the domain surface of the notes library — a thin
// guard-bound wrapper over *note.Service, so a service that escapes its
// callback fails with ErrOperationInactive rather than reaching the store
// without the exclusion the operation exists to provide.
type NoteService interface {
	List(context.Context) ([]note.Row, error)
	Get(ctx context.Context, id string) (note.Note, error)
	Create(ctx context.Context, body string) (note.Note, error)
	Update(ctx context.Context, id, body string) (note.Note, error)
	Delete(ctx context.Context, id string) error
	Search(ctx context.Context, query string) ([]note.Row, error)
}

// NoteOperation is the typed operation for the notes.* methods. Its gate is
// [config], for the same reason the snippets one holds it: a restore
// replacing the library underneath a write is exactly the two-writer race
// the config gate serialises. Notes never touch the vault, so the vault
// gate is deliberately not held.
type NoteOperation interface {
	AssistantOperation
	Run(context.Context, func(context.Context, NoteService) error) error
}

// NewNoteOperation builds a NoteOperation that acquires configGate then the
// execution lane, and hands the callback a guard-bound notes service.
func NewNoteOperation(configGate, lane control.Admission, svc *note.Service) NoteOperation {
	g := &guard{}
	return newOperation[NoteService](Direct("NoteOperation"), control.NewComposite(configGate, lane), g, &noteService{guard: g, svc: svc})
}

type noteService struct {
	guard *guard
	svc   *note.Service
}

func (s *noteService) List(ctx context.Context) ([]note.Row, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.svc.List(ctx)
}

func (s *noteService) Get(ctx context.Context, id string) (note.Note, error) {
	if err := s.guard.check(); err != nil {
		return note.Note{}, err
	}
	return s.svc.Get(ctx, id)
}

func (s *noteService) Create(ctx context.Context, body string) (note.Note, error) {
	if err := s.guard.check(); err != nil {
		return note.Note{}, err
	}
	return s.svc.Create(ctx, body)
}

func (s *noteService) Update(ctx context.Context, id, body string) (note.Note, error) {
	if err := s.guard.check(); err != nil {
		return note.Note{}, err
	}
	return s.svc.Update(ctx, id, body)
}

func (s *noteService) Delete(ctx context.Context, id string) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.svc.Delete(ctx, id)
}

func (s *noteService) Search(ctx context.Context, query string) ([]note.Row, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.svc.Search(ctx, query)
}
