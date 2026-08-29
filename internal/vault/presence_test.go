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
//
// Its departure window is ZERO, which makes a detach seal synchronously.
// That is deliberate and it is what keeps the tests below about the SEAL
// rather than about the observation: "is the count zero because the person
// left" is a separate question with its own tests further down, and mixing
// the two would make every assertion here wait on a timer it is not about.
func unsealedVaultWithClient(t *testing.T) *Vault {
	t.Helper()
	v := unsealedVaultWithClientAndWindow(t, 0)
	return v
}

// unsealedVaultWithClientAndWindow is the same vault with a departure window
// of the caller's choosing — the tests that are about the window itself.
func unsealedVaultWithClientAndWindow(t *testing.T, window time.Duration) *Vault {
	t.Helper()
	loweredCost(t)
	v, _, _ := testVault(t, newTestFileProvider(ProviderFile))
	v.SetDetachWindow(window)
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
	if _, getErr := v.Get(context.Background(), id); !errors.Is(getErr, ErrVaultSealed) {
		t.Errorf("Get on the sealed vault = %v, want ErrVaultSealed", getErr)
	}
	if rootKeyPresent(v) {
		t.Error("a read attempt unsealed the vault")
	}

	// The closing event, and nothing else.
	if unsealErr := v.Unseal(context.Background(), UnsealRequest{Passphrase: "hunter2"}); unsealErr != nil {
		t.Fatalf("Unseal after reattach: %v", unsealErr)
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

// ── what "the last client detached" actually means ────────────────────────
//
// The transport reports a COUNT, and a count of zero is not by itself a
// person leaving: the renderer reconnects on a dropped socket (AD-9), and a
// window that reloads tears one socket down before it opens the next. Both
// pass through zero, and the vault must not read either as a departure.
// Measured in the e2e stand: a `goto('/')` reload sealed a vault 199 ms
// after it had been set up, and the person's next click landed on an unlock
// sheet nobody had asked for (nocx-58q7d).
//
// So a detach ARMS a departure, and the departure is CONFIRMED only if the
// count is still zero when the window elapses. These tests own that window;
// the ones above own the seal it leads to.
//
// None of them waits on a duration. Every armed departure calls
// departureSettled when its window elapses — confirmed or not — so a test
// that must prove the vault did NOT seal waits for that event and then reads
// the state, rather than sleeping longer than the window and hoping.

// departures reports every armed departure whose window has elapsed: true
// when it was confirmed (the vault sealed), false when somebody had come
// back. Buffered deeply enough that no arming can block on a test that is
// not reading yet.
func departures(t *testing.T, v *Vault) <-chan bool {
	t.Helper()
	ch := make(chan bool, 16)
	v.mu.Lock()
	v.departureSettled = func(confirmed bool) { ch <- confirmed }
	v.mu.Unlock()
	return ch
}

// nextDeparture waits for one settled departure. The timeout is a test
// failure mode, not the thing under test — the window itself is milliseconds.
func nextDeparture(t *testing.T, ch <-chan bool) bool {
	t.Helper()
	select {
	case confirmed := <-ch:
		return confirmed
	case <-time.After(10 * time.Second):
		t.Fatal("no departure settled: the arming never elapsed")
		return false
	}
}

// A window reload is not a departure. This is the case the e2e stand
// reproduced: the socket is gone for a fraction of the window and the same
// person is back.
func TestClientsAttached_AReloadIsNotADeparture(t *testing.T) {
	v := unsealedVaultWithClientAndWindow(t, 50*time.Millisecond)
	settled := departures(t, v)

	v.ClientsAttached(0) // the old document's socket closes
	v.ClientsAttached(1) // the new document's socket opens

	if confirmed := nextDeparture(t, settled); confirmed {
		t.Error("the reload was read as a departure")
	}
	if !rootKeyPresent(v) {
		t.Error("a reload sealed the vault; the person never left")
	}
	if got := v.State(); got != StateUnsealed {
		t.Errorf("state = %v, want unsealed", got)
	}
}

// A dropped socket the renderer reconnects after is not a departure either.
// The reconnect is slower than a reload — the dispatcher backs off before it
// retries — so the absence here covers most of the window rather than a
// fraction of it, and still must not seal.
func TestClientsAttached_AReconnectIsNotADeparture(t *testing.T) {
	v := unsealedVaultWithClientAndWindow(t, 400*time.Millisecond)
	settled := departures(t, v)

	v.ClientsAttached(0)
	time.Sleep(200 * time.Millisecond) // the reconnect backoff, half the window
	v.ClientsAttached(1)

	if confirmed := nextDeparture(t, settled); confirmed {
		t.Error("the reconnect was read as a departure")
	}
	if !rootKeyPresent(v) {
		t.Error("a reconnect sealed the vault; the person never left")
	}
}

// And a second window closing arms nothing at all: the count never reached
// zero, so there is no departure to settle and the vault cannot seal.
func TestClientsAttached_ASecondWindowClosingArmsNothing(t *testing.T) {
	v := unsealedVaultWithClientAndWindow(t, 50*time.Millisecond)
	settled := departures(t, v)
	v.ClientsAttached(2)

	v.ClientsAttached(1)

	select {
	case <-settled:
		t.Error("one window of two closing armed a departure")
	case <-time.After(300 * time.Millisecond):
	}
	if !rootKeyPresent(v) {
		t.Error("the vault sealed while a client was still attached")
	}
}

// The departure itself: nobody came back, so the vault seals. Without this
// the window would be a way of never sealing at all, which is the exposure
// D9 was written to close.
func TestClientsAttached_ADepartureSealsWhenTheWindowElapses(t *testing.T) {
	v := unsealedVaultWithClientAndWindow(t, 50*time.Millisecond)
	settled := departures(t, v)

	v.ClientsAttached(0)

	if confirmed := nextDeparture(t, settled); !confirmed {
		t.Fatal("nobody came back and the departure was still not confirmed")
	}
	if rootKeyPresent(v) {
		t.Error("the root key is still in memory after the departure was confirmed")
	}
	if got := v.State(); got != StateSealed {
		t.Errorf("state = %v, want sealed", got)
	}
}

// A person who comes back and then really does leave must still get a seal:
// the second detach arms its own departure, and the first one being made
// stale must not have taken the second with it.
func TestClientsAttached_AReturnFollowedByARealDepartureStillSeals(t *testing.T) {
	v := unsealedVaultWithClientAndWindow(t, 50*time.Millisecond)
	settled := departures(t, v)

	v.ClientsAttached(0)
	v.ClientsAttached(1) // back inside the window — not a departure
	v.ClientsAttached(0) // and now they really go

	// Two armings, and they settle in whichever order their timers fire —
	// the claim is about the outcomes, not the sequence. Exactly one is a
	// confirmed departure: the return ended the first, the leaving is the
	// second.
	confirmations := 0
	for range 2 {
		if nextDeparture(t, settled) {
			confirmations++
		}
	}
	if confirmations != 1 {
		t.Errorf("confirmed departures = %d, want exactly 1 (the return ended the first arming)", confirmations)
	}
	if rootKeyPresent(v) {
		t.Error("the real departure left the root key in memory")
	}
}

// THE INTERVAL, BOTH ENDS, ON THE WINDOWED VAULT. The root key is absent
// from the moment a departure is CONFIRMED — the count has been zero for the
// whole window — until a client is attached AND a person unlocks. The middle
// is sampled: nothing between those two points may put key material back,
// and in particular a client attaching after the seal must not.
func TestRootKeyIsAbsentFromTheConfirmedDepartureUntilAClientUnlocks(t *testing.T) {
	v := unsealedVaultWithClientAndWindow(t, 50*time.Millisecond)
	settled := departures(t, v)
	id, err := v.Create(context.Background(), credential.NewSecret("s3cret"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The opening event is the CONFIRMATION, not the detach: the key is
	// still there when the socket goes, which is the whole difference this
	// change makes and is asserted rather than assumed.
	v.ClientsAttached(0)
	if !rootKeyPresent(v) {
		t.Fatal("the vault sealed before the departure window elapsed")
	}
	if confirmed := nextDeparture(t, settled); !confirmed {
		t.Fatal("the departure was never confirmed; the interval never opened")
	}
	if rootKeyPresent(v) {
		t.Fatal("the confirmed departure left the root key in memory")
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
	if _, getErr := v.Get(context.Background(), id); !errors.Is(getErr, ErrVaultSealed) {
		t.Errorf("Get on the sealed vault = %v, want ErrVaultSealed", getErr)
	}
	if rootKeyPresent(v) {
		t.Error("a read attempt unsealed the vault")
	}

	// The closing event, and nothing else.
	if unsealErr := v.Unseal(context.Background(), UnsealRequest{Passphrase: "hunter2"}); unsealErr != nil {
		t.Fatalf("Unseal after reattach: %v", unsealErr)
	}
	if !rootKeyPresent(v) {
		t.Fatal("the interval never closed: unsealing after reattach left no root key")
	}
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

// Closing a vault with a departure armed must not leave the arming running
// for the rest of the window, and must seal regardless — Close is the
// shutdown path and the root key may not outlive it while a timer runs down.
func TestClientsAttached_CloseEndsAnArmedDeparture(t *testing.T) {
	v := unsealedVaultWithClientAndWindow(t, time.Hour)
	settled := departures(t, v)

	v.ClientsAttached(0)
	v.Close()

	if rootKeyPresent(v) {
		t.Error("Close left the root key in memory")
	}
	select {
	case <-settled:
		t.Error("the arming outlived Close and settled a departure afterwards")
	case <-time.After(200 * time.Millisecond):
	}
}

// The shipped window is a real duration, not zero: a vault built by New must
// not seal on the first blink of a socket. Without this the constant could
// be dropped to zero and every test above would still pass on its own setter.
func TestNewVaultTakesTheShippedDetachWindow(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestFileProvider(ProviderFile))
	v.mu.Lock()
	got := v.detachWindow
	v.mu.Unlock()
	if got != DefaultDetachWindow {
		t.Errorf("a freshly built vault's detach window = %v, want %v", got, DefaultDetachWindow)
	}
	if DefaultDetachWindow <= 0 {
		t.Fatal("the shipped detach window is not positive; a reload would seal the vault")
	}
}
