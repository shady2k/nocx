package snippet

import (
	"errors"
	"sync"
)

var (
	ErrNotFound        = errors.New("snippet not found")
	ErrNotAPermutation = errors.New("reorder ids are not a permutation of the stored list")
)

// Service owns the policy over the store: ids are minted here, a reorder must
// be a permutation, and seeding happens exactly once.
type Service struct {
	store Store
	newID func() string
	mu    sync.Mutex
}

// NewService takes its id source rather than calling crypto/rand directly:
// an injected generator gives the collision case a test, and gives a
// generation failure somewhere to go other than a panic.
func NewService(store Store, newID func() string) *Service {
	return &Service{store: store, newID: newID}
}

// seeds are two ordinary records written when the document is first created.
// They are not built-ins: no override layer, no restore, no reset. Their only
// job is to teach the placeholder syntax at the moment the library would
// otherwise be empty (design §5.3).
//
// EVERY SEED MUST FIRE IN AN ORDINARY LOCAL PANE. The first pair did not:
// one asked for {{env:branch}}, which is null until the pane has a git
// binding, and the other for {{env:host}}, which is the ssh user's host and
// is empty on a local shell by definition — so both refused, every time, for
// anybody whose first act was to try one (owner review). A first example
// that cannot run teaches the opposite of what a seed is for.
//
// The rule that keeps this true: a seed may use {{env:cwd}} — a session
// always has a working directory — and PARAMETERS, which the person answers.
// It may not use an env key that depends on where the pane happens to be
// pointed.
//
// A parameter is written {{name}} or {{name=default}}. It used to be
// {{ask:name}}, and that spelling was retired when a colon became what
// decides who owns a span (nocx-9xu1j) — so the second seed below shipped a
// body that inserted its own placeholders verbatim until it was re-spelled.
// A seed is data, so nothing but TestSeedsUseNoRetiredSyntax can catch that.
func (s *Service) seeds() []Snippet {
	return []Snippet{
		{
			ID:    s.newID(),
			Title: "Explain this project",
			Body:  "Explain what the project in {{env:cwd}} does, and how it is laid out.",
		},
		{
			ID:    s.newID(),
			Title: "Forward a port over ssh",
			Body:  "ssh -L {{local=8080}}:localhost:{{remote=8080}} {{host}}",
		},
	}
}

// ensureSeededLocked writes the seed records if the document has never
// existed. Caller holds s.mu.
func (s *Service) ensureSeededLocked() error {
	exists, err := s.store.Exists()
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return s.store.SaveAll(s.seeds())
}

func (s *Service) List() ([]Snippet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureSeededLocked(); err != nil {
		return nil, err
	}
	return s.store.LoadAll()
}

func (s *Service) Create(title, body string) (Snippet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureSeededLocked(); err != nil {
		return Snippet{}, err
	}
	list, err := s.store.LoadAll()
	if err != nil {
		return Snippet{}, err
	}
	created := Snippet{ID: s.newID(), Title: title, Body: body}
	if err := s.store.SaveAll(append(list, created)); err != nil {
		return Snippet{}, err
	}
	return created, nil
}

func (s *Service) Update(id, title, body string) (Snippet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.store.LoadAll()
	if err != nil {
		return Snippet{}, err
	}
	for i := range list {
		if list[i].ID != id {
			continue
		}
		list[i].Title, list[i].Body = title, body
		if err := s.store.SaveAll(list); err != nil {
			return Snippet{}, err
		}
		return list[i], nil
	}
	return Snippet{}, ErrNotFound
}

func (s *Service) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.store.LoadAll()
	if err != nil {
		return err
	}
	out := make([]Snippet, 0, len(list))
	found := false
	for _, sn := range list {
		if sn.ID == id {
			found = true
			continue
		}
		out = append(out, sn)
	}
	if !found {
		return ErrNotFound
	}
	return s.store.SaveAll(out)
}

// Reorder takes the FULL id list and rejects anything that is not a
// permutation of what is stored. The whole check runs before any write, so a
// rejected reorder leaves the document byte-identical rather than
// half-applied — a partial reorder is how two clients silently drop a record.
func (s *Service) Reorder(ids []string) ([]Snippet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.store.LoadAll()
	if err != nil {
		return nil, err
	}
	if len(ids) != len(list) {
		return nil, ErrNotAPermutation
	}
	byID := make(map[string]Snippet, len(list))
	for _, sn := range list {
		byID[sn.ID] = sn
	}
	out := make([]Snippet, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		sn, ok := byID[id]
		if !ok {
			return nil, ErrNotAPermutation
		}
		if _, dup := seen[id]; dup {
			return nil, ErrNotAPermutation
		}
		seen[id] = struct{}{}
		out = append(out, sn)
	}
	if err := s.store.SaveAll(out); err != nil {
		return nil, err
	}
	return out, nil
}
