// Command mactests derives which tests in the OS package set can only run on
// macOS, from the code each test REACHES (nocx-ouru5.1.13, review round 2).
//
// THE GAP IT CLOSES. The first cut of this selection was file-based: a test was
// macOS-only when its own FILE was darwin-constrained. But a package's darwin
// IMPLEMENTATION lives in non-test files — `mounts_darwin.go`, `pty_darwin.go`,
// `identity_darwin.go` — and it is tested by tests in UNTAGGED files of the same
// directory. Those tests compile on both platforms; on Linux they exercise the
// linux file and never touch the darwin one, so running them on Linux says
// nothing about the darwin code they exist for. That is what the owner's rule
// forbids: "the Mac job holds only the tests that cannot run without a Mac", and
// a test whose subject is a darwin implementation cannot.
//
// THE RULE. A test is macOS-only when it reaches, WITHIN ITS OWN DIRECTORY, a
// function, method or package-level variable whose NAME is declared in a file
// that the darwin build compiles and the linux build does not.
//
// WHY A NAME, AND WHY THAT IS SOUND. A shared file — one both platforms compile
// — cannot call a function that exists on only one of them: that would not
// compile on the other. So a call made from shared code lands on a name that
// every platform build provides, and the per-platform files are where the two
// bodies of that name live (`mounts_darwin.go` and `mounts_linux.go` both define
// `mounts`). Matching the call's name against the names the darwin-only files
// declare is therefore exactly "this call has a darwin body", and it is why the
// analysis does not need the darwin build itself to enumerate the edges: the
// edges are the ones the shared code has, and the darwin-only files supply the
// question "which of them have a darwin body".
//
// That matters because the darwin build cannot be type-checked off a Mac: the
// emulator is cgo, and a cross-cgo darwin build needs a darwin C toolchain this
// repository does not carry. So the CALL GRAPH is built from the HOST build (SSA
// + CHA, the same family as the dead-code ratchet) and the darwin-only NAMES are
// read out of the files the darwin build adds. On a Mac the same command runs
// against the darwin build and must select the same tests — ci-mac re-derives the
// selection and fails when the committed file disagrees, which is what makes the
// two platforms' answers one answer.
//
// THE BOUNDARY is the directory, deliberately: a package's tests — its own and
// its external `_test` package — are the tests of that package's code. A CONSUMER
// package that reaches a platform implementation through another package
// (internal/app importing internal/pty) is covered for the platform half by that
// package's own tests; its own tests are about its own code and run wherever
// that code builds. Widening the graph across packages would pull every consumer
// of every platform package onto the Mac, which is the per-package behaviour this
// change exists to remove.
//
// Package initialisers are not a source of reachability: every test binary runs
// them, so "the package has a darwin initialiser" would select every test in it.
//
// Usage:
//
//	go run ./cmd/mactests [--write <path> | --check <path>] <pkg-dir>...
//
// Without a flag it prints the selection to stdout as `pkg<TAB>Test` lines.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// modulePath is the import path prefix every analysed directory is under.
const modulePath = "github.com/shady2k/nocx/"

// tags are the build tags the macOS test step runs with, so the graph is the
// graph those tests have. gtk3 is harmless on darwin (the platform tag list is
// empty there) and nocx_framecheck is the test-only detector ci.yml enables.
const tags = "gtk3,nocx_framecheck"

func main() {
	write := flag.String("write", "", "write the selection to this file")
	check := flag.String("check", "", "verify this file is the current selection")
	flag.Parse()
	dirs := flag.Args()
	if len(dirs) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mactests [--write <path> | --check <path>] <pkg-dir>...")
		os.Exit(2)
	}

	selection, err := derive(dirs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mactests: %v\n", err)
		os.Exit(1)
	}
	rendered := render(selection)

	switch {
	case *write != "":
		if err := os.WriteFile(*write, []byte(rendered), 0o644); err != nil { //nolint:gosec // a committed text file, and 0644 is what the repository stores
			fmt.Fprintf(os.Stderr, "mactests: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "mactests: wrote %d test(s) to %s\n", len(selection), *write)
	case *check != "":
		current, err := os.ReadFile(*check)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mactests: %v\n", err)
			os.Exit(1)
		}
		if string(current) != rendered {
			fmt.Fprintf(os.Stderr,
				"mactests: %s is not the current selection.\nRegenerate it and commit it:\n\n    make mac-only-tests\n\n", *check)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "mactests: %s is current (%d test(s))\n", *check, len(selection))
	default:
		fmt.Print(rendered)
	}
}

// entry is one selected test: the package directory as the Makefile spells it
// (`internal/pty`) and the test function.
type entry struct {
	pkg  string
	test string
}

func render(selection []entry) string {
	var b strings.Builder
	b.WriteString("# macOS-only tests REACHED through darwin implementations (nocx-ouru5.1.13).\n")
	b.WriteString("#\n")
	b.WriteString("# Generated by `make mac-only-tests` (go run ./cmd/mactests, SSA + CHA over the\n")
	b.WriteString("# host build with tags " + tags + "). ci-mac re-derives it and fails when this\n")
	b.WriteString("# file disagrees; ci-linux reads it and skips exactly these names. Never edit it\n")
	b.WriteString("# by hand: the rule is the call graph plus the darwin-only declarations, and\n")
	b.WriteString("# cmd/mactests is their statement. See that command for why the analysis runs on\n")
	b.WriteString("# the host build rather than a cross-cgo darwin one.\n")
	b.WriteString("#\n")
	b.WriteString("# Tests in darwin-constrained test FILES are not listed here — those are derived\n")
	b.WriteString("# from the source on any platform by scripts/mac-only-tests.mjs.\n")
	for _, e := range selection {
		fmt.Fprintf(&b, "%s\t%s\n", e.pkg, e.test)
	}
	return b.String()
}

// darwinOnlyFiles is the set of NON-TEST files in this directory that the darwin
// build compiles and the linux build does not, as paths relative to the worktree.
// Asked of the go tool rather than parsed out of the constraints, so `darwin`,
// `!linux` and a filename suffix all answer alike. CGO_ENABLED=0 in both listings
// so a cgo file cannot make the two sides disagree about a name that has nothing
// to do with the platform split.
func darwinOnlyFiles(dir string) ([]string, []string, error) {
	linuxRegular, linuxTests, err := listedFiles(dir, "linux")
	if err != nil {
		return nil, nil, err
	}
	darwinRegular, darwinTests, err := listedFiles(dir, "darwin")
	if err != nil {
		return nil, nil, err
	}
	linux := map[string]bool{}
	for _, name := range linuxRegular {
		linux[name] = true
	}
	onDarwin := map[string]bool{}
	for _, name := range darwinTests {
		onDarwin[name] = true
	}
	var darwinImpl []string
	for _, name := range darwinRegular {
		if !linux[name] {
			darwinImpl = append(darwinImpl, filepath.Join(dir, name))
		}
	}
	// TEST files the darwin build does not compile. A test declared in one of
	// them cannot be the Mac's — it does not exist there — and selecting it would
	// take it off the Linux side too, which is the one thing the partition must
	// never do.
	var linuxOnlyTests []string
	for _, name := range linuxTests {
		if !onDarwin[name] {
			linuxOnlyTests = append(linuxOnlyTests, name)
		}
	}
	return darwinImpl, linuxOnlyTests, nil
}

// listedFiles asks the go tool which non-test files a build for goos compiles in
// this directory. Listing needs no compiler of that platform, so this half runs
// anywhere.
func listedFiles(dir, goos string) (regular []string, tests []string, err error) {
	const format = "{{range .GoFiles}}{{.}}\n{{end}}{{range .TestGoFiles}}test/{{.}}\n{{end}}{{range .XTestGoFiles}}test/{{.}}\n{{end}}"
	cmd := exec.Command("go", "list", "-f", format, "./"+strings.TrimSuffix(dir, "/")) //nolint:gosec // the directory is this repository's own package path, passed by the Makefile from OS_PKG_DIRS
	cmd.Env = append(os.Environ(), "GOOS="+goos, "CGO_ENABLED=0")
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("go list GOOS=%s ./%s: %w", goos, dir, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if name, ok := strings.CutPrefix(line, "test/"); ok {
			tests = append(tests, name)
			continue
		}
		regular = append(regular, line)
	}
	return regular, tests, nil
}

// darwinNames reads the names those files declare: functions, methods (as
// Type.Name and, for a pointer receiver, the same) and package-level variables.
func darwinNames(files []string) (map[string]bool, error) {
	names := map[string]bool{}
	for _, path := range files {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					names[d.Name.Name] = true
					continue
				}
				for _, field := range d.Recv.List {
					if named := receiverName(field.Type); named != "" {
						names[named+"."+d.Name.Name] = true
					}
				}
			case *ast.GenDecl:
				if d.Tok != token.VAR {
					continue
				}
				for _, spec := range d.Specs {
					value, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, name := range value.Names {
						if name.Name != "_" {
							names[name.Name] = true
						}
					}
				}
			}
		}
	}
	return names, nil
}

// receiverName is the type name of a method receiver, through one or two
// pointers/stars, so `*T`, `T` and `**T` answer the same.
func receiverName(expr ast.Expr) string {
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		case *ast.IndexExpr: // a generic receiver: T[P]
			expr = e.X
		case *ast.Ident:
			return e.Name
		default:
			return ""
		}
	}
}

func derive(dirs []string) ([]entry, error) {
	selected := map[entry]bool{}
	for _, dir := range dirs {
		dir = strings.TrimSuffix(dir, "/")
		if _, err := os.Stat(dir); err != nil {
			return nil, fmt.Errorf("%s: %w", dir, err)
		}
		// ONE DIRECTORY AT A TIME, and that is a memory bound as well as the
		// boundary the analysis already has: SSA over the whole OS set at once
		// holds every one of its packages' bodies, which is gigabytes, while one
		// directory's universe is its own code and its dependencies.
		got, err := deriveDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range got {
			selected[e] = true
		}
	}

	out := make([]entry, 0, len(selected))
	for e := range selected {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].pkg != out[j].pkg {
			return out[i].pkg < out[j].pkg
		}
		return out[i].test < out[j].test
	})
	return out, nil
}

// deriveDir is the analysis for one directory: load it with its tests, build the
// graph, and walk from every test function.
func deriveDir(dir string) ([]entry, error) {
	files, linuxOnlyTests, err := darwinOnlyFiles(dir)
	if err != nil {
		return nil, err
	}
	names, err := darwinNames(files)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		// No darwin implementation in this directory: nothing here can need a
		// Mac, and loading it would only cost time.
		return nil, nil
	}

	// THE ROOTS' SYNTAX, AND NOTHING ELSE'S: NeedDeps+NeedSyntax would parse the
	// whole dependency graph so SSA could build every dependency's body — weight
	// with no edge this analysis may follow (the walk stops at the directory).
	// Dependencies still arrive with types, from export data, which is all a
	// cross-package call site needs.
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedTypesSizes,
		Tests:      true,
		BuildFlags: []string{"-tags", tags},
	}
	pkgs, err := packages.Load(cfg, "./"+dir+"/...")
	if err != nil {
		return nil, err
	}
	if n := packages.PrintErrors(pkgs); n > 0 {
		return nil, fmt.Errorf("%s: %d package error(s) above; a selection cannot be computed from a build that does not load", dir, n)
	}

	// ONLY THE ANALYSED DIRECTORY'S OWN PACKAGES GET BODIES. Dependencies are
	// types-only: the walk never leaves the directory, so a dependency's body is
	// weight with no edge this analysis may follow — and building internal/app's
	// whole dependency graph as SSA is gigabytes on a 6 GB box.
	prog, ssaPkgs := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()
	graph := cha.CallGraph(prog)

	// dirOf answers the analysed directory a package (or test variant) is in.
	base := modulePath + dir
	dirOf := func(p *ssa.Package) string {
		if p == nil || p.Pkg == nil {
			return ""
		}
		path := p.Pkg.Path()
		if path == base || strings.HasPrefix(path, base+"[") ||
			strings.HasPrefix(path, base+"_test") || strings.HasPrefix(path, base+".test") {
			return dir
		}
		return ""
	}

	darwinName := func(fn *ssa.Function) bool {
		if fn == nil {
			return false
		}
		if names[fn.Name()] {
			return true
		}
		if recv := fn.Signature.Recv(); recv != nil {
			if named, ok := recv.Type().(*types.Named); ok && names[named.Obj().Name()+"."+fn.Name()] {
				return true
			}
		}
		return false
	}

	linuxOnly := map[string]bool{}
	for _, name := range linuxOnlyTests {
		linuxOnly[name] = true
	}
	var out []entry
	for _, pkg := range ssaPkgs {
		if dirOf(pkg) != dir {
			continue
		}
		for name, member := range pkg.Members {
			if !strings.HasPrefix(name, "Test") || name == "TestMain" {
				continue
			}
			test, ok := member.(*ssa.Function)
			if !ok || test == nil || test.Blocks == nil {
				continue
			}
			if !test.Pos().IsValid() {
				continue
			}
			file := prog.Fset.Position(test.Pos()).Filename
			if linuxOnly[filepath.Base(file)] {
				// Declared in a test file the darwin build does not compile: it
				// cannot run on the Mac, and selecting it would remove it from
				// Linux as well.
				continue
			}
			if reachesDarwin(graph, test, dir, dirOf, darwinName, names) {
				out = append(out, entry{pkg: dir, test: name})
			}
		}
	}
	return out, nil
}

// reachesDarwin walks the call graph from one test and answers whether it
// arrives at a name the darwin-only files declare. The walk never leaves dir:
// that is the boundary the command's doc explains.
func reachesDarwin(
	graph *callgraph.Graph,
	test *ssa.Function,
	dir string,
	dirOf func(*ssa.Package) string,
	darwinName func(*ssa.Function) bool,
	names map[string]bool,
) bool {
	root := graph.Nodes[test]
	if root == nil {
		return false
	}
	seen := map[*callgraph.Node]bool{root: true}
	queue := []*callgraph.Node{root}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		if node == nil || node.Func == nil {
			continue
		}
		if darwinName(node.Func) || referencesDarwinVar(node.Func, names) {
			return true
		}
		for _, edge := range node.Out {
			callee := edge.Callee
			if callee == nil || callee.Func == nil || seen[callee] {
				continue
			}
			if dirOf(callee.Func.Pkg) != dir {
				// A CONSUMER is covered by that package's own tests; the walk
				// stops at the directory edge deliberately (see the command doc).
				continue
			}
			seen[callee] = true
			queue = append(queue, callee)
		}
	}
	return false
}

// referencesDarwinVar reports a read or write of a package-level variable the
// darwin-only files declare: the variable exists in that build, and reading it
// cannot happen without it.
func referencesDarwinVar(fn *ssa.Function, names map[string]bool) bool {
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			for _, operand := range instr.Operands(nil) {
				if operand == nil {
					continue
				}
				if global, ok := (*operand).(*ssa.Global); ok && global.Name() != "" && names[global.Name()] {
					return true
				}
			}
		}
	}
	return false
}
