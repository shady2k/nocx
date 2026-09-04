package assistant

// The ONE global agent policy (ADR-0020 §7 as amended 2026-08-16,
// accepted): the matrix from which every run's grant is minted, the default
// until the workspace grant source lands (nocx-mp2vd). The resolution order — workspace overrides global, global is
// the default — is stated ONCE, in content.ResolvePolicy; this package
// supplies the global side of that order and nothing else.
//
// The store persists the matrix as an atomic JSON document. An absent, empty
// or unparseable document IS a policy — the zero matrix, which asks for
// everything — never a reason to refuse loading or to guess. Unparseable
// includes a hand-edited document whose keys name tools rather than effects:
// ParseEffectPolicy rejects it, and the store degrades to ask (ADR-0028
// decision 4, "asserted by trying" — the wire and the store share the gate).

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/storage"
)

// GlobalPolicy is the seam the transport mints run grants through. One
// method family, one consumer; the concrete store implements it.
type GlobalPolicy interface {
	// Policy returns the current global policy. The zero matrix — every
	// effect asks — is a valid answer: it is what an unconfigured store
	// returns, and asking is the safe end.
	Policy() content.EffectPolicy
	// SetPolicy validates and persists a new policy; the next mint reads
	// it live, no restart.
	SetPolicy(p content.EffectPolicy) error
	// SetRule writes ONE invocation rule — adds it, or replaces the rule
	// wearing its id — leaving the matrix and every other rule alone. The
	// stored rule comes back, so a caller that supplied no id learns the
	// one the mint gave it.
	SetRule(rule content.InvocationRule) (content.InvocationRule, error)
	// ForgetRule removes ONE invocation rule by id. An id naming no rule
	// is not an error: the rule is already not there, and the answer says
	// so with false.
	ForgetRule(id string) (bool, error)
}

// ErrNoSuchRule is what a mutation naming a rule the document does not carry
// answers with. It is a sentinel rather than a string because the transport
// turns exactly this one into invalid params, and every other failure into an
// internal error: an id the caller invented is the caller's mistake, and a
// store that could not be written is ours.
var ErrNoSuchRule = errors.New("agent policy: no rule with that id")

// GlobalPolicyStore is the durable home of the global policy.
type GlobalPolicyStore struct {
	doc     storage.DocumentStore
	name    string
	mu      sync.RWMutex
	current content.EffectPolicy
}

// NewGlobalPolicyStore loads the policy document. A missing document, an
// unreadable one, or one that fails the strict matrix parse (a tool-name key
// anywhere is unparseable) all resolve to the zero matrix: a store that
// cannot be read is a policy that asks, never one that permits.
func NewGlobalPolicyStore(doc storage.DocumentStore, name string) *GlobalPolicyStore {
	s := &GlobalPolicyStore{doc: doc, name: name}
	var raw json.RawMessage
	found, err := doc.Read(name, &raw)
	if err != nil || !found {
		return s
	}
	if parsed, perr := content.ParseEffectPolicy(raw); perr == nil {
		s.current = parsed
	}
	return s
}

// Policy returns the current global policy (the zero matrix when none has
// been expressed).
func (s *GlobalPolicyStore) Policy() content.EffectPolicy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// SetPolicy persists a new policy and makes it current. A matrix-only caller
// cannot express invocation rules, so an omitted or null rules field must
// preserve the stored rules; only a non-nil rules slice can replace them.
// This keeps a forgetful caller from revoking standing answers it never
// received.
// The write happens before the value is served (AD-8: one owner of the
// number; a policy that was not persisted is a policy that vanished on
// restart).
func (s *GlobalPolicyStore) SetPolicy(p content.EffectPolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Rules == nil {
		p.Rules = append([]content.InvocationRule(nil), s.current.Rules...)
	}
	if err := s.doc.Write(s.name, p); err != nil {
		return err
	}
	s.current = p
	return nil
}

// SetRule writes ONE invocation rule into the stored document, read-modify-
// write under the store's OWN lock. That is the whole point of it existing:
// a caller that reads the policy, edits it and writes it back holds a
// document that is stale from the moment it was read, and the approval prompt
// writes rules at the same time the settings page does. The gesture is
// "this one rule", so the write is that and nothing else — every other rule,
// and all seven rows, are whatever the store held a microsecond ago.
//
// A rule with no id is APPENDED and minted one; a rule whose id names a
// stored rule REPLACES it in place, keeping its position, its creation time
// and where it came from unless the caller states otherwise. A rule carrying
// an id that names nothing is ErrNoSuchRule — ids are server-authoritative
// (AD-7), so choosing the identity of a new rule is not a caller's to do.
//
// Like WidenRowScope, it goes through the policy's WIRE FORM rather than
// reaching into the struct: content.ParseEffectPolicy is the ONE strict gate
// every stored policy crosses, it is where the id is minted
// (normalizeInvocationRules), and it is where a duplicate id, an invalid
// source, a loose permit and an unknown feature are refused. A second
// validator here would be a second answer to the same question.
func (s *GlobalPolicyStore) SetRule(rule content.InvocationRule) (content.InvocationRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rules := append([]content.InvocationRule(nil), s.current.Rules...)
	at := len(rules)
	if rule.ID != "" {
		at = -1
		for i := range rules {
			if rules[i].ID == rule.ID {
				at = i
				break
			}
		}
		if at < 0 {
			return content.InvocationRule{}, fmt.Errorf("%w: %q", ErrNoSuchRule, rule.ID)
		}
		// Provenance the replacement does not restate is the stored
		// rule's: an edit changes what the rule SAYS, and cannot change
		// when it came into being or that a person answered it.
		if rule.CreatedAt.IsZero() {
			rule.CreatedAt = rules[at].CreatedAt
		}
		if rule.Source == "" {
			rule.Source = rules[at].Source
		}
		rules[at] = rule
	} else {
		if rule.CreatedAt.IsZero() {
			rule.CreatedAt = time.Now()
		}
		rules = append(rules, rule)
	}

	next, err := s.writeRules(rules)
	if err != nil {
		return content.InvocationRule{}, err
	}
	return next.Rules[at], nil
}

// ForgetRule removes ONE invocation rule by id, under the store's own lock
// and with the same one-object discipline SetRule has.
//
// An unknown id is NOT an error and writes nothing. Forgetting is idempotent
// by nature — the person asked for the rule to be gone and it is gone — and
// raising would turn a double click, or a page whose read predates somebody
// else's forget, into an error dialog about a state the person already wanted.
// The answer says which happened so a caller is never guessing.
func (s *GlobalPolicyStore) ForgetRule(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	at := -1
	for i := range s.current.Rules {
		if s.current.Rules[i].ID == id && id != "" {
			at = i
			break
		}
	}
	if at < 0 {
		return false, nil
	}
	rules := append([]content.InvocationRule(nil), s.current.Rules[:at]...)
	rules = append(rules, s.current.Rules[at+1:]...)
	if _, err := s.writeRules(rules); err != nil {
		return false, err
	}
	return true, nil
}

// writeRules persists the current matrix carrying exactly these rules and
// makes it current. The caller holds the lock.
//
// The candidate crosses content.ParseEffectPolicy before anything is written,
// so a rule the gate refuses leaves both the document and the in-memory value
// exactly as they were — the interval closes where it opened, and there is no
// state in which the store holds a policy the parser would reject.
func (s *GlobalPolicyStore) writeRules(rules []content.InvocationRule) (content.EffectPolicy, error) {
	candidate := s.current
	candidate.Rules = rules
	raw, err := json.Marshal(candidate)
	if err != nil {
		return content.EffectPolicy{}, fmt.Errorf("agent policy: writing a rule: %w", err)
	}
	parsed, err := content.ParseEffectPolicy(raw)
	if err != nil {
		return content.EffectPolicy{}, fmt.Errorf("agent policy: writing a rule: %w", err)
	}
	if err := s.doc.Write(s.name, parsed); err != nil {
		return content.EffectPolicy{}, err
	}
	s.current = parsed
	return parsed, nil
}

// WidenRowScope returns p with one more scope on ONE effect's row — the whole
// of what the widening answer changes (design §5.3: the answer "atomically
// widens the row's scopes and approves this call").
//
// It goes through the policy's own WIRE FORM rather than reaching into the
// matrix struct, and that is deliberate. content.EffectPolicy states in as
// many words that SetRowDecision is its only exported mutator, "a caller
// reaching into the struct fields would be a second place that knows the
// lattice's shape" — and a second place is exactly what this would become.
// The JSON document already owns the effect→row mapping (the row keys ARE the
// effect names), and content.ParseEffectPolicy is the ONE strict gate every
// operator-supplied policy crosses, so a widening written this way is
// validated by the same code that would reject a hand-edited document naming
// a tool, a bad path scope or a tool-kind scope.
//
// Widening is idempotent: a scope the row already states is not appended
// twice, so answering the same question twice cannot grow the document.
// An effect outside the lattice is an error rather than a silent no-op — a
// widening that quietly widened nothing would resume a call whose next
// identical proposal asks again, which is the defect this exists to remove.
func WidenRowScope(p content.EffectPolicy, e content.Effect, scope content.GrantScope) (content.EffectPolicy, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return p, fmt.Errorf("agent policy: widening %s: %w", e, err)
	}
	var doc map[string]json.RawMessage
	if err = json.Unmarshal(raw, &doc); err != nil {
		return p, fmt.Errorf("agent policy: widening %s: %w", e, err)
	}
	rowRaw, ok := doc[string(e)]
	if !ok {
		return p, fmt.Errorf("agent policy: widening %s: no such effect row", e)
	}
	var row content.EffectRow
	if err = json.Unmarshal(rowRaw, &row); err != nil {
		return p, fmt.Errorf("agent policy: widening %s: %w", e, err)
	}
	for _, existing := range row.Scopes {
		if existing == scope {
			return p, nil
		}
	}
	row.Scopes = append(row.Scopes, scope)
	if doc[string(e)], err = json.Marshal(row); err != nil {
		return p, fmt.Errorf("agent policy: widening %s: %w", e, err)
	}
	next, err := json.Marshal(doc)
	if err != nil {
		return p, fmt.Errorf("agent policy: widening %s: %w", e, err)
	}
	widened, err := content.ParseEffectPolicy(next)
	if err != nil {
		return p, fmt.Errorf("agent policy: widening %s: %w", e, err)
	}
	return widened, nil
}
