package transport

// EVERY SESSION KIND HAS EXACTLY ONE ANSWERER, A HELPER RUNTIME (nocx-ygxjv.13,
// ADR-0057).
//
// The owner's invariant is that every session's channel — a local pty or a
// remote shell — is created by a helper, never by the coordinator process
// itself. composition_root_ssh_test.go (internal/app) proves the SSH half of
// that claim against the real composition root; this file is the general
// case, over BOTH kinds session.go declares, and it is a completeness check
// as much as a behavioural one: the coverage table below must know every name
// the Kind const block declares, or this test fails by NAMING the kind nobody
// taught it about, rather than silently passing over a third kind added later
// with no opinion at all.
//
// Two facts, asserted per kind:
//
//   - THE REGISTRY NEVER MANUFACTURES IT ITSELF. session.New(logger, nil) is
//     the exact call internal/app/app.go makes for the shipped coordinator: no
//     local PTY factory, no SSH factory. Asking that registry to Open a
//     session of this kind must fail — there is nothing in it that could
//     produce a channel.
//   - A HELPER IS THE ONLY ANSWERER. The same shape of registry — still no PTY
//     factory, still no SSH factory — wired only with a HelperSessionOpener
//     that ADOPTS a channel it made (session.Reg.Adopt, the seam
//     ws_helper_open_test.go's helperOpenTestOpener already uses for exactly
//     this) opens successfully, with the helper-minted session id. The only
//     way this can succeed with nothing else changed is that the helper, not
//     the registry, created the runtime.
//
// Read together: a kind that failed the first check and passed the second
// could not be answered any other way, for either kind session.go names
// today.
import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

// declaredSessionKindNames parses internal/session/session.go's own `type
// Kind int` const block and returns every name it declares, in source order.
// It reads the SOURCE rather than importing reflect/enum machinery because
// Kind has neither a String() nor a registered list of values — kindName's
// switch is the only other place that enumerates it, and reading source here
// keeps this test from silently agreeing with a kindName that itself forgot a
// case (both would have to be wrong together for this to miss it, which is
// no worse than the "declaredNames" technique internal/emulator/ghostty's
// convert_test.go already uses in this repository for the same problem: a
// closed set with no enumerable representation of its own).
func declaredSessionKindNames(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	// Relative to this package's own directory, which is what `go test` sets
	// as the working directory — the same convention convert_test.go relies
	// on for "../key.go".
	f, err := parser.ParseFile(fset, "../session/session.go", nil, 0)
	if err != nil {
		t.Fatalf("parse internal/session/session.go: %v", err)
	}
	var names []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		// A parenthesized const block gives its type once — Go's own repeat
		// rule then carries it down every ValueSpec that names none of its
		// own, so a case with no shown type belongs to the last shown one for
		// its own GenDecl, and it resets when the type changes.
		active := ""
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if vs.Type != nil {
				id, isIdent := vs.Type.(*ast.Ident)
				if isIdent {
					active = id.Name
				} else {
					active = ""
				}
			}
			if active != "Kind" {
				continue
			}
			for _, n := range vs.Names {
				names = append(names, n.Name)
			}
		}
	}
	return names
}

// kindUnderTest pairs a declared session.Kind with the wire spelling
// validateOpenRaw's closed set accepts for it (ws_session_handlers.go:
// "", "local" and "ssh" — "local" is used here so a failing subtest's name
// reads as the kind rather than as an omission).
type kindUnderTest struct {
	value session.Kind
	wire  string
}

// kindsThisTestCovers is the completeness anchor: every name
// declaredSessionKindNames finds must have an entry here, and every entry
// here must still be a declared name, or the test fails before it asks
// anything behavioural — which is the "fails when a new Kind is added
// without being covered" the bead asks for.
var kindsThisTestCovers = map[string]kindUnderTest{
	"KindLocal":  {value: session.KindLocal, wire: "local"},
	"KindRemote": {value: session.KindRemote, wire: "ssh"},
}

func TestEverySessionKindHasExactlyOneAnswererAHelperRuntime(t *testing.T) {
	declared := declaredSessionKindNames(t)
	if len(declared) < 2 {
		t.Fatalf("session.go's Kind const block yielded %v; the reader above is not reading what it means to", declared)
	}
	seen := make(map[string]bool, len(declared))
	for _, name := range declared {
		seen[name] = true
		if _, ok := kindsThisTestCovers[name]; !ok {
			t.Fatalf("session.Kind gained %q and this test's coverage table does not know it: "+
				"give %q a wire spelling in kindsThisTestCovers and a case in the switch below "+
				"before this can pass silently", name, name)
		}
	}
	for name := range kindsThisTestCovers {
		if !seen[name] {
			t.Fatalf("kindsThisTestCovers names %q, which session.go's Kind const block no longer "+
				"declares — prune the stale entry", name)
		}
	}

	for name, kind := range kindsThisTestCovers {
		t.Run(name+"/registry_never_answers_it_itself", func(t *testing.T) {
			testRegistryRefusesKindDirectly(t, kind)
		})
		t.Run(name+"/a_helper_is_the_only_answerer", func(t *testing.T) {
			testHelperIsTheOnlyAnswererForKind(t, kind)
		})
	}
}

// testRegistryRefusesKindDirectly builds the registry the exact way the
// shipped coordinator does — session.New(logger, nil), no local PTY factory,
// no SSH factory — and asks it to Open the kind directly, with no helper in
// the way at all. A success here would mean the registry manufactured the
// channel itself.
func testRegistryRefusesKindDirectly(t *testing.T, kind kindUnderTest) {
	t.Helper()
	reg := session.New(log.NewSlogAdapter(nil), nil)
	cfg := session.Config{Kind: kind.value, Cols: 80, Rows: 24}
	if kind.value == session.KindRemote {
		cfg.Host = "unreachable.invalid"
		cfg.Remote = &ssh.ConnectConfig{User: "test"}
	}
	if _, err := reg.Open(context.Background(), cfg); err == nil {
		t.Fatalf("Open(kind=%v) succeeded against a registry with no PTY factory and no SSH "+
			"factory: the registry answered this pane itself, which is the fallback ADR-0057 refuses", kind.value)
	}
}

// testHelperIsTheOnlyAnswererForKind builds the SAME shape of registry —
// still no PTY factory, still no SSH factory, so testRegistryRefusesKindDirectly
// has already shown it can manufacture nothing — and wires only a
// HelperSessionOpener that adopts a channel it made. The real WebSocket open
// path (the seam the product's own renderer reaches) is driven end to end, and
// success can only be explained by the helper having created the runtime.
func testHelperIsTheOnlyAnswererForKind(t *testing.T, kind kindUnderTest) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	reg := session.New(logger, nil)
	helperID := session.NewID()
	opener := &helperOpenTestOpener{reg: reg, id: helperID}

	opts := []WSServerOption{WithHelperSessionOpener(opener)}
	params := map[string]any{
		"kind": kind.wire, "cols": 80, "rows": 24, "paneId": helperOpenPane,
	}
	if kind.value == session.KindRemote {
		opts = append(opts, WithProfileResolver(&fakeResolver{
			resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
				return "remote.example", &ssh.ConnectConfig{User: "alice"}, nil
			},
		}))
		params["profileId"] = "p1"
	}

	ws := NewWSServer(logger, reg, opts...)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	resp := jsonrpcCall(t, conn, "open", params)
	var result struct {
		SessionID string `json:"sessionId"`
	}
	decodeJSONRPCResult(t, resp, &result)
	if !opener.called {
		t.Fatalf("the open for kind %q never reached the helper opener at all", kind.wire)
	}
	if result.SessionID != string(helperID) {
		t.Fatalf("sessionId = %q, want the helper-minted %q — something other than the helper's "+
			"adopted channel produced this session", result.SessionID, helperID)
	}
}
