package capability

import (
	"context"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/vault"
)

// ---------------------------------------------------------------------------
// Capture save — the vault + content settlement of a pending capture
// ---------------------------------------------------------------------------

// CaptureSaveService is the domain half of secrets.captureSave: create the
// vault secret (atomically name-collision-resolved), then rewrite every
// linked history row's redaction segment to the reference. The handler
// keeps the capture registry (connection-scoped in-memory state); this
// service owns the two stores the settlement writes. Never the other order
// — rewriting first can leave a reference to a secret that does not exist.
type CaptureSaveService interface {
	// CreateSecret stores the capture's value with its catalogue metadata
	// and atomic name-collision resolution; the name ACTUALLY used comes
	// back (the renderer must never predict that a suffixed name is free).
	CreateSecret(ctx context.Context, value credential.Secret, meta vault.SecretMeta) (credential.SecretID, string, error)
	// RewriteRedaction replaces one recorded row's redaction segment with a
	// vault reference. A row the retention sweep removed is ErrNotFound.
	//
	// The row is named by the capture link's entry id, which is a STRING
	// because two tables hold masked command text for exactly one more bead:
	// the interim command_history keys rows by an autoincrement rowid, and
	// the ledger's entries by the client-minted UUIDv7 the renderer sent.
	// The string is what both fit in; which store answers is decided in one
	// place, below.
	RewriteRedaction(ctx context.Context, entryID string, span content.Redaction, reference string) error
}

// CaptureSaveOperation is the typed operation for secrets.captureSave. Its
// gates are [vault, content].
type CaptureSaveOperation interface {
	AssistantOperation
	Run(context.Context, func(context.Context, CaptureSaveService) error) error
}

// NewCaptureSaveOperation builds a CaptureSaveOperation that acquires
// vaultGate before contentGate (the canonical order), then the execution
// lane.
func NewCaptureSaveOperation(
	vaultGate, contentGate, lane control.Admission,
	vaultLifecycle SecretVault,
	contentDB content.ContentDB,
) CaptureSaveOperation {
	g := &guard{}
	return newOperation[CaptureSaveService](
		Direct("CaptureSaveOperation"),
		control.NewComposite(vaultGate, contentGate, lane),
		g,
		newCaptureSaveService(g, vaultLifecycle, contentDB),
	)
}

// newCaptureSaveService builds the concrete capture-save service bound to
// guard g.
func newCaptureSaveService(g *guard, vaultLifecycle SecretVault, contentDB content.ContentDB) *captureSaveService {
	return &captureSaveService{guard: g, vault: vaultLifecycle, contentDB: contentDB}
}

type captureSaveService struct {
	guard     *guard
	vault     SecretVault
	contentDB content.ContentDB
}

func (s *captureSaveService) CreateSecret(ctx context.Context, value credential.Secret, meta vault.SecretMeta) (credential.SecretID, string, error) {
	if err := s.guard.check(); err != nil {
		return "", "", err
	}
	return s.vault.CreateNamedResolved(ctx, value, meta)
}

// RewriteRedaction replaces one masked span in a recorded command with the
// vault reference the user saved it as.
//
// IT USED TO BE A ROUTER, and the router is what nocx-rtg0.19 came to
// demolish. Two tables held masked command text — the interim command_history
// keyed by an AUTOINCREMENT rowid, the ledger keyed by a client-minted
// UUIDv7 — and this method chose between them by trying to parse the id as a
// decimal integer. The key spaces were disjoint by construction, so it was
// correct rather than a heuristic; it was still a dispatcher deciding which
// database to use from the SHAPE of a string, which is exactly the kind of
// scaffolding that outlives its reason when nobody writes down that it was
// scaffolding.
//
// command_history is gone, so there is one store and nothing to choose. An id
// naming no entry comes back ErrNotFound, which is the answer a swept row
// already gave and which the caller already treats as "nothing to rewrite".
func (s *captureSaveService) RewriteRedaction(ctx context.Context, entryID string, span content.Redaction, reference string) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.contentDB.Ledger().RewriteRedaction(ctx, entryID, span, reference)
}

// ---------------------------------------------------------------------------
// Tabby import — the config + vault write of profiles.tabby*
// ---------------------------------------------------------------------------

// TabbyImportService is the domain surface of the Tabby import flow: the
// config reads the planner needs, the vault write the executor needs, and
// the atomic config write. The plan/parse logic stays with the handler
// (it owns the Tabby YAML grammar); this service is the only store access.
type TabbyImportService interface {
	ListProfiles() ([]profile.SSHProfile, error)
	ListGroups() ([]profile.ProfileGroup, error)
	// CreateSecret stores value with its catalogue metadata (ADR-0016);
	// when no vault is wired, the plain store records it namelessly.
	CreateSecret(ctx context.Context, value credential.Secret, meta vault.SecretMeta) (credential.SecretID, error)
	AtomicImport(profiles []profile.SSHProfile, groups []profile.ProfileGroup) *profile.ImportResult
}

// TabbyImportOperation is the typed operation for profiles.importTabby,
// profiles.tabbyPreview and profiles.tabbyExecute. Its gates are
// [config, vault].
type TabbyImportOperation interface {
	AssistantOperation
	Run(context.Context, func(context.Context, TabbyImportService) error) error
}

// NewTabbyImportOperation builds a TabbyImportOperation that acquires
// configGate before vaultGate (the canonical order), then the execution
// lane.
func NewTabbyImportOperation(
	configGate, vaultGate, lane control.Admission,
	profiles profile.ProfileRepository,
	groups profile.GroupRepository,
	svc *profile.ProfileService,
	vaultLifecycle SecretVault,
	store credential.SecretStore,
) TabbyImportOperation {
	g := &guard{}
	return newOperation[TabbyImportService](
		Direct("TabbyImportOperation"),
		control.NewComposite(configGate, vaultGate, lane),
		g,
		newTabbyImportService(g, profiles, groups, svc, vaultLifecycle, store),
	)
}

// newTabbyImportService builds the concrete tabby-import service bound to
// guard g.
func newTabbyImportService(
	g *guard,
	profiles profile.ProfileRepository,
	groups profile.GroupRepository,
	svc *profile.ProfileService,
	vaultLifecycle SecretVault,
	store credential.SecretStore,
) *tabbyImportService {
	return &tabbyImportService{guard: g, profiles: profiles, groups: groups, svc: svc, vault: vaultLifecycle, store: store}
}

type tabbyImportService struct {
	guard    *guard
	profiles profile.ProfileRepository
	groups   profile.GroupRepository
	svc      *profile.ProfileService
	vault    SecretVault
	store    credential.SecretStore
}

func (s *tabbyImportService) ListProfiles() ([]profile.SSHProfile, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.profiles.LoadProfiles()
}

func (s *tabbyImportService) ListGroups() ([]profile.ProfileGroup, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.groups.LoadGroups()
}

func (s *tabbyImportService) CreateSecret(ctx context.Context, value credential.Secret, meta vault.SecretMeta) (credential.SecretID, error) {
	if err := s.guard.check(); err != nil {
		return "", err
	}
	if s.vault == nil {
		return s.store.Create(ctx, value)
	}
	return s.vault.CreateNamed(ctx, value, meta)
}

func (s *tabbyImportService) AtomicImport(profiles []profile.SSHProfile, groups []profile.ProfileGroup) *profile.ImportResult {
	return s.svc.AtomicImport(profiles, groups)
}
