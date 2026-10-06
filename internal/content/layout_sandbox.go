package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/shady2k/nocx/internal/sandbox"
)

const maxWorkspaceProfilePayloadBytes = 1 << 20

var _ sandbox.WorkspaceProfileStore = (*sqliteContent)(nil)

// GetWorkspaceProfile reads the typed sparse sandbox extension. The default
// workspace always inherits standard and has no persisted override or clock.
func (s *sqliteContent) GetWorkspaceProfile(ctx context.Context, workspaceID string) (sandbox.WorkspaceProfile, error) {
	if workspaceID == DefaultWorkspaceID {
		return sandbox.WorkspaceProfile{WorkspaceID: workspaceID}, nil
	}
	if s.closed.Load() {
		return sandbox.WorkspaceProfile{}, ErrClosed
	}
	var payload string
	err := s.conn(ctx).QueryRowContext(ctx, `SELECT payload FROM workspaces WHERE id = ?`, workspaceID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return sandbox.WorkspaceProfile{}, fmt.Errorf("%w: %s", ErrNoSuchWorkspace, workspaceID)
	}
	if err != nil {
		return sandbox.WorkspaceProfile{}, err
	}
	return decodeWorkspaceProfile(workspaceID, payload)
}

// UpdateWorkspaceProfile performs revision CAS and advances the workspace clock
// for both override edits and reset-to-inherit. The default workspace is never
// mutable, and a missing named workspace is never created by profile editing.
func (s *sqliteContent) UpdateWorkspaceProfile(ctx context.Context, workspaceID string, expected uint64, roots *sandbox.ProfileRoots) (sandbox.WorkspaceProfile, error) {
	if workspaceID == DefaultWorkspaceID {
		return sandbox.WorkspaceProfile{}, sandbox.ErrWorkspaceProfileUnsupported
	}
	if roots != nil {
		if err := sandbox.ValidateWorkspaceRoots(*roots); err != nil {
			return sandbox.WorkspaceProfile{}, err
		}
	}
	var out sandbox.WorkspaceProfile
	err := s.run(ctx, func(ctx context.Context) error {
		return s.inTx(ctx, func(tx *sql.Tx) error {
			var payload string
			if err := tx.QueryRowContext(ctx, `SELECT payload FROM workspaces WHERE id = ?`, workspaceID).Scan(&payload); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("%w: %s", ErrNoSuchWorkspace, workspaceID)
				}
				return err
			}
			current, err := decodeWorkspaceProfile(workspaceID, payload)
			if err != nil {
				return err
			}
			if current.Revision != expected {
				return sandbox.ErrProfileConflict
			}
			if current.Revision == math.MaxUint64 {
				return sandbox.ErrProfileRevisionExhausted
			}
			current.Revision++
			current.Override = cloneSandboxRoots(roots)
			encoded, err := encodeWorkspaceProfile(payload, current)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE workspaces SET payload = ? WHERE id = ?`, encoded, workspaceID); err != nil {
				return err
			}
			out = sandbox.WorkspaceProfileSnapshot(current)
			return nil
		})
	})
	if err != nil {
		return sandbox.WorkspaceProfile{}, err
	}
	return out, nil
}

// ListWorkspaceProfiles returns all named workspaces, including inheriting
// ones, in bounded stable order. This is mutable configuration, not authority.
func (s *sqliteContent) ListWorkspaceProfiles(ctx context.Context) ([]sandbox.WorkspaceProfile, error) {
	if s.closed.Load() {
		return nil, ErrClosed
	}
	rows, err := s.conn(ctx).QueryContext(ctx, `SELECT id, payload FROM workspaces WHERE id <> ? ORDER BY id LIMIT ?`, DefaultWorkspaceID, sandbox.MaxWorkspaceProfiles+1)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]sandbox.WorkspaceProfile, 0, sandbox.MaxWorkspaceProfiles)
	total := 0
	for rows.Next() {
		if len(out) == sandbox.MaxWorkspaceProfiles {
			return nil, &sandbox.ProfileError{Code: "too_many_workspaces", Field: "workspaces", Index: sandbox.MaxWorkspaceProfiles}
		}
		var id, payload string
		if err := rows.Scan(&id, &payload); err != nil {
			return nil, err
		}
		profile, err := decodeWorkspaceProfile(id, payload)
		if err != nil {
			return nil, err
		}
		total += len(id) + 256
		if profile.Override != nil {
			for _, path := range profile.Override.ReadOnlyDirs {
				total += len(path)
			}
			for _, path := range profile.Override.ReadWriteDirs {
				total += len(path)
			}
		}
		if total > sandbox.MaxConfigurationBytes {
			return nil, &sandbox.ProfileError{Code: "configuration_too_large", Field: "workspaces", Index: len(out)}
		}
		out = append(out, profile)
	}
	return out, rows.Err()
}

// RestoreWorkspaceProfiles validates every identity before writing, then updates
// all sparse values in one ContentDB transaction while retaining local clocks.
func (s *sqliteContent) RestoreWorkspaceProfiles(ctx context.Context, profiles []sandbox.WorkspaceProfile) error {
	if len(profiles) > sandbox.MaxWorkspaceProfiles {
		return &sandbox.ProfileError{Code: "too_many_workspaces", Field: "workspaces", Index: sandbox.MaxWorkspaceProfiles}
	}
	byID := make(map[string]sandbox.WorkspaceProfile, len(profiles))
	for i, profile := range profiles {
		if profile.WorkspaceID == "" || profile.WorkspaceID == DefaultWorkspaceID {
			return &sandbox.ProfileError{Code: "invalid_workspace", Field: "workspaces", Index: i}
		}
		if _, ok := byID[profile.WorkspaceID]; ok {
			return &sandbox.ProfileError{Code: "duplicate_workspace", Field: "workspaces", Index: i}
		}
		if profile.Override != nil {
			if err := sandbox.ValidateWorkspaceRoots(*profile.Override); err != nil {
				return &sandbox.ProfileError{Code: "invalid_workspace_profile", Field: "workspaces", Index: i}
			}
		}
		byID[profile.WorkspaceID] = sandbox.WorkspaceProfileSnapshot(profile)
	}
	return s.run(ctx, func(ctx context.Context) error {
		return s.inTx(ctx, func(tx *sql.Tx) error {
			// Validate destination membership for every entry before changing any row.
			payloads := make(map[string]string, len(byID))
			for id := range byID {
				var payload string
				if err := tx.QueryRowContext(ctx, `SELECT payload FROM workspaces WHERE id = ?`, id).Scan(&payload); err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						return fmt.Errorf("%w: %s", ErrNoSuchWorkspace, id)
					}
					return err
				}
				if _, err := decodeWorkspaceProfile(id, payload); err != nil {
					return err
				}
				payloads[id] = payload
			}
			for id, imported := range byID {
				current, err := decodeWorkspaceProfile(id, payloads[id])
				if err != nil {
					return err
				}
				if current.Revision == math.MaxUint64 {
					return sandbox.ErrProfileRevisionExhausted
				}
				current.Revision++
				current.Override = cloneSandboxRoots(imported.Override)
				encoded, err := encodeWorkspaceProfile(payloads[id], current)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE workspaces SET payload = ? WHERE id = ?`, encoded, id); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

type workspaceSandboxPayload struct {
	Revision uint64                `json:"revision"`
	Override *sandbox.ProfileRoots `json:"override"`
}

func decodeWorkspaceProfile(id, payload string) (sandbox.WorkspaceProfile, error) {
	if len(payload) > maxWorkspaceProfilePayloadBytes {
		return sandbox.WorkspaceProfile{}, &sandbox.ProfileError{Code: "workspace_payload_too_large", Field: "workspace"}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &fields); err != nil {
		return sandbox.WorkspaceProfile{}, &sandbox.ProfileError{Code: "invalid_workspace_payload", Field: "workspace"}
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	var stored *workspaceSandboxPayload
	if raw, ok := fields["sandbox"]; ok {
		if err := json.Unmarshal(raw, &stored); err != nil || stored == nil {
			return sandbox.WorkspaceProfile{}, &sandbox.ProfileError{Code: "invalid_workspace_profile", Field: "sandbox"}
		}
	}
	if stored == nil {
		stored = &workspaceSandboxPayload{}
	}
	if stored.Override != nil {
		if err := sandbox.ValidateWorkspaceRoots(*stored.Override); err != nil {
			return sandbox.WorkspaceProfile{}, err
		}
	}
	return sandbox.WorkspaceProfile{WorkspaceID: id, Revision: stored.Revision, Override: cloneSandboxRoots(stored.Override)}, nil
}

func encodeWorkspaceProfile(payload string, profile sandbox.WorkspaceProfile) (string, error) {
	if len(payload) > maxWorkspaceProfilePayloadBytes {
		return "", &sandbox.ProfileError{Code: "workspace_payload_too_large", Field: "workspace"}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &fields); err != nil {
		return "", &sandbox.ProfileError{Code: "invalid_workspace_payload", Field: "workspace"}
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	encoded, err := json.Marshal(workspaceSandboxPayload{Revision: profile.Revision, Override: profile.Override})
	if err != nil {
		return "", err
	}
	fields["sandbox"] = encoded
	result, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	if len(result) > maxWorkspaceProfilePayloadBytes {
		return "", &sandbox.ProfileError{Code: "workspace_payload_too_large", Field: "workspace"}
	}
	return string(result), nil
}

func cloneSandboxRoots(roots *sandbox.ProfileRoots) *sandbox.ProfileRoots {
	if roots == nil {
		return nil
	}
	copy := sandbox.ProfileRoots{ReadOnlyDirs: append(make([]string, 0, len(roots.ReadOnlyDirs)), roots.ReadOnlyDirs...), ReadWriteDirs: append(make([]string, 0, len(roots.ReadWriteDirs)), roots.ReadWriteDirs...)}
	return &copy
}
