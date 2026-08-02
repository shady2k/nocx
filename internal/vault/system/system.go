// Package system provides the OS keychain provider backed by the
// freedesktop.org Secret Service (org.freedesktop.secrets) over D-Bus on
// Linux, and the platform-native keychain on macOS/Windows.
package system

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/vault"
)

// keychainSecretService is the single service name for all nocx secrets in the
// OS keychain. Account = string(id); nothing is ever re-derived from a file
// path or from a key's contents.
const keychainSecretService = "nocx"

var _ vault.WritableProvider = (*Provider)(nil)

// Keyring abstracts the OS keychain so tests can substitute a fake.
// The production implementation wraps zalando/go-keyring.
//
// The Get method MUST return an error wrapping keyring.ErrNotFound
// (github.com/zalando/go-keyring.ErrNotFound) when no entry exists for the
// given service/user pair. The provider depends on errors.Is to detect this.
type Keyring interface {
	Set(service, user, password string) error
	Get(service, user string) (string, error)
	Delete(service, user string) error
	// DeleteAll removes every entry under service.
	//
	// It is here because there is no enumeration operation on any platform,
	// so "remove everything nocx stored" cannot be expressed as a loop over
	// known ids: an entry whose reference was lost is undiscoverable, and
	// this provider stores plaintext, so leaving it behind leaves a readable
	// password nothing can ever find again.
	DeleteAll(service string) error
}

// keyringAdapter adapts zalando/go-keyring's package-level functions to the
// Keyring interface. Not-found errors pass through directly so the provider
// can detect them with errors.Is.
type keyringAdapter struct{}

func (keyringAdapter) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}

func (keyringAdapter) Get(service, user string) (string, error) {
	return keyring.Get(service, user)
}

func (keyringAdapter) Delete(service, user string) error {
	return keyring.Delete(service, user)
}

func (keyringAdapter) DeleteAll(service string) error {
	return keyring.DeleteAll(service)
}

const defaultTimeout = 5 * time.Second

// Option configures the system provider.
type Option func(*Provider)

// WithTimeout sets the per-call timeout. The default is 5 seconds.
//
// This bounds how long the caller waits for a keyring operation. It does NOT
// cancel the underlying call — go-keyring takes no context, and a goroutine
// that timed out may still complete afterwards. A Put that "timed out" may
// land.
func WithTimeout(d time.Duration) Option {
	return func(p *Provider) { p.timeout = d }
}

// WithKeyring replaces the default OS keyring adapter with kr. Pass a fake
// keyring in tests.
func WithKeyring(kr Keyring) Option {
	return func(p *Provider) { p.keyring = kr }
}

// ReasonProbe observes the platform's secret store directly and reports why it
// is unusable. It is consulted only when a keyring error names no cause — see
// classifyReason — and returns the empty Reason when it has no opinion.
//
// The observation matters because go-keyring's own failure text does not always
// name the cause. A locked collection surfaces as "failed to unlock correct
// collection '…'", which says nothing a string match can use, and guessing from
// it told users to install a keyring they were already running (nocx-25k9.6).
type ReasonProbe func() vault.Reason

// WithReasonProbe replaces the platform observation used to explain a keyring
// error that names no cause. Pass a fixed answer in tests: arranging a real
// locked Secret Service is unreliable — a daemon started with --login holds the
// password and re-unlocks itself on the next access — so this seam is how the
// locked case is asserted at all.
func WithReasonProbe(rp ReasonProbe) Option {
	return func(p *Provider) { p.reasonProbe = rp }
}

// Provider implements vault.WritableProvider backed by the OS keychain via
// the Keyring interface.
type Provider struct {
	keyring     Keyring
	timeout     time.Duration
	reasonProbe ReasonProbe
}

// New creates a system provider with the given options. The default keyring
// wraps zalando/go-keyring.
func New(opts ...Option) *Provider {
	p := &Provider{
		keyring:     keyringAdapter{},
		timeout:     defaultTimeout,
		reasonProbe: platformReason,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

func (p *Provider) ID() vault.ProviderID { return vault.ProviderSystem }

// Status reports the provider's readiness. It delegates to Probe.
func (p *Provider) Status(ctx context.Context) vault.Status {
	return p.Probe(ctx)
}

// Probe checks that the keyring is usable by writing, reading, and removing a
// random entry. A failed removal is tolerated; every other failure produces a
// non-ready status with a reason.
func (p *Provider) Probe(ctx context.Context) vault.Status {
	id := randomProbeID()
	probeVal := "probe-" + id

	if err := p.runKeyringOp(ctx, func() error {
		return p.keyring.Set(keychainSecretService, id, probeVal)
	}); err != nil {
		return probeStatus(err)
	}

	val, err := p.runKeyringGetOp(ctx, func() (string, error) {
		return p.keyring.Get(keychainSecretService, id)
	})
	if err != nil {
		return probeStatus(err)
	}
	if val != probeVal {
		// Returned wrong value — the store is unreliable.
		return vault.Status{Reason: vault.ReasonDenied}
	}

	// Delete best-effort through the timeout wrapper so a hung keyring does
	// not block the probe. A failed or timed-out delete is tolerated.
	_ = p.runKeyringOp(ctx, func() error {
		return p.keyring.Delete(keychainSecretService, id)
	})

	return vault.Status{Ready: true}
}

func randomProbeID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func probeStatus(err error) vault.Status {
	var pe *vault.ProviderError
	if errors.As(err, &pe) {
		return vault.Status{Reason: pe.Reason}
	}
	return vault.Status{Reason: vault.ReasonNoService}
}

// Get returns the secret identified by id. A missing key returns
// ErrSecretNotFound.
//
// ctx bounds how long the caller waits. It does NOT cancel the underlying
// keyring call — see WithTimeout.
func (p *Provider) Get(ctx context.Context, id credential.SecretID) (credential.Secret, error) {
	val, err := p.runKeyringGetOp(ctx, func() (string, error) {
		return p.keyring.Get(keychainSecretService, string(id))
	})
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return credential.Secret{}, fmt.Errorf("%w: %s", vault.ErrSecretNotFound, id)
		}
		return credential.Secret{}, err
	}
	return credential.NewSecret(val), nil
}

// Put stores the secret under id.
//
// ctx bounds how long the caller waits. It does NOT cancel the underlying
// keyring call — see WithTimeout.
func (p *Provider) Put(ctx context.Context, id credential.SecretID, s credential.Secret) error {
	var plaintext string
	if err := s.Use(func(b []byte) error { plaintext = string(b); return nil }); err != nil {
		return err
	}
	return p.runKeyringOp(ctx, func() error {
		return p.keyring.Set(keychainSecretService, string(id), plaintext)
	})
}

// Delete removes the secret identified by id. Removing an absent key succeeds.
//
// ctx bounds how long the caller waits. It does NOT cancel the underlying
// keyring call — see WithTimeout.
func (p *Provider) Delete(ctx context.Context, id credential.SecretID) error {
	err := p.runKeyringOp(ctx, func() error {
		return p.keyring.Delete(keychainSecretService, string(id))
	})
	if err != nil && errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// PurgeAll removes every secret this provider has stored, including entries
// whose references were lost — see the Keyring doc for why a bulk delete is
// the only complete operation available.
//
// Scoped to keychainSecretService, which is a constant in this package. The
// service name must never come from a caller, and least of all from the
// renderer: it is the one argument that decides whose secrets are destroyed.
//
// ctx bounds how long the caller waits. It does NOT cancel the underlying
// keyring call — see WithTimeout. A timeout therefore means "unknown", not
// "did not happen", which is why the operation is safe to retry.
func (p *Provider) PurgeAll(ctx context.Context) error {
	return p.runKeyringOp(ctx, func() error {
		return p.keyring.DeleteAll(keychainSecretService)
	})
}

// --- internal helpers ---

// runKeyringOp runs fn in a goroutine and waits for it to complete or the
// context to expire. When the context expires first, ReasonTimeout is returned
// and the goroutine continues running — see WithTimeout.
func (p *Provider) runKeyringOp(ctx context.Context, fn func() error) error {
	ctx, cancel := p.withTimeout(ctx)
	defer cancel()

	ch := make(chan error, 1)
	go func() {
		ch <- fn()
	}()

	select {
	case <-ctx.Done():
		return &vault.ProviderError{
			Provider: vault.ProviderSystem,
			Reason:   vault.ReasonTimeout,
			Err:      ctx.Err(),
		}
	case err := <-ch:
		if err != nil {
			return p.classifyAndWrap(err)
		}
		return nil
	}
}

// runKeyringGetOp is like runKeyringOp but for operations that return a value.
func (p *Provider) runKeyringGetOp(ctx context.Context, fn func() (string, error)) (string, error) {
	ctx, cancel := p.withTimeout(ctx)
	defer cancel()

	ch := make(chan getResult, 1)
	go func() {
		val, err := fn()
		ch <- getResult{val: val, err: err}
	}()

	select {
	case <-ctx.Done():
		return "", &vault.ProviderError{
			Provider: vault.ProviderSystem,
			Reason:   vault.ReasonTimeout,
			Err:      ctx.Err(),
		}
	case r := <-ch:
		if r.err != nil {
			return "", p.classifyAndWrap(r.err)
		}
		return r.val, nil
	}
}

type getResult struct {
	val string
	err error
}

func (p *Provider) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if p.timeout > 0 {
		return context.WithTimeout(ctx, p.timeout)
	}
	return ctx, func() {}
}

// classifyAndWrap wraps a keyring error in ProviderError with the appropriate
// reason. keyring.ErrNotFound passes through unwrapped so the caller can
// detect it with errors.Is.
func (p *Provider) classifyAndWrap(err error) error {
	if errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return &vault.ProviderError{
		Provider: vault.ProviderSystem,
		Reason:   p.classifyReason(err),
		Err:      err,
	}
}

// classifyReason maps a keyring error to a Reason, in two steps. First the
// error text, which is believed when it names a cause; the matching is
// heuristic because go-keyring wraps platform-specific errors from three
// different backends.
//
// When the text names nothing, the reason is observed rather than guessed. The
// old default — "no-service" — is a claim about the machine, and it was wrong
// for the case that matters most: a running daemon whose collection is locked
// fails with "failed to unlock correct collection '…'", which contains no word
// this function matches. Users of a perfectly good keyring were told to install
// one and invent a master passphrase (nocx-25k9.6). "no-service" survives only
// as the answer when nothing can observe the store at all.
func (p *Provider) classifyReason(err error) vault.Reason {
	if err == nil {
		return ""
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "locked"):
		return vault.ReasonLocked
	case strings.Contains(s, "denied") || strings.Contains(s, "access"):
		return vault.ReasonDenied
	}
	if p.reasonProbe != nil {
		if r := p.reasonProbe(); r != "" {
			return r
		}
	}
	return vault.ReasonNoService
}
