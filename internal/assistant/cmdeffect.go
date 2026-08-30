package assistant

import (
	"fmt"
	"path/filepath"
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
// The class is derived from the structural resource report. A report with only
// resolved reads can lower the declared effect; writes, deletes, network
// access and unresolved parts retain the declared worst case.
// CommandInvocation is the parser result shared by effect classification and
// invocation-rule policy. The parser is deliberately owned here: policy
// consumers receive this result instead of tokenizing the command again.
//
// The invariant is `Disqualified ⇒ non-empty Unresolved`. Its CONVERSE has
// never held: `Disqualified` is false for a path-prefixed program that a bare
// name would have disqualified. What made that safe was `Unresolved`, filled
// by the `readPrograms` default at the bottom of the switch — not
// `Disqualified`. An audit that reads `Disqualified` as the guard is reading
// the wrong field.
func parseCanonicalInvocation(command string) content.Invocation {
	subcommands, disqualified, ok := splitCommand(command)
	inv := content.Invocation{Parsed: ok, Disqualified: disqualified}
	if !ok {
		return finalizeInvocation(inv, command)
	}
	inv.Commands = make([][]string, 0, len(subcommands))
	for _, subcommand := range subcommands {
		facts, wordsOK := commandWordFacts(subcommand)
		if !wordsOK || len(facts) == 0 {
			inv.Parsed = false
			inv.Commands = nil
			return finalizeInvocation(inv, command)
		}
		words := make([]string, 0, len(facts))
		for _, fact := range facts {
			words = append(words, fact.value)
		}
		inv.Commands = append(inv.Commands, words)
		if disqualifyingWords(words) {
			inv.Disqualified = true
		}
		inv.Resources = appendResourceReport(inv.Resources, subcommand, facts)
	}
	if reason := shellFeatureReason(command); strings.HasPrefix(reason, "runs in the background") {
		inv.Resources.Unresolved = append(inv.Resources.Unresolved, content.UnresolvedResource{
			Path: command, Verb: content.ResourceUnknown, Reason: reason,
		})
	}
	return finalizeInvocation(inv, command)
}

func finalizeInvocation(inv content.Invocation, command string) content.Invocation {
	if inv.Disqualified && len(inv.Resources.Unresolved) == 0 {
		inv.Resources.Unresolved = append(inv.Resources.Unresolved, content.UnresolvedResource{
			Path: command, Verb: content.ResourceUnknown,
			Reason: "the command uses a shell feature whose effect cannot be determined",
		})
	}
	return inv
}

func commandEffect(inv content.Invocation, declared content.Effect) content.Effect {
	if !inv.Parsed {
		return declared
	}
	return inv.Resources.Effect(declared)
}

type readProgramRule struct {
	disqualifies func([]string) bool
}

// readPrograms is the one closed allowlist for commands whose ordinary forms
// expose filesystem reads. Resource extraction, rather than this table, owns
// effect derivation: commands with writes or network access have their own
// resource verbs below.
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

type commandWordFact struct {
	value   string
	dynamic bool
}

func appendResourceReport(report content.ResourceReport, subcommand string, facts []commandWordFact) content.ResourceReport {
	words := make([]string, 0, len(facts))
	for _, fact := range facts {
		words = append(words, fact.value)
	}
	program := normalizedProgram(words[0])

	if reason := shellFeatureReason(subcommand); reason != "" {
		report.Unresolved = append(report.Unresolved, content.UnresolvedResource{
			Path: subcommand, Verb: content.ResourceUnknown, Reason: reason,
		})
	}
	disqualified := disqualifyingWords(words)
	if disqualified {
		report.Unresolved = append(report.Unresolved, content.UnresolvedResource{
			Path: program, Verb: content.ResourceUnknown,
			Reason: disqualifierReason(words),
		})
	}

	args := withoutRedirections(facts, &report)

	switch program {
	case "cp":
		operands, target := cpOperands(args)
		if target != nil {
			if len(operands) == 0 {
				return unresolvedCommand(report, program, "has no statically named source operand")
			}
			for _, operand := range operands {
				report = addResource(report, operand, content.ResourceRead)
			}
			return addResource(report, *target, content.ResourceWrite)
		}
		if len(operands) == 0 {
			return unresolvedCommand(report, program, "has no statically named operand")
		}
		for _, operand := range operands[:len(operands)-1] {
			report = addResource(report, operand, content.ResourceRead)
		}
		return addResource(report, operands[len(operands)-1], content.ResourceWrite)
	case "install":
		operands, target := installOperands(args)
		if target != nil {
			if len(operands) == 0 {
				return unresolvedCommand(report, program, "has no statically named source operand")
			}
			for _, operand := range operands {
				report = addResource(report, operand, content.ResourceRead)
			}
			return addResource(report, *target, content.ResourceWrite)
		}
		if len(operands) == 0 {
			return unresolvedCommand(report, program, "has no statically named operand")
		}
		for _, operand := range operands[:len(operands)-1] {
			report = addResource(report, operand, content.ResourceRead)
		}
		return addResource(report, operands[len(operands)-1], content.ResourceWrite)
	case "mv":
		operands := resourceOperands("mv", args)
		if len(operands) == 0 {
			return unresolvedCommand(report, program, "has no statically named operand")
		}
		for _, operand := range operands[:len(operands)-1] {
			report = addResource(report, operand, content.ResourceRead)
			report = addResource(report, operand, content.ResourceDelete)
		}
		return addResource(report, operands[len(operands)-1], content.ResourceWrite)
	case "rm":
		operands := resourceOperands("rm", args)
		if len(operands) == 0 {
			return unresolvedCommand(report, program, "has no statically named path")
		}
		for _, operand := range operands {
			report = addResource(report, operand, content.ResourceDelete)
		}
		return report
	case "tee":
		for _, operand := range resourceOperands("tee", args) {
			report = addResource(report, operand, content.ResourceWrite)
		}
		return report
	case "sort":
		for i := 0; i < len(args); i++ {
			if args[i].value == "-o" || args[i].value == "--output" {
				if i+1 >= len(args) {
					return unresolvedCommand(report, program, "has an output option without a path")
				}
				report = addResource(report, args[i+1], content.ResourceWrite)
				i++
				continue
			}
			if strings.HasPrefix(args[i].value, "-o") && args[i].value != "-o" {
				report = addResource(report, commandWordFact{value: strings.TrimPrefix(args[i].value, "-o"), dynamic: args[i].dynamic}, content.ResourceWrite)
				continue
			}
			if strings.HasPrefix(args[i].value, "--output=") {
				report = addResource(report, commandWordFact{value: strings.TrimPrefix(args[i].value, "--output="), dynamic: args[i].dynamic}, content.ResourceWrite)
				continue
			}
			if !strings.HasPrefix(args[i].value, "-") {
				report = addResource(report, args[i], content.ResourceRead)
			}
		}
		return report
	case "uniq":
		operands := resourceOperands("uniq", args)
		for i, operand := range operands {
			verb := content.ResourceRead
			if i == len(operands)-1 && len(operands) > 1 {
				verb = content.ResourceWrite
			}
			report = addResource(report, operand, verb)
		}
		return report
	case "source", ".":
		// Sourcing runs in the current shell and can permanently change its
		// environment; it is not subprocess execution of a file.
		operands := resourceOperands(program, args)
		if len(operands) == 0 {
			return unresolvedCommand(report, program, "has no statically named source file")
		}
		return addResource(report, operands[0], content.ResourceSource)
	case "bash", "sh":
		script, ok := shellScriptOperand(program, args)
		if !ok {
			return unresolvedCommand(report, program, "has no statically named script file")
		}
		if disqualified {
			return report
		}
		return addResource(report, script, content.ResourceExecute)
	case "curl":
		operands := resourceOperands("curl", args)
		if len(operands) == 0 {
			return unresolvedCommand(report, program, "has no statically named URL")
		}
		for _, operand := range operands {
			report = addResource(report, operand, content.ResourceNetwork)
		}
		return report
	case "ssh":
		operands := resourceOperands("ssh", args)
		if len(operands) == 0 {
			return unresolvedCommand(report, program, "has no statically named destination")
		}
		return addResource(report, operands[0], content.ResourceNetwork)
	case "kubectl":
		return addResource(report, commandWordFact{value: "kubectl cluster"}, content.ResourceNetwork)
	}

	if _, known := readPrograms[program]; !known {
		if isExecutablePath(facts[0].value) && !disqualified {
			return addResource(report, facts[0], content.ResourceExecute)
		}
		return unresolvedCommand(report, program, "is not a recognized resource access form")
	}
	for _, operand := range readOperands(program, args) {
		report = addResource(report, operand, content.ResourceRead)
	}
	return report
}

func addResource(report content.ResourceReport, fact commandWordFact, verb content.ResourceVerb) content.ResourceReport {
	if fact.dynamic {
		report.Unresolved = append(report.Unresolved, content.UnresolvedResource{
			Path: fact.value, Verb: verb,
			Reason: fmt.Sprintf("could not resolve %s without executing shell expansion", fact.value),
		})
		return report
	}
	report.Resources = append(report.Resources, content.Resource{Path: fact.value, Verb: verb})
	return report
}

func unresolvedCommand(report content.ResourceReport, program, detail string) content.ResourceReport {
	report.Unresolved = append(report.Unresolved, content.UnresolvedResource{
		Path: program, Verb: content.ResourceUnknown,
		Reason: fmt.Sprintf("could not determine resources: %s %s", program, detail),
	})
	return report
}

func appendUnresolvedRedirection(report content.ResourceReport, operator, target string) content.ResourceReport {
	report.Unresolved = append(report.Unresolved, content.UnresolvedResource{
		Path: target, Verb: redirectionVerb(operator),
		Reason: fmt.Sprintf("shell redirection %s remains unresolved even though its target was recorded", operator),
	})
	return report
}

func withoutRedirections(facts []commandWordFact, report *content.ResourceReport) []commandWordFact {
	args := make([]commandWordFact, 0, len(facts)-1)
	for i := 1; i < len(facts); i++ {
		fact := facts[i]
		if isRedirection(fact.value) {
			if i+1 >= len(facts) {
				*report = unresolvedCommand(*report, fact.value, "has no target path")
				continue
			}
			if isReadWriteRedirection(fact.value) {
				*report = addResource(*report, facts[i+1], content.ResourceRead)
				*report = addResource(*report, facts[i+1], content.ResourceWrite)
			} else {
				*report = addResource(*report, facts[i+1], redirectionVerb(fact.value))
			}
			*report = appendUnresolvedRedirection(*report, fact.value, facts[i+1].value)
			i++
			continue
		}
		args = append(args, fact)
	}
	return args
}

func isRedirection(word string) bool {
	if word == ">" || word == ">>" || word == "<" || word == "<<" || word == "<>" {
		return true
	}
	if strings.HasSuffix(word, ">>") || strings.HasSuffix(word, "<<") {
		return isFileDescriptor(word[:len(word)-2])
	}
	if strings.HasSuffix(word, "<>") {
		return isFileDescriptor(word[:len(word)-2])
	}
	if strings.HasSuffix(word, ">") || strings.HasSuffix(word, "<") {
		return isFileDescriptor(word[:len(word)-1])
	}
	return false
}

func isReadWriteRedirection(word string) bool {
	return word == "<>" || strings.HasSuffix(word, "<>") && isFileDescriptor(word[:len(word)-2])
}

func redirectionVerb(word string) content.ResourceVerb {
	if strings.HasSuffix(word, "<") {
		return content.ResourceRead
	}
	return content.ResourceWrite
}

func isFileDescriptor(word string) bool {
	return word != "" && strings.Trim(word, "0123456789") == ""
}

func isExecutablePath(program string) bool {
	return strings.HasPrefix(program, "/") ||
		strings.HasPrefix(program, "./") ||
		strings.HasPrefix(program, "../")
}

func resourceOperands(program string, args []commandWordFact) []commandWordFact {
	operands := make([]commandWordFact, 0, len(args))
	optionsEnded := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !optionsEnded && arg.value == "--" {
			optionsEnded = true
			continue
		}
		if !optionsEnded && strings.HasPrefix(arg.value, "-") {
			if optionTakesNextValue(program, arg.value) && i+1 < len(args) {
				i++
			}
			continue
		}
		operands = append(operands, arg)
	}
	return operands
}

func shellScriptOperand(program string, args []commandWordFact) (commandWordFact, bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg.value == "--" {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return commandWordFact{}, false
		}
		if !strings.HasPrefix(arg.value, "-") {
			return arg, true
		}
		if arg.value == "-c" || arg.value == "--command" ||
			(!strings.HasPrefix(arg.value, "--") && strings.Contains(arg.value[1:], "c")) {
			return commandWordFact{}, false
		}
		if optionTakesNextValue(program, arg.value) && i+1 < len(args) {
			i++
		}
	}
	return commandWordFact{}, false
}

func optionTakesNextValue(program, option string) bool {
	switch program {
	case "cut":
		return option == "-b" || option == "-c" || option == "-d" || option == "-f"
	case "du":
		return option == "-d"
	case "grep", "rg":
		return option == "-A" || option == "-B" || option == "-C" ||
			option == "-e" || option == "-f" || option == "-m"
	case "head", "tail":
		return option == "-c" || option == "-n"
	case "install":
		return option == "-g" || option == "-m" || option == "-o" ||
			option == "-t" || option == "-S"
	case "uniq":
		return option == "-f" || option == "-s" || option == "-w"
	case "ssh":
		return option == "-b" || option == "-c" || option == "-D" ||
			option == "-F" || option == "-i" || option == "-J" ||
			option == "-L" || option == "-l" || option == "-o" ||
			option == "-p" || option == "-R" || option == "-S" ||
			option == "-W"
	case "bash", "sh":
		return option == "-o" || option == "--option" || option == "--rcfile"
	case "curl":
		return option == "-A" || option == "-b" || option == "-d" ||
			option == "-e" || option == "-F" || option == "-H" ||
			option == "-o" || option == "-u" || option == "-X"
	default:
		return false
	}
}

func cpOperands(args []commandWordFact) ([]commandWordFact, *commandWordFact) {
	return targetDirectoryOperands("cp", args)
}

func installOperands(args []commandWordFact) ([]commandWordFact, *commandWordFact) {
	return targetDirectoryOperands("install", args)
}

func targetDirectoryOperands(program string, args []commandWordFact) ([]commandWordFact, *commandWordFact) {
	var operands []commandWordFact
	var target *commandWordFact
	optionsEnded := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !optionsEnded && arg.value == "--" {
			optionsEnded = true
			continue
		}
		if !optionsEnded && (arg.value == "-t" || arg.value == "--target-directory") {
			if i+1 < len(args) {
				i++
				target = &args[i]
			}
			continue
		}
		if !optionsEnded && strings.HasPrefix(arg.value, "--target-directory=") {
			value := strings.TrimPrefix(arg.value, "--target-directory=")
			target = &commandWordFact{value: value, dynamic: arg.dynamic}
			continue
		}
		if !optionsEnded && strings.HasPrefix(arg.value, "-") {
			if optionTakesNextValue(program, arg.value) && i+1 < len(args) {
				i++
			}
			continue
		}
		operands = append(operands, arg)
	}
	return operands, target
}

func readOperands(program string, args []commandWordFact) []commandWordFact {
	operands := resourceOperands(program, args)
	if program != "grep" && program != "rg" {
		return operands
	}
	if len(operands) <= 1 {
		return nil
	}
	return operands[1:]
}

func shellHasCommandString(words []string) bool {
	for _, word := range words {
		if word == "-c" || word == "--command" {
			return true
		}
		if strings.HasPrefix(word, "-") && !strings.HasPrefix(word, "--") &&
			strings.Contains(word[1:], "c") {
			return true
		}
	}
	return false
}

func disqualifierReason(words []string) string {
	program := normalizedProgram(words[0])
	switch {
	case strings.HasPrefix(program, "$"):
		return "the program name comes from a shell variable"
	case program == "sudo":
		return "sudo changes the executing identity before the nested command"
	case program == "env":
		return "env can replace the command and its environment"
	case program == "xargs":
		return "xargs constructs further commands from input"
	case program == "tee":
		return "tee writes named paths"
	case (program == "sh" || program == "bash") && shellHasCommandString(words[1:]):
		return "the shell will interpret a nested command string"
	case program == "find" && containsWord(words[1:], "-exec"):
		return "find executes a command for discovered paths"
	case program == "find" && containsWord(words[1:], "-delete"):
		return "find deletes discovered paths"
	case program == "git" && gitChangesPager(words[1:]):
		return "git invokes a pager whose command is configured by the invocation"
	default:
		return "the invocation uses an indirect command wrapper"
	}
}

func shellFeatureReason(command string) string {
	var quote byte
	for i := 0; i < len(command); i++ {
		c := command[i]
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
				continue
			}
			if c == '`' || (c == '$' && i+1 < len(command) && command[i+1] == '(') {
				return "contains command substitution whose result cannot be known without executing it"
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '`':
			return "contains command substitution whose result cannot be known without executing it"
		case '$':
			if i+1 < len(command) && command[i+1] == '(' {
				return "contains command substitution whose result cannot be known without executing it"
			}
		case '<', '>':
			if i+1 < len(command) && command[i+1] == '(' {
				return "contains process substitution whose path cannot be known without executing it"
			}
		case '&':
			if i > 0 && (command[i-1] == '|' || command[i-1] == '&') {
				continue
			}
			if i+1 >= len(command) || command[i+1] != '&' {
				return "runs in the background, so its resource use cannot be bounded here"
			}
		}
	}
	return ""
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
			disqualified = true
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

func commandWordFacts(command string) ([]commandWordFact, bool) {
	var facts []commandWordFact
	var word strings.Builder
	var quote byte
	wordStarted := false
	wordDynamic := false

	flush := func() {
		if wordStarted {
			facts = append(facts, commandWordFact{value: word.String(), dynamic: wordDynamic})
			word.Reset()
			wordStarted = false
			wordDynamic = false
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
			if c == '$' || c == '`' {
				wordDynamic = true
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
		case '>', '<':
			if wordStarted && isFileDescriptor(word.String()) {
				word.WriteByte(c)
				if i+1 < len(command) && (command[i+1] == c || (c == '<' && command[i+1] == '>')) {
					i++
					word.WriteByte(command[i])
				}
				flush()
				continue
			}
			flush()
			word.WriteByte(c)
			wordStarted = true
			if i+1 < len(command) && (command[i+1] == c || (c == '<' && command[i+1] == '>')) {
				i++
				word.WriteByte(command[i])
			}
			flush()
		default:
			if c == '$' || c == '`' {
				wordDynamic = true
			}
			word.WriteByte(c)
			wordStarted = true
		}
	}

	if quote != 0 {
		return nil, false
	}
	flush()
	return facts, true
}

func normalizedProgram(program string) string {
	return strings.ToLower(filepath.Base(program))
}

func disqualifyingWords(words []string) bool {
	if len(words) == 0 {
		return true
	}
	program := normalizedProgram(words[0])
	if strings.HasPrefix(program, "$") {
		return true
	}
	if program == "sudo" || program == "env" || program == "xargs" ||
		program == "watch" || program == "setsid" || program == "ionice" ||
		program == "flock" || program == "nohup" || program == "timeout" ||
		program == "tee" {
		return true
	}
	if (program == "sh" || program == "bash") && shellHasCommandString(words[1:]) {
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
