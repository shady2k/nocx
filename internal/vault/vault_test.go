package vault

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/storage"
)

// ---------------------------------------------------------------------------
// In-memory test providers
// ---------------------------------------------------------------------------

type testProvider struct {
	id      ProviderID
	mu      sync.Mutex
	data    map[credential.SecretID]credential.Secret
	fail    error
	delay   time.Duration
	putHook func() // fires on each Put
	getHook func() // fires on each Get
}

func newTestProvider(id ProviderID) *testProvider {
	return &testProvider{id: id, data: make(map[credential.SecretID]credential.Secret)}
}

func (p *testProvider) ID() ProviderID                  { return p.id }
func (p *testProvider) Status(_ context.Context) Status { return Status{Ready: true} }

func (p *testProvider) Get(_ context.Context, id credential.SecretID) (credential.Secret, error) {
	if p.getHook != nil {
		p.getHook()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail != nil {
		return credential.Secret{}, p.fail
	}
	s, ok := p.data[id]
	if !ok {
		return credential.Secret{}, ErrSecretNotFound
	}
	return s, nil
}

func (p *testProvider) Put(_ context.Context, id credential.SecretID, s credential.Secret) error {
	if p.delay > 0 {
		time.Sleep(p.delay)
	}
	if p.putHook != nil {
		p.putHook()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail != nil {
		return p.fail
	}
	var val []byte
	_ = s.Use(func(b []byte) error { val = make([]byte, len(b)); copy(val, b); return nil })
	p.data[id] = credential.NewSecretBytes(val)
	return nil
}

func (p *testProvider) Delete(_ context.Context, id credential.SecretID) error {
	if p.delay > 0 {
		time.Sleep(p.delay)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail != nil {
		return p.fail
	}
	delete(p.data, id)
	return nil
}

var _ WritableProvider = (*testProvider)(nil)

type panickingProvider struct{ *testProvider }

// readOnlyProvider wraps a Provider to be read-only for testing. It satisfies
// Provider but NOT WritableProvider — the Registry's type assertion fails.
type readOnlyProvider struct {
	id    ProviderID
	inner Provider
}

func (r *readOnlyProvider) ID() ProviderID                    { return r.id }
func (r *readOnlyProvider) Status(ctx context.Context) Status { return r.inner.Status(ctx) }
func (r *readOnlyProvider) Get(ctx context.Context, id credential.SecretID) (credential.Secret, error) {
	return r.inner.Get(ctx, id)
}

var _ Provider = (*readOnlyProvider)(nil)

// unreadyProvider is writable and otherwise functional, but reports itself as
// not ready — used to test capability detection when the system provider exists
// but cannot be written to (e.g. locked keychain).
type unreadyProvider struct{ *testProvider }

func (u *unreadyProvider) Status(_ context.Context) Status { return Status{Ready: false} }

var _ WritableProvider = (*unreadyProvider)(nil)

func (p *panickingProvider) Put(_ context.Context, _ credential.SecretID, _ credential.Secret) error {
	panic("put panicked as designed")
}

type testFileProvider struct {
	*testProvider
	unlocked    atomic.Bool
	instanceID  string
	dataKeyGen  bool
	dataKeyFail error  // injected NewDataKey failure
	unlockHook  func() // fires on each Unlock (blocks for race tests)
}

func newTestFileProvider(id ProviderID) *testFileProvider {
	return &testFileProvider{testProvider: newTestProvider(id)}
}

func (p *testFileProvider) SetInstanceID(id string) { p.instanceID = id }
func (p *testFileProvider) NewDataKey() ([]byte, error) {
	if p.dataKeyFail != nil {
		return nil, p.dataKeyFail
	}
	p.dataKeyGen = true
	return make([]byte, 32), nil
}

func (p *testFileProvider) Unlock(_ []byte) error {
	if p.unlockHook != nil {
		p.unlockHook()
	}
	p.unlocked.Store(true)
	return nil
}
func (p *testFileProvider) Lock() { p.unlocked.Store(false) }

var (
	_ unlocker       = (*testFileProvider)(nil)
	_ locker         = (*testFileProvider)(nil)
	_ dataKeyCreator = (*testFileProvider)(nil)
)

// ---------------------------------------------------------------------------
// Log capture
// ---------------------------------------------------------------------------

type captureHandler struct {
	mu     sync.Mutex
	attrs  []slog.Attr
	msgs   []string // rendered log text
	parent slog.Handler
}

func newCaptureHandler(parent slog.Handler) *captureHandler {
	return &captureHandler{parent: parent}
}

func (h *captureHandler) Enabled(_ context.Context, l slog.Level) bool {
	return h.parent.Enabled(context.TODO(), l)
}

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	// Build rendered text from message + attrs.
	var buf bytes.Buffer
	buf.WriteString(r.Message)
	buf.WriteByte(' ')
	r.Attrs(func(a slog.Attr) bool {
		h.attrs = append(h.attrs, a)
		buf.WriteString(a.Key)
		buf.WriteByte('=')
		buf.WriteString(a.Value.String())
		buf.WriteByte(' ')
		return true
	})
	h.msgs = append(h.msgs, buf.String())
	h.mu.Unlock()
	_ = h.parent.Handle(context.TODO(), r)
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &captureHandler{parent: h.parent.WithAttrs(attrs)}
}

func (h *captureHandler) WithGroup(name string) slog.Handler {
	return &captureHandler{parent: h.parent.WithGroup(name)}
}

func (h *captureHandler) containsPlaintext(s string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, a := range h.attrs {
		if a.Value.Kind() == slog.KindString && a.Value.String() == s {
			return true
		}
	}
	return false
}

func (h *captureHandler) containsPlaintextRendered(s string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.msgs {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Failing document store for test injection
// ---------------------------------------------------------------------------

type failingDocStore struct {
	storage.DocumentStore
	mu          sync.Mutex
	failOnWrite bool
}

func (s *failingDocStore) Write(name string, doc any) error {
	s.mu.Lock()
	fail := s.failOnWrite
	s.mu.Unlock()
	if fail {
		return fmt.Errorf("injected write failure")
	}
	return s.DocumentStore.Write(name, doc)
}

func (s *failingDocStore) Delete(name string) error {
	return nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func testVault(t *testing.T, providers ...Provider) (*Vault, storage.DocumentStore, *captureHandler) {
	t.Helper()
	tmpDir := t.TempDir()
	store := storage.NewDocumentStore(tmpDir)
	reg, err := NewRegistry(providers...)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	capH := newCaptureHandler(logger.Handler())
	logger = slog.New(capH)
	v, err := New(store, reg, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(v.Close)
	return v, store, capH
}

func mustSetup(t *testing.T, v *Vault, passphrase string) SetupResult {
	t.Helper()
	result, err := v.Setup(context.Background(), SetupRequest{Passphrase: passphrase})
	if err != nil {
		t.Fatalf("Setup(%q): %v", passphrase, err)
	}
	return result
}

// ---------------------------------------------------------------------------
// Behaviour 1: Silent setup where the probe succeeds
// ---------------------------------------------------------------------------

func TestSetup_Silent(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	result := mustSetup(t, v, "")
	if result.RecoveryCode != "" {
		t.Fatalf("silent setup returned recovery code %q, want empty", result.RecoveryCode)
	}
	if v.State() != StateUnsealed {
		t.Fatalf("state = %v, want %s", v.State(), StateUnsealed)
	}
	oskID := osKeyID(v.doc.Instance)
	sec, err := sys.Get(context.Background(), oskID)
	if err != nil {
		t.Fatalf("OS key not found: %v", err)
	}
	if sec.IsEmpty() {
		t.Fatal("OS key is empty")
	}
}

func TestSetup_WithPassphrase(t *testing.T) {
	loweredCost(t)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, fp)
	result := mustSetup(t, v, "hunter2")
	if result.RecoveryCode == "" {
		t.Fatal("passphrase setup should return a recovery code")
	}
	if v.State() != StateUnsealed {
		t.Fatalf("state = %v, want %s", v.State(), StateUnsealed)
	}
}

func TestSetup_EmptyPassphraseNoSystemProvider(t *testing.T) {
	loweredCost(t)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, fp)
	_, err := v.Setup(context.Background(), SetupRequest{})
	if err == nil {
		t.Fatal("silent setup without system provider should fail")
	}
}

// ---------------------------------------------------------------------------
// Behaviour 2: Document fields after setup
// ---------------------------------------------------------------------------

func TestSetup_SilentDocumentFields(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, store, _ := testVault(t, sys, fp)
	mustSetup(t, v, "")
	var doc Document
	found, err := store.Read("vault.json", &doc)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !found {
		t.Fatal("document not found")
	}
	if doc.Passphrase != nil {
		t.Fatal("Passphrase should be nil after silent setup")
	}
	if doc.Recovery != nil {
		t.Fatal("Recovery should be nil after silent setup")
	}
	if !doc.HasOSKey {
		t.Fatal("HasOSKey should be true after silent setup")
	}
}

func TestSetup_PassphraseDocumentFields(t *testing.T) {
	loweredCost(t)
	fp := newTestFileProvider(ProviderFile)
	v, store, _ := testVault(t, fp)
	mustSetup(t, v, "hunter2")
	var doc Document
	found, err := store.Read("vault.json", &doc)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !found {
		t.Fatal("document not found")
	}
	if doc.Passphrase == nil {
		t.Fatal("Passphrase should not be nil after passphrase setup")
	}
	if doc.Recovery == nil {
		t.Fatal("Recovery should not be nil after passphrase setup")
	}
	if doc.HasOSKey {
		t.Fatal("HasOSKey should be false after passphrase setup")
	}
}

// ---------------------------------------------------------------------------
// Behaviour 3: One owner — fifty concurrent Creates
// ---------------------------------------------------------------------------

func TestCreate_Concurrent(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "")

	ctx := context.Background()
	const n = 50
	type res struct {
		id  credential.SecretID
		err error
	}
	ch := make(chan res, n)

	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := v.Create(ctx, credential.NewSecret("concurrent-value"))
			ch <- res{id, err}
		}()
	}
	wg.Wait()
	close(ch)

	ids := make([]credential.SecretID, 0, n)
	for r := range ch {
		if r.err != nil {
			t.Fatalf("concurrent Create: %v", r.err)
		}
		ids = append(ids, r.id)
	}

	for _, id := range ids {
		sec, err := v.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		var s string
		_ = sec.Use(func(b []byte) error { s = string(b); return nil })
		if s != "concurrent-value" {
			t.Fatalf("value = %q, want %q", s, "concurrent-value")
		}
	}
}

// ---------------------------------------------------------------------------
// Behaviour 4: Generations
// ---------------------------------------------------------------------------

func TestSeal_RejectsSlowPut(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	slowProv := newTestProvider(ProviderFile)
	slowProv.delay = 100 * time.Millisecond
	v, _, _ := testVault(t, sys, slowProv)
	mustSetup(t, v, "")

	ctx := context.Background()
	v.mu.Lock()
	v.doc.DefaultProvider = ProviderFile
	v.mu.Unlock()

	before := len(v.doc.Journal)
	crCh := make(chan error, 1)
	go func() {
		_, err := v.Create(ctx, credential.NewSecret("slow-value"))
		crCh <- err
	}()

	time.Sleep(10 * time.Millisecond)
	v.Seal()

	err := <-crCh
	if err != ErrVaultSealed {
		t.Fatalf("Create after seal: got %v, want ErrVaultSealed", err)
	}

	after := len(v.doc.Journal)
	if after <= before {
		t.Fatal("journal entry should survive sealing during slow Put")
	}

	for i := before; i < after; i++ {
		e := v.doc.Journal[i]
		if e.Phase == PhaseSecretWritten || e.Phase == PhaseMetadataRepointed {
			t.Fatalf("entry %d unexpected phase %s", i, e.Phase)
		}
	}
}

// ---------------------------------------------------------------------------
// Behaviour 5: Honest limit
// ---------------------------------------------------------------------------

func TestGet_HonestLimit(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "")

	ctx := context.Background()
	id, err := v.Create(ctx, credential.NewSecret("honest-limit-value"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	sec, err := v.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get before seal: %v", err)
	}

	v.Seal()

	// The Secret obtained before seal remains readable (spec §4.5): once
	// bytes are out, the Vault does not own their lifetime.
	var s string
	_ = sec.Use(func(b []byte) error { s = string(b); return nil })
	if s != "honest-limit-value" {
		t.Fatalf("value = %q, want %q", s, "honest-limit-value")
	}
}

// ---------------------------------------------------------------------------
// Behaviour 6: State gating
// ---------------------------------------------------------------------------

func TestState_Transitions(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)

	check := func(want State) {
		t.Helper()
		if got := v.State(); got != want {
			t.Fatalf("state = %v, want %v", got, want)
		}
	}

	check(StateUninitialized)
	mustSetup(t, v, "")
	check(StateUnsealed)
	v.Seal()
	check(StateSealed)

	ctx := context.Background()
	if err := v.Unseal(ctx, UnsealRequest{UseOSKey: true}); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	check(StateUnsealed)
}

func TestStateGating_SecretStore(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	ctx := context.Background()
	validID := credential.SecretID("sec:v1:system:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	t.Run("uninitialized", func(t *testing.T) {
		if v.State() != StateUninitialized {
			t.Fatal("expected uninitialized")
		}
		if _, err := v.Create(ctx, credential.NewSecret("x")); err != ErrVaultUninitialized {
			t.Fatalf("Create: got %v, want ErrVaultUninitialized", err)
		}
		if _, err := v.Get(ctx, validID); err != ErrVaultUninitialized {
			t.Fatalf("Get: got %v, want ErrVaultUninitialized", err)
		}
		if err := v.Delete(ctx, validID); err != ErrVaultUninitialized {
			t.Fatalf("Delete: got %v, want ErrVaultUninitialized", err)
		}
		if _, err := v.Exists(ctx, validID); err != ErrVaultUninitialized {
			t.Fatalf("Exists: got %v, want ErrVaultUninitialized", err)
		}
	})

	mustSetup(t, v, "")

	t.Run("sealed", func(t *testing.T) {
		v.Seal()
		if v.State() != StateSealed {
			t.Fatal("expected sealed")
		}
		if _, err := v.Create(ctx, credential.NewSecret("x")); err != ErrVaultSealed {
			t.Fatalf("Create: got %v, want ErrVaultSealed", err)
		}
		if _, err := v.Get(ctx, validID); err != ErrVaultSealed {
			t.Fatalf("Get: got %v, want ErrVaultSealed", err)
		}
		if err := v.Delete(ctx, validID); err != ErrVaultSealed {
			t.Fatalf("Delete: got %v, want ErrVaultSealed", err)
		}
		if _, err := v.Exists(ctx, validID); err != ErrVaultSealed {
			t.Fatalf("Exists: got %v, want ErrVaultSealed", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Behaviour 7: Journal before delegation
// ---------------------------------------------------------------------------

func TestCreate_JournalBeforePut(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	panicking := &panickingProvider{testProvider: newTestProvider(ProviderFile)}
	v, _, _ := testVault(t, sys, panicking)
	mustSetup(t, v, "")

	v.mu.Lock()
	v.doc.DefaultProvider = ProviderFile
	v.mu.Unlock()

	before := len(v.doc.Journal)
	func() {
		defer func() { _ = recover() }()
		_, _ = v.Create(context.Background(), credential.NewSecret("journal-test"))
	}()
	after := len(v.doc.Journal)
	if after <= before {
		t.Fatal("journal entry should survive provider panic")
	}
}

// ---------------------------------------------------------------------------
// Behaviour 8: No silent fallback
// ---------------------------------------------------------------------------

func TestGet_NoSilentFallback(t *testing.T) {
	loweredCost(t)
	var sysGotCalled atomic.Bool

	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "")

	// Install a Get tracker AFTER setup (setup calls Put on sys, not Get).
	sys.getHook = func() { sysGotCalled.Store(true) }

	ctx := context.Background()
	id := credential.SecretID("sec:v1:unknown:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_, err := v.Get(ctx, id)

	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("want ProviderError, got %T: %v", err, err)
	}
	if pe.Reason != ReasonUnknownProvider {
		t.Fatalf("reason = %q, want %q", pe.Reason, ReasonUnknownProvider)
	}
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal("should wrap ErrProviderUnavailable")
	}
	if sysGotCalled.Load() {
		t.Fatal("default provider was called — silent fallback would be a security hole")
	}
}

// ---------------------------------------------------------------------------
// Behaviour 9: Malformed reference
// ---------------------------------------------------------------------------

func TestGet_MalformedReference(t *testing.T) {
	loweredCost(t)
	var providerCalled atomic.Bool

	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "")

	sys.getHook = func() { providerCalled.Store(true) }
	fp.getHook = func() { providerCalled.Store(true) }

	ctx := context.Background()
	bad := []credential.SecretID{
		"",
		"sec:0123456789abcdef0123456789abcdef",
		"key:v1:file:0123456789abcdef0123456789abcdef",
		"sec:v2:file:0123456789abcdef0123456789abcdef",
		"sec:v1::0123456789abcdef0123456789abcdef",
		"sec:v1:file:",
		"sec:v1:file:0123456789ABCDEF0123456789abcdef",
		"sec:v1:file:0123456789abcdef",
		"sec:v1:file:0123456789abcdef0123456789abcdef00",
		"sec:v1:file:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		"sec:v1:File:0123456789abcdef0123456789abcdef",
	}
	for _, id := range bad {
		_, err := v.Get(ctx, id)
		if err == nil {
			t.Fatalf("Get(%q) succeeded, want error", id)
		}
		if providerCalled.Load() {
			t.Fatal("provider was called despite malformed reference")
		}
	}
}

// ---------------------------------------------------------------------------
// Logging: no plaintext in logs
// ---------------------------------------------------------------------------

func TestCreate_NoPlaintextInLogs(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, capH := testVault(t, sys, fp)
	mustSetup(t, v, "")

	plaintext := "my-supersecret-password-42"
	_, err := v.Create(context.Background(), credential.NewSecret(plaintext))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if capH.containsPlaintext(plaintext) {
		t.Fatal("plaintext secret leaked into log output")
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestDelete_Basic(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "")

	ctx := context.Background()
	id, err := v.Create(ctx, credential.NewSecret("delete-me"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err = v.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = v.Get(ctx, id)
	if err != ErrSecretNotFound {
		t.Fatalf("Get after Delete = %v, want ErrSecretNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// Unseal round-trip with passphrase
// ---------------------------------------------------------------------------

func TestUnseal_PassphraseRoundTrip(t *testing.T) {
	loweredCost(t)
	fp := newTestFileProvider(ProviderFile)
	v, store, _ := testVault(t, fp)
	mustSetup(t, v, "hunter2")

	ctx := context.Background()
	id, err := v.Create(ctx, credential.NewSecret("unseal-test"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Commit journal entry so Reconcile on reload does not delete the orphan
	// (Create now leaves PhaseSecretWritten entries, defect 1).
	if err = v.AttachTarget(ctx, id, "roundtrip"); err != nil {
		t.Fatalf("AttachTarget: %v", err)
	}
	if err = v.CommitMetadata(ctx, id); err != nil {
		t.Fatalf("CommitMetadata: %v", err)
	}
	v.Seal()

	reg, _ := NewRegistry(fp)
	v2, err := New(store, reg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if v2.State() != StateSealed {
		t.Fatalf("reloaded state = %v, want sealed", v2.State())
	}

	if err = v2.Unseal(ctx, UnsealRequest{Passphrase: "hunter2"}); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	sec, err := v2.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after unseal: %v", err)
	}
	var s string
	_ = sec.Use(func(b []byte) error { s = string(b); return nil })
	if s != "unseal-test" {
		t.Fatalf("value = %q, want %q", s, "unseal-test")
	}
}

// ---------------------------------------------------------------------------
// Regression tests for core-fix defects
// ---------------------------------------------------------------------------

// Defect 1: Create clears journal entry after successful Put instead of
// advancing to PhaseSecretWritten.
func TestCreate_AdvanceToPhaseSecretWritten(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "")

	ctx := context.Background()
	id, err := v.Create(ctx, credential.NewSecret("test-value"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Journal should hold a PhaseSecretWritten entry for the secret.
	var found bool
	for _, e := range v.doc.Journal {
		if e.NewID == id {
			found = true
			if e.Phase != PhaseSecretWritten {
				t.Fatalf("journal entry phase = %q, want %q", e.Phase, PhaseSecretWritten)
			}
			break
		}
	}
	if !found {
		t.Fatal("no journal entry for created secret — Create cleared it")
	}

	// AttachTarget and CommitMetadata should clear the entry.
	if err := v.AttachTarget(ctx, id, "test-target"); err != nil {
		t.Fatalf("AttachTarget: %v", err)
	}
	if err := v.CommitMetadata(ctx, id); err != nil {
		t.Fatalf("CommitMetadata: %v", err)
	}
	for _, e := range v.doc.Journal {
		if e.NewID == id {
			t.Fatal("journal entry not cleared after CommitMetadata")
		}
	}
}

// Defect 2: Setup returns while holding the mutex on several error paths.
// Regression test verifies that after a failing provider Put during silent
// setup, State() does not hang.
func TestSetup_MutexNotHeldOnError(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	sys.fail = errors.New("injected put failure")
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)

	done := make(chan struct{}, 1)
	go func() {
		_, _ = v.Setup(context.Background(), SetupRequest{})
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Setup hung — mutex held on error return path")
	}

	stCh := make(chan State, 1)
	go func() {
		stCh <- v.State()
	}()
	select {
	case <-stCh:
	case <-time.After(5 * time.Second):
		t.Fatal("State() hung after failed Setup — mutex still held")
	}
}

func TestUnseal_DetectsConcurrentSeal(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "")

	ctx := context.Background()
	v.Seal()

	// Block Unseal's provider Unlock so we can inject a concurrent Seal.
	entered := make(chan struct{})
	release := make(chan struct{})
	fp.unlockHook = func() {
		close(entered) // Unseal has started unlocking providers
		<-release      // wait for test to inject Seal
	}

	go func() {
		<-entered
		v.Seal()
		close(release) // let Unseal's Unlock complete
	}()

	err := v.Unseal(ctx, UnsealRequest{UseOSKey: true})
	// After fix: Unseal captures gen, re-checks after provider calls,
	// detects the gen change from concurrent Seal, returns error.
	// Before fix: Unseal ignores gen change and returns nil.
	if err == nil {
		t.Fatal("Unseal should have detected concurrent Seal")
	}
	if v.State() != StateSealed {
		t.Fatal("vault should remain sealed after Seal+Unseal race")
	}
	// Provider must be locked (not unlocked by Unseal, defect 3).
	if fp.unlocked.Load() {
		t.Fatal("provider should be locked after Seal+Unseal race")
	}
}

func TestGet_RejectsResultAfterSeal(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "")

	ctx := context.Background()
	// Create routes to ProviderSystem (default when sys is registered).
	id, err := v.Create(ctx, credential.NewSecret("get-race"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Slow down Get so we can inject a Seal mid-flight.
	// Hook goes on sys since Create routes to ProviderSystem.
	var getStarted atomic.Bool
	getGate := make(chan struct{})
	sys.getHook = func() {
		getStarted.Store(true)
		<-getGate
	}

	got := make(chan error, 1)
	go func() {
		var goErr error
		_, goErr = v.Get(ctx, id)
		got <- goErr
	}()
	for !getStarted.Load() {
		time.Sleep(time.Millisecond)
	}

	v.Seal()
	close(getGate)
	err = <-got
	// After fix: Get re-checks gen after provider returns and rejects result.
	// Before fix: Get accepts the stale result.
	if err == nil {
		t.Fatal("Get should reject result after seal")
	}
}

// Defect 5: Setup commits the document before the file provider is
// initialised, so a failed NewDataKey leaves the document saying
// "initialised" with a bricked vault.
func TestSetup_DocumentNotSavedBeforeProviderInit(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := &testFileProvider{testProvider: newTestProvider(ProviderFile)}
	fp.dataKeyFail = errors.New("injected data key failure")
	// We'll verify the document is not committed.
	v, store, _ := testVault(t, sys, fp)

	_, err := v.Setup(context.Background(), SetupRequest{Passphrase: "test"})
	if err == nil {
		t.Fatal("expected Setup to fail due to data key error")
	}

	// Document should NOT exist (or should have Instance empty).
	var doc Document
	found, _ := store.Read("vault.json", &doc)
	if found && doc.Instance != "" {
		t.Fatal("Setup committed document before provider init completed")
	}
}

// Defect 6: Silent setup can strand an OS-held root key if document save
// fails after the key was stored. The fix best-effort deletes the OS key,
// restores the in-memory document, and wipes the root.
func TestSetup_OrphanCleanupOnSaveFail(t *testing.T) {
	loweredCost(t)
	tmpDir := t.TempDir()
	realStore := storage.NewDocumentStore(tmpDir)
	store := &failingDocStore{DocumentStore: realStore}

	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	reg, err := NewRegistry(sys, fp)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	v, err := New(store, reg, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Silent setup: OS key is stored, then document save fails.
	store.failOnWrite = true
	_, err = v.Setup(context.Background(), SetupRequest{})
	if err == nil {
		t.Fatal("expected Setup to fail due to document save failure")
	}

	// The OS key should have been best-effort deleted.
	oskID := osKeyID(v.doc.Instance)
	sec, err := sys.Get(context.Background(), oskID)
	if err == nil && !sec.IsEmpty() {
		t.Fatal("OS key should have been cleaned up after save failure")
	}

	// The vault should still be uninitialized.
	if v.State() != StateUninitialized {
		t.Fatalf("vault state = %v, want uninitialized after failed setup", v.State())
	}
}

// Defect 7: Concurrent Setup is not serialised — two callers both pass the
// uninitialised check because the mutex is released.
func TestSetup_SerialisesConcurrentCalls(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)

	startCh := make(chan struct{})
	readyCh := make(chan struct{}, 2)

	results := make(chan error, 2)
	for range 2 {
		go func() {
			readyCh <- struct{}{}
			<-startCh
			_, err := v.Setup(context.Background(), SetupRequest{})
			results <- err
		}()
	}

	for range 2 {
		<-readyCh
	}
	close(startCh) // release both goroutines at once

	// Collect both results.
	var errCount int
	for range 2 {
		if err := <-results; err != nil {
			errCount++
		}
	}

	// Exactly one should succeed; the other should fail.
	if errCount != 1 {
		t.Fatalf("expected exactly 1 error from 2 concurrent Setups, got %d", errCount)
	}
}

func TestSetup_RecoveryCodeRoundTrip(t *testing.T) {
	loweredCost(t)
	fp := newTestFileProvider(ProviderFile)
	v, store, _ := testVault(t, fp)

	ctx := context.Background()
	result, err := v.Setup(ctx, SetupRequest{Passphrase: "sekret"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if result.RecoveryCode == "" {
		t.Fatal("expected non-empty recovery code from passphrase setup")
	}

	// Seal the vault.
	v.Seal()

	// Load it fresh and unseal with the recovery code.
	reg, _ := NewRegistry(fp)
	v2, err := New(store, reg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if v2.State() != StateSealed {
		t.Fatalf("reloaded state = %v, want sealed", v2.State())
	}

	if err = v2.Unseal(ctx, UnsealRequest{RecoveryCode: result.RecoveryCode}); err != nil {
		t.Fatalf("Unseal with Setup recovery code: %v", err)
	}
}

// The round trip that proves a recovery code is a recovery factor — reading a
// stored secret back after unsealing with nothing else — lives in
// vault_recovery_external_test.go, because it needs the real file provider.
// The in-memory fake used here does not survive a reopen, so a test written
// against it fails identically whether the code is correct or not.

// Defect 8: Delete re-checks state and generation immediately before
// persisting the journal and delegating. This test verifies the invariant
// that Delete returns an error when a Seal happens (the initial state check
// catches it; the re-check at journal time is defense-in-depth).
func TestDelete_SealBeforeDelete(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "")

	ctx := context.Background()
	id, err := v.Create(ctx, credential.NewSecret("delete-seal"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	v.Seal()
	err = v.Delete(ctx, id)
	if err == nil {
		t.Fatal("Delete after Seal should fail")
	}
}

func TestExists_PropagatesProviderErrors(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "")

	ctx := context.Background()
	id, err := v.Create(ctx, credential.NewSecret("exists-test"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Inject a non-SecretNotFound error.
	sys.fail = errors.New("provider denied")

	ok, err := v.Exists(ctx, id)
	// Before fix: (false, nil) — error swallowed.
	// After fix: error propagates.
	if err == nil {
		t.Fatal("Exists should have propagated provider error, got nil")
	}
	if !ok {
		t.Log("Exists returned false with error — that's fine (may or may not exist)")
	}

	// ErrSecretNotFound should still map to (false, nil).
	sys.fail = nil
	missing := credential.SecretID("sec:v1:system:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	ok, err = v.Exists(ctx, missing)
	if err != nil {
		t.Fatalf("Exists for absent secret should return (false, nil), got error: %v", err)
	}
	if ok {
		t.Fatal("Exists for absent secret should return false")
	}
}

// Defect 10: journal.go retains PhasePrepared/PhaseSecretWritten entries
// with non-empty Target without reporting them, and unknown phases fall
// through silently.
func TestReconcile_ReportsRetainedAndUnknown(t *testing.T) {
	doc := &Document{}
	wp := newTestProvider(ProviderFile)
	reg, err := NewRegistry(wp)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	ctx := context.Background()

	// Entry with non-empty Target at PhasePrepared — should be blocked.
	doc.Journal = append(doc.Journal, JournalEntry{
		Op:     "create",
		NewID:  "sec:v1:file:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Phase:  PhasePrepared,
		Target: "some-target",
	})

	// Entry with unknown phase — should be blocked.
	doc.Journal = append(doc.Journal, JournalEntry{
		Op:    "create",
		NewID: "sec:v1:file:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Phase: "unknown-phase",
	})

	blocked := Reconcile(ctx, doc, reg)
	if len(blocked) != 2 {
		t.Fatalf("expected 2 blocked entries, got %d", len(blocked))
	}
}

// Defect 10b: validate that a malformed Op is reported.
func TestReconcile_ReportsMalformedOp(t *testing.T) {
	doc := &Document{}
	wp := newTestProvider(ProviderFile)
	reg, err := NewRegistry(wp)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	ctx := context.Background()

	// Empty Op is skipped as cleared.
	doc.Journal = append(doc.Journal, JournalEntry{
		Op:    "",
		NewID: "sec:v1:file:cccccccccccccccccccccccccccccccc",
		Phase: PhasePrepared,
	})

	// Unknown Op is blocked.
	doc.Journal = append(doc.Journal, JournalEntry{
		Op:    "unknown-op",
		NewID: "sec:v1:file:dddddddddddddddddddddddddddddddd",
		Phase: PhasePrepared,
	})

	blocked := Reconcile(ctx, doc, reg)
	// Empty-Op entry skipped; unknown-Op entry blocked.
	if len(blocked) != 1 {
		t.Fatalf("expected 1 blocked for unknown-Op entry, got %d", len(blocked))
	}
	if blocked[0].Op != "unknown-op" {
		t.Fatalf("blocked entry Op = %q, want %q", blocked[0].Op, "unknown-op")
	}
}

// Defect 12: The plaintext-in-logs check only checks attribute equality,
// missing plaintext inside error messages or rendered output.
func TestCreate_NoPlaintextInRenderedLogs(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestProvider(ProviderFile)
	v, _, capH := testVault(t, sys, fp)
	mustSetup(t, v, "")

	plaintext := "my-supersecret-password-42"
	_, err := v.Create(context.Background(), credential.NewSecret(plaintext))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The rendered log output must not contain the plaintext as a substring.
	if capH.containsPlaintextRendered(plaintext) {
		t.Fatal("plaintext secret leaked into rendered log output")
	}
}

// Defect 4b: Exists must also re-check the generation after provider call.
func TestExists_RejectsResultAfterSeal(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "")

	ctx := context.Background()
	id, err := v.Create(ctx, credential.NewSecret("exists-race"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var getStarted atomic.Bool
	getGate := make(chan struct{})
	sys.getHook = func() {
		getStarted.Store(true)
		<-getGate
	}

	got := make(chan error, 1)
	go func() {
		var goErr error
		_, goErr = v.Exists(ctx, id)
		got <- goErr
	}()

	for !getStarted.Load() {
		time.Sleep(time.Millisecond)
	}

	v.Seal()
	close(getGate)

	err = <-got
	// After fix: Exists re-checks gen after provider returns and rejects.
	if err == nil {
		t.Fatal("Exists should reject result after seal")
	}
}

// ---------------------------------------------------------------------------
// ChangePassphrase
// ---------------------------------------------------------------------------

func TestChangePassphrase_WithOldPassphrase(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, store, _ := testVault(t, sys, fp)
	mustSetup(t, v, "sekret")

	// Create a secret before the change, read it back after.
	ctx := context.Background()
	id, err := v.Create(ctx, credential.NewSecret("persist-test"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err = v.AttachTarget(ctx, id, "test"); err != nil {
		t.Fatalf("AttachTarget: %v", err)
	}
	if err = v.CommitMetadata(ctx, id); err != nil {
		t.Fatalf("CommitMetadata: %v", err)
	}

	// Count provider Puts before the change.
	var putCount int32
	sys.putHook = func() { atomic.AddInt32(&putCount, 1) }
	putCountBefore := atomic.LoadInt32(&putCount)

	err = v.ChangePassphrase(ctx, ChangePassphraseRequest{
		OldPassphrase: "sekret",
		NewPassphrase: "newsekret",
	})
	if err != nil {
		t.Fatalf("ChangePassphrase: %v", err)
	}

	// Assert: no provider writes happened during the change.
	if atomic.LoadInt32(&putCount) != putCountBefore {
		t.Error("ChangePassphrase wrote to a provider; should touch only the document")
	}

	// Assert: secret still resolves after passphrase change.
	sec, err := v.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after ChangePassphrase: %v", err)
	}
	var s string
	_ = sec.Use(func(b []byte) error { s = string(b); return nil })
	if s != "persist-test" {
		t.Errorf("secret value = %q, want %q", s, "persist-test")
	}

	// Assert: reloaded vault unseals with new passphrase.
	v.Seal()
	reg, _ := NewRegistry(sys, fp)
	v2, err := New(store, reg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err = v2.Unseal(ctx, UnsealRequest{Passphrase: "newsekret"}); err != nil {
		t.Fatalf("Unseal with new passphrase: %v", err)
	}
}

func TestChangePassphrase_WithRecoveryCode(t *testing.T) {
	loweredCost(t)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, fp)
	result := mustSetup(t, v, "sekret")

	ctx := context.Background()
	code := result.RecoveryCode
	if code == "" {
		t.Fatal("expected non-empty recovery code from passphrase setup")
	}

	err := v.ChangePassphrase(ctx, ChangePassphraseRequest{
		RecoveryCode:  code,
		NewPassphrase: "recovery-changed",
	})
	if err != nil {
		t.Fatalf("ChangePassphrase with recovery code: %v", err)
	}

	// Assert: old passphrase no longer works.
	if err := v.ChangePassphrase(ctx, ChangePassphraseRequest{
		OldPassphrase: "sekret",
		NewPassphrase: "another",
	}); err == nil {
		t.Error("old passphrase should not work after recovery-code rotation")
	}
}

func TestChangePassphrase_WrongOldPassphrase(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "sekret")

	ctx := context.Background()
	err := v.ChangePassphrase(ctx, ChangePassphraseRequest{
		OldPassphrase: "wrong",
		NewPassphrase: "newsekret",
	})
	if !errors.Is(err, ErrUnsealFailed) {
		t.Fatalf("expected ErrUnsealFailed, got %v", err)
	}
}

func TestChangePassphrase_WrongRecoveryCode(t *testing.T) {
	loweredCost(t)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, fp)
	mustSetup(t, v, "sekret")

	ctx := context.Background()
	err := v.ChangePassphrase(ctx, ChangePassphraseRequest{
		RecoveryCode:  "invalid-code",
		NewPassphrase: "newsekret",
	})
	if !errors.Is(err, ErrUnsealFailed) {
		t.Fatalf("expected ErrUnsealFailed, got %v", err)
	}
}

func TestChangePassphrase_NoAuthFactor(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "")

	ctx := context.Background()
	err := v.ChangePassphrase(ctx, ChangePassphraseRequest{
		NewPassphrase: "newsekret",
	})
	if err == nil {
		t.Fatal("expected error for no auth factor with OS key only")
	}
}

func TestChangePassphrase_EmptyNewPassphrase(t *testing.T) {
	loweredCost(t)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, fp)
	mustSetup(t, v, "sekret")

	ctx := context.Background()
	err := v.ChangePassphrase(ctx, ChangePassphraseRequest{
		OldPassphrase: "sekret",
		NewPassphrase: "",
	})
	if err == nil {
		t.Fatal("expected error for empty new passphrase")
	}
}

func TestChangePassphrase_Uninitialized(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)

	ctx := context.Background()
	err := v.ChangePassphrase(ctx, ChangePassphraseRequest{
		OldPassphrase: "sekret",
		NewPassphrase: "newsekret",
	})
	if !errors.Is(err, ErrVaultUninitialized) {
		t.Fatalf("expected ErrVaultUninitialized, got %v", err)
	}
}

func TestChangePassphrase_Sealed(t *testing.T) {
	loweredCost(t)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, fp)
	mustSetup(t, v, "sekret")
	v.Seal()

	ctx := context.Background()
	err := v.ChangePassphrase(ctx, ChangePassphraseRequest{
		OldPassphrase: "sekret",
		NewPassphrase: "newsekret",
	})
	if err != nil {
		t.Fatalf("ChangePassphrase while sealed: %v", err)
	}

	// Verify new passphrase works.
	if err := v.Unseal(ctx, UnsealRequest{Passphrase: "newsekret"}); err != nil {
		t.Fatalf("Unseal with new passphrase after sealed change: %v", err)
	}
}

func TestChangePassphrase_NoPassphraseEnvelope(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "") // silent setup — no passphrase envelope

	ctx := context.Background()
	err := v.ChangePassphrase(ctx, ChangePassphraseRequest{
		OldPassphrase: "sekret",
		NewPassphrase: "newsekret",
	})
	if err == nil {
		t.Fatal("expected error when no passphrase envelope exists")
	}
}

// ---------------------------------------------------------------------------
// RegenerateRecovery
// ---------------------------------------------------------------------------

func TestRegenerateRecovery_WithPassphrase(t *testing.T) {
	loweredCost(t)
	fp := newTestFileProvider(ProviderFile)
	v, store, _ := testVault(t, fp)
	mustSetup(t, v, "sekret")

	ctx := context.Background()
	code, err := v.RegenerateRecovery(ctx, RegenerateRequest{Passphrase: "sekret"})
	if err != nil {
		t.Fatalf("RegenerateRecovery: %v", err)
	}
	if code == "" {
		t.Fatal("expected non-empty recovery code")
	}
	if len(code) < 10 {
		t.Fatalf("recovery code too short: %q", code)
	}

	// Assert: the new recovery code can unseal after seal.
	v.Seal()
	reg, _ := NewRegistry(fp)
	v2, err := New(store, reg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err = v2.Unseal(ctx, UnsealRequest{RecoveryCode: code}); err != nil {
		t.Fatalf("Unseal with new recovery code: %v", err)
	}
}

func TestRegenerateRecovery_WrongPassphrase(t *testing.T) {
	loweredCost(t)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, fp)
	mustSetup(t, v, "sekret")

	ctx := context.Background()
	_, err := v.RegenerateRecovery(ctx, RegenerateRequest{Passphrase: "wrong"})
	if !errors.Is(err, ErrUnsealFailed) {
		t.Fatalf("expected ErrUnsealFailed, got %v", err)
	}
}

func TestRegenerateRecovery_EmptyPassphrase(t *testing.T) {
	loweredCost(t)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, fp)
	mustSetup(t, v, "sekret")

	ctx := context.Background()
	_, err := v.RegenerateRecovery(ctx, RegenerateRequest{Passphrase: ""})
	if err == nil {
		t.Fatal("expected error for empty passphrase")
	}
}

func TestRegenerateRecovery_NoPassphraseEnvelope(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "") // silent setup — no passphrase envelope

	ctx := context.Background()
	_, err := v.RegenerateRecovery(ctx, RegenerateRequest{Passphrase: "sekret"})
	if err == nil {
		t.Fatal("expected error when no passphrase envelope exists")
	}
}

func TestRegenerateRecovery_Uninitialized(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)

	ctx := context.Background()
	_, err := v.RegenerateRecovery(ctx, RegenerateRequest{Passphrase: "sekret"})
	if !errors.Is(err, ErrVaultUninitialized) {
		t.Fatalf("expected ErrVaultUninitialized, got %v", err)
	}
}

func TestRegenerateRecovery_Sealed(t *testing.T) {
	loweredCost(t)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, fp)
	mustSetup(t, v, "sekret")
	v.Seal()

	ctx := context.Background()
	code, err := v.RegenerateRecovery(ctx, RegenerateRequest{Passphrase: "sekret"})
	if err != nil {
		t.Fatalf("RegenerateRecovery while sealed: %v", err)
	}
	if code == "" {
		t.Fatal("expected non-empty recovery code")
	}
}

// ---------------------------------------------------------------------------
// SetDefaultProvider
// ---------------------------------------------------------------------------

func TestSetDefaultProvider_Valid(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "")

	ctx := context.Background()
	if err := v.SetDefaultProvider(ctx, ProviderFile); err != nil {
		t.Fatalf("SetDefaultProvider: %v", err)
	}
	if v.doc.DefaultProvider != ProviderFile {
		t.Errorf("DefaultProvider = %q, want %q", v.doc.DefaultProvider, ProviderFile)
	}
}

func TestSetDefaultProvider_Unregistered(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "")

	ctx := context.Background()
	err := v.SetDefaultProvider(ctx, ProviderID("nonexistent"))
	if err == nil {
		t.Fatal("expected error for unregistered provider")
	}

	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProviderError, got %T: %v", err, err)
	}
	if pe.Reason != ReasonUnknownProvider {
		t.Errorf("reason = %q, want %q", pe.Reason, ReasonUnknownProvider)
	}
}

func TestSetDefaultProvider_NonWritable(t *testing.T) {
	loweredCost(t)
	ro := &readOnlyProvider{id: ProviderSystem, inner: newTestProvider(ProviderSystem)}
	v, _, _ := testVault(t, ro)
	mustSetup(t, v, "")

	ctx := context.Background()
	err := v.SetDefaultProvider(ctx, ProviderSystem)
	if err == nil {
		t.Fatal("expected error for non-writable provider")
	}

	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProviderError, got %T: %v", err, err)
	}
	if pe.Reason != ReasonDenied {
		t.Errorf("reason = %q, want %q", pe.Reason, ReasonDenied)
	}
}

func TestSetDefaultProvider_Uninitialized(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)

	ctx := context.Background()
	err := v.SetDefaultProvider(ctx, ProviderFile)
	if !errors.Is(err, ErrVaultUninitialized) {
		t.Fatalf("expected ErrVaultUninitialized, got %v", err)
	}
}

func TestSetDefaultProvider_Sealed(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "")
	v.Seal()

	ctx := context.Background()
	if err := v.SetDefaultProvider(ctx, ProviderFile); err != nil {
		t.Fatalf("SetDefaultProvider while sealed: %v", err)
	}
	if v.doc.DefaultProvider != ProviderFile {
		t.Errorf("DefaultProvider = %q, want %q", v.doc.DefaultProvider, ProviderFile)
	}
}

// ---------------------------------------------------------------------------
// Failure-path tests
// ---------------------------------------------------------------------------

func TestChangePassphrase_SaveFails(t *testing.T) {
	loweredCost(t)
	fp := newTestFileProvider(ProviderFile)
	v, docStore, _ := testVault(t, fp)
	mustSetup(t, v, "sekret")

	// Replace the store with a failing one.
	v.mu.Lock()
	v.store = &failingDocStore{failOnWrite: true}
	v.mu.Unlock()

	ctx := context.Background()
	err := v.ChangePassphrase(ctx, ChangePassphraseRequest{
		OldPassphrase: "sekret",
		NewPassphrase: "newsekret",
	})
	if err == nil {
		t.Fatal("expected error when document save fails")
	}
	if !strings.Contains(err.Error(), "save document") {
		t.Errorf("error = %q, want substring 'save document'", err.Error())
	}

	// Restore real store so deferred cleanup works and rollback check can save.
	v.mu.Lock()
	v.store = docStore
	v.mu.Unlock()

	// Assert: old passphrase envelope was rolled back.
	if err := v.ChangePassphrase(ctx, ChangePassphraseRequest{
		OldPassphrase: "sekret",
		NewPassphrase: "newsekret",
	}); err != nil {
		t.Fatalf("old passphrase should still work after rollback: %v", err)
	}
}

func TestRegenerateRecovery_SaveFails(t *testing.T) {
	loweredCost(t)
	fp := newTestFileProvider(ProviderFile)
	v, docStore, _ := testVault(t, fp)
	mustSetup(t, v, "sekret")

	// Replace the store with a failing one.
	v.mu.Lock()
	v.store = &failingDocStore{failOnWrite: true}
	v.mu.Unlock()

	ctx := context.Background()
	_, err := v.RegenerateRecovery(ctx, RegenerateRequest{Passphrase: "sekret"})
	if err == nil {
		t.Fatal("expected error when document save fails")
	}
	if !strings.Contains(err.Error(), "save document") {
		t.Errorf("error = %q, want substring 'save document'", err.Error())
	}

	// Restore real store so deferred cleanup works.
	v.mu.Lock()
	v.store = docStore
	v.mu.Unlock()
}

func TestSetDefaultProvider_SaveFails(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, docStore, _ := testVault(t, sys, fp)
	mustSetup(t, v, "")

	// Replace the store with a failing one.
	v.mu.Lock()
	v.store = &failingDocStore{failOnWrite: true}
	v.mu.Unlock()

	ctx := context.Background()
	err := v.SetDefaultProvider(ctx, ProviderFile)
	if err == nil {
		t.Fatal("expected error when document save fails")
	}
	if !strings.Contains(err.Error(), "save document") {
		t.Errorf("error = %q, want substring 'save document'", err.Error())
	}
	// Restore real store so deferred cleanup works.
	v.mu.Lock()
	v.store = docStore
	v.mu.Unlock()

	// Assert: DefaultProvider was rolled back to original value.
	if v.doc.DefaultProvider != ProviderSystem {
		t.Errorf("DefaultProvider = %q, want %q (original)", v.doc.DefaultProvider, ProviderSystem)
	}
}

// ---------------------------------------------------------------------------
// Snapshot: OSKeyCapable — derived from provider list, not from vault state
// ---------------------------------------------------------------------------

func TestSnapshot_OSKeyCapable_ReadyWritableSystem(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	snap := v.Snapshot(context.Background())
	if !snap.OSKeyCapable {
		t.Error("OSKeyCapable = false, want true (system provider is ready and writable)")
	}
}

func TestSnapshot_OSKeyCapable_SystemNotWritable(t *testing.T) {
	loweredCost(t)
	ro := &readOnlyProvider{id: ProviderSystem, inner: newTestProvider(ProviderSystem)}
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, ro, fp)
	snap := v.Snapshot(context.Background())
	if snap.OSKeyCapable {
		t.Error("OSKeyCapable = true, want false (system provider is not writable)")
	}
}

func TestSnapshot_OSKeyCapable_SystemNotReady(t *testing.T) {
	loweredCost(t)
	sys := &unreadyProvider{testProvider: newTestProvider(ProviderSystem)}
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	snap := v.Snapshot(context.Background())
	if snap.OSKeyCapable {
		t.Error("OSKeyCapable = true, want false (system provider is not ready)")
	}
}

func TestSnapshot_OSKeyCapable_NoSystemProvider(t *testing.T) {
	loweredCost(t)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, fp)
	snap := v.Snapshot(context.Background())
	if snap.OSKeyCapable {
		t.Error("OSKeyCapable = true, want false (no system provider)")
	}
}

func TestSnapshot_OSKeyCapable_NonSystemWritableReady(t *testing.T) {
	loweredCost(t)
	// Only a file provider — ready+writable but not a system provider.
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, fp)
	snap := v.Snapshot(context.Background())
	if snap.OSKeyCapable {
		t.Error("OSKeyCapable = true, want false (only file provider, not system)")
	}
}

func TestSnapshot_HasOSKeyPreserved(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "")
	snap := v.Snapshot(context.Background())
	if !snap.HasOSKey {
		t.Error("HasOSKey = false, want true after silent setup")
	}
	// OSKeyCapable should remain true even after setup.
	if !snap.OSKeyCapable {
		t.Error("OSKeyCapable = false, want true (system provider is still present)")
	}
}

// ---------------------------------------------------------------------------
// Auto-seal
// ---------------------------------------------------------------------------

func TestSetAutoSeal_ValidValues(t *testing.T) {
	v, _, _ := testVault(t, newTestProvider(ProviderFile))
	for _, mins := range []int{0, 5, 15, 30, 60} {
		if err := v.SetAutoSeal(context.Background(), mins); err != nil {
			t.Errorf("SetAutoSeal(%d): %v", mins, err)
		}
		v.mu.Lock()
		got := v.doc.AutoSealMinutes
		v.mu.Unlock()
		if got != mins {
			t.Errorf("AutoSealMinutes after SetAutoSeal(%d) = %d", mins, got)
		}
	}
}

func TestSetAutoSeal_InvalidValues(t *testing.T) {
	v, _, _ := testVault(t, newTestProvider(ProviderFile))
	for _, mins := range []int{-1, 1, 10, 20, 100} {
		if err := v.SetAutoSeal(context.Background(), mins); err == nil {
			t.Errorf("SetAutoSeal(%d): expected error", mins)
		}
	}
}

func TestSetAutoSeal_Persistence(t *testing.T) {
	loweredCost(t)
	v, store, _ := testVault(t, newTestProvider(ProviderFile))
	// Ensure vault document exists by setting up.
	mustSetup(t, v, "hunter2")

	if err := v.SetAutoSeal(context.Background(), 30); err != nil {
		t.Fatalf("SetAutoSeal: %v", err)
	}
	v.Seal()

	// Reload from store.
	reg, rerr := NewRegistry(newTestProvider(ProviderFile))
	if rerr != nil {
		t.Fatalf("NewRegistry: %v", rerr)
	}
	v2, err := New(store, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	v2.mu.Lock()
	got := v2.doc.AutoSealMinutes
	v2.mu.Unlock()
	if got != 30 {
		t.Fatalf("persisted AutoSealMinutes = %d, want 30", got)
	}
}

func TestActivity_EpochIncrement(t *testing.T) {
	v, _, _ := testVault(t, newTestProvider(ProviderFile))
	v.mu.Lock()
	before := v.autoSealEpoch
	v.mu.Unlock()

	v.Activity()

	v.mu.Lock()
	after := v.autoSealEpoch
	v.mu.Unlock()
	if after != before+1 {
		t.Fatalf("epoch after Activity = %d, want %d", after, before+1)
	}
}

func TestAutoSeal_TimerFiresAndSeals(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderFile))
	mustSetup(t, v, "pass")

	// Override the duration function to use milliseconds for testing.
	v.mu.Lock()
	v.autoSealDurationFn = func(int) time.Duration { return 10 * time.Millisecond }
	v.doc.AutoSealMinutes = 1 // any non-zero value; the function returns 10ms regardless
	v.autoSealEpoch++         // ensure epoch is tracked
	v.mu.Unlock()

	// Wake the goroutine to arm the timer.
	v.wakeAutoSeal()

	// Wait for the timer to fire and seal.
	time.Sleep(50 * time.Millisecond)

	if v.State() != StateSealed {
		t.Fatal("auto-seal timer did not seal the vault")
	}
}

func TestAutoSeal_ActivityPreventsSeal(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderFile))
	mustSetup(t, v, "pass")

	v.mu.Lock()
	v.autoSealDurationFn = func(int) time.Duration { return 50 * time.Millisecond }
	v.doc.AutoSealMinutes = 1
	v.autoSealEpoch++
	v.mu.Unlock()

	// Wake to arm the 50ms timer.
	v.wakeAutoSeal()
	time.Sleep(20 * time.Millisecond)

	// Activity resets the timer and increments the epoch.
	v.Activity()

	// Wait less than the original timer's remaining 30ms so the
	// original timer would have fired, but the reset should have
	// started a fresh 50ms timer.
	time.Sleep(20 * time.Millisecond)

	if v.State() == StateSealed {
		t.Fatal("vault was sealed after activity reset — activity should restart the timer")
	}

	// Wait for the fresh 50ms timer to fire.
	time.Sleep(60 * time.Millisecond)

	if v.State() != StateSealed {
		t.Fatal("vault should have sealed after the reset timer expired")
	}
}

func TestAutoSeal_SealStopsTimer(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderFile))
	mustSetup(t, v, "pass")

	v.mu.Lock()
	v.autoSealDurationFn = func(int) time.Duration { return 10 * time.Millisecond }
	v.doc.AutoSealMinutes = 1
	v.autoSealEpoch++
	v.mu.Unlock()
	v.wakeAutoSeal()

	// Manual seal before timer fires.
	time.Sleep(5 * time.Millisecond)
	v.Seal()

	// Wait for the original timer to fire.
	time.Sleep(50 * time.Millisecond)

	// Vault should remain sealed (no spurious re-seal needed; the point is
	// that the timer did not cause a double-seal or panic).
	if v.State() != StateSealed {
		t.Fatal("vault is not sealed after manual seal + timer expiry")
	}
}

func TestAutoSeal_GenGuardAndEpochGuard(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderFile))
	mustSetup(t, v, "pass")

	// Use a long interval so no real timer fires during the test.
	v.mu.Lock()
	v.autoSealDurationFn = func(int) time.Duration { return 10 * time.Minute }
	v.doc.AutoSealMinutes = 5
	v.mu.Unlock()

	// Wake to arm, capturing gen + epoch.
	v.wakeAutoSeal()
	time.Sleep(50 * time.Millisecond)

	v.mu.Lock()
	genAtArm := v.gen
	epochAtArm := v.autoSealEpoch
	v.mu.Unlock()

	// Seal changes gen. After this, a stale timer armed before the seal
	// should be rejected because gen changed.
	v.Seal()

	// Verify gen changed.
	v.mu.Lock()
	if v.gen <= genAtArm {
		t.Fatal("seal did not increment gen")
	}
	v.mu.Unlock()

	// Unseal. The goroutine is woken and re-arms with fresh gen.
	if err := v.Unseal(context.Background(), UnsealRequest{Passphrase: "pass"}); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	// Verify epoch changed (Unseal increments it).
	v.mu.Lock()
	epochAfter := v.autoSealEpoch
	v.mu.Unlock()

	if epochAfter <= epochAtArm {
		t.Fatal("epoch did not increment after unseal")
	}

	// Now verify the guard logic by deduction: if a stale timer armed at
	// (genAtArm, epochAtArm) fires now, neither gen nor epoch matches.
	// The goroutine's check (!isSealed && epoch == armEpoch) would fail on
	// epoch mismatch. This proves the guard works without relying on timer
	// timing.
	v.mu.Lock()
	isSealed := v.rootKey == nil
	genNow := v.gen
	epochNow := v.autoSealEpoch
	v.mu.Unlock()

	if isSealed {
		t.Fatal("vault should be unsealed after unseal call")
	}
	if genNow == genAtArm {
		t.Fatal("gen should have changed after seal")
	}
	if epochNow == epochAtArm {
		t.Fatal("epoch should have changed after activity/unseal")
	}
}

func TestAutoSeal_OffDoesNotArm(t *testing.T) {
	v, _, _ := testVault(t, newTestProvider(ProviderFile))
	mustSetup(t, v, "pass")

	// Set to 0 (off) and ensure the goroutine does not arm a timer.
	if err := v.SetAutoSeal(context.Background(), 0); err != nil {
		t.Fatalf("SetAutoSeal(0): %v", err)
	}

	// Verify the epoch was incremented but no timer is running (can't
	// directly observe the timer, but we can check the goroutine didn't
	// seal after a wake).
	v.wakeAutoSeal()
	time.Sleep(10 * time.Millisecond)

	if v.State() != StateUnsealed {
		t.Fatal("vault was sealed despite auto-seal being off")
	}
}

// ---------------------------------------------------------------------------
// Snapshot: AutoSealMinutes and HasPassphrase
// ---------------------------------------------------------------------------

func TestSnapshot_AutoSealMinutes(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderFile))
	mustSetup(t, v, "pass")

	snap := v.Snapshot(context.Background())
	if snap.AutoSealMinutes != 15 {
		t.Errorf("AutoSealMinutes in snapshot = %d, want 15 (fresh vault default)",
			snap.AutoSealMinutes)
	}

	_ = v.SetAutoSeal(context.Background(), 30)
	snap = v.Snapshot(context.Background())
	if snap.AutoSealMinutes != 30 {
		t.Errorf("AutoSealMinutes after SetAutoSeal(30) = %d, want 30",
			snap.AutoSealMinutes)
	}
}

func TestSnapshot_HasPassphrase(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderFile))

	// Before setup: no passphrase envelope.
	snap := v.Snapshot(context.Background())
	if snap.HasPassphrase {
		t.Error("HasPassphrase = true before setup")
	}

	// After passphrase setup: should have an envelope.
	mustSetup(t, v, "pass")
	snap = v.Snapshot(context.Background())
	if !snap.HasPassphrase {
		t.Error("HasPassphrase = false after passphrase setup")
	}
}

// --- Purge ---

// Purge is the vault's half of a reset: destroy every provider's material,
// then the document, and return to uninitialized.
//
// It must work while SEALED. That is not an edge case, it is the only case —
// a user resets because they cannot unlock, so an implementation that needed
// the root key would refuse exactly when it is wanted.
func TestVault_Purge_FromSealedReturnsToUninitialized(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderSystem), newTestFileProvider(ProviderFile))
	mustSetup(t, v, "correct horse battery staple")
	v.Seal()

	if got := v.State(); got != StateSealed {
		t.Fatalf("precondition: state = %v, want sealed", got)
	}
	if _, err := v.Purge(context.Background()); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if got := v.State(); got != StateUninitialized {
		t.Errorf("state after Purge = %v, want uninitialized", got)
	}
}

// The document is what makes a vault exist, so a purged vault must not come
// back on the next start. Reconstructing from the same store is the only
// assertion that can tell "forgot in memory" from "gone".
func TestVault_Purge_DoesNotSurviveAReconstruct(t *testing.T) {
	loweredCost(t)
	v, store, _ := testVault(t, newTestProvider(ProviderSystem), newTestFileProvider(ProviderFile))
	mustSetup(t, v, "correct horse battery staple")

	if _, err := v.Purge(context.Background()); err != nil {
		t.Fatalf("Purge: %v", err)
	}

	reg, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	again, err := New(store, reg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("New after Purge: %v", err)
	}
	t.Cleanup(again.Close)
	if got := again.State(); got != StateUninitialized {
		t.Errorf("reconstructed state = %v, want uninitialized", got)
	}
}

// Re-running an interrupted reset must succeed rather than report that there
// is nothing to purge. There is no journal here; re-running IS the recovery.
func TestVault_Purge_IsIdempotent(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderSystem), newTestFileProvider(ProviderFile))
	mustSetup(t, v, "correct horse battery staple")

	if _, err := v.Purge(context.Background()); err != nil {
		t.Fatalf("first Purge: %v", err)
	}
	if _, err := v.Purge(context.Background()); err != nil {
		t.Errorf("second Purge: %v, want nil", err)
	}
}

// A provider that cannot be reached must not stop the rest. The keychain being
// unavailable is an ordinary Linux state, and refusing to clear the vault
// because of it would leave the user locked out with no way back — so the
// error is reported AND the vault is still cleared.
func TestVault_Purge_ReportsAFailedProviderAndStillClearsTheVault(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(
		t,
		newTestProvider(ProviderSystem),
		newTestFileProvider(ProviderFile),
		&refusingPurgeProvider{id: "stubborn"},
	)
	mustSetup(t, v, "correct horse battery staple")

	failures, err := v.Purge(context.Background())
	if err == nil {
		t.Fatal("Purge returned nil despite a provider that could not be purged")
	}
	if len(failures) != 1 || failures[0].Provider != "stubborn" {
		t.Errorf("failures = %+v, want one naming the stubborn provider", failures)
	}
	if got := v.State(); got != StateUninitialized {
		t.Errorf("state = %v, want uninitialized — the vault is cleared regardless", got)
	}
}

// refusingPurgeProvider is registered, purgeable, and always fails.
type refusingPurgeProvider struct{ id ProviderID }

func (p *refusingPurgeProvider) ID() ProviderID { return p.id }

func (p *refusingPurgeProvider) Status(_ context.Context) Status {
	return Status{Ready: true}
}

func (p *refusingPurgeProvider) Get(_ context.Context, _ credential.SecretID) (credential.Secret, error) {
	return credential.Secret{}, errors.New("not implemented")
}

func (p *refusingPurgeProvider) PurgeAll(_ context.Context) error {
	return errors.New("keychain is not answering")
}

// ---------------------------------------------------------------------------
// ADR-0016 — the secret owns its name: records
// ---------------------------------------------------------------------------

// CreateNamed persists the catalogue record in the same journal step that
// advances to PhaseSecretWritten, and the journal entry carries the metadata
// from PhasePrepared — the name joins the sequence, never a second path.
func TestCreateNamed_PersistsRecordAndJournal(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	fp := newTestFileProvider(ProviderFile)
	v, _, _ := testVault(t, sys, fp)
	mustSetup(t, v, "")
	defer v.Close()

	id, err := v.CreateNamed(context.Background(), credential.NewSecret("pw"),
		SecretMeta{Name: "root@192.168.0.57", Kind: "password"})
	if err != nil {
		t.Fatalf("CreateNamed: %v", err)
	}

	var rec *SecretRecord
	for i := range v.doc.Secrets {
		if v.doc.Secrets[i].ID == id {
			rec = &v.doc.Secrets[i]
			break
		}
	}
	if rec == nil {
		t.Fatal("no catalogue record for the created secret")
	}
	if rec.Name != "root@192.168.0.57" {
		t.Errorf("record name = %q, want %q", rec.Name, "root@192.168.0.57")
	}
	if rec.Kind != "password" {
		t.Errorf("record kind = %q, want %q", rec.Kind, "password")
	}

	// The journal entry at PhasePrepared/PhaseSecretWritten carries the same
	// metadata — that is the "name joins the sequence" part.
	var entry *JournalEntry
	for i := range v.doc.Journal {
		if v.doc.Journal[i].NewID == id {
			entry = &v.doc.Journal[i]
			break
		}
	}
	if entry == nil {
		t.Fatal("no journal entry for the created secret")
	}
	if entry.Name != "root@192.168.0.57" || entry.Kind != "password" {
		t.Errorf("journal carries name=%q kind=%q", entry.Name, entry.Kind)
	}
	if entry.Phase != PhaseSecretWritten {
		t.Errorf("journal phase = %q, want %q", entry.Phase, PhaseSecretWritten)
	}
}

// The nameless form (the SecretStore interface) still records: the record is
// the durable proof that lets Reconcile keep the secret, and a nameless
// record renders by fallback.
func TestCreate_RecordsNamelessPasswordKind(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderSystem))
	mustSetup(t, v, "")
	defer v.Close()

	id, err := v.Create(context.Background(), credential.NewSecret("pw"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec, ok := recordFor(v.doc.Secrets, id)
	if !ok {
		t.Fatal("Create left no catalogue record")
	}
	if rec.Kind != "password" {
		t.Errorf("record kind = %q, want %q", rec.Kind, "password")
	}
	if rec.Name != "" {
		t.Errorf("record name = %q, want empty", rec.Name)
	}
}

// Delete removes the catalogue record with the metadata-first journal write:
// no dangling row after the secret is gone.
func TestDelete_RemovesRecord(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderSystem))
	mustSetup(t, v, "")
	defer v.Close()

	id, err := v.CreateNamed(context.Background(), credential.NewSecret("pw"),
		SecretMeta{Name: "prod password", Kind: "password"})
	if err != nil {
		t.Fatalf("CreateNamed: %v", err)
	}
	if _, ok := recordFor(v.doc.Secrets, id); !ok {
		t.Fatal("record missing before delete")
	}

	if err := v.Delete(context.Background(), id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := recordFor(v.doc.Secrets, id); ok {
		t.Error("record still present after Delete")
	}
}

// RenameSecret addresses a row by its renderer-addressable handle and sets
// the name on the vault's own record. It must never accept a SecretID.
func TestRenameSecret_RecordRow(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderSystem))
	mustSetup(t, v, "")
	defer v.Close()

	id, err := v.CreateNamed(context.Background(), credential.NewSecret("pw"),
		SecretMeta{Name: "old name", Kind: "key-passphrase"})
	if err != nil {
		t.Fatalf("CreateNamed: %v", err)
	}

	if err := v.RenameSecret(context.Background(), rowID(id), "the prod passphrase", nil); err != nil {
		t.Fatalf("RenameSecret: %v", err)
	}

	rec, ok := recordFor(v.doc.Secrets, id)
	if !ok {
		t.Fatal("record missing after rename")
	}
	if rec.Name != "the prod passphrase" {
		t.Errorf("record name = %q, want %q", rec.Name, "the prod passphrase")
	}
	// The kind's owner is the vault: a rename never changes it.
	if rec.Kind != "key-passphrase" {
		t.Errorf("record kind = %q, want %q", rec.Kind, "key-passphrase")
	}
}

// RenameSecret resolves rows that were never recorded — pre-ADR-0016
// references — through the credential metadata, and gives them a record.
func TestRenameSecret_UnrecordedRefRow(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderSystem))
	mustSetup(t, v, "")
	defer v.Close()

	ref := credential.SecretID(refSys)
	inputs := []CredentialInventory{
		{ID: "cred:legacy:1", Username: "deploy", AuthMode: "password", SecretID: refSys},
	}

	if err := v.RenameSecret(context.Background(), rowID(ref), "deploy box", inputs); err != nil {
		t.Fatalf("RenameSecret: %v", err)
	}

	rec, ok := recordFor(v.doc.Secrets, ref)
	if !ok {
		t.Fatal("rename did not record the unrecorded ref")
	}
	if rec.Name != "deploy box" {
		t.Errorf("record name = %q, want %q", rec.Name, "deploy box")
	}
	if rec.Kind != "password" {
		t.Errorf("record kind = %q, want %q", rec.Kind, "password")
	}
}

func TestRenameSecret_RejectsEmptyName(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderSystem))
	mustSetup(t, v, "")
	defer v.Close()

	id, err := v.CreateNamed(context.Background(), credential.NewSecret("pw"),
		SecretMeta{Name: "named", Kind: "password"})
	if err != nil {
		t.Fatalf("CreateNamed: %v", err)
	}

	if err := v.RenameSecret(context.Background(), rowID(id), "   ", nil); err == nil {
		t.Error("rename with a blank name should be refused")
	}
	rec, _ := recordFor(v.doc.Secrets, id)
	if rec.Name != "named" {
		t.Errorf("name changed to %q despite refusal", rec.Name)
	}
}

func TestRenameSecret_UnknownRow(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderSystem))
	mustSetup(t, v, "")
	defer v.Close()

	if err := v.RenameSecret(context.Background(), "secrow:00000000000000000000000000000000", "x", nil); err == nil {
		t.Error("rename of an unknown row should fail")
	}
}

func TestRenameSecret_RejectsSecretID(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderSystem))
	mustSetup(t, v, "")
	defer v.Close()

	id, err := v.CreateNamed(context.Background(), credential.NewSecret("pw"),
		SecretMeta{Name: "named", Kind: "password"})
	if err != nil {
		t.Fatalf("CreateNamed: %v", err)
	}

	// A SecretID is not a row handle: it must not address the secret
	// (nocx-jb20.1 — the reference is never accepted as an identifier).
	if err := v.RenameSecret(context.Background(), string(id), "x", nil); err == nil {
		t.Error("rename addressed by SecretID should fail")
	}
	rec, _ := recordFor(v.doc.Secrets, id)
	if rec.Name != "named" {
		t.Errorf("name changed to %q despite refusal", rec.Name)
	}
}

// ── ReplaceSecret ──────────────────────────────────────────────────────

// ReplaceSecret overwrites the material behind an existing secret, addressed
// by the renderer-addressable row handle. The reference must NOT change: the
// new value lands under the SAME SecretID, so every connection referencing
// the secret keeps working, and the catalogue record (name, kind) is
// untouched.
func TestReplaceSecret_OverwritesValueKeepsReference(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderSystem))
	mustSetup(t, v, "")
	defer v.Close()

	id, err := v.CreateNamed(context.Background(), credential.NewSecret("old value"),
		SecretMeta{Name: "prod password", Kind: "password"})
	if err != nil {
		t.Fatalf("CreateNamed: %v", err)
	}

	if repErr := v.ReplaceSecret(context.Background(), rowID(id), credential.NewSecret("new value"), nil); repErr != nil {
		t.Fatalf("ReplaceSecret: %v", repErr)
	}

	// The reference is the same id — nothing was repointed.
	got, err := v.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get after replace: %v", err)
	}
	var val string
	if err := got.Use(func(b []byte) error { val = string(b); return nil }); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if val != "new value" {
		t.Errorf("value = %q, want %q", val, "new value")
	}

	// The record survived untouched: same name, same kind.
	rec, ok := recordFor(v.doc.Secrets, id)
	if !ok {
		t.Fatal("record missing after replace")
	}
	if rec.Name != "prod password" {
		t.Errorf("record name = %q, want %q", rec.Name, "prod password")
	}
	if rec.Kind != "password" {
		t.Errorf("record kind = %q, want %q", rec.Kind, "password")
	}
	// No orphan: the id still names exactly one value.
	if len(v.doc.Secrets) != 1 {
		t.Errorf("catalogue has %d records, want 1", len(v.doc.Secrets))
	}
}

// ReplaceSecret resolves rows that were never recorded — pre-ADR-0016
// references — through the credential metadata, exactly as RenameSecret does.
// It overwrites the value; it does not mint a record (nothing about the
// metadata changed).
func TestReplaceSecret_UnrecordedRefRow(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderSystem))
	mustSetup(t, v, "")
	defer v.Close()

	ref := credential.SecretID(refSys)
	if err := v.ReplaceSecret(context.Background(), "sec:not-a-row", credential.NewSecret("x"), nil); err == nil {
		t.Fatal("unknown row should fail")
	}

	// Put a real value under the legacy reference so Get has something to see.
	prov, _ := v.reg.Writable(ProviderSystem)
	if err := prov.Put(context.Background(), ref, credential.NewSecret("legacy key")); err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	inputs := []CredentialInventory{
		{ID: "cred:legacy:1", Username: "deploy", AuthMode: "publicKey", SecretID: refSys},
	}

	if err := v.ReplaceSecret(context.Background(), rowID(ref), credential.NewSecret("replaced key"), inputs); err != nil {
		t.Fatalf("ReplaceSecret on unrecorded row: %v", err)
	}

	got, err := v.Get(context.Background(), ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var val string
	_ = got.Use(func(b []byte) error { val = string(b); return nil })
	if val != "replaced key" {
		t.Errorf("value = %q, want %q", val, "replaced key")
	}
	if _, ok := recordFor(v.doc.Secrets, ref); ok {
		t.Error("replace must not mint a record for an unrecorded row")
	}
}

// The renderer may not name a secret (nocx-jb20.1): replace addresses rows by
// the row handle, and a SecretID sent in its place must be refused.
func TestReplaceSecret_RejectsSecretID(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderSystem))
	mustSetup(t, v, "")
	defer v.Close()

	id, err := v.CreateNamed(context.Background(), credential.NewSecret("pw"),
		SecretMeta{Name: "named", Kind: "password"})
	if err != nil {
		t.Fatalf("CreateNamed: %v", err)
	}
	if err := v.ReplaceSecret(context.Background(), string(id), credential.NewSecret("x"), nil); err == nil {
		t.Error("replace addressed by a SecretID should be refused")
	}
}

func TestReplaceSecret_UnknownRow(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderSystem))
	mustSetup(t, v, "")
	defer v.Close()

	if err := v.ReplaceSecret(context.Background(), "secrow:00000000000000000000000000000000", credential.NewSecret("x"), nil); err == nil {
		t.Error("replace of an unknown row should fail")
	}
}

func TestReplaceSecret_RejectsSealedVault(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderSystem))
	mustSetup(t, v, "")

	id, err := v.CreateNamed(context.Background(), credential.NewSecret("pw"),
		SecretMeta{Name: "named", Kind: "password"})
	if err != nil {
		t.Fatalf("CreateNamed: %v", err)
	}
	v.Seal()

	if err := v.ReplaceSecret(context.Background(), rowID(id), credential.NewSecret("x"), nil); !errors.Is(err, ErrVaultSealed) {
		t.Errorf("sealed vault: err = %v, want ErrVaultSealed", err)
	}
}

// The journal precedes the provider write, exactly as it does for Create
// (spec §4.2): a replace that times out may still land, and the entry is what
// makes the outcome reconcilable.
func TestReplaceSecret_JournalBeforePut(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	v, _, _ := testVault(t, sys)
	mustSetup(t, v, "")

	id, err := v.CreateNamed(context.Background(), credential.NewSecret("old"),
		SecretMeta{Name: "named", Kind: "password"})
	if err != nil {
		t.Fatalf("CreateNamed: %v", err)
	}

	var journaled atomic.Bool
	sys.putHook = func() {
		v.mu.Lock()
		defer v.mu.Unlock()
		for _, e := range v.doc.Journal {
			if e.Op == "replace" && e.NewID == id && e.Phase == PhasePrepared {
				journaled.Store(true)
			}
		}
	}
	if err := v.ReplaceSecret(context.Background(), rowID(id), credential.NewSecret("new"), nil); err != nil {
		t.Fatalf("ReplaceSecret: %v", err)
	}
	if !journaled.Load() {
		t.Fatal("journal entry (replace, PhasePrepared) must exist when the provider is called")
	}
	// The entry is cleared once the write completed: nothing to reconcile.
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, e := range v.doc.Journal {
		if e.Op == "replace" {
			t.Errorf("journal entry survived a successful replace: %+v", e)
		}
	}
}

// A failed provider write leaves the entry in the journal rather than
// pretending nothing happened: the write may have landed despite the error
// (a keyring timeout), and the entry is what reconciliation acts on.
func TestReplaceSecret_ProviderFailureLeavesJournalEntry(t *testing.T) {
	loweredCost(t)
	sys := newTestProvider(ProviderSystem)
	v, _, _ := testVault(t, sys)
	mustSetup(t, v, "")

	id, err := v.CreateNamed(context.Background(), credential.NewSecret("old"),
		SecretMeta{Name: "named", Kind: "password"})
	if err != nil {
		t.Fatalf("CreateNamed: %v", err)
	}

	sys.fail = errors.New("keyring is having a bad day")
	if err := v.ReplaceSecret(context.Background(), rowID(id), credential.NewSecret("new"), nil); err == nil {
		t.Fatal("replace should fail when the provider fails")
	}
	sys.fail = nil

	v.mu.Lock()
	defer v.mu.Unlock()
	found := false
	for _, e := range v.doc.Journal {
		if e.Op == "replace" && e.NewID == id {
			found = true
		}
	}
	if !found {
		t.Error("journal entry should survive a failed provider write")
	}
}
