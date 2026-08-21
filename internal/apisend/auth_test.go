package apisend

// Design §8. Auth is built from a VARIABLE NAME resolved through the
// binding store — never from an identifier in a file.
//
// The test that matters most is TestApply_AVaultIdentifierIsJustAnUnknown
// VariableName below, and it is written as an assertion about the ABSENCE
// OF A PATH rather than about a guard firing. §8's whole argument is that
// the attack is unspellable rather than caught: "A hostile file can write
// {{token}} and gets whatever the reader bound in their own environment; it
// has no way to spell 'the password behind the production SSH profile',
// because there is no syntax in which a file names a secret."
//
// A test that showed a check refusing an identifier would be evidence for
// the draft §8 REJECTED — a guard bolted onto a format that permits the
// attack. So the assertions here are: the vault is never asked, the server
// is never reached, and this package has no syntax in which an identifier
// could be written at all.

import (
	"context"
	"encoding/base64"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
)

// countingLookup answers variable names and counts what it was asked, so a
// test can assert what was NEVER asked for.
type countingLookup struct {
	values map[string]string
	err    error
	asked  []string
}

func (c *countingLookup) lookup(name string) (string, bool, error) {
	c.asked = append(c.asked, name)
	if c.err != nil {
		return "", false, c.err
	}
	v, ok := c.values[name]
	return v, ok, nil
}

// ─── the three schemes ─────────────────────────────────────────────────────

// TestApply_Bearer is the happy path, all the way to the wire: a variable
// name in the file, the value from the binding store, the header the server
// actually receives.
func TestApply_Bearer(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	look := &countingLookup{values: map[string]string{"token": "t0k3n"}}
	r, err := Apply(apicoll.Request{
		Method: http.MethodGet, URL: srv.URL,
		Auth: apicoll.Auth{Kind: apicoll.AuthBearer, Var: "token"},
	}, look.lookup)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := New().Send(context.Background(), r, Key{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got != "Bearer t0k3n" {
		t.Errorf("Authorization = %q, want Bearer t0k3n", got)
	}
}

// TestApply_Basic: the username is the non-secret half and lives in the
// file; the password is the variable.
func TestApply_Basic(t *testing.T) {
	r, err := Apply(apicoll.Request{
		URL:  "https://api/",
		Auth: apicoll.Auth{Kind: apicoll.AuthBasic, Var: "pw", User: "ada"},
	}, (&countingLookup{values: map[string]string{"pw": "lovelace"}}).lookup)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("ada:lovelace"))
	if v := headerValue(r, "Authorization"); v != want {
		t.Errorf("Authorization = %q, want %q", v, want)
	}
}

// TestApply_APIKey: the key rides the header the endpoint names — Azure's
// is `api-key`, not `X-API-Key` — with a default for the common case.
func TestApply_APIKey(t *testing.T) {
	named, err := Apply(apicoll.Request{
		URL:  "https://api/",
		Auth: apicoll.Auth{Kind: apicoll.AuthAPIKey, Var: "k", User: "api-key"},
	}, (&countingLookup{values: map[string]string{"k": "abc"}}).lookup)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if v := headerValue(named, "api-key"); v != "abc" {
		t.Errorf("api-key = %q, want abc", v)
	}

	unnamed, err := Apply(apicoll.Request{
		URL:  "https://api/",
		Auth: apicoll.Auth{Kind: apicoll.AuthAPIKey, Var: "k"},
	}, (&countingLookup{values: map[string]string{"k": "abc"}}).lookup)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if v := headerValue(unnamed, DefaultAPIKeyHeader); v != "abc" {
		t.Errorf("%s = %q, want abc", DefaultAPIKeyHeader, v)
	}
}

// TestApply_NoneLeavesTheRequestSendable: a request with no auth passes
// through and no header is invented.
func TestApply_NoneLeavesTheRequestSendable(t *testing.T) {
	for _, kind := range []string{"", apicoll.AuthNone} {
		r, err := Apply(apicoll.Request{URL: "https://api/", Auth: apicoll.Auth{Kind: kind}}, nil)
		if err != nil {
			t.Fatalf("Apply(%q): %v", kind, err)
		}
		if len(r.Headers) != 0 {
			t.Errorf("Apply(%q) added %v, want no header", kind, r.Headers)
		}
	}
}

// TestApply_ClearsTheAuthSoTheSenderAccepts: the sender REFUSES a request
// whose auth is still set (ErrAuthUnresolved), because sending a variable's
// name as though it were the credential would be worse than refusing. Apply
// is what turns one into the other, so the cleared field is part of its
// contract rather than an implementation detail.
func TestApply_ClearsTheAuthSoTheSenderAccepts(t *testing.T) {
	r, err := Apply(apicoll.Request{
		URL:  "https://api/",
		Auth: apicoll.Auth{Kind: apicoll.AuthBearer, Var: "token"},
	}, (&countingLookup{values: map[string]string{"token": "x"}}).lookup)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if r.Auth.Kind != apicoll.AuthNone || r.Auth.Var != "" {
		t.Errorf("Auth = %+v after Apply, want it cleared", r.Auth)
	}
	if _, _, err := buildRequest(context.Background(), r); err != nil {
		t.Errorf("the sender still refuses the request Apply produced: %v", err)
	}
}

// TestApply_LeavesTheCallersRequestUntouched: the file is the truth (§6.4).
// A caller that applied auth and then saved would write the token into the
// collection folder — which is the one thing §6.3 exists to prevent.
func TestApply_LeavesTheCallersRequestUntouched(t *testing.T) {
	in := apicoll.Request{
		URL:     "https://api/",
		Headers: []apicoll.Header{{Name: "X-A", Value: "1", Enabled: true}},
		Auth:    apicoll.Auth{Kind: apicoll.AuthBearer, Var: "token"},
	}
	if _, err := Apply(in, (&countingLookup{values: map[string]string{"token": "t0k3n"}}).lookup); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if in.Auth.Kind != apicoll.AuthBearer || in.Auth.Var != "token" {
		t.Errorf("Apply mutated the caller's auth: %+v", in.Auth)
	}
	if len(in.Headers) != 1 {
		t.Errorf("Apply mutated the caller's headers: %+v", in.Headers)
	}
}

// ─── unresolved ────────────────────────────────────────────────────────────

// TestApply_AnUnboundVariableBlocksTheSendAndNamesItself is §6.5's rule
// reaching auth. It is apicoll.ErrUnresolvedVariable and not a sentinel of
// this package's own: an unbound auth variable IS an unresolved variable,
// one concept has one owner, and the surface has one message for it.
func TestApply_AnUnboundVariableBlocksTheSendAndNamesItself(t *testing.T) {
	for _, kind := range []string{apicoll.AuthBearer, apicoll.AuthBasic, apicoll.AuthAPIKey} {
		_, err := Apply(apicoll.Request{
			URL:  "https://api/",
			Auth: apicoll.Auth{Kind: kind, Var: "token", User: "u"},
		}, (&countingLookup{values: map[string]string{"other": "x"}}).lookup)
		if !errors.Is(err, apicoll.ErrUnresolvedVariable) {
			t.Errorf("Apply(%s) returned %v, want apicoll.ErrUnresolvedVariable", kind, err)
		}
		if err != nil && !strings.Contains(err.Error(), "token") {
			t.Errorf("Apply(%s) error %q does not name the variable", kind, err)
		}
	}
}

// TestApply_NeverSendsAnEmptyCredential is the sentence §6.5 spends a
// paragraph on: `Authorization: Bearer ` is a plausible-looking request
// that teaches the wrong lesson about why it was rejected. The assertion is
// that the server was never reached at all.
func TestApply_NeverSendsAnEmptyCredential(t *testing.T) {
	var reached atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
	}))
	defer srv.Close()

	r, err := Apply(apicoll.Request{
		Method: http.MethodGet, URL: srv.URL,
		Auth: apicoll.Auth{Kind: apicoll.AuthBearer, Var: "token"},
	}, (&countingLookup{}).lookup)
	if err == nil {
		_, _ = New().Send(context.Background(), r, Key{})
		t.Fatal("Apply succeeded with an unbound variable")
	}
	if reached.Load() != 0 {
		t.Errorf("the server was reached %d times, want 0", reached.Load())
	}
}

// TestApply_RefusesAuthThatNamesNoVariableAtAll: a bearer auth with an
// empty Var is not "no auth" — the user asked for a credential and there is
// none, so the send is blocked rather than quietly downgraded to anonymous.
func TestApply_RefusesAuthThatNamesNoVariableAtAll(t *testing.T) {
	_, err := Apply(apicoll.Request{URL: "https://api/", Auth: apicoll.Auth{Kind: apicoll.AuthBearer}}, nil)
	if !errors.Is(err, apicoll.ErrUnresolvedVariable) {
		t.Fatalf("Apply returned %v, want it blocked", err)
	}
}

// TestApply_RefusesAnAuthKindItDoesNotKnow: three schemes, no more (§2). A
// fourth is refused rather than sent without its credential.
func TestApply_RefusesAnAuthKindItDoesNotKnow(t *testing.T) {
	if _, err := Apply(apicoll.Request{URL: "https://api/", Auth: apicoll.Auth{Kind: "oauth2", Var: "t"}},
		(&countingLookup{values: map[string]string{"t": "x"}}).lookup); err == nil {
		t.Fatal("Apply accepted an auth kind it does not implement")
	}
}

// TestApply_ReportsALookupFailure is §12.1 for the one external call auth
// makes. A sealed vault is not an unresolved variable — different sentence,
// different remedy — so the two must not be flattened into one.
func TestApply_ReportsALookupFailure(t *testing.T) {
	sealed := errors.New("vault is sealed")
	_, err := Apply(apicoll.Request{URL: "https://api/", Auth: apicoll.Auth{Kind: apicoll.AuthBearer, Var: "t"}},
		(&countingLookup{err: sealed}).lookup)
	if !errors.Is(err, sealed) {
		t.Fatalf("Apply returned %v, want the lookup's own failure", err)
	}
	if errors.Is(err, apicoll.ErrUnresolvedVariable) {
		t.Error("a failed lookup was reported as an unbound variable")
	}
}

// TestApply_NeverPutsTheCredentialInAnError: an error is written to a log
// the user did not choose, which is exactly where a token must not be.
func TestApply_NeverPutsTheCredentialInAnError(t *testing.T) {
	_, err := Apply(apicoll.Request{URL: "https://api/", Auth: apicoll.Auth{Kind: "oauth2", Var: "t"}},
		(&countingLookup{values: map[string]string{"t": "s3cr3t-material"}}).lookup)
	if err == nil {
		t.Fatal("Apply accepted an unknown kind")
	}
	if strings.Contains(err.Error(), "s3cr3t-material") {
		t.Fatalf("the error carries the credential: %q", err)
	}
}

// ─── the test that matters most ────────────────────────────────────────────

// TestApply_AVaultIdentifierIsJustAnUnknownVariableName.
//
// The file says `"auth": {"kind":"bearer","var":"keychain:ssh-prod-…"}` —
// a raw vault identifier belonging to an SSH profile, which is precisely
// what nocx-jb20.1 closed and what §8 must make unspellable rather than
// merely refused.
//
// Three assertions, and NONE of them is "a guard fired":
//
//  1. The variable does not resolve, so the send is blocked as unresolved —
//     the same answer any other unbound name gets. The identifier is not
//     special; it is just a name nobody bound.
//  2. THE VAULT IS NEVER ASKED. The lookup was consulted with a variable
//     name and answered "not bound"; nothing downstream of it ran. The
//     file's CONTENT is irrelevant because there is no path from it to a
//     vault read, not because something inspected it.
//  3. The server is never reached, so no credential of any kind went out.
func TestApply_AVaultIdentifierIsJustAnUnknownVariableName(t *testing.T) {
	var reached atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
	}))
	defer srv.Close()

	// The reader's own environment. `token` is bound, as any collection of
	// theirs would be; the SSH profile's passphrase is NOT among the
	// variables, because it is not a variable — it is vault material the
	// binding store never bound to a name.
	look := &countingLookup{values: map[string]string{"token": "the reader's own token"}}

	// It is deliberately shaped like a real vault identifier: the point of
	// the test is that the shape buys the file nothing.
	//nolint:gosec // G101: an identifier a hostile FILE might write, not a credential — it names nothing and this test asserts it resolves to nothing
	const sshProfileSecret = "keychain:nocx-ssh-prod-bastion-passphrase"
	_, err := Apply(apicoll.Request{
		Method: http.MethodGet, URL: srv.URL,
		Auth: apicoll.Auth{Kind: apicoll.AuthBearer, Var: sshProfileSecret},
	}, look.lookup)

	if !errors.Is(err, apicoll.ErrUnresolvedVariable) {
		t.Fatalf("Apply returned %v, want the ordinary unresolved-variable answer", err)
	}
	// 2 — and this is the assertion the whole test exists for. The lookup
	// saw a NAME and nothing else; there is no second call, no id-shaped
	// branch, no fallback that reads by identifier.
	if len(look.asked) != 1 || look.asked[0] != sshProfileSecret {
		t.Fatalf("the lookup was asked %v; want exactly the variable name, once", look.asked)
	}
	if reached.Load() != 0 {
		t.Errorf("the server was reached %d times, want 0", reached.Load())
	}
}

// TestPackageCannotSpellASecretIdentifier is the same claim made
// structurally, and it is what turns the test above from "this input did
// not work" into "no input can". §8's second bullet: removing the SPELLING
// removes the attack, and needs no check anywhere.
//
// The split between direct and transitive is deliberate and matches
// internal/apiimport's noexec test: internal/ssh reaches credential (a
// dialer needs a pooled connection, and connections have credentials), so
// the honest claim is about what THIS package can WRITE — there is no
// import through which credential.SecretID could be named, and no
// identifier of that name anywhere in its source.
func TestPackageCannotSpellASecretIdentifier(t *testing.T) {
	forbiddenImports := []string{
		"github.com/shady2k/nocx/internal/apibind",    // the only holder of an identifier
		"github.com/shady2k/nocx/internal/credential", // where SecretID is declared
		"github.com/shady2k/nocx/internal/vault",      // where one is minted and spent
		"github.com/shady2k/nocx/internal/capability", // where one is resolved for a caller
	}
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
				p, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatalf("%s: bad import path %s", name, imp.Path.Value)
				}
				for _, bad := range forbiddenImports {
					if p == bad {
						t.Errorf("%s imports %q — auth is built from a VARIABLE NAME, never from an identifier (§8)", name, bad)
					}
				}
			}
			// And no identifier of that name, however it were reached.
			ast.Inspect(f, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && strings.Contains(id.Name, "SecretID") {
					t.Errorf("%s names %q; there is no syntax in this package for a stored-secret identifier", name, id.Name)
				}
				return true
			})
		}
	}
	if files < 5 {
		t.Fatalf("walked %d non-test files — the walk found nothing to check", files)
	}
	t.Logf("walked %d non-test files", files)
}

// TestApply_SubstitutionAndAuthComposeIntoOneSend is the whole path a user
// walks, in one test: a file naming variables in the URL, a header and the
// auth; an environment supplying the plain ones; the binding store
// supplying the secret; and the request the server actually receives.
func TestApply_SubstitutionAndAuthComposeIntoOneSend(t *testing.T) {
	var gotAuth, gotTrace, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotTrace, gotPath = r.Header.Get("Authorization"), r.Header.Get("X-Trace"), r.URL.Path
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	env := apicoll.Environment{
		Name:       "dev",
		Values:     map[string]string{"baseUrl": srv.URL, "trace": "t-1"},
		SecretVars: []string{"token"},
		Route:      apicoll.Route{Kind: apicoll.RouteDirect},
	}
	secrets := &countingLookup{values: map[string]string{"token": "t0k3n"}}
	look := apicoll.Chain(env.Lookup(), secrets.lookup)

	file := apicoll.Request{
		Method:  http.MethodGet,
		URL:     "{{baseUrl}}/users",
		Headers: []apicoll.Header{{Name: "X-Trace", Value: "{{trace}}", Enabled: true}},
		Auth:    apicoll.Auth{Kind: apicoll.AuthBearer, Var: "token"},
	}
	resolved, err := apicoll.Substitute(file, look)
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	ready, err := Apply(resolved, look)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := New().Send(context.Background(), ready, Key{}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if gotPath != "/users" {
		t.Errorf("path = %q, want /users", gotPath)
	}
	if gotTrace != "t-1" {
		t.Errorf("X-Trace = %q, want t-1", gotTrace)
	}
	if gotAuth != "Bearer t0k3n" {
		t.Errorf("Authorization = %q, want Bearer t0k3n", gotAuth)
	}
	// The file is unchanged: nothing here wrote the token back into it.
	if file.URL != "{{baseUrl}}/users" || file.Auth.Var != "token" {
		t.Errorf("the request from the file was mutated: %+v", file)
	}
}

func headerValue(r apicoll.Request, name string) string {
	for _, h := range r.Headers {
		if strings.EqualFold(h.Name, name) && h.Enabled {
			return h.Value
		}
	}
	return ""
}
