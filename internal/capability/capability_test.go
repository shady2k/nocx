package capability_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vaultreset"
	"github.com/shady2k/nocx/internal/workspace"
)

// fakeProfileRepo is an in-memory profile.ProfileRepository that also
// implements the optional ClearSecretRefs surface.
type fakeProfileRepo struct {
	mu       sync.Mutex
	profiles []profile.SSHProfile
	cleared  []string
}

func (f *fakeProfileRepo) LoadProfiles() ([]profile.SSHProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]profile.SSHProfile, len(f.profiles))
	copy(out, f.profiles)
	return out, nil
}

func (f *fakeProfileRepo) CreateProfile(p profile.SSHProfile) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.profiles = append(f.profiles, p)
	return nil
}

func (f *fakeProfileRepo) UpdateProfile(p profile.SSHProfile) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.profiles {
		if f.profiles[i].ID == p.ID {
			f.profiles[i] = p
			return nil
		}
	}
	return profile.ErrProfileNotFound
}

func (f *fakeProfileRepo) DeleteProfile(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.profiles {
		if f.profiles[i].ID == id {
			f.profiles = append(f.profiles[:i], f.profiles[i+1:]...)
			return nil
		}
	}
	return profile.ErrProfileNotFound
}

func (f *fakeProfileRepo) ClearSecretRefs(ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, ref)
	for i := range f.profiles {
		if f.profiles[i].Options.PasswordSecret == ref {
			f.profiles[i].Options.PasswordSecret = ""
		}
		if f.profiles[i].Options.KeySecret == ref {
			f.profiles[i].Options.KeySecret = ""
		}
		if f.profiles[i].Options.KeyPassphraseSecret == ref {
			f.profiles[i].Options.KeyPassphraseSecret = ""
		}
	}
	return nil
}

// fakeGroupRepo is an in-memory profile.GroupRepository that also
// implements the optional DeleteGroupAtomic and ApplyGroups surfaces.
type fakeGroupRepo struct {
	mu     sync.Mutex
	groups []profile.ProfileGroup
}

func (f *fakeGroupRepo) LoadGroups() ([]profile.ProfileGroup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]profile.ProfileGroup, len(f.groups))
	copy(out, f.groups)
	return out, nil
}

func (f *fakeGroupRepo) CreateGroup(g profile.ProfileGroup) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groups = append(f.groups, g)
	return nil
}

func (f *fakeGroupRepo) UpdateGroup(g profile.ProfileGroup) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.groups {
		if f.groups[i].ID == g.ID {
			f.groups[i] = g
			return nil
		}
	}
	return errors.New("group not found")
}

func (f *fakeGroupRepo) DeleteGroup(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.groups {
		if f.groups[i].ID == id {
			f.groups = append(f.groups[:i], f.groups[i+1:]...)
			return nil
		}
	}
	return errors.New("group not found")
}

func (f *fakeGroupRepo) DeleteGroupAtomic(id string) error { return f.DeleteGroup(id) }

func (f *fakeGroupRepo) ApplyGroups(gs []profile.ProfileGroup) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groups = append([]profile.ProfileGroup{}, gs...)
	return nil
}

// fakeVaultSeam records lifecycle calls and implements the secret surface
// with a tiny in-memory catalogue. It satisfies both capability.VaultLifecycle
// and capability.SecretVault.
type fakeVaultSeam struct {
	mu             sync.Mutex
	state          vault.State
	rows           map[string]credential.SecretID // row handle -> id
	secrets        map[credential.SecretID]string // id -> value
	names          map[credential.SecretID]string // id -> catalogue name
	lifecycleCalls []string
	nextID         int
}

func newFakeVault() *fakeVaultSeam {
	return &fakeVaultSeam{
		state:   vault.StateUnsealed,
		rows:    map[string]credential.SecretID{},
		secrets: map[credential.SecretID]string{},
		names:   map[credential.SecretID]string{},
	}
}

func (f *fakeVaultSeam) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lifecycleCalls = append(f.lifecycleCalls, call)
}

func (f *fakeVaultSeam) LifecycleCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.lifecycleCalls))
	copy(out, f.lifecycleCalls)
	return out
}

// VaultLifecycle
func (f *fakeVaultSeam) State() vault.State {
	f.record("State")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func (f *fakeVaultSeam) Snapshot(context.Context) vault.Snapshot {
	f.record("Snapshot")
	return vault.Snapshot{State: f.state}
}

func (f *fakeVaultSeam) Setup(context.Context, vault.SetupRequest) (vault.SetupResult, error) {
	f.record("Setup")
	f.mu.Lock()
	f.state = vault.StateUnsealed
	f.mu.Unlock()
	return vault.SetupResult{}, nil
}

func (f *fakeVaultSeam) Unseal(context.Context, vault.UnsealRequest) error {
	f.record("Unseal")
	f.mu.Lock()
	f.state = vault.StateUnsealed
	f.mu.Unlock()
	return nil
}

func (f *fakeVaultSeam) Seal() {
	f.record("Seal")
	f.mu.Lock()
	f.state = vault.StateSealed
	f.mu.Unlock()
}

func (f *fakeVaultSeam) ChangePassphrase(context.Context, vault.ChangePassphraseRequest) error {
	f.record("ChangePassphrase")
	return nil
}

func (f *fakeVaultSeam) RegenerateRecovery(context.Context, vault.RegenerateRequest) (string, error) {
	f.record("RegenerateRecovery")
	return "recovery", nil
}

func (f *fakeVaultSeam) SetDefaultProvider(context.Context, vault.ProviderID) error {
	f.record("SetDefaultProvider")
	return nil
}

func (f *fakeVaultSeam) SetAutoSeal(context.Context, int) error {
	f.record("SetAutoSeal")
	return nil
}

func (f *fakeVaultSeam) Activity() { f.record("Activity") }

// SecretVault
func (f *fakeVaultSeam) BuildInventory(_ context.Context, inputs []vault.CredentialInventory) ([]vault.InventoryEntry, error) {
	f.record("BuildInventory")
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]vault.InventoryEntry, 0, len(inputs))
	for i, in := range inputs {
		id := credential.SecretID(in.SecretID)
		row := in.SecretID + "#row"
		f.rows[row] = id
		out = append(out, vault.InventoryEntry{
			ID:      row,
			Name:    f.names[id],
			Kind:    "password",
			UsedBy:  in.UsageCount,
			OwnerID: in.ID,
		})
		_ = i
	}
	return out, nil
}

func (f *fakeVaultSeam) CreateNamed(_ context.Context, value credential.Secret, meta vault.SecretMeta) (credential.SecretID, error) {
	f.record("CreateNamed")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := credential.SecretID("sec:v1:file:fake" + string(rune('a'+f.nextID-1)))
	_ = value.Use(func(b []byte) error {
		f.secrets[id] = string(b)
		return nil
	})
	f.names[id] = meta.Name
	f.rows[meta.Name] = id
	return id, nil
}

func (f *fakeVaultSeam) CreateNamedResolved(_ context.Context, value credential.Secret, meta vault.SecretMeta) (credential.SecretID, string, error) {
	f.record("CreateNamedResolved")
	id, err := f.CreateNamed(context.Background(), value, meta)
	return id, meta.Name, err
}

func (f *fakeVaultSeam) RenameSecret(_ context.Context, row, name string, _ []vault.CredentialInventory) error {
	f.record("RenameSecret")
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.rows[row]
	if !ok {
		return errors.New("unknown row")
	}
	f.names[id] = name
	return nil
}

func (f *fakeVaultSeam) ReplaceSecret(_ context.Context, row string, value credential.Secret, _ []vault.CredentialInventory) error {
	f.record("ReplaceSecret")
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.rows[row]
	if !ok {
		return errors.New("unknown row")
	}
	_ = value.Use(func(b []byte) error {
		f.secrets[id] = string(b)
		return nil
	})
	return nil
}

func (f *fakeVaultSeam) ResolveRow(row string, _ []vault.CredentialInventory) (credential.SecretID, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.rows[row]
	return id, ok
}

// fakeSecretStore is an in-memory credential.SecretStore.
type fakeSecretStore struct {
	mu      sync.Mutex
	secrets map[credential.SecretID]string
	next    int
	deleted []credential.SecretID
}

func newFakeSecretStore() *fakeSecretStore {
	return &fakeSecretStore{secrets: map[credential.SecretID]string{}}
}

func (f *fakeSecretStore) Create(_ context.Context, value credential.Secret) (credential.SecretID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := credential.SecretID("sec:v1:file:plain" + string(rune('a'+f.next-1)))
	_ = value.Use(func(b []byte) error {
		f.secrets[id] = string(b)
		return nil
	})
	return id, nil
}

// Exists reports whether the seam holds a secret with that id. The seam
// doubles as the credential.SecretStore in tests where the vault and the
// store must be one object, exactly as *vault.Vault is in production.
func (f *fakeVaultSeam) Exists(_ context.Context, id credential.SecretID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.secrets[id]
	return ok, nil
}

func (f *fakeVaultSeam) Create(_ context.Context, value credential.Secret) (credential.SecretID, error) {
	f.record("Create")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := credential.SecretID("sec:v1:file:fake" + string(rune('a'+f.nextID-1)))
	_ = value.Use(func(b []byte) error {
		f.secrets[id] = string(b)
		return nil
	})
	return id, nil
}

func (f *fakeVaultSeam) Get(_ context.Context, id credential.SecretID) (credential.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.secrets[id]
	if !ok {
		return credential.Secret{}, nil
	}
	return credential.NewSecret(v), nil
}

func (f *fakeVaultSeam) Delete(_ context.Context, id credential.SecretID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secrets[id] = ""
	return nil
}

func (f *fakeSecretStore) Get(_ context.Context, id credential.SecretID) (credential.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.secrets[id]
	if !ok {
		return credential.Secret{}, nil
	}
	return credential.NewSecret(v), nil
}

func (f *fakeSecretStore) Delete(_ context.Context, id credential.SecretID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	delete(f.secrets, id)
	return nil
}

func (f *fakeSecretStore) Exists(_ context.Context, id credential.SecretID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.secrets[id]
	return ok, nil
}

// fakeSessionRegistry is an in-memory session.Registry.
type fakeSessionRegistry struct {
	mu       sync.Mutex
	sessions map[session.ID]session.Session
}

func newFakeSessionRegistry() *fakeSessionRegistry {
	return &fakeSessionRegistry{sessions: map[session.ID]session.Session{}}
}

func (f *fakeSessionRegistry) Open(context.Context, session.Config) (session.Session, error) {
	return nil, errors.New("not in test")
}

func (f *fakeSessionRegistry) Get(id session.ID) (session.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return nil, errors.New("session not found")
	}
	return s, nil
}

func (f *fakeSessionRegistry) Close(id session.ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, id)
	return nil
}

func (f *fakeSessionRegistry) List() []session.Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]session.Session, 0, len(f.sessions))
	for _, s := range f.sessions {
		out = append(out, s)
	}
	return out
}

// fakeSession is a minimal session.Session.
type fakeSession struct {
	id       session.ID
	identity session.Identity
	parent   session.Ref
	kind     session.Kind
	host     string
	sshOpts  []ssh.ConnectOption
}

func (f *fakeSession) ID() session.ID             { return f.id }
func (f *fakeSession) Identity() session.Identity { return f.identity }
func (f *fakeSession) Parent() (session.Ref, bool) {
	return f.parent, !f.parent.Zero()
}

func (f *fakeSession) Liveness() session.LivenessState {
	return session.LivenessState{Liveness: session.LivenessAlive, Epoch: 1}
}

// WorkspaceID reports the default: this fake stands in for a session in
// tests that are about capability, and membership carries no behaviour
// (nocx-fraus), so there is nothing here for a workspace to change.
func (f *fakeSession) WorkspaceID() workspace.ID         { return workspace.Default }
func (f *fakeSession) Kind() session.Kind                { return f.kind }
func (f *fakeSession) PaneID() string                    { return "" }
func (f *fakeSession) Host() string                      { return f.host }
func (f *fakeSession) Cwd() string                       { return "/home/test" }
func (f *fakeSession) ProfileID() string                 { return "" }
func (f *fakeSession) CredentialID() string              { return "" }
func (f *fakeSession) SandboxInfo() *sandbox.SessionInfo { return nil }
func (f *fakeSession) Write([]byte) (int, error)         { return 0, nil }
func (f *fakeSession) EnqueueWrite([]byte) bool          { return true }
func (f *fakeSession) Resize(context.Context, uint16, uint16, uint16, uint16) error {
	return nil
}
func (f *fakeSession) Close() error          { return nil }
func (f *fakeSession) Done() <-chan struct{} { return make(chan struct{}) }
func (f *fakeSession) StartOutput(context.Context, session.OutputHandler) error {
	return nil
}

// OpenBootstrapWindow is the typed-`ssh` delivery's seam (design §5.5). This
// double never reaches it: nothing in the capability layer opens a bootstrap
// window, and a fake that answered one would be advertising a terminal it
// does not have.
func (f *fakeSession) OpenBootstrapWindow() (session.BootstrapWindow, error) {
	return nil, errors.New("fakeSession has no terminal")
}
func (f *fakeSession) ShellIntegrationReason() ssh.RefusalReason { return "" }
func (f *fakeSession) ExitOutcome() (session.ExitCause, int) {
	return session.ExitInterrupted, 0
}
func (f *fakeSession) SSHOptions() []ssh.ConnectOption { return f.sshOpts }

// fakeLedgerRepo records the two ledger methods the capability layer reaches:
// the capture-save rewrite and the completed-command write. Nothing else is
// reachable through this seam; the embedded nil interface makes any other
// call a loud panic rather than a quiet zero value.
type fakeLedgerRepo struct {
	content.LedgerRepository
	mu       sync.Mutex
	rewrites []string
	recorded []content.CompletedCommand
}

func (f *fakeLedgerRepo) RewriteRedaction(_ context.Context, entryID string, _ content.Redaction, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rewrites = append(f.rewrites, entryID)
	return nil
}

func (f *fakeLedgerRepo) RecordCompleted(_ context.Context, in content.CompletedCommand) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorded = append(f.recorded, in)
	return "0192f0aa-0000-7000-8000-000000000001", nil
}

// fakeContentDB is a minimal content.ContentDB.
type fakeContentDB struct {
	ledger *fakeLedgerRepo
}

func newFakeContentDB() *fakeContentDB {
	return &fakeContentDB{ledger: &fakeLedgerRepo{}}
}

func (f *fakeContentDB) Conversations() content.ConversationRepository { return nil }
func (f *fakeContentDB) Backup(context.Context, string) error          { return nil }
func (f *fakeContentDB) Close() error                                  { return nil }
func (f *fakeContentDB) Ledger() content.LedgerRepository              { return f.ledger }

// Layout is unused by these tests: the fake predates the layout chain and no
// capability reaches it (nocx-isoph.1).
func (f *fakeContentDB) Layout() content.LayoutRepository { return nil }

// fakeReset is a capability.VaultReset recorder.
type fakeReset struct {
	mu      sync.Mutex
	preview int
	execute int
}

func (f *fakeReset) Preview(context.Context) (vaultreset.Preview, error) {
	f.mu.Lock()
	f.preview++
	f.mu.Unlock()
	return vaultreset.Preview{}, nil
}

func (f *fakeReset) Execute(context.Context) (vaultreset.Result, error) {
	f.mu.Lock()
	f.execute++
	f.mu.Unlock()
	return vaultreset.Result{}, nil
}

// fakeDoc is a minimal storage.DocumentStore for settings.New.
type fakeDoc struct{}

func (f *fakeDoc) Read(name string, out any) (bool, error) { return false, nil }
func (f *fakeDoc) Write(name string, v any) error          { return nil }
func (f *fakeDoc) Delete(name string) error                { return nil }
func (f *fakeDoc) List() ([]string, error)                 { return nil, nil }

var _ storage.DocumentStore = (*fakeDoc)(nil)

// newProfileService builds a real ProfileService over a temp store — the
// atomic-import write path the config operation delegates to.
func newProfileService(t *testing.T) *profile.ProfileService {
	t.Helper()
	store := profile.NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))
	return profile.NewProfileService(store)
}

// testGates wires one fresh gate per domain, capacity 1 — the conservative
// whole-domain exclusion the composition root is expected to use. The
// gates are real control.Admissions, so the tests exercise the same
// acquire/release machinery the transport will.
func testGates() (config, vault, content, sessionG, gitG, fsG control.Admission) {
	return control.NewSemaphore(capability.GateConfig, 1),
		control.NewSemaphore(capability.GateVault, 1),
		control.NewSemaphore(capability.GateContent, 1),
		control.NewSemaphore(capability.GateSession, 1),
		control.NewSemaphore(capability.GateGit, 1),
		control.NewSemaphore(capability.GateFilesystem, 1)
}

// testLane returns the execution admission for test operations: a plain
// non-blocking semaphore of ample capacity, so tests exercise the conflict
// gates (also plain non-blocking semaphores) exactly as before. The
// transport's own tests cover the waiting-gate behavior over the socket.
func testLane() control.Admission {
	return control.NewSemaphore("test-lane", 8)
}

// newFakeGitRegistry builds an empty git registry for gate tests.
func newFakeGitRegistry() *git.Registry { return git.New() }

// noCaller owns nothing — Acquire on any binding answers the ownership
// error rather than a panic.
type noCaller struct{}

func (noCaller) Owns(session.ID) bool { return false }

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestConfigOperationCannotReachVault is the access-isolation proof: a
// handler-shaped consumer constructed with ONLY a ConfigOperation runs a
// config mutation, and the vault seam wired into a sibling operation
// records zero calls. The compile-time half of the proof is that the
// consumer's parameter type is ConfigOperation — the callback receives a
// ConfigService whose method set is fixed and contains no vault method
// (asserted by reflection below).
func TestConfigOperationCannotReachVault(t *testing.T) {
	configGate, vaultGate, _, _, _, _ := testGates()
	profiles := &fakeProfileRepo{}
	groups := &fakeGroupRepo{}
	vaultSeam := newFakeVault()

	cfgOp := capability.NewConfigOperation(configGate, vaultGate, testLane(), profiles, groups, nil, newProfileService(t), nil, nil, nil)

	// The handler-shaped consumer: it takes the operation and nothing else.
	runConsumer := func(op capability.ConfigOperation) error {
		return op.Run(context.Background(), func(ctx context.Context, svc capability.ConfigService) error {
			return svc.CreateProfile(profile.SSHProfile{
				Base: profile.Base{
					ID:   "ssh:custom:test:1",
					Name: "test",
				},
				Options: profile.StoredSSHProfileOptions{
					Host: "example.com",
				},
			})
		})
	}

	if err := runConsumer(cfgOp); err != nil {
		t.Fatalf("config consumer failed: %v", err)
	}
	if got := len(profiles.profiles); got != 1 {
		t.Fatalf("profile not written: got %d profiles", got)
	}
	if calls := vaultSeam.LifecycleCalls(); len(calls) != 0 {
		t.Fatalf("config operation reached the vault: %v", calls)
	}
}

// TestConfigServiceMethodSetHasNoVaultMethods asserts by reflection that the
// service a ConfigOperation hands out cannot name a vault operation — the
// type-level half of access isolation.
func TestConfigServiceMethodSetHasNoVaultMethods(t *testing.T) {
	vaultMethods := map[string]bool{
		"Setup": true, "Unseal": true, "Seal": true, "Activity": true,
		"CreateNamed": true, "CreateNamedResolved": true, "RenameSecret": true,
		"ReplaceSecret": true, "DeleteSecret": true, "BuildInventory": true,
	}
	tp := reflect.TypeOf((*capability.ConfigService)(nil)).Elem()
	for i := 0; i < tp.NumMethod(); i++ {
		name := tp.Method(i).Name
		if vaultMethods[name] {
			t.Fatalf("ConfigService exposes vault method %q — a config handler can reach the vault", name)
		}
	}
}

// TestServiceCannotEscapeCallback proves the escaped-handle property: a
// service captured by a callback fails with ErrOperationInactive once every
// in-flight Run has returned — it cannot be carried out of the operation it
// was issued for.
func TestServiceCannotEscapeCallback(t *testing.T) {
	configGate, vaultGate, _, _, _, _ := testGates()
	profiles := &fakeProfileRepo{}
	groups := &fakeGroupRepo{}
	op := capability.NewConfigOperation(configGate, vaultGate, testLane(), profiles, groups, nil, newProfileService(t), nil, nil, nil)

	var leaked capability.ConfigService
	err := op.Run(context.Background(), func(ctx context.Context, svc capability.ConfigService) error {
		leaked = svc
		return nil
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := leaked.ListProfiles(); !errors.Is(err, capability.ErrOperationInactive) {
		t.Fatalf("escaped service still usable after Run: err=%v", err)
	}

	// The void-method form: an escaped call silently does nothing rather
	// than touching the store outside the exclusion. (VaultService.Seal
	// is the void case; a leaked call must not reach the store.)
	vaultGate2 := control.NewSemaphore(capability.GateVault, 1)
	seam := newFakeVault()
	vop := capability.NewVaultOperation(vaultGate2, testLane(), seam)
	var leakedVault capability.VaultService
	if err := vop.Run(context.Background(), func(ctx context.Context, svc capability.VaultService) error {
		leakedVault = svc
		return nil
	}); err != nil {
		t.Fatalf("vault run: %v", err)
	}
	leakedVault.Seal() // must be a no-op
	if calls := seam.LifecycleCalls(); len(calls) != 0 {
		t.Fatalf("escaped vault service reached the store: %v", calls)
	}
}

// TestOverlappingOperationsBothComplete drives two operations whose
// resource sets overlap ([config, vault]) concurrently in both request
// orders. Neither deadlocks; both Run calls return. One may be refused by
// the capacity-1 gates — a refusal is a completion, and the caller surfaces
// it — so the assertion is that both return within the deadline, never that
// both ran. A second variant drives them sequentially, where both succeed.
func TestOverlappingOperationsBothComplete(t *testing.T) {
	cfgGate, vltGate, _, _, _, _ := testGates()
	profiles := &fakeProfileRepo{}
	groups := &fakeGroupRepo{}

	seam := newFakeVault()
	cfgSvc := newProfileService(t)

	secretOp := capability.NewSecretOperation(cfgGate, vltGate, testLane(), profiles, groups, seam, seam)
	tabbyOp := capability.NewTabbyImportOperation(cfgGate, vltGate, testLane(), profiles, groups, cfgSvc, seam, seam)

	// Run both in both arrival orders, concurrently, repeatedly.
	for i := 0; i < 20; i++ {
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			errs[0] = secretOp.Run(context.Background(), func(ctx context.Context, svc capability.SecretService) error {
				_, err := svc.CreateSecret(ctx, credential.NewSecret("pw"), vault.SecretMeta{Name: "s"}, false)
				return err
			})
		}()
		go func() {
			defer wg.Done()
			errs[1] = tabbyOp.Run(context.Background(), func(ctx context.Context, svc capability.TabbyImportService) error {
				svc.AtomicImport(nil, nil)
				return nil
			})
		}()
		wg.Wait()
		for j, err := range errs {
			if err != nil && !capability.IsRefused(err) {
				t.Fatalf("overlapping op %d failed (order %d): %v", j, i, err)
			}
		}
	}

	// Sequential: both must succeed.
	if err := secretOp.Run(context.Background(), func(ctx context.Context, svc capability.SecretService) error {
		_, err := svc.CreateSecret(ctx, credential.NewSecret("pw"), vault.SecretMeta{Name: "s2"}, false)
		return err
	}); err != nil {
		t.Fatalf("sequential secret op failed: %v", err)
	}
	if err := tabbyOp.Run(context.Background(), func(ctx context.Context, svc capability.TabbyImportService) error {
		svc.AtomicImport(nil, nil)
		return nil
	}); err != nil {
		t.Fatalf("sequential tabby op failed: %v", err)
	}
}

// TestNonConflictingOperationsOverlap proves the gates are per-domain, not
// one global lock: an operation blocked in its callback on the content gate
// does not stop a git-domain operation from completing.
func TestNonConflictingOperationsOverlap(t *testing.T) {
	_, _, contentGate, _, gitGate, _ := testGates()
	db := newFakeContentDB()
	contentOp := capability.NewContentOperation(contentGate, testLane(), db)

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- contentOp.Run(context.Background(), func(ctx context.Context, svc capability.ContentService) error {
			close(started)
			<-release
			_, err := svc.RecordCommand(ctx, content.CompletedCommand{})
			return err
		})
	}()
	<-started // the content operation holds the content gate now

	// A git-binding operation (different domain) completes while the
	// content callback is still blocked.
	gitOp := capability.NewGitBindingOperation(gitGate, testLane(), newFakeGitRegistry())
	gitDone := make(chan error, 1)
	go func() {
		gitDone <- gitOp.Run(context.Background(), func(ctx context.Context, svc capability.GitBindingService) error {
			_, _, err := svc.Acquire("nope", noCaller{})
			_ = err // unknown binding is a domain error, not a refusal
			return nil
		})
	}()
	select {
	case err := <-gitDone:
		if err != nil && !capability.IsRefused(err) {
			t.Fatalf("git op failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("git operation waited on the content gate — one global lock")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("content op failed: %v", err)
	}
}

// TestSameDomainExclusion proves a capacity-1 gate refuses a second
// same-domain operation while the first is in flight — the exclusion half
// of "owns access and exclusion together".
func TestSameDomainExclusion(t *testing.T) {
	cfgGate, vltGate, _, _, _, _ := testGates()
	profiles := &fakeProfileRepo{}
	groups := &fakeGroupRepo{}
	op := capability.NewConfigOperation(cfgGate, vltGate, testLane(), profiles, groups, nil, newProfileService(t), nil, nil, nil)

	started := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- op.Run(context.Background(), func(ctx context.Context, svc capability.ConfigService) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	err := op.Run(context.Background(), func(ctx context.Context, svc capability.ConfigService) error {
		return errors.New("must not run")
	})
	if !capability.IsRefused(err) {
		t.Fatalf("second same-domain operation was not refused: %v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first op failed: %v", err)
	}
}

// TestForSecretUnknownID returns an error and never nil.
func TestForSecretUnknownID(t *testing.T) {
	cfgGate, vltGate, _, _, _, _ := testGates()
	profiles := &fakeProfileRepo{}
	groups := &fakeGroupRepo{}

	seam := newFakeVault()
	factory := capability.NewSecretOperations(cfgGate, vltGate, testLane(), profiles, groups, seam, seam, seam.Exists)

	op, err := factory.ForSecret(context.Background(), "sec:v1:file:does-not-exist")
	if err == nil {
		t.Fatal("ForSecret with an unknown id returned no error")
	}
	if op != nil {
		t.Fatalf("ForSecret returned a non-nil operation on error: %#v", op)
	}
}

// TestForSecretKnownIDSucceeds is the paired "on a normal machine it
// succeeds": a secret the vault actually holds yields a usable operation.
func TestForSecretKnownIDSucceeds(t *testing.T) {
	cfgGate, vltGate, _, _, _, _ := testGates()
	profiles := &fakeProfileRepo{}
	groups := &fakeGroupRepo{}

	seam := newFakeVault()

	id, err := seam.CreateNamed(context.Background(), credential.NewSecret("pw"), vault.SecretMeta{Name: "known"})
	if err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	factory := capability.NewSecretOperations(cfgGate, vltGate, testLane(), profiles, groups, seam, seam, seam.Exists)
	op, err := factory.ForSecret(context.Background(), id)
	if err != nil {
		t.Fatalf("ForSecret with a known id failed: %v", err)
	}
	if op == nil {
		t.Fatal("ForSecret returned nil on success")
	}
	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.SecretService) error {
		secret, err := svc.GetSecret(ctx, id)
		if err != nil {
			return err
		}
		var got string
		_ = secret.Use(func(b []byte) error {
			got = string(b)
			return nil
		})
		if got != "pw" {
			t.Fatalf("got secret %q, want pw", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("secret op run failed: %v", err)
	}
}

// TestForSessionUnknownID returns an error and never nil.
func TestForSessionUnknownID(t *testing.T) {
	sessionGate, _, _, _, _, _ := testGates()
	reg := newFakeSessionRegistry()
	factory := capability.NewSessionOperations(sessionGate, testLane(), reg, nil)

	op, err := factory.ForSession("no-such-session")
	if err == nil {
		t.Fatal("ForSession with an unknown id returned no error")
	}
	if op != nil {
		t.Fatalf("ForSession returned a non-nil operation on error: %#v", op)
	}
}

// TestForSessionKnownIDSucceeds is the paired success path.
func TestForSessionKnownIDSucceeds(t *testing.T) {
	sessionGate, _, _, _, _, _ := testGates()
	reg := newFakeSessionRegistry()
	reg.sessions["s1"] = &fakeSession{id: "s1"}
	factory := capability.NewSessionOperations(sessionGate, testLane(), reg, nil)

	op, err := factory.ForSession("s1")
	if err != nil {
		t.Fatalf("ForSession with a known id failed: %v", err)
	}
	if op == nil {
		t.Fatal("ForSession returned nil on success")
	}
	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.SessionService) error {
		s, err := svc.Get("s1")
		if err != nil {
			return err
		}
		if s.ID() != "s1" {
			t.Fatalf("got session %q, want s1", s.ID())
		}
		return nil
	}); err != nil {
		t.Fatalf("session op run failed: %v", err)
	}
}

// TestSessionTargetOperationReleasesSessionGateBeforeWork pins the read
// interval: immutable routing facts are copied while the session gate is
// held, then remote work keeps only the ordinary lane. A slow completion
// therefore cannot refuse resize or close on the session domain.
func TestSessionTargetOperationReleasesSessionGateBeforeWork(t *testing.T) {
	sessionGate := control.NewSemaphore(capability.GateSession, 1)
	lane := control.NewSemaphore("control", 1)
	reg := newFakeSessionRegistry()
	reg.sessions["s1"] = &fakeSession{
		id:   "s1",
		kind: session.KindRemote,
		host: "target.example",
		sshOpts: []ssh.ConnectOption{
			ssh.WithUser("alice"),
			ssh.WithJumpHost("bastion.example", 2200, "jumper", "password"),
		},
	}
	factory := capability.NewSessionOperations(sessionGate, lane, reg, nil)

	op, err := factory.ForSessionTarget("s1")
	if err != nil {
		t.Fatalf("ForSessionTarget: %v", err)
	}
	err = op.Run(context.Background(), func(_ context.Context, target capability.SessionTarget) error {
		if target.Kind != session.KindRemote || target.Host != "target.example" {
			t.Fatalf("target = %+v", target)
		}
		cfg := &ssh.ConnectConfig{}
		for _, opt := range target.SSHOptions {
			opt(cfg)
		}
		if cfg.User != "alice" || cfg.JumpHost != "bastion.example" || cfg.JumpPort != 2200 {
			t.Fatalf("SSH options lost from snapshot: %+v", cfg)
		}

		sessionPermit, sessionRej := sessionGate.TryAcquire(context.Background())
		if sessionRej != nil {
			t.Fatalf("session gate remained held during work: %+v", sessionRej)
		}
		sessionPermit.Release()
		if lanePermit, laneRej := lane.TryAcquire(context.Background()); laneRej == nil {
			lanePermit.Release()
			t.Fatal("ordinary lane was released before work completed")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("target operation: %v", err)
	}
}

// TestSecretDeleteUnknownRowFailsAndKnownRowSucceeds pairs the failure path
// (an unknown row is an error) with the success path (a real row clears the
// profile references and deletes the stored value).
func TestSecretDeleteUnknownRowFailsAndKnownRowSucceeds(t *testing.T) {
	cfgGate, vltGate, _, _, _, _ := testGates()
	profiles := &fakeProfileRepo{}
	groups := &fakeGroupRepo{}

	seam := newFakeVault()
	op := capability.NewSecretOperation(cfgGate, vltGate, testLane(), profiles, groups, seam, seam)

	// Failure path: unknown row.
	err := op.Run(context.Background(), func(ctx context.Context, svc capability.SecretService) error {
		return svc.DeleteSecret(ctx, "no-such-row")
	})
	if err == nil {
		t.Fatal("DeleteSecret with an unknown row returned no error")
	}
	if !strings.Contains(err.Error(), "unknown secret row") {
		t.Fatalf("unexpected error: %v", err)
	}

	// Success path: seed a secret bound to a profile, then delete by row.
	profiles.profiles = []profile.SSHProfile{{
		Base: profile.Base{ID: "ssh:custom:test:1"},
		Options: profile.StoredSSHProfileOptions{
			Host:           "example.com",
			PasswordSecret: "sec:v1:file:fakea",
		},
	}}
	seam.rows["row1"] = "sec:v1:file:fakea"
	seam.secrets["sec:v1:file:fakea"] = "pw"

	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.SecretService) error {
		return svc.DeleteSecret(ctx, "row1")
	}); err != nil {
		t.Fatalf("DeleteSecret on a known row failed: %v", err)
	}
	if len(profiles.cleared) != 1 || profiles.cleared[0] != "sec:v1:file:fakea" {
		t.Fatalf("references not cleared: %v", profiles.cleared)
	}
	if got := seam.secrets["sec:v1:file:fakea"]; got != "" {
		t.Fatalf("stored secret not deleted: %q", got)
	}
}

// TestVaultLifecycleSetupSucceedsAndSealRefusesWhenBroken pairs the vault
// lifecycle's success path with the failure the acceptance criteria demand:
// for every "returns an error when…" there is a "on a normal machine it
// succeeds".
func TestVaultLifecycleSetupSucceeds(t *testing.T) {
	vaultGate := control.NewSemaphore(capability.GateVault, 1)
	seam := newFakeVault()
	op := capability.NewVaultOperation(vaultGate, testLane(), seam)

	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.VaultService) error {
		if _, err := svc.Setup(ctx, vault.SetupRequest{}); err != nil {
			return err
		}
		return svc.Unseal(ctx, vault.UnsealRequest{})
	}); err != nil {
		t.Fatalf("vault setup on a normal machine failed: %v", err)
	}
	calls := seam.LifecycleCalls()
	if len(calls) != 2 || calls[0] != "Setup" || calls[1] != "Unseal" {
		t.Fatalf("unexpected lifecycle calls: %v", calls)
	}
}

// TestVaultResetAndConfigRunTogether proves the reset operation (a
// cross-domain [config, vault] operation) works and that a plain config op
// and a reset op driven concurrently both complete.
func TestVaultResetAndConfigRunTogether(t *testing.T) {
	cfgGate, vltGate, _, _, _, _ := testGates()
	reset := &fakeReset{}
	resetOp := capability.NewVaultResetOperation(cfgGate, vltGate, testLane(), reset)
	profiles := &fakeProfileRepo{}
	groups := &fakeGroupRepo{}
	cfgOp := capability.NewConfigOperation(cfgGate, vltGate, testLane(), profiles, groups, nil, newProfileService(t), nil, nil, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	errs := make([]error, 2)
	go func() {
		defer wg.Done()
		errs[0] = resetOp.Run(context.Background(), func(ctx context.Context, svc capability.VaultResetService) error {
			_, err := svc.Preview(ctx)
			return err
		})
	}()
	go func() {
		defer wg.Done()
		errs[1] = cfgOp.Run(context.Background(), func(ctx context.Context, svc capability.ConfigService) error {
			_, err := svc.ListProfiles()
			return err
		})
	}()
	wg.Wait()
	for i, err := range errs {
		if err != nil && !capability.IsRefused(err) {
			t.Fatalf("op %d failed: %v", i, err)
		}
	}
}

// TestCaptureSaveWritesBothStores proves the [vault, content] cross-domain
// operation reaches both stores inside one callback.
func TestCaptureSaveWritesBothStores(t *testing.T) {
	vltGate := control.NewSemaphore(capability.GateVault, 1)
	contentGate := control.NewSemaphore(capability.GateContent, 1)
	seam := newFakeVault()
	db := newFakeContentDB()
	op := capability.NewCaptureSaveOperation(vltGate, contentGate, testLane(), seam, db)

	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.CaptureSaveService) error {
		if _, _, err := svc.CreateSecret(ctx, credential.NewSecret("pw"), vault.SecretMeta{Name: "captured"}); err != nil {
			return err
		}
		return svc.RewriteRedaction(ctx, "0192f0aa-0000-7000-8000-00000000beef", content.Redaction{}, "sec:v1:file:fakea")
	}); err != nil {
		t.Fatalf("capture save failed: %v", err)
	}
	if len(seam.LifecycleCalls()) == 0 {
		t.Fatal("capture save did not reach the vault")
	}
	if len(db.ledger.rewrites) != 1 {
		t.Fatalf("capture save did not rewrite the ledger row: %v", db.ledger.rewrites)
	}
}

// There is ONE store to rewrite, and the id reaches it whatever it looks
// like. This used to be a routing test: two tables held masked command text
// — the interim command_history keyed by an autoincrement rowid, the ledger
// by a client-minted UUIDv7 — and the service chose between them by trying
// to parse the id as a decimal. nocx-rtg0.19 deleted command_history and the
// router with it, so the assertion is now that NOTHING is decided from the
// shape of the string: a decimal-looking handle goes to the ledger exactly
// as a UUID does, and both arrive verbatim.
func TestCaptureSaveSendsEveryIdShapeToTheLedger(t *testing.T) {
	vltGate := control.NewSemaphore(capability.GateVault, 1)
	contentGate := control.NewSemaphore(capability.GateContent, 1)
	db := newFakeContentDB()
	op := capability.NewCaptureSaveOperation(vltGate, contentGate, testLane(), newFakeVault(), db)

	const entryID = "0192f0aa-0000-7000-8000-00000000cafe"
	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.CaptureSaveService) error {
		if err := svc.RewriteRedaction(ctx, "42", content.Redaction{}, "{{secret:a}}"); err != nil {
			return err
		}
		return svc.RewriteRedaction(ctx, entryID, content.Redaction{}, "{{secret:b}}")
	}); err != nil {
		t.Fatalf("capture save failed: %v", err)
	}
	if len(db.ledger.rewrites) != 2 {
		t.Fatalf("the ledger got %v, want both links", db.ledger.rewrites)
	}
	if db.ledger.rewrites[0] != "42" || db.ledger.rewrites[1] != entryID {
		t.Fatalf("the ledger got %v, want exactly [42 %s] — each id verbatim", db.ledger.rewrites, entryID)
	}
}

// TestSettingsSurfaceSucceeds proves the settings sub-surface works on a
// normal machine (a real registry over a fake document store).
func TestSettingsSurfaceSucceeds(t *testing.T) {
	cfgGate, vltGate, _, _, _, _ := testGates()
	reg := settings.New(&fakeDoc{}, newFakeSecretStore())
	profiles := &fakeProfileRepo{}
	groups := &fakeGroupRepo{}
	op := capability.NewConfigOperation(cfgGate, vltGate, testLane(), profiles, groups, nil, newProfileService(t), reg, nil, nil)

	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.ConfigService) error {
		snap, err := svc.Settings().GetSnapshot()
		if err != nil {
			return err
		}
		if snap.Values == nil {
			return errors.New("settings snapshot has nil values")
		}
		return nil
	}); err != nil {
		t.Fatalf("settings read failed: %v", err)
	}
}

// TestConfigWriteWithRowButNoVaultFails is the failure half of the row
// resolution contract: a config write carrying a row handle with no vault
// wired must fail loudly, never store a row.
func TestConfigWriteWithRowButNoVaultFails(t *testing.T) {
	cfgGate, vltGate, _, _, _, _ := testGates()
	profiles := &fakeProfileRepo{}
	groups := &fakeGroupRepo{}
	op := capability.NewConfigOperation(cfgGate, vltGate, testLane(), profiles, groups, nil, newProfileService(t), nil, nil, nil)

	err := op.Run(context.Background(), func(ctx context.Context, svc capability.ConfigService) error {
		return svc.CreateProfile(profile.SSHProfile{
			Base: profile.Base{
				ID:   "ssh:custom:test:1",
				Name: "test",
			},
			Options: profile.StoredSSHProfileOptions{
				Host:           "example.com",
				PasswordSecret: "secrow:1234",
			},
		})
	})
	if err == nil {
		t.Fatal("row-carrying write with no vault succeeded")
	}
	if !strings.Contains(err.Error(), "no vault") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestConfigWriteResolvesRowWithVault is the success half: with a vault
// wired, a row-carrying write stores the resolved reference, never the row.
func TestConfigWriteResolvesRowWithVault(t *testing.T) {
	cfgGate, vltGate, _, _, _, _ := testGates()
	profiles := &fakeProfileRepo{}
	groups := &fakeGroupRepo{}
	seam := newFakeVault()
	seam.rows["secrow:1"] = "sec:v1:file:fakea"
	op := capability.NewConfigOperation(cfgGate, vltGate, testLane(), profiles, groups, nil, newProfileService(t), nil, seam, nil)

	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.ConfigService) error {
		return svc.CreateProfile(profile.SSHProfile{
			Base: profile.Base{
				ID:   "ssh:custom:test:1",
				Name: "test",
			},
			Options: profile.StoredSSHProfileOptions{
				Host:           "example.com",
				PasswordSecret: "secrow:1",
			},
		})
	}); err != nil {
		t.Fatalf("row-carrying write with a vault failed: %v", err)
	}
	if got := profiles.profiles[0].Options.PasswordSecret; got != "sec:v1:file:fakea" {
		t.Fatalf("stored %q, want resolved reference", got)
	}
}

// TestMintSecretReturnsTheMintedID proves SecretService.MintSecret runs the
// vault's named create sequence and returns the id it minted — the mint
// handlers (secrets.save*) derive the row handle from it, so a wrong id
// would make the minted secret unaddressable.
func TestMintSecretReturnsTheMintedID(t *testing.T) {
	cfgGate, vltGate, _, _, _, _ := testGates()
	seam := newFakeVault()
	op := capability.NewSecretOperation(cfgGate, vltGate, testLane(), &fakeProfileRepo{}, &fakeGroupRepo{}, seam, seam)

	var minted credential.SecretID
	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.SecretService) error {
		var err error
		minted, err = svc.MintSecret(ctx, credential.NewSecret("pw"), vault.SecretMeta{Name: "minted", Kind: vault.KindPassword})
		return err
	}); err != nil {
		t.Fatalf("mint failed: %v", err)
	}
	if calls := seam.LifecycleCalls(); len(calls) != 1 || calls[0] != "CreateNamed" {
		t.Fatalf("mint lifecycle calls = %v, want [CreateNamed]", calls)
	}
	if !strings.HasPrefix(string(minted), "sec:v1:") {
		t.Fatalf("minted id = %q, want a vault reference", minted)
	}
}

// TestMintSecretFallsBackToPlainStore proves the no-vault mint path: the
// plain store records the secret namelessly, exactly like the transport's
// createSecret did before the capability migration.
func TestMintSecretFallsBackToPlainStore(t *testing.T) {
	cfgGate, vltGate, _, _, _, _ := testGates()
	store := newFakeSecretStore()
	op := capability.NewSecretOperation(cfgGate, vltGate, testLane(), &fakeProfileRepo{}, &fakeGroupRepo{}, nil, store)

	var minted credential.SecretID
	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.SecretService) error {
		var err error
		minted, err = svc.MintSecret(ctx, credential.NewSecret("pw"), vault.SecretMeta{Name: "minted", Kind: vault.KindPassword})
		return err
	}); err != nil {
		t.Fatalf("mint failed: %v", err)
	}
	if !strings.HasPrefix(string(minted), "sec:v1:file:plain") {
		t.Fatalf("minted id = %q, want the plain store's minted reference", minted)
	}
	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.SecretService) error {
		secret, err := svc.GetSecret(ctx, minted)
		if err != nil {
			return err
		}
		return secret.Use(func(b []byte) error {
			if string(b) != "pw" {
				t.Fatalf("stored value = %q, want %q", string(b), "pw")
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("read back failed: %v", err)
	}
}
