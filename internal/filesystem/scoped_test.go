package filesystem_test

// The narrowed-capability tests (ADR-0028 decision 4, design §5): a tool
// holds a capability scoped to path set A and CANNOT reach a path outside it
// — asserted by TRYING, not by inspecting. The scope is the provider-canonical
// identity, so a symlink inside the scope that resolves outside is refused
// too: the identity is what the check compares.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	filesystem "github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
)

func scopeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a"), 0o700); err != nil {
		t.Fatalf("mkdir a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "in.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatalf("write in.txt: %v", err)
	}
	// A sibling outside the scope.
	if err := os.MkdirAll(filepath.Join(dir, "b"), 0o700); err != nil {
		t.Fatalf("mkdir b: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b", "out.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatalf("write out.txt: %v", err)
	}
	return dir
}

// TestScopedReader_CannotReachOutsideScope is acceptance criterion 3: a
// capability scoped to path set A cannot reach a path outside it — asserted
// by trying, never by inspecting. The in-scope read works; the sibling
// outside the scope is refused before the provider is touched.
func TestScopedReader_CannotReachOutsideScope(t *testing.T) {
	dir := scopeDir(t)
	ctx := context.Background()

	s, err := filesystem.NewScopedReader(ctx, local.New(), []string{filepath.Join(dir, "a")})
	if err != nil {
		t.Fatalf("NewScopedReader: %v", err)
	}

	// In scope: the read returns the file.
	c, err := s.Read(ctx, filepath.Join(dir, "a", "in.txt"), 1<<20)
	if err != nil {
		t.Fatalf("in-scope read: %v", err)
	}
	if c.Text != "inside" || c.Total != 6 {
		t.Fatalf("in-scope content = %+v, want the file", c)
	}

	// Outside the scope: refused with ErrOutOfScope.
	_, err = s.Read(ctx, filepath.Join(dir, "b", "out.txt"), 1<<20)
	if !errors.Is(err, filesystem.ErrOutOfScope) {
		t.Fatalf("out-of-scope read error = %v, want ErrOutOfScope", err)
	}
	// A path that merely PREFIXES the scope is still outside: /grant-other
	// is not under /grant.
	if mkdirErr := os.MkdirAll(filepath.Join(dir, "a-other"), 0o700); mkdirErr != nil {
		t.Fatalf("mkdir a-other: %v", mkdirErr)
	}
	if writeErr := os.WriteFile(filepath.Join(dir, "a-other", "x.txt"), []byte("x"), 0o600); writeErr != nil {
		t.Fatalf("write x.txt: %v", writeErr)
	}
	_, err = s.Read(ctx, filepath.Join(dir, "a-other", "x.txt"), 1<<20)
	if !errors.Is(err, filesystem.ErrOutOfScope) {
		t.Fatalf("prefix-sibling read error = %v, want ErrOutOfScope (the scope is a separator boundary)", err)
	}
}

// TestScopedReader_SymlinkEscapeIsRefused is the identity half of the same
// criterion: a symlink INSIDE the scope that resolves OUTSIDE it is refused,
// because the scope is the canonical identity, not the spelled path.
func TestScopedReader_SymlinkEscapeIsRefused(t *testing.T) {
	dir := scopeDir(t)
	ctx := context.Background()
	if err := os.Symlink(filepath.Join(dir, "b", "out.txt"), filepath.Join(dir, "a", "escape.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	s, err := filesystem.NewScopedReader(ctx, local.New(), []string{filepath.Join(dir, "a")})
	if err != nil {
		t.Fatalf("NewScopedReader: %v", err)
	}
	_, err = s.Read(ctx, filepath.Join(dir, "a", "escape.txt"), 1<<20)
	if !errors.Is(err, filesystem.ErrOutOfScope) {
		t.Fatalf("symlink-escape read error = %v, want ErrOutOfScope", err)
	}
}

// TestScopedReader_ZeroRootsRefuseEverything is the empty-grant shape: a
// capability with no scope cannot express any call.
func TestScopedReader_ZeroRootsRefuseEverything(t *testing.T) {
	dir := scopeDir(t)
	s, err := filesystem.NewScopedReader(context.Background(), local.New(), nil)
	if err != nil {
		t.Fatalf("NewScopedReader: %v", err)
	}
	_, err = s.Read(context.Background(), filepath.Join(dir, "a", "in.txt"), 1<<20)
	if !errors.Is(err, filesystem.ErrOutOfScope) {
		t.Fatalf("zero-root read error = %v, want ErrOutOfScope", err)
	}
}

// TestScopedReader_UnknowableRootFailsConstruction is the other end of the
// construction interval: a scope whose identity cannot be resolved must not
// silently become a wider or narrower scope — the constructor refuses.
func TestScopedReader_UnknowableRootFailsConstruction(t *testing.T) {
	if _, err := filesystem.NewScopedReader(context.Background(), local.New(), []string{filepath.Join(t.TempDir(), "does-not-exist")}); err == nil {
		t.Fatal("NewScopedReader accepted an unknowable root")
	}
}
