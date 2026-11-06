package catalog_test

// A scope bounds one operation, never a lifetime loop and never a wait.
//
// ADR-8eej3e66 §2: a lifetime loop is not an operation; each iteration is.
// §3: turn is the deadline on waiting, max is the deadline on work. A scope
// opened once around a loop therefore expires on a healthy session that has
// nothing to record (run 42139974476's review), and one opened around a wait
// expires while the owner is still doing exactly what it should.
//
// Reading the source is the only check that catches the NEXT such site: the
// runtime cannot tell a loop from a batch. So this parses every production Go
// file that mentions the catalogue and refuses a BeginInternal/beginInternal
// call followed, in its own block, by a for, range or select. Finite batches
// that legitimately run one scope over an internal loop are named below with
// why the loop is the operation rather than a lifetime loop; a new site is red
// until somebody writes that sentence.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// finiteBatchScopes names functions whose interior loop is ONE operation's
// body, not a lifetime loop. The key is "file:function".
var finiteBatchScopes = map[string]string{
	"internal/app/worker_checkout_sweep.go:(*checkoutSweeper).RunOnce":    "one sweep judges the record's rows once; the ticker around RunOnce is loop.worker.checkoutSweep",
	"internal/app/worker_restart.go:restoreWorkerRecords":                 "one restore pass over the persisted workers; the function is the operation",
	"internal/app/worker_restart.go:restoreWorkerCloseRecords":            "one restore pass over close records; the function is the operation",
	"internal/transport/ws_files.go:(*WSServer).filesBaseline":            "one baseline over the paths added by one files.watch call",
	"internal/transport/ws_block_rows.go:(*blockStream).flushPendingRows": "one flush of the rows accumulated for one block",
	"internal/transport/ws_agent.go:(agentHandlers).runAskStream":         "one agent run; the stream loop is its work",
	"internal/transport/ws_lifecycle.go:(*WSServer).syncLifecycleLedger":  "one reconciliation of one lifecycle fact",
	"internal/transport/ws.go:(*WSServer).replayStoredForwards":           "one replay over the forwards stored for one profile",
	"internal/assistant/engine.go:streamModelAnswer":                      "one model answer; the SDK's loop is its work",
	"internal/content/ledger_sqlite.go:(*sqliteContent).Submit":           "one store transaction",
	"internal/vault/journal.go:Reconcile":                                 "one journal reconciliation pass",
	"internal/discovery/discovery.go:(*Detector).Sample":                  "one sample across the declared probes",
	"internal/notify/notify.go:(*Router).deliver":                         "one delivery to one sink",
	"internal/update/transaction.go:(*updater).Apply":                     "one update apply",
	"internal/helper/deploy/install.go:Ensure":                            "one install of one artifact",
	"internal/helper/deploy/platform.go:Probe":                            "one uname probe",
	"internal/git/local/local.go:run":                                     "one git process",
	"internal/ssh/ssh_bootstrap_feed.go:(*sessionFeed).ReadLine":          "one frame read under a timeout; waiting for its bytes is its work",
}

func TestNoScopeEnclosesALoopOrAWait(t *testing.T) {
	root := repoRootForScopeTest(t)
	var violations []string
	for _, dir := range []string{"internal", "cmd"} {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path) //nolint:gosec // repository source walk
			if err != nil {
				return err
			}
			if !strings.Contains(string(data), "BeginInternal(") && !strings.Contains(string(data), ".beginInternal(") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			violations = append(violations, scopePlacementViolations(t, rel, data)...)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(violations) != 0 {
		t.Fatalf("a scope encloses a loop or a wait; scope the operation instead, or name it in finiteBatchScopes with a reason:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

func repoRootForScopeTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

type scopeVisitor struct {
	t     *testing.T
	fset  *token.FileSet
	file  string
	stack []ast.Node
	out   *[]string
}

func (v *scopeVisitor) Visit(n ast.Node) ast.Visitor {
	if n == nil {
		v.stack = v.stack[:len(v.stack)-1]
		return nil
	}
	v.stack = append(v.stack, n)
	call, ok := n.(*ast.CallExpr)
	if !ok || !isBeginInternalCall(call) {
		return v
	}
	block, fnLabel := enclosingBlockAndFunc(v.stack)
	if block == nil || fnLabel == "" {
		return v
	}
	stmtIndex := -1
	for i, stmt := range block.List {
		if stmt.Pos() <= call.Pos() && call.End() <= stmt.End() {
			stmtIndex = i
			break
		}
	}
	if stmtIndex < 0 {
		return v
	}
	scopeName := assignedScopeName(block.List[stmtIndex], call)
	for _, stmt := range block.List[stmtIndex+1:] {
		// A deferred settlement runs at function exit; the loop after it is
		// still inside the scope's lifetime.
		if _, deferred := stmt.(*ast.DeferStmt); !deferred && scopeName != "" && statementSettlesScope(stmt, scopeName) {
			return v
		}
		switch stmt.(type) {
		case *ast.ForStmt, *ast.RangeStmt, *ast.SelectStmt:
			key := v.file + ":" + fnLabel
			if _, ok := finiteBatchScopes[key]; ok {
				return v
			}
			*v.out = append(*v.out, v.fset.Position(call.Pos()).String()+" "+key)
			return v
		}
	}
	return v
}

// assignedScopeName names the variable the call is assigned to, when the
// statement is `opScope := ...`. The scan stops at that variable's settlement:
// a wait AFTER the operation returned is not enclosed by it.
func assignedScopeName(stmt ast.Stmt, call *ast.CallExpr) string {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok || len(assign.Lhs) != 1 {
		return ""
	}
	id, ok := assign.Lhs[0].(*ast.Ident)
	if !ok {
		return ""
	}
	return id.Name
}

func statementSettlesScope(stmt ast.Stmt, name string) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != name {
			return true
		}
		switch sel.Sel.Name {
		case "Complete", "Settle", "Refuse", "Cancel", "Close":
			found = true
		}
		return true
	})
	return found
}

func isBeginInternalCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "BeginInternal", "beginInternal":
		return true
	}
	return false
}

// enclosingBlockAndFunc finds the deepest block containing the call and the
// nearest function declaration or literal. A literal is reported as "<closure>"
// because a closure body is one iteration's unit by construction.
func enclosingBlockAndFunc(stack []ast.Node) (*ast.BlockStmt, string) {
	var block *ast.BlockStmt
	label := ""
	for i := len(stack) - 1; i >= 0; i-- {
		switch n := stack[i].(type) {
		case *ast.BlockStmt:
			if block == nil {
				block = n
			}
		case *ast.FuncDecl:
			if label == "" {
				label = funcDeclLabel(n)
			}
		case *ast.FuncLit:
			if label == "" {
				label = "<closure>"
			}
		}
	}
	return block, label
}

func funcDeclLabel(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	recv := fn.Recv.List[0].Type
	switch t := recv.(type) {
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return "(*" + id.Name + ")." + fn.Name.Name
		}
	case *ast.Ident:
		return "(" + t.Name + ")." + fn.Name.Name
	}
	return fn.Name.Name
}

// scopePlacementViolations is the pure half, so the planted violations are
// fixtures rather than edits to the tree.
func scopePlacementViolations(t *testing.T, file string, src []byte) []string {
	t.Helper()
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	var out []string
	ast.Walk(&scopeVisitor{t: t, fset: fset, file: file, out: &out}, parsed)
	return out
}

// Red-first fixtures for the structural rule itself.
func TestScopePlacementCheckCatchesAPlantedLifetimeLoop(t *testing.T) {
	src := `package p

import (
	"context"

	"github.com/shady2k/nocx/internal/operation/catalog"
)

func run(ctx context.Context) {
	s := catalog.BeginInternal(ctx, catalog.OpAck)
	defer s.Complete()
	for {
		doWork()
	}
}
`
	got := scopePlacementViolations(t, "p/fixture.go", []byte(src))
	if len(got) != 1 {
		t.Fatalf("a scope around a loop produced %d violations, want 1: %v", len(got), got)
	}
}

func TestScopePlacementCheckCatchesAPlantedWait(t *testing.T) {
	src := `package p

import (
	"context"

	"github.com/shady2k/nocx/internal/operation/catalog"
)

func wait(ctx context.Context) {
	op := catalog.BeginInternal(ctx, catalog.OpAck)
	defer op.Complete()
	select {
	case <-ctx.Done():
	}
}
`
	if got := scopePlacementViolations(t, "p/fixture.go", []byte(src)); len(got) != 1 {
		t.Fatalf("a scope around a wait produced %d violations, want 1: %v", len(got), got)
	}
}

func TestScopePlacementCheckAllowsOneIterationAndOneOperation(t *testing.T) {
	src := `package p

import (
	"context"

	"github.com/shady2k/nocx/internal/operation/catalog"
)

func loop(ctx context.Context) {
	for {
		op := catalog.BeginInternal(ctx, catalog.OpAck)
		doWork()
		op.Complete()
		select {
		case <-ctx.Done():
			return
		}
	}
}

func one(ctx context.Context) {
	op := catalog.BeginInternal(ctx, catalog.OpAck)
	doWork()
	op.Complete()
	select {
	case <-ctx.Done():
	}
}
`
	if got := scopePlacementViolations(t, "p/fixture.go", []byte(src)); len(got) != 0 {
		t.Fatalf("per-iteration and settled-before-wait scopes produced violations: %v", got)
	}
}
