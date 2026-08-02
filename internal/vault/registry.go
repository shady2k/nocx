package vault

import "fmt"

// Registry is the injected provider map. Dispatch goes through it — never a
// switch — which is what makes a new provider an addition rather than an edit
// (AD-8, spec §4.1).
type Registry struct {
	byID  map[ProviderID]Provider
	order []ProviderID
}

// NewRegistry validates tag legality and uniqueness at construction, so a
// collision is a startup failure rather than a silent overwrite that would
// route someone's secrets to the wrong store.
func NewRegistry(providers ...Provider) (*Registry, error) {
	r := &Registry{byID: make(map[ProviderID]Provider, len(providers))}
	for _, p := range providers {
		id := p.ID()
		if err := validProviderTag(id); err != nil {
			return nil, fmt.Errorf("register provider: %w", err)
		}
		if _, dup := r.byID[id]; dup {
			return nil, fmt.Errorf("register provider: duplicate id %q", id)
		}
		r.byID[id] = p
		r.order = append(r.order, id)
	}
	return r, nil
}

func (r *Registry) Get(id ProviderID) (Provider, bool) {
	p, ok := r.byID[id]
	return p, ok
}

func (r *Registry) Writable(id ProviderID) (WritableProvider, bool) {
	p, ok := r.byID[id]
	if !ok {
		return nil, false
	}
	w, ok := p.(WritableProvider)
	return w, ok
}

// List returns providers in registration order so the UI is stable.
func (r *Registry) List() []Provider {
	out := make([]Provider, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.byID[id])
	}
	return out
}
