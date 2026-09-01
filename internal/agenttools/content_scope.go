package agenttools

import "github.com/shady2k/nocx/internal/content"

// ContentScope is the authority half of a notes or snippets tool. It carries
// only ResourceContent scopes that survived grant intersection; the operation
// that performs the work is supplied separately by the assistant run seam.
// Keeping the scope here prevents an executor from receiving a raw domain
// service or inventing a second authorization rule.
type ContentScope struct {
	scopes []content.GrantScope
}

// NewContentScope builds the narrowed capability from the call resources that
// the grant permits. A root content scope intentionally contains every note
// and snippet; an item scope contains only that item.
func NewContentScope(resources []ResourceRef) *ContentScope {
	scopes := make([]content.GrantScope, 0, len(resources))
	for _, ref := range resources {
		if ref.Kind == content.ResourceContent && ref.ID != "" {
			scopes = append(scopes, content.GrantScope{Kind: ref.Kind, ID: ref.ID})
		}
	}
	return &ContentScope{scopes: scopes}
}

// Allows reports whether the exact canonical resource is inside this
// narrowed capability. It is the execution backstop for policy's decision:
// a sibling note or a snippet can never pass through a note-only scope.
func (s *ContentScope) Allows(id string) bool {
	if s == nil || id == "" {
		return false
	}
	child := content.GrantScope{Kind: content.ResourceContent, ID: id}
	for _, parent := range s.scopes {
		if parent.Contains(child) {
			return true
		}
	}
	return false
}

func narrowContent(grant content.Grant, resources []ResourceRef, _ RunContext) (Capability, error) {
	return NewContentScope(grantedResources(grant, resources)), nil
}

func contentRootResources(_ map[string]any, _ RunContext) ([]ResourceRef, error) {
	return []ResourceRef{{Kind: content.ResourceContent, ID: "content"}}, nil
}

func contentItemResource(arg, kind string) ResolveResources {
	return func(args map[string]any, _ RunContext) ([]ResourceRef, error) {
		id, ok := args[arg].(string)
		if !ok || id == "" {
			return nil, nil
		}
		return []ResourceRef{{Kind: content.ResourceContent, ID: kind + "/" + id}}, nil
	}
}

// skillResource resolves the skill named by the call into its content
// sub-scope. A skill is a ResourceContent sub-scope exactly as a note and a
// snippet are: the resource vocabulary is the ledger's closed set, and
// ResourceContent's hierarchy already expresses a grantable library.
func skillResource(arg string) ResolveResources {
	return func(args map[string]any, _ RunContext) ([]ResourceRef, error) {
		name, ok := args[arg].(string)
		if !ok || name == "" {
			return nil, nil
		}
		return []ResourceRef{{Kind: content.ResourceContent, ID: "skill/" + name}}, nil
	}
}
