package content

import (
	"fmt"
	"strings"
)

// Invocation is the canonical, backend-parsed command representation used by
// both effect classification and invocation-rule matching. Parsed and
// Disqualified are parser facts, not operator input and are omitted from the
// persisted rule form.
type Invocation struct {
	Commands     [][]string `json:"commands,omitempty"`
	Parsed       bool       `json:"-"`
	Disqualified bool       `json:"-"`
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
