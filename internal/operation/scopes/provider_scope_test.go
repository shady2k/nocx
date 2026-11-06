package scopes

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// providerFields are the WSServer fields whose values are provider adapters:
// a store, a process runner, a connection or a model client. A call to a
// method of one of their types is a provider call, whatever the receiver
// expression is spelled.
var providerFields = []string{
	"git",
	"filesys",
	"contentDB",
	"vaultLifecycle",
	"apiCollections",
	"assistantClient",
	"sshConfigResolver",
	"backupService",
}

const baselinePath = "internal/operation/scopes/provider-calls-baseline.json"

// A call is a provider call when the method it resolves to is in the method
// set of a provider field's type. go/types resolves `p := s.filesys`,
// `server.filesys` and `s.filesys` to the same *types.Func, so the spelling
// of the receiver cannot hide it.
type providerCall struct {
	Key  string // file:<receiver type>.<method>#<occurrence>
	Pos  string
	Func string
}

func providerCalls(pkg *packages.Package, root, structName string, fields []string) ([]providerCall, error) {
	obj := pkg.Types.Scope().Lookup(structName)
	if obj == nil {
		return nil, fmt.Errorf("%s: type %s not found", pkg.PkgPath, structName)
	}
	named, ok := obj.Type().(*types.Named)
	if !ok {
		return nil, fmt.Errorf("%s: %s is not a named type", pkg.PkgPath, structName)
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return nil, fmt.Errorf("%s: %s is not a struct", pkg.PkgPath, structName)
	}
	providers := map[*types.Func]string{}
	wanted := map[string]bool{}
	for _, f := range fields {
		wanted[f] = true
	}
	for i := 0; i < st.NumFields(); i++ {
		field := st.Field(i)
		if !wanted[field.Name()] {
			continue
		}
		fieldType := field.Type()
		label := types.TypeString(fieldType, func(p *types.Package) string { return p.Name() })
		ms := types.NewMethodSet(fieldType)
		for j := 0; j < ms.Len(); j++ {
			fn, ok := ms.At(j).Obj().(*types.Func)
			if !ok {
				continue
			}
			providers[fn] = label
		}
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("%s: no method set found for any provider field %v", pkg.PkgPath, fields)
	}

	occurrences := map[string]int{}
	var out []providerCall
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			selection := pkg.TypesInfo.Selections[sel]
			if selection == nil {
				return true
			}
			fn, ok := selection.Obj().(*types.Func)
			if !ok {
				return true
			}
			label, ok := providers[fn]
			if !ok {
				return true
			}
			pos := pkg.Fset.Position(call.Pos())
			rel, err := filepath.Rel(root, pos.Filename)
			if err != nil {
				rel = pos.Filename
			}
			base := fmt.Sprintf("%s:%s.%s", filepath.ToSlash(rel), label, fn.Name())
			occurrences[base]++
			out = append(out, providerCall{
				Key:  fmt.Sprintf("%s#%d", base, occurrences[base]),
				Pos:  pos.String(),
				Func: fn.FullName(),
			})
			return true
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func loadTransport(t *testing.T) *packages.Package {
	t.Helper()
	root := repoRoot(t)
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Dir:        root,
		BuildFlags: []string{"-tags=gtk3,nocx_framecheck"},
	}
	pkgs, err := packages.Load(cfg, "./internal/transport")
	if err != nil {
		t.Fatalf("loading internal/transport: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("loaded %d packages, want 1", len(pkgs))
	}
	if len(pkgs[0].Errors) != 0 {
		for _, e := range pkgs[0].Errors {
			t.Errorf("load: %v", e)
		}
		t.Fatal("internal/transport did not type-check")
	}
	return pkgs[0]
}

func repoRoot(t *testing.T) string {
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

type baselineEntry struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

func loadBaseline(t *testing.T, root string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, baselinePath)) //nolint:gosec // repository-relative baseline
	if err != nil {
		t.Fatalf("read provider baseline: %v", err)
	}
	var entries []baselineEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("decode provider baseline: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if strings.TrimSpace(e.Reason) == "" {
			t.Fatalf("baseline entry %q has no reason", e.Key)
		}
		out[e.Key] = e.Reason
	}
	return out
}

var update = flag.Bool("update-provider-scope-baseline", false, "rewrite the provider-call baseline from the current tree")

func TestNoProviderCallOutsideAScope(t *testing.T) {
	root := repoRoot(t)
	pkg := loadTransport(t)
	calls, err := providerCalls(pkg, root, "WSServer", providerFields)
	if err != nil {
		t.Fatal(err)
	}
	baseline := loadBaseline(t, root)
	if *update {
		var entries []baselineEntry
		for _, c := range calls {
			reason := baseline[c.Key]
			entries = append(entries, baselineEntry{Key: c.Key, Reason: reason})
		}
		raw, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, baselinePath), append(raw, '\n'), 0o600); err != nil { //nolint:gosec // repository-relative baseline
			t.Fatal(err)
		}
		t.Fatalf("wrote %s with %d entries; fill every empty reason before committing", baselinePath, len(entries))
	}

	var fresh, stale []string
	seen := map[string]bool{}
	for _, c := range calls {
		seen[c.Key] = true
		if _, ok := baseline[c.Key]; !ok {
			fresh = append(fresh, fmt.Sprintf("%s  %s", c.Pos, c.Func))
		}
	}
	for key := range baseline {
		if !seen[key] {
			stale = append(stale, key)
		}
	}
	t.Logf("provider calls: %d found, %d baselined, %d new, %d baseline entries no longer present",
		len(calls), len(calls)-len(fresh), len(fresh), len(stale))
	if len(fresh) != 0 {
		t.Fatalf("a provider call is outside a scope and not in %s:\n  %s", baselinePath, strings.Join(fresh, "\n  "))
	}
}

// TestTheProviderCallCheckCatchesAnAliasedReceiver is the planted case the
// text check missed: the same call under a local alias.
func TestTheProviderCallCheckCatchesAnAliasedReceiver(t *testing.T) {
	fixture := loadFixture(t)
	calls, err := providerCalls(fixture, repoRoot(t), "Server", []string{"filesys"})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("fixture produced %d provider calls, want 3 (direct, alias, other receiver): %v", len(calls), calls)
	}
	for _, c := range calls {
		if strings.Contains(c.Func, "Lookalike") {
			t.Fatalf("a lookalike method was reported as a provider call: %+v", c)
		}
	}
}

func loadFixture(t *testing.T) *packages.Package {
	t.Helper()
	root := repoRoot(t)
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedFiles,
		Dir:  root,
	}
	pkgs, err := packages.Load(cfg, "./internal/operation/scopes/testdata/providerfixture")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	if len(pkgs) != 1 || len(pkgs[0].Errors) != 0 {
		t.Fatalf("fixture did not type-check: %+v", pkgs)
	}
	return pkgs[0]
}
