package main

// THE COORDINATOR LINKS NO SSH CLIENT — the half of that claim the SOURCE can
// be asked about, and the half it cannot.
//
// The owner's invariant (plan 2026-09-13 §1, §3) is that every ssh connection
// is made by the LOCAL helper: cmd/nocx-server resolves a destination, binds
// and authorizes a credential and answers host-key questions, and it dials
// nothing. The one repository builds both halves — the coordinator, and the
// helper installed on this machine — so nocx_local_ssh is what tells them
// apart, and the dial half of internal/ssh (the pool, the dial, the
// interactive channel) carries that tag.
//
// # What this walks, and why `go list` rather than a grep
//
// `go list -json -deps ./cmd/nocx-server` answers what the COMPILER puts in
// this binary: a build constraint that does not hold, a blank import and a
// dependency of a dependency are all the same question to it, and three
// different questions to a source grep. Over that closure, and over each
// package's non-test GoFiles, this test asks two things:
//
//   - does any file CALL a dial primitive (gossh.Dial, gossh.NewClientConn,
//     gossh.NewClient, sftp.NewClient), or HOLD a connection (*gossh.Client,
//     *gossh.Session)? The call is what makes a connection and the held type is
//     where one lives, and either one in this closure is the invariant broken.
//   - is internal/ssh's DIAL HALF absent from the untagged GoFiles, and present
//     once the tag is on? Both directions, because "absent" alone passes for a
//     file that was renamed away or deleted.
//
// # The blind spot, stated rather than implied
//
// This proves the SHAPE of a build, and no source walk can do better:
//
//   - a DOT import (`. "golang.org/x/crypto/ssh"`) makes a call an unqualified
//     identifier, and this walk attributes calls through the file's import
//     specs. An ALIASED import is covered — the specs are resolved, not the
//     spelling — which is why the blind spot is the dot form alone.
//   - reflection, or a value reached at runtime, is not a call site and is not
//     visible here at all.
//   - a THIRD-PARTY package that dials for us is not in these files. The walk
//     judges our sources; `go list` says which packages are linked, not what
//     they do.
//   - and the largest one: it says nothing about BEHAVIOUR. A coordinator can
//     link no dialer and still reach a dial through a seam it resolves at run
//     time.
//
// # Its pair
//
// The behavioural half is plan §7 check 1, and it is deliberately not written
// here: an opener that selects EVERY remote destination plus a fake
// capability.OpenService whose Open fails the test if it is reached, opening a
// pane over the real handler and asserting the counter is zero. That test
// belongs to the route commit that deletes the fallback, and until it exists
// this file is the source-walk half of a pair whose other half is owed —
// uniqueness is not reachability, and a walk cannot see a path taken.

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// nocxModule is this repository's module path: what makes a package in the
// closure OURS, and so what this walk is entitled to judge. A dependency's own
// dial is its business and is out of scope by construction (plan §1: the
// deployed artifact's contract is asserted by go list, in
// internal/helper/deploy/dependency_test.go).
const nocxModule = "github.com/shady2k/nocx"

// sshPackage is the client library, named by its MODULE path so that a file
// aliasing it — `gossh` — is still attributed correctly.
const sshPackage = "golang.org/x/crypto/ssh"

// dialCalls are the functions on an import that establish a connection, by
// import path. Every one of them answers the same question — "may this process
// make an ssh connection" — and every one of them is named by the invariant
// this file asserts.
var dialCalls = map[string]map[string]bool{
	sshPackage:            {"Dial": true, "NewClientConn": true, "NewClient": true},
	"github.com/pkg/sftp": {"NewClient": true},
}

// heldTypes are the types a live connection or its session is HELD in, on the
// client library. A file that never calls a dial but holds one of these has a
// connection from somewhere, and the closure is not a place it may come from.
var heldTypes = map[string]bool{"Client": true, "Session": true}

// coordinatorDialFiles is this repository's dial half of internal/ssh: the
// files nocx_local_ssh gates. (b) asserts them absent from the untagged graph
// and present in the tagged one, and the pair of assertions is what makes a
// rename or a deletion a failure rather than a pass.
var coordinatorDialFiles = []string{
	"ssh_dial.go",
	"ssh_real_dial.go",
	"pool.go",
	"ssh_pooled.go",
	"ssh_channel.go",
}

func TestTheCoordinatorLinksNoSSHClient(t *testing.T) {
	root := moduleRoot(t)

	// NO EXTRA TAGS, and that is the shipped coordinator rather than an
	// accident of how this test was invoked: `make build` and `make release`
	// add `release` and the platform's window tag, neither of which is a build
	// that links an ssh client, and nothing builds cmd/nocx-server with
	// nocx_local_ssh. The second context below asks the same question of the
	// shipped profile's build, so neither tag set is a hole.
	for _, tags := range [][]string{nil, {"release"}} {
		linked := coordinatorPackages(t, root, tags)
		label := "no extra tags"
		if len(tags) > 0 {
			label = "tags " + strings.Join(tags, ",")
		}

		// (a) no dial primitive and no held connection in any repo package
		// this build reaches.
		for _, pkg := range linked {
			for _, file := range pkg.GoFiles {
				path := filepath.Join(pkg.Dir, file)
				src, err := os.ReadFile(path) // #nosec G304 — the path is `go list`'s own output joined with a file IT named
				if err != nil {
					t.Fatalf("read %s: %v", path, err)
				}
				for _, use := range sshClientUses(path, src) {
					t.Errorf("%s: %s\n  %s is linked into cmd/nocx-server (%s): every ssh connection is this machine's helper's, and the dial half of internal/ssh is nocx_local_ssh-gated",
						rel(root, path), use, pkg.ImportPath, label)
				}
			}
		}

		// (b) the dial half is out of that build — and in the tagged one.
		untagged := packageNamed(linked, nocxModule+"/internal/ssh", t)
		for _, file := range coordinatorDialFiles {
			if contains(untagged.GoFiles, file) {
				t.Errorf("internal/ssh/%s is compiled into the coordinator (%s): it is the dial half and its build line must read //go:build nocx_local_ssh", file, label)
			}
		}
		tagged := packageNamed(coordinatorPackages(t, root, append(tags, "nocx_local_ssh")), nocxModule+"/internal/ssh", t)
		for _, file := range coordinatorDialFiles {
			if !contains(tagged.GoFiles, file) {
				t.Errorf("internal/ssh/%s is not compiled even WITH nocx_local_ssh: the file this split names is gone or renamed, and its absence from the untagged build would have passed on its own", file)
			}
		}
	}
}

// sshClientUses answers every place in one file that makes or holds an ssh
// connection: the position, and what was seen there.
//
// It parses, because the alternative is a text scan that reads the prose of
// this repository's own comments as findings — ssh_real.go's "deliberately not
// a `*gossh.Client`" is a sentence about the design, and a grep cannot tell it
// from a field.
func sshClientUses(path string, src []byte) []string {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		// A file this walk cannot read is one it cannot clear. Said as a
		// finding rather than skipped: silently passing on it is how a walk
		// stops covering the file that was moved.
		return []string{fmt.Sprintf("unparseable (%v): this walk cannot judge a file it cannot read", err)}
	}

	// The file's own names for the packages it imports, so `gossh` and
	// `ssh` and `x` are the same question.
	named := make(map[string]string, len(file.Imports))
	for _, spec := range file.Imports {
		importPath, unquoteErr := strconv.Unquote(spec.Path.Value)
		if unquoteErr != nil {
			continue
		}
		name := importPath[strings.LastIndex(importPath, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		named[name] = importPath
	}

	var uses []string
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.CallExpr:
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			qualifier, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if dialCalls[named[qualifier.Name]][sel.Sel.Name] {
				uses = append(uses, fmt.Sprintf("%s calls %s.%s(", fset.Position(n.Pos()), qualifier.Name, sel.Sel.Name))
			}
		case *ast.StarExpr:
			sel, ok := n.X.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			qualifier, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if named[qualifier.Name] == sshPackage && heldTypes[sel.Sel.Name] {
				uses = append(uses, fmt.Sprintf("%s holds *%s.%s", fset.Position(n.Pos()), qualifier.Name, sel.Sel.Name))
			}
		}
		return true
	})
	return uses
}

// listedPackage is the part of `go list -json` this walk reads: where the
// package is, and which of its Go files this build compiles (GoFiles excludes
// _test.go, which is the scope this file asserts over — a stand inside a test
// may dial; the shipped program may not).
type listedPackage struct {
	ImportPath string
	Dir        string
	GoFiles    []string
}

// coordinatorPackages lists every package `go list -deps ./cmd/nocx-server`
// reaches under the given tags, filtered to this repository's own.
func coordinatorPackages(t *testing.T, root string, tags []string) []listedPackage {
	t.Helper()
	args := []string{"list", "-json", "-deps"}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	args = append(args, "./cmd/nocx-server")
	cmd := exec.Command("go", args...) // #nosec G204 — "go" and every argument are literals assembled here
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps ./cmd/nocx-server %v: %v", tags, err)
	}

	var packages []listedPackage
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	for {
		var pkg listedPackage
		if decodeErr := decoder.Decode(&pkg); decodeErr != nil {
			break
		}
		if strings.HasPrefix(pkg.ImportPath, nocxModule+"/") {
			packages = append(packages, pkg)
		}
	}
	if len(packages) == 0 {
		t.Fatal("go list answered no package of this module: the module path in this file has drifted from go.mod")
	}
	return packages
}

func packageNamed(packages []listedPackage, importPath string, t *testing.T) listedPackage {
	t.Helper()
	for _, pkg := range packages {
		if pkg.ImportPath == importPath {
			return pkg
		}
	}
	t.Fatalf("%s is not in the coordinator's dependency graph: a package this test asserts about is not linked, so its assertions are vacuous", importPath)
	return listedPackage{}
}

func contains(files []string, name string) bool {
	for _, file := range files {
		if file == name {
			return true
		}
	}
	return false
}

// moduleRoot walks up to the directory holding go.mod: `go list` runs against
// the module, and `go test` puts the working directory at the package.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory: this test cannot name the module it walks")
		}
		dir = parent
	}
}

func rel(root, path string) string {
	relPath, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return relPath
}
