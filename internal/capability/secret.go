package capability

import (
	"context"
	"fmt"
	"regexp"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/vault"
)

// SecretVault is the vault surface the vault-secret domain needs: the
// catalogue-aware secret operations and row resolution. Satisfied by
// *vault.Vault. The lifecycle operations (setup, seal, …) are deliberately
// absent — a SecretOperation cannot seal the vault.
type SecretVault interface {
	BuildInventory(ctx context.Context, inputs []vault.CredentialInventory) ([]vault.InventoryEntry, error)
	CreateNamed(ctx context.Context, value credential.Secret, meta vault.SecretMeta) (credential.SecretID, error)
	CreateNamedResolved(ctx context.Context, value credential.Secret, meta vault.SecretMeta) (credential.SecretID, string, error)
	RenameSecret(ctx context.Context, row string, name string, inputs []vault.CredentialInventory) error
	ReplaceSecret(ctx context.Context, row string, value credential.Secret, inputs []vault.CredentialInventory) error
	ResolveRow(row string, inputs []vault.CredentialInventory) (credential.SecretID, bool)
}

// SecretService is the vault-secret domain surface: inventory, create,
// rename, replace, delete, resolve and read. It is what a SecretOperation
// hands its callback.
//
// The renderer addresses secrets by row handle (secrow:...) — never by a
// SecretID (nocx-jb20.1) — so every method that takes a row resolves it
// internally. The inventory-input projection (which profiles reference a
// secret) is computed inside the service from the profile/group stores;
// the handler never sees those stores.
type SecretService interface {
	// Inventory returns the vault inventory — the Secrets page.
	Inventory(ctx context.Context) ([]vault.InventoryEntry, error)
	// CreateSecret stores value with its catalogue metadata (ADR-0016).
	// resolve selects the atomic name-collision resolution (the prompt's
	// ⌘S save); the name ACTUALLY used comes back — the renderer must
	// never predict that a suffixed name is free.
	CreateSecret(ctx context.Context, value credential.Secret, meta vault.SecretMeta, resolve bool) (realName string, err error)
	// MintSecret stores value with its catalogue metadata (ADR-0016) and
	// returns the SecretID the vault minted, so the caller can derive the
	// row handle it answers with (secrets.savePassword & friends reply
	// with the row). When no vault is wired, the plain store records the
	// secret namelessly — the same fallback the transport's createSecret
	// used.
	MintSecret(ctx context.Context, value credential.Secret, meta vault.SecretMeta) (credential.SecretID, error)
	RenameSecret(ctx context.Context, row, name string) error
	ReplaceSecret(ctx context.Context, row string, value credential.Secret) error
	// DeleteSecret clears every profile reference to the secret — one
	// atomic write — then deletes the stored value (metadata first,
	// ADR-0011 §4).
	DeleteSecret(ctx context.Context, row string) error
	// ResolveRow maps a row handle to the SecretID behind it. False for
	// an unknown row.
	ResolveRow(row string) (credential.SecretID, bool)
	// Usage answers the profiles that use the secret behind a row
	// (ADR-0017). An unknown row or an unused secret answers an empty
	// list.
	Usage(ctx context.Context, row string) ([]profile.ProfileRef, error)
	// PlanLine finds every {{secret:NAME}} reference in a line and maps
	// each name to the SecretID behind it (vault.resolveLine). It reads no
	// material: that read waits for a person and would hold this
	// operation's gates while it did, so it belongs to the caller, after
	// the operation has released them (nocx-o3606). SubstituteLine then
	// puts the two halves together.
	PlanLine(ctx context.Context, line string) (LinePlan, error)
}

// LinePlan is everything a line substitution needs from the config and vault
// stores and nothing that needs a secret read: the line as it arrived, and
// each reference with the id behind it.
type LinePlan struct {
	// Line is the line exactly as the caller sent it.
	Line string
	// Refs are the references in order of appearance. Empty means the line
	// carries none and is its own answer.
	Refs []PlannedLineRef
}

// PlannedLineRef is one {{secret:NAME}} reference and what it points at.
type PlannedLineRef struct {
	// Name is the reference as written, between the braces.
	Name string
	// Start and End bound the whole {{secret:NAME}} token in Line.
	Start, End int
	// ID is the secret the name resolves to. Empty when the vault holds no
	// secret with that name — the reference is then left as written and
	// reported unresolved, never silently dropped.
	ID credential.SecretID
}

// SubstituteLine puts the plan and the material the caller resolved back
// together. Pure: no store, no vault, no gate — which is what lets the read
// between the two halves happen outside the operation.
//
// A reference whose id resolved to nothing usable is left exactly as written
// and reported unresolved: a retry after unsealing resolves differently, so
// quietly dropping it would be a lie about what the command will run.
func SubstituteLine(plan LinePlan, values map[credential.SecretID]credential.Secret) (string, []ResolvedLineRef) {
	refs := make([]ResolvedLineRef, 0, len(plan.Refs))
	out := make([]byte, 0, len(plan.Line))
	cursor := 0
	for _, ref := range plan.Refs {
		out = append(out, plan.Line[cursor:ref.Start]...)
		value, ok := lineMaterial(values, ref.ID)
		refs = append(refs, ResolvedLineRef{Name: ref.Name, Resolved: ok})
		if ok {
			out = append(out, value...)
		} else {
			out = append(out, plan.Line[ref.Start:ref.End]...)
		}
		cursor = ref.End
	}
	out = append(out, plan.Line[cursor:]...)
	return string(out), refs
}

// lineMaterial reads one resolved secret through Secret.Use — the only way
// the bytes are ever readable — and reports whether there was anything to
// substitute.
func lineMaterial(values map[credential.SecretID]credential.Secret, id credential.SecretID) (string, bool) {
	if id == "" {
		return "", false
	}
	secret, ok := values[id]
	if !ok || secret.IsEmpty() {
		return "", false
	}
	var val string
	if err := secret.Use(func(b []byte) error {
		val = string(b)
		return nil
	}); err != nil {
		return "", false
	}
	return val, true
}

// ResolvedLineRef is one reference in a resolved line (vault.resolveLine).
type ResolvedLineRef struct {
	// Name is the reference as written ({{secret:NAME}}).
	Name string
	// Resolved is false when the vault holds no secret with that name or
	// its store did not answer.
	Resolved bool
}

// SecretOperation is the typed operation for one vault secret. Its gates
// are [config, vault]: the secret operations compute their inventory
// inputs from profile reads and deleteSecret writes profile references.
type SecretOperation interface {
	Run(context.Context, func(context.Context, SecretService) error) error
}

// SecretOperations builds per-secret operations. The KIND of resource is
// compile-time (a SecretOperation can only reach secrets); the id is
// runtime. ForSecret returns an error for an unknown id and never nil — a
// nil handle is not enforcement.
type SecretOperations struct {
	configGate control.Admission
	vaultGate  control.Admission
	lane       control.Admission
	profiles   profile.ProfileRepository
	groups     profile.GroupRepository
	vault      SecretVault
	store      credential.SecretStore
	// exists answers whether id names a stored secret. Wired from the
	// vault's own existence check; nil means "no check" (a test seam).
	exists func(context.Context, credential.SecretID) (bool, error)
}

// NewSecretOperations wires the per-secret factory. Each ForSecret call
// returns a fresh operation with its own guarded service.
func NewSecretOperations(
	configGate, vaultGate, lane control.Admission,
	profiles profile.ProfileRepository,
	groups profile.GroupRepository,
	v SecretVault,
	store credential.SecretStore,
	exists func(context.Context, credential.SecretID) (bool, error),
) *SecretOperations {
	return &SecretOperations{
		configGate: configGate,
		vaultGate:  vaultGate,
		lane:       lane,
		profiles:   profiles,
		groups:     groups,
		vault:      v,
		store:      store,
		exists:     exists,
	}
}

// ForSecret returns a SecretOperation scoped to id, or an error when the
// vault holds no secret with that id. Never nil on success.
func (f *SecretOperations) ForSecret(ctx context.Context, id credential.SecretID) (SecretOperation, error) {
	if f.exists != nil {
		ok, err := f.exists(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("capability: check secret %q: %w", id, err)
		}
		if !ok {
			return nil, fmt.Errorf("capability: unknown secret %q", id)
		}
	}
	g := &guard{}
	return newOperation[SecretService](
		control.NewComposite(f.configGate, f.vaultGate, f.lane),
		g,
		newSecretService(g, f.profiles, f.groups, f.vault, f.store),
	), nil
}

// NewSecretOperation builds a single SecretOperation (the non-dynamically
// keyed form; most handlers use NewSecretOperations + ForSecret). It is
// for handlers whose operation is fixed at construction.
func NewSecretOperation(
	configGate, vaultGate, lane control.Admission,
	profiles profile.ProfileRepository,
	groups profile.GroupRepository,
	v SecretVault,
	store credential.SecretStore,
) SecretOperation {
	g := &guard{}
	return newOperation[SecretService](
		control.NewComposite(configGate, vaultGate, lane),
		g,
		newSecretService(g, profiles, groups, v, store),
	)
}

// newSecretService builds the concrete vault-secret service bound to
// guard g.
func newSecretService(
	g *guard,
	profiles profile.ProfileRepository,
	groups profile.GroupRepository,
	v SecretVault,
	store credential.SecretStore,
) *secretService {
	return &secretService{guard: g, profiles: profiles, groups: groups, vault: v, store: store}
}

type secretService struct {
	guard    *guard
	profiles profile.ProfileRepository
	groups   profile.GroupRepository
	vault    SecretVault
	store    credential.SecretStore
}

func (s *secretService) Inventory(ctx context.Context) ([]vault.InventoryEntry, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	inputs, err := s.inventoryInputs()
	if err != nil {
		return nil, err
	}
	return s.vault.BuildInventory(ctx, inputs)
}

func (s *secretService) CreateSecret(ctx context.Context, value credential.Secret, meta vault.SecretMeta, resolve bool) (string, error) {
	if err := s.guard.check(); err != nil {
		return "", err
	}
	if resolve {
		_, realName, err := s.vault.CreateNamedResolved(ctx, value, meta)
		return realName, err
	}
	_, err := s.vault.CreateNamed(ctx, value, meta)
	return meta.Name, err
}

func (s *secretService) MintSecret(ctx context.Context, value credential.Secret, meta vault.SecretMeta) (credential.SecretID, error) {
	if err := s.guard.check(); err != nil {
		return "", err
	}
	if s.vault == nil {
		return s.store.Create(ctx, value)
	}
	return s.vault.CreateNamed(ctx, value, meta)
}

func (s *secretService) RenameSecret(ctx context.Context, row, name string) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	inputs, err := s.inventoryInputs()
	if err != nil {
		return err
	}
	return s.vault.RenameSecret(ctx, row, name, inputs)
}

func (s *secretService) ReplaceSecret(ctx context.Context, row string, value credential.Secret) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	inputs, err := s.inventoryInputs()
	if err != nil {
		return err
	}
	return s.vault.ReplaceSecret(ctx, row, value, inputs)
}

func (s *secretService) DeleteSecret(ctx context.Context, row string) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	inputs, err := s.inventoryInputs()
	if err != nil {
		return err
	}
	id, ok := s.vault.ResolveRow(row, inputs)
	if !ok {
		return fmt.Errorf("unknown secret row %q", row)
	}
	pc, ok := s.profiles.(interface{ ClearSecretRefs(string) error })
	if !ok {
		return fmt.Errorf("profile store does not support reference clearing")
	}
	if err := pc.ClearSecretRefs(string(id)); err != nil {
		return err
	}
	// Stored secret second, best-effort like every other metadata-first
	// deletion (ADR-0011 §4): the metadata removal stands regardless, and
	// a failed provider delete is a brief unreachable orphan the journal
	// reconciles.
	_ = s.store.Delete(ctx, id)
	return nil
}

func (s *secretService) ResolveRow(row string) (credential.SecretID, bool) {
	if err := s.guard.check(); err != nil {
		return "", false
	}
	inputs, err := s.inventoryInputs()
	if err != nil {
		return "", false
	}
	return s.vault.ResolveRow(row, inputs)
}

func (s *secretService) Usage(ctx context.Context, row string) ([]profile.ProfileRef, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	profiles, groups, err := s.loadConfig()
	if err != nil {
		return nil, err
	}
	ref, ok := s.vault.ResolveRow(row, inventoryInputs(profiles, groups))
	if !ok {
		return nil, nil
	}
	for _, u := range profile.ComputeSecretUsage(profiles, groups, profile.SparseSSHOptions{}) {
		if u.SecretID == string(ref) {
			return u.Profiles, nil
		}
	}
	return nil, nil
}

// resolveLineRefRE matches one {{secret:NAME}} reference. NAME is any text
// up to the closing braces — vault inventory names carry spaces, so the
// grammar is deliberately permissive.
var resolveLineRefRE = regexp.MustCompile(`\{\{secret:(.+?)\}\}`)

func (s *secretService) PlanLine(ctx context.Context, line string) (LinePlan, error) {
	if err := s.guard.check(); err != nil {
		return LinePlan{}, err
	}
	plan := LinePlan{Line: line, Refs: []PlannedLineRef{}}
	locs := resolveLineRefRE.FindAllStringSubmatchIndex(line, -1)
	if len(locs) == 0 {
		return plan, nil
	}
	profiles, groups, err := s.loadConfig()
	if err != nil {
		return LinePlan{}, err
	}
	inputs := inventoryInputs(profiles, groups)
	// A sealed or uninitialized vault fails HERE, before any reference is
	// planned: the inventory is the vault's own read and it refuses while
	// shut. That failure is the actionable one the caller answers with —
	// distinct from "no such secret", and reached without holding anyone up.
	entries, err := s.vault.BuildInventory(ctx, inputs)
	if err != nil {
		return LinePlan{}, err
	}
	nameToRow := make(map[string]string, len(entries))
	for _, e := range entries {
		if _, exists := nameToRow[e.Name]; !exists {
			nameToRow[e.Name] = e.ID
		}
	}
	plan.Refs = make([]PlannedLineRef, 0, len(locs))
	for _, loc := range locs {
		name := line[loc[2]:loc[3]]
		ref := PlannedLineRef{Name: name, Start: loc[0], End: loc[1]}
		if row, ok := nameToRow[name]; ok {
			if id, ok := s.vault.ResolveRow(row, inputs); ok {
				ref.ID = id
			}
		}
		plan.Refs = append(plan.Refs, ref)
	}
	return plan, nil
}

// loadConfig reads the profile/group stores for the inventory projection.
func (s *secretService) loadConfig() ([]profile.SSHProfile, []profile.ProfileGroup, error) {
	profiles, err := s.profiles.LoadProfiles()
	if err != nil {
		return nil, nil, err
	}
	groups, err := s.groups.LoadGroups()
	if err != nil {
		return nil, nil, err
	}
	return profiles, groups, nil
}

// inventoryInputs loads the profile/group stores and projects the secret
// bindings into the vault's inventory input shape.
func (s *secretService) inventoryInputs() ([]vault.CredentialInventory, error) {
	profiles, groups, err := s.loadConfig()
	if err != nil {
		return nil, err
	}
	return inventoryInputs(profiles, groups), nil
}

// inventoryInputs projects profile secret bindings into the vault's
// inventory input shape: one entry per distinct bound secret, with its
// usage count and, for a single-use secret, the effective host and port of
// the sole profile (ADR-0017: a connection references a secret). It is the
// transport's vaultInventoryInputs, owned here so the vault-secret
// operations can compute their inputs without handing the handler the
// profile store.
func inventoryInputs(profiles []profile.SSHProfile, groups []profile.ProfileGroup) []vault.CredentialInventory {
	usage := profile.ComputeSecretUsage(profiles, groups, profile.SparseSSHOptions{})

	profByID := make(map[string]profile.SSHProfile, len(profiles))
	for _, p := range profiles {
		profByID[p.ID] = p
	}

	inputs := make([]vault.CredentialInventory, 0, len(usage))
	for _, u := range usage {
		ci := vault.CredentialInventory{
			SecretID:   u.SecretID,
			UsageCount: len(u.Profiles),
		}
		if len(u.Profiles) > 0 {
			if p, ok := profByID[u.Profiles[0].ProfileID]; ok {
				eff, resolveErr := profile.ResolveEffectiveProfile(p, groups, profile.SparseSSHOptions{})
				if resolveErr == nil {
					ci.Username = eff.ResolvedOptions.User
					ci.AuthMode = string(eff.ResolvedOptions.Auth)
					if len(u.Profiles) == 1 {
						ci.ID = u.Profiles[0].ProfileID
						ci.SingleHost = eff.ResolvedOptions.Host
						ci.SinglePort = eff.ResolvedOptions.Port
					}
				}
			}
		}
		inputs = append(inputs, ci)
	}
	return inputs
}
