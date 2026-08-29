package capability

import (
	"context"

	"github.com/shady2k/nocx/internal/snippet"
	"github.com/shady2k/nocx/internal/transport/control"
)

// SnippetService is the domain surface of the snippets library: the five
// list and mutation methods. It is a thin guard-bound wrapper over
// *snippet.Service: every method checks the operation's guard, so a service
// that escapes its callback fails with ErrOperationInactive instead of
// touching the store without the exclusion the operation exists to provide.
type SnippetService interface {
	List() ([]snippet.Snippet, error)
	Create(title, body string) (snippet.Snippet, error)
	Update(id, title, body string) (snippet.Snippet, error)
	Delete(id string) error
	Reorder(ids []string) ([]snippet.Snippet, error)
}

// SnippetOperation is the typed operation for the snippets.* methods. Its
// gates are [config]: the library is one document under the profile
// directory that backup/restore also writes, so a snippet mutation must
// conflict with a config-domain operation (a restore replacing the document
// under it) the way profiles.* and settings.* do. Snippets never touch the
// vault, so the vault gate is deliberately not held.
type SnippetOperation interface {
	AssistantOperation
	Run(context.Context, func(context.Context, SnippetService) error) error
}

// NewSnippetOperation builds a SnippetOperation that acquires configGate
// then the execution lane, and hands the callback a guard-bound snippet
// service.
func NewSnippetOperation(configGate, lane control.Admission, svc *snippet.Service) SnippetOperation {
	g := &guard{}
	return newOperation[SnippetService](Direct("SnippetOperation"), control.NewComposite(configGate, lane), g, newSnippetService(g, svc))
}

// newSnippetService builds the concrete guard-bound snippet service.
func newSnippetService(g *guard, svc *snippet.Service) *snippetService {
	return &snippetService{guard: g, svc: svc}
}

type snippetService struct {
	guard *guard
	svc   *snippet.Service
}

func (s *snippetService) check() error {
	if err := s.guard.check(); err != nil {
		return err
	}
	if s.svc == nil {
		return ErrOperationUnavailable
	}
	return nil
}

func (s *snippetService) List() ([]snippet.Snippet, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	return s.svc.List()
}

func (s *snippetService) Create(title, body string) (snippet.Snippet, error) {
	if err := s.check(); err != nil {
		return snippet.Snippet{}, err
	}
	return s.svc.Create(title, body)
}

func (s *snippetService) Update(id, title, body string) (snippet.Snippet, error) {
	if err := s.check(); err != nil {
		return snippet.Snippet{}, err
	}
	return s.svc.Update(id, title, body)
}

func (s *snippetService) Delete(id string) error {
	if err := s.check(); err != nil {
		return err
	}
	return s.svc.Delete(id)
}

func (s *snippetService) Reorder(ids []string) ([]snippet.Snippet, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	return s.svc.Reorder(ids)
}
