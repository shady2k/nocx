package apiimport

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// directlyForbidden are packages this one may not import. The first two are
// the only ways Go starts a process; golang.org/x/sys is the third, one
// level down.
//
// syscall is here rather than in the transitive list because `os` pulls it
// in and always will — so the honest claim is about what THIS package
// reaches for, and the ban on os.StartProcess below closes the gap that
// leaves.
var directlyForbidden = []string{
	"os/exec",
	"syscall",
	"golang.org/x/sys/unix",
	"golang.org/x/sys/windows",
	"net",
	"net/http",
	"crypto/tls",
}

// transitivelyForbidden are packages that must not be reachable at all.
// syscall is deliberately absent (see above); everything that could start a
// process or open a socket is present.
var transitivelyForbidden = []string{
	"os/exec",
	"net",
	"net/http",
	"crypto/tls",
	"os/signal",
}

// TestPackageNeverExecs asserts the ABSENCE OF THE EXEC rather than the
// absence of damage, which is the assertion design §10 actually asks for: a
// test that runs `curl -d '$(touch /tmp/pwned)'` and then checks /tmp is
// clean proves that this input did not fire, not that no input can. There
// is no shell to reach because nothing in this package or below it can
// start a process.
//
// The same walk bans the network, which is the other half: AN IMPORT NEVER
// FIRES A REQUEST (§10, §13). A package that cannot reach net/http cannot
// fire one by accident in a later edit either.
func TestPackageNeverExecs(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	files := 0
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			files++
			for _, imp := range f.Imports {
				p, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatalf("%s: bad import path %s", name, imp.Path.Value)
				}
				for _, bad := range directlyForbidden {
					if p == bad {
						t.Fatalf("%s imports %q — this package parses curl, it never runs it", name, bad)
					}
				}
			}
			// os is imported for os.FileMode; os.StartProcess is the one
			// thing in it that starts a process.
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				if id.Name == "os" && (sel.Sel.Name == "StartProcess" || sel.Sel.Name == "FindProcess") {
					t.Fatalf("%s calls os.%s", name, sel.Sel.Name)
				}
				return true
			})
		}
	}
	if files < 3 {
		t.Fatalf("walked %d non-test files — the walk found nothing to check", files)
	}
	t.Logf("walked %d non-test files", files)
}

// TestPackageDependenciesNeverExec is the same claim one level out: not
// only does this package not import os/exec, nothing it imports does
// either, so there is no seam through which a dependency could grow one.
func TestPackageDependenciesNeverExec(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH; the direct-import check above still holds")
	}
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	deps := strings.Fields(string(out))
	if len(deps) < 10 {
		t.Fatalf("go list -deps returned %d packages — it did not resolve the package", len(deps))
	}
	for _, d := range deps {
		for _, bad := range transitivelyForbidden {
			if d == bad {
				t.Fatalf("%q is reachable from this package", bad)
			}
		}
	}
	t.Logf("checked %d transitive dependencies", len(deps))
}
