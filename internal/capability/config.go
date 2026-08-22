package capability

import (
	"context"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/credential"
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

// EndpointSecrets is the vault surface the endpoint write paths need
// (ADR-0030): mint a secret from its value, rotate the material behind the
// endpoint's own secret (same id, same name — ADR-0017 §2's rotate), and
// destroy the material behind one. Satisfied by *vault.Vault. Narrow on
// purpose — endpoint CRUD touches nothing else in the vault.
type EndpointSecrets interface {
	CreateNamed(ctx context.Context, value credential.Secret, meta vault.SecretMeta) (credential.SecretID, error)
	ReplaceSecret(ctx context.Context, row string, value credential.Secret, inputs []vault.CredentialInventory) error
	Delete(ctx context.Context, id credential.SecretID) error
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

	// Endpoints (ADR-0030, nocx-rzjw). The write methods take the endpoint
	// record in WIRE form — CredentialRef and header ValueRefs are renderer
	// row handles (secrow:...) the service resolves to stored references
	// before anything is written, exactly like profile options — plus the
	// API key as an input: a credential.Secret that is minted (create),
	// rotated (update with a new key) or left alone (update without one).
	// A key and a key row are mutually exclusive. The key never survives
	// the call, never crosses back, and never appears in a result. The ctx
	// bounds the vault calls (mint, rotate, material delete), exactly as
	// TabbyImportService.CreateSecret's does.
	ListEndpoints() ([]profile.Endpoint, error)
	// GetEndpoint returns the stored endpoint with the given id — the
	// single-record lookup the Test button's credential resolution needs
	// (nocx-reu5): the probe names the endpoint and the backend resolves
	// the credential it owns, exactly as connections.test resolves a
	// profile by its id. profile.ErrEndpointNotFound when none exists.
	GetEndpoint(id string) (profile.Endpoint, error)
	// ResolveSecretRow maps a renderer row handle to its stored reference —
	// the endpoints.probe draft headers are wire form (row handles) and
	// must resolve before the probe dials, so the probe handler can resolve
	// them without ever holding the vault.
	ResolveSecretRow(row string) (string, error)
	// CreateEndpoint stores the endpoint, minting key into the vault first
	// when it is non-empty: the material must exist before the record
	// references it (ADR-0011 §4's order, ADR-0030). A row handle in
	// e.CredentialRef references an existing secret instead of minting.
	CreateEndpoint(ctx context.Context, e profile.Endpoint, key credential.Secret) (profile.Endpoint, error)
	// UpdateEndpoint replaces the record. A nil or empty key keeps the
	// existing credential — "absent or empty" means "keep the existing
	// material", never "erase it" (design §4.5.4); a non-empty one rotates
	// the material behind the endpoint's OWN secret (same id, same name)
	// or mints when the endpoint had none; a row handle in e.CredentialRef
	// references an existing secret instead.
	UpdateEndpoint(ctx context.Context, e profile.Endpoint, key *credential.Secret) (profile.Endpoint, error)
	// DeleteEndpoint removes the record (clearing its reference from every
	// remaining record in the same write) and then deletes the material,
	// metadata-first.
	DeleteEndpoint(ctx context.Context, id string) error

	// AtomicImport merges profiles and groups into the store atomically.
	AtomicImport(profiles []profile.SSHProfile, groups []profile.ProfileGroup) *profile.ImportResult

	// Settings is the settings surface of the config domain.
	Settings() SettingsService
}

// ConfigOperation is the typed operation for the config domain. Its gates
// are [config, vault]: the write paths resolve vault row handles and the
// secret-class settings are vault-backed, so a config operation conflicts
// with a vault operation even though the config handler itself never sees
// the vault. See the package doc for the conservative-grain rationale.
type ConfigOperation interface {
	Run(context.Context, func(context.Context, ConfigService) error) error
}

// NewConfigOperation builds a ConfigOperation that acquires configGate
// before vaultGate (the canonical order), then the execution lane: conflict
// admission precedes the worker permit, so waiting conflict work never sits
// on a lane slot.
func NewConfigOperation(
	configGate, vaultGate, lane control.Admission,
	profiles profile.ProfileRepository,
	groups profile.GroupRepository,
	endpoints profile.EndpointRepository,
	svc *profile.ProfileService,
	reg *settings.Registry,
	rows RowResolver,
	secrets EndpointSecrets,
) ConfigOperation {
	g := &guard{}
	return newOperation[ConfigService](control.NewComposite(configGate, vaultGate, lane), g, newConfigService(g, profiles, groups, endpoints, svc, reg, rows, secrets))
}

// newConfigService builds the concrete config service bound to guard g.
// The guard is the operation's own, so a service that escapes its callback
// fails on its next use outside every in-flight Run.
func newConfigService(
	g *guard,
	profiles profile.ProfileRepository,
	groups profile.GroupRepository,
	endpoints profile.EndpointRepository,
	svc *profile.ProfileService,
	reg *settings.Registry,
	rows RowResolver,
	secrets EndpointSecrets,
) *configService {
	return &configService{
		guard:     g,
		profiles:  profiles,
		groups:    groups,
		endpoints: endpoints,
		svc:       svc,
		settings:  reg,
		rows:      rows,
		secrets:   secrets,
	}
}

type configService struct {
	guard     *guard
	profiles  profile.ProfileRepository
	groups    profile.GroupRepository
	endpoints profile.EndpointRepository
	svc       *profile.ProfileService
	settings  *settings.Registry
	rows      RowResolver
	secrets   EndpointSecrets
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

func (s *configService) ListEndpoints() ([]profile.Endpoint, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.endpoints.LoadEndpoints()
}

// GetEndpoint returns one stored endpoint by id, or a wrapped
// profile.ErrEndpointNotFound when none exists — the Test button's
// credential resolution names a record and the backend resolves its
// credential (nocx-reu5), so the lookup must be distinguishable from a
// store failure.
func (s *configService) GetEndpoint(id string) (profile.Endpoint, error) {
	if err := s.guard.check(); err != nil {
		return profile.Endpoint{}, err
	}
	ep, err := s.loadEndpoint(id)
	if err != nil {
		return profile.Endpoint{}, err
	}
	if ep == nil {
		return profile.Endpoint{}, fmt.Errorf("%s: %w", id, profile.ErrEndpointNotFound)
	}
	return *ep, nil
}

// ResolveSecretRow maps a renderer row handle to its stored reference — the
// probe's draft header rows (nocx-lyyk). A read that REPORTS: the resolution
// needs the same inventory inputs the write paths use, so it lives on the
// service, and the probe handler calls it without ever holding the vault.
func (s *configService) ResolveSecretRow(row string) (string, error) {
	if err := s.guard.check(); err != nil {
		return "", err
	}
	return s.rowToRef(row)
}

// CreateEndpoint stores the endpoint, minting the key into the vault FIRST
// when one is given: the material must exist before the record references
// it, so a crash between the two leaves an ownerless secret the vault's
// journal retires rather than a record pointing at a secret that cannot
// exist (ADR-0011 §4's order, ADR-0030). The record is validated BEFORE
// the mint — a bad record must not orphan a freshly-minted key.
//
// The endpoint may instead REFERENCE a secret the vault already holds: the
// renderer's row handle in e.CredentialRef (and in header ValueRefs) is
// resolved to the stored reference before anything is written, exactly as
// profile options resolve today. A typed key and a key row are mutually
// exclusive — one source per credential, checked here as the backstop to the
// wire's own check (nocx-rzjw).
func (s *configService) CreateEndpoint(ctx context.Context, e profile.Endpoint, key credential.Secret) (profile.Endpoint, error) {
	if err := s.guard.check(); err != nil {
		return profile.Endpoint{}, err
	}
	if err := profile.ValidateEndpoint(e); err != nil {
		return profile.Endpoint{}, err
	}
	// Row handles resolve BEFORE the mint or the write: a bad row must not
	// orphan a freshly-minted key (the same ordering as validation).
	headers, err := s.resolveEndpointHeaders(e.Headers)
	if err != nil {
		return profile.Endpoint{}, err
	}
	e.Headers = headers
	if e.CredentialRef != "" {
		if !key.IsEmpty() {
			return profile.Endpoint{}, errors.New("endpoint credential has two sources: a typed key and a key row are mutually exclusive")
		}
		ref, rowErr := s.rowToRef(e.CredentialRef)
		if rowErr != nil {
			return profile.Endpoint{}, rowErr
		}
		e.CredentialRef = ref
	} else if !key.IsEmpty() {
		if s.secrets == nil {
			return profile.Endpoint{}, errors.New("endpoint credentials unavailable: vault not wired")
		}
		id, err := s.secrets.CreateNamed(ctx, key, vault.SecretMeta{
			Name: endpointKeyName(e.Name),
			Kind: vault.KindPassword,
		})
		if err != nil {
			return profile.Endpoint{}, fmt.Errorf("store endpoint key: %w", err)
		}
		e.CredentialRef = string(id)
	}
	if err := s.endpoints.CreateEndpoint(e); err != nil {
		return profile.Endpoint{}, err
	}
	return e, nil
}

// resolveEndpointHeaders converts every renderer row handle in the
// wire-form header list to its stored reference — the header-value half of
// the row-resolution contract, exactly like resolveOptions for profile
// options. Literal values pass through untouched.
func (s *configService) resolveEndpointHeaders(headers []profile.EndpointHeader) ([]profile.EndpointHeader, error) {
	if len(headers) == 0 {
		return headers, nil
	}
	out := make([]profile.EndpointHeader, len(headers))
	for i, h := range headers {
		if h.ValueRef == "" {
			out[i] = h
			continue
		}
		ref, err := s.rowToRef(h.ValueRef)
		if err != nil {
			return nil, fmt.Errorf("header %q: %w", h.Name, err)
		}
		out[i] = profile.EndpointHeader{Name: h.Name, Value: h.Value, ValueRef: ref}
	}
	return out, nil
}

// UpdateEndpoint replaces the record. Three credential sources, one per
// update:
//
//   - e.CredentialRef names a row handle (the form's "use an existing
//     secret" choice, nocx-rzjw): it is resolved and referenced. The swap
//     touches no material — nothing is minted, rotated or deleted — and an
//     abandoned owned key simply stops being referenced, staying visible on
//     the Secrets page where ADR-0016 makes ownerless secrets first-class.
//   - A nil or empty key keeps the existing credential (design §4.5.4).
//   - A non-empty key rotates the material behind the endpoint's OWN secret —
//     same id, same name (ADR-0017 §2's rotate, which is why an update never
//     orphans a key and never dangles another record that happens to share
//     the secret) — or mints when the endpoint had no credential.
//
// Vault-first for the key paths: the rotation's reference never changes, so
// a record write that fails afterwards leaves the endpoint with its old
// fields and the rotated material — consistent, and the user retries.
func (s *configService) UpdateEndpoint(ctx context.Context, e profile.Endpoint, key *credential.Secret) (profile.Endpoint, error) {
	if err := s.guard.check(); err != nil {
		return profile.Endpoint{}, err
	}
	if err := profile.ValidateEndpoint(e); err != nil {
		return profile.Endpoint{}, err
	}

	existing, err := s.loadEndpoint(e.ID)
	if err != nil {
		return profile.Endpoint{}, err
	}
	if existing == nil {
		return profile.Endpoint{}, fmt.Errorf("%s: %w", e.ID, profile.ErrEndpointNotFound)
	}

	headers, err := s.resolveEndpointHeaders(e.Headers)
	if err != nil {
		return profile.Endpoint{}, err
	}
	e.Headers = headers

	switch {
	case e.CredentialRef != "":
		if key != nil && !key.IsEmpty() {
			return profile.Endpoint{}, errors.New("endpoint credential has two sources: a typed key and a key row are mutually exclusive")
		}
		ref, rowErr := s.rowToRef(e.CredentialRef)
		if rowErr != nil {
			return profile.Endpoint{}, rowErr
		}
		e.CredentialRef = ref
	case key != nil && !key.IsEmpty():
		if s.secrets == nil {
			return profile.Endpoint{}, errors.New("endpoint credentials unavailable: vault not wired")
		}
		if existing.CredentialRef == "" {
			// The endpoint had no key; this update adds one. mintID and
			// mintErr are distinct names on purpose: err already lives at
			// function scope, and the repo's lint gate flags the shadow.
			mintID, mintErr := s.secrets.CreateNamed(ctx, *key, vault.SecretMeta{
				Name: endpointKeyName(e.Name),
				Kind: vault.KindPassword,
			})
			if mintErr != nil {
				return profile.Endpoint{}, fmt.Errorf("store endpoint key: %w", mintErr)
			}
			e.CredentialRef = string(mintID)
		} else {
			// Rotate the material behind the endpoint's own secret. The
			// catalogue record resolves the row without inventory inputs.
			if err := s.secrets.ReplaceSecret(ctx, vault.RowFor(credential.SecretID(existing.CredentialRef)), *key, nil); err != nil {
				return profile.Endpoint{}, fmt.Errorf("rotate endpoint key: %w", err)
			}
			e.CredentialRef = existing.CredentialRef
		}
	default:
		// Keep the existing credential, whatever it is.
		e.CredentialRef = existing.CredentialRef
	}

	if err := s.endpoints.UpdateEndpoint(e); err != nil {
		return profile.Endpoint{}, err
	}
	return e, nil
}

// DeleteEndpoint removes the record — one atomic store write that also
// clears its reference from every remaining record (ADR-0030) — then
// deletes the material through the vault, metadata-first (ADR-0011 §4).
//
// The record deletion never depends on the vault: the endpoint is the
// user's intent, and a keyless endpoint needs no vault at all. The
// material delete is best-effort exactly as vault.deleteSecret's is: a
// provider failure leaves the vault's journaled pending delete, retried by
// Reconcile at the next start, and a sealed vault refuses before
// journaling, leaving the secret visible and deletable on the Secrets
// page. When the vault seam is missing (impossible in the production
// composition root, which always wires the vault), the record is still
// removed and the secret remains visible on the Secrets page.
func (s *configService) DeleteEndpoint(ctx context.Context, id string) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	ref, err := s.endpoints.DeleteEndpoint(id)
	if err != nil {
		return err
	}
	if ref == "" || s.secrets == nil {
		return nil // no credential to remove, or no vault to remove it with
	}
	_ = s.secrets.Delete(ctx, credential.SecretID(ref))
	return nil
}

// loadEndpoint returns the stored endpoint with the given id, or nil when
// none exists.
func (s *configService) loadEndpoint(id string) (*profile.Endpoint, error) {
	all, err := s.endpoints.LoadEndpoints()
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, nil
}

// endpointKeyName derives the auto-name of an endpoint's minted key
// (ADR-0016: the name is generated from what is already known — the
// endpoint's display name — never from the material).
func endpointKeyName(endpointName string) string {
	return fmt.Sprintf("%s API key", endpointName)
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
	SetPaths(p *settings.PathList, v []string) error
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

func (s *settingsService) SetPaths(p *settings.PathList, v []string) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.reg.SetPaths(p, v)
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

// newOperation wires the generic core: admission, guard and service. Every
// concrete operation constructor delegates here.
func newOperation[S any](admission control.Admission, g *guard, svc S) *operation[S] {
	return &operation[S]{admission: admission, guard: g, service: svc}
}
