package content

import (
	"fmt"
	"strings"
	"time"
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

// FeatureWritesOptionNamedPath names the one semantic feature a rule may
// match today: the command writes a file to a path named by one of its own
// options rather than by an operand or a shell redirection.
//
// The vocabulary is CLOSED — knownInvocationFeatures below is the whole of
// it, and a rule naming anything else is an unparseable policy. This package
// owns the vocabulary because it owns the rules that match it; the classifier
// in internal/assistant records these constants rather than its own spelling
// of them, so there is one name per fact and not two.
const FeatureWritesOptionNamedPath = "writes-option-named-path"

// EvaluatorVersion is the READING of commands the rules in this package are
// evaluated under. A widened rule is a claim about what a command does, and a
// permit agreed to under one reading was agreed to on an account of the
// command that a later reading can falsify — so a rule records the version it
// was saved under and goes inert when the two differ (design §5.6).
//
// It lives HERE and not in internal/assistant for the reason the feature
// vocabulary does: internal/assistant imports internal/content, so a content
// evaluator comparing against an assistant constant would be an import cycle.
// content owns the rules and owns their evaluation, so it owns the version
// those rules were evaluated under; the classifier aliases this name rather
// than declaring a second one.
//
// It is 2: the reading changed once, when a path named by an option VALUE
// became a written resource rather than a skipped token (nocx-3j47q).
const EvaluatorVersion = 2

// RuleSource is where a rule came from, and it is a closed set of two. A rule
// a person answered into being and a rule an operator wrote by hand are
// different objects with different trust, and a page that lets you take an
// answer back cannot say what it is taking back without this.
type RuleSource string

const (
	// SourceAnswered: minted from a person's answer to a prompt, over the
	// exact command line they were shown.
	SourceAnswered RuleSource = "answered"
	// SourceWritten: written into the policy document. A document IS
	// written, so this is what an unstated source parses as.
	SourceWritten RuleSource = "written"
)

func (s RuleSource) valid() bool {
	switch s {
	case SourceAnswered, SourceWritten:
		return true
	default:
		return false
	}
}

var knownInvocationFeatures = map[string]struct{}{
	FeatureWritesOptionNamedPath: {},
}

// InvocationSelector is a closed sum with exactly one field set: it says
// WHICH invocations a rule speaks about, and nothing about what it decides.
//
// The three variants are not interchangeable, and the difference is a safety
// property rather than a convenience (design §5.5):
//
//   - Exact matches a fixed number of subcommands and a fixed number of
//     tokens in each, positionally; a '*' matches any one token's contents and
//     never spans a token boundary or a shell separator. This is the only form
//     a person's answer to a prompt can save, because it is the only one that
//     covers exactly the command line they were shown.
//   - Program matches a command word carrying ANY arguments. It may permit
//     only while bound to the effect it was granted under (InvocationRule's
//     GrantedUnder), because "any find" without that binding is a permit for
//     `find . -delete`.
//   - HasFeature matches a command word carrying a semantic feature the
//     CLASSIFIER recorded — never the spelling of a token. `-o`, `--output`,
//     `--output=file`, an attached short option and `-- -o` are one fact
//     written five ways, and a rule over token text is evaded by the first of
//     them the parser normalizes differently. It may never permit.
type InvocationSelector struct {
	Exact      [][]string  `json:"exact,omitempty"`
	Program    string      `json:"program,omitempty"`
	HasFeature *FeatureRef `json:"hasFeature,omitempty"`
}

// FeatureRef names one command word and one feature of the closed vocabulary
// above. Both halves are required: a feature alone would speak for every
// program that can carry it.
type FeatureRef struct {
	Program string `json:"program"`
	Feature string `json:"feature"`
}

// InvocationRule is an exception to the effect matrix for the invocations its
// selector covers. A rule never names a tool (ADR-0028 decision 4): it names a
// command word in a parsed invocation, which is a different thing.
//
// GrantedUnder is the effect the widening permit was granted for, and it is
// checked against the effect the CALL classified as — not against the effect
// the rule was written beside. It is what stops a permit written while a
// program was reading from reaching the same program deleting. The guard
// itself cannot live in Matches, which is not told what the call classified
// as; it lives in the rule loop in EvaluateInvocation.
//
// The last three fields are PROVENANCE (design §5.6): a rule is an object a
// person can take back, so it records when it came into being, where it came
// from, and the reading of commands it was agreed to under. EvaluatorVersion
// has teeth — see needsConfirmation below and the second guard in
// EvaluateInvocation — while CreatedAt and Source are what the page says the
// rule IS. A rule that states no version states an UNKNOWN one, which is not
// the current one.
type InvocationRule struct {
	ID               string             `json:"id"`
	Selector         InvocationSelector `json:"selector"`
	Decision         Decision           `json:"decision"`
	GrantedUnder     Effect             `json:"grantedUnder,omitempty"`
	CreatedAt        time.Time          `json:"createdAt"`
	Source           RuleSource         `json:"source"`
	EvaluatorVersion int                `json:"evaluatorVersion"`
}

// needsConfirmation reports that this rule is inert because it was saved
// under a different reading of commands than the one running now.
//
// Only a LOOSE selector is in that danger. A Program or HasFeature rule
// speaks about command lines nobody was shown, so what it covers is whatever
// the classifier makes of them; an Exact rule names the literal command line
// the person read, and its meaning does not move when the classifier learns
// to see more.
//
// And only a PERMIT, which is the same asymmetry the selectors themselves
// carry: a permit is a claim about what a command does, and a later reading
// can falsify that claim, so it waits for a person. A refusal — or an ask,
// which is a refusal to decide — makes no such claim, and a richer reading
// can only make it cover MORE, which is the safe direction. Inerting one
// would drop it through to a row that may permit, so a version bump nobody
// performed would delete a safety control. It never does.
func (r InvocationRule) needsConfirmation() bool {
	if r.Decision != DecisionPermit {
		return false
	}
	if r.Selector.Program == "" && r.Selector.HasFeature == nil {
		return false
	}
	return r.EvaluatorVersion != EvaluatorVersion
}

// normalizeInvocationRules applies the defaults a DOCUMENT's rules get, and
// only a document's: an operator must be able to hand-write a policy file
// without inventing ids, so a rule with none is minted one here and it
// becomes stable the next time the document is saved. A rule with no source
// is written, because a document is. A rule with no createdAt keeps the zero
// time — a creation time is a fact, and one nobody recorded is not invented.
//
// The version is deliberately NOT defaulted: an unstated reading is unknown,
// and unknown is not current.
func normalizeInvocationRules(rules []InvocationRule) {
	for i := range rules {
		if rules[i].ID == "" {
			rules[i].ID = mintID()
		}
		if rules[i].Source == "" {
			rules[i].Source = SourceWritten
		}
	}
}

// LiteralInvocationRule builds a standing rule from a person's exact command
// line. It produces only an Exact selector, and pattern characters are refused
// so only operator-authored rules can use token matching operators.
func LiteralInvocationRule(inv Invocation, decision Decision) (InvocationRule, error) {
	if !inv.Parsed {
		return InvocationRule{}, fmt.Errorf("invocation is not parsed")
	}
	if inv.Disqualified {
		return InvocationRule{}, fmt.Errorf("invocation is disqualified")
	}
	if len(inv.Resources.Unresolved) > 0 {
		unresolved := inv.Resources.Unresolved[0]
		if unresolved.Reason != "" {
			return InvocationRule{}, fmt.Errorf(
				"the command contains unresolved input %q: %s; its meaning can change between executions",
				unresolved.Path, unresolved.Reason,
			)
		}
		return InvocationRule{}, fmt.Errorf(
			"the command contains unresolved input %q; its meaning can change between executions",
			unresolved.Path,
		)
	}
	rule := InvocationRule{
		// NO ID, and that is the decision (nocx-2019q). An id is the
		// DOCUMENT's name for a rule — what a page takes it back by and
		// what policy.forgetRule names — so it is minted where a rule is
		// STORED (normalizeInvocationRules, on the one strict parse every
		// stored policy crosses) and nowhere else. Minting here gave an
		// identity to a rule that may never be stored: standingOffer builds
		// one on every question purely to read its Label back to a person,
		// and threw the id away every time. It also put a second mint in
		// front of the store's own, so the id a caller held was not
		// necessarily the id the document ended up wearing — and the
		// approval receipt's Undo is exact only if it is.
		//
		// This is why GlobalPolicyStore.SetRule can keep its contract
		// whole: an id names a rule to REPLACE, an absent id is a new rule
		// the store names, and the in-process prompt goes through the same
		// door as the wire with no exception carved for it.
		Selector:         InvocationSelector{Exact: inv.Commands},
		Decision:         decision,
		CreatedAt:        time.Now(),
		Source:           SourceAnswered,
		EvaluatorVersion: EvaluatorVersion,
	}
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
// compound, disqualified, unparsed, unresolved and pattern-bearing invocations
// are not representable without granting more than the question showed.
//
// The boundary is that a standing rule may only be saved over text whose
// meaning cannot change between the reading and the next match.
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

// Label returns the canonical, shell-safe spelling of what the rule covers.
// It is presentation of the SELECTOR, not a second parse of any command line:
// an exact selector reads back as the command it names, a program selector as
// that word followed by an ellipsis, and a feature selector as the word and
// the feature it must carry.
func (r InvocationRule) Label() string {
	switch {
	case r.Selector.HasFeature != nil:
		return ruleTokenLabel(r.Selector.HasFeature.Program) + " \u2026 (" + r.Selector.HasFeature.Feature + ")"
	case r.Selector.Program != "":
		return ruleTokenLabel(r.Selector.Program) + " \u2026"
	}
	commands := make([]string, 0, len(r.Selector.Exact))
	for _, command := range r.Selector.Exact {
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

// Matches reports whether this rule's selector covers the complete canonical
// invocation. It answers SHAPE alone: whether a matching permit actually
// reaches the call is the GrantedUnder guard in EvaluateInvocation, which is
// the only place the effect the call classified as is known.
//
// The soundness bar is the same for all three variants, and it is the one the
// exact form has always applied: an unparsed, disqualified or unresolved
// invocation matches no rule, because its meaning can differ between the
// reading and the next execution. A refusal that therefore does not fire
// falls back to the row, which is the fail-toward-asking default.
func (r InvocationRule) Matches(inv Invocation) bool {
	if !inv.Parsed || inv.Disqualified || len(inv.Resources.Unresolved) != 0 ||
		len(inv.Commands) == 0 {
		return false
	}
	return r.Selector.matches(inv)
}

func (s InvocationSelector) matches(inv Invocation) bool {
	switch {
	case s.HasFeature != nil:
		// A refusal may over-match and may not under-match, so ANY
		// subcommand carrying the word is enough. The feature is a fact
		// about the whole report, which cannot attribute it to one
		// subcommand of a compound line.
		if !hasFeature(inv.Resources.Features, s.HasFeature.Feature) {
			return false
		}
		for _, command := range inv.Commands {
			if len(command) > 0 && command[0] == s.HasFeature.Program {
				return true
			}
		}
		return false
	case s.Program != "":
		// A permit may not over-match, so EVERY subcommand must be that
		// word: "df -h ; rm -rf /" is not an invocation of df.
		for _, command := range inv.Commands {
			if len(command) == 0 || command[0] != s.Program {
				return false
			}
		}
		return true
	}
	if len(s.Exact) != len(inv.Commands) {
		return false
	}
	for i, patternCommand := range s.Exact {
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

func hasFeature(features []string, want string) bool {
	for _, f := range features {
		if f == want {
			return true
		}
	}
	return false
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

// validateInvocationRules is the gate the asymmetry is enforced at, so that
// the unsafe form is not a rule an operator may write and be careful with —
// it is a document that does not parse (ParseEffectPolicy, and WithRule,
// which drops what this rejects).
func validateInvocationRules(rules []InvocationRule) error {
	// An id is what a page takes a rule back by, so two rules may not wear
	// one. An EMPTY id is not a collision: it is a rule that has not been
	// through a document yet (WithRule, a literal built in a test), and the
	// parse path mints one before it reaches here.
	seen := make(map[string]int, len(rules))
	for i, rule := range rules {
		if rule.ID != "" {
			if first, dup := seen[rule.ID]; dup {
				return fmt.Errorf(
					"rule %d: id %q is already used by rule %d; a rule is taken back by its id",
					i, rule.ID, first)
			}
			seen[rule.ID] = i
		}
		if rule.Source != "" && !rule.Source.valid() {
			return fmt.Errorf("rule %d: source %q is not answered or written", i, rule.Source)
		}
		if !rule.Decision.valid() {
			return fmt.Errorf("rule %d: decision %q is not permit, ask or refuse", i, rule.Decision)
		}
		if rule.GrantedUnder != "" && !LatticeEffect(rule.GrantedUnder) {
			return fmt.Errorf("rule %d: grantedUnder %q is not an effect class", i, rule.GrantedUnder)
		}
		set := 0
		if len(rule.Selector.Exact) > 0 {
			set++
		}
		if rule.Selector.Program != "" {
			set++
		}
		if rule.Selector.HasFeature != nil {
			set++
		}
		if set != 1 {
			return fmt.Errorf(
				"rule %d: selector must set exactly one of exact, program or hasFeature, not %d",
				i, set)
		}
		switch {
		case rule.Selector.HasFeature != nil:
			// A feature selector matches a whole class of command lines
			// nobody was shown, so it may narrow and never widen.
			if rule.Decision == DecisionPermit {
				return fmt.Errorf(
					"rule %d: a hasFeature selector may not permit; a loose matcher may only narrow", i)
			}
			if rule.Selector.HasFeature.Program == "" {
				return fmt.Errorf("rule %d: hasFeature names no program", i)
			}
			if _, ok := knownInvocationFeatures[rule.Selector.HasFeature.Feature]; !ok {
				return fmt.Errorf(
					"rule %d: %q is not a feature the classifier records; the vocabulary is closed",
					i, rule.Selector.HasFeature.Feature)
			}
		case rule.Selector.Program != "":
			// A program selector covers every argument list, so a permit
			// is only as narrow as the effect it is bound to.
			if rule.Decision == DecisionPermit && rule.GrantedUnder == "" {
				return fmt.Errorf(
					"rule %d: a program selector may not permit without the effect it was granted under", i)
			}
		default:
			for j, command := range rule.Selector.Exact {
				if len(command) == 0 {
					return fmt.Errorf("rule %d: exact subcommand %d is empty", i, j)
				}
				for k, token := range command {
					if token == "" {
						return fmt.Errorf("rule %d: exact token %d.%d is empty", i, j, k)
					}
				}
			}
		}
	}
	return nil
}

// latticeEffects is the ADR-0020 effect lattice, in the lattice's own order:
// the closed seven the matrix has a row for. It is written down ONCE — every
// loop over "each effect class" reads it, and the exported predicate below
// answers membership of it — because a second spelling of the seven is a
// second list to forget to extend.
var latticeEffects = []Effect{
	EffectObserve, EffectMutateReversible, EffectMutateDestructive,
	EffectPrivilegeChange, EffectDisclose, EffectCrossBoundary, EffectDelegate,
}

// LatticeEffects returns the ADR-0020 effect lattice in the lattice's own
// order — a copy, so a caller cannot reorder the one list.
//
// It is exported for the same reason the predicate below is, one step further
// on: a caller that has to put a SET of effects into a stable order has to
// know that order, and the only alternatives are a second spelling of the
// seven or a sentence whose row names come out in a different order on every
// call. internal/transport orders the rows one matrix write moved with it,
// before naming them to a person.
func LatticeEffects() []Effect {
	return append([]Effect(nil), latticeEffects...)
}

// LatticeEffect reports membership of the ADR-0020 effect lattice — the
// closed seven the matrix has a row for. It is exported because the wire asks
// the same question this package's own gate does: policy.explain must refuse
// an effect outside the lattice rather than explain the ask an absent row
// would decide, and a second list of the seven in the transport would be a
// second answer to drift from this one.
func LatticeEffect(e Effect) bool {
	switch e {
	case EffectObserve, EffectMutateReversible, EffectMutateDestructive,
		EffectPrivilegeChange, EffectDisclose, EffectCrossBoundary, EffectDelegate:
		return true
	}
	return false
}
