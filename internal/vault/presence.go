package vault

// THE VAULT SEALS WHEN THE LAST CLIENT DETACHES (design D9).
//
// Until the coordinator existed, quitting the window WAS stopping the
// backend, so App.Shutdown -> Vault.Close -> Seal was a complete answer: the
// root key never outlived the thing the person had just closed. The
// coordinator outlives the window on purpose, so that answer now leaves the
// root key in a live process heap for days — an exposure this epic created
// and this file is the payment for.
//
// The rule is stated on CLIENT PRESENCE, not on a timer and not on process
// exit. The transport counts attached clients and tells this file whenever
// the count changes; zero means nobody can see the vault, and a vault nobody
// can see holds nothing.
//
// THE INVARIANT, WITH BOTH ENDS NAMED. The root key is absent from memory
// from the moment the last client detaches until a client is attached AND a
// person unlocks. Attaching does not open it; only Unseal does. The closing
// event is the successful unseal, and nothing between those two points can
// put key material back — Seal wipes the bytes, bumps the generation so any
// write still in flight is rejected, and every later read finds StateSealed.
//
// THE COST IS REAL AND IS PAID IN THE OPEN. An SSH session that needs a
// secret while you are away cannot reconnect by itself, so it SUSPENDS: the
// vault holds the operation, records that it is waiting for an unlock, and
// raises the prompt the moment a client attaches. That is what awaitClient
// below is, and unlock.go's raise loop is where it is used. The alternative
// — failing the operation with "no client connected" — is the silent
// failure D9 exists to avoid.

import (
	"context"
	"time"
)

// DefaultUnlockSuspension is how long an operation may wait for a client to
// come back before it gives up.
//
// WHICH OPERATIONS MAY WAIT: only the ones that already block on a person —
// a credential.Operation read, which by ADR-0032 as amended is raised
// outside every capability admission and outside the control read loop. A
// report read never reaches here (it maps sealed to ErrSealedQuiet), and no
// admission is held across the wait, so a suspended operation costs a
// goroutine and nothing else.
//
// FOR HOW LONG: eight hours, which is "away for a working day" — the case
// D9 names. It is a ceiling, not a schedule: the caller's own context
// usually ends the wait far sooner, and this exists so that a caller with no
// deadline of its own still gets an answer instead of a goroutine that waits
// forever. Reaching it is ErrUnlockSuspended, which is a refusal a person
// can read, not a hang.
const DefaultUnlockSuspension = 8 * time.Hour

// SetUnlockSuspension replaces the suspension ceiling. Tests use it to make
// the expiry observable in milliseconds; nothing in a shipped build calls it,
// which is why it is a setter rather than a construction parameter — the
// production value is the constant above and there is no configuration in
// front of it.
func (v *Vault) SetUnlockSuspension(d time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.unlockSuspension = d
}

// ClientsAttached reports how many clients are attached to the backend. It
// is called by the transport on every attach and every detach, with the
// count taken after the change, and it is the ONE place the seal-on-detach
// policy lives.
//
// Zero seals. Non-zero releases whatever is suspended waiting for a client,
// and nothing else — in particular it does not unseal, because a client
// being present is not a person having unlocked.
//
// A window reload, a dropped socket and a quit all reach here identically,
// and that is deliberate: the transport cannot tell them apart either, and a
// grace period that guessed would be exactly the timer D9 refuses.
func (v *Vault) ClientsAttached(n int) {
	v.mu.Lock()
	v.clients = n
	v.clientsKnown = true
	waiters := v.clientWait
	if n > 0 {
		// Wake everything suspended on "nobody to ask". The channel is the
		// signal; a fresh one is installed so the next suspension has
		// something to wait on.
		if waiters != nil {
			close(waiters)
			v.clientWait = nil
		}
	}
	// A prompt that is already on the wire when the last client leaves is
	// addressed to nobody: the transport broadcast it to the connections
	// that existed then, and a client attaching later never sees that
	// notification. So the ATTEMPT is abandoned here and unlock.go's loop
	// suspends and raises a fresh one when somebody returns. The PROMPT
	// survives — every joined caller keeps waiting on the same one, so a
	// returning client still sees a single dialog naming every operation
	// that is waiting.
	var abandon context.CancelFunc
	if n == 0 && v.unlockAttempt != nil {
		v.unlockAttemptAbandoned = true
		abandon = v.unlockAttempt
	}
	v.mu.Unlock()

	if abandon != nil {
		abandon()
	}
	if n > 0 {
		return
	}

	// Seal outside the lock: Seal takes it itself, and calls provider Lock
	// after releasing it (ADR-0011 §4).
	sealed := v.State() == StateUnsealed
	v.Seal()
	if sealed {
		v.logger.Info("vault sealed: the last client detached")
	}
}

// awaitClient blocks until at least one client is attached, the caller's
// context ends, or the suspension ceiling is reached.
//
// It returns nil the instant a client is already there, so the ordinary case
// — a person is looking at the window — costs one lock acquisition and no
// waiting at all. The suspended case is logged once per suspension with the
// reason the prompt would have carried, because that log line is the only
// record of a waiting operation while there is nobody to show a dialog to;
// the person learns of it when they return and the dialog names it.
//
// tracked reports whether presence is being reported to this vault at all. A
// vault nobody tells — every one built without a transport, which is most of
// them outside the composition root — has a client count of zero that means
// "nobody has said", and suspending on that would hang callers rather than
// answering them. Such a vault keeps the behaviour it had before D9: raise
// once, and let ErrNoUnlockClient be the answer.
func (v *Vault) awaitClient(ctx context.Context, reason string) (tracked bool, err error) {
	v.mu.Lock()
	if !v.clientsKnown {
		v.mu.Unlock()
		return false, nil
	}
	if v.clients > 0 {
		v.mu.Unlock()
		return true, nil
	}
	if v.clientWait == nil {
		v.clientWait = make(chan struct{})
	}
	wait := v.clientWait
	limit := v.unlockSuspension
	if limit <= 0 {
		limit = DefaultUnlockSuspension
	}
	v.mu.Unlock()

	v.logger.Info("vault unlock suspended: no client is attached to show the prompt",
		"reason", reason, "resumesWhen", "a client attaches", "expiresIn", limit)

	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-wait:
		v.logger.Info("vault unlock resuming: a client attached", "reason", reason)
		return true, nil
	case <-ctx.Done():
		return true, ctx.Err()
	case <-timer.C:
		v.logger.Warn("vault unlock suspension expired with no client",
			"reason", reason, "waited", limit)
		return true, ErrUnlockSuspended
	}
}
