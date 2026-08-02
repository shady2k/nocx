package system_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/system"
	"github.com/shady2k/nocx/internal/vault/vaulttest"
)

// TestContract runs the shared provider contract against a fake keyring. The
// contract must pass on every platform because CI has no Secret Service.
func TestContract(t *testing.T) {
	vaulttest.RunProviderContract(t, "system", func(t *testing.T) vault.WritableProvider {
		return system.New(system.WithKeyring(newMemKeyring()))
	})
}

// TestProbeWithFakeKeyring verifies that Probe succeeds with an injected fake
// keyring, regardless of whether a Secret Service daemon is available on this
// machine.
func TestProbeWithFakeKeyring(t *testing.T) {
	kr := newMemKeyring()
	p := system.New(system.WithKeyring(kr))
	ctx := context.Background()
	status := p.Probe(ctx)
	if !status.Ready {
		t.Fatalf("Probe with fake keyring: Ready=false, Reason=%q", status.Reason)
	}
}

// TestProbe_LockedKeyring verifies that Probe returns ReasonLocked when the
// keyring is locked (Set fails with a "locked" error). A locked keychain must
// not be reported as "no-service" — the renderer shows different copy for each.
func TestProbe_LockedKeyring(t *testing.T) {
	kr := lockedKeyring{}
	p := system.New(system.WithKeyring(kr))
	ctx := context.Background()
	status := p.Probe(ctx)
	if status.Ready {
		t.Fatal("Probe with locked keyring: Ready=true, want false")
	}
	if status.Reason != vault.ReasonLocked {
		t.Fatalf("Probe Reason = %q, want %q", status.Reason, vault.ReasonLocked)
	}
}

// lockedKeyring is a Keyring that always returns errors containing "locked" on
// Set, simulating an OS keychain whose collection is locked.
type lockedKeyring struct{}

func (lockedKeyring) Set(service, user, password string) error {
	return errors.New("secret service: collection is locked")
}

func (lockedKeyring) Get(service, user string) (string, error) {
	return "", errors.New("secret service: collection is locked")
}

func (lockedKeyring) Delete(service, user string) error {
	return errors.New("secret service: collection is locked")
}

func (lockedKeyring) DeleteAll(service string) error {
	return errors.New("secret service: collection is locked")
}

// goKeyringLockedMessage is the error zalando/go-keyring actually returns when
// the Secret Service collection it writes to is locked and the daemon cannot
// self-unlock (secret_service.Unlock, v0.2.8). Note what the text does not
// contain: the word "locked". That is why classifyReason's string matching fell
// through to its default and reported a running-but-locked keyring as
// "no-service" (nocx-25k9.6).
const goKeyringLockedMessage = `failed to unlock correct collection '/org/freedesktop/secrets/aliases/default'`

// TestProbe_UnhelpfulErrorAsksThePlatform pins the defect and its fix: when the
// keyring error names no cause, the reason must come from observing the secret
// store, not from guessing "no service". Each case is a real machine state.
func TestProbe_UnhelpfulErrorAsksThePlatform(t *testing.T) {
	tests := []struct {
		name    string
		observe vault.Reason
		want    vault.Reason
	}{
		{
			// The bug: a daemon owns org.freedesktop.secrets and its
			// collection is locked. Telling the user to install a keyring
			// they are already running is the misdiagnosis the reason codes
			// exist to prevent.
			name:    "locked collection is reported as locked",
			observe: vault.ReasonLocked,
			want:    vault.ReasonLocked,
		},
		{
			name:    "present but uninterrogable is reported as denied",
			observe: vault.ReasonDenied,
			want:    vault.ReasonDenied,
		},
		{
			// A genuinely absent service must still read as absent.
			name:    "no service is still reported as no-service",
			observe: vault.ReasonNoService,
			want:    vault.ReasonNoService,
		},
		{
			// Off Linux there is nothing to interrogate; the probe abstains
			// and the historical default stands.
			name:    "an abstaining probe leaves the default in place",
			observe: "",
			want:    vault.ReasonNoService,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := system.New(
				system.WithKeyring(failingKeyring{err: errors.New(goKeyringLockedMessage)}),
				system.WithReasonProbe(func() vault.Reason { return tt.observe }),
			)
			status := p.Probe(context.Background())
			if status.Ready {
				t.Fatal("Probe with a failing keyring: Ready=true, want false")
			}
			if status.Reason != tt.want {
				t.Fatalf("Probe Reason = %q, want %q", status.Reason, tt.want)
			}
		})
	}
}

// TestProbe_ErrorTextBeatsTheProbeWhenItNamesTheCause verifies the platform
// probe is a fallback, not an override: an error that says what went wrong is
// believed, so a macOS/Windows backend keeps working unchanged.
func TestProbe_ErrorTextBeatsTheProbeWhenItNamesTheCause(t *testing.T) {
	p := system.New(
		system.WithKeyring(lockedKeyring{}),
		// An observation that disagrees. The error text is specific; take it.
		system.WithReasonProbe(func() vault.Reason { return vault.ReasonNoService }),
	)
	status := p.Probe(context.Background())
	if status.Reason != vault.ReasonLocked {
		t.Fatalf("Probe Reason = %q, want %q", status.Reason, vault.ReasonLocked)
	}
}

// failingKeyring is a Keyring whose every operation fails with err.
type failingKeyring struct{ err error }

func (k failingKeyring) Set(service, user, password string) error { return k.err }

func (k failingKeyring) Get(service, user string) (string, error) { return "", k.err }

func (k failingKeyring) Delete(service, user string) error { return k.err }
func (k failingKeyring) DeleteAll(service string) error    { return k.err }

// TestTimeoutWriteStillLands verifies that a Put that times out still
// completes in the background, and that the value is readable afterwards.
//
// This is the behaviour described on WithTimeout: the timeout bounds waiting,
// it does NOT cancel the underlying operation.
func TestTimeoutWriteStillLands(t *testing.T) {
	kr := newBlockingKeyring()
	p := system.New(system.WithKeyring(kr), system.WithTimeout(50*time.Millisecond))

	ctx := context.Background()
	id, err := vault.MintReferenceForTest(vault.ProviderSystem)
	if err != nil {
		t.Fatalf("MintReferenceForTest: %v", err)
	}

	// Put with a blocking Set — should time out.
	err = p.Put(ctx, id, credential.NewSecret("delayed"))
	var pe *vault.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("Put error = %T(%[1]v), want *ProviderError", err)
	}
	if pe.Reason != vault.ReasonTimeout {
		t.Fatalf("Put Reason = %q, want ReasonTimeout", pe.Reason)
	}
	if !errors.Is(err, vault.ErrProviderUnavailable) {
		t.Fatalf("Put error must wrap ErrProviderUnavailable")
	}

	// Unblock the background Set goroutine and wait for it to complete.
	kr.unblockSet()
	kr.waitForSetDone()

	// The value should now be stored. Read it back through the provider.
	got, err := p.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after timed-out Put: %v", err)
	}
	var gotStr string
	if err := got.Use(func(b []byte) error { gotStr = string(b); return nil }); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if gotStr != "delayed" {
		t.Fatalf("got %q, want delayed", gotStr)
	}
}

// TestDeleteAbsentIsIdempotent verifies that deleting a never-stored key is
// not an error — covering the path where the keyring returns ErrNotFound.
func TestDeleteAbsentIsIdempotent(t *testing.T) {
	kr := newMemKeyring()
	p := system.New(system.WithKeyring(kr))
	ctx := context.Background()

	id, err := vault.MintReferenceForTest(vault.ProviderSystem)
	if err != nil {
		t.Fatalf("MintReferenceForTest: %v", err)
	}
	if err := p.Delete(ctx, id); err != nil {
		t.Fatalf("Delete(absent) = %v, want nil", err)
	}
}

// TestGetNotFound verifies that Get of a never-stored key returns
// ErrSecretNotFound, wrapping the correct sentinel.
func TestGetNotFound(t *testing.T) {
	kr := newMemKeyring()
	p := system.New(system.WithKeyring(kr))
	ctx := context.Background()

	id, err := vault.MintReferenceForTest(vault.ProviderSystem)
	if err != nil {
		t.Fatalf("MintReferenceForTest: %v", err)
	}
	_, err = p.Get(ctx, id)
	if !errors.Is(err, vault.ErrSecretNotFound) {
		t.Fatalf("Get(absent) = %v, want ErrSecretNotFound", err)
	}
}

// --- test keyrings ---

// memKeyring is an in-memory Keyring for tests. Missing keys return an error
// wrapping keyring.ErrNotFound.
type memKeyring struct {
	mu    sync.Mutex
	store map[string]string
}

func newMemKeyring() *memKeyring {
	return &memKeyring{store: make(map[string]string)}
}

func (k *memKeyring) Set(service, user, password string) error {
	k.mu.Lock()
	k.store[key(service, user)] = password
	k.mu.Unlock()
	return nil
}

func (k *memKeyring) Get(service, user string) (string, error) {
	k.mu.Lock()
	v, ok := k.store[key(service, user)]
	k.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("memKeyring: %w", keyring.ErrNotFound)
	}
	return v, nil
}

func (k *memKeyring) Delete(service, user string) error {
	k.mu.Lock()
	delete(k.store, key(service, user))
	k.mu.Unlock()
	return nil
}

func (k *memKeyring) DeleteAll(service string) error {
	k.mu.Lock()
	for stored := range k.store {
		if strings.HasPrefix(stored, service+".") {
			delete(k.store, stored)
		}
	}
	k.mu.Unlock()
	return nil
}

func key(service, user string) string { return service + "." + user }

// blockingKeyring blocks Set until unblockSet is called. Used to test timeout
// behaviour.
type blockingKeyring struct {
	mu       sync.Mutex
	store    map[string]string
	setBlock chan struct{}
	setDone  chan struct{}
	doneOnce sync.Once
}

func newBlockingKeyring() *blockingKeyring {
	return &blockingKeyring{
		store:    make(map[string]string),
		setBlock: make(chan struct{}),
		setDone:  make(chan struct{}),
	}
}

func (k *blockingKeyring) Set(service, user, password string) error {
	<-k.setBlock
	k.mu.Lock()
	k.store[key(service, user)] = password
	k.mu.Unlock()
	k.doneOnce.Do(func() { close(k.setDone) })
	return nil
}

func (k *blockingKeyring) Get(service, user string) (string, error) {
	k.mu.Lock()
	v, ok := k.store[key(service, user)]
	k.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("blockingKeyring: %w", keyring.ErrNotFound)
	}
	return v, nil
}

func (k *blockingKeyring) Delete(service, user string) error {
	k.mu.Lock()
	delete(k.store, key(service, user))
	k.mu.Unlock()
	return nil
}

func (k *blockingKeyring) DeleteAll(service string) error {
	k.mu.Lock()
	for stored := range k.store {
		if strings.HasPrefix(stored, service+".") {
			delete(k.store, stored)
		}
	}
	k.mu.Unlock()
	return nil
}

func (k *blockingKeyring) unblockSet()     { close(k.setBlock) }
func (k *blockingKeyring) waitForSetDone() { <-k.setDone }

// --- PurgeAll ---

// PurgeAll is how a vault reset removes what nocx put in the OS keychain.
//
// It has to be a bulk delete by service rather than a walk over known ids,
// because the keyring exposes no enumeration on any platform: an entry whose
// reference was lost earlier cannot be discovered, so a walk silently leaves
// it behind — as plaintext, since this provider stores plaintext. Bulk delete
// is the only operation that can be complete.
func TestProvider_PurgeAll_RemovesEveryEntryUnderOurService(t *testing.T) {
	kr := newMemKeyring()
	p := system.New(system.WithKeyring(kr))
	ctx := context.Background()

	if err := p.Put(ctx, "id-one", credential.NewSecretBytes([]byte("a"))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := p.Put(ctx, "id-two", credential.NewSecretBytes([]byte("b"))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// An entry nothing references any more — exactly the case a walk misses.
	if err := kr.Set("nocx", "orphaned-id", "c"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := p.PurgeAll(ctx); err != nil {
		t.Fatalf("PurgeAll: %v", err)
	}

	for _, id := range []credential.SecretID{"id-one", "id-two"} {
		if _, err := p.Get(ctx, id); err == nil {
			t.Errorf("%s still readable after PurgeAll", id)
		}
	}
	if _, err := kr.Get("nocx", "orphaned-id"); err == nil {
		t.Error("orphaned entry survived PurgeAll")
	}
}

// Another application's entries are not ours to remove. The scope is the
// service name, and it is a constant in this package — never anything a caller
// or the renderer supplies.
func TestProvider_PurgeAll_LeavesOtherServicesAlone(t *testing.T) {
	kr := newMemKeyring()
	p := system.New(system.WithKeyring(kr))
	ctx := context.Background()

	if err := p.Put(ctx, "ours", credential.NewSecretBytes([]byte("a"))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := kr.Set("some-other-app", "theirs", "b"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := p.PurgeAll(ctx); err != nil {
		t.Fatalf("PurgeAll: %v", err)
	}

	if _, err := kr.Get("some-other-app", "theirs"); err != nil {
		t.Errorf("another application's entry was removed: %v", err)
	}
}

// The reset runs while the vault is sealed and the keychain may simply not be
// there — no Secret Service running is an ordinary Linux state. The failure
// has to arrive as an error the caller can report, not a panic or a silent
// success that would let the UI claim everything was removed.
func TestProvider_PurgeAll_ReportsAnUnavailableKeychain(t *testing.T) {
	p := system.New(system.WithKeyring(failingKeyring{err: errors.New("no-service")}))
	if err := p.PurgeAll(context.Background()); err == nil {
		t.Error("PurgeAll returned nil when the keyring failed")
	}
}
