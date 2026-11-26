package hashline

// The engine is pure by bytes, and this is the mechanical half of that claim:
// no non-test file in this package may import os or path/filepath. Reading the
// file, deciding it is too large and installing the bytes back belong to the
// provider seam (filesystem.VersionedFiles), because the answers differ on this
// machine and on a host behind the far helper — and the package that used to
// own them is exactly why the assistant's edit path could only reach one of the
// two.

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNonTestFilesImportNeitherOSNorFilepath(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	banned := map[string]bool{"os": true, "path/filepath": true}
	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		file, parseErr := parser.ParseFile(token.NewFileSet(), filepath.Join(".", name), nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		for _, spec := range file.Imports {
			path, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				t.Fatalf("%s: unquote import %s: %v", name, spec.Path.Value, unquoteErr)
			}
			if banned[path] {
				t.Errorf("%s imports %q; the engine is pure by bytes", name, path)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("no non-test files scanned; the test would pass over an empty package")
	}
}
