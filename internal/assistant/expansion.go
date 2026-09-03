package assistant

// Variables are expanded FOR THE QUESTION ONLY (nocx-4h0m7.5). The verbatim
// command string is what runs — ADR-0045/nocx-y47mi settled that a parsed
// representation may sit BESIDE the verbatim string, never instead of it —
// so nothing here ever rewrites a command. What it does is decide which
// parts of a command a live shell may be ASKED about, so the person deciding
// sees what `$HOME` is before they approve `rm -rf $HOME/x` rather than
// after.
//
// The incident this exists for: a script referred to `$HOME/xxx`, something
// upstream had been rewriting `$HOME`, the rewrite silently stopped, and
// files in the real home were deleted. Nothing that reads command TEXT could
// have caught it — the text was correct throughout.
//
// TWO RULES BOUND IT, AND BOTH ARE STRUCTURAL.
//
// 1. WE INSPECT, WE DO NOT REWRITE. Substituting the expanded command would
//    close the window completely and was considered and refused: a rewritten
//    command can behave differently through our own fault — re-quoting a
//    value that contains quotes or newlines, or a command that meant to be
//    re-evaluated later. The expansion is a FACT shown beside the command,
//    with its own three-state honesty (expanded / unsafe / not asked).
//
// 2. ONLY SAFE EXPANSIONS MAY BE ASKED FOR, and the distinction is
//    SYNTACTIC and therefore decidable:
//      safe, pure reads   $VAR ${VAR} ${VAR:-x} ${VAR#p} ${VAR/a/b} ~ ~user,
//                         brace expansion, arithmetic with no assignment
//      never asked for    $(cmd) and `cmd` (execute), <(cmd) >(cmd)
//                         (execute), ${VAR:=x} (ASSIGNS), ${VAR:?msg}
//                         (EXITS the shell), $((x++)) $((x=5)) (assign)
//    Globs are safe to expand but READ DIRECTORIES and the result can be
//    enormous, so the answer is a count with the list available, never 143
//    paths inline (see ExpansionValue.Count).
//
// WHY THIS IS NOT A SECOND TOKENIZER, and why it could not reuse the first.
// `cmdeffect.go` owns the command parse and keeps it: effects, resources and
// invocation rules are all derived there, and nothing here touches them.
// What this file owns is a different question — WHERE in the verbatim string
// each expansion sits and whether reading it has an effect — and that
// question needs byte spans into the ORIGINAL command. `commandWordFacts`
// cannot supply them: it consumes the subcommands `splitCommand` REBUILDS
// (quotes dropped, separators removed, whitespace trimmed), so its offsets
// do not address the string the person is shown and the bytes it discards
// are exactly the ones a verbatim display must keep. The two are pinned
// together instead by TestExpansionsIn_AgreesWithTheParserOnSubstitution:
// every construct `splitCommand` disqualifies a command for is a construct
// this file refuses to ask about, so the classifications cannot drift.

import (
	"context"
	"errors"
	"strings"
)

// ExpansionKind names the shell construct an expansion is. It is the wire's
// vocabulary too: the surface words each kind differently.
type ExpansionKind string

const (
	// ExpansionParameter is $VAR, ${VAR} and every pure-read modifier.
	ExpansionParameter ExpansionKind = "parameter"
	// ExpansionTilde is ~ or ~user at the start of a word.
	ExpansionTilde ExpansionKind = "tilde"
	// ExpansionGlob is a word carrying an unquoted *, ? or [. It is safe to
	// expand and reads directories, so its answer is a count.
	ExpansionGlob ExpansionKind = "glob"
	// ExpansionBrace is a word carrying an unquoted {a,b} alternation.
	ExpansionBrace ExpansionKind = "brace"
	// ExpansionArithmetic is $((…)). Safe only without an assignment.
	ExpansionArithmetic ExpansionKind = "arithmetic"
	// ExpansionCommand is $(…) or `…`: it RUNS something. Never asked for.
	ExpansionCommand ExpansionKind = "command-substitution"
	// ExpansionProcess is <(…) or >(…): it RUNS something. Never asked for.
	ExpansionProcess ExpansionKind = "process-substitution"
)

// Expansion is one expansion site in the verbatim command, addressed by the
// byte span it occupies there. Askable says whether a live shell may be
// asked for its value; Reason is why not, in the words the person reads.
//
// The unit is deliberately mixed and the mixture is the shell's own: a
// glob, a brace alternation and a tilde are WORD operations, so their span
// is the whole word, while a parameter expansion is a piece of a word and
// spans only itself. A word carrying anything unsafe is reported WHOLE and
// unaskable — the safe half of such a word cannot be shown expanded while
// the rest is left as written without misdescribing what will run.
type Expansion struct {
	// Text is the verbatim source text of the span — what the query names
	// and what the display substitutes for.
	Text string `json:"text"`
	// Name is the parameter's name when the expansion has one ("HOME" for
	// $HOME, ${HOME:-x} and ${#HOME}); empty otherwise. It is what a
	// refusal sentence names.
	Name string        `json:"name,omitempty"`
	Kind ExpansionKind `json:"kind"`
	// Askable: reading this cannot change anything, so a live shell may be
	// asked for it.
	Askable bool `json:"askable"`
	// Reason is why an unaskable expansion is left exactly as written.
	Reason string `json:"reason,omitempty"`

	start int
	end   int
}

// Assignment is a `NAME=value` prefix the command applies to ONE command of
// its own — `HOME=/tmp rm -rf $HOME/x`. It is the PARSE's business and never
// the shell's: the live shell knows nothing about it, so a person shown the
// shell's `$HOME` beside this command would be shown the wrong fact. The
// assignment is reported, and every expansion of an assigned name becomes
// unaskable.
type Assignment struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ExpansionReport is what the verbatim command says about itself, before any
// shell is consulted. It is a pure function of the text.
type ExpansionReport struct {
	Expansions  []Expansion  `json:"expansions,omitempty"`
	Assignments []Assignment `json:"assignments,omitempty"`
	// Programs are the program word of each subcommand, when that word is a
	// plain name. They are what the alias/function question is asked about:
	// `cmdeffect.go`'s own header records that "an alias or shell function
	// can make `ls` mean something else in their rc files, which nocx does
	// not read", and a live shell can close part of that hole.
	Programs []string `json:"programs,omitempty"`
}

// volatileParameters are parameters whose value changes on every read. They
// are excluded from the query for a reason that is not safety: asking for
// one would make the re-read comparison before submission refuse every run
// that mentioned it, and a detector that fires on nothing is worse than no
// detector. Their span is still reported, labelled.
var volatileParameters = map[string]bool{
	"RANDOM": true, "SRANDOM": true, "SECONDS": true, "LINENO": true,
	"EPOCHREALTIME": true, "EPOCHSECONDS": true, "BASHPID": true,
	"?": true, "!": true, "_": true, "-": true,
}

// ── the scan ──────────────────────────────────────────────────────────────

type construct struct {
	kind    ExpansionKind
	name    string
	start   int
	end     int
	askable bool
	reason  string
}

// expansionsIn classifies every expansion site in one verbatim command. It
// executes nothing and reads nothing outside the string.
func expansionsIn(command string) ExpansionReport {
	var report ExpansionReport
	assigned := map[string]bool{}
	atCommandStart := true
	i := 0
	for i < len(command) {
		c := command[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			i++
			continue
		case c == '\n' || c == ';':
			atCommandStart = true
			i++
			continue
		case c == '|':
			atCommandStart = true
			i++
			continue
		case c == '&':
			// `>&1` and `&>` are redirection syntax, not a separator —
			// the same distinction shellFeatureReason draws, for the same
			// reason: reading them as a separator would make the next
			// token look like a program word.
			if (i > 0 && (command[i-1] == '>' || command[i-1] == '<')) ||
				(i+1 < len(command) && command[i+1] == '>') {
				i++
				continue
			}
			atCommandStart = true
			i++
			continue
		case (c == '<' || c == '>') && !(i+1 < len(command) && command[i+1] == '('):
			// A redirection operator, not a word. Its target is the next
			// word and is scanned as one.
			i++
			continue
		}
		word, cons, next := scanWord(command, i)
		i = next
		if word == "" {
			continue
		}
		if atCommandStart {
			if name, value, ok := assignmentPrefix(word); ok {
				report.Assignments = append(report.Assignments, Assignment{Name: name, Value: value})
				assigned[name] = true
			} else {
				atCommandStart = false
				if len(cons) == 0 && plainProgramWord(word) {
					report.Programs = appendUnique(report.Programs, word)
				}
			}
		}
		report.Expansions = append(report.Expansions, expansionsForWord(word, cons, i-len(word))...)
	}
	// The assignment sweep runs last so a name assigned anywhere in the
	// command disqualifies every expansion of it, not only the ones after
	// the assignment. Bash expands a command's words BEFORE applying that
	// command's own assignment prefix, so the shell's value is arguably the
	// one that will be read — but "arguably" is not a fact to put in front
	// of somebody about to delete a directory, and the assignment is stated
	// on the surface either way.
	for idx := range report.Expansions {
		e := &report.Expansions[idx]
		if !e.Askable || e.Name == "" || !assigned[e.Name] {
			continue
		}
		e.Askable = false
		e.Reason = "the command sets " + e.Name + " itself for this command, so the shell's value is not the one it will be read with"
	}
	return report
}

// expansionsForWord turns one word's constructs into the expansions the
// query and the surface see. wordStart is the word's byte offset in command.
func expansionsForWord(word string, cons []construct, wordStart int) []Expansion {
	wordEnd := wordStart + len(word)
	for _, con := range cons {
		if con.askable {
			continue
		}
		// One unsafe construct makes the whole word unsafe: the word is
		// expanded as a unit and half a substitution is a lie.
		return []Expansion{{
			Text: word, Name: con.name, Kind: con.kind,
			Askable: false, Reason: con.reason,
			start: wordStart, end: wordEnd,
		}}
	}
	if kind, ok := wordLevelKind(word); ok {
		return []Expansion{{
			Text: word, Kind: kind, Askable: true,
			start: wordStart, end: wordEnd,
		}}
	}
	out := make([]Expansion, 0, len(cons))
	for _, con := range cons {
		out = append(out, Expansion{
			Text: word[con.start-wordStart : con.end-wordStart], Name: con.name, Kind: con.kind,
			Askable: true, start: con.start, end: con.end,
		})
	}
	return out
}

// wordLevelKind reports whether the word is expanded as a WHOLE by the shell
// — a tilde prefix, a glob pattern or a brace alternation — in which case
// the query names the word rather than the constructs inside it, because
// that is the unit the shell answers in.
func wordLevelKind(word string) (ExpansionKind, bool) {
	var quote byte
	tilde := len(word) > 0 && word[0] == '~'
	glob, brace, comma := false, false, false
	depth := 0
	for i := 0; i < len(word); i++ {
		c := word[i]
		if quote == '\'' {
			if c == '\'' {
				quote = 0
			}
			continue
		}
		if c == '\\' {
			i++
			continue
		}
		if quote == '"' {
			if c == '"' {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '$', '`':
			// Skip the construct: its own metacharacters are not the
			// word's. scanWord already classified it.
			i = skipConstruct(word, i) - 1
		case '*', '?', '[':
			glob = true
		case '{':
			depth++
		case ',':
			if depth > 0 {
				comma = true
			}
		case '}':
			if depth > 0 {
				depth--
				if comma {
					brace = true
				}
			}
		}
	}
	switch {
	case tilde:
		return ExpansionTilde, true
	case glob:
		return ExpansionGlob, true
	case brace:
		return ExpansionBrace, true
	}
	return "", false
}

// scanWord consumes one word starting at i and reports the constructs inside
// it. It returns the word's verbatim text and the index just past it.
func scanWord(command string, i int) (string, []construct, int) {
	start := i
	var cons []construct
	var quote byte
	for i < len(command) {
		c := command[i]
		if quote == '\'' {
			if c == '\'' {
				quote = 0
			}
			i++
			continue
		}
		if c == '\\' {
			// A trailing backslash escapes the end of the string. Clamp
			// rather than run past it: an unterminated escape must not
			// index out of the word it is in.
			if i+2 > len(command) {
				return command[start:], cons, len(command)
			}
			i += 2
			continue
		}
		if quote == '"' {
			switch {
			case c == '"':
				quote = 0
				i++
			case c == '`' || c == '$':
				con, next := scanConstruct(command, i)
				cons = append(cons, con)
				i = next
			default:
				i++
			}
			continue
		}
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			return command[start:i], cons, i
		case c == ';' || c == '|' || c == '&':
			return command[start:i], cons, i
		case c == '\'' || c == '"':
			quote = c
			i++
		case c == '$' || c == '`':
			con, next := scanConstruct(command, i)
			cons = append(cons, con)
			i = next
		case (c == '<' || c == '>') && i+1 < len(command) && command[i+1] == '(':
			con, next := scanConstruct(command, i)
			cons = append(cons, con)
			i = next
		case c == '<' || c == '>':
			return command[start:i], cons, i
		case c == '~' && i == start:
			i++
		default:
			i++
		}
	}
	return command[start:i], cons, i
}

// scanConstruct consumes one expansion construct beginning at i and
// classifies it. i addresses `$`, a backtick, or the `<`/`>` of a process
// substitution.
func scanConstruct(command string, i int) (construct, int) {
	switch command[i] {
	case '`':
		end := i + 1
		for end < len(command) && command[end] != '`' {
			if command[end] == '\\' {
				end++
			}
			end++
		}
		if end < len(command) {
			end++
		}
		return construct{
			kind: ExpansionCommand, start: i, end: end,
			reason: "it runs a command to produce its value, and nocx never runs a command to build a question",
		}, end
	case '<', '>':
		end := matchParen(command, i+1)
		return construct{
			kind: ExpansionProcess, start: i, end: end,
			reason: "it runs a command and substitutes the path of its output, and nocx never runs a command to build a question",
		}, end
	}
	// command[i] == '$'
	if i+1 >= len(command) {
		return construct{
			kind: ExpansionParameter, start: i, end: i + 1, askable: false,
			reason: "a bare $ expands to nothing",
		}, i + 1
	}
	switch command[i+1] {
	case '(':
		if i+2 < len(command) && command[i+2] == '(' {
			end := matchArithmetic(command, i+3)
			body := command[i+3 : maxInt(i+3, end-2)]
			if arithmeticAssigns(body) {
				return construct{
					kind: ExpansionArithmetic, start: i, end: end,
					reason: "it assigns a variable as a side effect of being read",
				}, end
			}
			return construct{kind: ExpansionArithmetic, start: i, end: end, askable: true}, end
		}
		end := matchParen(command, i+1)
		return construct{
			kind: ExpansionCommand, start: i, end: end,
			reason: "it runs a command to produce its value, and nocx never runs a command to build a question",
		}, end
	case '{':
		end := matchBrace(command, i+2)
		body := command[i+2 : maxInt(i+2, end-1)]
		con := construct{kind: ExpansionParameter, start: i, end: end, name: bracedName(body)}
		switch {
		case strings.Contains(body, "$(") || strings.Contains(body, "`"):
			con.reason = "it runs a command to produce its value, and nocx never runs a command to build a question"
			con.kind = ExpansionCommand
		case bracedAssigns(body):
			con.reason = "it assigns " + con.name + " as a side effect of being read"
		case bracedExits(body):
			con.reason = "it exits the shell when " + con.name + " is unset, and a question must never end a session"
		case volatileParameters[con.name]:
			con.reason = "its value changes on every read, so it cannot be shown as a fact"
		default:
			con.askable = true
		}
		return con, end
	}
	end := i + 1
	if isSpecialParameterByte(command[end]) {
		end++
	} else {
		for end < len(command) && isNameByte(command[end]) {
			end++
		}
	}
	name := command[i+1 : end]
	con := construct{kind: ExpansionParameter, start: i, end: end, name: name, askable: true}
	if name == "" {
		con.askable = false
		con.reason = "a bare $ expands to nothing"
	} else if volatileParameters[name] {
		con.askable = false
		con.reason = "its value changes on every read, so it cannot be shown as a fact"
	}
	return con, end
}

// skipConstruct is wordLevelKind's view of the same constructs: it needs
// only their extent, never their classification.
func skipConstruct(word string, i int) int {
	if word[i] == '`' {
		end := i + 1
		for end < len(word) && word[end] != '`' {
			end++
		}
		if end < len(word) {
			end++
		}
		return end
	}
	if i+1 >= len(word) {
		return i + 1
	}
	switch word[i+1] {
	case '(':
		if i+2 < len(word) && word[i+2] == '(' {
			return matchArithmetic(word, i+3)
		}
		return matchParen(word, i+1)
	case '{':
		return matchBrace(word, i+2)
	}
	end := i + 1
	if isSpecialParameterByte(word[end]) {
		return end + 1
	}
	for end < len(word) && isNameByte(word[end]) {
		end++
	}
	return end
}

// matchParen returns the index just past the `)` closing the `(` at open.
// An unterminated construct runs to the end of the string, which keeps the
// whole remainder inside an UNSAFE span — the safe direction.
func matchParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(s)
}

// matchArithmetic returns the index just past the `))` closing an
// arithmetic expansion whose body starts at body.
func matchArithmetic(s string, body int) int {
	depth := 0
	for i := body; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				if i+1 < len(s) && s[i+1] == ')' {
					return i + 2
				}
				return i + 1
			}
			depth--
		}
	}
	return len(s)
}

// matchBrace returns the index just past the `}` closing a `${` whose body
// starts at body.
func matchBrace(s string, body int) int {
	depth := 0
	for i := body; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '{':
			depth++
		case '}':
			if depth == 0 {
				return i + 1
			}
			depth--
		}
	}
	return len(s)
}

// bracedName is the parameter name inside a `${…}` body: the leading `#`
// (length) and `!` (indirection) sigils are not part of it.
func bracedName(body string) string {
	body = strings.TrimPrefix(strings.TrimPrefix(body, "#"), "!")
	end := 0
	if end < len(body) && isSpecialParameterByte(body[end]) {
		return body[:end+1]
	}
	for end < len(body) && isNameByte(body[end]) {
		end++
	}
	return body[:end]
}

// bracedAssigns reports `${VAR:=x}` and `${VAR=x}`, which WRITE the variable
// as a side effect of being read.
func bracedAssigns(body string) bool {
	name := bracedName(body)
	rest := body[strings.Index(body, name)+len(name):]
	return strings.HasPrefix(rest, ":=") || strings.HasPrefix(rest, "=")
}

// bracedExits reports `${VAR:?msg}` and `${VAR?msg}`, which EXIT a
// non-interactive shell when the variable is unset.
func bracedExits(body string) bool {
	name := bracedName(body)
	rest := body[strings.Index(body, name)+len(name):]
	return strings.HasPrefix(rest, ":?") || strings.HasPrefix(rest, "?")
}

// arithmeticAssigns reports an arithmetic expansion that writes: `x=5`,
// `x+=1`, `x++`, `--x`. A comparison (`==`, `!=`, `<=`, `>=`) is a read.
func arithmeticAssigns(body string) bool {
	if strings.Contains(body, "++") || strings.Contains(body, "--") {
		return true
	}
	for i := 0; i < len(body); i++ {
		if body[i] != '=' {
			continue
		}
		if i+1 < len(body) && body[i+1] == '=' {
			i++
			continue
		}
		if i > 0 && strings.IndexByte("=!<>", body[i-1]) >= 0 {
			continue
		}
		return true
	}
	return false
}

func isNameByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// isSpecialParameterByte covers the one-byte parameters a name scan would
// otherwise read as empty: $?, $$, $!, $#, $*, $@, $-, $_ and $0..$9.
func isSpecialParameterByte(c byte) bool {
	return strings.IndexByte("?$!#*@-_", c) >= 0
}

// assignmentPrefix reports a leading `NAME=value` word. Only a word with no
// expansion construct in its NAME half can be one, and a word whose value
// half carries a construct is still an assignment — the value is reported
// verbatim, never expanded.
func assignmentPrefix(word string) (string, string, bool) {
	eq := strings.IndexByte(word, '=')
	if eq <= 0 {
		return "", "", false
	}
	name := word[:eq]
	for i := 0; i < len(name); i++ {
		if !isNameByte(name[i]) {
			return "", "", false
		}
	}
	if name[0] >= '0' && name[0] <= '9' {
		return "", "", false
	}
	return name, word[eq+1:], true
}

// plainProgramWord reports a program word the alias/function question can be
// asked about: a literal name, not a path and not a pattern.
func plainProgramWord(word string) bool {
	if word == "" || (word[0] >= '0' && word[0] <= '9') {
		return false
	}
	for i := 0; i < len(word); i++ {
		c := word[i]
		if !isNameByte(c) && c != '.' && c != '-' && c != '+' && c != ':' {
			return false
		}
	}
	return true
}

func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ── the query and its answer ──────────────────────────────────────────────

// ExpansionQuery is ONE question per approval, never one per variable: every
// askable expansion in the command and every program word whose identity a
// live shell can settle, in a single round trip. Nothing in Expressions can
// execute anything — that is what expansionsIn decided.
type ExpansionQuery struct {
	Expressions []string
	Programs    []string
}

// ExpansionQueryFor builds the one query for a verbatim command.
func ExpansionQueryFor(command string) ExpansionQuery {
	report := expansionsIn(command)
	q := ExpansionQuery{Programs: report.Programs}
	for _, e := range report.Expansions {
		if !e.Askable {
			continue
		}
		q.Expressions = appendUnique(q.Expressions, e.Text)
	}
	return q
}

// ExpansionValue is one answered expression. Count and Truncated exist for
// globs: a pattern can match an enormous number of paths, and the honest
// answer is "143 paths" with the first few named, never 143 paths inline.
type ExpansionValue struct {
	Expression string `json:"expression"`
	Value      string `json:"value"`
	Count      int    `json:"count,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
}

// ProgramFactKind is what a live shell says a program word IS.
type ProgramFactKind string

const (
	ProgramAlias    ProgramFactKind = "alias"
	ProgramFunction ProgramFactKind = "function"
	ProgramBuiltin  ProgramFactKind = "builtin"
	ProgramFile     ProgramFactKind = "file"
	ProgramNotFound ProgramFactKind = "not-found"
)

// ProgramFact answers "what does `ls` actually mean in this shell". It
// closes, partly, the hole cmdeffect.go names in its own header: nocx does
// not read rc files, so an alias or a function can make a known-looking
// program mean something else.
type ProgramFact struct {
	Word   string          `json:"word"`
	Kind   ProgramFactKind `json:"kind"`
	Target string          `json:"target,omitempty"`
}

// ExpansionAnswer is what one live shell answered.
type ExpansionAnswer struct {
	Values   []ExpansionValue `json:"values,omitempty"`
	Programs []ProgramFact    `json:"programs,omitempty"`
}

// ErrExpansionUnavailable is the honest outcome where nocx's integration is
// not deployed or cannot be reached: nothing is expanded and every
// expansion is marked NOT ASKED, which the surface words differently from
// "unsafe, left as written". It is never an error that fails a run.
var ErrExpansionUnavailable = errors.New("expansion: this session's shell cannot be asked")

// ExpansionUnavailableError carries the SENTENCE a person reads for why no
// shell could be asked. It wraps ErrExpansionUnavailable so a caller can
// still test the class, and it exists because the reasons differ in a way
// that matters on the surface: a remote host with no bundle deployed, a
// native prompt that never integrated, and an integrated shell that could
// not be reached in time are three facts, not one.
type ExpansionUnavailableError struct {
	Reason string
}

func (e *ExpansionUnavailableError) Error() string { return e.Reason }

func (e *ExpansionUnavailableError) Unwrap() error { return ErrExpansionUnavailable }

// ExpansionSource asks ONE live shell ONE query. Implementations must never
// execute anything the query names — the query is already restricted to
// pure reads, and an implementation that evaluated it as a command string
// would defeat the whole classification.
//
// A nil source, or one returning ErrExpansionUnavailable, is the remote host
// where our integration is not deployed: expand nothing, mark everything
// unresolved, say so.
type ExpansionSource interface {
	Expand(ctx context.Context, sessionID string, q ExpansionQuery) (ExpansionAnswer, error)
}

// ── the facts carried to the person, and back ─────────────────────────────

// ExpansionState is the three-state honesty of one expansion on the
// approval surface. The distinction between the last two is the whole point:
// "we refuse to ask" and "we could not ask" are different facts and a
// surface that merged them would tell a person their shell had been
// consulted when it had not.
type ExpansionState string

const (
	// ExpansionExpanded: a live shell answered, and Value is what it said.
	ExpansionExpanded ExpansionState = "expanded"
	// ExpansionUnsafe: reading it would have an effect, so it is left
	// exactly as written and Reason says why.
	ExpansionUnsafe ExpansionState = "unsafe"
	// ExpansionUnasked: it is a pure read, and no shell could be asked.
	ExpansionUnasked ExpansionState = "unasked"
)

// ExpansionPart is one expansion as the person sees it.
type ExpansionPart struct {
	Text   string         `json:"text"`
	Name   string         `json:"name,omitempty"`
	Kind   ExpansionKind  `json:"kind"`
	State  ExpansionState `json:"state"`
	Value  string         `json:"value,omitempty"`
	Count  int            `json:"count,omitempty"`
	Reason string         `json:"reason,omitempty"`
}

// ExpansionFacts is what rides BESIDE the verbatim command in the approval
// question (rule 1: beside, never instead). Command is a DISPLAY form — the
// verbatim string with every answered span substituted and every other span
// left exactly as written. It is never sent to a shell and never runs.
type ExpansionFacts struct {
	// Asked is whether a live shell was consulted at all. False is the
	// remote host, the session with no integration, and the shell that did
	// not answer; Reason says which.
	Asked  bool   `json:"asked"`
	Reason string `json:"reason,omitempty"`
	// Command is the expanded display form, beside the verbatim one.
	Command     string          `json:"command"`
	Parts       []ExpansionPart `json:"parts,omitempty"`
	Assignments []Assignment    `json:"assignments,omitempty"`
	Programs    []ProgramFact   `json:"programs,omitempty"`
	// Values is the exact set of expression→value pairs the person was
	// shown. It is the carrier the re-read before submission compares
	// against, and it is not on the wire.
	Values []ExpansionValue `json:"-"`
}

// expandedDisplay substitutes every answered span into the verbatim command
// and leaves every other span exactly as written. It builds a STRING FOR A
// PERSON TO READ. It is never submitted, never re-quoted, and never handed
// to a shell.
func expandedDisplay(command string, report ExpansionReport, values map[string]string) string {
	var b strings.Builder
	at := 0
	for _, e := range report.Expansions {
		value, ok := values[e.Text]
		if !ok || e.start < at {
			continue
		}
		b.WriteString(command[at:e.start])
		b.WriteString(value)
		at = e.end
	}
	b.WriteString(command[at:])
	return b.String()
}

// ExpansionFactsFor builds the facts for one verbatim command, asking source
// once. A nil source, an unavailable source, and a source that fails are all
// the SAME product outcome — nothing was expanded and the surface says so —
// and they differ only in the reason.
//
// It never returns an error: failing to expand is not a reason to refuse a
// call. The window simply says less, honestly.
func ExpansionFactsFor(ctx context.Context, source ExpansionSource, sessionID, command string) ExpansionFacts {
	report := expansionsIn(command)
	facts := ExpansionFacts{
		Command:     command,
		Assignments: report.Assignments,
	}
	if len(report.Expansions) == 0 && len(report.Programs) == 0 {
		return facts
	}
	var answer ExpansionAnswer
	var askErr error
	q := ExpansionQueryFor(command)
	switch {
	case source == nil:
		askErr = ErrExpansionUnavailable
	case len(q.Expressions) == 0 && len(q.Programs) == 0:
		// Nothing may be asked for, so nothing is asked. This is not a
		// failure and must not be reported as one.
	default:
		answer, askErr = source.Expand(ctx, sessionID, q)
	}
	values := map[string]string{}
	if askErr == nil {
		facts.Asked = true
		facts.Values = answer.Values
		facts.Programs = answer.Programs
		for _, v := range answer.Values {
			values[v.Expression] = v.Value
		}
	} else {
		facts.Reason = expansionUnavailableReason(askErr)
	}
	facts.Command = expandedDisplay(command, report, values)
	facts.Parts = expansionParts(report, answer, askErr == nil)
	return facts
}

func expansionUnavailableReason(err error) string {
	var named *ExpansionUnavailableError
	if errors.As(err, &named) {
		return named.Reason
	}
	if errors.Is(err, ErrExpansionUnavailable) {
		return "nocx's shell integration is not live in this session, so no value could be read"
	}
	return "the shell did not answer, so no value could be read: " + err.Error()
}

func expansionParts(report ExpansionReport, answer ExpansionAnswer, asked bool) []ExpansionPart {
	byExpression := make(map[string]ExpansionValue, len(answer.Values))
	for _, v := range answer.Values {
		byExpression[v.Expression] = v
	}
	parts := make([]ExpansionPart, 0, len(report.Expansions))
	for _, e := range report.Expansions {
		part := ExpansionPart{Text: e.Text, Name: e.Name, Kind: e.Kind}
		switch {
		case !e.Askable:
			part.State, part.Reason = ExpansionUnsafe, e.Reason
		case !asked:
			part.State = ExpansionUnasked
		default:
			v, ok := byExpression[e.Text]
			if !ok {
				part.State = ExpansionUnasked
				break
			}
			part.State, part.Value, part.Count = ExpansionExpanded, v.Value, v.Count
		}
		parts = append(parts, part)
	}
	return parts
}

// ── the window between the question and the submission ────────────────────

// ExpansionChangedError names the ONE variable whose value moved between the
// question a person answered and the call about to run. It is a DETECTOR,
// not a fix: without substitution there is a window, and two things shrink
// it to almost nothing — the agent's own lane has nothing else running in
// it, so there is no second writer, and the values are read AGAIN
// immediately before submitting and compared with what the person was
// shown. A change refuses the run, loudly. That is the trade this repo makes
// everywhere else: "silently did something else" becomes "loudly refused".
type ExpansionChangedError struct {
	Expression string
}

func (e *ExpansionChangedError) Error() string {
	return e.Expression + " changed between the question you were shown and this call, so the approval no longer describes what would run"
}

// ExpansionUnverifiableError is the other half of the same fence: the values
// could not be read again, so nothing can be compared. It refuses too — an
// approval whose premise cannot be re-established is not an approval.
type ExpansionUnverifiableError struct {
	Err error
}

func (e *ExpansionUnverifiableError) Error() string {
	return "the shell could not be asked again before running, so what the person approved could not be confirmed"
}

func (e *ExpansionUnverifiableError) Unwrap() error { return e.Err }

// VerifyExpansions re-reads the values the person was shown and compares.
// shown is what the approval question carried; a nil or empty shown set is
// the case where nothing was ever expanded — there is nothing to compare and
// the call proceeds, because the person approved a window that said so.
//
// It re-asks for the EXACT expressions the person was shown rather than
// re-deriving them from the command. The command is byte-identical by
// construction — a changed argument hashes differently and never resumes
// under the old approval — so re-deriving would only introduce a second
// classification that could disagree with the first.
func VerifyExpansions(ctx context.Context, source ExpansionSource, sessionID string, shown []ExpansionValue) error {
	if len(shown) == 0 {
		return nil
	}
	if source == nil {
		return &ExpansionUnverifiableError{Err: ErrExpansionUnavailable}
	}
	q := ExpansionQuery{}
	for _, v := range shown {
		q.Expressions = appendUnique(q.Expressions, v.Expression)
	}
	answer, err := source.Expand(ctx, sessionID, q)
	if err != nil {
		return &ExpansionUnverifiableError{Err: err}
	}
	now := make(map[string]ExpansionValue, len(answer.Values))
	for _, v := range answer.Values {
		now[v.Expression] = v
	}
	for _, was := range shown {
		is, ok := now[was.Expression]
		if !ok {
			return &ExpansionChangedError{Expression: was.Expression}
		}
		if is.Value != was.Value || is.Count != was.Count {
			return &ExpansionChangedError{Expression: was.Expression}
		}
	}
	return nil
}
