package content

import (
	"fmt"
	"strings"
	"unicode"
)

// Invocation is the canonical, backend-parsed command representation used by
// both effect classification and invocation-rule matching. Parsed and
// Disqualified are parser facts, not operator input and are omitted from the
// persisted rule form.
type Invocation struct {
	Commands     [][]string     `json:"commands,omitempty"`
	Parsed       bool           `json:"-"`
	Disqualified bool           `json:"-"`
	Resources    ResourceReport `json:"-"`
}

// InvocationRule is an exception to the effect matrix for one exact command
// shape. Patterns match a fixed number of subcommands and a fixed number of
// tokens in each subcommand; they never name a tool. A '*' matches only within
// one token, never "and whatever follows". This deliberately narrow bound is
// safe because over-matching is the failure mode these rules remove.
type InvocationRule struct {
	Pattern  [][]string `json:"pattern"`
	Decision Decision   `json:"decision"`
}

// LiteralInvocationRule builds a standing rule from a person's exact command
// line. Pattern characters are refused so only operator-authored rules can use
// token matching operators.
func LiteralInvocationRule(inv Invocation, decision Decision) (InvocationRule, error) {
	if !inv.Parsed {
		return InvocationRule{}, fmt.Errorf("invocation is not parsed")
	}
	if inv.Disqualified {
		return InvocationRule{}, fmt.Errorf("invocation is disqualified")
	}
	rule := InvocationRule{Pattern: inv.Commands, Decision: decision}
	if err := validateInvocationRules([]InvocationRule{rule}); err != nil {
		return InvocationRule{}, err
	}
	for _, command := range inv.Commands {
		for _, token := range command {
			if strings.ContainsRune(token, '*') {
				return InvocationRule{}, fmt.Errorf(
					"the token %q is a pattern, not a literal command word; a standing answer is saved exactly as shown, and a pattern would make it cover more than the command you were shown",
					token,
				)
			}
		}
	}
	return rule, nil
}

// StandingRule returns the only invocation rule a person may be offered from
// this canonical parse. A standing answer must show one complete command:
// compound, disqualified, unparsed and pattern-bearing invocations are not
// representable without granting more than the question showed.
func StandingRule(inv Invocation) (InvocationRule, string) {
	if !inv.Parsed {
		return InvocationRule{}, "the command could not be parsed safely"
	}
	if len(inv.Commands) == 0 {
		return InvocationRule{}, "the command has no complete invocation to show"
	}
	if len(inv.Commands) != 1 {
		return InvocationRule{}, "the command contains more than one command"
	}
	if inv.Disqualified {
		return InvocationRule{}, "the command uses an indirect wrapper or shell feature"
	}
	rule, err := LiteralInvocationRule(inv, DecisionPermit)
	if err != nil {
		return InvocationRule{}, err.Error()
	}
	return rule, ""
}

// Label returns the canonical, shell-safe spelling of the invocation pattern.
// It is presentation of Pattern, not a second parse of the original command.
func (r InvocationRule) Label() string {
	commands := make([]string, 0, len(r.Pattern))
	for _, command := range r.Pattern {
		tokens := make([]string, 0, len(command))
		for _, token := range command {
			tokens = append(tokens, ruleTokenLabel(token))
		}
		commands = append(commands, strings.Join(tokens, " "))
	}
	return strings.Join(commands, " ; ")
}

func ruleTokenLabel(token string) string {
	if token != "" {
		safe := true
		for _, r := range token {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) &&
				!strings.ContainsRune("_./:@+,=-", r) {
				safe = false
				break
			}
		}
		if safe {
			return token
		}
	}
	return "'" + strings.ReplaceAll(token, "'", "'\\''") + "'"
}

// Matches reports whether this rule covers the complete canonical invocation.
// Every subcommand and every token must match. A token '*' matches any one
// token's contents; it never spans token boundaries or shell separators.
func (r InvocationRule) Matches(inv Invocation) bool {
	if !inv.Parsed || inv.Disqualified || len(r.Pattern) != len(inv.Commands) {
		return false
	}
	for i, patternCommand := range r.Pattern {
		command := inv.Commands[i]
		if len(patternCommand) != len(command) {
			return false
		}
		for j, patternToken := range patternCommand {
			if !tokenPatternMatches(patternToken, command[j]) {
				return false
			}
		}
	}
	return true
}

func tokenPatternMatches(pattern, token string) bool {
	pi, ti := 0, 0
	star := -1
	starToken := -1
	for ti < len(token) {
		if pi < len(pattern) && pattern[pi] == token[ti] {
			pi++
			ti++
			continue
		}
		if pi < len(pattern) && pattern[pi] == '*' {
			star = pi
			starToken = ti
			pi++
			continue
		}
		if star >= 0 {
			pi = star + 1
			starToken++
			ti = starToken
			continue
		}
		return false
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

func validateInvocationRules(rules []InvocationRule) error {
	for i, rule := range rules {
		if !rule.Decision.valid() {
			return fmt.Errorf("rule %d: decision %q is not permit, ask or refuse", i, rule.Decision)
		}
		if len(rule.Pattern) == 0 {
			return fmt.Errorf("rule %d: pattern must contain a subcommand", i)
		}
		for j, command := range rule.Pattern {
			if len(command) == 0 {
				return fmt.Errorf("rule %d: pattern subcommand %d is empty", i, j)
			}
			for k, token := range command {
				if token == "" {
					return fmt.Errorf("rule %d: pattern token %d.%d is empty", i, j, k)
				}
			}
		}
	}
	return nil
}
