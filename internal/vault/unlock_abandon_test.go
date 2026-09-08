package vault

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/waittest"
)

// A CALLER THAT GAVE UP IS NOT STILL WAITING, AND THE DIALOG MUST NOT SAY IT
// IS (nocx-gx83p).
//
// Measured on a dev stand: four presses of "Check this skill" whose websocket
// dropped before the person answered left the vault raising
//
//	"4 operations need the vault: audit a skill; audit a skill; audit a
//	 skill; audit a skill"
//
// for minutes afterwards, while nothing at all was waiting for it. The prompt
// text is what a person decides on, so a list of ghosts is not cosmetic: it
// asks them to unlock for work that no longer exists.
func TestUnlockPrompt_ACallerThatGivesUpLeavesTheReasonList(t *testing.T) {
	req := &fakeUnlockRequester{entered: make(chan struct{}, 4)}
	v := sealedVault(t, req)
	v.ClientsAttached(0)

	first := make(chan error, 1)
	go func() { first <- v.EnsureUnsealed(context.Background(), "the ssh session needs its key") }()
	waittest.WaitFor(t, "the first operation to suspend", func() bool {
		v.mu.Lock()
		defer v.mu.Unlock()
		return v.clientWait != nil
	})

	// The second joins the prompt the first raised, then its own context
	// ends — the shape of a request whose socket closed.
	gone, abandon := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() { second <- v.EnsureUnsealed(gone, "audit a skill") }()
	waittest.WaitFor(t, "the second operation to join the pending prompt", func() bool {
		v.mu.Lock()
		p := v.unlockPending
		v.mu.Unlock()
		if p == nil {
			return false
		}
		return strings.Contains(p.reason(), "audit a skill")
	})
	abandon()
	if err := <-second; err == nil {
		t.Fatal("the abandoned caller returned success, want its own context error")
	}

	waittest.WaitFor(t, "the abandoned caller's sentence to leave the prompt", func() bool {
		v.mu.Lock()
		p := v.unlockPending
		v.mu.Unlock()
		return p != nil && !strings.Contains(p.reason(), "audit a skill")
	})

	v.ClientsAttached(1)
	if err := <-first; err != nil {
		t.Fatalf("the surviving caller failed: %v", err)
	}
	reasons := req.recordedReasons()
	if len(reasons) != 1 || strings.Contains(reasons[0], "audit a skill") {
		t.Fatalf("the dialog named a caller that had gone: %q", reasons)
	}
}

// AND WHEN THE LAST ONE GOES, THERE IS NOBODY TO ASK FOR.
//
// The raise runs on the vault's own prompt context, not on any caller's, so
// it outlives every one of them by design — that is what lets a suspended
// operation resume when a client comes back. But an operation with no waiter
// left is not suspended, it is over: on the same stand the raise loop went on
// suspending and resuming for minutes after the last press was dead, holding
// the admission `skills.audit` runs under, which is what answered everything
// else with "Control plane busy".
func TestUnlockPrompt_TheRaiseStopsWhenTheLastCallerHasGone(t *testing.T) {
	req := &fakeUnlockRequester{entered: make(chan struct{}, 4)}
	v := sealedVault(t, req)
	v.ClientsAttached(0)

	gone, abandon := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- v.EnsureUnsealed(gone, "audit a skill") }()
	waittest.WaitFor(t, "the operation to suspend on an absent client", func() bool {
		v.mu.Lock()
		defer v.mu.Unlock()
		return v.clientWait != nil
	})

	abandon()
	if err := <-done; err == nil {
		t.Fatal("the abandoned caller returned success, want its own context error")
	}

	// Nothing is waiting now, so the pending prompt must be retired rather
	// than left for the next caller to join.
	waittest.WaitFor(t, "the pending prompt to be retired", func() bool {
		v.mu.Lock()
		defer v.mu.Unlock()
		return v.unlockPending == nil
	})

	// And a client attaching afterwards must not raise a dialog for work
	// that no longer exists.
	v.ClientsAttached(1)
	select {
	case <-req.entered:
		t.Fatal("a prompt was raised for an operation that had already given up")
	case <-time.After(150 * time.Millisecond):
	}
}
