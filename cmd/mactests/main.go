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
	"go/constant"
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

// repoRoot is the module root, found by walking up for go.mod. Every path this
// command takes is relative to it (the Makefile passes OS_PKG_DIRS), so the work
// is done there whatever directory the command was started from — which is what
// lets its own tests run from cmd/mactests.
var repoRoot = func() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		if filepath.Dir(dir) == dir {
			return wd
		}
	}
}()

// modulePath is the import path prefix every analysed directory is under.
const modulePath = "github.com/shady2k/nocx/"

// defaultTags are the build tags the untagged macOS test step runs with, so the
// graph is the graph those tests have. gtk3 is harmless on darwin (the platform
// tag list is empty there) and nocx_framecheck is the test-only detector ci.yml
// enables. The TAGGED half of the selection runs with nocx_local_ssh as well
// (`--tags`), because the tag decides which files exist: a tagged test can reach
// a darwin implementation just as an untagged one can, and it is a different
// analysis.
const defaultTags = "gtk3,nocx_framecheck"

func main() {
	write := flag.String("write", "", "write the selection to this file")
	check := flag.String("check", "", "verify this file is the current selection")
	tags := flag.String("tags", defaultTags, "build tags the analysed pass runs with")
	flag.Parse()
	dirs := flag.Args()
	if len(dirs) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mactests [--write <path> | --check <path>] <pkg-dir>...")
		os.Exit(2)
	}

	selection, err := derive(dirs, *tags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mactests: %v\n", err)
		os.Exit(1)
	}
	rendered := render(selection, *tags)

	switch {
	case *write != "":
		if err := os.WriteFile(*write, []byte(rendered), 0o644); err != nil { //nolint:gosec // a committed text file, and 0644 is what the repository stores
			fmt.Fprintf(os.Stderr, "mactests: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "mactests: wrote %d test(s) to %s\n", len(selection), *write)
	case *check != "":
		// AND IT SAYS WHAT WOULD CHANGE, by name, because "not current" with no
		// difference is a report nobody can act on (measured: the first macOS run
		// of this check said exactly that, and the fix took a second round).
		current, err := os.ReadFile(*check)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "mactests: %s does not exist; `make mac-selection` writes it\n", *check)
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "mactests: %v\n", err)
			os.Exit(1)
		}
		have := map[entry]bool{}
		for _, line := range strings.Split(string(current), "\n") {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			pkg, test, ok := strings.Cut(line, "\t")
			if !ok {
				fmt.Fprintf(os.Stderr, "mactests: %s holds a line that is not pkg<TAB>test: %q\n", *check, line)
				os.Exit(1)
			}
			have[entry{pkg: pkg, test: test}] = true
		}
		want := map[entry]bool{}
		for _, e := range selection {
			want[e] = true
		}
		var added, removed []entry
		for e := range want {
			if !have[e] {
				added = append(added, e)
			}
		}
		for e := range have {
			if !want[e] {
				removed = append(removed, e)
			}
		}
		if len(added)+len(removed) > 0 {
			sort.Slice(added, func(i, j int) bool { return lessEntry(added[i], added[j]) })
			sort.Slice(removed, func(i, j int) bool { return lessEntry(removed[i], removed[j]) })
			fmt.Fprintf(os.Stderr, "mactests: %s differs from this build's derivation (%d to add, %d to remove):\n", *check, len(added), len(removed))
			for _, e := range added {
				fmt.Fprintf(os.Stderr, "+ %s\t%s\n", e.pkg, e.test)
			}
			for _, e := range removed {
				fmt.Fprintf(os.Stderr, "- %s\t%s\n", e.pkg, e.test)
			}
			fmt.Fprintf(os.Stderr, "\n`make mac-selection` rewrites it from this build.\n")
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "mactests: %s is current (%d test(s))\n", *check, len(selection))
	default:
		fmt.Print(rendered)
	}
}

// splitTags turns the flag's comma-joined spelling into the list go list and
// go/packages each want.
func splitTags(tags string) []string {
	if tags == "" {
		return nil
	}
	parts := strings.Split(tags, ",")
	out := parts[:0]
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// entry is one selected test: the package directory as the Makefile spells it
// (`internal/pty`) and the test function.
type entry struct {
	pkg  string
	test string
}

// lessEntry orders a selection the way every file here is ordered.
func lessEntry(a, b entry) bool {
	if a.pkg != b.pkg {
		return a.pkg < b.pkg
	}
	return a.test < b.test
}

func render(selection []entry, tags string) string {
	var b strings.Builder
	b.WriteString("# macOS-only tests REACHED through darwin implementations (nocx-ouru5.1.13).\n")
	b.WriteString("#\n")
	b.WriteString("# Generated by `make mac-only-tests` (go run ./cmd/mactests: SSA + CHA over the\n")
	b.WriteString("# host build, with tags " + tags + "), and re-derived by ci-mac before its tests\n")
	b.WriteString("# fail when this file disagrees. The Mac runs exactly these, plus the tests in\n")
	b.WriteString("# darwin-constrained test FILES and the one LISTED entry, which\n")
	b.WriteString("# scripts/mac-only-tests.mjs derives on any platform.\n")
	b.WriteString("#\n")
	b.WriteString("# NEVER EDIT IT BY HAND: the rule is the call graph plus the names the darwin\n")
	b.WriteString("# build declares, and cmd/mactests is its statement. That command also says why\n")
	b.WriteString("# the analysis runs on the host build rather than a cross-cgo darwin one.\n")
	b.WriteString("#\n")
	b.WriteString("# The LINUX job runs the whole OS set and skips nothing: a test that reaches a\n")
	b.WriteString("# platform seam exercises the linux implementation there, and Linux ships too.\n")
	for _, e := range selection {
		fmt.Fprintf(&b, "%s\t%s\n", e.pkg, e.test)
	}
	return b.String()
}

// fileSets is one directory's build classification, asked of the go tool under
// both GOOS values with the pass's tags: which NON-TEST files only darwin
// compiles (the implementations a test can reach), and which TEST files only one
// of the two builds compiles.
type fileSets struct {
	// darwinImpl are the non-test files the darwin build compiles and the linux
	// build does not.
	darwinImpl []string
	// darwinTests are TEST files only the darwin build compiles. Their tests are
	// the DERIVED selection already — macOS-only by their own file — so the
	// reachability walk must not list them again: on a Mac they load, they run,
	// and they call the darwin implementation directly, which is how the
	// committed file stopped matching what `make mac-check` re-derived.
	darwinTests map[string]bool
	// linuxTests are TEST files only the linux build compiles. A test declared in
	// one cannot be the Mac's, and selecting it would also take it off the Linux
	// side, which is the only side that runs it.
	linuxTests map[string]bool
	// darwinTestNames and linuxTestNames are the tests those two file sets
	// DECLARE. The walk skips by name as well as by file position: the position
	// is only visible in the build being analysed, and the analysis that has to
	// be exactly right runs on the Mac, where the darwin-only test files exist
	// and their tests load and run like any other.
	darwinTestNames map[string]bool
	linuxTestNames  map[string]bool
}

// classify asks the go tool which files each build compiles in this directory.
// Listing needs no compiler for either platform, so this half of the derivation
// runs anywhere.
func classify(dir string, tags []string) (fileSets, error) {
	linuxRegular, linuxTests, err := listedFiles(dir, "linux", tags)
	if err != nil {
		return fileSets{}, err
	}
	darwinRegular, darwinTests, err := listedFiles(dir, "darwin", tags)
	if err != nil {
		return fileSets{}, err
	}
	onLinux := map[string]bool{}
	for _, name := range linuxRegular {
		onLinux[name] = true
	}
	onLinuxTest := map[string]bool{}
	for _, name := range linuxTests {
		onLinuxTest[name] = true
	}
	onDarwinTest := map[string]bool{}
	for _, name := range darwinTests {
		onDarwinTest[name] = true
	}
	sets := fileSets{
		darwinTests:     map[string]bool{},
		linuxTests:      map[string]bool{},
		darwinTestNames: map[string]bool{},
		linuxTestNames:  map[string]bool{},
	}
	for _, name := range darwinRegular {
		if !onLinux[name] {
			sets.darwinImpl = append(sets.darwinImpl, filepath.Join(repoRoot, dir, name))
		}
	}
	for _, name := range darwinTests {
		if !onLinuxTest[name] {
			sets.darwinTests[name] = true
		}
	}
	for _, name := range linuxTests {
		if !onDarwinTest[name] {
			sets.linuxTests[name] = true
		}
	}
	for name := range sets.darwinTests {
		names, err := testNamesIn(filepath.Join(repoRoot, dir, name))
		if err != nil {
			return fileSets{}, err
		}
		for _, test := range names {
			sets.darwinTestNames[test] = true
		}
	}
	for name := range sets.linuxTests {
		names, err := testNamesIn(filepath.Join(repoRoot, dir, name))
		if err != nil {
			return fileSets{}, err
		}
		for _, test := range names {
			sets.linuxTestNames[test] = true
		}
	}
	return sets, nil
}

// listedFiles asks the go tool which files a build for goos — with this pass's
// tags — compiles in this directory, regular and test files apart. Listing needs
// no compiler of that platform, so this half runs anywhere.
func listedFiles(dir, goos string, tags []string) (regular []string, tests []string, err error) {
	const format = "{{range .GoFiles}}{{.}}\n{{end}}{{range .TestGoFiles}}test/{{.}}\n{{end}}{{range .XTestGoFiles}}test/{{.}}\n{{end}}"
	args := []string{"list", "-f", format}
	if len(tags) > 0 {
		args = append(args, "-tags="+strings.Join(tags, ","))
	}
	args = append(args, "./"+strings.TrimSuffix(dir, "/"))
	cmd := exec.Command("go", args...) //nolint:gosec // the directory is this repository's own package path, passed by the Makefile from OS_PKG_DIRS
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GOOS="+goos, "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, nil, fmt.Errorf("go list GOOS=%s ./%s: %w: %s", goos, dir, err, strings.TrimSpace(string(out)))
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

// testNamesIn parses one test file and answers the test functions it declares.
func testNamesIn(path string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		if strings.HasPrefix(fn.Name.Name, "Test") && fn.Name.Name != "TestMain" {
			out = append(out, fn.Name.Name)
		}
	}
	return out, nil
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

func derive(dirs []string, tags string) ([]entry, error) {
	selected := map[entry]bool{}
	for _, dir := range dirs {
		dir = strings.TrimSuffix(dir, "/")
		if _, err := os.Stat(filepath.Join(repoRoot, dir)); err != nil {
			return nil, fmt.Errorf("%s: %w", dir, err)
		}
		// ONE DIRECTORY AT A TIME, and that is a memory bound as well as the
		// boundary the analysis already has: SSA over the whole OS set at once
		// holds every one of its packages' bodies, which is gigabytes, while one
		// directory's universe is its own code and its dependencies.
		got, err := deriveDir(dir, splitTags(tags))
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
	sort.Slice(out, func(i, j int) bool { return lessEntry(out[i], out[j]) })
	return out, nil
}

// deriveDir is the analysis for one directory: load it with its tests, build the
// graph, and walk from every test function.
func deriveDir(dir string, tags []string) ([]entry, error) {
	sets, err := classify(dir, tags)
	if err != nil {
		return nil, err
	}
	names, err := darwinNames(sets.darwinImpl)
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
		Dir:        repoRoot,
		BuildFlags: []string{"-tags", strings.Join(tags, ",")},
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

	// EVERY TEST THIS PACKAGE RUNS, and the ones the file rule already owns. A
	// test declared in a darwin-only file is the derived selection (on a Mac it
	// loads and calls the darwin implementation directly), a linux-only one does
	// not exist on the Mac, and neither may be listed here.
	tests := map[string]*ssa.Function{}
	owned := map[string]bool{}
	for _, pkg := range ssaPkgs {
		if dirOf(pkg) != dir {
			continue
		}
		for name, member := range pkg.Members {
			if !strings.HasPrefix(name, "Test") || name == "TestMain" {
				continue
			}
			test, ok := member.(*ssa.Function)
			if !ok || test == nil || test.Blocks == nil || !test.Pos().IsValid() {
				continue
			}
			tests[name] = test
			file := filepath.Base(prog.Fset.Position(test.Pos()).Filename)
			if sets.darwinTestNames[name] || sets.linuxTestNames[name] || sets.linuxTests[file] || sets.darwinTests[file] {
				owned[name] = true
			}
		}
	}
	runnable := map[string]bool{}
	for name := range tests {
		if !owned[name] {
			runnable[name] = true
		}
	}

	// ONE TEST'S ANSWER, WITH ITS CHILDREN'S (round 6). A test that re-executes
	// this binary for another test of the package reaches whatever THAT test
	// reaches; the recursion is over named children, and the memo makes a cycle
	// terminate (a test that names itself, as the child helper does).
	memo := map[string]bool{}
	visiting := map[string]bool{}
	var selected func(name string) bool
	selected = func(name string) bool {
		if answer, ok := memo[name]; ok {
			return answer
		}
		if visiting[name] {
			return false
		}
		test, ok := tests[name]
		if !ok {
			return false
		}
		visiting[name] = true
		hit, children := walk(graph, test, dir, dirOf, darwinName, names, runnable)
		answer := hit
		for _, child := range children {
			if child == name || answer {
				continue
			}
			answer = selected(child)
		}
		visiting[name] = false
		memo[name] = answer
		return answer
	}

	var out []entry
	for name := range runnable {
		if selected(name) {
			out = append(out, entry{pkg: dir, test: name})
		}
	}
	return out, nil
}

// walk is ONE test's reachability before the tests it re-executes are counted:
// whether its in-package closure arrives at a darwin name or variable, and which
// other tests of this package it names as a CHILD to run.
//
// THE CHILD CASE (review of PR #776, round 6). Some tests run the test binary
// again — `exec.Command(os.Args[0], "-test.run=^TestChild$")`, or a shell line
// built the same way — and do their real work in the child. The parent reaches
// darwin code only through the child's body, and the child on its own measures
// nothing a person asked for, so neither is selected by edges alone. The names
// are read off a string constant: the `-test.run=` argument, or a constant that
// IS a test's name (a helper that passes it as a separate argv entry).
func walk(
	graph *callgraph.Graph,
	test *ssa.Function,
	dir string,
	dirOf func(*ssa.Package) string,
	darwinName func(*ssa.Function) bool,
	names map[string]bool,
	tests map[string]bool,
) (hit bool, children []string) {
	root := graph.Nodes[test]
	if root == nil {
		return false, nil
	}
	seen := map[*callgraph.Node]bool{root: true}
	seenChild := map[string]bool{}
	queue := []*callgraph.Node{root}
	// enqueue follows a FUNCTION VALUE the test hands to something else — a
	// t.Run callback, a goroutine body — which no call edge reaches: the callee
	// of that call is testing.T.Run (a dependency, whose body this analysis does
	// not have), and the callback is only an argument. Without this, a test whose
	// whole subject lives in its subtests is invisible to the walk — measured on
	// internal/pty's TestClose_EndsAnInteractiveShell and
	// TestRunCancellationWakesActualReadAndInput, whose callbacks call NewLocal
	// and the wake pipe, and which the selector therefore dropped.
	enqueue := func(fn *ssa.Function) {
		if fn == nil {
			return
		}
		node := graph.Nodes[fn]
		if node == nil || seen[node] {
			return
		}
		seen[node] = true
		queue = append(queue, node)
	}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		if node == nil || node.Func == nil {
			continue
		}
		if darwinName(node.Func) || referencesDarwinVar(node.Func, names) {
			hit = true
		}
		for _, block := range node.Func.Blocks {
			for _, instr := range block.Instrs {
				for _, operand := range instr.Operands(nil) {
					if operand == nil {
						continue
					}
					switch value := (*operand).(type) {
					case *ssa.Function:
						if dirOf(value.Pkg) == dir {
							enqueue(value)
						}
					case *ssa.MakeClosure:
						if fn, ok := value.Fn.(*ssa.Function); ok && dirOf(fn.Pkg) == dir {
							enqueue(fn)
						}
					case *ssa.Const:
						if child, ok := reexecutedTest(value, tests); ok && !seenChild[child] {
							seenChild[child] = true
							children = append(children, child)
						}
					}
				}
			}
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
	return hit, children
}

// reexecutedTest reads the test a re-exec names: `-test.run=<pattern>` with the
// anchors trimmed, or a constant that IS a test's name (a helper passing it as a
// separate argv entry). Anything else — a name in a message, a pattern that is
// not one test — names nothing.
func reexecutedTest(c *ssa.Const, tests map[string]bool) (string, bool) {
	if c == nil || c.Value == nil {
		return "", false
	}
	if c.Value.Kind() != constant.String {
		return "", false
	}
	text := constant.StringVal(c.Value)
	if at := strings.Index(text, "-test.run="); at >= 0 {
		pattern := text[at+len("-test.run="):]
		if end := strings.IndexAny(pattern, " \t\n'\"&|;"); end >= 0 {
			pattern = pattern[:end]
		}
		pattern = strings.TrimPrefix(pattern, "^")
		pattern = strings.TrimSuffix(pattern, "$")
		if tests[pattern] {
			return pattern, true
		}
	}
	if tests[text] {
		return text, true
	}
	return "", false
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
