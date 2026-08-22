package apicoll_test

// A REQUEST HAS VARIABLES OF ITS OWN (nocx-kprt4.1, .2), and the whole of
// what that means is an ORDER and one REFUSAL.
//
// The order: request → environment. `id` in `/users/{{id}}` belongs to the
// request, because two requests legitimately want different ones; `baseUrl`
// belongs to the environment, because every request under it wants the same
// one. So the request's answer wins and the environment's is inherited —
// nothing that resolved before resolves differently.
//
// The refusal: a request row whose name the environment declares SECRET. A
// credential belongs in the vault and a request file goes into git, so the
// two meeting is refused by name rather than decided silently in either
// direction (design §8).

import (
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
)

func on(name, value string) apicoll.Param {
	return apicoll.Param{Name: name, Value: value, Enabled: true}
}

// resolve runs one substitution through the order the send path builds.
func resolve(t *testing.T, r apicoll.Request, env apicoll.Environment) (apicoll.Request, error) {
	t.Helper()
	own, err := apicoll.RequestLookup(r, env)
	if err != nil {
		return apicoll.Request{}, err
	}
	return apicoll.Substitute(r, apicoll.Chain(own, env.Lookup()))
}

// BOTH DIRECTIONS, which is what makes this an order rather than a
// replacement: the request wins where it answers, and the environment still
// answers everything else.
func TestRequestVariables_TheRequestWinsAndTheEnvironmentIsInherited(t *testing.T) {
	env := apicoll.Environment{
		Name:   "dev",
		Values: map[string]string{"baseUrl": "https://api.example.test", "id": "the-environment-s"},
	}
	req := apicoll.Request{
		Method:    "GET",
		URL:       "{{baseUrl}}/users/{{id}}",
		Variables: []apicoll.Param{on("id", "42")},
	}

	got, err := resolve(t, req, env)
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if got.URL != "https://api.example.test/users/42" {
		t.Fatalf("url = %q — want the request's own id and the environment's baseUrl", got.URL)
	}
}

// The inheritance on its own: a request with variables of its own does not
// stop the environment answering the names it does not.
func TestRequestVariables_ANameOnlyTheEnvironmentAnswersStillResolves(t *testing.T) {
	env := apicoll.Environment{Name: "dev", Values: map[string]string{"baseUrl": "https://x.test"}}
	req := apicoll.Request{
		Method:    "GET",
		URL:       "{{baseUrl}}/users/{{id}}",
		Variables: []apicoll.Param{on("id", "42")},
	}
	got, err := resolve(t, req, env)
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if got.URL != "https://x.test/users/42" {
		t.Errorf("url = %q", got.URL)
	}
}

// EVERY FIELD, because a substitution that works in three places out of four
// is the shape that ships. The request's own variables reach the same four
// places the environment's do.
func TestRequestVariables_ResolveInEveryFieldSubstitutionReaches(t *testing.T) {
	req := apicoll.Request{
		Method:    "POST",
		URL:       "https://x.test/{{v}}",
		Headers:   []apicoll.Header{{Name: "X-Probe", Value: "h-{{v}}", Enabled: true}},
		Query:     []apicoll.Param{on("q", "q-{{v}}")},
		Body:      apicoll.Body{Kind: apicoll.BodyRaw, Text: "b-{{v}}"},
		Variables: []apicoll.Param{on("v", "here")},
	}
	got, err := resolve(t, req, apicoll.Environment{})
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	for what, saw := range map[string]string{
		"the URL":    got.URL,
		"the header": got.Headers[0].Value,
		"the query":  got.Query[0].Value,
		"the body":   got.Body.Text,
	} {
		if !strings.Contains(saw, "here") {
			t.Errorf("%s = %q, want the request's own variable resolved into it", what, saw)
		}
	}
}

// WITH NO ENVIRONMENT AT ALL the request's variables still resolve — the
// zero Environment declares nothing and answers nothing, which is the honest
// argument for a send that names none.
func TestRequestVariables_ResolveWithNoEnvironment(t *testing.T) {
	req := apicoll.Request{
		Method:    "GET",
		URL:       "https://x.test/users/{{id}}",
		Variables: []apicoll.Param{on("id", "42")},
	}
	got, err := resolve(t, req, apicoll.Environment{})
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if got.URL != "https://x.test/users/42" {
		t.Errorf("url = %q", got.URL)
	}
}

// A DISABLED ROW ANSWERS NOTHING, exactly as a disabled header sends
// nothing: it is a row the person keeps and has switched off, so it takes no
// part in the send — and the environment answers instead, which is the
// visible consequence.
func TestRequestVariables_ADisabledRowTakesNoPart(t *testing.T) {
	env := apicoll.Environment{Values: map[string]string{"id": "from-the-environment"}}
	req := apicoll.Request{
		Method:    "GET",
		URL:       "https://x.test/{{id}}",
		Variables: []apicoll.Param{{Name: "id", Value: "switched-off", Enabled: false}},
	}
	got, err := resolve(t, req, env)
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if got.URL != "https://x.test/from-the-environment" {
		t.Errorf("url = %q, want the disabled row to have taken no part", got.URL)
	}
}

// THE REFUSAL. A request row cannot answer a name the environment declares
// secret: that is a file in git choosing what a reader's request sends,
// against a vault-held value the reader deliberately kept out of it.
func TestRequestVariables_ARowShadowingASecretIsRefusedByName(t *testing.T) {
	env := apicoll.Environment{Name: "dev", SecretVars: []string{"token"}}
	req := apicoll.Request{
		Method:    "GET",
		URL:       "https://x.test/bot{{token}}/send",
		Variables: []apicoll.Param{on("token", "the-file-s-own-value")},
	}

	_, err := apicoll.RequestLookup(req, env)
	if !errors.Is(err, apicoll.ErrSecretShadowed) {
		t.Fatalf("err = %v, want ErrSecretShadowed", err)
	}
	if !strings.Contains(err.Error(), "token") {
		t.Errorf("err = %v, want it to name the variable", err)
	}
	// NEVER THE VALUE. The refusal is written where a person did not choose
	// to put it, and the row it is about holds something somebody typed.
	if strings.Contains(err.Error(), "the-file-s-own-value") {
		t.Fatalf("the refusal carries the row's value: %v", err)
	}
}

// …and it is the SECRET DECLARATION that refuses, not the name: the same
// row against an environment that declares nothing secret is an ordinary
// request variable. Without this the refusal could be "any row named token".
func TestRequestVariables_TheSameRowIsFineWhereNothingIsDeclaredSecret(t *testing.T) {
	req := apicoll.Request{
		Method:    "GET",
		URL:       "https://x.test/bot{{token}}/send",
		Variables: []apicoll.Param{on("token", "an-ordinary-value")},
	}
	got, err := resolve(t, req, apicoll.Environment{Name: "dev"})
	if err != nil {
		t.Fatalf("RequestLookup: %v", err)
	}
	if got.URL != "https://x.test/botan-ordinary-value/send" {
		t.Errorf("url = %q", got.URL)
	}
}

// A DISABLED ROW CANNOT SHADOW ANYTHING either, because it resolves nothing:
// refusing it would refuse a send over a row taking no part in it.
func TestRequestVariables_ADisabledRowDoesNotShadowASecret(t *testing.T) {
	env := apicoll.Environment{Name: "dev", SecretVars: []string{"token"}}
	req := apicoll.Request{
		Method:    "GET",
		URL:       "https://x.test/x",
		Variables: []apicoll.Param{{Name: "token", Value: "off", Enabled: false}},
	}
	if _, err := apicoll.RequestLookup(req, env); err != nil {
		t.Fatalf("a switched-off row was refused: %v", err)
	}
}

// A DUPLICATE NAME: the first row wins, which is the one nearer the top of
// the table a person is reading — and the same answer on every machine.
func TestRequestVariables_TheFirstRowOfADuplicateNameWins(t *testing.T) {
	req := apicoll.Request{
		Method:    "GET",
		URL:       "https://x.test/{{id}}",
		Variables: []apicoll.Param{on("id", "first"), on("id", "second")},
	}
	got, err := resolve(t, req, apicoll.Environment{})
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if got.URL != "https://x.test/first" {
		t.Errorf("url = %q, want the first row's value", got.URL)
	}
}

// A NAMELESS ROW is not a variable. The editor's tables all allow one — it is
// what a half-typed row looks like — and it must resolve nothing rather than
// answer the empty name.
func TestRequestVariables_ANamelessRowIsNotAVariable(t *testing.T) {
	req := apicoll.Request{
		Method:    "GET",
		URL:       "https://x.test/{{id}}",
		Variables: []apicoll.Param{{Name: "   ", Value: "x", Enabled: true}},
	}
	if _, err := resolve(t, req, apicoll.Environment{}); err == nil {
		t.Fatal("the send resolved with only a nameless row to answer with")
	}
}
