package vault

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/storage"
)

// State models the vault lifecycle.
type State int

const (
	// StateUninitialized means no key material exists — Setup is required.
	StateUninitialized State = iota
	// StateSealed means the root key is not in memory.
	StateSealed
	// StateUnsealed means the root key is in memory and operations are permitted.
	StateUnsealed
)

var stateNames = map[State]string{
	StateUninitialized: "uninitialized",
	StateSealed:        "sealed",
	StateUnsealed:      "unsealed",
}

func (s State) String() string { return stateNames[s] }

// SetupRequest carries the parameters for first-time initialization.
type SetupRequest struct {
	// Passphrase is the master passphrase. When empty the vault probes the
	// system provider for silent setup (spec §5.2).
	Passphrase string
}

// SetupResult reports the outcome of initialization.
type SetupResult struct {
	// RecoveryCode is set only when Setup ran with a passphrase. It is empty
	// after a silent setup.
	RecoveryCode string
}

// UnsealRequest carries the means by which the caller wants to unseal.
type UnsealRequest struct {
	Passphrase   string
	RecoveryCode string
	UseOSKey     bool
}

// Vault owns the seal lifecycle, generation counter, provider routing and the
// credential.SecretStore interface. It serialises mutations with a single
// mutex (spec §4.5) but releases it before calling any provider method, then
// re-acquires it to record the outcome (ADR-0011 §4).
type Vault struct {
	mu           sync.Mutex
	gen          uint64 // incremented on each Seal
	store        storage.DocumentStore
	reg          *Registry
	doc          Document
	rootKey      []byte // nil when sealed
	logger       *slog.Logger
	initializing bool // guards concurrent Setup (defect 7)

	autoSealWake       chan struct{} // buffered(1), wakes the auto-seal goroutine
	autoSealQuit       chan struct{} // closed by Close to stop the goroutine
	closeOnce          sync.Once
	autoSealEpoch      uint64                  // incremented on each Activity/SetAutoSeal
	autoSealDurationFn func(int) time.Duration // minutes→duration; overridden by tests
}

// New loads the vault document, decides the initial state, and runs
// Reconcile once before returning. A vault that starts serving without
// reconciling can hand out a reference the journal was about to retire.
// Reconciliation failures are logged but do not block construction.
func New(docs storage.DocumentStore, reg *Registry, logger *slog.Logger) (*Vault, error) {
	doc, found, err := loadDocument(docs)
	if err != nil {
		return nil, err
	}
	v := &Vault{
		store:              docs,
		reg:                reg,
		doc:                doc,
		logger:             logger,
		autoSealWake:       make(chan struct{}, 1),
		autoSealQuit:       make(chan struct{}),
		autoSealDurationFn: defaultAutoSealDuration,
	}
	if found {
		// Reconcile before returning — provider calls happen here, outside any
		// vault lock (this is construction, no lock to hold).
		blocked := Reconcile(context.Background(), &v.doc, reg)
		for _, e := range blocked {
			logger.Warn("reconciliation blocked", "entry", e.String())
		}
		if err := saveDocument(docs, v.doc); err != nil {
			return nil, fmt.Errorf("save after reconcile: %w", err)
		}
	}

	if !found {
		// Fresh vaults default to 15-minute auto-seal timeout.
		v.doc.AutoSealMinutes = defaultAutoSealMinutes
	}

	// Start auto-seal goroutine after all initialization succeeds so a
	// construction error does not leak it.
	go v.autoSealLoop()

	return v, nil
}

// State returns the current vault lifecycle state.
func (v *Vault) State() State {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.stateLocked()
}

func (v *Vault) stateLocked() State {
	if v.doc.Instance == "" {
		return StateUninitialized
	}
	if v.rootKey == nil {
		return StateSealed
	}
	return StateUnsealed
}

// Setup initialises the vault for the first time.
//
// When req.Passphrase is empty and the system provider is registered and
// reports ready, setup is silent: a root key is minted and stored as an
// OS-held copy. No passphrase envelope, no recovery code.
//
// When req.Passphrase is non-empty, a passphrase envelope and a recovery
// code are created and stored in the document.
func (v *Vault) Setup(ctx context.Context, req SetupRequest) (SetupResult, error) {
	v.mu.Lock()
	if v.stateLocked() != StateUninitialized || v.initializing {
		v.mu.Unlock()
		return SetupResult{}, errors.New("vault is already initialized")
	}
	v.initializing = true
	v.mu.Unlock()

	// --- clean up initializing on error, and on success too ---
	var setupOK bool
	defer func() {
		if !setupOK {
			v.mu.Lock()
			v.initializing = false
			v.mu.Unlock()
		}
	}()

	// --- determine mode ---
	//
	// System readiness is probed HERE, before the document lock, because a
	// provider is never called while that lock is held (ADR-0011 §4). It also
	// has to be probed at all: the registry answers whether a provider is
	// REGISTERED, and app.go registers the system provider on every platform,
	// so "registered" is true on machines with no Secret Service at all.
	silent := req.Passphrase == ""
	sysReady := false
	if sys, sysOK := v.reg.Get(ProviderSystem); sysOK {
		sysReady = sys.Status(ctx).Ready
		if silent && !sysReady {
			return SetupResult{}, fmt.Errorf("system provider not ready (%s): provide a passphrase",
				sys.Status(ctx).Reason)
		}
	} else if silent {
		return SetupResult{}, fmt.Errorf("silent setup requires system provider: %w", ErrProviderUnavailable)
	}

	// --- mint root key ---
	root, err := newRootKey()
	if err != nil {
		return SetupResult{}, fmt.Errorf("mint root key: %w", err)
	}

	// --- generate instance id ---
	var instBuf [16]byte
	if _, err := rand.Read(instBuf[:]); err != nil {
		return SetupResult{}, fmt.Errorf("instance id: %w", err)
	}
	instance := hex.EncodeToString(instBuf[:])

	var result SetupResult
	var oskID credential.SecretID

	v.mu.Lock()
	origDoc := v.doc // save for rollback on provider-init failure
	v.doc.Instance = instance

	sysProv, _ := v.reg.Writable(ProviderSystem)

	if silent && sysProv != nil {
		// Store root key in system provider. Release lock for the call.
		oskID = osKeyID(instance)
		rootSecret := credential.NewSecretBytes(root)
		v.mu.Unlock()
		putErr := sysProv.Put(ctx, oskID, rootSecret)
		v.mu.Lock()
		if putErr != nil {
			for i := range len(root) {
				root[i] = 0
			}
			v.doc = origDoc
			v.initializing = false
			v.mu.Unlock()
			return SetupResult{}, fmt.Errorf("store OS-held key: %w", putErr)
		}
		v.doc.HasOSKey = true
	} else {
		// Passphrase-based setup — wrap root key and generate recovery code.
		e, err := wrapWithPassphrase(root, req.Passphrase)
		if err != nil {
			for i := range len(root) {
				root[i] = 0
			}
			v.doc = origDoc
			v.initializing = false
			v.mu.Unlock()
			return SetupResult{}, fmt.Errorf("wrap passphrase: %w", err)
		}
		v.doc.Passphrase = &e

		// Generate a recovery code wrapping THE EXISTING root key.
		// NOT newRecoveryCode, which mints a new root along with the code.
		var raw [16]byte
		if _, rerr := rand.Read(raw[:]); rerr != nil {
			for i := range len(root) {
				root[i] = 0
			}
			v.doc = origDoc
			v.initializing = false
			v.mu.Unlock()
			return SetupResult{}, fmt.Errorf("generate recovery code: %w", rerr)
		}
		code := crockfordEncode(raw[:])
		recEnv, err := wrapWithPassphrase(root, code)
		if err != nil {
			for i := range len(root) {
				root[i] = 0
			}
			v.doc = origDoc
			v.initializing = false
			v.mu.Unlock()
			return SetupResult{}, fmt.Errorf("wrap recovery: %w", err)
		}
		v.doc.Recovery = &recEnv
		result.RecoveryCode = code
	}

	// Set default provider.
	//
	// Readiness, not mere registration. Choosing the system provider because it
	// exists sends every secret to a keychain that is not there: on any machine
	// without a Secret Service, passphrase setup succeeded and then the first
	// password save failed with "provider system unavailable (no-service)".
	// Nothing caught it, because the only double for a provider reported itself
	// ready unconditionally.
	if sysProv != nil && sysReady {
		v.doc.DefaultProvider = ProviderSystem
	} else {
		v.doc.DefaultProvider = ProviderFile
	}

	// Snapshot state and release lock before provider init/unlock calls.
	// ADR-0011 §4: never call a provider while holding the document lock.
	providers := make([]Provider, len(v.reg.List()))
	copy(providers, v.reg.List())
	instanceID := v.doc.Instance
	v.rootKey = root
	v.mu.Unlock()

	// Track lockable providers for rollback on failure (defect 3 pattern).
	type lockable interface{ Lock() }
	unlocked := make([]lockable, 0, len(providers))

	// Initialise file providers outside lock: SetInstanceID, then Unlock (sets
	// rootKey), then NewDataKey (needs rootKey from Unlock).
	// Propagate failures so we don't return an unsealed vault with a locked
	// provider (matches Unseal's error handling).
	for _, p := range providers {
		if dkc, ok := p.(dataKeyCreator); ok {
			dkc.SetInstanceID(instanceID)
		}
		if u, ok := p.(unlocker); ok {
			if err := u.Unlock(root); err != nil {
				// Roll back: re-lock any providers already unlocked.
				for i := range len(root) {
					root[i] = 0
				}
				v.mu.Lock()
				v.rootKey = nil
				v.doc = origDoc
				v.initializing = false
				rollback := make([]lockable, len(unlocked))
				copy(rollback, unlocked)
				v.mu.Unlock()
				for _, lk := range rollback {
					lk.Lock()
				}
				// Best-effort clean up OS key stored before provider init.
				if oskID != "" && sysProv != nil {
					v.reportOrphanedOSKey(ctx, sysProv, oskID)
				}
				return SetupResult{}, fmt.Errorf("unlock provider %s: %w", p.ID(), err)
			}
			if lk, ok := p.(locker); ok {
				unlocked = append(unlocked, lk)
			}
		}
	}
	for _, p := range providers {
		if dkc, ok := p.(dataKeyCreator); ok {
			if _, err := dkc.NewDataKey(); err != nil {
				// Roll back: re-lock any providers already unlocked.
				for i := range len(root) {
					root[i] = 0
				}
				v.mu.Lock()
				v.rootKey = nil
				v.doc = origDoc
				v.initializing = false
				rollback := make([]lockable, len(unlocked))
				copy(rollback, unlocked)
				v.mu.Unlock()
				for _, lk := range rollback {
					lk.Lock()
				}
				// Best-effort clean up OS key stored before provider init.
				if oskID != "" && sysProv != nil {
					v.reportOrphanedOSKey(ctx, sysProv, oskID)
				}
				return SetupResult{}, fmt.Errorf("new data key for %s: %w", p.ID(), err)
			}
		}
	}

	// --- save document AFTER successful provider init (defect 5 fix) ---
	v.mu.Lock()
	if saveErr := saveDocument(v.store, v.doc); saveErr != nil {
		// Best-effort clean up OS key (defect 6). MUST NOT call provider
		// while holding lock — snapshot, unlock, call delete, re-lock.
		v.mu.Unlock()
		if oskID != "" && sysProv != nil {
			v.reportOrphanedOSKey(ctx, sysProv, oskID)
		}
		// Wipe root bytes and re-lock providers before returning.
		for i := range len(root) {
			root[i] = 0
		}
		v.mu.Lock()
		v.rootKey = nil
		v.doc = origDoc
		v.initializing = false
		rollback := make([]lockable, len(unlocked))
		copy(rollback, unlocked)
		v.mu.Unlock()
		for _, lk := range rollback {
			lk.Lock()
		}
		return SetupResult{}, fmt.Errorf("save document: %w", saveErr)
	}
	v.initializing = false
	setupOK = true
	// Increment epoch so a stale auto-seal timer (armed before setup) will not
	// fire and immediately re-seal the freshly initialized vault.
	v.autoSealEpoch++
	v.mu.Unlock()

	v.wakeAutoSeal()
	v.logger.Info(
		"vault initialized",
		"state", "unsealed",
		"defaultProvider", v.doc.DefaultProvider,
		"hasOSKey", v.doc.HasOSKey,
	)

	return result, nil
}

// Unseal recovers the root key using the requested means and unlocks the
// file provider. Returns ErrUnsealFailed when the passphrase, recovery code
// or OS-held key cannot be used.
func (v *Vault) Unseal(ctx context.Context, req UnsealRequest) error {
	v.mu.Lock()
	switch v.stateLocked() {
	case StateUninitialized:
		v.mu.Unlock()
		return ErrVaultUninitialized
	case StateUnsealed:
		v.mu.Unlock()
		return nil // idempotent
	}
	v.mu.Unlock() // release for provider calls

	t0 := time.Now()

	var root []byte
	var err error

	switch {
	case req.UseOSKey:
		root, err = v.unsealWithOSKey(ctx)
	case req.Passphrase != "":
		root, err = v.unsealWithPassphrase(req.Passphrase)
	case req.RecoveryCode != "":
		root, err = v.unsealWithRecoveryCode(req.RecoveryCode)
	default:
		return ErrUnsealFailed
	}

	if err != nil {
		v.logger.Warn("unseal failed", "error", err, "duration", time.Since(t0))
		return err
	}

	v.mu.Lock()

	// Guard: vault may have been unsealed by a concurrent caller.
	if v.rootKey != nil {
		v.mu.Unlock()
		return nil
	}

	gen := v.gen

	// Snapshot providers and set rootKey, then release lock before calling
	// provider methods (ADR-0011 §4).
	providers := make([]Provider, len(v.reg.List()))
	copy(providers, v.reg.List())
	v.rootKey = root
	v.mu.Unlock()

	// Track lockable providers that we unlock, for rollback on error or race.
	type lockable interface{ Lock() }
	unlocked := make([]lockable, 0, len(providers))

	// Unlock any provider that needs the root key (file provider).
	for _, p := range providers {
		if u, ok := p.(unlocker); ok {
			if err := u.Unlock(root); err != nil {
				// Unlock failed — wipe candidate root bytes, re-lock any we
				// already unlocked, wipe rootKey, and return sealed.
				for i := range len(root) {
					root[i] = 0
				}
				v.mu.Lock()
				v.rootKey = nil
				rollback := make([]lockable, len(unlocked))
				copy(rollback, unlocked)
				v.mu.Unlock()
				for _, lk := range rollback {
					lk.Lock()
				}
				v.logger.Warn("unseal failed", "error", err, "duration", time.Since(t0))
				return fmt.Errorf("unlock provider %s: %w", p.ID(), err)
			}
			if lk, ok := p.(locker); ok {
				unlocked = append(unlocked, lk)
			}
		}
	}
	// Re-acquire lock and check that no Seal happened during our Unlock calls.
	v.mu.Lock()
	if v.gen != gen || v.rootKey == nil {
		// Concurrent Seal: re-lock providers we just unlocked,
		// wipe the root key, and fail the unseal.
		for i := range len(root) {
			root[i] = 0
		}
		v.rootKey = nil
		rollback := make([]lockable, len(unlocked))
		copy(rollback, unlocked)
		v.mu.Unlock()
		for _, lk := range rollback {
			lk.Lock()
		}
		v.logger.Warn("unseal rejected by concurrent seal", "duration", time.Since(t0))
		return ErrVaultSealed
	}
	// Increment epoch so a stale auto-seal timer (armed before this unseal)
	// will not fire and immediately re-seal.
	v.autoSealEpoch++
	v.mu.Unlock()

	v.wakeAutoSeal()
	v.logger.Info("vault unsealed", "duration", time.Since(t0))
	return nil
}

func (v *Vault) unsealWithOSKey(ctx context.Context) ([]byte, error) {
	if !v.doc.HasOSKey {
		return nil, fmt.Errorf("%w: no OS-held key configured", ErrUnsealFailed)
	}
	sysProv, ok := v.reg.Writable(ProviderSystem)
	if !ok {
		return nil, fmt.Errorf("%w: system provider not available", ErrUnsealFailed)
	}
	oskID := osKeyID(v.doc.Instance)
	sec, err := sysProv.Get(ctx, oskID)
	if err != nil {
		return nil, fmt.Errorf("%w: read OS-held key: %w", ErrUnsealFailed, err)
	}
	if sec.IsEmpty() {
		return nil, fmt.Errorf("%w: OS-held key not found", ErrUnsealFailed)
	}
	var root []byte
	if useErr := sec.Use(func(b []byte) error {
		root = make([]byte, len(b))
		copy(root, b)
		return nil
	}); useErr != nil {
		return nil, fmt.Errorf("%w: read OS-held key: %w", ErrUnsealFailed, useErr)
	}
	return root, nil
}

func (v *Vault) unsealWithPassphrase(pass string) ([]byte, error) {
	if v.doc.Passphrase == nil {
		return nil, fmt.Errorf("%w: no passphrase envelope", ErrUnsealFailed)
	}
	root, err := unwrapWithPassphrase(*v.doc.Passphrase, pass)
	if err != nil {
		return nil, err // already ErrUnsealFailed
	}
	return root, nil
}

func (v *Vault) unsealWithRecoveryCode(code string) ([]byte, error) {
	if v.doc.Recovery == nil {
		return nil, fmt.Errorf("%w: no recovery envelope", ErrUnsealFailed)
	}
	root, err := unwrapWithPassphrase(*v.doc.Recovery, code)
	if err != nil {
		return nil, err // already ErrUnsealFailed
	}
	return root, nil
}

// Seal transitions the vault to sealed state: the root key is wiped from
// memory, any lockable provider is locked, and the generation counter is
// incremented. Operations in flight that complete after Seal will have their
// results rejected.
func (v *Vault) Seal() {
	v.mu.Lock()

	// Already sealed is idempotent.
	if v.rootKey == nil {
		v.mu.Unlock()
		return
	}

	v.gen++
	for i := range len(v.rootKey) {
		v.rootKey[i] = 0
	}
	v.rootKey = nil

	// Snapshot lockable providers and release lock before calling Lock()
	// on them (ADR-0011 §4).
	providers := make([]Provider, len(v.reg.List()))
	copy(providers, v.reg.List())
	gen := v.gen
	v.mu.Unlock()

	// Wake auto-seal goroutine so it stops the timer — the vault is no longer
	// unsealed. The goroutine reads fresh state under the lock, so this is safe
	// to call after releasing v.mu.
	v.wakeAutoSeal()

	for _, p := range providers {
		if lk, ok := p.(locker); ok {
			lk.Lock()
		}
	}

	v.logger.Info("vault sealed", "generation", gen)
}

// defaultAutoSealMinutes is the factory default for newly initialized vaults.
const defaultAutoSealMinutes = 15

// defaultAutoSealDuration converts minutes to a timer duration. Tests override
// this via Vault.autoSealDurationFn to use sub-minute intervals.
func defaultAutoSealDuration(minutes int) time.Duration {
	return time.Duration(minutes) * time.Minute
}

// validateAutoSealMinutes returns an error when minutes is not one of the
// accepted values (0 = off, 5, 15, 30, 60).
func validateAutoSealMinutes(minutes int) error {
	switch minutes {
	case 0, 5, 15, 30, 60:
		return nil
	default:
		return fmt.Errorf("auto-seal minutes must be 0 (off), 5, 15, 30, or 60; got %d", minutes)
	}
}

// Activity reports user activity (keyboard, mouse, UI actions) to reset the
// idle auto-seal timer. This is an explicit signal from the frontend — it is
// never inferred from terminal output, background jobs, network events, or
// incoming WebSocket messages.
func (v *Vault) Activity() {
	v.mu.Lock()
	v.autoSealEpoch++
	v.mu.Unlock()
	v.wakeAutoSeal()
}

// SetAutoSeal configures the idle auto-seal timeout and persists it. Accepted
// values: 0 (off), 5, 15, 30, 60. When the vault is unsealed the timer is
// armed immediately with the new interval.
func (v *Vault) SetAutoSeal(ctx context.Context, minutes int) error {
	if err := validateAutoSealMinutes(minutes); err != nil {
		return err
	}

	v.mu.Lock()
	oldMins := v.doc.AutoSealMinutes
	oldEpoch := v.autoSealEpoch
	v.doc.AutoSealMinutes = minutes
	v.autoSealEpoch++
	if err := saveDocument(v.store, v.doc); err != nil {
		v.doc.AutoSealMinutes = oldMins
		v.autoSealEpoch = oldEpoch
		v.mu.Unlock()
		return fmt.Errorf("save auto-seal setting: %w", err)
	}
	v.mu.Unlock()

	v.wakeAutoSeal()
	return nil
}

// wakeAutoSeal wakes the auto-seal goroutine so it re-reads vault state and
// arms or stops the timer accordingly. Non-blocking: a goroutine already
// processing a previous wake will pick up the latest state when it loops.
func (v *Vault) wakeAutoSeal() {
	select {
	case v.autoSealWake <- struct{}{}:
	default:
	}
}

// Close stops the auto-seal goroutine and seals the vault.
//
// It is safe to call more than once and safe to call on a vault that was never
// unsealed. Owning the goroutine's lifetime is the point: without it every
// Vault leaks one for the life of the process, which is invisible with the one
// vault an app builds and is dozens in a test binary, each holding its Vault
// alive. Sealing on the way out is not decoration either — Close is what runs
// at shutdown, and leaving the root key in a live heap afterwards would undo
// the reason the seal lifecycle exists.
func (v *Vault) Close() {
	v.closeOnce.Do(func() {
		close(v.autoSealQuit)
	})
	v.Seal()
}

// autoSealLoop is the background goroutine that manages the auto-seal timer.
// It runs until Close.
//
// On each wake event it re-reads vault state under the lock and arms or stops
// the timer. When the timer fires it checks whether the epoch and generation
// are unchanged before calling Seal — ensuring an activity or configuration
// change that arrives near-simultaneously with an expiry wins.
func (v *Vault) autoSealLoop() {
	var t *time.Timer
	var c <-chan time.Time
	var armEpoch uint64

	for {
		select {
		case <-v.autoSealQuit:
			if t != nil {
				t.Stop()
			}
			return

		case <-v.autoSealWake:
			if t != nil {
				t.Stop()
				t, c = nil, nil
			}

			v.mu.Lock()
			mins := v.doc.AutoSealMinutes
			durFn := v.autoSealDurationFn
			isSealed := v.rootKey == nil
			armEpoch = v.autoSealEpoch
			v.mu.Unlock()

			if !isSealed && mins > 0 {
				t = time.NewTimer(durFn(mins))
				c = t.C
			}

		case <-c:
			c = nil

			v.mu.Lock()
			isSealed := v.rootKey == nil
			epoch := v.autoSealEpoch
			v.mu.Unlock()

			if !isSealed && epoch == armEpoch {
				v.Seal()
			}
		}
	}
}

// ChangePassphraseRequest carries the current authentication factor and the
// new passphrase for vault.ChangePassphrase.
type ChangePassphraseRequest struct {
	OldPassphrase string `json:"oldPassphrase,omitempty"`
	RecoveryCode  string `json:"recoveryCode,omitempty"`
	NewPassphrase string `json:"newPassphrase"`
}

// RegenerateRequest carries the passphrase for vault.RegenerateRecovery.
type RegenerateRequest struct {
	Passphrase string `json:"passphrase"`
}

// ChangePassphrase replaces the passphrase envelope. It requires the old
// passphrase or a recovery code (the OS-held key alone is not sufficient):
// a factor that only unlocks must not be able to replace the factor that
// recovers. Unsealing is temporary; rotating is not.
//
// The root key is unwrapped from the existing envelope with the old factor
// and rewrapped with the new passphrase. No secrets are re-encrypted
// (spec §5.1). The operation works sealed because it touches only envelopes
// in the document — no provider call is needed.
func (v *Vault) ChangePassphrase(ctx context.Context, req ChangePassphraseRequest) error {
	v.mu.Lock()
	if v.stateLocked() == StateUninitialized {
		v.mu.Unlock()
		return ErrVaultUninitialized
	}
	if v.doc.Passphrase == nil {
		v.mu.Unlock()
		return fmt.Errorf("no passphrase envelope: %w", ErrUnsealFailed)
	}
	if req.NewPassphrase == "" {
		v.mu.Unlock()
		return fmt.Errorf("new passphrase must not be empty")
	}
	if req.OldPassphrase == "" && req.RecoveryCode == "" {
		v.mu.Unlock()
		return fmt.Errorf("old passphrase or recovery code required")
	}
	// Snapshot envelopes under lock. The root key is extracted from the
	// envelope itself — we never read v.rootKey outside the lock.
	oldPassEnv := *v.doc.Passphrase
	var recovEnv *Envelope
	if v.doc.Recovery != nil {
		r := *v.doc.Recovery
		recovEnv = &r
	}
	gen := v.gen
	v.mu.Unlock()

	// Authenticate using one factor and extract the root key from the envelope.
	var root []byte
	var authErr error
	if req.OldPassphrase != "" {
		root, authErr = unwrapWithPassphrase(oldPassEnv, req.OldPassphrase)
	} else {
		if recovEnv == nil {
			return fmt.Errorf("%w: no recovery envelope", ErrUnsealFailed)
		}
		root, authErr = unwrapWithPassphrase(*recovEnv, req.RecoveryCode)
	}
	if authErr != nil {
		return authErr // already ErrUnsealFailed
	}
	// Wipe root bytes on every return path from here.
	defer func() {
		for i := range len(root) {
			root[i] = 0
		}
	}()

	// Auth passed — wrap the root key with the new passphrase.
	newEnv, err := wrapWithPassphrase(root, req.NewPassphrase)
	if err != nil {
		return fmt.Errorf("wrap new passphrase: %w", err)
	}

	v.mu.Lock()
	// Guard: if the vault was sealed and re-initialized, gen changed.
	if v.gen != gen {
		v.mu.Unlock()
		return ErrVaultSealed
	}
	oldDocEnv := v.doc.Passphrase // save for rollback
	v.doc.Passphrase = &newEnv
	if saveErr := saveDocument(v.store, v.doc); saveErr != nil {
		v.doc.Passphrase = oldDocEnv // restore
		v.mu.Unlock()
		return fmt.Errorf("save document: %w", saveErr)
	}
	v.mu.Unlock()

	return nil
}

// RegenerateRecovery replaces the recovery code envelope. It requires the
// current passphrase (the OS-held key alone is not sufficient): reissuing
// the recovery code must not be possible without the passphrase.
//
// The root key is unwrapped from the passphrase envelope, then wrapped with
// a freshly generated recovery code. A new root key is NOT minted —
// newRecoveryCode would do that, which is wrong here.
func (v *Vault) RegenerateRecovery(ctx context.Context, req RegenerateRequest) (string, error) {
	v.mu.Lock()
	if v.stateLocked() == StateUninitialized {
		v.mu.Unlock()
		return "", ErrVaultUninitialized
	}
	if v.doc.Passphrase == nil {
		v.mu.Unlock()
		return "", fmt.Errorf("no passphrase envelope: %w", ErrUnsealFailed)
	}
	if req.Passphrase == "" {
		v.mu.Unlock()
		return "", fmt.Errorf("passphrase required")
	}
	passEnv := *v.doc.Passphrase
	gen := v.gen
	v.mu.Unlock()

	// Verify passphrase and extract root key.
	root, err := unwrapWithPassphrase(passEnv, req.Passphrase)
	if err != nil {
		return "", err // already ErrUnsealFailed
	}
	// Wipe root bytes on every return path from here.
	defer func() {
		for i := range len(root) {
			root[i] = 0
		}
	}()

	// Generate a fresh recovery code wrapping THE EXISTING root key.
	// Match newRecoveryCode's format: 16 random bytes → Crockford base32.
	var raw [16]byte
	if _, rerr := rand.Read(raw[:]); rerr != nil {
		return "", fmt.Errorf("recovery code: %w", rerr)
	}
	code := crockfordEncode(raw[:])

	env, err := wrapWithPassphrase(root, code)
	if err != nil {
		return "", fmt.Errorf("wrap recovery: %w", err)
	}

	v.mu.Lock()
	if v.gen != gen {
		v.mu.Unlock()
		return "", ErrVaultSealed
	}
	oldRecovery := v.doc.Recovery
	v.doc.Recovery = &env
	if saveErr := saveDocument(v.store, v.doc); saveErr != nil {
		v.doc.Recovery = oldRecovery // restore
		v.mu.Unlock()
		return "", fmt.Errorf("save document: %w", saveErr)
	}
	v.mu.Unlock()

	return code, nil
}

// SetDefaultProvider changes the default provider for new secrets. Existing
// references are immutable — the provider is encoded in the reference itself
// (sec:v1:<provider>:<32hex>), so nothing can migrate without minting a new
// reference (spec §4.1). Only registered, writable providers are accepted.
func (v *Vault) SetDefaultProvider(ctx context.Context, p ProviderID) error {
	v.mu.Lock()
	if v.stateLocked() == StateUninitialized {
		v.mu.Unlock()
		return ErrVaultUninitialized
	}

	// Check the provider is registered AND writable.
	if _, ok := v.reg.Writable(p); !ok {
		if _, exists := v.reg.Get(p); exists {
			v.mu.Unlock()
			return unavailable(p, ReasonDenied, errors.New("provider is not writable"))
		}
		v.mu.Unlock()
		return unavailable(p, ReasonUnknownProvider, fmt.Errorf("provider %q is not registered", p))
	}

	oldDefault := v.doc.DefaultProvider
	v.doc.DefaultProvider = p
	if saveErr := saveDocument(v.store, v.doc); saveErr != nil {
		v.doc.DefaultProvider = oldDefault // restore
		v.mu.Unlock()
		return fmt.Errorf("save document: %w", saveErr)
	}
	v.mu.Unlock()
	return nil
}

// --- credential.SecretStore ---

// Create implements credential.SecretStore: mints a fresh reference, journals
// PhasePrepared, delegates to the default writable provider, then journals
// PhaseSecretWritten together with the catalogue record (ADR-0016). Callers
// that know the secret's name and kind — connection saves, the Secrets page —
// use CreateNamed; this form records a nameless password-kind secret, which
// the surfaces render by fallback.
func (v *Vault) Create(ctx context.Context, value credential.Secret) (credential.SecretID, error) {
	return v.create(ctx, value, SecretMeta{Kind: KindPassword})
}

// CreateNamed is Create with the secret's catalogue metadata: the display
// name (generated from host and user by connection saves, asked of the user
// on the Secrets page) and the kind. The name joins the journal at
// PhasePrepared and is persisted with the record at PhaseSecretWritten — it
// is never written by a second, independent path (ADR-0016).
func (v *Vault) CreateNamed(ctx context.Context, value credential.Secret, meta SecretMeta) (credential.SecretID, error) {
	if err := validateKind(meta.Kind); err != nil {
		return "", err
	}
	meta.Name = strings.TrimSpace(meta.Name)
	return v.create(ctx, value, meta)
}

func (v *Vault) create(ctx context.Context, value credential.Secret, meta SecretMeta) (credential.SecretID, error) {
	t0 := time.Now()

	v.mu.Lock()
	st := v.stateLocked()
	switch st {
	case StateUninitialized:
		v.mu.Unlock()
		v.logger.Info("vault: refusing Create, not initialized")
		return "", ErrVaultUninitialized
	case StateSealed:
		hasInstance := v.doc.Instance != ""
		v.mu.Unlock()
		// Logged because "the page says unlocked and the write says sealed" is
		// otherwise indistinguishable from a genuine contradiction: nothing on
		// this path said anything at INFO, so a whole diagnosis had to proceed
		// by elimination.
		v.logger.Info("vault: refusing Create, sealed", "hasInstance", hasInstance)
		return "", ErrVaultSealed
	}
	gen := v.gen

	prov, err := v.defaultWritableLocked()
	if err != nil {
		v.mu.Unlock()
		return "", err
	}
	provID := prov.ID()

	id, mintErr := mintID(provID)
	if mintErr != nil {
		v.mu.Unlock()
		return "", mintErr
	}
	// Journal PhasePrepared before delegating. The entry carries the
	// catalogue metadata (ADR-0016): the name rides the sequence from here.
	v.doc.Journal = append(v.doc.Journal, JournalEntry{
		Op:    "create",
		NewID: id,
		Phase: PhasePrepared,
		Name:  meta.Name,
		Kind:  meta.Kind,
	})
	if saveErr := saveDocument(v.store, v.doc); saveErr != nil {
		v.mu.Unlock()
		return "", fmt.Errorf("journal save: %w", saveErr)
	}
	v.mu.Unlock() // release before provider call

	// Delegation — provider call outside lock.
	putErr := prov.Put(ctx, id, value)

	v.mu.Lock()
	defer v.mu.Unlock()

	// Reject the result if the vault moved under the write. Two DIFFERENT
	// things can have happened and they used to share one error, which made
	// them indistinguishable from outside: the vault was sealed (rootKey
	// cleared), or the generation advanced while the provider call was in
	// flight. Reporting the second as ErrVaultSealed tells the UI to raise
	// Unlock — and unlocking does not fix a generation change, so the user
	// unlocks, retries, and is asked to unlock again, forever. That loop is
	// what nocx-25k9.20 records.
	if v.rootKey == nil {
		// Journal entry survives for reconciliation — do not clear it.
		v.logger.Warn("create rejected: vault sealed during the provider call",
			"secretID", id, "provider", provID, "duration", time.Since(t0))
		return "", ErrVaultSealed
	}
	if v.gen != gen {
		// Journal entry survives for reconciliation — do not clear it.
		v.logger.Warn("create rejected: vault generation advanced during the provider call",
			"secretID", id, "provider", provID,
			"genAtStart", gen, "genNow", v.gen, "duration", time.Since(t0))
		return "", ErrVaultGenerationChanged
	}

	// The put failed — the journal entry survives for reconciliation.
	if putErr != nil {
		v.logger.Warn("provider put failed",
			"secretID", id, "provider", provID,
			"error", putErr, "duration", time.Since(t0))
		return "", putErr
	}

	// Advance to PhaseSecretWritten and land the catalogue record in the
	// SAME save (ADR-0016): the value and the name are two writes in one
	// sequence, never two paths. A crash before this save leaves the entry
	// at PhasePrepared with no record — Reconcile deletes the orphan; a
	// crash after leaves the record proving the secret exists. The entry
	// itself stays until the caller attaches a metadata target and commits
	// (ADR-0011 §4).
	for i := range v.doc.Journal {
		if v.doc.Journal[i].NewID == id {
			v.doc.Journal[i].Phase = PhaseSecretWritten
			break
		}
	}
	v.setRecordLocked(SecretRecord{ID: id, Name: meta.Name, Kind: meta.Kind})
	if saveErr := saveDocument(v.store, v.doc); saveErr != nil {
		return "", fmt.Errorf("save after create: %w", saveErr)
	}

	v.logger.Info("secret created",
		"secretID", id, "provider", provID, "duration", time.Since(t0))
	return id, nil
}

// ReasonUnknownProvider is returned when a provider ID does not match any
// registered provider. It is a routing/configuration error, not a provider
// health status — no Provider.Status() implementation returns it.
const ReasonUnknownProvider = "unknown-provider"

// Get resolves id to a provider and reads the secret. It never falls back to
// the default provider — an unregistered tag returns ErrProviderUnavailable
// with ReasonUnknownProvider (spec §6 invariant 5).
//
// A secret obtained before a seal remains readable afterwards (spec §4.5):
// once bytes are out, the Vault does not own their lifetime.
func (v *Vault) Get(ctx context.Context, id credential.SecretID) (credential.Secret, error) {
	t0 := time.Now()

	// Parse first (behaviour 9): a malformed reference fails without touching
	// any provider or checking state.
	pID, err := parseID(id)
	if err != nil {
		return credential.Secret{}, err
	}

	v.mu.Lock()
	st := v.stateLocked()
	if st == StateUninitialized {
		v.mu.Unlock()
		return credential.Secret{}, ErrVaultUninitialized
	}
	if st == StateSealed {
		v.mu.Unlock()
		return credential.Secret{}, ErrVaultSealed
	}
	gen := v.gen
	v.mu.Unlock()

	// Route to the named provider — never the default.
	p, ok := v.reg.Get(pID)
	if !ok {
		return credential.Secret{}, unavailable(pID, ReasonUnknownProvider,
			fmt.Errorf("provider %q is not registered", pID))
	}

	sec, err := p.Get(ctx, id)
	if err != nil {
		v.logger.Warn("provider get failed",
			"secretID", id, "provider", pID, "error", err, "duration", time.Since(t0))
		return credential.Secret{}, err
	}

	// Re-check generation: reject result if Seal happened during the call
	// (defect 4). A Secret already returned to the caller before the re-check
	// stays readable (spec §4.5 honest limit).
	v.mu.Lock()
	if v.gen != gen || v.rootKey == nil {
		v.mu.Unlock()
		v.logger.Warn("get result rejected by generation change",
			"secretID", id, "provider", pID, "duration", time.Since(t0))
		return credential.Secret{}, ErrVaultSealed
	}
	v.mu.Unlock()

	v.logger.Info("secret retrieved",
		"secretID", id, "provider", pID, "duration", time.Since(t0))
	return sec, nil
}

// Delete removes the secret from its provider. Following ADR-0011 §4, the
// reference is removed first (journal cleared), then the provider delete is
// retriable (the entry survives if the provider call fails).
func (v *Vault) Delete(ctx context.Context, id credential.SecretID) error {
	t0 := time.Now()

	pID, err := parseID(id)
	if err != nil {
		return err
	}

	v.mu.Lock()
	st := v.stateLocked()
	switch st {
	case StateUninitialized:
		v.mu.Unlock()
		return ErrVaultUninitialized
	case StateSealed:
		v.mu.Unlock()
		return ErrVaultSealed
	}
	gen := v.gen

	prov, ok := v.reg.Writable(pID)
	v.mu.Unlock()

	if !ok {
		return unavailable(pID, ReasonUnknownProvider,
			fmt.Errorf("provider %q is not writable", pID))
	}

	// Journal the deletion before the provider call.
	v.mu.Lock()
	// Re-check state and gen before journaling and delegating (defect 8).
	if v.rootKey == nil || v.gen != gen {
		v.mu.Unlock()
		return ErrVaultSealed
	}
	v.doc.Journal = append(v.doc.Journal, JournalEntry{
		Op:    "delete",
		NewID: id,
		Phase: PhasePrepared,
	})
	// The catalogue record goes with the journal write, not after the
	// provider delete: deletion is metadata-first (ADR-0011 §4), and the
	// record is metadata. A failed provider delete leaves an invisible
	// orphan, never a dangling row.
	dropRecord(&v.doc, id)
	if saveErr := saveDocument(v.store, v.doc); saveErr != nil {
		v.mu.Unlock()
		return fmt.Errorf("journal save: %w", saveErr)
	}
	v.mu.Unlock()

	// Provider call outside lock.
	delErr := prov.Delete(ctx, id)

	v.mu.Lock()
	defer v.mu.Unlock()

	if v.gen != gen || v.rootKey == nil {
		v.logger.Warn("delete result rejected by generation change",
			"secretID", id, "provider", pID, "duration", time.Since(t0))
		return ErrVaultSealed
	}

	// Clear journal on success; retain on failure so reconciliation retries.
	if delErr != nil {
		v.logger.Warn("provider delete failed",
			"secretID", id, "provider", pID,
			"error", delErr, "duration", time.Since(t0))
		return delErr
	}

	v.clearJournalEntryLocked(id)
	if saveErr := saveDocument(v.store, v.doc); saveErr != nil {
		return fmt.Errorf("save after delete: %w", saveErr)
	}

	v.logger.Info("secret deleted",
		"secretID", id, "provider", pID, "duration", time.Since(t0))
	return nil
}

// Exists reports whether a secret exists in its named provider.
func (v *Vault) Exists(ctx context.Context, id credential.SecretID) (bool, error) {
	pID, err := parseID(id)
	if err != nil {
		return false, err
	}

	v.mu.Lock()
	st := v.stateLocked()
	switch st {
	case StateUninitialized:
		v.mu.Unlock()
		return false, ErrVaultUninitialized
	case StateSealed:
		v.mu.Unlock()
		return false, ErrVaultSealed
	}
	gen := v.gen
	v.mu.Unlock()

	p, ok := v.reg.Get(pID)
	if !ok {
		return false, unavailable(pID, ReasonUnknownProvider,
			fmt.Errorf("provider %q is not registered", pID))
	}

	_, err = p.Get(ctx, id)

	// Re-check generation before error mapping (defect 4): an in-flight
	// operation must not deliver a result if Seal happened during the call.
	v.mu.Lock()
	if v.gen != gen || v.rootKey == nil {
		v.mu.Unlock()
		return false, ErrVaultSealed
	}
	v.mu.Unlock()

	if err != nil {
		// Only ErrSecretNotFound maps to absence (defect 9).
		// Denied, timeout, locked, and corrupt all propagate.
		if errors.Is(err, ErrSecretNotFound) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

// --- helpers ---

// defaultWritableLocked returns the default writable provider. Must hold v.mu.
func (v *Vault) defaultWritableLocked() (WritableProvider, error) {
	p, ok := v.reg.Writable(v.doc.DefaultProvider)
	if !ok {
		return nil, unavailable(v.doc.DefaultProvider, ReasonUnknownProvider,
			fmt.Errorf("default provider %q is not writable", v.doc.DefaultProvider))
	}
	return p, nil
}

// osKeyID derives a deterministic SecretID for the OS-held root key from the
// vault instance. Each installation has a unique instance, so OS key entries
// from different vaults never collide.
func osKeyID(instance string) credential.SecretID {
	h := sha256.Sum256([]byte(instance))
	return credential.SecretID(fmt.Sprintf("sec:v1:system:%x", h[:16]))
}

// unlocker is satisfied by providers that need a root key to operate (e.g.
// the file provider). The vault discovers them by type assertion.
type unlocker interface {
	Unlock(rootKey []byte) error
}

// locker is satisfied by providers whose secrets must be wiped on seal.
type locker interface {
	Lock()
}

// dataKeyCreator is satisfied by providers that need a data key during setup.
type dataKeyCreator interface {
	NewDataKey() ([]byte, error)
	SetInstanceID(id string)
}

// clearJournalEntryLocked removes the journal entry for id, if any.
func (v *Vault) clearJournalEntryLocked(id credential.SecretID) {
	for i := range v.doc.Journal {
		if v.doc.Journal[i].NewID == id {
			v.doc.Journal[i] = JournalEntry{} // zero value = cleared
			return
		}
	}
}

// AttachTarget records who will reference the secret in metadata. This is
// step 1 of the two-phase commit that clears the journal entry left by Create.
func (v *Vault) AttachTarget(ctx context.Context, id credential.SecretID, target string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	for i := range v.doc.Journal {
		if v.doc.Journal[i].NewID == id && v.doc.Journal[i].Phase == PhaseSecretWritten {
			v.doc.Journal[i].Target = target
			return saveDocument(v.store, v.doc)
		}
	}
	return fmt.Errorf("no PhaseSecretWritten entry for %q", id)
}

// CommitMetadata advances the journal entry to PhaseMetadataRepointed,
// best-effort deletes the old secret if one is named, then clears the entry.
func (v *Vault) CommitMetadata(ctx context.Context, id credential.SecretID) error {
	v.mu.Lock()

	// Find the entry and save the old ID before clearing.
	var oldID credential.SecretID
	var found bool
	for i := range v.doc.Journal {
		if v.doc.Journal[i].NewID == id && v.doc.Journal[i].Phase == PhaseSecretWritten {
			oldID = v.doc.Journal[i].OldID
			v.doc.Journal[i].Phase = PhaseMetadataRepointed
			found = true
			break
		}
	}
	if !found {
		v.mu.Unlock()
		return fmt.Errorf("no PhaseSecretWritten entry for %q", id)
	}

	// The old secret's record dies with the old secret: a record naming a
	// value that is about to be deleted is a dangling row (ADR-0016). It is
	// dropped in the same write as the phase advance, like Delete drops it
	// with the journal — metadata first.
	if oldID != "" {
		dropRecord(&v.doc, oldID)
	}
	if saveErr := saveDocument(v.store, v.doc); saveErr != nil {
		v.mu.Unlock()
		return fmt.Errorf("save before finalize: %w", saveErr)
	}
	v.mu.Unlock()

	// Best-effort delete the old secret outside lock.
	if oldID != "" {
		pID, pErr := parseID(oldID)
		if pErr == nil {
			if wp, ok := v.reg.Writable(pID); ok {
				_ = wp.Delete(ctx, oldID)
			}
		}
	}

	// Clear the journal entry.
	v.mu.Lock()
	v.clearJournalEntryLocked(id)
	if saveErr := saveDocument(v.store, v.doc); saveErr != nil {
		v.mu.Unlock()
		return fmt.Errorf("save after finalize: %w", saveErr)
	}
	v.mu.Unlock()

	return nil
}

// setRecordLocked upserts a catalogue record by SecretID. Must hold v.mu.
func (v *Vault) setRecordLocked(rec SecretRecord) {
	for i := range v.doc.Secrets {
		if v.doc.Secrets[i].ID == rec.ID {
			v.doc.Secrets[i] = rec
			return
		}
	}
	v.doc.Secrets = append(v.doc.Secrets, rec)
}

// RenameSecret sets the display name of a secret, addressing it by the
// renderer-addressable row handle rather than by a SecretID (nocx-jb20.1:
// the reference is never accepted from the renderer as an identifier).
// inputs are the same credential metadata BuildInventory takes, so a secret
// referenced by metadata but never recorded (pre-ADR-0016) can be resolved
// and gains its record here.
//
// Refuses an empty name — a renamed secret is never left nameless — and
// refuses sealed/uninitialized vaults like every other mutation.
func (v *Vault) RenameSecret(ctx context.Context, row string, name string, inputs []CredentialInventory) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	st := v.stateLocked()
	switch st {
	case StateUninitialized:
		return ErrVaultUninitialized
	case StateSealed:
		return ErrVaultSealed
	}

	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("secret name is required")
	}

	id, kind, ok := v.resolveRowLocked(row, inputs)
	if !ok {
		return fmt.Errorf("unknown secret row %q", row)
	}
	if err := validateKind(kind); err != nil {
		return err
	}

	// A recorded secret keeps its recorded kind — the vault is the kind's
	// owner once it has been recorded. An unrecorded reference takes the kind
	// its metadata implies.
	for i := range v.doc.Secrets {
		if v.doc.Secrets[i].ID == id {
			v.doc.Secrets[i].Name = name
			return saveDocument(v.store, v.doc)
		}
	}
	v.doc.Secrets = append(v.doc.Secrets, SecretRecord{ID: id, Name: name, Kind: kind})
	return saveDocument(v.store, v.doc)
}

// ReplaceSecret overwrites the material behind an existing secret, addressed
// by the renderer-addressable row handle rather than by a SecretID
// (nocx-jb20.1: the reference is never accepted from the renderer as an
// identifier). The reference does NOT change: the new value is written to the
// SAME SecretID through the owning provider, so every connection referencing
// the secret keeps working and the catalogue record (name, kind) is untouched.
// inputs are the same credential metadata BuildInventory takes, so a secret
// referenced by metadata but never recorded (pre-ADR-0016) can be resolved
// the way RenameSecret resolves it.
//
// The write is journaled before the provider call, like every other provider
// write (spec §4.2) — a replace that times out may still land, and the entry
// is what makes that reconcilable. A replace is single-store (value only; the
// record is untouched), so reconciliation has nothing to repair: whichever
// half of the write landed, the id still names a valid secret.
func (v *Vault) ReplaceSecret(ctx context.Context, row string, value credential.Secret, inputs []CredentialInventory) error {
	t0 := time.Now()

	v.mu.Lock()
	st := v.stateLocked()
	switch st {
	case StateUninitialized:
		v.mu.Unlock()
		v.logger.Info("vault: refusing ReplaceSecret, not initialized")
		return ErrVaultUninitialized
	case StateSealed:
		v.mu.Unlock()
		v.logger.Info("vault: refusing ReplaceSecret, sealed")
		return ErrVaultSealed
	}
	gen := v.gen

	id, _, ok := v.resolveRowLocked(row, inputs)
	if !ok {
		v.mu.Unlock()
		return fmt.Errorf("unknown secret row %q", row)
	}

	provID, err := parseID(id)
	if err != nil {
		v.mu.Unlock()
		return err
	}
	prov, ok := v.reg.Writable(provID)
	if !ok {
		v.mu.Unlock()
		return unavailable(provID, ReasonUnknownProvider,
			fmt.Errorf("provider %q is not writable", provID))
	}

	// Journal PhasePrepared before delegating — the same discipline as every
	// other provider write (spec §4.2).
	v.doc.Journal = append(v.doc.Journal, JournalEntry{
		Op:    "replace",
		NewID: id,
		Phase: PhasePrepared,
	})
	if saveErr := saveDocument(v.store, v.doc); saveErr != nil {
		v.mu.Unlock()
		return fmt.Errorf("journal save: %w", saveErr)
	}
	v.mu.Unlock() // release before provider call

	// Delegation — provider call outside lock.
	putErr := prov.Put(ctx, id, value)

	v.mu.Lock()
	defer v.mu.Unlock()

	// Reject the result if the vault moved under the write (nocx-25k9.20):
	// sealed, or the generation advanced while the provider call was in
	// flight. Either way the journal entry survives for reconciliation.
	if v.rootKey == nil {
		v.logger.Warn("replace rejected: vault sealed during the provider call",
			"secretID", id, "provider", provID, "duration", time.Since(t0))
		return ErrVaultSealed
	}
	if v.gen != gen {
		v.logger.Warn("replace rejected: vault generation advanced during the provider call",
			"secretID", id, "provider", provID,
			"genAtStart", gen, "genNow", v.gen, "duration", time.Since(t0))
		return ErrVaultGenerationChanged
	}
	if putErr != nil {
		v.logger.Warn("provider put failed",
			"secretID", id, "provider", provID,
			"error", putErr, "duration", time.Since(t0))
		return putErr
	}

	// The write landed. Clear the journal entry: nothing downstream commits a
	// replace — the vault owns the whole sequence — so the vault clears it.
	for i := range v.doc.Journal {
		if v.doc.Journal[i].Op == "replace" && v.doc.Journal[i].NewID == id {
			v.doc.Journal[i] = JournalEntry{}
			break
		}
	}
	if saveErr := saveDocument(v.store, v.doc); saveErr != nil {
		return fmt.Errorf("save after replace: %w", saveErr)
	}

	v.logger.Info("secret replaced",
		"secretID", id, "provider", provID, "duration", time.Since(t0))
	return nil
}

// ResolveRow maps a renderer-addressable row handle to the SecretID behind
// it — the inverse half of the inventory's row minting. Backend-only: the
// renderer is never handed a SecretID, and the row handle is the only
// identifier it is allowed to hold (nocx-jb20.1). The row set is the same one
// BuildInventory shows: vault records first, then references in the
// credential metadata (so a pre-ADR-0016 secret can be resolved too).
//
// The transport uses this to clear credential references (metadata first,
// ADR-0011 §4) before the stored secret is deleted.
func (v *Vault) ResolveRow(row string, inputs []CredentialInventory) (credential.SecretID, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	id, _, ok := v.resolveRowLocked(row, inputs)
	return id, ok
}

// resolveRowLocked maps a row handle to its (SecretID, kind). Records are the
// primary source; unrecorded references come from the credential metadata.
// Must hold v.mu.
func (v *Vault) resolveRowLocked(row string, inputs []CredentialInventory) (credential.SecretID, string, bool) {
	for _, rec := range v.doc.Secrets {
		if rowID(rec.ID) == row {
			return rec.ID, rec.Kind, true
		}
	}
	for _, cred := range inputs {
		for _, sr := range collectRefs(cred) {
			if rowID(sr.ref) == row {
				return sr.ref, sr.kind, true
			}
		}
	}
	return "", "", false
}

// reportOrphanedOSKey deletes the OS-held root key written earlier in a Setup
// that is now failing, and — this is the point of the helper — makes a failed
// deletion visible.
//
// Dropping that error with `_ =` was the tempting shape, and it is the wrong
// one here. A stranded root key in the OS store is an orphan nobody can find
// later: go-keyring exposes Set, Get, Delete and DeleteAll and no enumeration
// at all, so there is no sweep that could discover it (nocx-dm0). The one
// moment its identifier is known is right now, in this function, so the
// identifier goes into the log at WARN even though the operation the user sees
// is already failing for another reason.
//
// The id is not secret — it is derived from the vault instance and appears in
// metadata elsewhere — so logging it breaks no invariant. The key material it
// names is never logged.
func (v *Vault) reportOrphanedOSKey(ctx context.Context, sysProv WritableProvider, oskID credential.SecretID) {
	if err := sysProv.Delete(ctx, oskID); err != nil {
		v.logger.Warn("setup rollback could not remove the OS-held root key; it is now an orphan in the system store and cannot be found by any later sweep",
			"secretID", oskID, "provider", sysProv.ID(), "error", err)
	}
}

// ProviderSnapshot is a read-only projection of a provider for the vault.status
// RPC. It carries no entry names, no locators and nothing from which a storage
// location can be reconstructed.
type ProviderSnapshot struct {
	ID       ProviderID `json:"id"`
	Writable bool       `json:"writable"`
	Ready    bool       `json:"ready"`
	Reason   Reason     `json:"reason,omitempty"`
}

// Snapshot is a consistent view of the vault at one moment. It is the response
// shape for vault.status and the payload for vault.changed broadcasts.
type Snapshot struct {
	State State `json:"-"`

	// HasOSKey is STATE: this vault holds an OS-held key and can be unsealed
	// by one. False on every uninitialized vault, by construction.
	HasOSKey bool `json:"osKeyAvailable"`

	// OSKeyCapable is CAPABILITY: this machine has a system keyring that is
	// ready and writable, so Setup can mint an OS-held key with no passphrase.
	//
	// The distinction is the whole of nocx-25k9.8. One field carried both
	// meanings, the renderer read it to decide whether setup could be silent,
	// and since it is false before setup the silent path never ran — which made
	// the system provider unreachable from the UI. Before using either of
	// these, decide whether the question is about the machine or the vault.
	OSKeyCapable bool `json:"osKeyCapable"`

	// HasPassphrase is STATE: the vault document contains a passphrase
	// envelope. When false, ChangePassphrase and RegenerateRecovery are
	// impossible — the vault was set up with only an OS-held key.
	HasPassphrase bool `json:"hasPassphrase"`

	// AutoSealMinutes is the configured idle auto-seal timeout. 0 = off.
	// The Snapshot is the transport projection, so Live (mutable) values are
	// always read under the lock and are never stale for long — see the method
	// that constructs it for the exact locking window.
	AutoSealMinutes int `json:"autoSealMinutes"`

	// DefaultProvider is the store new secrets are written to. Empty on an
	// uninitialized vault, which has not chosen one yet.
	//
	// It is STATE and it is chosen exactly once, by Setup, from what was ready
	// at that moment — it never follows the machine afterwards. A keychain that
	// appears later does not silently take over the store of record, because
	// secrets already written to the file provider would stay there while new
	// ones went elsewhere, and nothing would have told the user their material
	// is now in two places. Moving is SetDefaultProvider, and that is a
	// decision the user makes.
	DefaultProvider ProviderID `json:"defaultProvider"`

	Providers []ProviderSnapshot `json:"providers"`
}

// Snapshot returns a read-only projection of the vault for the transport layer.
// It holds no lock across provider calls: state and registry contents are read
// under the mutex, then released before each provider is queried.
func (v *Vault) Snapshot(ctx context.Context) Snapshot {
	v.mu.Lock()
	state := v.stateLocked()
	hasOSKey := v.doc.HasOSKey
	hasPass := v.doc.Passphrase != nil
	autoSealMins := v.doc.AutoSealMinutes
	defaultProv := v.doc.DefaultProvider
	providers := v.reg.List()
	v.mu.Unlock()
	snap := Snapshot{
		State:           state,
		HasOSKey:        hasOSKey,
		HasPassphrase:   hasPass,
		AutoSealMinutes: autoSealMins,
		DefaultProvider: defaultProv,
	}

	for _, p := range providers {
		status := p.Status(ctx)
		_, writable := p.(WritableProvider)
		ps := ProviderSnapshot{
			ID:       p.ID(),
			Writable: writable,
			Ready:    status.Ready,
			Reason:   status.Reason,
		}
		snap.Providers = append(snap.Providers, ps)
		if ps.ID == ProviderSystem && ps.Writable && ps.Ready {
			snap.OSKeyCapable = true
		}
	}

	return snap
}

// MarshalJSON serialises Snapshot with a string state value.
func (s Snapshot) MarshalJSON() ([]byte, error) {
	type alias struct {
		State           string             `json:"state"`
		HasOSKey        bool               `json:"osKeyAvailable"`
		OSKeyCapable    bool               `json:"osKeyCapable"`
		HasPassphrase   bool               `json:"hasPassphrase"`
		AutoSealMinutes int                `json:"autoSealMinutes"`
		Providers       []ProviderSnapshot `json:"providers"`
	}
	return json.Marshal(alias{
		State:           s.State.String(),
		HasOSKey:        s.HasOSKey,
		OSKeyCapable:    s.OSKeyCapable,
		HasPassphrase:   s.HasPassphrase,
		AutoSealMinutes: s.AutoSealMinutes,
		Providers:       s.Providers,
	})
}
