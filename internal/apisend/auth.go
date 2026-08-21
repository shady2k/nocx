package apisend

// Auth: bearer, basic, api-key — three schemes, no more (design §2) — each
// built from a VARIABLE NAME resolved through the binding store, never from
// an identifier in a file.
//
// # Why there is nothing here that takes an identifier
//
// §8 is not a check, it is a shape. "A hostile file can write {{token}} and
// gets whatever the reader bound in their own environment; it has no way to
// spell 'the password behind the production SSH profile', because there is
// no syntax in which a file names a secret." An earlier draft proposed
// opaque vault references in files plus a resolver that refused cross-scope
// reads; it was rejected twice over, and the second reason is this file's:
// refusing a reference the file should never have been able to write is a
// guard bolted onto a format that permits the attack.
//
// So this package resolves a NAME. It cannot import the binding store, it
// cannot name a credential.SecretID, and it has no parameter through which
// one could arrive — asserted in auth_test.go by walking this package's own
// source. A file whose auth variable is a raw vault identifier has named a
// variable nobody bound, and the send is blocked exactly as it would be for
// any other misspelled name. The file's CONTENT is irrelevant, which is a
// stronger property than a check that inspects it.

import (
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/shady2k/nocx/internal/apicoll"
)

// DefaultAPIKeyHeader is where an api-key rides when the request does not
// name a header. Most endpoints spell it this way; the ones that do not —
// Azure's `api-key` is the common example — say so on the request, and
// guessing for them would send a header the user did not write.
//
//nolint:gosec // G101: this is a header NAME, not a key. The value it carries is resolved from a variable at Apply and appears nowhere in this file.
const DefaultAPIKeyHeader = "X-API-Key"

// Apply turns the request's auth into the ONE HEADER it becomes and clears
// the auth, which is the form Send accepts: buildRequest refuses a request
// whose auth is still set, because sending a variable's NAME as though it
// were the credential would be worse than refusing and sending nothing
// while the user believes they are authenticated would be the silent
// degrade AGENTS.md forbids.
//
// The caller's request is NOT modified. The file is the truth (§6.4), and a
// caller that applied auth and then saved would write the token into the
// collection folder — the one thing §6.3 exists to prevent.
//
// Order with apicoll.Substitute: substitute FIRST, then apply. Auth.Var is
// a variable name rather than text containing one, so neither pass feeds
// the other; running substitution first means a resolved credential that
// happens to contain `{{…}}` is never rescanned, which is the injection the
// no-rescan rule already refuses inside substitution.
//
// look is the same apicoll.Lookup substitution uses — one seam for "what is
// this variable worth", so an unbound auth variable and an unbound URL
// variable are one concept with one owner and one message.
func Apply(r apicoll.Request, look apicoll.Lookup) (apicoll.Request, error) {
	kind := r.Auth.Kind
	if kind == "" || kind == apicoll.AuthNone {
		return r, nil
	}

	value, err := resolveAuthVar(r.Auth, look)
	if err != nil {
		return apicoll.Request{}, err
	}

	var header apicoll.Header
	switch kind {
	case apicoll.AuthBearer:
		header = apicoll.Header{Name: "Authorization", Value: "Bearer " + value, Enabled: true}
	case apicoll.AuthBasic:
		// The username is the non-secret half and lives in the file; only
		// the password is a variable.
		header = apicoll.Header{
			Name:    "Authorization",
			Value:   "Basic " + base64.StdEncoding.EncodeToString([]byte(r.Auth.User+":"+value)),
			Enabled: true,
		}
	case apicoll.AuthAPIKey:
		name := r.Auth.User
		if name == "" {
			name = DefaultAPIKeyHeader
		}
		header = apicoll.Header{Name: name, Value: value, Enabled: true}
	default:
		// Three schemes, no more. A fourth is refused rather than sent
		// without its credential — and the error names the KIND and never
		// the value, because an error is written to a log the user did not
		// choose.
		return apicoll.Request{}, fmt.Errorf("%s: auth kind %q is not one of %s, %s or %s",
			component, kind, apicoll.AuthBearer, apicoll.AuthBasic, apicoll.AuthAPIKey)
	}

	out := r
	// A fresh slice: the header goes onto the copy and never onto the
	// caller's backing array.
	out.Headers = make([]apicoll.Header, 0, len(r.Headers)+1)
	for _, h := range r.Headers {
		// The auth header replaces a row of the same name rather than
		// joining it. Two Authorization headers is a request no server
		// reads the way the user meant, and letting a file's own row win
		// would make the auth block decorative.
		if h.Enabled && http.CanonicalHeaderKey(h.Name) == http.CanonicalHeaderKey(header.Name) {
			continue
		}
		out.Headers = append(out.Headers, h)
	}
	out.Headers = append(out.Headers, header)
	out.Auth = apicoll.Auth{Kind: apicoll.AuthNone}
	return out, nil
}

// resolveAuthVar asks for the variable and reports the two failures apart.
//
// An auth block naming no variable at all is NOT "no auth": the user asked
// for a credential and there is none, so the send is blocked rather than
// quietly downgraded to anonymous — the same reason §6.5 refuses an empty
// substitution.
func resolveAuthVar(a apicoll.Auth, look apicoll.Lookup) (string, error) {
	if a.Var == "" {
		return "", fmt.Errorf("%w: the %s auth names no variable", apicoll.ErrUnresolvedVariable, a.Kind)
	}
	if look == nil {
		return "", fmt.Errorf("%w: the auth variable %q is not bound in this environment",
			apicoll.ErrUnresolvedVariable, a.Var)
	}
	v, found, err := look(a.Var)
	if err != nil {
		// A sealed vault is not an unresolved variable: different sentence,
		// different remedy, so the two are never flattened into one.
		return "", fmt.Errorf("%s: resolving the auth variable %q: %w", component, a.Var, err)
	}
	if !found {
		return "", fmt.Errorf("%w: the auth variable %q is not bound in this environment",
			apicoll.ErrUnresolvedVariable, a.Var)
	}
	return v, nil
}
