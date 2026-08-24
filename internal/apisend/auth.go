package apisend

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"github.com/shady2k/nocx/internal/apicoll"
)

// Auth: bearer, basic, api-key — three schemes, no more (design §2) — each
// mapped from the request's ALREADY-SUBSTITUTED text onto the ONE header
// Send accepts.
// pass as the URL. apisend never calls a Lookup for auth; there is exactly
// one resolver and it lives in apicoll. What Apply owns is the mapping from
// a scheme onto a header, the refusal of a scheme with an empty credential
// (§6.5 — that is still a blocked send, never a silent downgrade to
// anonymous), and the question of WHAT WAS PLACED, answered by
// construction rather than by inspecting the text.
//
// # How a value is elided, and why the second argument exists
//
// The sender must know which bytes to redact from the raw diagnostic. Two
// kinds of value end up in an auth header:
//
//   - A LITERAL the person typed. The product does not hide or move a
//     credential a person typed (the decision recorded in nocx-tg9l8): it
//     is sent, and it appears in the raw view. NOTHING is placed for it.
//   - A value the BINDING DOCUMENT answered through a variable name. That
//     is a secret by construction (apicoll.Chain: the environment's plain
//     values are tried first, so the binding answering means the binding
//     answered), and its bytes must not reach the raw diagnostic — the
//     raw view shows a chip naming the variable (ADR-0021).
//
// The caller knows WHICH of the two each field was — the substitution that
// resolved it ran beside the binding document (capability.Snapshot), so the
// name of a binding-answered field is a fact the caller holds, never a
// value it inferred from the text. Apply is handed that fact as
// authSource: the variable name a field resolved from, or "" for a literal.
//
// The reported value for basic auth is the ENCODED credential and not the
// password: nothing in the composed request contains the plaintext — it is
// base64 by the time it is a header — so a caller redacting the plaintext
// would find nothing to redact and leave the blob sitting in a JSON-RPC
// frame.
type SecretSource struct {
	// Token is the variable name the bearer/apikey credential resolved
	// from, "" when it is a literal.
	Token string
	// Password is the variable name the basic password resolved from, ""
	// when it is a literal.
	Password string
}

// DefaultAPIKeyHeader is where an api-key rides when the request does not
// name a header. Most endpoints spell it this way; the ones that do not —
// Azure's `api-key` is the common example — say so on the request, and
// guessing for them would send a header the user did not write.
//
//nolint:gosec // G101: this is a header NAME, not a key. The value it carries is resolved from a variable at Apply and appears nowhere in this file.
const DefaultAPIKeyHeader = "X-API-Key"

// Apply turns the request's auth into the ONE HEADER it becomes and clears
// the auth, which is the form Send accepts: buildRequest refuses a request
// whose auth is still set, because sending a credential's text — or worse,
// a still-unresolved `{{variable}}` — as though it were a header would be
// worse than refusing.
//
// The caller's request is NOT modified. The file is the truth (§6.4), and a
// caller that applied auth and then saved would write the token into the
// collection folder — the one thing §6.3 exists to prevent.
//
// The request is ALREADY SUBSTITUTED. The order is substitute first, then
// apply: substitution resolves the {{variables}} and a resolved credential
// that happens to contain `{{…}}` is never rescanned — the same no-rescan
// rule refuses that inside substitution.
//
// A scheme with an EMPTY credential field is still a blocked send: the
// user asked for a credential and there is none, so the request is refused
// rather than quietly downgraded to anonymous — the same reason §6.5
// refuses an empty substitution.
//
// The second return is what was placed, per authSource. A literal places
// nothing — the raw diagnostic shows it, which is the honest answer for a
// value the person typed.
func Apply(r apicoll.Request, authSource SecretSource) (apicoll.Request, []NamedSecret, error) {
	kind := r.Auth.Kind
	if kind == "" || kind == apicoll.AuthNone {
		return r, nil, nil
	}

	var (
		header apicoll.Header
		name   string // the variable name to badge the placement by, "" for a literal
	)
	switch kind {
	case apicoll.AuthBearer:
		if r.Auth.Token == "" {
			return apicoll.Request{}, nil, errEmptyCredential(kind, "token")
		}
		header = apicoll.Header{Name: "Authorization", Value: "Bearer " + r.Auth.Token, Enabled: true}
		name = authSource.Token

	case apicoll.AuthBasic:
		if r.Auth.Password == "" {
			return apicoll.Request{}, nil, errEmptyCredential(kind, "password")
		}
		// The username is the non-secret half and lives in the file; the
		// password is the credential.
		header = apicoll.Header{
			Name:    "Authorization",
			Value:   "Basic " + base64.StdEncoding.EncodeToString([]byte(r.Auth.User+":"+r.Auth.Password)),
			Enabled: true,
		}
		name = authSource.Password

	case apicoll.AuthAPIKey:
		if r.Auth.Token == "" {
			return apicoll.Request{}, nil, errEmptyCredential(kind, "token")
		}
		headerName := r.Auth.User
		if headerName == "" {
			headerName = DefaultAPIKeyHeader
		}
		header = apicoll.Header{Name: headerName, Value: r.Auth.Token, Enabled: true}
		name = authSource.Token

	default:
		// Three schemes, no more (§2). A fourth is refused rather than sent
		// without its credential — and the error names the KIND and never
		// the value the kind might be carrying, because an error is written
		// to a log a user did not choose.
		return apicoll.Request{}, nil, fmt.Errorf("%s: auth kind %q is not one of %s, %s or %s",
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

	if name == "" {
		return out, nil, nil
	}
	// What was placed is the header's value minus the scheme word: "Bearer "
	// is not a secret and marking it would badge four public characters, but
	// everything after it is the credential — including the base64 blob.
	return out, []NamedSecret{{Name: name, Value: placed(kind, header.Value)}}, nil
}

// errEmptyCredential is §6.5's rule about the EMPTY field rather than the
// unresolved one: a scheme a person chose with no credential in it is a
// blocked send, and the sentence names the field so a run shows where to
// look.
func errEmptyCredential(kind, field string) error {
	return fmt.Errorf("%w: %s auth has no %s", apicoll.ErrUnresolvedVariable, kind, field)
}

// placed strips the scheme word an Authorization header carries, so the
// marked span is the credential and not the sentence around it.
func placed(kind, headerValue string) string {
	switch kind {
	case apicoll.AuthBearer:
		return strings.TrimPrefix(headerValue, "Bearer ")
	case apicoll.AuthBasic:
		return strings.TrimPrefix(headerValue, "Basic ")
	default:
		return headerValue
	}
}
