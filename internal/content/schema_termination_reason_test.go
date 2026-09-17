package content_test

// THE TRAP, CLOSED (nocx-4yjwk.7).
//
// The vocabulary of `TerminationReason` is declared TWICE — once as Go
// constants in ledger.go and once as a CHECK constraint on
// `executions.termination_reason` in schemaV1 — and the two halves are not
// wired to each other by the compiler. A reason added to the Go list and not
// to the CHECK does not fail where it was written. It fails much later, at the
// terminal close of a real run: the UPDATE is refused, the error is caught and
// logged, the run reaches no terminal state, and the startup sweep repairs it
// as `interrupted`. A person is left with a run that has no durable ending and
// a log line nobody reads — the soft degrade AGENTS.md spends a section on.
//
// That is the class of defect this file exists to catch, and it catches it at
// the only moment it is cheap: the commit that adds the constant.
//
// IT ENUMERATES THE SOURCE, NOT A LIST. A hand-maintained slice of every
// reason would be a THIRD copy of the vocabulary and would fail in exactly the
// same way — somebody adds a constant and forgets the slice, and the test goes
// on passing over the reasons it already knew about. So the constants are read
// out of `ledger.go` itself with go/ast: whatever is declared with the type
// `TerminationReason` is what gets asserted, and there is nothing to forget.
//
// AND IT HAS BOTH ENDS. Go → database catches a widened Go list; database → Go
// catches a widened CHECK, which is the same divergence arriving from the other
// side and would leave the database accepting a value nothing can produce or
// read back. Neither direction alone is the invariant.

import (
	"context"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/shady2k/nocx/internal/content"
)

// terminationReasonsDeclaredInGo reads every constant of type
// TerminationReason out of ledger.go. Parsing the source is what makes this
// test self-maintaining: it sees a constant added today without anybody
// remembering this file exists.
func terminationReasonsDeclaredInGo(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(".", "ledger.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse ledger.go for the TerminationReason constants: %v", err)
	}
	var reasons []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			ident, ok := value.Type.(*ast.Ident)
			if !ok || ident.Name != "TerminationReason" {
				continue
			}
			for _, expr := range value.Values {
				lit, ok := expr.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("a TerminationReason constant is not a string literal: %s — this test reads the source, so the vocabulary must be readable from it", value.Names[0].Name)
				}
				unquoted, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unquote %s = %s: %v", value.Names[0].Name, lit.Value, err)
				}
				reasons = append(reasons, unquoted)
			}
		}
	}
	if len(reasons) == 0 {
		t.Fatal("no TerminationReason constants were found in ledger.go — the parse found nothing, so an empty pass here would prove nothing")
	}
	sort.Strings(reasons)
	return reasons
}

// terminationReasonsAcceptedByTheDatabase reads the CHECK's own list back out
// of the shipped DDL. It is the second end of the invariant: a value the
// database accepts and Go cannot name is the same divergence from the other
// side.
func terminationReasonsAcceptedByTheDatabase(t *testing.T, path, keyHex string) []string {
	t.Helper()
	ddl := executionsDDL(t, path, keyHex)
	clause := regexp.MustCompile(`(?is)termination_reason\s+TEXT\s+CHECK\s*\(\s*termination_reason\s+IN\s*\(([^)]*)\)`)
	found := clause.FindStringSubmatch(ddl)
	if found == nil {
		t.Fatalf("no termination_reason CHECK found in the executions DDL — the column has lost its constraint:\n%s", ddl)
	}
	var reasons []string
	for _, part := range strings.Split(found[1], ",") {
		reasons = append(reasons, strings.Trim(strings.TrimSpace(part), "'"))
	}
	sort.Strings(reasons)
	return reasons
}

// executionsDDL reads the shipped CREATE statement for `executions` back off
// the file, so the CHECK is read from what the database actually holds rather
// than from the constant this build compiled.
func executionsDDL(t *testing.T, path, keyHex string) string {
	t.Helper()
	db, err := driver.Open("file:"+path+"?vfs=adiantum", func(c *sqlite3.Conn) error {
		return c.Exec("PRAGMA hexkey='" + keyHex + "'")
	})
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	var ddl string
	if err := db.QueryRowContext(context.Background(),
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='executions'`).Scan(&ddl); err != nil {
		t.Fatalf("read the executions DDL: %v", err)
	}
	return ddl
}

// THE HEADLINE: every reason Go can name, the database accepts.
//
// It is asserted by INSERTING each one rather than by comparing two lists,
// because the list comparison is a statement about text and this is a
// statement about what the file will actually store. A CHECK that names the
// value under different whitespace, or a column that lost its constraint
// entirely, both answer this question correctly.
func TestEveryTerminationReasonGoCanNameTheDatabaseAccepts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	db, err := content.Open(context.Background(), content.Config{
		Path: path, Key: testKey(), Budget: testBudget,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	keyHex := hex.EncodeToString(testKey())

	if err := rawLedger(t, path, keyHex,
		`INSERT INTO environments (id, kind, first_seen) VALUES ('env', 'local', 1)`,
		`INSERT INTO environment_observations (environment_id, version, observed_at, criticality)
			VALUES ('env', 1, 1, 'routine')`,
		`INSERT INTO entries (id, ingest_seq, client, digest, environment_id, cwd, kind, source, intent,
			phase, status, submitted_at) VALUES ('e', 1, 'c', 'd', 'env', '/', 'shell', 'user', 'x', 'open', 'pending', 1)`,
	); err != nil {
		t.Fatalf("seed rows: %v", err)
	}

	reasons := terminationReasonsDeclaredInGo(t)
	for _, reason := range reasons {
		stmt := fmt.Sprintf(
			`INSERT INTO executions (entry_id, environment_obs_id, termination_reason) VALUES ('e', 1, '%s')`, reason)
		if err := rawLedger(t, path, keyHex, stmt); err != nil {
			t.Errorf("the database REFUSED the termination reason %q that Go declares: %v\n\n"+
				"A reason in content.TerminationReason and not in the executions.termination_reason CHECK does not fail "+
				"where it was written — it fails at the terminal close of a real run, which is caught, logged, and "+
				"repaired by the startup sweep as `interrupted`. The run ends with no durable ending. Widen the CHECK "+
				"in schemaV1 and add the rung that carries existing databases across it (schema_migrate.go).", reason, err)
		}
	}

	// AND THE OTHER END: nothing the database accepts is unnameable in Go.
	// A CHECK widened without its constant leaves the file able to store a
	// value no code can produce or read back, which is the same divergence
	// arriving from the other side.
	accepted := terminationReasonsAcceptedByTheDatabase(t, path, keyHex)
	if strings.Join(accepted, ",") != strings.Join(reasons, ",") {
		t.Errorf("the CHECK and content.TerminationReason name different vocabularies:\n  database: %v\n  Go:       %v",
			accepted, reasons)
	}
}

// AND THE CHECK IS NOT VACUOUS. Without this, the test above is satisfied by a
// column with no constraint at all — which is the exact shape of "somebody
// removed the CHECK", and it would pass silently forever.
func TestTheTerminationReasonCheckStillRefusesAValueGoCannotName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	db, err := content.Open(context.Background(), content.Config{
		Path: path, Key: testKey(), Budget: testBudget,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	keyHex := hex.EncodeToString(testKey())

	if err := rawLedger(t, path, keyHex,
		`INSERT INTO environments (id, kind, first_seen) VALUES ('env', 'local', 1)`,
		`INSERT INTO environment_observations (environment_id, version, observed_at, criticality)
			VALUES ('env', 1, 1, 'routine')`,
		`INSERT INTO entries (id, ingest_seq, client, digest, environment_id, cwd, kind, source, intent,
			phase, status, submitted_at) VALUES ('e', 1, 'c', 'd', 'env', '/', 'shell', 'user', 'x', 'open', 'pending', 1)`,
	); err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	if err := rawLedger(t, path, keyHex,
		`INSERT INTO executions (entry_id, environment_obs_id, termination_reason) VALUES ('e', 1, 'answer-unrevoked')`,
	); err == nil {
		t.Fatal("the executions.termination_reason CHECK accepted a reason no Go constant names — the column has lost its constraint, and the test above now proves nothing")
	}
}
