package filesystem

// contained is the capability's containment predicate — the enforcement half
// of ADR-0020's path rule, and the one check that compares canonical
// identities rather than spelled strings. These are its arithmetic: what a
// filesystem root contains, and that admitting one has not turned every root
// into a universal one.

import "testing"

// TestContained_FilesystemRootContainsEveryPath is the first acceptance
// criterion of nocx-cd6vp. The run grant's path fence is "/" for every run
// the product mints (ADR-0028 decision 4, amended 2026-08-26), and the
// boundary test used to append a separator to it — asking whether a path
// starts with "//", which no canonical path does. The whole fence was
// therefore unsatisfiable and files.read, files.edit and files.create
// refused every file in the shipped app.
func TestContained_FilesystemRootContainsEveryPath(t *testing.T) {
	root := []string{"/"}
	for _, path := range []string{"/", "/etc/passwd", "/home/dev/x.txt", "/tmp"} {
		if !contained(path, root) {
			t.Fatalf("contained(%q, [\"/\"]) = false; the filesystem root contains every canonical path", path)
		}
	}
}

// TestContained_DirectoryRootKeepsItsSeparatorBoundary is the paired half:
// admitting "/" must not make containment universally true, which is the
// trap in this fix — with containment always true, exact() is the only
// remaining check and it matches a symlink's TARGET, so a row scoped to one
// directory would read a file outside it. An ordinary root still contains
// exactly itself and its descendants at a separator boundary.
func TestContained_DirectoryRootKeepsItsSeparatorBoundary(t *testing.T) {
	roots := []string{"/home/dev/project"}
	for _, path := range []string{"/home/dev/project", "/home/dev/project/a.txt", "/home/dev/project/sub/b.txt"} {
		if !contained(path, roots) {
			t.Fatalf("contained(%q, %v) = false, want true", path, roots)
		}
	}
	for _, path := range []string{"/home/dev", "/home/dev/project-other/a.txt", "/etc/passwd", "/"} {
		if contained(path, roots) {
			t.Fatalf("contained(%q, %v) = true; the root's boundary is a separator, not a prefix", path, roots)
		}
	}
}

// TestContained_NoRootsContainNothing is the empty-scope end: a capability
// built from a row that names no path has no authority at all, and "/" being
// admissible must not change that.
func TestContained_NoRootsContainNothing(t *testing.T) {
	for _, path := range []string{"/", "/etc/passwd"} {
		if contained(path, nil) {
			t.Fatalf("contained(%q, nil) = true; no roots is no authority", path)
		}
	}
}
