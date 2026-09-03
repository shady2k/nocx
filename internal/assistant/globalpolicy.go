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
	"fmt"
	"sync"

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
}

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
