package apiimport

import (
	"strings"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/secrets"
)

// dropSecretReferences removes imported vault references while preserving
// surrounding text. Importing one is a visible loss, so callers itemise each
// exact reference through Unsupported.
func dropSecretReferences(value string) (string, []string) {
	var (
		out  strings.Builder
		refs []string
		pos  int
	)
	for pos < len(value) {
		rel := strings.Index(value[pos:], "{{secret:")
		if rel < 0 {
			out.WriteString(value[pos:])
			break
		}
		start := pos + rel
		endRel := strings.Index(value[start+len("{{secret:"):], "}}")
		if endRel < 0 {
			out.WriteString(value[pos:])
			break
		}
		end := start + len("{{secret:") + endRel + 2
		out.WriteString(value[pos:start])
		refs = append(refs, value[start:end])
		pos = end
	}
	if pos == len(value) && len(value) == 0 {
		return value, refs
	}
	return out.String(), refs
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

func absorbHeaderSecrets(headers []apicoll.Header, namer *varNamer) ([]apicoll.Header, *apicoll.Auth, []Unsupported) {
	var (
		kept  = headers[:0:0]
		auth  *apicoll.Auth
		unsup []Unsupported
	)
	for _, h := range headers {
		if strings.EqualFold(h.Name, "Authorization") {
			a, bad := authFromHeader(h.Value, namer)
			if bad != "" {
				unsup = append(unsup, Unsupported{
					What: "Authorization",
					Why:  "the imported credential was not written into the request; supply it after import",
				})
				continue
			}
			if a.Kind != apicoll.AuthNone {
				auth = &a
			}
			continue
		}
		if headerValueIsSecret(h.Name, h.Value) {
			unsup = append(unsup, Unsupported{
				What: h.Name,
				Why:  "the imported credential was not written into the request; supply it after import",
			})
			continue
		}
		kept = append(kept, h)
	}
	return kept, auth, unsup
}
