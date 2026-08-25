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

// SetPolicy persists a new policy and makes it current. The write happens
// before the value is served (AD-8: one owner of the number; a policy that
// was not persisted is a policy that vanished on restart).
func (s *GlobalPolicyStore) SetPolicy(p content.EffectPolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.doc.Write(s.name, p); err != nil {
		return err
	}
	s.current = p
	return nil
}
