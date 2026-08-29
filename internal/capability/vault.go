package capability

import (
	"context"

	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vaultreset"
)

// VaultLifecycle is the seal-lifecycle surface of the vault, seen from the
// capability layer. Satisfied by *vault.Vault. The secret operations
// (create, rename, replace, delete, inventory) live on SecretService, not
// here: a VaultOperation is for lifecycle work and cannot reach a secret.
type VaultLifecycle interface {
	State() vault.State
	Snapshot(ctx context.Context) vault.Snapshot
	Setup(ctx context.Context, req vault.SetupRequest) (vault.SetupResult, error)
	Unseal(ctx context.Context, req vault.UnsealRequest) error
	Seal()
	ChangePassphrase(ctx context.Context, req vault.ChangePassphraseRequest) error
	RegenerateRecovery(ctx context.Context, req vault.RegenerateRequest) (string, error)
	SetDefaultProvider(ctx context.Context, p vault.ProviderID) error
	SetAutoSeal(ctx context.Context, minutes int) error
	Activity()
}

// VaultService is the vault-lifecycle domain surface: setup, seal lifecycle,
// provider routing and activity. It is what a VaultOperation hands its
// callback. Read policy: reads participate in the vault gate — the vault is
// one store and lifecycle ops (seal) must not interleave a multi-step secret
// flow.
type VaultService interface {
	State() vault.State
	Snapshot(ctx context.Context) vault.Snapshot
	Setup(ctx context.Context, req vault.SetupRequest) (vault.SetupResult, error)
	Unseal(ctx context.Context, req vault.UnsealRequest) error
	Seal()
	ChangePassphrase(ctx context.Context, req vault.ChangePassphraseRequest) error
	RegenerateRecovery(ctx context.Context, req vault.RegenerateRequest) (string, error)
	SetDefaultProvider(ctx context.Context, p vault.ProviderID) error
	SetAutoSeal(ctx context.Context, minutes int) error
	Activity()
}

// VaultOperation is the typed operation for the vault-lifecycle domain.
// Its gate is [vault]. See the package doc for the conservative-grain
// rationale (vault-secret operations are a separate SecretOperation).
type VaultOperation interface {
	AssistantOperation
	Run(context.Context, func(context.Context, VaultService) error) error
}

// NewVaultOperation builds a VaultOperation that acquires the vault gate
// before the execution lane.
func NewVaultOperation(vaultGate, lane control.Admission, lifecycle VaultLifecycle) VaultOperation {
	g := &guard{}
	return newOperation[VaultService](Direct("VaultOperation"), control.NewComposite(vaultGate, lane), g, newVaultService(g, lifecycle))
}

// newVaultService builds the concrete vault-lifecycle service bound to
// guard g.
func newVaultService(g *guard, lifecycle VaultLifecycle) *vaultService {
	return &vaultService{guard: g, lifecycle: lifecycle}
}

type vaultService struct {
	guard     *guard
	lifecycle VaultLifecycle
}

func (s *vaultService) State() vault.State {
	if !s.guard.ok() {
		return vault.State(0)
	}
	return s.lifecycle.State()
}

func (s *vaultService) Snapshot(ctx context.Context) vault.Snapshot {
	if !s.guard.ok() {
		return vault.Snapshot{}
	}
	return s.lifecycle.Snapshot(ctx)
}

func (s *vaultService) Setup(ctx context.Context, req vault.SetupRequest) (vault.SetupResult, error) {
	if err := s.guard.check(); err != nil {
		return vault.SetupResult{}, err
	}
	return s.lifecycle.Setup(ctx, req)
}

func (s *vaultService) Unseal(ctx context.Context, req vault.UnsealRequest) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.lifecycle.Unseal(ctx, req)
}

func (s *vaultService) Seal() {
	if !s.guard.ok() {
		return
	}
	s.lifecycle.Seal()
}

func (s *vaultService) ChangePassphrase(ctx context.Context, req vault.ChangePassphraseRequest) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.lifecycle.ChangePassphrase(ctx, req)
}

func (s *vaultService) RegenerateRecovery(ctx context.Context, req vault.RegenerateRequest) (string, error) {
	if err := s.guard.check(); err != nil {
		return "", err
	}
	return s.lifecycle.RegenerateRecovery(ctx, req)
}

func (s *vaultService) SetDefaultProvider(ctx context.Context, p vault.ProviderID) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.lifecycle.SetDefaultProvider(ctx, p)
}

func (s *vaultService) SetAutoSeal(ctx context.Context, minutes int) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.lifecycle.SetAutoSeal(ctx, minutes)
}

func (s *vaultService) Activity() {
	if !s.guard.ok() {
		return
	}
	s.lifecycle.Activity()
}

// ---------------------------------------------------------------------------
// Vault reset — a cross-domain operation
// ---------------------------------------------------------------------------

// VaultReset is the reset orchestrator seam, satisfied by
// *vaultreset.Service. Reset is deliberately its own operation: it destroys
// the vault AND the profile references pointing at it — two stores in one
// deliberate sequence — and it must work on a vault that is broken or
// half-built, which is the only state it is ever wanted in.
type VaultReset interface {
	Preview(ctx context.Context) (vaultreset.Preview, error)
	Execute(ctx context.Context) (vaultreset.Result, error)
}

// VaultResetService is what a VaultResetOperation hands its callback.
type VaultResetService interface {
	Preview(ctx context.Context) (vaultreset.Preview, error)
	Execute(ctx context.Context) (vaultreset.Result, error)
}

// VaultResetOperation is the typed operation for vault.reset and
// vault.resetPreview. Its gates are [config, vault]: a reset destroys
// profile references and the vault together.
type VaultResetOperation interface {
	AssistantOperation
	Run(context.Context, func(context.Context, VaultResetService) error) error
}

// NewVaultResetOperation builds a VaultResetOperation that acquires
// configGate before vaultGate (the canonical order), then the execution
// lane, for every Run.
func NewVaultResetOperation(configGate, vaultGate, lane control.Admission, reset VaultReset) VaultResetOperation {
	g := &guard{}
	return newOperation[VaultResetService](Direct("VaultResetOperation"), control.NewComposite(configGate, vaultGate, lane), g, newVaultResetService(g, reset))
}

// newVaultResetService builds the concrete reset service bound to guard g.
func newVaultResetService(g *guard, reset VaultReset) *vaultResetService {
	return &vaultResetService{guard: g, reset: reset}
}

type vaultResetService struct {
	guard *guard
	reset VaultReset
}

func (s *vaultResetService) Preview(ctx context.Context) (vaultreset.Preview, error) {
	if err := s.guard.check(); err != nil {
		return vaultreset.Preview{}, err
	}
	return s.reset.Preview(ctx)
}

func (s *vaultResetService) Execute(ctx context.Context) (vaultreset.Result, error) {
	if err := s.guard.check(); err != nil {
		return vaultreset.Result{}, err
	}
	return s.reset.Execute(ctx)
}
