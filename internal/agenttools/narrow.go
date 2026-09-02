package agenttools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
)

// The effect ROW each general-purpose file tool's capability is bounded by.
// The row is where a person expressed which paths that class of call may
// touch (content.EffectPolicy: one row per effect, each carrying its own
// scopes), so the row — never the grant's derived fence union — is what
// filesystemRoots reads.
//
// These name the same effect as the tool's declaration row in registry.go.
// They are restated here rather than read back from `declarations` because
// that table's initializer names these constructors, and a constructor
// reading the table would be an initialization cycle;
// TestFileToolNarrowEffectsMatchDeclarations asserts the two agree, so the
// restatement cannot drift silently.
const (
	filesReadRowEffect  = content.EffectObserve
	filesWriteRowEffect = content.EffectMutateReversible
)

// narrowFilesRead is the files.read row's capability constructor. It receives
// the resources resolved from this call and keeps only the path identities
// that are also in the grant; other grant paths are not authority for this
// call.
func narrowFilesRead(grant content.Grant, resources []ResourceRef, runCtx RunContext) (Capability, error) {
	return narrowFilesReadWithSkillRoots(nil)(grant, resources, runCtx)
}

func narrowFilesReadWithSkillRoots(skillRoots []string) Narrow {
	skillRoots = cloneSkillRoots(skillRoots)
	return func(grant content.Grant, resources []ResourceRef, _ RunContext) (Capability, error) {
		paths := resourceIDs(grant, resources, content.ResourcePath)
		if err := refuseSkillPath("files.read", paths, skillRoots, false); err != nil {
			return nil, err
		}
		r, err := filesystem.NewScopedReaderWithExactFiles(context.Background(), local.New(), filesystemRoots(grant, filesReadRowEffect), paths)
		if err != nil {
			return nil, fmt.Errorf("narrow files.read: %w", err)
		}
		return r, nil
	}
}

// filesystemRoots is the containment scope the narrowed capability holds for
// one effect: the path ids of THAT effect row's scopes — the selector a
// person wrote, already materialized against the run fence at mint time
// (content.EffectPolicy.WithRunScopes).
//
// It is deliberately NOT grant.Scopes. That union is the run fence's
// DECLARATION coverage (see content.EffectPolicy.AsGrant), and since
// ADR-0028 decision 4's amendment of 2026-08-26 its path member is "/" for
// every run the product mints. Reading it therefore discarded whatever
// narrowing the operator expressed and handed the capability either the
// whole filesystem or — because a "/" root satisfied no containment test —
// nothing at all (nocx-cd6vp). Reading the row restores the narrowing, and
// it is what gives contained() something real to canonicalize against: a row
// scoped to one directory refuses a symlink out of it by canonical identity,
// which the lexical policy predicate cannot do.
func filesystemRoots(grant content.Grant, effect content.Effect) []string {
	scopes := grant.Policy.RowScopes(effect)
	roots := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if scope.Kind == content.ResourcePath {
			roots = append(roots, scope.ID)
		}
	}
	return roots
}

func narrowFilesEdit(grant content.Grant, resources []ResourceRef, runCtx RunContext) (Capability, error) {
	return narrowFilesEditWithSkillRoots(nil)(grant, resources, runCtx)
}

func narrowFilesEditWithSkillRoots(skillRoots []string) Narrow {
	skillRoots = cloneSkillRoots(skillRoots)
	return func(grant content.Grant, resources []ResourceRef, _ RunContext) (Capability, error) {
		paths := resourceIDs(grant, resources, content.ResourcePath)
		if err := refuseSkillPath("files.edit", paths, skillRoots, false); err != nil {
			return nil, err
		}
		editor, err := filesystem.NewScopedEditorWithExactFiles(context.Background(), local.New(), filesystemRoots(grant, filesWriteRowEffect), paths)
		if err != nil {
			return nil, fmt.Errorf("narrow files.edit: %w", err)
		}
		return editor, nil
	}
}

func narrowFilesCreate(grant content.Grant, resources []ResourceRef, runCtx RunContext) (Capability, error) {
	return narrowFilesCreateWithSkillRoots(nil)(grant, resources, runCtx)
}

func narrowFilesCreateWithSkillRoots(skillRoots []string) Narrow {
	skillRoots = cloneSkillRoots(skillRoots)
	return func(grant content.Grant, resources []ResourceRef, _ RunContext) (Capability, error) {
		// A new target cannot itself be canonicalized until it exists. Bind the
		// capability to the existing parent directories of the resolved targets;
		// ScopedEditor.Create canonicalizes that parent before writing.
		paths := resourceIDs(grant, resources, content.ResourcePath)
		if err := refuseSkillPath("files.create", paths, skillRoots, true); err != nil {
			return nil, err
		}
		parents := make([]string, 0, len(paths))
		for _, path := range paths {
			parents = append(parents, filepath.Dir(path))
		}
		editor, err := filesystem.NewScopedEditorWithExactParents(context.Background(), local.New(), filesystemRoots(grant, filesWriteRowEffect), parents)
		if err != nil {
			return nil, fmt.Errorf("narrow files.create: %w", err)
		}
		return editor, nil
	}
}

func cloneSkillRoots(roots []string) []string {
	return append([]string(nil), roots...)
}

func refuseSkillPath(tool string, paths, skillRoots []string, create bool) error {
	if len(skillRoots) == 0 {
		return nil
	}
	provider := local.New()
	canonicalRoots := make([]string, 0, len(skillRoots))
	for _, root := range skillRoots {
		root = filepath.Clean(root)
		if root == "." || !filepath.IsAbs(root) {
			continue
		}
		canonicalRoots = append(canonicalRoots, root)
		if canonical, err := provider.Canonical(context.Background(), root); err == nil {
			canonicalRoots = append(canonicalRoots, canonical)
		}
	}
	for _, path := range paths {
		candidates := []string{filepath.Clean(path)}
		if create {
			if canonicalParent, err := provider.Canonical(context.Background(), filepath.Dir(path)); err == nil {
				candidates = append(candidates, filepath.Join(canonicalParent, filepath.Base(path)))
			}
		} else if canonical, err := provider.Canonical(context.Background(), path); err == nil {
			candidates = append(candidates, canonical)
		}
		for _, candidate := range candidates {
			for _, root := range canonicalRoots {
				if pathWithin(root, candidate) {
					return fmt.Errorf("%s cannot access skill roots; use %s", tool, skillAlternative(tool))
				}
			}
		}
	}
	return nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func skillAlternative(tool string) string {
	switch tool {
	case "files.read":
		return "skills.read"
	case "files.edit":
		return "skills.update"
	default:
		return "skills.create"
	}
}

// narrowSession is the session.list and session.read constructor. The
// capability carries only the resolved session identities that the grant also
// permits.
func narrowSession(grant content.Grant, resources []ResourceRef, runCtx RunContext) (Capability, error) {
	scoped := grantedResources(grant, resources)
	scopes := make([]content.GrantScope, 0, len(scoped))
	for _, ref := range scoped {
		if ref.Kind == content.ResourceSession {
			scopes = append(scopes, content.GrantScope{Kind: ref.Kind, ID: ref.ID})
		}
	}
	return NewSessionReader(scopes, runCtx.AutomaticSessionItems, runCtx.MarkedSessionWindows), nil
}

func narrowURL(grant content.Grant, resources []ResourceRef, _ RunContext) (Capability, error) {
	scoped := grantedResources(grant, resources)
	urls := make([]string, 0, len(scoped))
	for _, ref := range scoped {
		if ref.Kind == content.ResourceDestination {
			urls = append(urls, ref.ID)
		}
	}
	return &URLScope{URLs: urls}, nil
}

// narrowRun is the run row's capability constructor. It carries only the
// resolved session identities that the grant also permits.
func narrowRun(grant content.Grant, resources []ResourceRef, _ RunContext) (Capability, error) {
	scoped := grantedResources(grant, resources)
	scopes := make([]content.GrantScope, 0, len(scoped))
	for _, ref := range scoped {
		if ref.Kind == content.ResourceSession {
			scopes = append(scopes, content.GrantScope{Kind: ref.Kind, ID: ref.ID})
		}
	}
	return NewRunner(scopes), nil
}

// narrowRunWait is the session.wait row's capability constructor. Same
// authority as narrowRun — the right to keep waiting on a command travels
// with the right to have started it — in the capability type the renderer
// dispatch switch distinguishes it by.
func narrowRunWait(grant content.Grant, resources []ResourceRef, _ RunContext) (Capability, error) {
	scoped := grantedResources(grant, resources)
	scopes := make([]content.GrantScope, 0, len(scoped))
	for _, ref := range scoped {
		if ref.Kind == content.ResourceSession {
			scopes = append(scopes, content.GrantScope{Kind: ref.Kind, ID: ref.ID})
		}
	}
	return NewRunWatcher(scopes), nil
}
