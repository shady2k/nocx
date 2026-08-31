package agenttools

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
)

// narrowFilesRead is the files.read row's capability constructor. It receives
// the resources resolved from this call and keeps only the path identities
// that are also in the grant; other grant paths are not authority for this
// call.
func narrowFilesRead(grant content.Grant, resources []ResourceRef, _ RunContext) (Capability, error) {
	paths := resourceIDs(grant, resources, content.ResourcePath)
	r, err := filesystem.NewScopedReaderWithExactFiles(context.Background(), local.New(), filesystemRoots(grant), paths)
	if err != nil {
		return nil, fmt.Errorf("narrow files.read: %w", err)
	}
	return r, nil
}

func filesystemRoots(grant content.Grant) []string {
	roots := make([]string, 0, len(grant.Scopes))
	for _, scope := range grant.Scopes {
		if scope.Kind == content.ResourcePath {
			roots = append(roots, scope.ID)
		}
	}
	return roots
}

func narrowFilesEdit(grant content.Grant, resources []ResourceRef, _ RunContext) (Capability, error) {
	paths := resourceIDs(grant, resources, content.ResourcePath)
	editor, err := filesystem.NewScopedEditorWithExactFiles(context.Background(), local.New(), filesystemRoots(grant), paths)
	if err != nil {
		return nil, fmt.Errorf("narrow files.edit: %w", err)
	}
	return editor, nil
}

func narrowFilesCreate(grant content.Grant, resources []ResourceRef, _ RunContext) (Capability, error) {
	// A new target cannot itself be canonicalized until it exists. Bind the
	// capability to the existing parent directories of the resolved targets;
	// ScopedEditor.Create canonicalizes that parent before writing.
	paths := resourceIDs(grant, resources, content.ResourcePath)
	parents := make([]string, 0, len(paths))
	for _, path := range paths {
		parents = append(parents, filepath.Dir(path))
	}
	editor, err := filesystem.NewScopedEditorWithExactParents(context.Background(), local.New(), filesystemRoots(grant), parents)
	if err != nil {
		return nil, fmt.Errorf("narrow files.create: %w", err)
	}
	return editor, nil
}

// narrowSession is the session.list and session.read constructor. The
// capability carries only the resolved session identities that the grant also
// permits.
func narrowSession(grant content.Grant, resources []ResourceRef, _ RunContext) (Capability, error) {
	scoped := grantedResources(grant, resources)
	scopes := make([]content.GrantScope, 0, len(scoped))
	for _, ref := range scoped {
		if ref.Kind == content.ResourceSession {
			scopes = append(scopes, content.GrantScope{Kind: ref.Kind, ID: ref.ID})
		}
	}
	return NewSessionReader(scopes), nil
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
