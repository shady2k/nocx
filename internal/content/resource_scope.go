package content

import (
	"fmt"
	"os"
	"strings"
	"unicode"
)

// ValidateGrantScope validates both the resource kind and its canonical id.
// It is the shared boundary for policy parsing and capability narrowing: a
// scope that cannot be represented canonically cannot be enforced safely.
func ValidateGrantScope(scope GrantScope) error {
	if !validResourceKind(scope.Kind) {
		return fmt.Errorf("resource scope: kind %q is not a resource kind", scope.Kind)
	}
	if scope.ID == "" {
		return fmt.Errorf("resource scope: %s has an empty id", scope.Kind)
	}

	switch scope.Kind {
	case ResourcePath:
		if !isAbsolutePath(scope.ID) {
			return fmt.Errorf("resource scope: path %q is not absolute", scope.ID)
		}
	case ResourceContent:
		if err := validateContentID(scope.ID); err != nil {
			return fmt.Errorf("resource scope: content id %q: %w", scope.ID, err)
		}
	case ResourceWorkspace:
		if err := validateWorkspaceID(scope.ID); err != nil {
			return fmt.Errorf("resource scope: workspace id %q: %w", scope.ID, err)
		}
	}
	return nil
}

// Contains reports whether the child resource is inside the parent scope.
// Different ResourceKinds never contain one another. Content and workspace
// use their canonical sub-scope hierarchy; existing singleton resources use
// exact identity, while paths retain lexical, segment-aware containment.
//
// This is deliberately a policy-time predicate over recorded scope IDs. For
// ResourcePath it performs no provider canonicalization and is NEVER a
// filesystem authorization check: callers authorizing a filesystem read must
// use the provider-backed capability in internal/filesystem/scoped.go,
// specifically its contained check, which defeats symlink escapes.
func (scope GrantScope) Contains(child GrantScope) bool {
	if ValidateGrantScope(scope) != nil || ValidateGrantScope(child) != nil || scope.Kind != child.Kind {
		return false
	}

	switch scope.Kind {
	case ResourcePath:
		return pathScopeContains(scope.ID, child.ID)
	case ResourceContent:
		parent, _ := parseContentID(scope.ID)
		candidate, _ := parseContentID(child.ID)
		if parent.kind == "content" {
			return true
		}
		if parent.kind != candidate.kind {
			return false
		}
		// Family roots revise the deliberate former hierarchy, which had no
		// "every note" scope. Positive note+snippet roots are required to
		// express a skills-off grant without the universal content root.
		return parent.id == "" || parent.id == candidate.id
	case ResourceWorkspace:
		parent, _ := parseWorkspaceID(scope.ID)
		candidate, _ := parseWorkspaceID(child.ID)
		if parent.workspace != candidate.workspace || parent.depth > candidate.depth {
			return false
		}
		if parent.depth == 1 {
			return true
		}
		if parent.tab != candidate.tab {
			return false
		}
		if parent.depth == 2 {
			return true
		}
		return parent.pane == candidate.pane
	default:
		return scope.ID == child.ID
	}
}

type contentResourceID struct {
	kind string
	id   string
}

func parseContentID(id string) (contentResourceID, bool) {
	switch id {
	case "content":
		return contentResourceID{kind: "content"}, true
	case "note", "snippet", "skill":
		return contentResourceID{kind: id}, true
	}
	parts := strings.Split(id, "/")
	if len(parts) != 2 || (parts[0] != "note" && parts[0] != "snippet" && parts[0] != "skill") || !validResourceAtom(parts[1]) {
		return contentResourceID{}, false
	}
	return contentResourceID{kind: parts[0], id: parts[1]}, true
}

func validateContentID(id string) error {
	if _, ok := parseContentID(id); !ok {
		return fmt.Errorf("want content, note, snippet, skill, note/<id>, snippet/<id> or skill/<name>")
	}
	return nil
}

type workspaceResourceID struct {
	workspace string
	tab       string
	pane      string
	depth     int
}

func parseWorkspaceID(id string) (workspaceResourceID, bool) {
	parts := strings.Split(id, "/")
	if len(parts) != 2 && len(parts) != 4 && len(parts) != 6 {
		return workspaceResourceID{}, false
	}
	if parts[0] != "workspace" || !validResourceAtom(parts[1]) {
		return workspaceResourceID{}, false
	}
	out := workspaceResourceID{workspace: parts[1], depth: len(parts) / 2}
	if len(parts) == 2 {
		return out, true
	}
	if parts[2] != "tab" || !validResourceAtom(parts[3]) {
		return workspaceResourceID{}, false
	}
	out.tab = parts[3]
	if len(parts) == 4 {
		return out, true
	}
	if parts[4] != "pane" || !validResourceAtom(parts[5]) {
		return workspaceResourceID{}, false
	}
	out.pane = parts[5]
	return out, true
}

func validateWorkspaceID(id string) error {
	if _, ok := parseWorkspaceID(id); !ok {
		return fmt.Errorf("want workspace/<id>, workspace/<id>/tab/<id> or workspace/<id>/tab/<id>/pane/<id>")
	}
	return nil
}

func validResourceAtom(s string) bool {
	if s == "" || s == "." || s == ".." || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '/' {
			return false
		}
	}
	return true
}

// pathScopeContains is intentionally the policy-time lexical check, not the
// execution-time filesystem check. It has no provider or context, so it
// cannot canonicalize symlinks. The enforcement check is
// internal/filesystem/scoped.go's provider-backed contained function; this
// predicate MUST NOT authorize a filesystem read.
func pathScopeContains(parent, child string) bool {
	if parent == "/" {
		return strings.HasPrefix(child, "/")
	}
	if child == parent {
		return true
	}
	for _, sep := range pathScopeSeparators() {
		if strings.HasPrefix(child, parent+string(sep)) {
			return true
		}
	}
	return false
}

func pathScopeSeparators() []byte {
	if os.PathSeparator == '/' {
		return []byte{'/'}
	}
	return []byte{byte(os.PathSeparator), '/'}
}
