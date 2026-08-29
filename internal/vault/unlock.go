package vault

// The vault raises its own unlock, and exactly one prompt serves every
// waiting caller.
//
// A sealed vault cannot answer for a secret. Whoever needs the vault open
// must raise the unlock prompt; the transport's RequestUnlock delivers one
// prompt to whichever renderer is there (AD-9 reconnect, several clients,
// first answer wins). But the transport registers a fresh ask per call and
// broadcasts its own vault.unlockRequest each time, so N callers meeting a
// sealed vault at once — eight restored ssh panes reconnecting on startup,
// say, when the vault is always sealed right after a start — would produce
// N prompts with N distinct request ids. Nobody has seen it because today
// the unlock is raised by one user action at a time; the moment the vault
// becomes its own producer it is the default case, not the corner.
//
// The deduplication therefore lives HERE, not in the transport: the vault
// holds the state "I am sealed and one unlock is already pending", raises
// ONE prompt through UnlockRequester when it finds itself sealed, and every
// caller that arrives while that prompt is outstanding JOINS it instead of
// raising a second. One answer resolves all of them — unlocked, everyone
// gets their data; not unlocked, everyone gets the same error. The
// transport keeps exactly what it is good for: delivering one prompt.
//
// The interface is deliberately declared here rather than imported from
// internal/transport — the transport already imports the vault, so a second
// declaration of the same shape is the only form that does not cycle, and
// *transport.WSServer satisfies it structurally (the same translation the
// composition root already performs between identically-named seams, e.g.
// remoteLauncherAdapter).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// UnlockRequester lets the vault ask a renderer to show the unlock dialog.
// A single method behind an interface (AD-8), wired at the one composition
// root. *transport.WSServer satisfies it: RequestUnlock broadcasts a
// vault.unlockRequest notification to connected clients and blocks until
// one resolves it via vault.unlockResolved or the context is done.
//
// The returned error is the caller-visible outcome and is passed through to
// every waiter verbatim — ErrNoClientConnected ("no renderer is attached"),
// ErrUnlockCancelled ("the user dismissed the dialog") and context errors
// all reach the caller exactly as they do from the transport today.
type UnlockRequester interface {
	RequestUnlock(ctx context.Context, reason string) error
}

// SetUnlockRequester attaches the renderer-facing prompt carrier. Called
// once at the composition root, after the transport is built; a vault
// without a requester answers a sealed call with ErrVaultSealed, exactly as
// it always has, because there is no one to ask.
func (v *Vault) SetUnlockRequester(req UnlockRequester) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.unlockReq = req
}

// EnsureUnsealed makes sure the vault can answer for secrets, raising the
// unlock prompt when it cannot.
//
//   - StateUnsealed: returns nil immediately — no prompt.
//   - StateUninitialized: returns ErrVaultUninitialized — there is nothing
//     to unlock; setup is a different flow with its own surface.
//   - StateSealed with no requester attached: returns ErrVaultSealed — the
//     exact answer such a caller has always seen.
//   - StateSealed with a requester: the first caller to arrive becomes the
//     leader and raises ONE prompt; every caller that arrives while it is
//     outstanding joins the same prompt rather than raising a second. One
//     resolution answers all of them.
//
// The reason is composed at raise time from every waiter joined at that
// instant — the count and each caller's sentence — never just the first
// caller's. A caller joining after the prompt is already on the wire is
// still resolved by the same answer; only the dialog text, which was
// broadcast with the waiters known then, cannot be rewritten.
//
// The prompt runs on the vault's own context, not the leader's: a caller
// whose context is cancelled while others still wait is released with its
// own error, and the prompt keeps serving the rest. The prompt's lifetime
// is the vault's — Close cancels it, releasing every waiter with
// context.Canceled and dropping the transport's pending ask.
func (v *Vault) EnsureUnsealed(ctx context.Context, reason string) error {
	v.mu.Lock()
	switch v.stateLocked() {
	case StateUnsealed:
		v.mu.Unlock()
		return nil
	case StateUninitialized:
		v.mu.Unlock()
		return ErrVaultUninitialized
	}
	req := v.unlockReq
	if req == nil {
		v.mu.Unlock()
		return ErrVaultSealed
	}
	if p := v.unlockPending; p != nil {
		p.join(reason)
		v.mu.Unlock()
		return p.wait(ctx)
	}
	pctx := v.promptCtx
	p := &unlockPrompt{done: make(chan struct{})}
	p.join(reason)
	v.unlockPending = p
	v.mu.Unlock()

	// One prompt per pending state. The snapshot of the waiter set happens
	// under the vault lock, so every caller that joined before this point is
	// named in the dialog; the ask itself runs outside any lock.
	//
	// THE PENDING STATE IS RETIRED BEFORE THE ANSWER IS FANNED OUT, and the
	// order used to be the other way round. `resolve` releases every waiter,
	// so the caller that asked is back in its own code — and able to make a
	// second request — while `unlockPending` still pointed at a prompt that
	// was already over. That second request joined the dead prompt and was
	// handed the FIRST one's answer without anything being raised: a person
	// who dismissed the unlock and immediately did something else that needs
	// the vault got silence, which is the defect
	// TestEnsureUnsealed_SecondUserActionRaisesFreshPrompt exists to catch.
	// It reproduces under load at count=400 and reported on CI as a 5.00 s
	// "the second prompt was never raised".
	//
	// Retiring first keeps what the old order was protecting. A caller that
	// arrives while the ask is still up still finds the pending prompt and
	// joins it, which is the case that must not raise a second dialog. A
	// caller that arrives after `RequestUnlock` has returned is a new user
	// action against a vault that is still sealed, and it SHOULD raise a
	// fresh prompt — telling those two apart is what the pointer is for, and
	// leaving it set past the resolution is what stopped it doing that.
	go func() {
		err := v.raiseUnlock(pctx, req, p)
		v.mu.Lock()
		if v.unlockPending == p {
			v.unlockPending = nil
		}
		v.mu.Unlock()
		if h := v.beforeResolve; h != nil {
			h()
		}
		p.resolve(err)
	}()

	return p.wait(ctx)
}

// unlockPrompt is one outstanding unlock ask and the waiters joined to it.
// It broadcasts ONE resolution to every waiter: done is closed exactly once
// and the outcome is stored under mu. A plain buffered channel would hand
// the answer to one waiter and leave the rest blocked — the exact defect
// this seam exists to prevent.
type unlockPrompt struct {
	mu      sync.Mutex
	reasons []string // every joined waiter's sentence, in join order
	done    chan struct{}
	err     error
	once    sync.Once
}

// join records one more waiting caller. Called under the vault lock, with
// the resolution applied in two steps — done is closed first, the pending
// marker cleared second — so there is no lost-caller interleaving: a caller
// that joins before the close receives the resolution; one that joins
// between the close and the clear receives it immediately (done is already
// closed); one that arrives after the clear starts a new prompt or answers
// from the vault's state. There is no third outcome.
func (p *unlockPrompt) join(reason string) {
	p.mu.Lock()
	p.reasons = append(p.reasons, reason)
	p.mu.Unlock()
}

// reason composes the dialog text: every waiting caller known at raise
// time, never just the first. One caller keeps its own sentence; several
// become a count plus each caller's sentence.
func (p *unlockPrompt) reason() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch n := len(p.reasons); n {
	case 0:
		return ""
	case 1:
		return p.reasons[0]
	default:
		return fmt.Sprintf("%d operations need the vault: %s", n, strings.Join(p.reasons, "; "))
	}
}

// resolve releases every waiter with the same outcome. Only the leader
// goroutine ever calls it, and once guards the close regardless.
func (p *unlockPrompt) resolve(err error) {
	p.once.Do(func() {
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		close(p.done)
	})
}

// wait blocks until the prompt is answered or the caller's own context
// ends. A caller that gives up — its own cancel or deadline — is released
// with its own error; the prompt keeps running for everyone else.
func (p *unlockPrompt) wait(ctx context.Context) error {
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// raiseUnlock puts ONE prompt in front of a person, waiting for a person to
// be there.
//
// A detached coordinator cannot ask anybody. ADR-0032 is explicit that the
// dialog is renderer-owned — there is no daemon-native prompt to fall back
// on — so the only honest options are to fail or to suspend, and D9 chooses
// to suspend: the operation is held, the reason is recorded, and the prompt
// goes out the moment a client attaches.
//
// The loop exists for exactly two states, and no others:
//
//   - nobody is attached: awaitClient suspends until somebody is, the
//     caller's context ends, or the suspension ceiling is reached.
//   - somebody was attached when we looked and had gone by the time the
//     broadcast happened: the transport answers ErrNoUnlockClient, which is
//     the same sentinel this package declares, and we suspend again.
//
// And it does not loop at all on a vault nobody reports presence to. Such a
// vault has no attach to wait for, so a suspension would be a hang; it
// raises once and returns whatever came back, which is what every caller saw
// before D9.
//
// Every other outcome — unsealed, cancelled by the person, the caller's
// context — is the answer and is returned verbatim to every joined waiter.
// In particular a dismissal is NOT retried: a person who said no has
// answered, and asking again would be the dialog loop nocx-25k9.20 bought.
//
// WHAT HAPPENS WHEN A CLIENT ATTACHES WHILE AN ASK IS PENDING. Nothing, if
// the ask is still on the wire to a client that can answer it — the prompt
// is one dialog and it already exists. If the last client left while the ask
// was outstanding, ClientsAttached(0) abandoned the ATTEMPT (presence.go),
// so this loop is back in awaitClient and the attach raises a fresh
// broadcast for the SAME prompt: still one dialog, now addressed to somebody
// who can see it, and still naming every operation joined to it.
func (v *Vault) raiseUnlock(ctx context.Context, req UnlockRequester, p *unlockPrompt) error {
	for {
		v.mu.Lock()
		reason := p.reason()
		v.mu.Unlock()

		tracked, err := v.awaitClient(ctx, reason)
		if err != nil {
			return err
		}

		actx, cancel := context.WithCancel(ctx)
		v.mu.Lock()
		v.unlockAttempt = cancel
		v.unlockAttemptAbandoned = false
		v.mu.Unlock()

		err = req.RequestUnlock(actx, reason)

		v.mu.Lock()
		abandoned := v.unlockAttemptAbandoned
		v.unlockAttempt = nil
		v.unlockAttemptAbandoned = false
		v.mu.Unlock()
		cancel()

		if !tracked {
			// Nobody reports presence to this vault, so there is no attach
			// to wait for and no second chance to take. One raise, one
			// answer — what every caller saw before D9.
			return err
		}
		switch {
		case abandoned:
			// The last client detached under this ask. Suspend and re-raise.
		case errors.Is(err, ErrNoUnlockClient):
			// The client left between the check and the broadcast.
		default:
			return err
		}
	}
}
