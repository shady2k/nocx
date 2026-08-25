package apicoll

// Design §6.5: `{{var}}` resolves in URL, headers, query and body — and an
// unresolved variable BLOCKS THE SEND AND NAMES ITSELF. One that works in
// three places out of four is the shape that ships, so there is a test for
// each of the four, and a test for each of the two things the resolution is
// forbidden to do instead of failing.

import (
	"errors"
	"strings"
	"testing"
)

// staticLookup is an environment's answers, as a Lookup.
func staticLookup(m map[string]string) Lookup {
	return func(name string) (string, bool, error) {
		v, ok := m[name]
		return v, ok, nil
	}
}

// TestSubstitute_ResolvesInURLHeadersQueryAndBody is the acceptance
// criterion for the substitution: all four places, in one request, in one
// pass.
func TestSubstitute_ResolvesInURLHeadersQueryAndBody(t *testing.T) {
	in := Request{
		Method: "POST",
		URL:    "{{baseUrl}}/users/{{userId}}",
		Headers: []Header{
			{Name: "X-{{headerName}}", Value: "v-{{headerValue}}", Enabled: true},
		},
		Query: []Param{
			{Name: "q-{{queryName}}", Value: "{{queryValue}}", Enabled: true},
		},
		Body: Body{Kind: BodyRaw, Text: `{"tenant":"{{tenant}}"}`},
		Auth: Auth{Kind: AuthBearer, Token: "{{token}}"},
	}
	look := staticLookup(map[string]string{
		"baseUrl":     "https://api.example.com",
		"userId":      "42",
		"headerName":  "Trace",
		"headerValue": "abc",
		"queryName":   "page",
		"queryValue":  "2",
		"tenant":      "acme",
		"token":       "t0k3n",
	})

	got, err := Substitute(in, look)
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if got.URL != "https://api.example.com/users/42" {
		t.Errorf("URL = %q, want the resolved URL", got.URL)
	}
	if got.Headers[0].Name != "X-Trace" || got.Headers[0].Value != "v-abc" {
		t.Errorf("header = %q: %q, want X-Trace: v-abc", got.Headers[0].Name, got.Headers[0].Value)
	}
	if got.Query[0].Name != "q-page" || got.Query[0].Value != "2" {
		t.Errorf("query = %q=%q, want q-page=2", got.Query[0].Name, got.Query[0].Value)
	}
	if got.Body.Text != `{"tenant":"acme"}` {
		t.Errorf("body = %q, want the resolved body", got.Body.Text)
	}
	if got.Auth.Token != "t0k3n" {
		t.Errorf("auth Token = %q, want the resolved token — auth is text like every other field", got.Auth.Token)
	}
}

// TestSubstitute_ResolvesInAuthBearerAndBasic pushes the same text semantics
// through the two credential fields: a `{{name}}` written into a bearer
// Token or a basic Password resolves in the same pass as the URL.
func TestSubstitute_ResolvesInAuthBearerAndBasic(t *testing.T) {
	in := Request{
		Method: "GET",
		URL:    "https://api/",
		Auth: Auth{
			Kind:     AuthBasic,
			User:     "ada",
			Password: "{{pw}}",
		},
	}
	got, err := Substitute(in, staticLookup(map[string]string{"pw": "lovelace"}))
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if got.Auth.Password != "lovelace" {
		t.Errorf("auth Password = %q, want the resolved password", got.Auth.Password)
	}
	if got.Auth.User != "ada" {
		t.Errorf("auth User = %q, want the untouched literal", got.Auth.User)
	}
}

// TestSubstitute_AnUnresolvedVariableBlocksTheSendAndNamesItself is §6.5's
// rule, checked in each of the five places: the answer is an error naming
// the variable, never a request that goes out.
func TestSubstitute_AnUnresolvedVariableBlocksTheSendAndNamesItself(t *testing.T) {
	cases := []struct {
		where string
		req   Request
	}{
		{"url", Request{URL: "{{missing}}/x"}},
		{"header", Request{URL: "https://h/", Headers: []Header{{Name: "A", Value: "{{missing}}", Enabled: true}}}},
		{"query", Request{URL: "https://h/", Query: []Param{{Name: "a", Value: "{{missing}}", Enabled: true}}}},
		{"body", Request{URL: "https://h/", Body: Body{Kind: BodyRaw, Text: "{{missing}}"}}},
		{"auth", Request{URL: "https://h/", Auth: Auth{Kind: AuthBearer, Token: "{{missing}}"}}},
	}
	for _, c := range cases {
		t.Run(c.where, func(t *testing.T) {
			_, err := Substitute(c.req, staticLookup(map[string]string{"other": "x"}))
			if err == nil {
				t.Fatalf("Substitute succeeded with an unresolved variable in the %s", c.where)
			}
			if !errors.Is(err, ErrUnresolvedVariable) {
				t.Errorf("error = %v, want ErrUnresolvedVariable", err)
			}
			if !strings.Contains(err.Error(), "missing") {
				t.Errorf("error = %q, want it to NAME the variable", err)
			}
			if !strings.Contains(strings.ToLower(err.Error()), c.where) {
				t.Errorf("error = %q, want it to say where the variable is (%s)", err, c.where)
			}
		})
	}
}

// TestSubstitute_NamesEveryUnresolvedVariableNotOnlyTheFirst: a request
// missing three values is three things to fix, and reporting one at a time
// is three round trips through a failing send.
func TestSubstitute_NamesEveryUnresolvedVariableNotOnlyTheFirst(t *testing.T) {
	_, err := Substitute(Request{
		URL:     "{{a}}/x",
		Headers: []Header{{Name: "H", Value: "{{b}}", Enabled: true}},
		Body:    Body{Kind: BodyRaw, Text: "{{c}}"},
	}, staticLookup(nil))
	if err == nil {
		t.Fatal("Substitute succeeded with three unresolved variables")
	}
	for _, name := range []string{"a", "b", "c"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error = %q, want it to name %q", err, name)
		}
	}
	var unres *UnresolvedError
	if !errors.As(err, &unres) {
		t.Fatalf("error = %v, want an *UnresolvedError a surface can list", err)
	}
	if len(unres.Uses) != 3 {
		t.Errorf("UnresolvedError names %d uses, want 3", len(unres.Uses))
	}
}

// TestSubstitute_NeverSubstitutesAnEmptyStringForAMissingValue is the half
// of §6.5 that matters most: `Authorization: Bearer ` is a plausible
// request that teaches the wrong lesson about why it was rejected.
func TestSubstitute_NeverSubstitutesAnEmptyStringForAMissingValue(t *testing.T) {
	got, err := Substitute(Request{
		URL:     "https://api/",
		Headers: []Header{{Name: "Authorization", Value: "Bearer {{token}}", Enabled: true}},
	}, staticLookup(nil))
	if err == nil {
		t.Fatalf("Substitute succeeded; got header %q", got.Headers[0].Value)
	}
	// And the returned request is the zero one, so a caller that ignores
	// the error has nothing plausible to send either.
	if got.URL != "" || got.Headers != nil {
		t.Errorf("Substitute returned %+v with an error, want the zero Request", got)
	}
}

// TestSubstitute_AnEmptyBoundValueIsAValue: the pair to the rule above. A
// variable BOUND to the empty string is resolved, because the user said so;
// only an UNBOUND one blocks.
func TestSubstitute_AnEmptyBoundValueIsAValue(t *testing.T) {
	got, err := Substitute(Request{URL: "https://api/{{suffix}}"},
		staticLookup(map[string]string{"suffix": ""}))
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if got.URL != "https://api/" {
		t.Errorf("URL = %q, want https://api/", got.URL)
	}
}

// TestSubstitute_LeavesTheCallersRequestUntouched: the file is the truth
// (§6.4), so a resolved request is a projection of it and never an edit to
// it. A caller that substituted and then saved would write the token into
// the collection folder.
func TestSubstitute_LeavesTheCallersRequestUntouched(t *testing.T) {
	in := Request{
		URL:     "{{baseUrl}}/x",
		Headers: []Header{{Name: "A", Value: "{{v}}", Enabled: true}},
		Query:   []Param{{Name: "q", Value: "{{v}}", Enabled: true}},
		Body:    Body{Kind: BodyRaw, Text: "{{v}}"},
	}
	if _, err := Substitute(in, staticLookup(map[string]string{"baseUrl": "https://api", "v": "SECRET"})); err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if in.URL != "{{baseUrl}}/x" || in.Headers[0].Value != "{{v}}" ||
		in.Query[0].Value != "{{v}}" || in.Body.Text != "{{v}}" {
		t.Errorf("Substitute mutated the caller's request: %+v", in)
	}
}

// TestSubstitute_DoesNotRescanASubstitutedValue: a bound value that happens
// to contain `{{...}}` is data, not a second reference. Rescanning it is an
// injection: whoever set the value would choose which other variable the
// request resolves.
func TestSubstitute_DoesNotRescanASubstitutedValue(t *testing.T) {
	got, err := Substitute(Request{URL: "https://api/{{a}}"},
		staticLookup(map[string]string{"a": "{{b}}", "b": "SECRET"}))
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if got.URL != "https://api/{{b}}" {
		t.Errorf("URL = %q, want the value written once and never rescanned", got.URL)
	}
}

// TestSubstitute_IgnoresDisabledRows: a disabled row is a row the user
// keeps and does not send, so a variable in one cannot block the send.
func TestSubstitute_IgnoresDisabledRows(t *testing.T) {
	got, err := Substitute(Request{
		URL:     "https://api/",
		Headers: []Header{{Name: "A", Value: "{{missing}}", Enabled: false}},
		Query:   []Param{{Name: "q", Value: "{{missing}}", Enabled: false}},
	}, staticLookup(nil))
	if err != nil {
		t.Fatalf("Substitute: %v — a disabled row is not sent, so it cannot block", err)
	}
	if got.Headers[0].Value != "{{missing}}" {
		t.Errorf("disabled header = %q, want it kept verbatim", got.Headers[0].Value)
	}
}

// TestSubstitute_LeavesTextThatIsNotAVariableReference: a JSON body is full
// of braces, and `{{"a":1}}` is not a variable nobody bound — it is a body.
func TestSubstitute_LeavesTextThatIsNotAVariableReference(t *testing.T) {
	for _, text := range []string{
		`{{"a":1}}`,
		`{{ two words }}`,
		`{{}}`,
		`{{`,
		`}}`,
		`{{a`,
	} {
		got, err := Substitute(Request{URL: "https://api/", Body: Body{Kind: BodyRaw, Text: text}},
			staticLookup(nil))
		if err != nil {
			t.Errorf("Substitute(%q): %v — this is text, not a reference", text, err)
			continue
		}
		if got.Body.Text != text {
			t.Errorf("body %q became %q, want it left alone", text, got.Body.Text)
		}
	}
}

// TestSubstitute_FindsAReferenceInsideBraces: the pair to the test above —
// the scanner must not become so cautious that a real reference in a JSON
// body is missed.
func TestSubstitute_FindsAReferenceInsideBraces(t *testing.T) {
	got, err := Substitute(
		Request{URL: "https://api/", Body: Body{Kind: BodyRaw, Text: `{"id":"{{userId}}"}`}},
		staticLookup(map[string]string{"userId": "42"}))
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if got.Body.Text != `{"id":"42"}` {
		t.Errorf("body = %q, want the reference resolved", got.Body.Text)
	}
}

// TestSubstitute_NeverTouchesAFileReference: a fileRef is a path inside a
// hostile collection folder (§13.1), and a variable expanded into a path is
// a traversal the path rules never see. The reference is resolved by the
// package that owns the folder, from the literal text in the file.
func TestSubstitute_NeverTouchesAFileReference(t *testing.T) {
	got, err := Substitute(
		Request{URL: "https://api/", Body: Body{Kind: BodyFile, FileRef: "{{escape}}/body.json"}},
		staticLookup(map[string]string{"escape": "../../.ssh"}))
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if got.Body.FileRef != "{{escape}}/body.json" {
		t.Errorf("fileRef = %q, want it left verbatim", got.Body.FileRef)
	}
}

// TestSubstitute_ReportsALookupFailure is §12.1 for the one external call
// substitution makes: the lookup may reach a sealed vault, and a sealed
// vault is not an unresolved variable — it is a different sentence for the
// user, so it must not be flattened into one.
func TestSubstitute_ReportsALookupFailure(t *testing.T) {
	sealed := errors.New("vault is sealed")
	_, err := Substitute(Request{URL: "https://api/{{token}}"},
		func(string) (string, bool, error) { return "", false, sealed })
	if !errors.Is(err, sealed) {
		t.Fatalf("error = %v, want the lookup's own failure", err)
	}
	if errors.Is(err, ErrUnresolvedVariable) {
		t.Error("a failed lookup was reported as an unresolved variable; they are two different answers")
	}
}

// TestSubstitute_WithNoLookupAtAll: a nil Lookup resolves nothing rather
// than panicking, and a request with no references still passes through it.
func TestSubstitute_WithNoLookupAtAll(t *testing.T) {
	got, err := Substitute(Request{URL: "https://api/x"}, nil)
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if got.URL != "https://api/x" {
		t.Errorf("URL = %q, want it unchanged", got.URL)
	}
	if _, err := Substitute(Request{URL: "https://api/{{a}}"}, nil); !errors.Is(err, ErrUnresolvedVariable) {
		t.Errorf("error = %v, want ErrUnresolvedVariable", err)
	}
}

// TestEnvironmentValue_NeverAnswersADeclaredSecret: an environment file
// holds plain values and the NAMES of its secret variables, never their
// values (§6.3). A file that declares `token` secret AND puts a plain value
// under the same name must not shadow the value the reader bound — a
// collection arriving in a pull request would otherwise choose what the
// user's request sends.
func TestEnvironmentValue_NeverAnswersADeclaredSecret(t *testing.T) {
	env := Environment{
		Name:       "prod",
		Values:     map[string]string{"baseUrl": "https://api", "token": "planted"},
		SecretVars: []string{"token"},
	}
	if v, ok := env.Value("baseUrl"); !ok || v != "https://api" {
		t.Errorf("Value(baseUrl) = %q, %v — a plain value is answered", v, ok)
	}
	if v, ok := env.Value("token"); ok {
		t.Errorf("Value(token) = %q, true — a declared secret is never answered from the file", v)
	}
}

// TestChain_TriesInOrderAndStopsAtTheFirstAnswer pins the one thing a
// composition of lookups has to get right, in one place rather than at
// every call site.
func TestChain_TriesInOrderAndStopsAtTheFirstAnswer(t *testing.T) {
	var asked []string
	first := func(name string) (string, bool, error) {
		asked = append(asked, "first")
		if name == "a" {
			return "1", true, nil
		}
		return "", false, nil
	}
	second := func(name string) (string, bool, error) {
		asked = append(asked, "second")
		return "2", true, nil
	}
	c := Chain(nil, first, second)

	if v, ok, err := c("a"); err != nil || !ok || v != "1" {
		t.Errorf("Chain(a) = %q, %v, %v, want 1", v, ok, err)
	}
	if len(asked) != 1 || asked[0] != "first" {
		t.Errorf("asked %v, want the second lookup never consulted once the first answered", asked)
	}
	if v, ok, err := c("b"); err != nil || !ok || v != "2" {
		t.Errorf("Chain(b) = %q, %v, %v, want 2", v, ok, err)
	}
}

// TestChain_StopsAtAFailure: a sealed vault in the middle of a chain must
// not be walked past into a later lookup that happens to have a stale
// answer — that is how a request goes out with the wrong credential.
func TestChain_StopsAtAFailure(t *testing.T) {
	boom := errors.New("sealed")
	c := Chain(
		func(string) (string, bool, error) { return "", false, boom },
		func(string) (string, bool, error) { return "stale", true, nil },
	)
	v, ok, err := c("a")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the failure", err)
	}
	if ok || v != "" {
		t.Errorf("Chain returned %q, %v after a failure, want nothing", v, ok)
	}
}
