package mcpstdio

// THE BRIDGE IS WHERE THE PANE'S BEARER EXISTS IN THE CLEAR (nocx-50w7p.16,
// AC3), so it is the process whose OUTPUT has to be asked about it.
//
// The owner's decision puts the bearer in the per-launch `mcp.json` `env`, which
// means the agent's MCP client hands it to this process through the environment
// and nowhere else. Two things can then leak it without anybody typing it
// anywhere: a log line, and a child process's environment. This file asserts
// the absence of both — and, for the log, asserts it on a run where the bearer
// demonstrably WAS presented, because "the value does not appear" is worth
// nothing if the value was never in play.
//
// WHAT IT DOES NOT COVER, said rather than implied: the staged configuration
// itself (internal/shellintegration renders the bearer into `mcp.json` and
// clears it before the agent is exec'd) and the far shell's own staging have
// their tests where that code lives. The claim here is about THIS process's
// two exits: its log, and the children it does not have.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/toolendpoint/panebind"
)

// lockedLogs is where a bridge's diagnostics land when a test wants to read
// them: the handler writes from Serve's own goroutine and from the per-request
// goroutines underneath it, so the buffer is read under the same lock it is
// written under.
type lockedLogs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedLogs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// startBearerEndpoint serves the shape the helper's LANE gives the endpoint: a
// connection that presents the pane's bearer before it says anything else, and
// only then newline-delimited JSON-RPC.
//
// It is deliberately not the shipped endpoint. What this file asks about is what
// the BRIDGE writes, so the far side has to be a scripted reader — and a
// scripted reader that CONSUMES the bearer is the one thing this package's
// startEndpoint cannot be, because it reads every line as a request.
func startBearerEndpoint(t *testing.T, handler func(net.Conn, rpcEnvelope), sawBearer func(string)) string {
	t.Helper()
	listener := listenEndpoint(t)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				reader := bufio.NewReader(conn)
				// THE PANE'S BEARER, read exactly as toolendpoint's own
				// preamble reader reads it.
				token, err := panebind.ReadToken(reader)
				if err != nil {
					return
				}
				sawBearer(token)
				for {
					request, err := readJSONLine(reader)
					if err != nil {
						return
					}
					handler(conn, request)
				}
			}()
		}
	}()
	return listener.Addr().String()
}

// The catalogue one `tools/list` answer needs, in the shape every other test in
// this package answers with. NOT named for the bearer: gosec's credential
// heuristic flags the word beside a long literal, and a suppression here would
// be a suppression on the file that exists to look for leaks.
const oneToolCatalogue = `{"tools":[{"name":"alpha.run","summary":"run alpha","params":{"type":"object"},"result":{"type":"object"}}]}`

// TestTheBridgesLogsNeverCarryTheBearer — the bearer enters through the
// environment exactly as it does from the staged config, the bridge runs a whole
// session with it, and the text it wrote for a person to read does not contain
// it.
func TestTheBridgesLogsNeverCarryTheBearer(t *testing.T) {
	t.Setenv(shellintegration.AgentToolTokenEnvVar, testBearer)

	logs := &lockedLogs{}
	saw := make(chan string, 4)
	socket := startBearerEndpoint(t, func(conn net.Conn, request rpcEnvelope) {
		if request.Method != "tools.catalogue" {
			return
		}
		writeJSONLine(t, conn, rpcEnvelope{
			JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(oneToolCatalogue),
		})
	}, func(token string) { saw <- token })

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
	}, "\n") + "\n"
	runAdapterLogging(t, socket, input, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

	// THE BEARER REALLY WAS IN PLAY. Without this, the absence below would pass
	// for a run that never presented one — and the process under test is
	// exactly the one that reads it out of the environment.
	select {
	case got := <-saw:
		if got != testBearer {
			t.Fatalf("the endpoint was presented %q, want the bearer the environment carried", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the bridge never presented a bearer, so nothing here was tested")
	}

	out := logs.String()
	if !strings.Contains(out, "mcp bridge serving") {
		t.Fatalf("the bridge logged nothing at all, so an absence here would prove nothing: %q", out)
	}
	if strings.Contains(out, testBearer) {
		t.Fatalf("the pane's bearer appears in the bridge's log output: %q", out)
	}
}

// TestTheBridgesLogsNeverCarryTheBearerWhenTheEndpointIsGone is the same claim
// on the FAILURE path, and it is a different test rather than a second assertion
// because a failing dial is where a "helpful" diagnostic puts the whole
// configuration in a line: the reason the socket could not be reached is the
// value a person pastes, and the bearer is sitting in the same struct as the
// socket path. It drives the link directly, which is the object that holds both.
func TestTheBridgesLogsNeverCarryTheBearerWhenTheEndpointIsGone(t *testing.T) {
	logs := &lockedLogs{}
	link := newEndpointLink(
		filepath.Join(t.TempDir(), "no-endpoint-here.sock"), &net.Dialer{}, testBearer,
		slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := link.call(ctx, "tools.catalogue", json.RawMessage(`{}`)); err == nil {
		t.Fatal("a dial to a socket that does not exist succeeded")
	}

	out := logs.String()
	if !strings.Contains(out, "mcp tool endpoint unreachable") {
		t.Fatalf("the failed dial was not logged, so an absence here would be vacuous: %q", out)
	}
	if strings.Contains(out, testBearer) {
		t.Fatalf("the pane's bearer appears in the bridge's log output for a failed dial: %q", out)
	}
}

// TestTheBridgeRunsNoChildProcess — the other exit. A child inherits this
// process's environment, and this process's environment is where the owner put
// the bearer, so a bridge that ran one would hand the pane's admission to
// whatever it started.
//
// The assertion is the ABSENCE OF THE EXEC rather than the absence of a leak: a
// test that ran the bridge and inspected one child would prove that this run
// started nothing, not that no run can. The walk is over this package and its
// whole dependency closure, which is what `nocx-helper mcp` runs to completion
// — the rest of that binary's subcommands DO start processes (they open shells),
// which is why the claim is scoped to the bridge and not to the artifact.
//
// WHAT A SOURCE WALK CANNOT SEE, stated: a DOT import would make a call an
// unqualified identifier; a linked dependency that spawns from inside its own
// code is not judged by these files at all (the go list half says which packages
// are linked, not what they do); and a seam resolved at run time is not a call
// site. What it does see is the ordinary way this would happen — an `exec`
// import and a call in the code that would make one.
//
// THE THREE WAYS TO START A PROCESS, and why `syscall` is banned by IMPORT
// rather than by the closure. `os/exec` is the ordinary one; `os.StartProcess`
// is the same act one layer down in a package this file must import anyway; and
// `syscall.ForkExec`/`Exec` is the third, reachable only by importing syscall
// directly — which this package does not. The closure check cannot say anything
// about syscall, because `os` pulls it in and always will, so the honest claim
// is about what THIS package reaches for: a direct import, and the call sites in
// the one package it does hold. This is internal/apiimport's noexec_test.go
// applied to the same question, and it is why a future direct syscall spawn
// cannot slip past the union of the two.
func TestTheBridgeRunsNoChildProcess(t *testing.T) {
	// Packages whose very presence in a non-test file of this package is the
	// ability to start a process. syscall and golang.org/x/sys are here for the
	// reason apiimport's list gives: `os` always drags syscall in transitively,
	// so a closure check can only be asked about the direct import.
	forbidden := []string{"os/exec", "syscall", "golang.org/x/sys/unix", "golang.org/x/sys/windows"}

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	files := 0
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			files++
			for _, imp := range f.Imports {
				path, unquoteErr := strconv.Unquote(imp.Path.Value)
				if unquoteErr != nil {
					t.Fatalf("%s: bad import path %s", name, imp.Path.Value)
				}
				for _, bad := range forbidden {
					if path == bad {
						t.Fatalf("%s imports %s — the bridge must not be able to start a child, because a child "+
							"inherits the environment the pane's bearer arrives in", name, bad)
					}
				}
			}
			// os is imported for os.Getenv and os.Stdin; the two things in it
			// that start or reach a process are named here, and the syscall
			// primitives beside them in case a build constraint ever carries the
			// import in.
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				switch id.Name {
				case "os":
					if sel.Sel.Name == "StartProcess" || sel.Sel.Name == "FindProcess" {
						t.Fatalf("%s calls os.%s", name, sel.Sel.Name)
					}
				case "syscall":
					switch sel.Sel.Name {
					case "ForkExec", "Exec", "StartProcess":
						t.Fatalf("%s calls syscall.%s", name, sel.Sel.Name)
					}
				}
				return true
			})
		}
	}
	if files < 1 {
		t.Fatalf("walked %d non-test files — the walk found nothing to check", files)
	}

	// AND NOTHING BELOW IT EITHER: an import of this package is not a promise
	// that a dependency cannot spawn.
	if _, lookErr := exec.LookPath("go"); lookErr != nil {
		t.Skip("no go toolchain on PATH; the direct-import check above still holds")
	}
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	deps := strings.Fields(string(out))
	if len(deps) < 10 {
		t.Fatalf("go list -deps returned %d packages — it did not resolve the package", len(deps))
	}
	for _, dep := range deps {
		if dep == "os/exec" {
			t.Fatal("os/exec is reachable from this package, so a child could be started from somewhere below it")
		}
	}
	t.Logf("walked %d non-test files and %d transitive dependencies", files, len(deps))
}
