package filesystem

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/shady2k/nocx/internal/hashline"
)

// ScopedEditor is the narrowed capability for files.edit and files.create. It
// holds spelled grant roots and, for files.edit, exact file identities; for
// files.create, exact parent directories. Every operation acquires one
// short-lived Handle from the pane's own binding, canonicalizes the scope and
// the target on it, checks containment, and commits the bytes through the
// binding's conditional write — so the check and the install happen on the
// machine that owns the file, and the window between them is closed at the
// rename rather than in this process.
//
// It holds no provider (stage 10, ADR-anntnxdj §5), and construction performs
// no I/O: a capability is built before anybody has opened the Files panel.
type ScopedEditor struct {
	source       HandleSource
	roots        []string
	exactFiles   []string
	exactParents []string
	exactOnly    bool
	parentExact  bool
}

// NewScopedEditorWithExactFiles retains spelled grant roots for containment
// while authorizing only the exact file identities in files. A file cannot
// itself be a directory root, because a root would authorize descendants, so
// exact files are a separate scope kind.
func NewScopedEditorWithExactFiles(source HandleSource, roots, files []string) *ScopedEditor {
	if source == nil {
		return nil
	}
	return &ScopedEditor{
		source:     source,
		roots:      append([]string(nil), roots...),
		exactFiles: append([]string(nil), files...),
		exactOnly:  true,
	}
}

// NewScopedEditorWithExactParents retains spelled grant roots for containment
// while authorizing only the exact parent directories in parents. This is the
// files.create shape: the target does not exist yet, so its existing parent is
// the narrowest canonical scope available.
func NewScopedEditorWithExactParents(source HandleSource, roots, parents []string) *ScopedEditor {
	if source == nil {
		return nil
	}
	return &ScopedEditor{
		source:       source,
		roots:        append([]string(nil), roots...),
		exactParents: append([]string(nil), parents...),
		parentExact:  true,
	}
}

// acquire takes one handle and canonicalizes every spelled scope on it. The
// caller defers the release on every path after a nil error.
func (e *ScopedEditor) acquire(ctx context.Context) (Handle, func(), []string, []string, []string, error) {
	h, release, err := e.source.Acquire(ctx)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	roots, err := canonicalAll(ctx, h, e.roots)
	if err != nil {
		release()
		return nil, nil, nil, nil, nil, fmt.Errorf("filesystem: scope root: %w", err)
	}
	files, err := canonicalAll(ctx, h, e.exactFiles)
	if err != nil {
		release()
		return nil, nil, nil, nil, nil, fmt.Errorf("filesystem: exact file: %w", err)
	}
	parents, err := canonicalAll(ctx, h, e.exactParents)
	if err != nil {
		release()
		return nil, nil, nil, nil, nil, fmt.Errorf("filesystem: exact parent: %w", err)
	}
	return h, release, roots, files, parents, nil
}

// Edit applies a strict one-file patch after provider-backed containment. The
// read, the patch and the install all happen on the handle the call holds: the
// revision is checked against the bytes the provider returned, and the install
// is a Commit whose Expect carries that read's Version, so a file that changed
// while a person was answering the approval question is refused by the host
// that owns it (ErrChangedSinceRead) and nothing is installed.
func (e *ScopedEditor) Edit(ctx context.Context, path, revision, patch string) (hashline.Result, error) {
	h, release, roots, files, _, err := e.acquire(ctx)
	if err != nil {
		return hashline.Result{}, err
	}
	defer release()
	canonical, err := h.Canonical(ctx, path)
	if err != nil {
		return hashline.Result{}, err
	}
	if !e.allows(canonical, roots, files) {
		return hashline.Result{}, fmt.Errorf("%w: %s", ErrOutOfScope, path)
	}
	versioned, err := h.Versioned()
	if err != nil {
		return hashline.Result{}, err
	}
	data, version, err := versioned.ReadVersion(ctx, canonical, MaxReadBytes)
	if err != nil {
		return hashline.Result{}, err
	}
	newData, result, err := hashline.Apply(canonical, data, revision, patch)
	if err != nil {
		return hashline.Result{}, err
	}
	if _, commitErr := versioned.Commit(ctx, canonical, newData, Expect{Version: &version}); commitErr != nil {
		if err := e.afterCommit(ctx, versioned, canonical, newData, commitErr); err != nil {
			return hashline.Result{}, err
		}
	}
	result.Path = path
	return result, nil
}

// afterCommit decides what a failed commit means, and the one case it settles
// is the lost reply. A helper that installed the bytes and lost the response
// answers ErrUnknownWriteOutcome, and the outcome is decidable here because the
// caller knows exactly which bytes it meant to install: re-reading the file and
// finding those bytes means the write landed and the call succeeded; finding
// anything else — or failing to re-read at all — leaves the honest answer, that
// the outcome is unknown and the file must be read again.
//
// Nothing is written on this path, and the recovered success reports the
// revision of the bytes that were installed, which is the same revision the
// successful commit would have reported.
func (e *ScopedEditor) afterCommit(ctx context.Context, versioned VersionedFiles, canonical string, intended []byte, commitErr error) error {
	var unknown *ErrUnknownWriteOutcome
	if !errors.As(commitErr, &unknown) {
		return commitErr
	}
	installed, _, readErr := versioned.ReadVersion(ctx, canonical, MaxReadBytes)
	if readErr == nil && ContentDigest(installed) == ContentDigest(intended) {
		return nil
	}
	return commitErr
}

func (e *ScopedEditor) allows(canonical string, roots, files []string) bool {
	if e.exactOnly {
		return exact(canonical, files) && contained(canonical, roots)
	}
	return contained(canonical, roots)
}

// Create creates a file only beneath a canonicalized existing parent, which is
// the identity that can be authorized before the new object exists. The write
// is a conditional commit with Expect{Absent: true}, so a name that appeared
// between the check and the rename is refused by the host (ErrExists) rather
// than overwritten.
func (e *ScopedEditor) Create(ctx context.Context, path, content string) (hashline.Result, error) {
	h, release, roots, _, parents, err := e.acquire(ctx)
	if err != nil {
		return hashline.Result{}, err
	}
	defer release()
	parent, err := h.Canonical(ctx, filepath.Dir(path))
	if err != nil {
		return hashline.Result{}, err
	}
	if !e.allowsParent(parent, roots, parents) {
		return hashline.Result{}, fmt.Errorf("%w: %s", ErrOutOfScope, path)
	}
	target := filepath.Join(parent, filepath.Base(path))
	versioned, err := h.Versioned()
	if err != nil {
		return hashline.Result{}, err
	}
	data := []byte(content)
	// Content the line-addressed engine cannot express is refused here as it was
	// when this package wrote the file itself: a NUL byte or invalid UTF-8 is not
	// a text file, and creating one through a tool that speaks in lines would
	// hand the model a revision no later edit could use.
	if err := hashline.ValidateText(data); err != nil {
		return hashline.Result{}, err
	}
	if _, err := versioned.Commit(ctx, target, data, Expect{Absent: true}); err != nil {
		return hashline.Result{}, err
	}
	return hashline.Result{Path: path, Canonical: target, Revision: hashline.Revision(target, data)}, nil
}

func (e *ScopedEditor) allowsParent(canonical string, roots, parents []string) bool {
	if e.parentExact {
		return exact(canonical, parents) && contained(canonical, roots)
	}
	return contained(canonical, roots)
}

// ReadSnapshot reads through the same canonical scope guard as files.read,
// with the bytes and the version coming from the binding's whole-file read
// rather than from this process's disk. A file larger than MaxReadBytes is
// refused by the provider with its typed byte-limit error rather than cut: a
// revision names the file's complete bytes, and a revision of a prefix would
// describe bytes the caller never saw.
func (s *ScopedReader) ReadSnapshot(ctx context.Context, path string, maxBytes int64) (hashline.Snapshot, error) {
	h, release, roots, files, err := s.acquire(ctx)
	if err != nil {
		return hashline.Snapshot{}, err
	}
	defer release()
	canonical, err := h.Canonical(ctx, path)
	if err != nil {
		return hashline.Snapshot{}, err
	}
	if !s.allows(canonical, roots, files) {
		return hashline.Snapshot{}, fmt.Errorf("%w: %s", ErrOutOfScope, path)
	}
	versioned, err := h.Versioned()
	if err != nil {
		return hashline.Snapshot{}, err
	}
	data, _, err := versioned.ReadVersion(ctx, canonical, MaxReadBytes)
	if err != nil {
		return hashline.Snapshot{}, err
	}
	snapshot := hashline.Read(canonical, data, maxBytes)
	snapshot.Path = path
	return snapshot, nil
}
