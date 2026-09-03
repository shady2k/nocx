package filesystem_test

// The capability under the run grant's real path fence. ADR-0028 decision 4,
// as amended 2026-08-26, makes that fence "/" for every run: the narrowing a
// person expressed lives in the policy matrix, not in a second per-run fence.
// A capability built on that fence must therefore be able to express a call
// at all — which, before nocx-cd6vp, it could not: the containment check
// appended a separator to the root and asked whether the path began with
// "//".

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	filesystem "github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/hashline"
)

// TestScopedReader_FilesystemRootFenceReadsARealFile: a reader whose root is
// the whole filesystem returns the file's content, not ErrOutOfScope.
func TestScopedReader_FilesystemRootFenceReadsARealFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "real.txt")
	if err := os.WriteFile(path, []byte("real"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	s, err := filesystem.NewScopedReaderWithExactFiles(ctx, local.New(), []string{"/"}, []string{path})
	if err != nil {
		t.Fatalf("NewScopedReaderWithExactFiles: %v", err)
	}
	got, err := s.Read(ctx, path, 1<<20)
	if err != nil {
		t.Fatalf("read under a %q fence: %v", "/", err)
	}
	if got.Text != "real" {
		t.Fatalf("read under a %q fence = %q, want the file's content", "/", got.Text)
	}
}

// TestScopedEditor_FilesystemRootFenceEditsAndCreates: the same for the two
// mutation capabilities, which share the containment check.
func TestScopedEditor_FilesystemRootFenceEditsAndCreates(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(existing, []byte("before\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	editor, err := filesystem.NewScopedEditorWithExactFiles(ctx, local.New(), []string{"/"}, []string{existing})
	if err != nil {
		t.Fatalf("NewScopedEditorWithExactFiles: %v", err)
	}
	snapshot, err := hashline.Read(existing, 64<<10)
	if err != nil {
		t.Fatalf("hashline.Read: %v", err)
	}
	if _, editErr := editor.Edit(ctx, existing, snapshot.Revision, "PUT 1.=1:\n+after"); editErr != nil {
		t.Fatalf("edit under a %q fence: %v", "/", editErr)
	}

	target := filepath.Join(dir, "new.txt")
	creator, err := filesystem.NewScopedEditorWithExactParents(ctx, local.New(), []string{"/"}, []string{dir})
	if err != nil {
		t.Fatalf("NewScopedEditorWithExactParents: %v", err)
	}
	if _, createErr := creator.Create(ctx, target, "created\n"); createErr != nil {
		t.Fatalf("create under a %q fence: %v", "/", createErr)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("create under a %q fence wrote nothing: %v", "/", err)
	}
}
