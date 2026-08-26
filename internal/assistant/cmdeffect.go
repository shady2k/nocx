package assistant

import (
	"strings"

	"github.com/shady2k/nocx/internal/content"
)

// CommandEffect derives the effect of one run command from its text and the
// tool's declared worst-case effect. It is a PURE function of those facts: no
// registry lookup, settings read, shell invocation, or runtime state.
//
// The parser deliberately does not pretend to be the person's shell. An alias
// or shell function can make `ls` mean something else in their rc files, which
// nocx does not read. A lowered call therefore becomes `observe`, whose
// default policy is still "Ask every time"; a mistaken alias classification
// loses only the blanket grant, not the chance to ask.
//
// The command is lowered only when every subcommand is in readPrograms and no
// disqualifier occurs anywhere in the whole command. Otherwise the declared
// effect is retained. This is the existing content.Effect lattice's join:
// each subcommand is either observe or the declared effect, and observe is the
// least member, so no second local ordering can disagree with content.Effect.
func CommandEffect(command string, declared content.Effect) content.Effect {
	subcommands, disqualified, ok := splitCommand(command)
	if !ok || disqualified {
		return declared
	}

	for _, subcommand := range subcommands {
		words, ok := commandWords(subcommand)
		if !ok || len(words) == 0 || disqualifyingWords(words) {
			return declared
		}
		rule, ok := readPrograms[words[0]]
		if !ok || (rule.disqualifies != nil && rule.disqualifies(words[1:])) {
			return declared
		}
	}

	return content.EffectObserve
}

type readProgramRule struct {
	disqualifies func([]string) bool
}

// readPrograms is the one closed allowlist. Exact token lookup enforces word
// boundaries: `ls` is present, while `lsof` is a different token and is not.
//
// Every entry is a command that cannot write in any invocation, except for
// sort and uniq. They remain here because their ordinary read forms are
// useful, and their write-capable arguments are rejected by the per-program
// rules kept beside this table. The other audited machine commands are
// intentionally absent: hostname, date, mount and ip each have invocations
// that change machine state rather than merely reading it.
var readPrograms = map[string]readProgramRule{
	"cat":    {},
	"cut":    {},
	"df":     {},
	"du":     {},
	"file":   {},
	"free":   {},
	"grep":   {},
	"head":   {},
	"id":     {},
	"ls":     {},
	"ps":     {},
	"pwd":    {},
	"rg":     {},
	"sort":   {disqualifies: sortWrites},
	"stat":   {},
	"tail":   {},
	"uname":  {},
	"uniq":   {disqualifies: uniqWrites},
	"uptime": {},
	"wc":     {},
	"whoami": {},
}

func sortWrites(args []string) bool {
	for _, arg := range args {
		if arg == "-o" || strings.HasPrefix(arg, "-o") ||
			arg == "--output" || strings.HasPrefix(arg, "--output=") {
			return true
		}
	}
	return false
}

func uniqWrites(args []string) bool {
	operands := 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return operands+len(args)-i-1 >= 2
		}
		if arg == "-f" || arg == "-s" || arg == "-w" {
			if i+1 < len(args) {
				i++
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		operands++
	}
	return operands >= 2
}

func splitCommand(command string) (subcommands []string, disqualified, ok bool) {
	var current strings.Builder
	var quote byte
	for i := 0; i < len(command); i++ {
		c := command[i]

		if quote == '\'' {
			current.WriteByte(c)
			if c == '\'' {
				quote = 0
			}
			continue
		}
		if c == '\\' {
			current.WriteByte(c)
			if i+1 >= len(command) {
				return nil, false, false
			}
			i++
			current.WriteByte(command[i])
			continue
		}
		if quote == '"' {
			current.WriteByte(c)
			if c == '"' {
				quote = 0
			}
			if c == '$' && i+1 < len(command) && command[i+1] == '(' {
				disqualified = true
			}
			if c == '`' {
				disqualified = true
			}
			continue
		}

		switch c {
		case '\'', '"':
			quote = c
			current.WriteByte(c)
		case '$':
			current.WriteByte(c)
			if i+1 < len(command) && command[i+1] == '(' {
				disqualified = true
			}
		case '`':
			current.WriteByte(c)
			disqualified = true
		case '>':
			current.WriteByte(c)
			disqualified = true
			if i+1 < len(command) && command[i+1] == '>' {
				i++
				current.WriteByte(command[i])
			}
		case '<':
			current.WriteByte(c)
			if i+1 < len(command) && command[i+1] == '(' {
				disqualified = true
			}
		case '\n':
			if !appendSubcommand(&subcommands, &current) {
				return nil, false, false
			}
		case '&':
			if i+1 < len(command) && command[i+1] == '&' {
				if !appendSubcommand(&subcommands, &current) {
					return nil, false, false
				}
				i++
				continue
			}
			disqualified = true
			if !appendSubcommand(&subcommands, &current) {
				return nil, false, false
			}
		case '|':
			if !appendSubcommand(&subcommands, &current) {
				return nil, false, false
			}
			if i+1 < len(command) && (command[i+1] == '|' || command[i+1] == '&') {
				i++
			}
		case ';':
			if !appendSubcommand(&subcommands, &current) {
				return nil, false, false
			}
		default:
			current.WriteByte(c)
		}
	}

	if quote != 0 || !appendSubcommand(&subcommands, &current) || len(subcommands) == 0 {
		return nil, false, false
	}
	return subcommands, disqualified, true
}

func appendSubcommand(subcommands *[]string, current *strings.Builder) bool {
	subcommand := strings.TrimSpace(current.String())
	current.Reset()
	if subcommand == "" {
		return false
	}
	*subcommands = append(*subcommands, subcommand)
	return true
}

func commandWords(command string) ([]string, bool) {
	var words []string
	var word strings.Builder
	var quote byte
	wordStarted := false

	flush := func() {
		if wordStarted {
			words = append(words, word.String())
			word.Reset()
			wordStarted = false
		}
	}

	for i := 0; i < len(command); i++ {
		c := command[i]
		if quote != 0 {
			if quote == '\'' {
				if c == '\'' {
					quote = 0
				} else {
					word.WriteByte(c)
				}
				continue
			}
			if c == '"' {
				quote = 0
				continue
			}
			if c == '\\' {
				if i+1 >= len(command) {
					return nil, false
				}
				i++
				word.WriteByte(command[i])
				continue
			}
			word.WriteByte(c)
			continue
		}

		switch c {
		case '\'', '"':
			quote = c
			wordStarted = true
		case '\\':
			if i+1 >= len(command) {
				return nil, false
			}
			i++
			word.WriteByte(command[i])
			wordStarted = true
		case ' ', '\t', '\r':
			flush()
		case '#':
			if !wordStarted {
				return words, true
			}
			word.WriteByte(c)
		default:
			word.WriteByte(c)
			wordStarted = true
		}
	}

	if quote != 0 {
		return nil, false
	}
	flush()
	return words, true
}

func disqualifyingWords(words []string) bool {
	if len(words) == 0 {
		return true
	}
	program := words[0]
	if strings.HasPrefix(program, "$") {
		return true
	}
	if program == "sudo" || program == "env" || program == "xargs" ||
		program == "watch" || program == "setsid" || program == "ionice" ||
		program == "flock" || program == "nohup" || program == "timeout" ||
		program == "tee" {
		return true
	}
	if (program == "sh" || program == "bash") && containsWord(words[1:], "-c") {
		return true
	}
	if program == "find" && (containsWord(words[1:], "-exec") || containsWord(words[1:], "-delete")) {
		return true
	}
	if program == "git" && gitChangesPager(words[1:]) {
		return true
	}
	return false
}

func containsWord(words []string, want string) bool {
	for _, word := range words {
		if word == want {
			return true
		}
	}
	return false
}

func gitChangesPager(words []string) bool {
	for i, word := range words {
		if word == "-c" && i+1 < len(words) && strings.HasPrefix(words[i+1], "core.pager") {
			return true
		}
		if strings.HasPrefix(word, "-ccore.pager") || strings.HasPrefix(word, "--config core.pager") {
			return true
		}
	}
	return false
}
