package vault

// D9, asserted on the vault's own state.
//
// Every check here reads rootKey or State directly rather than a log line or
// a process exit, because the claim is about what is in memory: the root key
// is absent from the moment the last client detaches until a client is
// attached AND a person unlocks. Both ends of that interval are named, and
// the middle is sampled — nothing between them may put key material back.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/waittest"
)

// rootKeyPresent reads the one piece of state the whole invariant is about.
func rootKeyPresent(v *Vault) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.rootKey != nil
}

// unsealedVaultWithClient returns a vault that is set up, unsealed and has
// one client attached — the state a person is in while looking at a window.
func unsealedVaultWithClient(t *testing.T) *Vault {
	t.Helper()
	loweredCost(t)
	v, _, _ := testVault(t, newTestFileProvider(ProviderFile))
	mustSetup(t, v, "hunter2")
	v.ClientsAttached(1)
	if !rootKeyPresent(v) {
		t.Fatal("setup left the vault sealed; the interval under test cannot start")
	}
	return v
}

// The opening event of the invariant.
func TestClientsAttached_LastDetachSealsTheVault(t *testing.T) {
	v := unsealedVaultWithClient(t)

	v.ClientsAttached(0)

	if rootKeyPresent(v) {
		t.Error("the root key is still in memory after the last client detached")
	}
	if got := v.State(); got != StateSealed {
		t.Errorf("state = %v, want sealed", got)
	}
}

// One client of two leaving is not the last one leaving. Without this the
// rule would be "seal whenever anything disconnects", which would shut the
// vault under the person still using it.
func TestClientsAttached_ASecondWindowClosingDoesNotSeal(t *testing.T) {
	v := unsealedVaultWithClient(t)
	v.ClientsAttached(2)

	v.ClientsAttached(1)

	if !rootKeyPresent(v) {
		t.Error("the vault sealed while a client was still attached")
	}
}

// The closing event, and what may NOT close it. Attaching a client does not
// unseal — only a person unlocking does — so the interval is sampled across
// an attach, an activity signal and a read attempt before the unseal.
func TestRootKeyIsAbsentFromLastDetachUntilAClientUnlocks(t *testing.T) {
	v := unsealedVaultWithClient(t)
	id, err := v.Create(context.Background(), credential.NewSecret("s3cret"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	v.ClientsAttached(0) // interval opens
	if rootKeyPresent(v) {
		t.Fatal("the root key survived the last detach")
	}

	// Nothing in the middle may reopen it.
	v.ClientsAttached(1)
	if rootKeyPresent(v) {
		t.Error("a client attaching unsealed the vault; only a person unlocking may")
	}
	v.Activity()
	if rootKeyPresent(v) {
		t.Error("an activity signal unsealed the vault")
	}
	if _, err := v.Get(context.Background(), id); !errors.Is(err, ErrVaultSealed) {
		t.Errorf("Get on the sealed vault = %v, want ErrVaultSealed", err)
	}
	if rootKeyPresent(v) {
		t.Error("a read attempt unsealed the vault")
	}

	// The closing event, and nothing else.
	if err := v.Unseal(context.Background(), UnsealRequest{Passphrase: "hunter2"}); err != nil {
		t.Fatalf("Unseal after reattach: %v", err)
	}
	if !rootKeyPresent(v) {
		t.Fatal("the interval never closed: unsealing after reattach left no root key")
	}
	// And the vault is usable again — sealing is not a one-way door.
	got, err := v.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get after the reattach unlock: %v", err)
	}
	if err := got.Use(func(b []byte) error {
		if string(b) != "s3cret" {
			t.Errorf("secret = %q, want %q", b, "s3cret")
		}
		return nil
	}); err != nil {
		t.Fatalf("Use: %v", err)
	}
}

// The detach/attach cycle is repeatable. A one-way door would pass the two
// checks above and still leave a person unable to work after their second
// window close.
func TestClientsAttached_SealAndUnlockCycleRepeats(t *testing.T) {
	v := unsealedVaultWithClient(t)

	for i := range 3 {
		v.ClientsAttached(0)
		if rootKeyPresent(v) {
			t.Fatalf("cycle %d: detach did not seal", i)
		}
		v.ClientsAttached(1)
		if err := v.Unseal(context.Background(), UnsealRequest{Passphrase: "hunter2"}); err != nil {
			t.Fatalf("cycle %d: Unseal: %v", i, err)
		}
		if !rootKeyPresent(v) {
			t.Fatalf("cycle %d: unseal left the vault sealed", i)
		}
	}
}

// Sealing an already-sealed vault on a detach is a no-op, and a detach on a
// vault nobody ever set up must not invent state.
func TestClientsAttached_DetachOnAnUninitializedVaultIsHarmless(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestFileProvider(ProviderFile))

	v.ClientsAttached(1)
	v.ClientsAttached(0)

	if got := v.State(); got != StateUninitialized {
		t.Errorf("state = %v, want uninitialized", got)
	}
}

// ── the cost, paid in the open ────────────────────────────────────────────

// An operation that needs a secret while nobody is attached SUSPENDS. It
// does not fail, it does not invent a value, and it does not hang: the
// prompt goes out the moment a client attaches, carrying the reason the
// operation gave.
func TestEnsureUnsealed_SuspendsUntilAClientAttaches(t *testing.T) {
	req := &fakeUnlockRequester{entered: make(chan struct{}, 4)}
	v := sealedVault(t, req)
	v.ClientsAttached(0)

	done := make(chan error, 1)
	go func() {
		done <- v.EnsureUnsealed(context.Background(), "the ssh session needs its key")
	}()

	waittest.WaitFor(t, "the operation to suspend on an absent client", func() bool {
		v.mu.Lock()
		defer v.mu.Unlock()
		return v.clientWait != nil
	})
	if n := req.callCount(); n != 0 {
		t.Fatalf("RequestUnlock was called %d times with nobody attached", n)
	}
	select {
	case err := <-done:
		t.Fatalf("the operation failed instead of suspending: %v", err)
	default:
	}

	v.ClientsAttached(1)

	select {
	case <-req.entered:
	case <-time.After(waittest.DefaultTimeout):
		t.Fatal("no prompt was raised after a client attached")
	}
	if err := <-done; err != nil {
		t.Fatalf("EnsureUnsealed after the client returned: %v", err)
	}
	reasons := req.recordedReasons()
	if len(reasons) != 1 || !strings.Contains(reasons[0], "the ssh session needs its key") {
		t.Errorf("the prompt did not carry the waiting operation's reason: %q", reasons)
	}
}

// The suspension has an end. Reaching it is a refusal a person can read, not
// a goroutine that waits forever.
func TestEnsureUnsealed_SuspensionExpiresRatherThanHanging(t *testing.T) {
	req := &fakeUnlockRequester{entered: make(chan struct{}, 4)}
	v := sealedVault(t, req)
	v.ClientsAttached(0)
	v.SetUnlockSuspension(20 * time.Millisecond)

	err := v.EnsureUnsealed(context.Background(), "the ssh session needs its key")

	if !errors.Is(err, ErrUnlockSuspended) {
		t.Fatalf("EnsureUnsealed = %v, want ErrUnlockSuspended", err)
	}
	if n := req.callCount(); n != 0 {
		t.Errorf("a prompt was broadcast to nobody %d times", n)
	}
}

// A caller that gives up while suspended is released with its own error and
// leaves nothing behind — the suspension is not a leak.
func TestEnsureUnsealed_SuspendedCallerHonoursItsOwnContext(t *testing.T) {
	req := &fakeUnlockRequester{entered: make(chan struct{}, 4)}
	v := sealedVault(t, req)
	v.ClientsAttached(0)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- v.EnsureUnsealed(ctx, "a report that changed its mind") }()
	waittest.WaitFor(t, "the operation to suspend", func() bool {
		v.mu.Lock()
		defer v.mu.Unlock()
		return v.clientWait != nil
	})
	cancel()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureUnsealed = %v, want context.Canceled", err)
	}
}

// The failure path of the external call this file depends on: the transport
// answers ErrNoUnlockClient because the client went away between the count
// and the broadcast. The vault suspends and raises again, rather than
// reporting a failure the person never caused.
func TestEnsureUnsealed_ARaceLostToADetachIsRetriedNotReported(t *testing.T) {
	req := &fakeUnlockRequester{
		entered: make(chan struct{}, 4),
		release: make(chan error, 2),
	}
	v := sealedVault(t, req)
	v.ClientsAttached(1)

	done := make(chan error, 1)
	go func() { done <- v.EnsureUnsealed(context.Background(), "a saved connection") }()

	<-req.entered
	req.release <- ErrNoUnlockClient // the transport found nobody

	waittest.WaitFor(t, "a second raise after the lost race", func() bool {
		return req.callCount() >= 2
	})
	req.release <- nil

	if err := <-done; err != nil {
		t.Fatalf("EnsureUnsealed = %v, want the retry to succeed", err)
	}
}

// Any other failure is the answer. A dismissal is a person's decision, and
// re-raising it would be the unlock loop nocx-25k9.20 bought.
func TestEnsureUnsealed_ADismissalIsNotRetried(t *testing.T) {
	req := &fakeUnlockRequester{
		entered: make(chan struct{}, 4),
		release: make(chan error, 1),
	}
	v := sealedVault(t, req)
	v.ClientsAttached(1)

	done := make(chan error, 1)
	go func() { done <- v.EnsureUnsealed(context.Background(), "a saved connection") }()
	<-req.entered
	sentinel := errors.New("unlock cancelled by user")
	req.release <- sentinel

	if err := <-done; !errors.Is(err, sentinel) {
		t.Fatalf("EnsureUnsealed = %v, want the dismissal verbatim", err)
	}
	if n := req.callCount(); n != 1 {
		t.Errorf("the dismissed prompt was raised %d times, want 1", n)
	}
}

// A prompt already on the wire when the last client leaves is addressed to
// nobody: the notification went to connections that no longer exist. The
// ATTEMPT is abandoned and re-raised when somebody returns; the PROMPT is
// the same one, so the returning person sees one dialog, not two.
func TestEnsureUnsealed_APromptOutstandingWhenTheLastClientLeavesIsReRaised(t *testing.T) {
	req := &fakeUnlockRequester{
		entered: make(chan struct{}, 4),
		release: make(chan error, 2),
	}
	v := sealedVault(t, req)
	v.ClientsAttached(1)

	done := make(chan error, 1)
	go func() { done <- v.EnsureUnsealed(context.Background(), "the ssh session needs its key") }()
	<-req.entered

	var prompt *unlockPrompt
	v.mu.Lock()
	prompt = v.unlockPending
	v.mu.Unlock()
	if prompt == nil {
		t.Fatal("no prompt outstanding after the first raise")
	}

	v.ClientsAttached(0) // the window closed under the dialog

	waittest.WaitFor(t, "the abandoned attempt to suspend again", func() bool {
		v.mu.Lock()
		defer v.mu.Unlock()
		return v.clientWait != nil
	})
	v.mu.Lock()
	same := v.unlockPending == prompt
	v.mu.Unlock()
	if !same {
		t.Error("the prompt itself was discarded; the returning person would see a second dialog")
	}

	v.ClientsAttached(1)
	waittest.WaitFor(t, "the prompt to be raised again for the returning client", func() bool {
		return req.callCount() >= 2
	})
	req.release <- nil

	if err := <-done; err != nil {
		t.Fatalf("EnsureUnsealed = %v, want the re-raised prompt to answer it", err)
	}
	for _, r := range req.recordedReasons() {
		if !strings.Contains(r, "the ssh session needs its key") {
			t.Errorf("a raise lost the waiting operation's reason: %q", r)
		}
	}
}

// Several suspended operations share one prompt when the client returns —
// the coalescing the vault already owns must survive the suspension.
func TestEnsureUnsealed_SuspendedOperationsShareOnePromptOnReturn(t *testing.T) {
	req := &fakeUnlockRequester{
		entered: make(chan struct{}, 4),
		release: make(chan error, 1),
	}
	v := sealedVault(t, req)
	v.ClientsAttached(0)

	const callers = 3
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- v.EnsureUnsealed(context.Background(), reasonFor(i))
		}()
	}
	waitForJoined(t, v, callers)

	v.ClientsAttached(1)
	<-req.entered
	req.release <- nil
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("a joined caller got %v, want nil", err)
		}
	}
	if n := req.callCount(); n != 1 {
		t.Errorf("%d prompts were raised for %d suspended operations, want 1", n, callers)
	}
}

func reasonFor(i int) string {
	return "operation " + string(rune('a'+i)) + " needs the vault"
}

// A vault nobody reports presence to keeps exactly the behaviour it had
// before D9: one raise, and ErrNoUnlockClient is the answer.
//
// This is the case every package that builds a vault of its own is in, and
// the reason presence is a tri-state rather than a count. Reading the zero
// count as "nobody is attached" made eight coalescing tests in this package
// and eight over-the-socket tests in internal/transport hang for their full
// timeout, because a suspension has nothing to wait for when nothing will
// ever report an attach.
func TestEnsureUnsealed_AVaultNobodyReportsPresenceToDoesNotSuspend(t *testing.T) {
	req := &fakeUnlockRequester{
		entered: make(chan struct{}, 4),
		release: make(chan error, 1),
	}
	v := sealedVault(t, req) // no ClientsAttached call, ever
	req.release <- ErrNoUnlockClient

	err := v.EnsureUnsealed(context.Background(), "an operation on an unwatched vault")

	if !errors.Is(err, ErrNoUnlockClient) {
		t.Fatalf("EnsureUnsealed = %v, want ErrNoUnlockClient returned rather than a suspension", err)
	}
	if n := req.callCount(); n != 1 {
		t.Errorf("the prompt was raised %d times, want exactly 1", n)
	}
}

// And the moment presence IS reported, the same vault suspends. The two
// halves are what make the tri-state a decision rather than a default.
func TestEnsureUnsealed_ReportingPresenceIsWhatTurnsSuspensionOn(t *testing.T) {
	req := &fakeUnlockRequester{entered: make(chan struct{}, 4)}
	v := sealedVault(t, req)
	v.ClientsAttached(0) // "presence is reported, and there is nobody"

	done := make(chan error, 1)
	go func() { done <- v.EnsureUnsealed(context.Background(), "an operation on a watched vault") }()

	waittest.WaitFor(t, "the operation to suspend once presence is reported", func() bool {
		v.mu.Lock()
		defer v.mu.Unlock()
		return v.clientWait != nil
	})
	select {
	case err := <-done:
		t.Fatalf("the operation answered %v instead of suspending", err)
	default:
	}
	v.ClientsAttached(1)
	if err := <-done; err != nil {
		t.Fatalf("EnsureUnsealed after the client attached: %v", err)
	}
}
