package transport

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Unit tests for completeLocalPath — the resolution rules, in isolation from
// the socket. The wire shape is pinned in ws_contract_test.go; these pin the
// semantics (what a partial path means, what a directory listing answers).

// completeLocalPath is the synchronous shape these tests assert against: the
// production caller passes the request's context so a withdrawn keystroke
// stops the walk, and no test here exercises that lifetime.
func completeLocalPath(text, cwd string, limit int) []fsCompleteEntry {
	entries, _ := completeLocalPathContext(context.Background(), text, cwd, limit)
	return entries
}

func writeFixture(t *testing.T, dir string, files []string, dirs []string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	for _, f := range files {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
}

func names(entries []fsCompleteEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

func TestCompleteLocalPath_RelativeToCwd(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, []string{"main.go", "main_test.go", "go.mod", "README.md"}, []string{"src", "pkg"})

	got := completeLocalPath("ma", dir, 10)
	want := []string{"main.go", "main_test.go"}
	if !reflect.DeepEqual(names(got), want) {
		t.Errorf("completeLocalPath(ma) = %v, want %v", names(got), want)
	}
	// Paths are absolute: the renderer uses them as stable ids and shows the
	// completed path, never a relative guess.
	if got[0].Path != filepath.Join(dir, "main.go") {
		t.Errorf("path = %q, want %q", got[0].Path, filepath.Join(dir, "main.go"))
	}
}

func TestCompleteLocalPath_DirectoryEntries(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, nil, []string{"src", "pkg"})

	got := completeLocalPath("src", dir, 10)
	if len(got) != 1 || got[0].Name != "src" || !got[0].IsDir {
		t.Errorf("completeLocalPath(src) = %+v, want one dir entry src", got)
	}

	// A trailing slash lists the directory's contents (empty prefix).
	writeFixture(t, filepath.Join(dir, "src"), []string{"a.go"}, nil)
	got = completeLocalPath("src/", dir, 10)
	if !reflect.DeepEqual(names(got), []string{"a.go"}) {
		t.Errorf("completeLocalPath(src/) = %v, want [a.go]", names(got))
	}
}

func TestCompleteLocalPath_Absolute(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, []string{"alpha.txt", "beta.txt"}, nil)

	got := completeLocalPath(filepath.Join(dir, "al"), "", 10)
	if !reflect.DeepEqual(names(got), []string{"alpha.txt"}) {
		t.Errorf("absolute completion = %v, want [alpha.txt]", names(got))
	}
}

func TestCompleteLocalPath_HiddenFilesFollowShellRule(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, []string{".gitignore", "visible.txt"}, nil)

	// A bare prefix never surfaces dotfiles.
	if got := completeLocalPath("v", dir, 10); len(got) != 1 || got[0].Name != "visible.txt" {
		t.Errorf("bare prefix listed %v, want only visible.txt", names(got))
	}
	// A prefix that itself starts with . does.
	got := completeLocalPath(".gi", dir, 10)
	if !reflect.DeepEqual(names(got), []string{".gitignore"}) {
		t.Errorf("dot prefix listed %v, want [.gitignore]", names(got))
	}
	// An empty prefix (trailing slash) lists everything visible, never hidden.
	got = completeLocalPath("./", dir, 10)
	if !reflect.DeepEqual(names(got), []string{"visible.txt"}) {
		t.Errorf("empty prefix listed %v, want [visible.txt]", names(got))
	}
}

func TestCompleteLocalPath_DotAndDotDotListTheDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, []string{"file.txt"}, []string{"sub"})
	writeFixture(t, filepath.Join(dir, "sub"), []string{"inner.txt"}, nil)

	// `cd sub` + Tab completes "sub" as a directory; `sub/` lists it. The
	// "." final segment lists the directory itself (empty prefix).
	got := completeLocalPath(".", dir, 10)
	if !reflect.DeepEqual(names(got), []string{"file.txt", "sub"}) {
		t.Errorf("completeLocalPath(.) = %v, want [file.txt sub]", names(got))
	}

	// `..` lists the parent directory.
	got = completeLocalPath("..", filepath.Join(dir, "sub"), 10)
	if !reflect.DeepEqual(names(got), []string{"file.txt", "sub"}) {
		t.Errorf("completeLocalPath(..) = %v, want [file.txt sub]", names(got))
	}
}

func TestCompleteLocalPath_NoCwdForRelative(t *testing.T) {
	// Relative text with no cwd answers nothing — the backend cannot know
	// where "src" points, and guessing would be the local-masquerade defect
	// §8.5 names.
	if got := completeLocalPath("src", "", 10); len(got) != 0 {
		t.Errorf("relative with no cwd = %v, want []", names(got))
	}
}

func TestCompleteLocalPath_MissingDirectoryIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if got := completeLocalPath("nope", dir, 10); len(got) != 0 {
		t.Errorf("missing dir = %v, want []", names(got))
	}
}

func TestCompleteLocalPath_LimitApplies(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a1", "a2", "a3", "a4", "a5"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}
	if got := completeLocalPath("a", dir, 2); len(got) != 2 {
		t.Errorf("limit 2 = %v, want 2 entries", names(got))
	}
}

func TestCompleteLocalPath_SortedOrder(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"zeta", "alpha", "mid"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}
	// "./" is the reachable empty-prefix shape (the handler refuses "").
	got := completeLocalPath("./", dir, 10)
	if !reflect.DeepEqual(names(got), []string{"alpha", "mid", "zeta"}) {
		t.Errorf("order = %v, want sorted [alpha mid zeta]", names(got))
	}
}

func TestCompleteLocalPath_SymlinkToDirectoryCompletesAsDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, nil, []string{"real"})
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got := completeLocalPath("l", dir, 10)
	if len(got) != 1 || got[0].Name != "link" {
		t.Fatalf("completeLocalPath(l) = %v, want one entry link", names(got))
	}
	// The dirent type of a symlink is not a directory; the target's is. The
	// dirs-only rule for cd/pushd/rmdir must see it as a directory or half a
	// home directory disappears from completion.
	if !got[0].IsDir {
		t.Errorf("symlink-to-dir IsDir = false, want true (completion follows the target)")
	}
}

func TestCompleteLocalPath_SymlinkToFileStaysAFile(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, []string{"target.txt"}, nil)
	if err := os.Symlink(filepath.Join(dir, "target.txt"), filepath.Join(dir, "file-link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got := completeLocalPath("file-l", dir, 10)
	if len(got) != 1 || got[0].Name != "file-link" {
		t.Fatalf("completeLocalPath(file-l) = %v, want one entry file-link", names(got))
	}
	if got[0].IsDir {
		t.Errorf("symlink-to-file IsDir = true, want false")
	}
}

func TestCompleteLocalPath_BrokenSymlinkKeepsDirentType(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "does-not-exist"), filepath.Join(dir, "dangling")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got := completeLocalPath("d", dir, 10)
	if len(got) != 1 || got[0].Name != "dangling" {
		t.Fatalf("completeLocalPath(d) = %v, want one entry dangling", names(got))
	}
	// The target cannot be stat'ed: the entry must not claim a directory it
	// is not.
	if got[0].IsDir {
		t.Errorf("broken symlink IsDir = true, want false")
	}
}
