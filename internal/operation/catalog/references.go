package catalog

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// UnreferencedHandles is the static check behind "every declaration is live".
// Construction can prove a registered method has a declaration; it cannot
// prove an arbitrary function is a live caller, so the second direction is a
// source check: every declaration's generated handle must appear in a
// production Go file outside this package.
//
// It is a pure function over source text so a planted violation is a test, not
// an edit to the tree.
func UnreferencedHandles(decls []Declaration, sources map[string]string) []ID {
	var all strings.Builder
	for _, src := range sources {
		all.WriteString(src)
		all.WriteByte('\n')
	}
	haystack := all.String()
	var missing []ID
	for _, d := range decls {
		needle := "catalog." + HandleName(d.ID)
		if !strings.Contains(haystack, needle) {
			missing = append(missing, d.ID)
		}
	}
	return missing
}

// CheckHandleReferences reads the repository's production Go sources and
// reports every declaration whose handle no file names. The catalogue package
// itself is skipped: a handle mentioned only by its own generated definition
// is not a caller.
func CheckHandleReferences(root string) ([]ID, error) {
	sources := map[string]string{}
	for _, dir := range []string{"internal", "cmd", "main.go"} {
		path := filepath.Join(root, dir)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		err := filepath.WalkDir(path, func(p string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if p == filepath.Join(root, "internal/operation/catalog") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(p) //nolint:gosec // p comes from WalkDir over the repository root, never caller input
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			sources[rel] = string(data)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", dir, err)
		}
	}
	return UnreferencedHandles(Declarations(), sources), nil
}
