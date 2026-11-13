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
//
// WHAT IT CANNOT SEE, stated here because the check's header repeats it: a
// dial behind an interface whose implementation lives elsewhere (the call is
// a method on an interface the analyser cannot chase); a value copied into
// another package through a field or a channel; a network named by a
// variable that is not a constant; and code that does not compile. It also
// does not read the renderer, which nocx-oalo1.3.5 owns.
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
	"strings"

	"golang.org/x/tools/go/packages"
)

// Fact is one place that names a connection fact.
type Fact struct {
	Rule   string `json:"rule"`
	File   string `json:"file"`
	Line   int    `json:"line"`
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
		candidates, cerr := candidatePatterns(labelAbs)
		if cerr != nil {
			fail(cerr)
		}
		patterns = candidates
	}
	// No cgo: every prerequisite of an SSH connection is Go, and the native
	// archives a cgo build needs (libghostty-vt, webkit) are not something a
	// check may demand of a machine whose only job is reading this tree.
	cfg.Env = append(os.Environ(), "CGO_ENABLED=0")
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		fail(err)
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

// mentionsTarget reports whether a file NAMES an object the analysis could
// report: a selector on the import alias of net (one of the dial names) or of
// x/crypto/ssh (any name), or a dot-import of either. This is a candidate
// filter only — every decision is still made by go/types — and it is
// deliberately generous for x/crypto, whose objects are all in scope.
func mentionsTarget(f *ast.File) bool {
	aliases := map[string]bool{} // alias -> true when it names x/crypto/ssh
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		isSSH := strings.HasPrefix(path, sshPkg)
		if path != netPkg && !isSSH {
			continue
		}
		if imp.Name != nil {
			switch imp.Name.Name {
			case ".":
				return true // an object of the package is in file scope
			case "_":
				continue
			}
			aliases[imp.Name.Name] = isSSH
			continue
		}
		parts := strings.Split(path, "/")
		aliases[parts[len(parts)-1]] = isSSH
	}
	if len(aliases) == 0 {
		return false
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		isSSH, ok := aliases[id.Name]
		if !ok {
			return true
		}
		if isSSH || netObjectNames[sel.Sel.Name] {
			found = true
			return false
		}
		return true
	})
	return found
}

// candidatePatterns answers the package patterns a full-tree run needs: one
// per directory holding a file that imports net or x/crypto/ssh. The scan is
// syntax-only and ignores build tags on purpose — a file behind a tag is
// still a second owner, and the two passes decide whether it compiles.
func candidatePatterns(scopeAbs string) ([]string, error) {
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
		rel, rerr := filepath.Rel(scopeAbs, dir)
		if rerr != nil {
			return nil, rerr
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
		for _, arg := range x.Args {
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
		selection, _ := info.Selections[x]
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
		case isFunc && isNetDial(obj.Name()) && pkgLevel && fn.Type().(*types.Signature).Recv() == nil:
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

// isDialerDialMethod reports whether fn is a dial method of net.Dialer itself
// (not of some type that happens to have a method with the same name).
func isDialerDialMethod(fn *types.Func) bool {
	if fn.Name() != "Dial" && fn.Name() != "DialContext" {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	named, ok := deref(sig.Recv().Type()).(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Pkg().Path() == netPkg && named.Obj().Name() == "Dialer"
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

// isConnInterface reports whether fn is the SendRequest method of the ssh.Conn
// interface (as opposed to some other interface's method of the same name).
func isConnInterface(fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	iface, ok := sig.Recv().Type().Underlying().(*types.Interface)
	if !ok {
		return false
	}
	// The method's receiver type is the interface itself only for interface
	// methods; compare the object's name and package.
	return fn.Pkg() != nil && fn.Pkg().Path() == sshPkg && fn.Name() == "SendRequest" && iface.NumMethods() > 0
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
