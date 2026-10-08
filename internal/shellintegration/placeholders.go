package shellintegration

// Placeholder expansion for an agent launch line (nocx-bag8j; the 2026-08-15
// spec's §5, "launch configuration is data, per agent").
//
// # What this owns, and what it does not
//
// The agent record (internal/agentrecord) carries the line: the command, the
// arguments, the resume shapes — any of which may name a placeholder. This
// package owns turning that line into the argv a launch runs, which is two
// operations in one deliberate order:
//
//  1. SPLIT the free-form string into argv, honouring quotes — a value the
//     person quoted is one argument, whatever spaces it holds.
//  2. EXPAND each token against the spawn's values, case-insensitively,
//     INSIDE the token — the token is the unit of substitution, never the
//     argument, so a placeholder in a larger argument still lands.
//
// The order IS the correctness. Expanded first, a branch name with a space in
// it becomes two arguments and the failure appears far from its cause; split
// first, an expanded value can never cross an argument boundary, because
// nothing after the split re-reads the bytes as shell.
//
// # An unknown placeholder passes THROUGH, unchanged, on purpose
//
// termic's launcher behaves this way and the reason is worth copying with the
// behaviour: a placeholder nobody recognised is either a typo, in which case
// the agent is about to say so when the argument does not parse — or a
// placeholder from a NEWER record this build does not know yet, in which case
// the argument still reaches the process roughly as its author wrote it.
// Expanding an unknown to the empty string does neither: it silently rewrites
// the line, and a launch that lies about its own arguments is the one defect
// this bead exists to keep out. A placeholder with no value in the set is the
// same fact, and takes the same road.
//
// # What the values are
//
// They are the facts of the SPAWN, composed by whoever launches: the
// workspace the pane lands in, the checkout it made, the conversation id it
// mints. This file does not derive any of them — a second derivation of a
// spawn fact is the second owner AD-8 refuses. The set of NAMES is closed
// (§5's list); the set of VALUES is whatever the caller holds, and a name
// absent from it is simply never expanded.

import (
	"errors"
	"strings"
)

// placeholderOpen and placeholderClose delimit a token. The doubled braces a
// template habit produces do not match (the opener must be immediately
// followed by a name character), so they pass through like any other text.
const (
	placeholderOpen  = "{"
	placeholderClose = "}"
)

// placeholderName reports whether c can be part of a placeholder's name. A
// placeholder is letters and digits and nothing else: `{workspace-slug}` is
// not a spelling of WORKSPACE_SLUG, it is a token nobody knows, and naming
// the rule this tightly is what keeps the pass-through honest — anything the
// rule does not accept is, by construction, something expansion never
// touches.
func placeholderName(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// expandToken substitutes every {NAME} in one argv element whose NAME is in
// values, case-insensitively. Tokens with no value — unknown names included —
// survive untouched, for the reason the file header states.
func expandToken(token string, values map[string]string) string {
	if !strings.Contains(token, placeholderOpen) {
		return token
	}
	var b strings.Builder
	for i := 0; i < len(token); {
		c := token[i]
		if c != placeholderOpen[0] || i+1 >= len(token) || !placeholderName(token[i+1]) {
			b.WriteByte(c)
			i++
			continue
		}
		end := i + 1
		for end < len(token) && placeholderName(token[end]) {
			end++
		}
		if end >= len(token) || token[end] != placeholderClose[0] {
			// No closing brace: not a placeholder, and rewriting the text a
			// person wrote over a missing brace would be the silent edit the
			// pass-through exists to refuse.
			b.WriteByte(c)
			i++
			continue
		}
		name := token[i+1 : end]
		if value, ok := lookupName(values, name); ok {
			b.WriteString(value)
		} else {
			b.WriteString(token[i : end+1])
		}
		i = end + 1
	}
	return b.String()
}

// lookupName answers values[name] case-insensitively. The set is small — §5
// names seven — so the scan is not worth a second normalised map, and the
// first match in map order is unique because the names differ by more than
// case.
func lookupName(values map[string]string, name string) (string, bool) {
	for k, v := range values {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return "", false
}

// ExpandAgentArgv expands every element of an argv against the spawn's
// values. It takes argv and not a string ON PURPOSE, and that is the whole
// point of the bead: the split has already happened, so no value this
// function substitutes can cross an argument boundary. The composition the
// caller wants is SplitAgentCommand, then this, then QuoteAgentArgv when the
// argv has to become a line again.
func ExpandAgentArgv(argv []string, values map[string]string) []string {
	if len(values) == 0 {
		return argv
	}
	out := make([]string, len(argv))
	for i, token := range argv {
		out[i] = expandToken(token, values)
	}
	return out
}

// SplitAgentCommand splits a free-form launch line into argv, honouring
// quotes the way the shell that will not see this line any more would have:
// double quotes group with $ and backticks still live inside them, single
// quotes group absolutely, a backslash escapes the next byte outside single
// quotes.
//
// It REFUSES an unterminated quote rather than guessing where it ended: the
// line goes to a process as argv after this, and a launch nocx had to
// imagine the end of is a launch nobody asked for.
func SplitAgentCommand(line string) ([]string, error) {
	var (
		argv    []string
		current strings.Builder
		quote   byte
		joined  bool
	)
	flush := func() {
		if joined {
			argv = append(argv, current.String())
			current.Reset()
			joined = false
		}
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			} else {
				current.WriteByte(c)
			}
		case quote == '"':
			if c == '"' {
				quote = 0
			} else {
				current.WriteByte(c)
			}
		case c == '\\' && quote != '\'':
			if i+1 >= len(line) {
				return nil, errors.New("the line ends in a lone backslash, and there is no honest argv for it")
			}
			i++
			current.WriteByte(line[i])
			joined = true
		case c == '\'' || c == '"':
			quote = c
			joined = true
		case c == ' ' || c == '\t':
			flush()
		default:
			current.WriteByte(c)
			joined = true
		}
	}
	if quote != 0 {
		return nil, errors.New("the line ends inside a quote, and a launch nocx had to imagine the end of is not a launch to run")
	}
	flush()
	return argv, nil
}

// QuoteAgentArgv renders argv back into one line whose re-split is the argv
// it was built from — the form a pane receives, where a shell tokenises
// before exec. An element is quoted exactly when the split would otherwise
// read it as more than one word or as syntax; anything else travels bare, so
// an ordinary line looks like the line the person would have typed.
func QuoteAgentArgv(argv []string) string {
	var b strings.Builder
	for i, token := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		if token == "" {
			b.WriteString(`''`)
			continue
		}
		if needsQuoting(token) {
			b.WriteByte('\'')
			for _, c := range []byte(token) {
				if c == '\'' {
					b.WriteString(`'\''`)
				} else {
					b.WriteByte(c)
				}
			}
			b.WriteByte('\'')
		} else {
			b.WriteString(token)
		}
	}
	return b.String()
}

// needsQuoting reports whether the POSIX tokenizer would split token into
// more than one word (or read part of it as syntax) if it travelled bare.
// Only bytes that make the split differ need the quotes; `$` and backtick
// stay live inside double quotes, so single quotes are the honest wrapper for
// anything carrying them too.
func needsQuoting(token string) bool {
	if token == "" {
		return true
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		switch {
		case c == ' ' || c == '\t':
			return true
		case c == '\'' || c == '"' || c == '\\' || c == '$' || c == '`':
			return true
		case c == ';' || c == '&' || c == '|' || c == '(' || c == ')' ||
			c == '<' || c == '>' || c == '*' || c == '?' || c == '[' || c == '#' || c == '~':
			return true
		}
	}
	return false
}
