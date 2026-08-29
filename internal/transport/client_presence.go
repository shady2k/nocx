package transport

import "sync"

// WHO IS WATCHING — the one fact the vault's seal-on-last-detach policy
// reads (design D9).
//
// The transport is the only thing that knows how many clients are attached,
// and it must not be the thing that decides what that means: sealing is the
// vault's policy and lives in internal/vault/presence.go. So this seam
// carries a count and nothing else — no "seal", no "the last one left", no
// interpretation. AD-8, one owner per behaviour.
//
// The count is taken AFTER the change to the connection set, so zero really
// means zero and a reader never has to subtract.

// ClientPresence is told how many clients are attached, every time that
// number changes. Satisfied by *vault.Vault.
//
// It is deliberately not part of VaultLifecycle: that interface is the
// vault.* JSON-RPC method surface, and presence is not a method anybody
// calls over the wire. A transport built without this option reports to
// nobody, which is what every test that is not about presence wants.
type ClientPresence interface {
	// ClientsAttached is called with the number of attached clients after
	// an attach or a detach. It may block for the duration of its own
	// policy — it is called off the connection's own goroutine, never from
	// the read loop's critical path — but it must not call back into the
	// transport's connection registry.
	ClientsAttached(n int)
}

// WithClientPresence attaches the presence observer. Without it the backend
// never seals on detach, which is correct for a transport with no vault and
// wrong for the shipped composition root — internal/app wires the vault
// here, and that line is the whole of D9's plumbing.
func WithClientPresence(p ClientPresence) WSServerOption {
	return func(s *WSServer) { s.presence = p }
}

// presenceNotifier serializes presence deliveries so the observer sees them
// in the order the count actually reached those values.
//
// The count cannot simply be read at the call site and passed along: two
// goroutines that both changed the set could then deliver in the opposite
// order and leave the observer believing the last value it saw. Holding this
// mutex ACROSS the read and the delivery is what fixes it — whichever
// goroutine delivers last read the count last, so the final delivery is the
// final truth. Both mutations happen before their notify, so there is always
// a delivery after the last one.
type presenceNotifier struct {
	mu sync.Mutex
}

// notePresence reports the current client count to the observer. It is
// called after every register and every unregister, and is a no-op when no
// observer is wired.
func (s *WSServer) notePresence() {
	if s.presence == nil {
		return
	}
	s.presenceNotify.mu.Lock()
	defer s.presenceNotify.mu.Unlock()

	s.connsMu.Lock()
	n := len(s.conns)
	s.connsMu.Unlock()

	// Outside connsMu: the observer seals a vault, which takes locks of its
	// own and calls provider code. Holding the connection registry across
	// that would make every future broadcast wait on a keyring.
	s.presence.ClientsAttached(n)
}
