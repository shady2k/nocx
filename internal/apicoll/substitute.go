package apicoll

// Variable substitution: `{{var}}` resolves in the URL, the headers, the
// query, the body and the auth (design §6.5). An auth field is text like
// every other — a person pastes a token and it is SENT; a person writes
// `{{token}}` and it resolves in the same pass as the address above it.
//
// The rule that shapes this file is the one §6.5 states and then explains:
// AN UNRESOLVED VARIABLE BLOCKS THE SEND AND NAMES ITSELF. Not the literal
// braces sent to the server, not an empty string quietly substituted. The
// empty-string version is worse than the failure it hides — an
// `Authorization: Bearer ` header is a plausible-looking request that
// teaches the wrong lesson about why it was rejected.
//
// Substitution takes a Lookup rather than an Environment because a secret
// variable's value is NOT in the environment file (§6.3): the file names it
// and the binding document holds it. Composing the two is Chain's job, and
// this package deliberately never learns which half an answer came from —
// it holds no identifier for stored credential material and no way to ask
// for one.

import (
	"errors"
	"fmt"
	"strings"
)

// varOpen and varClose delimit a reference. Two braces, matching what a
// Postman collection already spells, which is the whole reason the format
// is not ours to choose.
const (
	varOpen  = "{{"
	varClose = "}}"

	// maxVarNameLen bounds what is scanned as a name. A "variable" longer
	// than this is text that happens to contain braces.
	maxVarNameLen = 128
)

// ErrUnresolvedVariable is the send-blocking answer of §6.5. It is a
// sentinel because it is a distinct sentence for the user — "you have not
// given this variable a value" is not "the vault is locked" and not "the
// server refused" — and a surface that cannot tell them apart offers the
// wrong remedy.
var ErrUnresolvedVariable = errors.New("apicoll: the request uses a variable with no value")

// VarUse is one unresolved reference: which variable, and where it was
// found. Field is written for a person — "the URL", `header "Authorization"
// value` — because "token is unresolved" in a request with four references
// to it is not enough to act on.
type VarUse struct {
	Name  string `json:"name"`
	Field string `json:"field"`
}

// UnresolvedError names EVERY unresolved reference, not the first. A
// request missing three values is three things to fix, and reporting them
// one at a time is three round trips through a send that was never going to
// happen.
type UnresolvedError struct {
	Uses []VarUse
}

func (e *UnresolvedError) Error() string {
	parts := make([]string, 0, len(e.Uses))
	for _, u := range e.Uses {
		parts = append(parts, fmt.Sprintf("%s (in %s)", u.Name, u.Field))
	}
	return fmt.Sprintf("%s: %s", ErrUnresolvedVariable.Error(), strings.Join(parts, ", "))
}

// Unwrap makes errors.Is(err, ErrUnresolvedVariable) true while errors.As
// still reaches the list, so a surface can either give the one sentence or
// enumerate the variables.
func (e *UnresolvedError) Unwrap() error { return ErrUnresolvedVariable }

// ErrSecretShadowed is a request-scope variable whose name the environment
// declares SECRET.
//
// It is a refusal rather than a precedence rule, and the difference is §8.
// A credential belongs in the vault; a request file goes into git. Letting a
// row in that file answer a name the environment reserved for a stored value
// would let a collection arriving in a pull request choose what a reader's
// request sends — with the reader's own environment saying the value is
// secret and the file quietly supplying a different one. The request's
// variables win over the environment's PLAIN values, which is the whole
// point of them; they do not win over the vault, and the one case where the
// two meet is refused by name instead of being decided silently either way.
//
// Refused rather than ignored, because ignoring it is the same shape read
// from the other side: a person editing the request would see their value in
// the table and something else on the wire.
var ErrSecretShadowed = errors.New("apicoll: a request variable would shadow a name this environment declares secret")

type secretShadowedError struct {
	name string
}

func (e *secretShadowedError) Error() string {
	return fmt.Sprintf("%s: %q", ErrSecretShadowed, e.name)
}

func (e *secretShadowedError) Unwrap() error { return ErrSecretShadowed }

// SecretShadowedName returns the request or folder variable name that caused
// ErrSecretShadowed. The name is for the scope explanation only; callers must
// still preserve the refusal sentence from the original error.
func SecretShadowedName(err error) (string, bool) {
	var shadow *secretShadowedError
	if !errors.As(err, &shadow) {
		return "", false
	}
	return shadow.name, true
}

// RequestLookup answers a request's own variables and its inherited folder
// variables, refusing either scope when it would shadow an environment
// secret. Folder rows arrive nearest-first from ReadRequest, so the existing
// first-row-wins rule gives request → nearest folder → parent folders.
//
// Disabled rows answer nothing, exactly as a disabled header sends nothing:
// a row the user keeps and has switched off takes no part in a send, so it
// cannot resolve a reference and cannot shadow anything either.
//
// env is the environment the send goes out under, and the zero value is the
// honest argument for a send that names none — it declares nothing secret,
// so nothing is refused and the request's own variables resolve as they
// would anywhere else.
func RequestLookup(r Request, env Environment) (Lookup, error) {
	values := make(map[string]string, len(r.Variables)+len(r.folderVariables))
	add := func(rows []Param) error {
		for _, v := range rows {
			name := strings.TrimSpace(v.Name)
			if !v.Enabled || name == "" {
				continue
			}
			if env.IsSecret(name) {
				return &secretShadowedError{name: name}
			}
			// FIRST ROW WINS, so a duplicate name is the one nearer the top
			// of the table — or, for inherited rows, the nearest folder.
			if _, already := values[name]; !already {
				values[name] = v.Value
			}
		}
		return nil
	}
	if err := add(r.Variables); err != nil {
		return nil, err
	}
	if err := add(r.folderVariables); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		// Nothing to answer: a nil Lookup, which Chain skips. A closure
		// that always answered "not found" would be the same thing at the
		// cost of a call per reference.
		return nil, nil
	}
	return func(name string) (string, bool, error) {
		v, ok := values[name]
		return v, ok, nil
	}, nil
}

// Lookup answers what a variable is worth. Three returns, and the middle
// one is the point: "not bound" is a NORMAL state that blocks the send,
// while an error is the lookup itself failing — a sealed vault, an
// unreadable document. Flattening them into one would tell a user to bind a
// variable they had already bound.
type Lookup func(name string) (value string, found bool, err error)

// Chain tries each lookup in order and returns the first answer. A nil
// entry is skipped, so a caller with no binding store composes the same way
// as one with it.
//
// It is a function here rather than two lines at each call site because the
// ORDER is load-bearing and one behaviour has one owner (AGENTS.md): the
// environment's plain values first, the binding document's secret values
// after. A failure stops the walk — a sealed vault must never fall through
// to a later lookup with a stale answer, because that is a request going
// out with the wrong credential rather than not going out at all.
func Chain(lookups ...Lookup) Lookup {
	return func(name string) (string, bool, error) {
		for _, l := range lookups {
			if l == nil {
				continue
			}
			v, ok, err := l(name)
			if err != nil {
				return "", false, err
			}
			if ok {
				return v, true, nil
			}
		}
		return "", false, nil
	}
}

// Value answers a variable from the environment's PLAIN values.
//
// A variable the file declares secret is never answered here, even when the
// file also carries a plain value under that name. The environment file
// holds no secret values (§6.3), so such a value is either a mistake or a
// collection arriving in a pull request choosing what the reader's request
// sends — and the second is exactly what §8 exists to make impossible. The
// declaration wins; the binding document answers the name.
func (e Environment) Value(name string) (string, bool) {
	if e.IsSecret(name) {
		return "", false
	}
	v, ok := e.Values[name]
	return v, ok
}

// IsSecret reports whether the environment declares name as a secret
// variable.
func (e Environment) IsSecret(name string) bool {
	for _, s := range e.SecretVars {
		if s == name {
			return true
		}
	}
	return false
}

// Lookup is the environment as a Lookup, for Chain. It never fails: a file
// already read is not an external call.
func (e Environment) Lookup() Lookup {
	return func(name string) (string, bool, error) {
		v, ok := e.Value(name)
		return v, ok, nil
	}
}

// Substitute resolves every reference in r and returns the resolved
// request. The caller's request is NOT modified: the file is the truth
// (§6.4) and a resolved request is a projection of it, so a caller that
// substituted and then saved would write the token into the collection
// folder.
// Five places, and there is a test for each: a substitution that works in
// four out of five is the shape that ships.
//
// The auth is among them since nocx-6hg2w.20: an auth field is text like
// every other, so a `{{token}}` written into it resolves in the same pass
// as one written into the URL. There is exactly ONE resolver from here on;
// nothing resolves an auth variable a second time.
//
// Two places it deliberately does NOT reach:
//
//   - Body.FileRef. A file reference is a path inside a hostile collection
//     folder (§13.1); a variable expanded into a path is a traversal the
//     path rules never see, because they validate the literal text in the
//     file and this would hand them something else.
//   - A disabled row. It is a row the user keeps and does not send, so a
//     variable in one cannot block a send it takes no part in.
//
// On failure the ZERO request comes back, so a caller that ignores the
// error has nothing plausible to send either.
func Substitute(r Request, look Lookup) (Request, error) {
	s := substituter{look: look}

	out := r
	out.URL = s.expand(r.URL, "the URL")
	out.Headers = cloneHeaders(r.Headers)
	for i := range out.Headers {
		if !out.Headers[i].Enabled {
			continue
		}
		name := out.Headers[i].Name
		out.Headers[i].Name = s.expand(name, fmt.Sprintf("header %q name", name))
		out.Headers[i].Value = s.expand(out.Headers[i].Value, fmt.Sprintf("header %q value", name))
	}
	out.Query = cloneParams(r.Query)
	for i := range out.Query {
		if !out.Query[i].Enabled {
			continue
		}
		name := out.Query[i].Name
		out.Query[i].Name = s.expand(name, fmt.Sprintf("query %q name", name))
		out.Query[i].Value = s.expand(out.Query[i].Value, fmt.Sprintf("query %q value", name))
	}
	// An auth block whose scheme is none is inert, exactly like a disabled
	// row: stray braces in a field no send reads must not block a request
	// that goes out anonymously.
	if r.Auth.Kind == AuthBearer || r.Auth.Kind == AuthBasic || r.Auth.Kind == AuthAPIKey {
		out.Auth = r.Auth
		out.Auth.Token = s.expand(r.Auth.Token, "the auth token")
		out.Auth.Password = s.expand(r.Auth.Password, "the auth password")
		out.Auth.User = s.expand(r.Auth.User, "the auth user")
	}
	if r.Body.Kind == BodyRaw || r.Body.Kind == BodyForm {
		out.Body.Text = s.expand(r.Body.Text, "the body")
	}

	if s.err != nil {
		return Request{}, s.err
	}
	if len(s.missing) > 0 {
		return Request{}, &UnresolvedError{Uses: s.missing}
	}
	return out, nil
}

// substituter carries the two ways one pass can fail. Both are collected
// rather than returned early, because the user wants every missing variable
// at once — and a lookup failure ends the pass, since every later answer
// from a broken lookup would be a guess.
type substituter struct {
	look    Lookup
	missing []VarUse
	err     error
}

// expand rewrites one string. The output is built forward and a substituted
// value is NEVER rescanned: a bound value containing `{{b}}` is data, and
// rescanning it would let whoever set the value choose which other variable
// the request resolves.
func (s *substituter) expand(in, field string) string {
	if !strings.Contains(in, varOpen) {
		return in
	}
	var b strings.Builder
	rest := in
	for {
		i := strings.Index(rest, varOpen)
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:i])
		after := rest[i+len(varOpen):]
		j := strings.Index(after, varClose)
		if j < 0 {
			// An unterminated `{{` is text. There is nothing to resolve and
			// nothing to complain about — a body full of braces is a body.
			b.WriteString(rest[i:])
			return b.String()
		}
		name := strings.TrimSpace(after[:j])
		if !validVarName(name) {
			// Not a reference: `{{"a":1}}` is JSON. Write the opening
			// braces and resume INSIDE them, so a real reference nested in
			// braces is still found.
			b.WriteString(varOpen)
			rest = after
			continue
		}
		b.WriteString(s.resolve(name, field))
		rest = after[j+len(varClose):]
	}
}

// resolve answers one reference, recording why it could not. The text it
// returns on failure is discarded — Substitute returns the zero request —
// and is the reference itself only so that a debugger printing the partial
// build sees what was being resolved.
func (s *substituter) resolve(name, field string) string {
	if s.err != nil {
		return varOpen + name + varClose
	}
	if s.look == nil {
		s.missing = append(s.missing, VarUse{Name: name, Field: field})
		return varOpen + name + varClose
	}
	v, ok, err := s.look(name)
	switch {
	case err != nil:
		s.err = err
		return varOpen + name + varClose
	case !ok:
		s.missing = append(s.missing, VarUse{Name: name, Field: field})
		return varOpen + name + varClose
	}
	// An empty BOUND value is a value: the user said so. Only an unbound
	// name blocks — which is the distinction the whole of §6.5 rests on.
	return v
}

// validVarName decides what is a reference and what is text that happens to
// contain braces. Letters, digits, `_`, `-` and `.` — a superset of what
// the importer mints (internal/apiimport/names.go) and a subset of what a
// JSON body contains, which is what keeps `{{"a":1}}` a body rather than a
// variable nobody bound.
//
// The cost of the rule is real and worth stating: a typo with a space in it
// — `{{ my token }}` — is sent as literal text rather than reported. That
// is the trade against turning every brace-heavy body into a false refusal,
// and the body is the far more common case.
func validVarName(name string) bool {
	if name == "" || len(name) > maxVarNameLen {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

// ExactReference reports whether s is EXACTLY one `{{name}}` reference and
// the name inside it — the whole string and nothing else. It is the grammar
// validVarName applies, exported for a caller that must know whether a
// FIELD resolved as a single variable (capability.Snapshot answers "which
// auth field came from the binding document" this way). A value that is
// partly text — `Bearer {{token}}` — is not a bare reference and gets "", ok.
func ExactReference(s string) (string, bool) {
	if !strings.HasPrefix(s, varOpen) || !strings.HasSuffix(s, varClose) || len(s) < len(varOpen)+len(varClose) {
		return "", false
	}
	name := strings.TrimSpace(s[len(varOpen) : len(s)-len(varClose)])
	if !validVarName(name) {
		return "", false
	}
	return name, true
}

func cloneHeaders(in []Header) []Header {
	if in == nil {
		return nil
	}
	out := make([]Header, len(in))
	copy(out, in)
	return out
}

func cloneParams(in []Param) []Param {
	if in == nil {
		return nil
	}
	out := make([]Param, len(in))
	copy(out, in)
	return out
}
