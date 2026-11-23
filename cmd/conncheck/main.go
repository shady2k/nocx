// Command conncheck reports "connection-owner" facts: every place OUTSIDE
// the SSH transport that names a fact about a connection, resolved by
// IDENTITY rather than by spelling (nocx-oalo1.3.4).
//
// It is the analysis half of .githooks/check-connection-owners.mjs, which
// keeps the policy (rules, allowlist, baseline) and the CLI. Run it through
// that check; the contract is the JSON on stdout and exit 2 on any failure to
// analyse the tree (a tree that cannot be loaded cannot be proven clean).
//
// The facts it answers, each with the object it resolved to (never the text
// of the line):
//
//	net-dial              a reference to net.Dial, DialTimeout, DialUDP,
//	                      DialTCP, DialIP, DialUnix, the net.Dialer type, or
//	                      one of its dial methods — unless the call's network
//	                      argument is the CONSTANT "unix", evaluated through
//	                      go/types. A comment mentioning unix is not an
//	                      argument, and an aliased import cannot hide the
//	                      object.
//	xcrypto-ssh-import    a reference to any object of golang.org/x/crypto/ssh
//	                      or its subpackages, by package path and name. The
//	                      policy decides which key-format objects are allowed
//	                      and in which file; this program has no allowlist.
//	exit-error-read       a reference to the ssh.ExitError TYPE, whatever the
//	                      import is called.
//	keepalive-request     a call of SendRequest on an *ssh.Client or an
//	                      ssh.Conn, whatever the request name is — a second
//	                      prober is a second prober.
//	transport-re-export   IN a transport package: an exported package-level
//	                      var or func that hands out a net dial or ssh.Dial /
//	                      NewClientConn, which would make every caller of it a
//	                      second owner without naming one.
//	helper-decider        a reference to the helper client's Dial, to the host
//	                      mode store's Lookup, to the machine consent store's
//	                      Lookup, or to git/helper's NewFactory: the two inputs,
//	                      the one dial that decides whether a far helper serves a
//	                      connection, and the Git factory that must be built on
//	                      the client that decision produced (ALX-641, design
//	                      §5.10 C23). The decision has one owner,
//	                      internal/farhelper.
//	panel-host-path       a reference to ssh.NewFSConn, to proto.OpSamplePorts,
//	                      to a lease acquisition ((*client.Client).
//	                      AcquireProbeLease), to a ProbeLease exec (Uname,
//	                      Home, SamplePorts, Completion, Inflate,
//	                      CommandNames), or to the argument of
//	                      discovery.NewScheduler whose type is not the far
//	                      helper's own: the roads by which a panel reaches a
//	                      host without the far helper (design §5.10 C24). A
//	                      panel takes the connection's helper client from
//	                      internal/farhelper, and the connector it hands the
//	                      scheduler is a concrete type declared THERE, by value
//	                      or behind a pointer — a named interface declared in
//	                      the same package is a shape, not the axis.
//
// WHAT IT CANNOT SEE, stated here because the check's header repeats it: a
// call through an interface; a method value (for example `f := x().Lookup`)
// in a file without a tracked import; or a value passed across packages in a
// field or channel. For net dials, a network named by a non-constant variable
// is also not exempted as unix. Code that does not compile cannot be analysed.
// It does not read the renderer, which nocx-oalo1.3.5 owns.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Fact is one place that names a connection fact.
type Fact struct {
	Rule   string `json:"rule"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column,omitempty"`
	Func   string `json:"func"`
	Object string `json:"object"`
	// Detail carries the constant network argument a net-dial fact was NOT
	// exempted for, or the request name a keepalive test used. Empty when
	// there is nothing to add.
	Detail string `json:"detail,omitempty"`
}

const (
	sshPkg = "golang.org/x/crypto/ssh"
	netPkg = "net"

	// nocxModule is this module's own import prefix, and nocxInternal its
	// packages. The two rules that fence the far helper (design §5.10's C23
	// and C24) are facts about THIS repository's packages, so they carry no
	// external import to resolve: go/types names the object and the policy
	// decides where it may live.
	nocxModule             = "github.com/shady2k/nocx/"
	nocxInternal           = nocxModule + "internal/"
	discoveryConnectorPath = "github.com/shady2k/nocx/internal/discovery"
)

func main() {
	module := flag.String("module", ".", "module root (the directory holding go.mod)")
	scope := flag.String("scope", "", "report only files under this directory; paths are labelled relative to it (default: the module root)")
	tags := flag.String("tags", "", "comma-separated build tags for the load")
	pkgsFlag := flag.String("pkgs", "./...", "comma-separated package patterns to load")
	flag.Parse()

	root, err := filepath.Abs(*module)
	if err != nil {
		fail(err)
	}
	label := *scope
	if label == "" {
		label = root
	}
	labelAbs, err := filepath.Abs(label)
	if err != nil {
		fail(err)
	}

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:   root,
		Env:   os.Environ(),
		Tests: false,
	}
	if *tags != "" {
		cfg.BuildFlags = []string{"-tags=" + *tags}
	}
	patterns := strings.Split(*pkgsFlag, ",")
	for i := range patterns {
		patterns[i] = strings.TrimSpace(patterns[i])
	}
	// The default scope analyses only the packages that can possibly carry a
	// fact: those whose files import net or golang.org/x/crypto/ssh. That is a
	// syntactic prefilter (go/parser, imports only) and it is SOUND — a
	// reference to an object of a package requires importing it — while it
	// keeps the type-checked load to a few dozen packages instead of the
	// whole module, which is what makes the check affordable on a machine
	// with no cgo toolchain.
	if len(patterns) == 1 && patterns[0] == "./..." {
		candidates, cerr := candidatePatterns(root, labelAbs)
		if cerr != nil {
			fail(cerr)
		}
		patterns = candidates
	}
	// No cgo: every prerequisite of an SSH connection is Go, and the native
	// archives a cgo build needs (libghostty-vt, webkit) are not something a
	// check may demand of a machine whose only job is reading this tree.
	cfg.Env = append(os.Environ(), "CGO_ENABLED=0")
	// An empty candidate list is a tree with nothing to analyse, and it is NOT
	// a licence to load the current directory: `go list` with no arguments
	// answers ".", so a scope whose files carry no candidate would be analysed
	// as the module root instead of as itself.
	var pkgs []*packages.Package
	if len(patterns) > 0 {
		pkgs, err = packages.Load(cfg, patterns...)
		if err != nil {
			fail(err)
		}
	}

	var loadErrs []string
	facts := &collector{abs: labelAbs, root: root}
	for _, p := range pkgs {
		// A candidate directory with no buildable files under this tag set is
		// not a failure: the other pass (with the tags) analyses it. Every
		// other error means the tree cannot be proven clean.
		excluded := len(p.GoFiles) == 0 && len(p.CompiledGoFiles) == 0
		for _, e := range p.Errors {
			if excluded && strings.Contains(e.Msg, "build constraints exclude all Go files") {
				continue
			}
			// fmt.Sprint rather than the package error's own text method: the
			// log-context ratchet reads such a call as a log site, and this is
			// not one.
			loadErrs = append(loadErrs, fmt.Sprint(e))
		}
		if p.TypesInfo == nil || p.Types == nil || p.Fset == nil {
			continue
		}
		facts.packageFacts(p)
	}
	if len(loadErrs) > 0 {
		sort.Strings(loadErrs)
		for _, e := range loadErrs {
			fmt.Fprintln(os.Stderr, "conncheck: "+e)
		}
		fail(fmt.Errorf("%d package load error(s): the tree cannot be analysed, so it cannot be proven clean", len(loadErrs)))
	}
	sort.Slice(facts.list, func(i, j int) bool {
		a, b := facts.list[i], facts.list[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		return a.Object < b.Object
	})
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(struct {
		Facts []Fact `json:"facts"`
	}{facts.list}); err != nil {
		fail(err)
	}
}

// netObjectNames are every name of package net the analysis can report. A
// file that imports net for SplitHostPort alone cannot carry a fact, and
// treating it as a candidate would force a type-check of a package a
// cgo-less machine cannot build.
var netObjectNames = map[string]bool{
	"Dial": true, "DialTimeout": true, "DialUDP": true, "DialTCP": true,
	"DialIP": true, "DialUnix": true, "Dialer": true,
}

// nocxTrackedPkgs are this module's own packages whose objects the C23/C24
// rules name. A file that imports one of them is a candidate: the rule's fact
// is a REFERENCE to an object of that package, and an object cannot be
// referenced without importing the package. Generous on purpose — the
// type-checked walk still decides, by identity.
var nocxTrackedPkgs = map[string]bool{
	nocxInternal + "helper/client":  true,
	nocxInternal + "helper/consent": true,
	nocxInternal + "helper/proto":   true,
	nocxInternal + "git/helper":     true,
	nocxInternal + "hostmode":       true,
	nocxInternal + "ssh":            true,
	discoveryConnectorPath:          true,
}

// nocxTargetNames are the names a C23/C24 fact is reached by through one of
// the tracked packages' aliases: the rule's own objects, and the types whose
// methods the rule reports. The receiver type belongs here because
// `*hostmode.Store` is what a file writes in order to hold the store, and the
// method on it is spelled `s.Lookup`.
var nocxTargetNames = map[string]bool{
	"Dial": true, "Lookup": true, "NewFSConn": true, "OpSamplePorts": true,
	"AcquireProbeLease": true, "NewFactory": true, "Store": true,
	// A ProbeLease's exec surface, and the type that carries it.
	"Uname": true, "Home": true, "SamplePorts": true,
	"Completion": true, "Inflate": true, "CommandNames": true, "ProbeLease": true,
	// The helper client type, whose lease acquisition the C24 rule reports.
	"Client": true,
}

// nocxCallNames are the names whose CALL is a fact wherever the receiver came
// from: a method is reached through a value, and the value's type may have been
// named in another package.
var nocxCallNames = map[string]bool{
	"Lookup": true, "AcquireProbeLease": true, "NewFactory": true,
	"Uname": true, "Home": true, "SamplePorts": true,
	"Completion": true, "Inflate": true, "CommandNames": true,
	"NewFSConn": true, "NewScheduler": true,
}

// mentionsTarget reports whether a file can carry a fact the analysis reports,
// so that the type-checked load can skip the rest of the tree. It is a
// candidate filter only — every decision is still made by go/types.
//
// For net and x/crypto/ssh the marker is a selector on the import's alias: an
// object of either cannot be referenced without naming it, and the net names
// are narrowed because a file importing net for SplitHostPort alone cannot
// carry a dial.
//
// For this module's own packages there are two markers, and both are needed.
// One is a selector on a tracked import's alias whose NAME the rules know
// (nocxTargetNames). A method is reached through its receiver, not through its
// package, so the second marker is a CALL of a name the rules know whatever
// the receiver is: `s.Lookup`, `l.SamplePorts`, `a.client.AcquireProbeLease`
// and `helper.Lookup` are all references the walk reports, and none of them
// names a package. The receiver's type name is usually in the same file (a
// parameter or a field must be typed), which is why the first marker alone
// covers most of them; a value that arrives from another package's function is
// why the second exists.
//
// This filter CANNOT be "any file importing a tracked package": one of them is
// internal/helper/session, which imports internal/ssh and reaches
// internal/emulator/ghostty, a package made of cgo files and therefore absent
// from this load. A filter that admitted it would turn a clean tree into a load
// failure on every developer's machine and in CI.
func mentionsTarget(f *ast.File) bool {
	type kind string
	const (
		kindNet  kind = "net"
		kindSSH  kind = "ssh"
		kindNocx kind = "nocx"
	)
	aliases := map[string]kind{}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		// The backquoted-path case closes the C23/C24 candidate-filter gap.
		// Do not broaden legacy external-package selection at the same time.
		if strings.HasPrefix(imp.Path.Value, "`") && !nocxTrackedPkgs[path] {
			continue
		}
		var k kind
		switch {
		case nocxTrackedPkgs[path]:
			k = kindNocx
		case strings.HasPrefix(path, sshPkg):
			k = kindSSH
		case path == netPkg:
			k = kindNet
		default:
			continue
		}
		if imp.Name != nil {
			switch imp.Name.Name {
			case ".":
				return true // an object of the package is in file scope
			case "_":
				continue
			}
			aliases[imp.Name.Name] = k
			continue
		}
		parts := strings.Split(path, "/")
		aliases[parts[len(parts)-1]] = k
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		switch x := n.(type) {
		case *ast.CallExpr:
			// A method is reached through its receiver, so its call name is
			// the candidate marker even when its concrete type is returned by
			// another package. This may load a cgo-only package if it later
			// acquires a matching method name; that must fail visibly rather
			// than silently hide a fact.
			sel, ok := unparen(x.Fun).(*ast.SelectorExpr)
			if ok && nocxCallNames[sel.Sel.Name] {
				found = true
				return false
			}
			return true
		case *ast.SelectorExpr:
			id, ok := x.X.(*ast.Ident)
			if !ok {
				return true
			}
			k, ok := aliases[id.Name]
			if !ok {
				return true
			}
			if k == kindSSH || (k == kindNet && netObjectNames[x.Sel.Name]) ||
				(k == kindNocx && nocxTargetNames[x.Sel.Name]) {
				found = true
				return false
			}
			return true
		}
		return true
	})
	return found
}

// candidatePatterns answers the package patterns a full-tree run needs: one
// per directory holding a candidate file, found by walking the scope. The scan
// is syntax-only and ignores build tags on purpose — a file behind a tag is
// still a second owner, and the two passes decide whether it compiles.
//
// The patterns are written relative to the MODULE root, because that is the
// load's Dir; the scope only decides which directories are walked. The two are
// the same in the ordinary run and differ for a fixture, whose files are
// labelled as product paths while the module stays the repository's own.
func candidatePatterns(root, scopeAbs string) ([]string, error) {
	dirs := map[string]bool{}
	err := filepath.WalkDir(scopeAbs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != scopeAbs && (name == "vendor" || name == "testdata" || name == "node_modules" ||
				name == ".git" || name == "build" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		if mentionsTarget(f) {
			dirs[filepath.Dir(path)] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	patterns := make([]string, 0, len(dirs))
	for dir := range dirs {
		rel, rerr := filepath.Rel(root, dir)
		if rerr != nil {
			return nil, rerr
		}
		if rel == ".." || strings.HasPrefix(rel, "../") {
			return nil, fmt.Errorf("scope %s is outside the module root %s: its packages cannot be named from there", scopeAbs, root)
		}
		patterns = append(patterns, "./"+filepath.ToSlash(rel))
	}
	sort.Strings(patterns)
	return patterns, nil
}

func fail(err error) {
	// fmt.Sprint rather than the error's own method: the log-context ratchet
	// reads a `.Error(` call as a log site, and this is not one.
	fmt.Fprintln(os.Stderr, "conncheck: "+fmt.Sprint(err))
	os.Exit(2)
}

type collector struct {
	abs  string // the scope directory, absolute
	root string // the module root, absolute
	list []Fact

	// The per-package state of the unix exemption, reset in packageFacts.
	// A net.Dialer CONSTRUCTION is not itself a dial, so it is reported only
	// when the function it sits in does not dial unix and nothing else: a
	// function whose one dial is a unix socket (the MCP bridge, the
	// coordinator's discovery socket, the helper endpoint) is not an SSH
	// connection, and reporting the Dialer there would force an allowlist
	// entry that hides a real dial added later in the same file.
	startIdx     int
	dialerTypes  map[string][]int // func label -> indices of net.Dialer facts
	unixDials    map[string]int
	nonUnixDials map[string]int
}

// packageFacts walks one loaded package and appends every fact in it.
func (c *collector) packageFacts(p *packages.Package) {
	info := p.TypesInfo
	c.startIdx = len(c.list)
	c.dialerTypes = map[string][]int{}
	c.unixDials = map[string]int{}
	c.nonUnixDials = map[string]int{}
	for _, file := range p.Syntax {
		name := p.Fset.File(file.Pos()).Name()
		rel, ok := c.relative(name)
		if !ok {
			continue
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				label := funcLabel(p, d)
				c.reexportFunc(d, rel, p, info)
				// The signature names objects too — parameters, results AND
				// the receiver, which is the third half of a method's type
				// and was the one left out. A plain func's receiver is a nil
				// *ast.FieldList, and a nil pointer in an interface is not a
				// nil interface: visiting it panics the walker.
				if d.Recv != nil {
					c.visitNode(d.Recv, nil, nil, label, rel, p, info)
				}
				c.visitNode(d.Type, nil, nil, label, rel, p, info)
				c.visitNode(d.Body, nil, nil, label, rel, p, info)
			case *ast.GenDecl:
				c.visitGenDecl(d, rel, p, info)
			}
		}
	}
	c.exemptUnixOnlyDialers()
}

// exemptUnixOnlyDialers drops the net.Dialer construction facts of every
// function whose only dial through a Dialer is the constant "unix".
func (c *collector) exemptUnixOnlyDialers() {
	drop := map[int]bool{}
	for label, idx := range c.dialerTypes {
		if c.unixDials[label] > 0 && c.nonUnixDials[label] == 0 {
			for _, i := range idx {
				drop[i] = true
			}
		}
	}
	if len(drop) == 0 {
		return
	}
	kept := c.list[:c.startIdx]
	for i := c.startIdx; i < len(c.list); i++ {
		if !drop[i] {
			kept = append(kept, c.list[i])
		}
	}
	c.list = kept
}

// reexportFunc reports an EXPORTED package-level func whose whole body is a
// dial: `func DialIt(...) { return net.Dial(...) }`. A helper like that hands
// the connection out under a name of its own, which is a second owner made
// invisible to every caller's file.
func (c *collector) reexportFunc(d *ast.FuncDecl, rel string, p *packages.Package, info *types.Info) {
	if d.Recv != nil || d.Body == nil || !d.Name.IsExported() || len(d.Body.List) != 1 {
		return
	}
	ret, ok := d.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	if !ok {
		return
	}
	obj := c.dialObject(call.Fun, p, info)
	if obj == "" {
		return
	}
	c.add(Fact{
		Rule: "transport-re-export", File: rel,
		Line: p.Fset.Position(d.Name.Pos()).Line,
		Func: funcLabel(p, d), Object: obj,
		Detail: "exported as " + d.Name.Name,
	})
}

// relative answers the fact label for an absolute file name, and whether the
// file is inside the scope at all.
func (c *collector) relative(name string) (string, bool) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(c.abs, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// visitGenDecl walks a package-level declaration. `const` and `type` blocks can
// name a fact too: a package-level `var d = net.Dialer{}` is where a dialer is
// built, and `var RawDial = net.Dial` is the re-export rule.
func (c *collector) visitGenDecl(d *ast.GenDecl, rel string, p *packages.Package, info *types.Info) {
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.ValueSpec:
			if s.Type != nil {
				c.visitNode(s.Type, nil, nil, "<package>", rel, p, info)
			}
			for _, v := range s.Values {
				c.visitNode(v, nil, nil, "<package>", rel, p, info)
			}
			// The re-export rule: an exported package-level var whose value
			// is a dial function hands the connection out to every caller.
			if d.Tok == token.VAR {
				for i, name := range s.Names {
					if i >= len(s.Values) || !name.IsExported() {
						continue
					}
					if obj := c.dialObject(s.Values[i], p, info); obj != "" {
						c.add(Fact{
							Rule: "transport-re-export", File: rel,
							Line: p.Fset.Position(name.Pos()).Line,
							Func: "<package>", Object: obj,
							Detail: "exported as " + name.Name,
						})
					}
				}
			}
		case *ast.TypeSpec:
			// A generic type's constraints are part of its declaration; a
			// non-generic type's TypeParams is a nil *ast.FieldList, whose
			// interface is non-nil and cannot be walked.
			if s.TypeParams != nil {
				c.visitNode(s.TypeParams, nil, nil, "<package>", rel, p, info)
			}
			c.visitNode(s.Type, nil, nil, "<package>", rel, p, info)
		}
	}
}

// visitNode walks n and records a fact for every IDENTIFIER that names a
// target object.
//
// The identifier, not the expression around it, is what decides: `net.Dial`,
// `(net.Dial)(…)`, a dot-imported `Dial(…)`, `x := net.Dial` and
// `f(net.Dial)` are one reference each, and all four report. The same walk
// resolves method references (`c.SendRequest`, `d.DialContext`) through the
// object go/types bound them to, so an alias, a parenthesis or a method value
// changes nothing.
//
// `callee` is the call whose callee subtree this node sits in, or nil. It is
// the ONLY context a fact needs, because the unix exemption is a property of
// the call a dial is the callee of — never of the line it is written on.
func (c *collector) visitNode(n ast.Node, callee *ast.CallExpr, sel *types.Selection, label, rel string, p *packages.Package, info *types.Info) {
	switch x := n.(type) {
	case nil:
		return
	case *ast.CallExpr:
		// The callee subtree carries THIS call; the arguments do not, because
		// a dial nested in an argument is its own call and gets its own
		// context when it is visited.
		c.visitNode(x.Fun, x, nil, label, rel, p, info)
		for i, arg := range x.Args {
			c.connectorArgumentFact(x, i, arg, label, rel, p, info)
			c.visitNode(arg, nil, nil, label, rel, p, info)
		}
		return
	case *ast.FuncLit:
		// The WHOLE literal: its signature names objects as readily as its
		// body does, and skipping one half is how `var open = func(c
		// *ssh.Client) { … }` used to produce no fact at all. A closure
		// inherits the enclosing function's label, because the baseline is
		// about WHERE a fact is decided and a goroutine inside a function is
		// that function's decision.
		c.visitNode(x.Type, nil, nil, label, rel, p, info)
		c.visitNode(x.Body, nil, nil, label, rel, p, info)
		return
	case *ast.Ident:
		c.identFact(x, callee, sel, rel, label, p, info)
		return
	case *ast.SelectorExpr:
		// X may be a package name (a PkgName, not a target object) or an
		// expression; Sel is the identifier that names the object, and the
		// SELECTION (when there is one) is where a method's receiver lives —
		// an interface method's own signature has none.
		selection := info.Selections[x]
		c.visitNode(x.X, nil, nil, label, rel, p, info)
		c.visitNode(x.Sel, callee, selection, label, rel, p, info)
		return
	}
	ast.Inspect(n, func(child ast.Node) bool {
		if child == nil || child == n {
			return true
		}
		if _, ok := child.(*ast.FuncDecl); ok {
			return false
		}
		// Delegate real work to visitNode so calls, selectors and identifiers
		// keep their own handling; ast.Inspect is only the traversal here.
		if c.isTracked(child) {
			c.visitNode(child, callee, nil, label, rel, p, info)
			return false
		}
		return true
	})
}

// connectorArgumentFact reports the concrete road into the Ports scheduler.
// The parameter's declared type is the architectural seam; the argument's
// static type distinguishes the legacy probes adapter from farhelper.
func calledObject(expr ast.Expr, info *types.Info) types.Object {
	switch e := unparen(expr).(type) {
	case *ast.Ident:
		return info.Uses[e]
	case *ast.SelectorExpr:
		return info.Uses[e.Sel]
	}
	return nil
}

func isFarhelperClientArgument(arg ast.Expr, info *types.Info) bool {
	selector, ok := unparen(arg).(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Client" {
		return false
	}
	id, ok := unparen(selector.X).(*ast.Ident)
	if !ok {
		return false
	}
	obj := info.Uses[id]
	if obj == nil {
		obj = info.Defs[id]
	}
	if obj == nil {
		return false
	}
	typ := obj.Type()
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	named, ok := typ.(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == nocxInternal+"farhelper" && named.Obj().Name() == "Lease"
}

func (c *collector) connectorArgumentFact(call *ast.CallExpr, index int, arg ast.Expr, label, rel string, p *packages.Package, info *types.Info) {
	obj := calledObject(call.Fun, info)
	if obj == nil {
		if ident, ok := unparen(call.Fun).(*ast.Ident); ok && p.Types != nil {
			obj = p.Types.Scope().Lookup(ident.Name)
		}
	}
	if obj == nil {
		if ident, ok := unparen(call.Fun).(*ast.Ident); ok {
			obj = info.Uses[ident]
			if obj != nil && obj.Pkg() == nil {
				obj = nil
			}
		}
	}
	if fn, ok := obj.(*types.Func); ok && fn.Pkg() != nil && fn.Pkg().Path() == nocxInternal+"git/helper" && fn.Name() == "NewFactory" {
		if !isFarhelperClientArgument(arg, info) {
			pos := p.Fset.Position(arg.Pos())
			c.add(Fact{Rule: "helper-decider", File: rel, Line: pos.Line, Column: pos.Column, Func: label, Object: nocxInternal + "git/helper.NewFactory"})
		}
		return
	}
	var sig *types.Signature
	if fn, ok := obj.(*types.Func); ok && fn.Pkg() != nil && fn.Pkg().Path() == discoveryConnectorPath {
		sig, _ = fn.Type().(*types.Signature)
	} else if tv, ok := info.Types[unparen(call.Fun)]; ok {
		sig, _ = tv.Type.Underlying().(*types.Signature)
	}
	if sig == nil {
		if ident, ok := unparen(call.Fun).(*ast.Ident); ok {
			for _, file := range p.Syntax {
				ast.Inspect(file, func(node ast.Node) bool {
					assign, ok := node.(*ast.AssignStmt)
					if !ok {
						return true
					}
					for i, lhs := range assign.Lhs {
						name, ok := lhs.(*ast.Ident)
						if !ok || name.Name != ident.Name || i >= len(assign.Rhs) {
							continue
						}
						if rhs, ok := unparen(assign.Rhs[i]).(*ast.SelectorExpr); ok {
							obj = info.Uses[rhs.Sel]
						}
					}
					return true
				})
			}
			if fn, ok := obj.(*types.Func); ok && fn.Pkg() != nil && fn.Pkg().Path() == discoveryConnectorPath {
				sig, _ = fn.Type().(*types.Signature)
			}
		}
	}
	if sig == nil || index >= sig.Params().Len() {
		return
	}
	param, ok := sig.Params().At(index).Type().(*types.Named)
	if !ok || param.Obj().Pkg() == nil || param.Obj().Pkg().Path() != discoveryConnectorPath || param.Obj().Name() != "Connector" {
		return
	}
	argType := info.TypeOf(arg)
	if argType == nil {
		return
	}
	if farhelperOwns(argType) {
		return
	}
	qualifier := func(pkg *types.Package) string { return pkg.Path() }
	connectorType := types.TypeString(param, qualifier)
	argumentType := types.TypeString(argType, qualifier)
	c.add(Fact{
		Rule: "panel-host-path", File: rel, Line: p.Fset.Position(arg.Pos()).Line,
		Func: label, Object: connectorType + "(" + argumentType + ")",
	})
}

// isTracked reports whether a node needs its own visit (so ast.Inspect hands it
// to visitNode rather than descending blindly).
func (c *collector) isTracked(n ast.Node) bool {
	switch n.(type) {
	case *ast.CallExpr, *ast.SelectorExpr, *ast.FuncLit, *ast.Ident:
		return true
	}
	return false
}

// identFact records the fact one identifier names, if it names a target object.
// Everything about the surrounding expression — a parenthesis, a dot import, a
// call, a method value, an assignment — is irrelevant to whether the object is
// the transport's; only the call it is the callee of can exempt a dial.
func (c *collector) identFact(id *ast.Ident, callee *ast.CallExpr, sel *types.Selection, rel, label string, p *packages.Package, info *types.Info) {
	obj := info.Uses[id]
	if obj == nil || obj.Pkg() == nil {
		return
	}
	pos := p.Fset.Position(id.Pos())
	path := obj.Pkg().Path()
	fn, isFunc := obj.(*types.Func)
	if path == discoveryConnectorPath && fn != nil && fn.Name() == "NewScheduler" && callee == nil {
		c.add(Fact{Rule: "panel-host-path", File: rel, Line: pos.Line, Column: pos.Column, Func: label, Object: path + ".NewScheduler", Detail: "constructor value"})
		return
	}
	// A package-level object is one the package itself declares: a function,
	// type, constant or var. A FIELD or METHOD is an object of the type it
	// belongs to, and a reference to one (`req.Type`, `ch.Close()`) says
	// nothing about who owns the connection — the type reference beside it
	// does. The two method rules above are the exceptions, by identity.
	pkgLevel := obj.Parent() == obj.Pkg().Scope()

	if path == netPkg {
		switch {
		case obj.Name() == "Dialer" && !isFunc:
			// The type, recorded so an unix-only function can drop it below.
			c.dialerTypes[label] = append(c.dialerTypes[label], len(c.list))
			c.add(Fact{Rule: "net-dial", File: rel, Line: pos.Line, Func: label, Object: netPkg + ".Dialer"})
		case isFunc && isNetDial(obj.Name()) && pkgLevel && receiverType(fn) == nil:
			if callee != nil && c.constantUnix(callee, 0, info) {
				c.unixDials[label]++
				return
			}
			c.nonUnixDials[label]++
			c.add(Fact{Rule: "net-dial", File: rel, Line: pos.Line, Func: label, Object: netPkg + "." + obj.Name()})
		case isFunc && isDialerDialMethod(fn):
			if callee != nil && c.constantUnix(callee, dialerNetworkArg(fn.Name()), info) {
				c.unixDials[label]++
				return
			}
			c.nonUnixDials[label]++
			detail := ""
			if callee == nil {
				detail = "method value"
			}
			c.add(Fact{
				Rule: "net-dial", File: rel, Line: pos.Line, Func: label,
				Object: "(*net.Dialer)." + fn.Name(), Detail: detail,
			})
		}
		return
	}
	if strings.HasPrefix(path, nocxInternal) {
		c.nocxFact(obj, fn, isFunc, pkgLevel, rel, label, pos)
		return
	}
	if !strings.HasPrefix(path, sshPkg) {
		return
	}
	if path == sshPkg && obj.Name() == "ExitError" && !isFunc && pkgLevel {
		c.add(Fact{
			Rule: "exit-error-read", File: rel, Line: pos.Line, Func: label,
			Object: sshPkg + ".ExitError",
		})
		return
	}
	if isFunc && fn.Name() == "SendRequest" {
		if object, ok := sshSendRequestObject(fn, sel); ok {
			detail := ""
			if callee == nil {
				detail = "method value"
			} else {
				detail = requestName(callee, info)
			}
			c.add(Fact{
				Rule: "keepalive-request", File: rel, Line: pos.Line, Func: label,
				Object: object, Detail: detail,
			})
			return
		}
	}
	if !pkgLevel {
		return
	}
	c.add(Fact{Rule: "xcrypto-ssh-import", File: rel, Line: pos.Line, Func: label, Object: objectName(obj)})
}

// nocxFact records a C23/C24 fact: a reference to one of this module's own
// objects that either decides whether a far helper serves a connection or
// reaches a host for a panel. The object go/types bound the identifier to is
// the whole decision, so an alias, a dot import or a method value is the same
// reference.
func (c *collector) nocxFact(obj types.Object, fn *types.Func, isFunc, pkgLevel bool, rel, label string, pos token.Position) {
	if obj == nil || obj.Pkg() == nil {
		return
	}
	path := obj.Pkg().Path()
	switch path {
	case nocxInternal + "helper/client":
		if !isFunc {
			return
		}
		switch {
		case pkgLevel && receiverType(fn) == nil && fn.Name() == "Dial":
			c.add(Fact{Rule: "helper-decider", File: rel, Line: pos.Line, Column: pos.Column, Func: label, Object: path + ".Dial"})
		case methodOn(fn, "ProbeLease", "Uname"), methodOn(fn, "ProbeLease", "Home"),
			methodOn(fn, "ProbeLease", "SamplePorts"), methodOn(fn, "ProbeLease", "Completion"),
			methodOn(fn, "ProbeLease", "Inflate"), methodOn(fn, "ProbeLease", "CommandNames"):
			// Every exec a lease carries, not only the ports sample: a panel
			// that runs ANY probe for itself has taken the road C24 closes.
			c.add(Fact{Rule: "panel-host-path", File: rel, Line: pos.Line, Func: label, Object: methodObject(fn)})
		case methodOn(fn, "Client", "AcquireProbeLease"):
			// The acquisition, because the execs above travel on the returned
			// lease as often as not: a panel's adapter acquires the lease from
			// a client it holds and calls every exec through an interface, so
			// the lease's own methods are resolved to the interface and never
			// reach this walk. The concrete acquisition is the seam the
			// product actually uses, and it is the road C24 closes.
			c.add(Fact{Rule: "panel-host-path", File: rel, Line: pos.Line, Func: label, Object: methodObject(fn)})
		}
	case nocxInternal + "git/helper":
		// ALX-641: a Git factory may not be built on a foreign client. The
		// factory carries the client it was built with to every later open, so
		// one built on a client the far-helper axis did not choose keeps
		// serving Git after that axis went unavailable or the host went raw.
		if isFunc && pkgLevel && receiverType(fn) == nil && fn.Name() == "NewFactory" {
			c.add(Fact{Rule: "helper-decider", File: rel, Line: pos.Line, Func: label, Object: path + ".NewFactory"})
		}
	case nocxInternal + "hostmode", nocxInternal + "helper/consent":
		if isFunc && methodOn(fn, "Store", "Lookup") {
			c.add(Fact{Rule: "helper-decider", File: rel, Line: pos.Line, Func: label, Object: methodObject(fn)})
		}
	case nocxInternal + "ssh":
		if isFunc && pkgLevel && receiverType(fn) == nil && fn.Name() == "NewFSConn" {
			c.add(Fact{Rule: "panel-host-path", File: rel, Line: pos.Line, Func: label, Object: path + ".NewFSConn"})
		}
	case nocxInternal + "helper/proto":
		// A constant is not a function, so isFunc is false and the name is the
		// whole identity; the object still has to be package-level, because a
		// local constant of the same name is not the wire op.
		if !isFunc && pkgLevel && obj.Name() == "OpSamplePorts" {
			c.add(Fact{Rule: "panel-host-path", File: rel, Line: pos.Line, Func: label, Object: path + ".OpSamplePorts"})
		}
	}
}

// methodOn reports whether fn is the named method OF the named type, in its own
// package: a Lookup on some other type is not the store the rule names.
func methodOn(fn *types.Func, typeName, methodName string) bool {
	if fn.Name() != methodName {
		return false
	}
	named, ok := deref(receiverType(fn)).(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Pkg().Path() == fn.Pkg().Path() && named.Obj().Name() == typeName
}

// methodObject spells a method the way the baseline keys it: the package, the
// receiver type and the name, with the pointer recorded when the receiver is
// one.
func methodObject(fn *types.Func) string {
	recv := receiverType(fn)
	named, ok := deref(recv).(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return objectName(fn)
	}
	owner := named.Obj().Pkg().Path() + "." + named.Obj().Name()
	if _, pointer := recv.(*types.Pointer); pointer {
		owner = named.Obj().Pkg().Path() + ".(*" + named.Obj().Name() + ")"
	}
	return owner + "." + fn.Name()
}

// isDialerDialMethod reports whether fn is a dial method of net.Dialer itself
// (not of some type that happens to have a method with the same name).
func isDialerDialMethod(fn *types.Func) bool {
	if fn.Name() != "Dial" && fn.Name() != "DialContext" {
		return false
	}
	named, ok := deref(receiverType(fn)).(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Pkg().Path() == netPkg && named.Obj().Name() == "Dialer"
}

// receiverType answers the type a function object is declared on, or nil for a
// function: net.Dial and a (*T).Dial method share a name and nothing else, and
// this is the one place the difference is read.
//
// types.Func.Type is a *types.Signature for every function object, so the
// assertion cannot fail; it is checked because an unchecked one is a lint
// error here (errcheck), and the tree does not buy green by suppressing it.
func receiverType(fn *types.Func) types.Type {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return nil
	}
	return sig.Recv().Type()
}

// dialerNetworkArg answers which argument of a dialer method names the network.
func dialerNetworkArg(name string) int {
	if name == "DialContext" {
		return 1
	}
	return 0
}

// sshSendRequestObject names the SendRequest method's owner when it is the
// connection itself: x/crypto's Client, or its Conn interface. Any other type
// with a method of that name is ours and is not a prober.
func sshSendRequestObject(fn *types.Func, sel *types.Selection) (string, bool) {
	if fn.Pkg() == nil || fn.Pkg().Path() != sshPkg || fn.Name() != "SendRequest" {
		return "", false
	}
	if sel == nil {
		return "", false
	}
	recv := deref(sel.Recv())
	if named, ok := recv.(*types.Named); ok {
		obj := named.Obj()
		if obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == sshPkg && obj.Name() == "Client" {
			return "(*" + sshPkg + ".Client).SendRequest", true
		}
	}
	if iface, ok := recv.(*types.Interface); ok && iface != nil && iface.NumMethods() > 0 {
		return "(" + sshPkg + ".Conn).SendRequest", true
	}
	return "", false
}

// dialObject answers the dial function a value expression names, for the
// re-export rule; "" when it names none.
func (c *collector) dialObject(expr ast.Expr, p *packages.Package, info *types.Info) string {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	// Package-qualified only: `client.Dial(...)` is a METHOD call, and its
	// object shares the name without being the package-level function.
	if x, ok := sel.X.(*ast.Ident); !ok || info.Uses[x] == nil {
		return ""
	} else if _, isPkg := info.Uses[x].(*types.PkgName); !isPkg {
		return ""
	}
	obj := info.Uses[sel.Sel]
	if obj == nil || obj.Pkg() == nil {
		return ""
	}
	path := obj.Pkg().Path()
	if path == netPkg && isNetDial(obj.Name()) {
		return path + "." + obj.Name()
	}
	if path == sshPkg && (obj.Name() == "Dial" || obj.Name() == "NewClientConn") {
		return path + "." + obj.Name()
	}
	return ""
}

// constantUnix reports whether the network argument at index i is the
// CONSTANT string "unix" — evaluated by go/types, never read off the line.
func (c *collector) constantUnix(call *ast.CallExpr, i int, info *types.Info) bool {
	if i < 0 || i >= len(call.Args) {
		return false
	}
	tv, ok := info.Types[call.Args[i]]
	if !ok || tv.Value == nil {
		return false
	}
	return tv.Value.Kind() == constant.String && constant.StringVal(tv.Value) == "unix"
}

// requestName answers the string literal a SendRequest carries, when it is a
// literal, so a baseline entry says which request it was.
func requestName(call *ast.CallExpr, info *types.Info) string {
	if len(call.Args) == 0 {
		return ""
	}
	if tv, ok := info.Types[call.Args[0]]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
		return constant.StringVal(tv.Value)
	}
	return ""
}

func isNetDial(name string) bool {
	switch name {
	case "Dial", "DialTimeout", "DialUDP", "DialTCP", "DialIP", "DialUnix":
		return true
	}
	return false
}

func objectName(obj types.Object) string {
	if obj.Pkg() == nil {
		return obj.Name()
	}
	return obj.Pkg().Path() + "." + obj.Name()
}

func deref(t types.Type) types.Type {
	if p, ok := t.(*types.Pointer); ok {
		return p.Elem()
	}
	return t
}

// unparen strips any number of parentheses. `(x.Lookup)(fp)` and
// `(discovery.NewScheduler)(…)` are the same calls as their bare forms — only
// the expression around the callee differs — so every question about a callee
// asks it of the expression inside.
func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// farhelperOwns reports whether a type belongs to the far helper's own package,
// which is the owner the Ports seam (C24) is fenced AROUND. The pointer is the
// ordinary shape of the wiring — `*farhelper.PortsConnector` handed to the
// scheduler — so the type behind it is the type asked about.
func farhelperOwns(t types.Type) bool {
	named, ok := deref(t).(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	// A named INTERFACE is a shape, not the axis's connector: anything may
	// implement it, so admitting one would licence a foreign connector behind a
	// declaration that merely lives in farhelper (ALX-685).
	if _, isInterface := named.Underlying().(*types.Interface); isInterface {
		return false
	}
	return farhelperPath(named.Obj().Pkg().Path())
}

// farhelperPath reports whether an import path names the far helper's package.
// The fence is the package's place in this tree, so the comparison is against
// the whole path and nothing less: a neighbour called
// `internal/farhelperlegacy` starts with the same letters and is somebody else,
// and a package whose path merely ENDS in `/internal/farhelper` — a nested
// `internal/app/internal/farhelper` — is somebody else too, however familiar
// its name (ALX-750).
func farhelperPath(path string) bool {
	return path == nocxInternal+"farhelper"
}

// funcLabel answers the baseline's name for a function or method: the package
// path plus the receiver type, so an entry reads `app.(*App).Run`.
func funcLabel(p *packages.Package, d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return p.PkgPath + "." + d.Name.Name
	}
	recv := d.Recv.List[0].Type
	for {
		if star, ok := recv.(*ast.StarExpr); ok {
			recv = star.X
			continue
		}
		if idx, ok := recv.(*ast.IndexExpr); ok {
			recv = idx.X
			continue
		}
		break
	}
	if ident, ok := recv.(*ast.Ident); ok {
		return p.PkgPath + "." + ident.Name + "." + d.Name.Name
	}
	return p.PkgPath + "." + d.Name.Name
}

func (c *collector) add(f Fact) { c.list = append(c.list, f) }
