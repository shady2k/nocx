package apiimport

import (
	"strings"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/secrets"
)

// secretOffer is one value the import will hand to the BindWriter and to
// nobody else. It exists only inside an ImportInto call: FromCurl is
// deliberately unable to return one and the Postman converter is not
// exported at all, so no credential this package LIFTED OUT OF A DOCUMENT
// AND INTO A VARIABLE can leave it by any path but the BindWriter (§8).
//
// That is a statement about values this package took charge of, and not
// about every byte a line contains: FromCurl leaves a curl line's own
// Authorization header alone, on the request, because it has no file to
// write it into and nowhere to bind it (see FromCurl).
type secretOffer struct {
	Environment string
	Variable    string
	Value       []byte
}

// varRef reports whether s is exactly one of our variable references and
// returns the name inside it. A value that is ALREADY {{token}} is not a
// secret: binding it would store the eight literal bytes of the reference
// as though they were a credential, and the request would then resolve to
// itself.
func varRef(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{{") || !strings.HasSuffix(s, "}}") || len(s) <= 4 {
		return "", false
	}
	name := strings.TrimSpace(s[2 : len(s)-2])
	if name == "" || strings.ContainsAny(name, "{}") {
		return "", false
	}
	return name, true
}

// varNamer hands out variable names that do not collide. Deterministic —
// same input, same names — because these end up in a file that is meant to
// be diffed in a pull request.
type varNamer struct{ used map[string]bool }

func newVarNamer() *varNamer { return &varNamer{used: map[string]bool{}} }

func (n *varNamer) take(base string) string {
	base = sanitizeVarName(base)
	if base == "" {
		base = "secret"
	}
	if !n.used[base] {
		n.used[base] = true
		return base
	}
	for i := 2; ; i++ {
		cand := base + "_" + itoa(i)
		if !n.used[cand] {
			n.used[cand] = true
			return cand
		}
	}
}

// reserve records a name that came from the input, so a later generated
// name does not collide with it.
func (n *varNamer) reserve(name string) {
	if name != "" {
		n.used[name] = true
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

// sanitizeVarName keeps a variable name to what {{...}} can carry.
func sanitizeVarName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r == '-' || r == '.' || r == ' ':
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// headerValueIsSecret asks the question internal/secrets already answers:
// is this text credential-shaped? Reusing that detector rather than writing
// a second one is the point — two derivations of "is this a secret" agree
// everywhere until the one place they do not (AGENTS.md), and this one is
// already tuned for precision over recall, which is what matters here: a
// false positive turns a working header into a variable nobody bound.
//
// The finding has to land in the VALUE. "Authorization" in a header name is
// not a credential; what follows the colon may be.
func headerValueIsSecret(name, value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	if _, ok := varRef(value); ok {
		return false
	}
	valueAt := len(name) + 2 // "name: "
	for _, f := range secrets.Detect(name + ": " + value) {
		if f.ValueEnd > valueAt {
			return true
		}
	}
	return false
}

// absorbHeaderSecrets is the one owner of "a header may carry a
// credential", used by both entrances that WRITE A COLLECTION — the Postman
// document and the curl line, through ImportInto. Two derivations of that
// question would agree on every header anyone tried and disagree on the one
// that mattered (AGENTS.md), and the two entrances converge on one model,
// so they converge on this too.
//
// The curl line converted for the FORM does not come here at all, and that
// is not a third derivation: it is the same question answered "there is no
// file, so nothing is absorbed" once, in parseCurl, by the argument that
// says so.
//
// It returns the headers that survive, the auth an Authorization header
// resolved to (nil when there was none), the values to offer the
// BindWriter, and what it refused. A credential is NEVER among the headers
// it returns: either it became a variable or the header was dropped and
// itemised.
func absorbHeaderSecrets(headers []apicoll.Header, namer *varNamer) ([]apicoll.Header, *apicoll.Auth, []secretOffer, []Unsupported) {
	var (
		kept   = headers[:0:0]
		auth   *apicoll.Auth
		offers []secretOffer
		unsup  []Unsupported
	)
	for _, h := range headers {
		if strings.EqualFold(h.Name, "Authorization") {
			a, offer, bad := authFromHeader(h.Value, namer)
			if bad != "" {
				unsup = append(unsup, Unsupported{
					What: "Authorization: " + bad,
					Why:  "only Bearer and Basic map onto the model's auth, and the credential was NOT written into the request",
				})
				continue
			}
			auth = &a
			if offer != nil {
				offers = append(offers, *offer)
			}
			continue
		}
		if headerValueIsSecret(h.Name, h.Value) {
			v := namer.take(h.Name)
			offers = append(offers, secretOffer{Variable: v, Value: []byte(h.Value)})
			h.Value = "{{" + v + "}}"
		}
		kept = append(kept, h)
	}
	return kept, auth, offers, unsup
}
