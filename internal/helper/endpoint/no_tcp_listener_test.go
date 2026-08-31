package endpoint_test

// THE BOUNDARY, ASSERTED WHERE IT CAN ACTUALLY BE ASSERTED.
//
// A port on 127.0.0.1 is reachable by ANY user of the machine, and the whole
// authorization model is the Unix account (D12: same-UID trust, any nocx under
// that account may connect). A loopback listener would annul it — not degrade
// it, annul it — so the requirement is not "we do not open one today" but "no
// listener on this path is ever anything but a Unix socket".
//
// A runtime check cannot say that. It can only report what one run happened to
// open, on one platform, down whichever branches that run took. So the check is
// on the SOURCE of every package the coordinator↔helper path is made of: every
// listen in it names its network, and every one of those names is unix.
//
// It is a ratchet in the shape of a test, and it is the only form in which
// this particular invariant survives the next person who needs "just a quick
// port for debugging".

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// thePath is every package a byte travels through between a coordinator and
// the helper that serves it: the endpoint itself, the connection's protocol
// engine, the session service behind it, the coordinator's client, and the
// binary that is both ends of it.
var thePath = []string{
	"../../../internal/helper/endpoint",
	"../../../internal/helper/host",
	"../../../internal/helper/session",
	"../../../internal/helper/client",
	"../../../internal/helper/proto",
	"../../../cmd/nocx-helper",
}

// listeners are the ways a Go program starts listening. Each names its network
// as a string argument, and the position of that argument is what this table
// records.
var listeners = map[string]int{
	"Listen":       0, // net.Listen(network, address), and (*net.ListenConfig).Listen(ctx, network, address) is caught by the ctx-aware form below
	"ListenPacket": 0,
	"ListenTCP":    0,
	"ListenUDP":    0,
	"ListenIP":     0,
}

func TestNoTCPListenerAnywhereInThePath(t *testing.T) {
	for _, dir := range thePath {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		if len(files) == 0 {
			t.Fatalf("%s has no Go files: the path this test walks has moved", dir)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			checkFileHasNoNetworkListener(t, file)
		}
	}
}

func checkFileHasNoNetworkListener(t *testing.T, file string) {
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
	ast.Inspect(f, func(n ast.Node) bool {
		// Rule one, precise: a listen through the net package names its
		// network, and the name must be unix.
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "net" {
					if at, listens := listeners[sel.Sel.Name]; listens {
						checkNetwork(t, fset, call, sel.Sel.Name, at)
					}
				}
			}
			return true
		}
		// Rule two, blunt and deliberately so: no file on this path may even
		// SPELL a network that is not unix. It is what catches the listen this
		// test cannot resolve — a net.ListenConfig held in a variable, a
		// network built from a constant, a library handed an address — without
		// dragging the type checker in for one bit of information.
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if forbiddenNetworks[value] {
			t.Errorf("%s: the literal %q. The authoritative endpoint is a private Unix socket; "+
				"a port on 127.0.0.1 is reachable by any user of the machine and would annul D12's same-UID trust",
				fset.Position(lit.Pos()), value)
		}
		return true
	})
}

// forbiddenNetworks are the network names Go's net package understands that
// are not a Unix socket.
var forbiddenNetworks = map[string]bool{
	"tcp": true, "tcp4": true, "tcp6": true,
	"udp": true, "udp4": true, "udp6": true,
	"ip": true, "ip4": true, "ip6": true,
}

func checkNetwork(t *testing.T, fset *token.FileSet, call *ast.CallExpr, name string, at int) {
	t.Helper()
	// (*net.ListenConfig).Listen takes the context first, so the network is
	// one further along. Distinguished by arity rather than by type
	// resolution, which would need the whole type checker for one bit.
	if name == "Listen" && len(call.Args) == 3 {
		at = 1
	}
	if at >= len(call.Args) {
		t.Errorf("%s: net.%s with no network argument to check", fset.Position(call.Pos()), name)
		return
	}
	lit, ok := call.Args[at].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		t.Errorf("%s: net.%s takes a network this test cannot read; a listener on this path must name %q literally",
			fset.Position(call.Pos()), name, "unix")
		return
	}
	network, err := strconv.Unquote(lit.Value)
	if err != nil {
		t.Errorf("%s: unreadable network %s", fset.Position(call.Pos()), lit.Value)
		return
	}
	if network != "unix" {
		t.Errorf("%s: net.%s listens on %q, and the endpoint is a Unix socket",
			fset.Position(call.Pos()), name, network)
	}
}
