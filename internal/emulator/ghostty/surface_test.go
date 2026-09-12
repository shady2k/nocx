package ghostty

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestThePortExportsOnlyItsOwnTypes is the first acceptance criterion of
// nocx-ygxjv.7, and it is a property of the SOURCE rather than of a build:
// internal/emulator may not name the adapter package or C, and no exported
// identifier there may have a type qualified by any other package at all.
//
// The second half is the stronger one and the one that matters. "It imports no
// CGo" would be satisfied by a port that re-declared an upstream struct field by
// field, so the assertion is about the whole exported surface: every type a
// caller can name is a type declared in internal/emulator. Today that means any
// package-qualified name in an exported signature is a failure, whatever the
// package is; if the port ever needs to name one, this test is where the
// decision to allow it has to be argued.
func TestThePortExportsOnlyItsOwnTypes(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir("..")
	if err != nil {
		t.Fatalf("read the port's directory: %v", err)
	}

	files := 0
	exported := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := "../" + name
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if file.Name.Name != "emulator" {
			t.Fatalf("%s declares package %s, which is not the port", path, file.Name.Name)
		}
		files++

		aliases := importAliases(file.Imports)
		for alias, imported := range aliases {
			if imported == "C" || strings.Contains(imported, "ghostty") {
				t.Errorf("%s imports %q as %q: the port must not reach upstream at all",
					path, imported, alias)
			}
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				exported++
				checkNoForeignType(t, fset, path, d.Name.Name, d.Type, aliases)
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if !s.Name.IsExported() {
							continue
						}
						exported++
						checkNoForeignType(t, fset, path, s.Name.Name, s.Type, aliases)
					case *ast.ValueSpec:
						for _, ident := range s.Names {
							if !ident.IsExported() {
								continue
							}
							exported++
							if s.Type != nil {
								checkNoForeignType(t, fset, path, ident.Name, s.Type, aliases)
								continue
							}
							// With no declared type the value's OWN type is
							// what a caller sees, and a composite literal is
							// the one shape whose type is written down here:
							// `var Err = errors.New(...)` names nothing, while
							// `var X = upstream.Thing{...}` names upstream.
							for _, value := range s.Values {
								ast.Inspect(value, func(n ast.Node) bool {
									if lit, ok := n.(*ast.CompositeLit); ok && lit.Type != nil {
										checkNoForeignType(t, fset, path, ident.Name, lit.Type, aliases)
									}
									return true
								})
							}
						}
					}
				}
			}
		}
	}
	// A scan that found nothing must not read as a surface that is clean.
	if files < 3 || exported < 20 {
		t.Fatalf("read %d files and %d exported identifiers, which is too few for this test to have read the port", files, exported)
	}
}

// checkNoForeignType reports every package-qualified name inside one exported
// declaration's type.
func checkNoForeignType(t *testing.T, fset *token.FileSet, file, name string, node ast.Node, aliases map[string]string) {
	t.Helper()
	ast.Inspect(node, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		imported, ok := aliases[ident.Name]
		if !ok {
			return true
		}
		t.Errorf("%s: %s has a type from %q (%s.%s): the port exports its own types or none",
			fset.Position(sel.Pos()), name, imported, ident.Name, sel.Sel.Name)
		return true
	})
}

func importAliases(imports []*ast.ImportSpec) map[string]string {
	out := map[string]string{}
	for _, imp := range imports {
		path := strings.Trim(imp.Path.Value, `"`)
		name := path
		if slash := strings.LastIndex(path, "/"); slash >= 0 {
			name = path[slash+1:]
		}
		if imp.Name != nil {
			name = imp.Name.Name
		}
		out[name] = path
	}
	return out
}
