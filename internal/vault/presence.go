package vault

// THE VAULT SEALS WHEN THE LAST CLIENT LEAVES (design D9).
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
// the count changes; nobody attached means nobody can see the vault, and a
// vault nobody can see holds nothing.
//
// A COUNT OF ZERO IS NOT BY ITSELF SOMEBODY LEAVING, and reading it as one
// is the defect this file was rewritten for (nocx-58q7d). The renderer
// reconnects on a dropped socket (AD-9), and a window that reloads tears one
// socket down before it opens the next: both pass through zero with the same
// person sitting in front of the same window. Measured in the e2e stand, a
// `goto('/')` reload sealed a vault 199 ms after it had been set up, and the
// person's next click landed on an unlock sheet nobody had asked for —
// eighteen specs reported it as "ui-prompt-overlay intercepts pointer
// events", which is what an unbidden modal over the whole application looks
// like from outside.
//
// So a detach ARMS a departure and the departure is CONFIRMED only if the
// count is still zero a short window later. Any attach inside the window
// makes the arming stale and nothing is sealed.
//
// WHY THAT IS NOT THE TIMER D9 REFUSES. D9 refuses a timer as the POLICY —
// "seal after N minutes idle" — because idleness is not the fact it cares
// about, and because this vault already has that policy and it belongs to
// somebody else: autoSealLoop, driven by the person's own 0/5/15/30/60
// setting. This window decides nothing. It makes ONE observation reliable,
// because a socket close is not the fact "the person left" and cannot be
// read as one at the instant it happens. The vault still seals if and only
// if the last client left; only the moment it is known moves, and it moves
// by the length of a reload.
//
// THE INVARIANT, WITH BOTH ENDS NAMED. The root key is absent from the
// moment a departure is confirmed until a client is attached AND a person
// unlocks. Attaching does not open it; only Unseal does. The closing event
// is the successful unseal, and nothing between those two points can put key
// material back — Seal wipes the bytes, bumps the generation so any write
// still in flight is rejected, and every later read finds StateSealed.
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

// DefaultDetachWindow is how long the client count must stay at zero before
// the vault reads it as the person having left.
//
// TEN SECONDS, and the number comes from the thing it has to outlast rather
// than from taste. The renderer reconnects with an exponential backoff that
// tops out at 5 s plus up to 50% jitter (frontend/src/dispatcher.ts), so
// 7.5 s is the longest a live renderer can be away between one attempt and
// the next; a reload is two orders of magnitude quicker than that. Ten
// seconds clears the worst reconnect with room and is four orders of
// magnitude short of the exposure D9 was written to close, which is a
// coordinator holding the key for days.
//
// It is a ceiling on how WRONG the observation may be, not a grace a person
// is granted: somebody who really has gone waits ten seconds for their seal,
// and nothing else in the lifecycle moves.
const DefaultDetachWindow = 10 * time.Second

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

// SetDetachWindow replaces the departure window. Same shape and same reason
// as SetUnlockSuspension: the production value is the constant above and
// nothing in a shipped build calls this. Zero confirms a departure
// immediately and synchronously, which is what a test about the SEAL rather
// than about the observation wants.
func (v *Vault) SetDetachWindow(d time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if d < 0 {
		d = 0
	}
	v.detachWindow = d
}

// ClientsAttached reports how many clients are attached to the backend. It
// is called by the transport on every attach and every detach, with the
// count taken after the change, and it is the ONE place the seal-on-departure
// policy lives.
//
// Zero ARMS a departure — see confirmDeparture. Non-zero releases whatever is
// suspended waiting for a client, and nothing else — in particular it does
// not unseal, because a client being present is not a person having
// unlocked.
func (v *Vault) ClientsAttached(n int) {
	v.mu.Lock()
	v.clients = n
	v.clientsKnown = true
	// Every report, attach or detach, makes any arming that is still waiting
	// out its window stale. An attach is a return; a second detach owns its
	// own departure.
	v.presenceEpoch++
	epoch := v.presenceEpoch
	window := v.detachWindow
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
	//
	// This half is NOT windowed, and the asymmetry is the point: whether an
	// ask can still be answered is a fact about the connections it was
	// broadcast to, and those are gone the moment the socket is. Whether the
	// PERSON has left is the question the window exists for.
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

	if window <= 0 {
		v.confirmDeparture(epoch)
		return
	}
	go v.awaitDeparture(epoch, window)
}

// awaitDeparture waits out the window and then asks confirmDeparture whether
// the person actually left.
//
// It runs on the vault's own lifetime (promptCtx), so Close ends it rather
// than leaving a timer running against a vault that is already shut — Close
// seals on its own way out, so an arming that survived it could only seal
// something already sealed and would still hold the Vault alive for the rest
// of the window.
func (v *Vault) awaitDeparture(epoch uint64, window time.Duration) {
	timer := time.NewTimer(window)
	defer timer.Stop()
	select {
	case <-v.promptCtx.Done():
		return
	case <-timer.C:
	}
	v.confirmDeparture(epoch)
}

// confirmDeparture seals the vault if the arming identified by epoch is
// still the current one and nobody is attached.
//
// Sealing happens outside the lock: Seal takes it itself, and calls provider
// Lock after releasing it (ADR-0011 §4).
func (v *Vault) confirmDeparture(epoch uint64) {
	v.mu.Lock()
	confirmed := v.presenceEpoch == epoch && v.clients == 0
	wasUnsealed := v.rootKey != nil
	settled := v.departureSettled
	window := v.detachWindow
	v.mu.Unlock()

	if confirmed {
		v.Seal()
		if wasUnsealed {
			v.logger.Info("vault sealed: the last client left and did not come back",
				"window", window)
		}
	}
	if settled != nil {
		settled(confirmed)
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
// It reads the raw count, not the departure window: whether there is
// somebody to show a dialog TO is answered by the connections that exist
// now, and a client that has gone cannot be shown one merely because the
// vault has not yet concluded it left.
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
