package capability

import (
	"context"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/vault"
)

// RowResolver is the narrow vault surface the config write path needs:
// resolving a renderer row handle (secrow:...) to the stored reference
// (sec:v1:...) behind it (ADR-0017). *vault.Vault satisfies it. It is a
// seam, not a store handle: the only thing a config handler can reach
// through it is the answer to "which reference does this row name".
type RowResolver interface {
	ResolveRow(row string, inputs []vault.CredentialInventory) (credential.SecretID, bool)
}

// ConfigService is the config domain surface: profiles, groups, settings
// and the atomic import. It is what a ConfigOperation hands its callback.
//
// Row-handle contract: the WRITE methods take the renderer's wire form —
// secret bindings in options and defaults are row handles (secrow:...) —
// and resolve them to stored references (sec:v1:...) before storage, the
// way the transport's optionsFromWire/groupFromWire do today. The READ
// methods return the stored form (references); the handler applies the
// pure reference→row mapping (vault.RowFor) for responses. A handler
// constructed with a ConfigOperation therefore never needs the vault: the
// service is the only reach, and the service resolves.
type ConfigService interface {
	// Profiles.
	ListProfiles() ([]profile.SSHProfile, error)
	CreateProfile(p profile.SSHProfile) error
	UpdateProfile(p profile.SSHProfile) error
	DeleteProfile(id string) error
	// PatchProfile applies set/unset patch operations to one stored
	// profile and persists it. The three secret paths
	// (options.passwordSecret, options.keySecret,
	// options.keyPassphraseSecret) carry row handles and are resolved
	// before storage, exactly as profiles.patch resolves them today.
	PatchProfile(id string, sets map[string]any, unsets []string) error

	// Groups.
	ListGroups() ([]profile.ProfileGroup, error)
	CreateGroup(g profile.ProfileGroup) error
	UpdateGroup(g profile.ProfileGroup) error
	DeleteGroup(id string) error
	// ResolveGroup converts every row handle in g's defaults to its stored
	// reference — the pure read side of the row-resolution contract, needed
	// by the groups.impact handler to compare a PROPOSED group against the
	// stored references without storing anything.
	ResolveGroup(g profile.ProfileGroup) (profile.ProfileGroup, error)
	// DeleteGroupAtomic deletes a group and promotes its children to
	// root in one store write. Refuses when the wired store does not
	// support atomic deletion.
	DeleteGroupAtomic(id string) error
	// ApplyGroups atomically applies one or more full group updates —
	// the groups.apply write path for ParentGroupID and Defaults changes.
	// Group defaults carry row handles and are resolved before storage.
	ApplyGroups(gs []profile.ProfileGroup) error
	// ClearSecretRefs removes every reference to ref from the stored
	// profiles in one atomic write — the metadata-first half of secret
	// deletion (ADR-0011 §4).
	ClearSecretRefs(ref string) error

	// AtomicImport merges profiles and groups into the store atomically.
	AtomicImport(profiles []profile.SSHProfile, groups []profile.ProfileGroup) *profile.ImportResult

	// Settings is the settings surface of the config domain.
	Settings() SettingsService

	// Model roles resolve an agent's role choice against its model catalogue.
	// The roles surface and the ask transaction use the same resolver.
	// ListRoleAssignments returns the stored assignments; the wire's
	// roles.list completes them to the closed role set, so an unassigned
	// role is a visible null row, never an absent one.
	ListRoleAssignments() ([]profile.RoleAssignment, error)
	AssignRole(a profile.RoleAssignment) error
	ResolveRole(role profile.ModelRole, agentID string, models []harness.Model) (string, error)
}

// ConfigOperation is the typed operation for the config domain. Its admission
// is the store set it writes — config and vault together: the write paths
// resolve vault row handles and the secret-class settings are vault-backed, so
// a config operation conflicts with a vault operation even though the config
// handler itself never sees the vault. It is ONE plan (StoreGuard.Shared with
// both stores), never the config plan and the vault plan composed: the two
// share the vault-reset barrier, and taking a shared barrier twice in one call
// is the self-deadlock stores.go describes. See the package doc for the
// conservative-grain rationale.
type ConfigOperation interface {
	AssistantOperation
	Run(context.Context, func(context.Context, ConfigService) error) error
}

// NewConfigOperation builds a ConfigOperation over the plan its stores are
// read and written through — StoreGuard.Shared(a config store, the vault
// store), acquired once. Conflict admission is the only admission, so waiting
// conflict work never occupies anything else.
func NewConfigOperation(
	stores control.Admission,
	profiles profile.ProfileRepository,
	groups profile.GroupRepository,
	roles profile.RoleRepository,
	svc *profile.ProfileService,
	reg *settings.Registry,
	rows RowResolver,
) ConfigOperation {
	g := &guard{}
	return newOperation[ConfigService](Direct("ConfigOperation"), stores, g, newConfigService(g, profiles, groups, roles, svc, reg, rows))
}

// newConfigService builds the concrete config service bound to guard g.
// The guard is the operation's own, so a service that escapes its callback
// fails on its next use outside every in-flight Run.
func newConfigService(
	g *guard,
	profiles profile.ProfileRepository,
	groups profile.GroupRepository,
	roles profile.RoleRepository,
	svc *profile.ProfileService,
	reg *settings.Registry,
	rows RowResolver,
) *configService {
	return &configService{
		guard:    g,
		profiles: profiles,
		groups:   groups,
		roles:    roles,
		svc:      svc,
		settings: reg,
		rows:     rows,
	}
}

type configService struct {
	guard    *guard
	profiles profile.ProfileRepository
	groups   profile.GroupRepository
	roles    profile.RoleRepository
	svc      *profile.ProfileService
	settings *settings.Registry
	rows     RowResolver
}

func (s *configService) ListProfiles() ([]profile.SSHProfile, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.profiles.LoadProfiles()
}

func (s *configService) CreateProfile(p profile.SSHProfile) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	opts, err := s.resolveOptions(p.Options)
	if err != nil {
		return err
	}
	p.Options = opts
	return s.profiles.CreateProfile(p)
}

func (s *configService) UpdateProfile(p profile.SSHProfile) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	opts, err := s.resolveOptions(p.Options)
	if err != nil {
		return err
	}
	p.Options = opts
	return s.profiles.UpdateProfile(p)
}

func (s *configService) DeleteProfile(id string) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.profiles.DeleteProfile(id)
}

func (s *configService) PatchProfile(id string, sets map[string]any, unsets []string) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	all, err := s.profiles.LoadProfiles()
	if err != nil {
		return fmt.Errorf("load profiles: %w", err)
	}
	var target *profile.SSHProfile
	for i := range all {
		if all[i].ID == id {
			target = &all[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("profile %q not found", id)
	}
	opts := &target.Options
	for path, value := range sets {
		switch path {
		case "options.passwordSecret", "options.keySecret", "options.keyPassphraseSecret":
			row, isStr := value.(string)
			if !isStr {
				return fmt.Errorf("%s must be a string", path)
			}
			resolved, resolveErr := s.rowToRef(row)
			if resolveErr != nil {
				return resolveErr
			}
			value = resolved
		}
		profile.ApplyPatchSet(opts, path, value)
	}
	for _, path := range unsets {
		profile.ApplyPatchUnset(opts, path)
	}
	if opts.Host == "" {
		return errors.New("host is required and cannot be unset")
	}
	return s.profiles.UpdateProfile(*target)
}

func (s *configService) ListGroups() ([]profile.ProfileGroup, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.groups.LoadGroups()
}

func (s *configService) CreateGroup(g profile.ProfileGroup) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	resolved, err := s.resolveGroup(g)
	if err != nil {
		return err
	}
	return s.groups.CreateGroup(resolved)
}

func (s *configService) UpdateGroup(g profile.ProfileGroup) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	resolved, err := s.resolveGroup(g)
	if err != nil {
		return err
	}
	return s.groups.UpdateGroup(resolved)
}

// ResolveGroup converts every row handle in g's defaults to its stored
// reference — the pure read side of the row-resolution contract. The
// groups.impact handler needs it to compare a PROPOSED group's defaults
// against the stored references without storing anything.
func (s *configService) ResolveGroup(g profile.ProfileGroup) (profile.ProfileGroup, error) {
	if err := s.guard.check(); err != nil {
		return profile.ProfileGroup{}, err
	}
	return s.resolveGroup(g)
}

func (s *configService) DeleteGroup(id string) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.groups.DeleteGroup(id)
}

func (s *configService) DeleteGroupAtomic(id string) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	ag, ok := s.groups.(interface{ DeleteGroupAtomic(string) error })
	if !ok {
		return errors.New("group store does not support atomic delete")
	}
	return ag.DeleteGroupAtomic(id)
}

func (s *configService) ApplyGroups(gs []profile.ProfileGroup) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	resolved := make([]profile.ProfileGroup, len(gs))
	for i, g := range gs {
		r, err := s.resolveGroup(g)
		if err != nil {
			return err
		}
		resolved[i] = r
	}
	ag, ok := s.groups.(interface {
		ApplyGroups([]profile.ProfileGroup) error
	})
	if !ok {
		return errors.New("group store does not support atomic apply")
	}
	return ag.ApplyGroups(resolved)
}

func (s *configService) ClearSecretRefs(ref string) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	pc, ok := s.profiles.(interface{ ClearSecretRefs(string) error })
	if !ok {
		return errors.New("profile store does not support reference clearing")
	}
	return pc.ClearSecretRefs(ref)
}

func (s *configService) ListRoleAssignments() ([]profile.RoleAssignment, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	if s.roles == nil {
		return nil, errors.New("role store not wired")
	}
	return s.roles.LoadRoleAssignments()
}

// AssignRole persists one agent's role choice.
func (s *configService) AssignRole(a profile.RoleAssignment) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	if s.roles == nil {
		return errors.New("role store not wired")
	}
	return s.roles.AssignRole(a)
}

// ResolveRole reads current choices through the one pure resolver.
func (s *configService) ResolveRole(role profile.ModelRole, agentID string, models []harness.Model) (string, error) {
	assignments, err := s.ListRoleAssignments()
	if err != nil {
		return "", err
	}
	return profile.ResolveRole(role, agentID, assignments, models)
}

func (s *configService) AtomicImport(profiles []profile.SSHProfile, groups []profile.ProfileGroup) *profile.ImportResult {
	return s.svc.AtomicImport(profiles, groups)
}

func (s *configService) Settings() SettingsService {
	return &settingsService{guard: s.guard, reg: s.settings}
}

// resolveOptions converts every row handle in o to its stored reference.
func (s *configService) resolveOptions(o profile.StoredSSHProfileOptions) (profile.StoredSSHProfileOptions, error) {
	var err error
	if o.PasswordSecret, err = s.rowToRef(o.PasswordSecret); err != nil {
		return o, err
	}
	if o.KeySecret, err = s.rowToRef(o.KeySecret); err != nil {
		return o, err
	}
	if o.KeyPassphraseSecret, err = s.rowToRef(o.KeyPassphraseSecret); err != nil {
		return o, err
	}
	return o, nil
}

// resolveGroup converts every row handle in a group's defaults to its
// stored reference.
func (s *configService) resolveGroup(g profile.ProfileGroup) (profile.ProfileGroup, error) {
	if g.Defaults == nil {
		return g, nil
	}
	sp, err := s.resolveSparse(g.Defaults.SparseSSHOptions)
	if err != nil {
		return g, err
	}
	g.Defaults.SparseSSHOptions = sp
	return g, nil
}

// resolveSparse converts the row handles in sparse options to references.
func (s *configService) resolveSparse(sp profile.SparseSSHOptions) (profile.SparseSSHOptions, error) {
	var err error
	if sp.PasswordSecret != nil {
		if *sp.PasswordSecret, err = s.rowToRef(*sp.PasswordSecret); err != nil {
			return sp, err
		}
	}
	if sp.KeySecret != nil {
		if *sp.KeySecret, err = s.rowToRef(*sp.KeySecret); err != nil {
			return sp, err
		}
	}
	if sp.KeyPassphraseSecret != nil {
		if *sp.KeyPassphraseSecret, err = s.rowToRef(*sp.KeyPassphraseSecret); err != nil {
			return sp, err
		}
	}
	return sp, nil
}

// rowToRef resolves one row handle to its stored reference. Empty stays
// empty; an unknown row is an error (nocx-jb20.1).
func (s *configService) rowToRef(row string) (string, error) {
	if row == "" {
		return "", nil
	}
	if s.rows == nil {
		return "", errors.New("no vault: cannot resolve a secret row")
	}
	inputs, err := s.secretRowInputs()
	if err != nil {
		return "", err
	}
	id, ok := s.rows.ResolveRow(row, inputs)
	if !ok {
		return "", fmt.Errorf("unknown secret row %q", row)
	}
	return string(id), nil
}

// secretRowInputs returns the row set ResolveRow checks beyond the vault's
// own catalogue records: the secret references bound to stored profiles —
// the transport's secretRowInputs, owned here so the config write path can
// resolve rows without ever handing the handler the profile store.
func (s *configService) secretRowInputs() ([]vault.CredentialInventory, error) {
	profiles, err := s.profiles.LoadProfiles()
	if err != nil {
		return nil, err
	}
	inputs := make([]vault.CredentialInventory, 0, len(profiles))
	for _, p := range profiles {
		o := p.Options
		if o.PasswordSecret == "" && o.KeySecret == "" && o.KeyPassphraseSecret == "" {
			continue
		}
		inputs = append(inputs, vault.CredentialInventory{
			ID:                  p.ID,
			SecretID:            o.PasswordSecret,
			PassphraseSecretID:  o.KeyPassphraseSecret,
			KeyMaterialSecretID: o.KeySecret,
		})
	}
	return inputs, nil
}

// SettingsService is the settings surface of the config domain. It is a
// sub-surface of ConfigService (Settings()), never an independent
// operation: settings live on the same store family as profiles and groups
// and share the config gates.
type SettingsService interface {
	Descriptors() []settings.Descriptor
	Declarations() []settings.Declaration
	Groups() []settings.SettingsGroup
	SectionGroups() map[string]string
	GetSnapshot() (settings.SettingsSnapshot, error)
	Reset(d settings.Descriptor) error
	SetBool(b *settings.Bool, v bool) error
	SetString(s *settings.String, v string) error
	SetNumber(n *settings.Number, v float64) error
	SetSelect(s *settings.Select, v string) error
	SecretSet(s *settings.Secret, v string) error
	SecretDelete(s *settings.Secret) error
	SecretExists(s *settings.Secret) (bool, error)
}

type settingsService struct {
	guard *guard
	reg   *settings.Registry
}

func (s *settingsService) Descriptors() []settings.Descriptor {
	if !s.guard.ok() {
		return nil
	}
	return s.reg.Descriptors()
}

func (s *settingsService) Declarations() []settings.Declaration {
	if !s.guard.ok() {
		return nil
	}
	return s.reg.Declarations()
}

func (s *settingsService) Groups() []settings.SettingsGroup {
	if !s.guard.ok() {
		return nil
	}
	return s.reg.Groups()
}

func (s *settingsService) SectionGroups() map[string]string {
	if !s.guard.ok() {
		return nil
	}
	return s.reg.SectionGroups()
}

func (s *settingsService) GetSnapshot() (settings.SettingsSnapshot, error) {
	if err := s.guard.check(); err != nil {
		return settings.SettingsSnapshot{}, err
	}
	return s.reg.GetSnapshot()
}

func (s *settingsService) Reset(d settings.Descriptor) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.reg.Reset(d)
}

func (s *settingsService) SetBool(b *settings.Bool, v bool) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.reg.SetBool(b, v)
}

func (s *settingsService) SetString(st *settings.String, v string) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.reg.SetString(st, v)
}

func (s *settingsService) SetNumber(n *settings.Number, v float64) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.reg.SetNumber(n, v)
}

func (s *settingsService) SetSelect(sel *settings.Select, v string) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.reg.SetSelect(sel, v)
}

func (s *settingsService) SecretSet(sec *settings.Secret, v string) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.reg.SecretSet(sec, v)
}

func (s *settingsService) SecretDelete(sec *settings.Secret) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.reg.SecretDelete(sec)
}

func (s *settingsService) SecretExists(sec *settings.Secret) (bool, error) {
	if err := s.guard.check(); err != nil {
		return false, err
	}
	return s.reg.SecretExists(sec)
}

// newOperation wires the generic core: disposition, admission, guard and
// service. Every concrete operation constructor delegates here.
func newOperation[S any](disposition Disposition, admission control.Admission, g *guard, svc S) *operation[S] {
	if err := disposition.Validate(); err != nil {
		panic("capability: invalid operation disposition: " + err.Error())
	}
	gate, _ := answeringGateIn(admission)
	return &operation[S]{
		admission: admission, guard: g, service: svc, disposition: disposition,
		answeringGate: gate,
		// Whether the callback runs inside a held resource set. Asked once,
		// here, because the admission cannot change for the life of the
		// operation and Run must not walk a composite per call.
		ordered: control.HoldsOrder(admission),
	}
}
