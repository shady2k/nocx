package assistant

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/shady2k/nocx/internal/content"
)

// CommandEffect derives the effect of one run command from its text and the
// tool's reachable effect set. It is a PURE function of those facts: no
// registry lookup, settings read, shell invocation, or runtime state.
//
// The parser deliberately does not pretend to be the person's shell. An alias
// or shell function can make `ls` mean something else in their rc files, which
// nocx does not read. A lowered call therefore becomes `observe`, whose
// default policy is still "Ask every time"; a mistaken alias classification
// loses only the blanket grant, not the chance to ask.
//
// The class is derived from the structural resource report. A report with only
// resolved resources selects its mapped member; writes, deletes, network
// access and unresolved parts select the set's worst member.
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
// CanonicalInvocation is the parser above, for the one caller outside this
// package that has to ask the SAME question a run asks: policy.explain, which
// explains what the policy decides about a command line a person is looking
// at. It is deliberately the same function and not a second reading — an
// explanation derived from a different parse would explain a decision nobody
// took, which is the whole failure the trace exists to prevent.
func CanonicalInvocation(command string) content.Invocation {
	return parseCanonicalInvocation(command)
}

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
		inv.Resources = appendDynamicUnresolved(
			appendResourceReport(inv.Resources, subcommand, facts), facts,
		)
		if redirectionDisqualifies(facts) {
			inv.Disqualified = true
		}
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

// commandSelection is the one classification of a run command: the row that
// governs it and every candidate its resources derived (nocx-jxq97). The
// candidates are carried so an approval surface can say that a call both
// reached a host and wrote a file while one row still governs the decision
// (ADR-0020 §7). A command the parser could not read has no report to derive
// from: it takes the declared worst and names nothing, because a candidate for
// an unparsed command would be a guess offered as a fact.
func commandSelection(inv content.Invocation, declared []content.Effect) content.EffectSelection {
	if !inv.Parsed {
		return content.EffectSelection{Effect: content.WorstEffect(declared)}
	}
	return inv.Resources.SelectEffect(declared)
}

func commandEffect(inv content.Invocation, declared []content.Effect) content.Effect {
	return commandSelection(inv, declared).Effect
}

// ClassifyInvocation is commandEffect above, for the one caller outside this
// package that has to ask the SAME question a run asks about a command NOBODY
// HAS RUN: policy.classify, which reads a command line a person typed so that a
// widening permit can be minted from a classification rather than from a word.
//
// It is deliberately the same function and not a second reading, for
// CanonicalInvocation's reason one step further on. The parser answers what the
// command IS; this answers which row governs it, and the permit the page then
// writes carries that answer in GrantedUnder — where the evaluator checks it
// against the effect the CALL classified as. Two readings would mint a permit
// under one account of the command and enforce it under another, which is the
// exact failure GrantedUnder exists to prevent.
//
// declared is the tool declaration table's answer and never a caller's opinion:
// the effect a call classifies as is bounded by what a tool can reach at all,
// and a set invented at the call site would classify against a machine that
// does not exist.
func ClassifyInvocation(inv content.Invocation, declared []content.Effect) content.Effect {
	return commandEffect(inv, declared)
}

type readProgramRule struct {
	disqualifies func([]string) bool
}

// readPrograms is the one closed allowlist for commands whose ordinary forms
// expose filesystem reads. Resource extraction, rather than this table, owns
// effect derivation: commands with writes or network access have their own
// resource verbs below.
// Side-effect-free output commands such as echo are included because their
// no-resource form is also observe.
var readPrograms = map[string]readProgramRule{
	"cat":    {},
	"cut":    {},
	"df":     {},
	"du":     {},
	"echo":   {},
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
	value       string
	dynamic     bool
	fdDupTarget bool
}

func appendResourceReport(report content.ResourceReport, subcommand string, facts []commandWordFact) content.ResourceReport {
	programFacts := commandProgramFacts(facts)
	if len(programFacts) == 0 {
		withoutRedirections(facts, &report)
		return unresolvedCommand(report, facts[0].value, "has no command")
	}
	words := make([]string, 0, len(programFacts))
	for _, fact := range programFacts {
		words = append(words, fact.value)
	}
	// This is an allow-list: over-matching would permit more, so preserve case.
	program := allowListProgram(words[0])

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
		operands := resourceOperands("mv", args, &report)
		if len(operands) == 0 {
			return unresolvedCommand(report, program, "has no statically named operand")
		}
		for _, operand := range operands[:len(operands)-1] {
			report = addResource(report, operand, content.ResourceRead)
			report = addResource(report, operand, content.ResourceDelete)
		}
		return addResource(report, operands[len(operands)-1], content.ResourceWrite)
	case "rm":
		operands := resourceOperands("rm", args, &report)
		if len(operands) == 0 {
			return unresolvedCommand(report, program, "has no statically named path")
		}
		for _, operand := range operands {
			report = addResource(report, operand, content.ResourceDelete)
		}
		return report
	case "tee":
		for _, operand := range resourceOperands("tee", args, &report) {
			report = addResource(report, operand, content.ResourceWrite)
		}
		return report
	case "sort":
		// The output option and its three spellings are resolved by the same
		// table curl uses, so a written option value has one owner.
		for _, operand := range resourceOperands("sort", args, &report) {
			report = addResource(report, operand, content.ResourceRead)
		}
		return report
	case "uniq":
		operands := resourceOperands("uniq", args, &report)
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
		operands := resourceOperands(program, args, &report)
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
		operands := resourceOperands("curl", args, &report)
		if len(operands) == 0 {
			return unresolvedCommand(report, program, "has no statically named URL")
		}
		for _, operand := range operands {
			report = addResource(report, operand, content.ResourceNetwork)
		}
		return report
	case "ssh":
		operands := resourceOperands("ssh", args, &report)
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
	for _, operand := range readOperands(program, args, &report) {
		report = addResource(report, operand, content.ResourceRead)
	}
	return report
}

func commandProgramFacts(facts []commandWordFact) []commandWordFact {
	for i := 0; i < len(facts); {
		if !isRedirection(facts[i].value) {
			return facts[i:]
		}
		i += 2
	}
	return nil
}

// Dynamic tokens are recorded even when they are options rather than resource
// operands, because a standing rule must cover every token it was shown.
func appendDynamicUnresolved(report content.ResourceReport, facts []commandWordFact) content.ResourceReport {
	for _, fact := range facts {
		if !fact.dynamic {
			continue
		}
		alreadyUnresolved := false
		for _, unresolved := range report.Unresolved {
			if unresolved.Path == fact.value {
				alreadyUnresolved = true
				break
			}
		}
		if alreadyUnresolved {
			continue
		}
		report.Unresolved = append(report.Unresolved, content.UnresolvedResource{
			Path: fact.value, Verb: content.ResourceUnknown,
			Reason: fmt.Sprintf("could not resolve %s without executing shell expansion", fact.value),
		})
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

func addFeature(report content.ResourceReport, feature string) content.ResourceReport {
	for _, existing := range report.Features {
		if existing == feature {
			return report
		}
	}
	report.Features = append(report.Features, feature)
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
	programSeen := false
	for i := 0; i < len(facts); i++ {
		fact := facts[i]
		if isRedirection(fact.value) {
			if i+1 >= len(facts) {
				*report = unresolvedCommand(*report, fact.value, "has no target path")
				continue
			}
			target := facts[i+1].value
			// A null-device sink and a file-descriptor duplication only
			// redirect or discard streams; neither mutates a filesystem path.
			// Keep recording and marking real file targets unresolved because
			// their writes remain genuine mutations.
			if !isNonMutatingRedirectionTarget(target, facts[i+1].fdDupTarget) {
				if isReadWriteRedirection(fact.value) {
					*report = addResource(*report, facts[i+1], content.ResourceRead)
					*report = addResource(*report, facts[i+1], content.ResourceWrite)
				} else {
					*report = addResource(*report, facts[i+1], redirectionVerb(fact.value))
				}
				*report = appendUnresolvedRedirection(*report, fact.value, target)
			}
			i++
			continue
		}
		if !programSeen {
			programSeen = true
			continue
		}
		args = append(args, fact)
	}
	return args
}

func redirectionDisqualifies(facts []commandWordFact) bool {
	for i := 0; i < len(facts); i++ {
		if !isRedirection(facts[i].value) {
			continue
		}
		if i+1 >= len(facts) ||
			!isNonMutatingRedirectionTarget(facts[i+1].value, facts[i+1].fdDupTarget) {
			return true
		}
		i++
	}
	return false
}

func isNonMutatingRedirectionTarget(target string, fdDupTarget bool) bool {
	return target == "/dev/null" ||
		(fdDupTarget && strings.HasPrefix(target, "&") && isFileDescriptor(target[1:]))
}

func isRedirection(word string) bool {
	if word == ">" || word == ">>" || word == "<" || word == "<<" || word == "<>" || word == "&>" {
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

// featureWritesOptionNamedPath is recorded when a command writes a file to a
// path named by one of its own option values rather than by an operand or a
// shell redirection. A refusal matches this fact, never the spelling of the
// token that carried it.
//
// The name is content's, not this package's: content owns the closed feature
// vocabulary because it owns the rules that match it, and one fact carries one
// name rather than two spellings that agree until they do not.
const featureWritesOptionNamedPath = content.FeatureWritesOptionNamedPath

// EvaluatorVersion is the reading of commands THIS file implements, and it is
// content's constant rather than a second one: content owns the rules and
// their evaluation, so it owns the version they were saved under, and a
// constant declared here could not be compared against by content at all
// (this package imports content, never the other way round). One fact, one
// name.
const EvaluatorVersion = content.EvaluatorVersion

// resourceOperands returns the operands of an invocation, skipping options and
// the values they consume. A skipped option value that is a path the command
// WRITES is not silently dropped: it is appended to the report through the
// pointer, because it is a resource exactly as an operand would be.
func resourceOperands(program string, args []commandWordFact, report *content.ResourceReport) []commandWordFact {
	operands := make([]commandWordFact, 0, len(args))
	optionsEnded := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !optionsEnded && arg.value == "--" {
			optionsEnded = true
			continue
		}
		if !optionsEnded && strings.HasPrefix(arg.value, "-") {
			if handled, resolved, target, consumed := optionWrittenTarget(program, args, i); handled {
				if resolved {
					*report = addResource(*report, target, content.ResourceWrite)
					*report = addFeature(*report, featureWritesOptionNamedPath)
				} else {
					*report = unresolvedCommand(*report, program, "has an output option without a path")
				}
				i += consumed
				continue
			}
			if optionTakesNextValue(program, arg.value) && i+1 < len(args) {
				i++
			}
			continue
		}
		operands = append(operands, arg)
	}
	return operands
}

// optionWritesNextValue reports whether this option's VALUE is a path the
// command writes. It is strictly a subset of optionTakesNextValue for any
// program whose operands are read through resourceOperands: an entry here that
// is missing there would never be consulted, because the value would already
// have been taken for an operand. sort is the exception and is deliberate —
// its branch consults this table directly so that "which option value is a
// written path" has one owner rather than two.
//
// The distinction is per program and cannot be guessed from the letter.
// curl -o and sort -o name output files; ssh -o is a config keyword, bash -o
// is a shell option name, install -o is an owner, and grep -f is a pattern
// file the command READS. install -t and cp -t also name a written path, but
// targetDirectoryOperands has owned those since before this table existed and
// records them as the write target rather than as a skipped option value.
func optionWritesNextValue(program, option string) bool {
	name := option
	if i := strings.IndexByte(option, '='); i >= 0 {
		name = option[:i]
	}
	switch program {
	case "curl", "sort":
		return name == "-o" || name == "--output"
	default:
		return false
	}
}

// optionWrittenTarget resolves the target of a write-bearing option in the
// three forms it can take: "-o file" and "--output file" (the value is the
// next word), "--output=file" (attached after =), and "-ofile" (attached to a
// short option).
//
// handled reports that args[i] is a write-bearing option; resolved reports
// that it had a value at all, so a trailing "-o" becomes an unresolved report
// rather than a silently ignored write. consumed is the number of ADDITIONAL
// words taken.
func optionWrittenTarget(program string, args []commandWordFact, i int) (handled, resolved bool, target commandWordFact, consumed int) {
	arg := args[i]
	if eq := strings.IndexByte(arg.value, '='); eq >= 0 {
		if !optionWritesNextValue(program, arg.value) {
			return false, false, commandWordFact{}, 0
		}
		return true, true, commandWordFact{value: arg.value[eq+1:], dynamic: arg.dynamic}, 0
	}
	if optionWritesNextValue(program, arg.value) {
		if i+1 >= len(args) {
			return true, false, commandWordFact{}, 0
		}
		return true, true, args[i+1], 1
	}
	// An attached short option: "-ofile" is "-o" and "file".
	if !strings.HasPrefix(arg.value, "--") && len(arg.value) > 2 &&
		optionWritesNextValue(program, arg.value[:2]) {
		return true, true, commandWordFact{value: arg.value[2:], dynamic: arg.dynamic}, 0
	}
	return false, false, commandWordFact{}, 0
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

func readOperands(program string, args []commandWordFact, report *content.ResourceReport) []commandWordFact {
	operands := resourceOperands(program, args, report)
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
	// This is a deny-list: over-matching only refuses more, so fold case.
	program := denyListProgram(words[0])
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
			if (i > 0 && (command[i-1] == '>' || command[i-1] == '<')) ||
				(i+1 < len(command) && command[i+1] == '>') {
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
			if (i > 0 && (command[i-1] == '>' || command[i-1] == '<')) ||
				(i+1 < len(command) && command[i+1] == '>') {
				current.WriteByte(c)
				continue
			}
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
	wordFDDupTarget := false

	flush := func() {
		if wordStarted {
			facts = append(facts, commandWordFact{
				value:       word.String(),
				dynamic:     wordDynamic,
				fdDupTarget: wordFDDupTarget,
			})
			word.Reset()
			wordStarted = false
			wordDynamic = false
			wordFDDupTarget = false
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
			if c == '>' && wordStarted && word.String() == "&" {
				word.WriteByte(c)
				flush()
				continue
			}
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
			if c == '&' && i > 0 && (command[i-1] == '>' || command[i-1] == '<') {
				wordFDDupTarget = true
			}
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

func allowListProgram(program string) string {
	return filepath.Base(program)
}

func denyListProgram(program string) string {
	return strings.ToLower(filepath.Base(program))
}

func disqualifyingWords(words []string) bool {
	if len(words) == 0 {
		return true
	}
	// This is a deny-list: over-matching only refuses more, so fold case.
	program := denyListProgram(words[0])
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
