package session_test

// THERE IS ONE OWNER OF A LOCAL PTY, AND IT IS THIS PACKAGE.
//
// D11 of the level-1 design says the helper runs on every machine including
// yours, and that there is no local special case and no code path that exists
// only for one of them. Until nocx-ie23r.3 there were two owners: the helper's
// LocalSpawner here, and internal/app's localPTYFactory, which forked the
// shell inside the coordinator so a local pane died with the backend. The
// second one is deleted. This is what keeps it deleted.
//
// WHY A SOURCE WALK AND NOT A GREP AT REVIEW. A grep is a thing somebody
// remembers to do. The failure this guards against is not a person arguing for
// a second PTY owner — nobody will — it is somebody reaching for pty.NewLocal
// to fix something local in a hurry, in a package where it compiles fine and
// every test still passes, because a coordinator-forked shell works perfectly
// until the coordinator goes away. That is exactly the shape a ratchet catches
// and a review does not.
//
// WHAT THIS TEST CANNOT SAY, and the reason it is not the whole assertion:
// uniqueness is not reachability. It passes unchanged on a tree where local
// open is broken everywhere and nothing ever reaches this constructor at all.
// The paired half is internal/app's TestALocalPaneIsTheDaemonsChild, which
// opens a real pane through the shipped composition root and reads the shell's
// parent out of the operating system.
//
// It is the same shape, and for the same class of reason, as
// internal/helper/endpoint's no_tcp_listener_test.go.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// theLocalOpenPath is every package a local pane's open travels through,
// between the process a person launched and the process that forks the shell:
// both shipped binaries, the composition root, the control plane, the session
// registry, the helper client and the carrier that reaches this machine's
// endpoint — and this package, which is where the fork belongs.
var theLocalOpenPath = []string{
	"../../..",
	"../../../cmd/nocx-server",
	"../../../cmd/nocx-helper",
	"../../../internal/app",
	"../../../internal/transport",
	"../../../internal/session",
	"../../../internal/helper/client",
	"../../../internal/helper/local",
	"../../../internal/helper/endpoint",
	"../../../internal/helper/host",
	"../../../internal/helper/session",
}

// theOwner is the one directory on that path where a local PTY may be
// constructed. Compared as a cleaned path so the entry above and this one
// cannot drift apart.
const theOwner = "../../../internal/helper/session"

func TestOnlyTheHelperConstructsALocalPTY(t *testing.T) {
	owner := filepath.Clean(theOwner)
	found := false
	for _, dir := range theLocalOpenPath {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		if len(files) == 0 {
			t.Fatalf("%s has no Go files: the path this test walks has moved", dir)
		}
		mayOwn := filepath.Clean(dir) == owner
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			if constructsALocalPTY(t, file) && mayOwn {
				found = true
			}
		}
	}
	// The negative alone is satisfiable by deleting the constructor, by
	// renaming it, or by this test walking the wrong tree — all three of which
	// would leave it green while saying nothing. So the owner must still own
	// one.
	if !found {
		t.Fatalf("no pty.NewLocal call in %s: either the local PTY owner moved, "+
			"or this test is no longer looking where the shell is forked", theOwner)
	}
}

// constructsALocalPTY reports whether file calls pty.NewLocal, and fails the
// test when it does so from a package that may not.
//
// Matched on the SELECTOR — package identifier plus function name — rather
// than on the text, so a comment naming the constructor (this file is full of
// them, and so is internal/app's) is not a violation and a call written across
// two lines still is.
func constructsALocalPTY(t *testing.T, file string) bool {
	t.Helper()
	src, err := os.ReadFile(file) // #nosec G304 — the path comes from this test's own glob
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	owner := filepath.Clean(filepath.Dir(file)) == filepath.Clean(theOwner)
	calls := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "pty" || sel.Sel.Name != "NewLocal" {
			return true
		}
		calls = true
		if !owner {
			t.Errorf("%s: pty.NewLocal. A local pane's shell is forked by the helper daemon "+
				"(%s) and by nothing else — a second local PTY owner is a shell that dies with "+
				"the coordinator, which is what nocx-ie23r deleted (D11, ADR-0057)",
				fset.Position(call.Pos()), theOwner)
		}
		return true
	})
	return calls
}
