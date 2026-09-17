package ghostty

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// This file is the evidence ADR-0065 point 3 asks for. The conversions in
// convert.go are explicit and keyed on the HEADER'S NAMES — the case labels are
// C.GHOSTTY_* constants — where the spike this package replaces did the
// opposite (.internal/spikes/emulator/ghostty/binding.go:369,
// `Key(C.GHOSTTY_KEY_ARROW_LEFT)`): it declared Go types named Key, Mods and
// CellWidth and copied upstream's values into them, which is a cast with
// paperwork.
//
// # Why these two tests read the source, and what that is worth
//
// A cgo package cannot have cgo in a _test.go file ("use of cgo in test ...
// not supported"), so the upstream constants the conversions are keyed on
// cannot be named from a test at all. What IS testable from here is the shape
// of the tables and their completeness against the port's own declarations —
// which is the part a cast would violate — and the VALUES those conversions
// produce are tested behaviourally, through a real terminal, in
// terminal_test.go.
//
// The behavioural half is the stronger one and it does fail on a cast: the
// port's numbering is deliberately its own (WidthNarrow is 1 where upstream's
// narrow is 0, WidthWide is 2 where upstream's is 1), so a conversion that
// copied upstream's number through would report WidthNarrow for a wide cluster
// and TestCellReportsGraphemeWidthAndStyle would fail.
//
// What the source half adds is completeness: every Key the port declares has a
// conversion, and no two of them convert to the same upstream key. Neither is
// visible from behaviour — a printable key with no text encodes to nothing, so
// two port keys pointing at one upstream key would look identical.

// TestEveryDeclaredKeyHasItsOwnConversion walks the port's own declaration of
// Key and the adapter's own conversion of it, and refuses to let one exist
// without the other.
//
// The count is derived rather than written down, so adding a key and its
// conversion together passes and adding a key alone fails: the case that would
// otherwise ship is a key that compiles, is listed in the port, and silently
// encodes as ErrUnsupported.
func TestEveryDeclaredKeyHasItsOwnConversion(t *testing.T) {
	declared := declaredNames(t, "../key.go", "Key")
	if len(declared) < 70 {
		t.Fatalf("the port declares %d keys, which is too few for this test to be reading what it means to", len(declared))
	}
	converted := keyConversionCases(t, "convert.go")

	// A LIST, not a set: one duplicate return and one missing case leave the
	// same number of distinct names, so the duplicates are named here before
	// the count is compared at all.
	returned := map[string]bool{}
	for _, upstream := range converted {
		if returned[upstream] {
			t.Errorf("%s is returned by two cases: one of those keys encodes as the other", upstream)
		}
		returned[upstream] = true
	}
	// KeyUnknown is the port's zero value and deliberately has no upstream
	// name: a key nobody set must encode nothing rather than encode as
	// UNIDENTIFIED.
	if returned["GHOSTTY_KEY_UNIDENTIFIED"] {
		t.Error("the zero key converts to an upstream key: an unset Key would encode")
	}
	if want := len(declared) - 1; len(converted) != want {
		t.Errorf("the port declares %d keys (minus the zero value, %d) and the conversion returns %d upstream constants",
			len(declared), want, len(converted))
	}
}

// keyConversionCases returns every upstream constant cKey returns, in the order
// the function returns them: its case labels are the PORT's keys and its
// returns are the upstream names, which is the direction a conversion runs in.
//
// It is a list rather than a set because a set cannot tell a duplicate from a
// gap — both leave the same number of distinct names — and the caller needs to
// see both separately.
func keyConversionCases(t *testing.T, path string) []string {
	t.Helper()
	file := parseGo(t, path)
	var found []string
	seenFunction := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "cKey" || fn.Body == nil {
			continue
		}
		seenFunction = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "C" {
				found = append(found, sel.Sel.Name)
			}
			return true
		})
	}
	if !seenFunction {
		t.Fatalf("no cKey function found in %s: the key conversion is not a table", path)
	}
	return found
}

// declaredNames returns the names of every constant in the const block whose
// FIRST spec declares the given type, which is the port's enumeration.
//
// It reads the value-free specs after the first one as members of the same
// block, which is what an iota enumeration is; a spec that restates a type or a
// value starts a different enumeration and ends this one.
func declaredNames(t *testing.T, path, typeName string) []string {
	t.Helper()
	file := parseGo(t, path)
	var names []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		first, ok := gen.Specs[0].(*ast.ValueSpec)
		if !ok {
			continue
		}
		if id, ok := first.Type.(*ast.Ident); !ok || id.Name != typeName {
			continue
		}
		for i, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if i > 0 && value.Type != nil {
				break
			}
			for _, name := range value.Names {
				names = append(names, name.Name)
			}
		}
	}
	return names
}

func parseGo(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}
