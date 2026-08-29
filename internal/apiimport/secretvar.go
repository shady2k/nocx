package apiimport

import (
	"strings"
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

// itoa is the small-number formatter names.go uses for its de-duplicating
// suffix. It stays here, where the last of the naming helpers lives.
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
