package sandbox

import (
	"context"
	"errors"
	"math"
	"sync"

	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/workspace"
)

const (
	StandardDocumentName = "sandbox.json"
	DefaultWorkspaceID   = string(workspace.Default)
	MaxWorkspaceProfiles = 4096
)

var (
	ErrProfileConflict             = errors.New("sandbox: profile revision conflict")
	ErrProfileRevisionExhausted    = errors.New("sandbox: profile revision exhausted")
	ErrWorkspaceProfileUnsupported = errors.New("sandbox: workspace profile cannot be changed")
	ErrProfileRestoreBusy          = errors.New("sandbox: profile restore is active")
	ErrProfileRestoreScopeClosed   = errors.New("sandbox: profile restore scope is closed")
)

// ProfileError contains a bounded machine-readable refusal without path data.
type ProfileError struct {
	Code  string `json:"code"`
	Field string `json:"field"`
	Index int    `json:"index"`
	cause error  `json:"-"`
}

func (e *ProfileError) Error() string { return "sandbox: profile operation failed (" + e.Code + ")" }
func (e *ProfileError) Unwrap() error { return e.cause }

// ConfigurationSnapshot is the mutable profile subset safe for backup/export.
type ConfigurationSnapshot struct {
	Standard   StandardDocument   `json:"standard"`
	Workspaces []WorkspaceProfile `json:"workspaces"`
}

// ConfigLock serializes profile CAS and restore writes for one application.
type ConfigLock struct {
	mu      sync.Mutex
	restore *restoreScope
}

type restoreScope struct{ active bool }

func (l *ConfigLock) with(fn func() error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return fn()
}

func (l *ConfigLock) withWrite(fn func() error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.restore != nil {
		return ErrProfileRestoreBusy
	}
	return fn()
}

// WorkspaceProfileStore is the narrow ContentDB seam for sparse workspace
// defaults. It resolves/stores profiles but never chooses pane membership.
type WorkspaceProfileStore interface {
	// GetWorkspaceProfile reads one named workspace's sparse profile clock/value.
	GetWorkspaceProfile(context.Context, string) (WorkspaceProfile, error)
	// UpdateWorkspaceProfile CASes an override; nil resets to inheritance.
	UpdateWorkspaceProfile(context.Context, string, uint64, *ProfileRoots) (WorkspaceProfile, error)
	// ListWorkspaceProfiles returns all named workspace clocks and sparse values.
	ListWorkspaceProfiles(context.Context) ([]WorkspaceProfile, error)
	// RestoreWorkspaceProfiles atomically restores known workspace values with local clocks.
	RestoreWorkspaceProfiles(context.Context, []WorkspaceProfile) error
}

// ProfileRepository owns standard configuration CAS and serializes it with
// workspace profile operations through one application-instance lock.
type ProfileRepository struct {
	doc        storage.DocumentStore
	name       string
	workspaces WorkspaceProfileStore
	lock       *ConfigLock
}

// NewProfileRepository binds the typed document to the one layout-owned store.
func NewProfileRepository(doc storage.DocumentStore, name string, workspaces WorkspaceProfileStore) *ProfileRepository {
	return &ProfileRepository{doc: doc, name: name, workspaces: workspaces, lock: &ConfigLock{}}
}

// NewStandardProfileRepository creates a standard-only repository without ContentDB.
func NewStandardProfileRepository(doc storage.DocumentStore) *ProfileRepository {
	return NewProfileRepository(doc, StandardDocumentName, nil)
}

// GetStandard returns an independent snapshot. A missing document has the
// canonical initial value; it is not persisted until a successful CAS write.
func (r *ProfileRepository) GetStandard() (StandardDocument, error) {
	r.lock.mu.Lock()
	defer r.lock.mu.Unlock()
	return r.getStandardUnlocked()
}

func (r *ProfileRepository) getStandardUnlocked() (StandardDocument, error) {
	var out StandardDocument
	found, err := r.doc.Read(r.name, &out)
	if err != nil {
		return StandardDocument{}, &ProfileError{Code: "document_read_failed", Field: "standard", cause: err}
	}
	if !found {
		return initialStandard(), nil
	}
	if err := validateStandard(out); err != nil {
		return StandardDocument{}, err
	}
	return cloneStandard(out), nil
}

// UpdateStandard atomically changes enabled and both root classes under one
// revision check. Every successful update advances the durable CAS clock.
func (r *ProfileRepository) UpdateStandard(expected uint64, enabled bool, roots ProfileRoots) (StandardDocument, error) {
	if err := validateRoots(roots); err != nil {
		return StandardDocument{}, err
	}
	roots = cloneRoots(roots)
	var updated StandardDocument
	err := r.lock.withWrite(func() error {
		current, err := r.getStandardUnlocked()
		if err != nil {
			return err
		}
		if current.Revision != expected {
			return ErrProfileConflict
		}
		if current.Revision == math.MaxUint64 {
			return ErrProfileRevisionExhausted
		}
		updated = StandardDocument{SchemaVersion: ProfileSchemaVersion, Revision: current.Revision + 1, Enabled: enabled, ProfileRoots: roots}
		if err := r.doc.Write(r.name, updated); err != nil {
			return &ProfileError{Code: "document_write_failed", Field: "standard", cause: err}
		}
		return nil
	})
	if err != nil {
		return StandardDocument{}, err
	}
	return cloneStandard(updated), nil
}

// WithStandardRevision holds the shared configuration mutex across revision
// validation and the caller's short durable CAS operation.
func (r *ProfileRepository) WithStandardRevision(expected uint64, fn func() error) error {
	r.lock.mu.Lock()
	defer r.lock.mu.Unlock()
	if r.lock.restore != nil {
		return ErrProfileRestoreBusy
	}
	current, err := r.getStandardUnlocked()
	if err != nil {
		return err
	}
	if current.Revision != expected {
		return ErrProfileConflict
	}
	return fn()
}

// GetWorkspaceProfile reads a named workspace profile, or default inheritance.
func (r *ProfileRepository) GetWorkspaceProfile(ctx context.Context, workspaceID string) (WorkspaceProfile, error) {
	if r.workspaces == nil {
		return WorkspaceProfile{}, ErrWorkspaceProfileUnsupported
	}
	r.lock.mu.Lock()
	defer r.lock.mu.Unlock()
	return r.workspaces.GetWorkspaceProfile(ctx, workspaceID)
}

// UpdateWorkspaceProfile CASes a complete override; nil resets to standard inheritance.
func (r *ProfileRepository) UpdateWorkspaceProfile(ctx context.Context, workspaceID string, expected uint64, roots *ProfileRoots) (WorkspaceProfile, error) {
	if workspaceID == DefaultWorkspaceID {
		return WorkspaceProfile{}, ErrWorkspaceProfileUnsupported
	}
	var copyRoots *ProfileRoots
	if roots != nil {
		if err := validateRoots(*roots); err != nil {
			return WorkspaceProfile{}, err
		}
		c := cloneRoots(*roots)
		copyRoots = &c
	}
	var result WorkspaceProfile
	err := r.lock.withWrite(func() error {
		if r.workspaces == nil {
			return ErrWorkspaceProfileUnsupported
		}
		var err error
		result, err = r.workspaces.UpdateWorkspaceProfile(ctx, workspaceID, expected, copyRoots)
		return err
	})
	if err != nil {
		return WorkspaceProfile{}, err
	}
	return cloneWorkspaceProfile(result), nil
}

// ExportConfiguration snapshots only mutable standard and workspace defaults.
func (r *ProfileRepository) ExportConfiguration() (ConfigurationSnapshot, error) {
	var out ConfigurationSnapshot
	err := r.lock.with(func() error {
		standard, err := r.getStandardUnlocked()
		if err != nil {
			return err
		}
		out.Standard = cloneStandard(standard)
		if r.workspaces != nil {
			out.Workspaces, err = r.workspaces.ListWorkspaceProfiles(context.Background())
			if err != nil {
				return err
			}
			if len(out.Workspaces) > MaxWorkspaceProfiles {
				return &ProfileError{Code: "too_many_workspaces", Field: "workspaces", Index: MaxWorkspaceProfiles}
			}
			out.Workspaces = cloneWorkspaceProfiles(out.Workspaces)
		}
		return ValidateConfigurationSnapshot(out)
	})
	if err != nil {
		return ConfigurationSnapshot{}, err
	}
	return out, nil
}

// ConfigurationRestorer is the short-write capability supplied to one active
// backup/restore operation.
type ConfigurationRestorer interface {
	RestoreConfiguration(ConfigurationSnapshot) error
}

type scopedConfigurationRestorer struct {
	repo  *ProfileRepository
	scope *restoreScope
}

// WithConfigurationRestore blocks profile mutations while the surrounding
// backup journal performs IO, but holds the config mutex only for admissions
// and individual short profile writes.
func (r *ProfileRepository) WithConfigurationRestore(fn func(ConfigurationRestorer) error) error {
	scope := &restoreScope{active: true}
	r.lock.mu.Lock()
	if r.lock.restore != nil {
		r.lock.mu.Unlock()
		return ErrProfileRestoreBusy
	}
	r.lock.restore = scope
	r.lock.mu.Unlock()
	defer func() {
		r.lock.mu.Lock()
		scope.active = false
		if r.lock.restore == scope {
			r.lock.restore = nil
		}
		r.lock.mu.Unlock()
	}()
	return fn(&scopedConfigurationRestorer{repo: r, scope: scope})
}

func (s *scopedConfigurationRestorer) RestoreConfiguration(in ConfigurationSnapshot) error {
	lock := s.repo.lock
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.restore != s.scope || !s.scope.active {
		return ErrProfileRestoreScopeClosed
	}
	if err := ValidateConfigurationSnapshot(in); err != nil {
		return err
	}
	in.Standard = cloneStandard(in.Standard)
	in.Workspaces = cloneWorkspaceProfiles(in.Workspaces)
	return s.repo.restoreConfigurationLocked(in)
}

func (r *ProfileRepository) restoreConfigurationLocked(in ConfigurationSnapshot) error {
	current, err := r.getStandardUnlocked()
	if err != nil {
		return err
	}
	if current.Revision == math.MaxUint64 {
		return ErrProfileRevisionExhausted
	}
	next := StandardDocument{SchemaVersion: ProfileSchemaVersion, Revision: current.Revision + 1, Enabled: in.Standard.Enabled, ProfileRoots: cloneRoots(in.Standard.ProfileRoots)}
	if r.workspaces == nil && len(in.Workspaces) > 0 {
		return ErrWorkspaceProfileUnsupported
	}
	if r.workspaces != nil {
		if err := r.workspaces.RestoreWorkspaceProfiles(context.Background(), in.Workspaces); err != nil {
			return err
		}
	}
	if err := r.doc.Write(r.name, next); err != nil {
		return &ProfileError{Code: "document_write_failed", Field: "standard", cause: err}
	}
	return nil
}

func initialStandard() StandardDocument {
	return StandardDocument{SchemaVersion: ProfileSchemaVersion, Revision: 0, Enabled: false, ProfileRoots: ProfileRoots{ReadOnlyDirs: []string{}, ReadWriteDirs: []string{}}}
}

func validateStandard(doc StandardDocument) error {
	if doc.SchemaVersion != ProfileSchemaVersion {
		return &ProfileError{Code: "unsupported_schema", Field: "schemaVersion"}
	}
	return validateRoots(doc.ProfileRoots)
}

func validateRoots(roots ProfileRoots) error {
	if len(roots.ReadOnlyDirs) > MaxProfileRoots {
		return &ProfileError{Code: "too_many_roots", Field: "readOnlyDirs", Index: MaxProfileRoots}
	}
	if len(roots.ReadWriteDirs) > MaxProfileRoots {
		return &ProfileError{Code: "too_many_roots", Field: "readWriteDirs", Index: MaxProfileRoots}
	}
	for _, class := range []struct {
		name  string
		paths []string
	}{{"readOnlyDirs", roots.ReadOnlyDirs}, {"readWriteDirs", roots.ReadWriteDirs}} {
		for i, path := range class.paths {
			if len(path) == 0 {
				return &ProfileError{Code: "empty_path", Field: class.name, Index: i}
			}
			if len(path) > MaxPathBytes {
				return &ProfileError{Code: "path_too_long", Field: class.name, Index: i}
			}
			for _, ch := range path {
				if ch == 0 || ch == '\n' || ch == '\r' {
					return &ProfileError{Code: "invalid_path", Field: class.name, Index: i}
				}
			}
		}
	}
	return nil
}

func cloneRoots(in ProfileRoots) ProfileRoots {
	out := ProfileRoots{ReadOnlyDirs: make([]string, len(in.ReadOnlyDirs)), ReadWriteDirs: make([]string, len(in.ReadWriteDirs))}
	copy(out.ReadOnlyDirs, in.ReadOnlyDirs)
	copy(out.ReadWriteDirs, in.ReadWriteDirs)
	return out
}

func cloneStandard(in StandardDocument) StandardDocument {
	in.ProfileRoots = cloneRoots(in.ProfileRoots)
	return in
}

func cloneWorkspaceProfile(in WorkspaceProfile) WorkspaceProfile {
	if in.Override != nil {
		roots := cloneRoots(*in.Override)
		in.Override = &roots
	}
	return in
}

func cloneWorkspaceProfiles(in []WorkspaceProfile) []WorkspaceProfile {
	out := make([]WorkspaceProfile, len(in))
	for i := range in {
		out[i] = cloneWorkspaceProfile(in[i])
	}
	return out
}

// EffectiveWorkspaceRoots applies sparse override inheritance without aliasing.
func EffectiveWorkspaceRoots(workspace WorkspaceProfile, standard StandardDocument) ProfileRoots {
	if workspace.Override != nil {
		return cloneRoots(*workspace.Override)
	}
	return cloneRoots(standard.ProfileRoots)
}

func (r *ProfileRepository) GetWorkspaceProfileForPane(ctx context.Context, layout interface {
	WorkspaceForPane(context.Context, string) (string, error)
}, paneID string,
) (WorkspaceProfile, error) {
	if r.workspaces == nil {
		return WorkspaceProfile{}, ErrWorkspaceProfileUnsupported
	}
	workspaceID, err := layout.WorkspaceForPane(ctx, paneID)
	if err != nil {
		return WorkspaceProfile{}, err
	}
	return r.GetWorkspaceProfile(ctx, workspaceID)
}

func CopyStandardToOverride(standard StandardDocument) *ProfileRoots {
	roots := cloneRoots(standard.ProfileRoots)
	return &roots
}
func ValidateWorkspaceRoots(roots ProfileRoots) error               { return validateRoots(roots) }
func WorkspaceProfileSnapshot(in WorkspaceProfile) WorkspaceProfile { return cloneWorkspaceProfile(in) }
