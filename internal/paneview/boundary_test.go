package paneview

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The AD-6 amendment grants a pane's screen exactly two powers and names three
// things it may never decide: a worker state, a lifecycle attempt, an execution
// attempt. A test that read a frame and asserted "and no lifecycle attempt was
// created" would be checking one path through code that could still grow
// another tomorrow.
//
// So the invariant is asserted where it is actually enforced: this package
// cannot name those concepts, because it does not import the packages that own
// them and its exported surface mentions none of them. The day somebody adds
// the import, this fails — before the behaviour exists to be tested.
//
// It came here from internal/panegrid, which carried the same three checks for
// the same reason. What moved with ADR-0066 is where the parser runs; the list
// of what may be decided did not move at all.

// forbidden are the packages that own the three authorities. If paneview can
// reach one, it can alter one.
var forbidden = []string{
	"internal/lifecycle", // lifecycle attempts
	"internal/lifecyclepub",
	"internal/session",   // sessions and their identity (AD-7)
	"internal/content",   // the ledger: execution attempts, entries, artifacts
	"internal/notify",    // what reaches a human, and by which channel
	"internal/transport", // the control plane; also an import cycle
}

// isForbidden reports whether an import path names one of the authorities.
// The match is by PATH PREFIX and not by substring: internal/sessionruntime
// contains the text "internal/session" and is this package's own contract
// vocabulary (Completeness, Revision), while internal/session is the session
// model AD-7 forbids a frame to decide. A substring test cannot tell those
// apart, and one that "fixed" the complaint by banning both would take the
// frame's completeness with the authority it must not have.
func isForbidden(path string) (string, bool) {
	for _, bad := range forbidden {
		if path == bad || strings.HasPrefix(path, bad+"/") {
			return bad, true
		}
	}
	return "", false
}

func packageFiles(t *testing.T) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	out := map[string]*ast.File{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", e.Name()), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		out[e.Name()] = f
	}
	if len(out) == 0 {
		t.Fatal("no Go files found; the check would pass vacuously")
	}
	return out
}

func TestPaneViewCannotReachTheAuthoritiesItMayNotDecide(t *testing.T) {
	files := packageFiles(t)
	checked := 0
	for name, f := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: bad import literal %s", name, imp.Path.Value)
			}
			if bad, banned := isForbidden(path); banned {
				t.Errorf("%s imports %q: paneview may not reach %s — "+
					"the AD-6 amendment forbids a pane's screen to decide a worker state, "+
					"a lifecycle attempt or an execution attempt, and this import "+
					"is how that stops being structurally impossible",
					name, path, bad)
			}
		}
	}
	if checked == 0 {
		t.Fatal("only test files were checked; the assertion would be vacuous")
	}
}

// And the contrast that makes the test above evidence rather than a tautology:
// the same walk finds the imports this package DOES have, so a run that
// reported nothing cannot be a run that looked at nothing.
func TestTheImportCheckActuallySeesImports(t *testing.T) {
	files := packageFiles(t)
	var seen []string
	for name, f := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			seen = append(seen, p)
		}
		_ = name
	}
	if len(seen) == 0 {
		t.Fatal("no imports seen at all; the forbidden-import test proves nothing")
	}
	for _, want := range []string{"internal/emulator", "internal/log"} {
		found := false
		for _, s := range seen {
			if strings.Contains(s, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("expected paneview to import something matching %q; saw %v", want, seen)
		}
	}
}

// The exported surface must not name the three authorities either: a type
// called ExecutionAttempt would be a power regardless of which package declared
// it.
func TestExportedSurfaceNamesNoAuthority(t *testing.T) {
	files := packageFiles(t)
	banned := []string{"lifecycle", "execution", "attempt", "worker", "session", "ledger", "notify"}
	for name, f := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			var ident string
			switch d := n.(type) {
			case *ast.TypeSpec:
				ident = d.Name.Name
			case *ast.FuncDecl:
				ident = d.Name.Name
			default:
				return true
			}
			if !ast.IsExported(ident) {
				return true
			}
			low := strings.ToLower(ident)
			for _, b := range banned {
				if strings.Contains(low, b) {
					t.Errorf("%s: exported %q contains %q — a pane's screen may not "+
						"name an authority it is forbidden to decide", name, ident, b)
				}
			}
			return true
		})
	}
}
