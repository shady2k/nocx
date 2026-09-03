package agenttools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
)

// contentPkgDir is internal/content read as source rather than as an import.
// The ledger's ResourceKind members are constants, so no runtime value can
// enumerate them; the only way to ask "what does the ledger declare today" is
// to read the declaration.
const contentPkgDir = "../content"

// ledgerResourceKinds reads every ResourceKind constant out of the content
// package's own source. Parsing it is the point: a list written here would be
// a THIRD copy of the vocabulary and would fall behind exactly the way
// allResourceKinds did — twice, while a comment went on claiming the two
// agreed. The ledger owns the set (AD-8), so the ledger's source is what this
// package is measured against.
func ledgerResourceKinds(t *testing.T) []content.ResourceKind {
	t.Helper()
	entries, err := os.ReadDir(contentPkgDir)
	if err != nil {
		t.Fatalf("read %s: %v", contentPkgDir, err)
	}
	fset := token.NewFileSet()
	var kinds []content.ResourceKind
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(contentPkgDir, name), nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				ident, ok := vs.Type.(*ast.Ident)
				if !ok || ident.Name != "ResourceKind" {
					continue
				}
				for _, v := range vs.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					unquoted, uerr := strconv.Unquote(lit.Value)
					if uerr != nil {
						t.Fatalf("unquote %s: %v", lit.Value, uerr)
					}
					kinds = append(kinds, content.ResourceKind(unquoted))
				}
			}
		}
	}
	if len(kinds) == 0 {
		t.Fatalf("no ResourceKind constants found under %s — the parser stopped matching the source", contentPkgDir)
	}
	return kinds
}

// TestResourceKindSetMatchesTheLedger is the tripwire the comment used to be.
// Both directions matter: a kind the ledger declares and this package omits is
// a tool that cannot be assembled (the defect), and a kind this package names
// that the ledger does not declare is a value the grant_scopes CHECK would
// reject at persist time.
func TestResourceKindSetMatchesTheLedger(t *testing.T) {
	declared := map[content.ResourceKind]bool{}
	for _, k := range ledgerResourceKinds(t) {
		declared[k] = true
	}
	known := map[content.ResourceKind]bool{}
	for _, k := range allResourceKinds {
		known[k] = true
	}
	for k := range declared {
		if !known[k] {
			t.Errorf("content declares resource kind %q and allResourceKinds omits it — a tool naming it cannot assemble", k)
		}
		if !supportedResourceKind(k) {
			t.Errorf("content declares resource kind %q and supportedResourceKind rejects it — a tool naming it cannot assemble", k)
		}
	}
	for k := range known {
		if !declared[k] {
			t.Errorf("allResourceKinds names resource kind %q that content does not declare — the ledger would refuse to persist it", k)
		}
	}
}

// TestAssemble_AcceptsEveryLedgerResourceKind is the same criterion at the
// seam a tool author reaches: every kind the ledger declares survives
// assembly, so a workspace- or wave-scoped tool can be declared at all.
func TestAssemble_AcceptsEveryLedgerResourceKind(t *testing.T) {
	for _, kind := range ledgerResourceKinds(t) {
		t.Run(string(kind), func(t *testing.T) {
			// A declaration naming ResourceContent must also name the content
			// sub-scope family a grant has to contain (registry.go's
			// "missing scope family" rule). That is a separate, deliberate
			// requirement; satisfying it here keeps this test measuring the
			// kind set and nothing else.
			family := ""
			if kind == content.ResourceContent {
				family = "note"
			}
			reg, err := assemble(schemaFS(t, map[string]string{
				"x.schema.json": filesReadSchema,
			}), []Declaration{{
				Name:          "x",
				Description:   "a tool scoped to " + string(kind),
				Effect:        []content.Effect{content.EffectObserve},
				OutputTrust:   OutputTrustUntrusted,
				ResultBound:   ResultBound{MaxBytes: 1024, Truncation: TruncationDropTail},
				Deadline:      time.Second,
				Cancellation:  CancellationReturnError,
				ResourceKinds: []content.ResourceKind{kind},
				ScopeFamily:   family,
				Executes:      InGo,
				Params:        "x.schema.json",
			}})
			if err != nil {
				t.Fatalf("assemble a tool declaring %q: %v", kind, err)
			}
			if len(reg.tools) != 1 {
				t.Fatalf("assembled %v, want the tool declaring %q present", toolNames(reg.tools), kind)
			}
		})
	}
}
