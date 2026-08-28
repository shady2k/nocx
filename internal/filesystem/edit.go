package filesystem

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/shady2k/nocx/internal/hashline"
)

// ScopedEditor is the narrowed capability for files.edit and files.create. It
// holds provider-canonical roots and, for files.edit, exact canonical file
// identities. It checks the requested object before handing it to the
// byte-exact hashline engine.
type ScopedEditor struct {
	provider     Provider
	roots        []string
	exactFiles   []string
	exactParents []string
	exactOnly    bool
	parentExact  bool
}

// NewScopedEditor builds a mutating capability over exactly the given roots.
// Roots are canonicalized at construction, just like ScopedReader.
func NewScopedEditor(ctx context.Context, p Provider, roots []string) (*ScopedEditor, error) {
	return newScopedEditor(ctx, p, roots, nil, nil, false, false)
}

// NewScopedEditorWithExactFiles retains canonical grant roots for provider
// containment while authorizing only the canonical file identities in files.
// A file cannot itself be a directory root, because a root would authorize
// descendants, so exact files are a separate scope kind.
func NewScopedEditorWithExactFiles(ctx context.Context, p Provider, roots, files []string) (*ScopedEditor, error) {
	return newScopedEditor(ctx, p, roots, files, nil, true, false)
}

// NewScopedEditorWithExactParents retains canonical grant roots for provider
// containment while authorizing only the canonical parent directories in
// parents. This is the files.create shape: the target does not exist yet, so
// its existing parent is the narrowest canonical scope available.
func NewScopedEditorWithExactParents(ctx context.Context, p Provider, roots, parents []string) (*ScopedEditor, error) {
	return newScopedEditor(ctx, p, roots, nil, parents, false, true)
}

func newScopedEditor(ctx context.Context, p Provider, roots, files, parents []string, exactOnly, parentExact bool) (*ScopedEditor, error) {
	e := &ScopedEditor{provider: p, exactOnly: exactOnly, parentExact: parentExact}
	for _, root := range roots {
		canonical, err := p.Canonical(ctx, root)
		if err != nil {
			return nil, fmt.Errorf("filesystem: scope root %q: %w", root, err)
		}
		e.roots = append(e.roots, canonical)
	}
	for _, file := range files {
		canonical, err := p.Canonical(ctx, file)
		if err != nil {
			return nil, fmt.Errorf("filesystem: exact file %q: %w", file, err)
		}
		e.exactFiles = append(e.exactFiles, canonical)
	}
	for _, parent := range parents {
		canonical, err := p.Canonical(ctx, parent)
		if err != nil {
			return nil, fmt.Errorf("filesystem: exact parent %q: %w", parent, err)
		}
		e.exactParents = append(e.exactParents, canonical)
	}
	return e, nil
}

// Edit applies a strict one-file patch after provider-backed containment.
func (e *ScopedEditor) Edit(ctx context.Context, path, revision, patch string) (hashline.Result, error) {
	canonical, err := e.provider.Canonical(ctx, path)
	if err != nil {
		return hashline.Result{}, err
	}
	if !e.allows(canonical) {
		return hashline.Result{}, fmt.Errorf("%w: %s", ErrOutOfScope, path)
	}
	return hashline.Apply(canonical, revision, patch)
}

func (e *ScopedEditor) allows(canonical string) bool {
	if e.exactOnly {
		return exact(canonical, e.exactFiles) && contained(canonical, e.roots)
	}
	return contained(canonical, e.roots)
}

// Create creates a file only beneath a canonicalized existing parent, which
// is the identity that can be authorized before the new object exists.
func (e *ScopedEditor) Create(ctx context.Context, path, content string) (hashline.Result, error) {
	parent, err := e.provider.Canonical(ctx, filepath.Dir(path))
	if err != nil {
		return hashline.Result{}, err
	}
	if !e.allowsParent(parent) {
		return hashline.Result{}, fmt.Errorf("%w: %s", ErrOutOfScope, path)
	}
	return hashline.Create(filepath.Join(parent, filepath.Base(path)), content)
}

func (e *ScopedEditor) allowsParent(canonical string) bool {
	if e.parentExact {
		return exact(canonical, e.exactParents) && contained(canonical, e.roots)
	}
	return contained(canonical, e.roots)
}

// ReadSnapshot reads through the same canonical scope guard as Read while
// preserving the exact bytes used to mint the revision.
func (s *ScopedReader) ReadSnapshot(ctx context.Context, path string, maxBytes int64) (hashline.Snapshot, error) {
	canonical, err := s.provider.Canonical(ctx, path)
	if err != nil {
		return hashline.Snapshot{}, err
	}
	if !s.allows(canonical) {
		return hashline.Snapshot{}, fmt.Errorf("%w: %s", ErrOutOfScope, path)
	}
	snapshot, err := hashline.Read(canonical, maxBytes)
	if err != nil {
		return hashline.Snapshot{}, err
	}
	snapshot.Path = path
	return snapshot, nil
}
