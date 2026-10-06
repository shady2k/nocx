package content

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shady2k/nocx/internal/sandbox"
)

var _ LaunchRepository = (*sqliteContent)(nil)

type launchQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type launchScanner interface {
	Scan(...any) error
}

const launchColumns = `l.id,l.pane_id,l.workspace_id,l.standard_revision,l.workspace_revision,l.source_session_id,l.source_head_id,l.source_host,l.source_account,l.source_generation,l.mode,l.state,l.helper_session_id,l.helper_host,l.helper_account,l.helper_generation,l.policy_digest,l.policy_version,l.policy,l.created_at,l.updated_at,g.id`

func scanLaunch(row launchScanner) (Launch, error) {
	var out Launch
	var sourceID, sourceHeadID, helperID, helperHost, helperAccount, helperGeneration sql.NullString
	var digest, policyJSON sql.NullString
	var version sql.NullInt64
	var grantID sql.NullInt64
	var created, updated int64
	err := row.Scan(&out.ID, &out.PaneID, &out.WorkspaceID, &out.StandardRevision, &out.WorkspaceRevision, &sourceID, &sourceHeadID, &out.Source.Host, &out.Source.Account, &out.Source.Generation, &out.Mode, &out.State, &helperID, &helperHost, &helperAccount, &helperGeneration, &digest, &version, &policyJSON, &created, &updated, &grantID)
	if errors.Is(err, sql.ErrNoRows) {
		return Launch{}, ErrNotFound
	}
	if err != nil {
		return Launch{}, err
	}
	out.Source.SessionID = sourceID.String
	out.SourceHeadID = sourceHeadID.String
	out.TargetGeneration = helperGeneration.String
	if helperID.Valid {
		h := HelperIdentity{Host: helperHost.String, Account: helperAccount.String, Generation: helperGeneration.String, SessionID: helperID.String}
		if !h.valid() {
			return Launch{}, ErrInvalidLaunch
		}
		out.Helper = &h
	}
	out.PolicyDigest = digest.String
	out.PolicyVersion = int(version.Int64)
	if policyJSON.Valid {
		policy, err := decodeStoredLaunchPolicy(policyJSON.String, out.PolicyDigest, out.PolicyVersion)
		if err != nil {
			return Launch{}, err
		}
		out.Policy = &policy
	}
	if grantID.Valid {
		id := grantID.Int64
		out.GrantID = &id
	}
	if out.Mode == LaunchEnforce && out.GrantID == nil || out.Mode == LaunchOff && out.GrantID != nil {
		return Launch{}, ErrInvalidLaunch
	}
	out.CreatedAt = time.UnixMilli(created)
	out.UpdatedAt = time.UnixMilli(updated)
	return out, nil
}

func decodeStoredLaunchPolicy(raw, digest string, version int) (sandbox.Policy, error) {
	policy, err := sandbox.DecodePolicy([]byte(raw))
	if err != nil {
		return sandbox.Policy{}, err
	}
	if policy.Version != version {
		return sandbox.Policy{}, ErrLaunchUnsupportedPolicy
	}
	_, actual, err := sandbox.EncodePolicy(policy)
	if err != nil {
		return sandbox.Policy{}, err
	}
	if actual != digest {
		return sandbox.Policy{}, ErrInvalidLaunch
	}
	return policy, nil
}

func launchByID(ctx context.Context, q launchQueryer, id string) (Launch, error) {
	return scanLaunch(q.QueryRowContext(ctx, `SELECT `+launchColumns+` FROM pane_launches l LEFT JOIN authority_grants g ON g.launch_id=l.id AND g.execution_id IS NULL WHERE l.id=?`, id))
}

func (s *sqliteContent) Prepare(ctx context.Context, in LaunchPrepare) (Launch, error) {
	if in.ID == "" || in.PaneID == "" || in.WorkspaceID == "" || !in.Source.valid() || in.TargetGeneration == "" || in.Mode != LaunchOff && in.Mode != LaunchEnforce {
		return Launch{}, ErrInvalidLaunch
	}
	var policyJSON string
	if in.Mode == LaunchEnforce {
		if in.Policy == nil || in.PolicyVersion != sandbox.PolicyVersion || in.Policy.Version != in.PolicyVersion || in.Policy.WorkspaceID != in.WorkspaceID || in.Policy.StandardRevision != in.StandardRevision || in.Policy.WorkspaceRevision != in.WorkspaceRevision {
			return Launch{}, ErrInvalidLaunch
		}
		encoded, digest, err := sandbox.EncodePolicy(*in.Policy)
		if err != nil {
			return Launch{}, err
		}
		if digest != in.PolicyDigest {
			return Launch{}, ErrInvalidLaunch
		}
		policyJSON = string(encoded)
	} else if in.Policy != nil || in.PolicyDigest != "" || in.PolicyVersion != 0 {
		return Launch{}, ErrInvalidLaunch
	}
	var result Launch
	err := s.run(ctx, func(ctx context.Context) error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		prior, err := launchByID(ctx, tx, in.ID)
		if err == nil {
			if sameLaunchIntent(prior, in) {
				result = prior
				return nil
			}
			return ErrLaunchIntentConflict
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if err = validatePaneWorkspace(ctx, tx, in.PaneID, in.WorkspaceID); err != nil {
			return err
		}
		if err = validateWorkspaceRevision(ctx, tx, in.WorkspaceID, in.WorkspaceRevision); err != nil {
			return err
		}
		if err = validateCurrentHead(ctx, tx, in.PaneID, in.ExpectedHeadID, in.Source); err != nil {
			return err
		}
		if in.ExpectedHeadID == "" {
			if err = validateSource(ctx, tx, in.Source, in.PaneID, in.WorkspaceID); err != nil {
				return err
			}
		} else {
			head, headErr := launchByID(ctx, tx, in.ExpectedHeadID)
			if headErr != nil {
				return headErr
			}
			if head.State != LaunchEnded {
				if err = validateSource(ctx, tx, in.Source, in.PaneID, in.WorkspaceID); err != nil {
					return err
				}
			}
		}
		var preparing int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM pane_launches WHERE state='preparing'`).Scan(&preparing); err != nil {
			return err
		}
		if preparing >= 32 {
			return ErrLaunchConflict
		}
		now := time.Now().UnixMilli()
		var encoded any = policyJSON
		var digest any = in.PolicyDigest
		var version any = in.PolicyVersion
		if in.Mode == LaunchOff {
			encoded = nil
			digest = nil
			version = nil
		}
		var expectedHead any = in.ExpectedHeadID
		if in.ExpectedHeadID == "" {
			expectedHead = nil
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO pane_launches (id,pane_id,workspace_id,standard_revision,workspace_revision,source_session_id,source_head_id,source_host,source_account,source_generation,mode,state,helper_host,helper_account,helper_generation,policy_digest,policy_version,policy,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,'preparing',?,?,?,?,?,?,?,?)`, in.ID, in.PaneID, in.WorkspaceID, in.StandardRevision, in.WorkspaceRevision, in.Source.SessionID, expectedHead, in.Source.Host, in.Source.Account, in.Source.Generation, in.Mode, in.Source.Host, in.Source.Account, in.TargetGeneration, digest, version, encoded, now, now)
		if err != nil {
			return mapLaunchWriteError(err)
		}
		if in.Mode == LaunchEnforce {
			if _, err = tx.ExecContext(ctx, `INSERT INTO authority_grants (execution_id,launch_id,version,issued_at,expires_at,policy) VALUES (NULL,?,?,?,NULL,?)`, in.ID, in.PolicyVersion, now, policyJSON); err != nil {
				return err
			}
		}
		result, err = launchByID(ctx, tx, in.ID)
		if err != nil {
			return err
		}
		return tx.Commit()
	})
	return result, err
}

func sameLaunchIntent(existing Launch, in LaunchPrepare) bool {
	if existing.PaneID != in.PaneID || existing.WorkspaceID != in.WorkspaceID || existing.Source != in.Source || existing.SourceHeadID != in.ExpectedHeadID || existing.TargetGeneration != in.TargetGeneration || existing.StandardRevision != in.StandardRevision || existing.WorkspaceRevision != in.WorkspaceRevision || existing.Mode != in.Mode || existing.PolicyDigest != in.PolicyDigest || existing.PolicyVersion != in.PolicyVersion {
		return false
	}
	if existing.Mode == LaunchEnforce {
		return existing.Policy != nil && in.Policy != nil && existing.Policy.StandardRevision == in.StandardRevision && existing.Policy.WorkspaceRevision == in.WorkspaceRevision
	}
	return true
}

func validatePaneWorkspace(ctx context.Context, q launchQueryer, paneID, workspaceID string) error {
	var kind string
	var closed sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT p.kind,p.closed_at FROM panes p JOIN tabs t ON t.id=p.tab_id WHERE p.id=? AND t.workspace_id=?`, paneID, workspaceID).Scan(&kind, &closed)
	if errors.Is(err, sql.ErrNoRows) || closed.Valid || kind != "local" {
		return ErrLaunchConflict
	}
	return err
}

func validateWorkspaceRevision(ctx context.Context, q launchQueryer, workspaceID string, revision uint64) error {
	var got int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(payload,'$.sandbox.revision'),0) FROM workspaces WHERE id=?`, workspaceID).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) || got < 0 || uint64(got) != revision {
		return ErrLaunchConflict
	}
	return err
}

func validateSource(ctx context.Context, q launchQueryer, source HelperIdentity, paneID, workspaceID string) error {
	var pane string
	var generation, host, account sql.NullString
	err := q.QueryRowContext(ctx, `SELECT json_extract(s.payload,'$.pane'),json_extract(s.payload,'$.generation'),json_extract(s.payload,'$.host'),json_extract(s.payload,'$.account') FROM sessions s JOIN workspaces w ON w.id=s.workspace_id WHERE s.id=? AND s.workspace_id=? AND s.ended_at IS NULL`, source.SessionID, workspaceID).Scan(&pane, &generation, &host, &account)
	if errors.Is(err, sql.ErrNoRows) || pane != paneID || generation.String != source.Generation || host.String != source.Host || account.String != source.Account {
		return ErrLaunchConflict
	}
	return err
}

func validateCurrentHead(ctx context.Context, q launchQueryer, paneID, expectedHead string, source HelperIdentity) error {
	var actual string
	err := q.QueryRowContext(ctx, `SELECT launch_id FROM pane_launch_heads WHERE pane_id=?`, paneID).Scan(&actual)
	if expectedHead == "" {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return ErrLaunchConflict
	}
	if err != nil || actual != expectedHead {
		return ErrLaunchConflict
	}
	current, err := launchByID(ctx, q, actual)
	if err != nil {
		return ErrLaunchConflict
	}
	if current.State != LaunchActive && current.State != LaunchEnded || current.Helper == nil || *current.Helper != source {
		return ErrLaunchConflict
	}
	return nil
}

func mapLaunchWriteError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrLaunchConflict, err)
}

func (s *sqliteContent) Commit(ctx context.Context, in LaunchCommit) (Launch, error) {
	if in.LaunchID == "" || in.SourceCwd == "" || !in.ExpectedSource.valid() || !in.Candidate.valid() || in.Candidate == in.ExpectedSource {
		return Launch{}, ErrInvalidLaunch
	}
	var result Launch
	err := s.run(ctx, func(ctx context.Context) error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		launch, err := launchByID(ctx, tx, in.LaunchID)
		if err != nil {
			return err
		}
		if launch.State == LaunchActive {
			if launch.Helper != nil && *launch.Helper == in.Candidate && launch.Source == in.ExpectedSource && launch.SourceHeadID == in.ExpectedHeadID {
				result = launch
				return nil
			}
			return ErrLaunchIntentConflict
		}
		if launch.State != LaunchPreparing || launch.Source != in.ExpectedSource || launch.SourceHeadID != in.ExpectedHeadID {
			return ErrLaunchConflict
		}
		if in.Candidate.Generation != launch.TargetGeneration || in.Candidate.Host != launch.Source.Host || in.Candidate.Account != launch.Source.Account || in.Binding.ID != in.Candidate.SessionID || in.Binding.WorkspaceID != launch.WorkspaceID || in.Binding.PaneID != launch.PaneID || in.Binding.Generation != in.Candidate.Generation || in.Binding.Host != in.Candidate.Host || in.Binding.Account != in.Candidate.Account || in.Binding.LifecycleApplied == nil || *in.Binding.LifecycleApplied != 0 {
			return ErrInvalidLaunch
		}
		if err = validatePaneWorkspace(ctx, tx, launch.PaneID, launch.WorkspaceID); err != nil {
			return err
		}
		var cwd string
		if err = tx.QueryRowContext(ctx, `SELECT cwd FROM panes WHERE id=?`, launch.PaneID).Scan(&cwd); err != nil {
			return err
		}
		if cwd != in.SourceCwd {
			return ErrLaunchConflict
		}
		if err = validateWorkspaceRevision(ctx, tx, launch.WorkspaceID, launch.WorkspaceRevision); err != nil {
			return err
		}
		if err = validateCurrentHead(ctx, tx, launch.PaneID, in.ExpectedHeadID, in.ExpectedSource); err != nil {
			return err
		}
		if in.ExpectedHeadID == "" {
			if err = validateSource(ctx, tx, in.ExpectedSource, launch.PaneID, launch.WorkspaceID); err != nil {
				return err
			}
		} else {
			head, headErr := launchByID(ctx, tx, in.ExpectedHeadID)
			if headErr != nil {
				return headErr
			}
			if head.State != LaunchEnded {
				if err = validateSource(ctx, tx, in.ExpectedSource, launch.PaneID, launch.WorkspaceID); err != nil {
					return err
				}
			}
		}
		now := time.Now().UnixMilli()
		payload, payloadErr := encodeSessionPayload(in.Binding)
		if payloadErr != nil {
			return payloadErr
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO sessions (id,workspace_id,started_at,payload) VALUES (?,?,?,?)`, in.Binding.ID, launch.WorkspaceID, now, payload); err != nil {
			return err
		}
		var previous string
		headErr := tx.QueryRowContext(ctx, `SELECT launch_id FROM pane_launch_heads WHERE pane_id=?`, launch.PaneID).Scan(&previous)
		if headErr != nil && !errors.Is(headErr, sql.ErrNoRows) {
			return headErr
		}
		if headErr == nil && previous != in.LaunchID {
			if _, err = tx.ExecContext(ctx, `UPDATE pane_launches SET state='ended',updated_at=? WHERE id=? AND state='active'`, now, previous); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE pane_launches SET state='active',helper_session_id=?,helper_host=?,helper_account=?,helper_generation=?,updated_at=? WHERE id=? AND state='preparing'`, in.Candidate.SessionID, in.Candidate.Host, in.Candidate.Account, in.Candidate.Generation, now, in.LaunchID)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO pane_launch_heads(pane_id,launch_id) VALUES(?,?) ON CONFLICT(pane_id) DO UPDATE SET launch_id=excluded.launch_id`, launch.PaneID, in.LaunchID); err != nil {
			return err
		}
		ret := SessionRetirement{Identity: in.ExpectedSource, OperationID: in.LaunchID, Cause: RetirementReplacement, ClosePending: true, CreatedAt: time.UnixMilli(now)}
		if err = insertRetirement(ctx, tx, ret); err != nil {
			return err
		}
		result, err = launchByID(ctx, tx, in.LaunchID)
		if err != nil {
			return err
		}
		return tx.Commit()
	})
	return result, err
}

func (s *sqliteContent) Fail(ctx context.Context, id string, candidate *HelperIdentity) error {
	if id == "" || candidate != nil && !candidate.valid() {
		return ErrInvalidLaunch
	}
	return s.run(ctx, func(ctx context.Context) error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		launch, err := launchByID(ctx, tx, id)
		if errors.Is(err, ErrNotFound) {
			return err
		}
		if err != nil {
			return err
		}
		if launch.State == LaunchFailed {
			if candidate != nil {
				if launch.Helper != nil {
					if *launch.Helper != *candidate {
						return ErrLaunchIntentConflict
					}
				} else {
					if _, err = tx.ExecContext(ctx, `UPDATE pane_launches SET helper_session_id=?,helper_host=?,helper_account=?,helper_generation=?,updated_at=? WHERE id=? AND state='failed'`, candidate.SessionID, candidate.Host, candidate.Account, candidate.Generation, time.Now().UnixMilli(), id); err != nil {
						return err
					}
					if err = insertRetirement(ctx, tx, SessionRetirement{Identity: *candidate, OperationID: id, Cause: RetirementFailedCandidate, ClosePending: true, CreatedAt: time.Now()}); err != nil {
						return err
					}
				}
			}
			return tx.Commit()
		}
		if launch.State != LaunchPreparing {
			return ErrLaunchConflict
		}
		now := time.Now().UnixMilli()
		if candidate == nil {
			_, err = tx.ExecContext(ctx, `UPDATE pane_launches SET state='failed',updated_at=? WHERE id=? AND state='preparing'`, now, id)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE pane_launches SET state='failed',helper_session_id=?,helper_host=?,helper_account=?,helper_generation=?,updated_at=? WHERE id=? AND state='preparing'`, candidate.SessionID, candidate.Host, candidate.Account, candidate.Generation, now, id)
		}
		if err != nil {
			return err
		}
		if candidate != nil {
			if err = insertRetirement(ctx, tx, SessionRetirement{Identity: *candidate, OperationID: id, Cause: RetirementFailedCandidate, ClosePending: true, CreatedAt: time.UnixMilli(now)}); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

func insertRetirement(ctx context.Context, tx *sql.Tx, in SessionRetirement) error {
	if !in.Identity.valid() || in.OperationID == "" || (in.Cause != RetirementReplacement && in.Cause != RetirementFailedCandidate) {
		return ErrInvalidLaunch
	}
	pending := 0
	if in.ClosePending {
		pending = 1
	}
	created := in.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO session_retirements(host_session_id,host,account,generation,operation_id,cause,close_pending,created_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(host,account,generation,host_session_id) DO NOTHING`, in.Identity.SessionID, in.Identity.Host, in.Identity.Account, in.Identity.Generation, in.OperationID, in.Cause, pending, created.UnixMilli())
	if err != nil {
		return err
	}
	var operation, cause string
	var state int
	err = tx.QueryRowContext(ctx, `SELECT operation_id,cause,close_pending FROM session_retirements WHERE host=? AND account=? AND generation=? AND host_session_id=?`, in.Identity.Host, in.Identity.Account, in.Identity.Generation, in.Identity.SessionID).Scan(&operation, &cause, &state)
	if err != nil {
		return err
	}
	if operation != in.OperationID || cause != string(in.Cause) || state != pending {
		return ErrLaunchIntentConflict
	}
	return nil
}

func (s *sqliteContent) RecordRetirement(ctx context.Context, in SessionRetirement) error {
	return s.run(ctx, func(ctx context.Context) error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err = insertRetirement(ctx, tx, in); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func (s *sqliteContent) CompleteRetirement(ctx context.Context, in RetirementConfirmation) error {
	if !in.Identity.valid() || in.Resolution != RetirementClosed && in.Resolution != RetirementAbsent {
		return ErrInvalidLaunch
	}
	return s.run(ctx, func(ctx context.Context) error {
		result, err := s.db.ExecContext(ctx, `UPDATE session_retirements SET close_pending=0 WHERE host=? AND account=? AND generation=? AND host_session_id=?`, in.Identity.Host, in.Identity.Account, in.Identity.Generation, in.Identity.SessionID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *sqliteContent) GetLaunch(ctx context.Context, id string) (Launch, error) {
	return launchByID(ctx, s.db, id)
}

// SourceBinding reads the ordinary opener's durable restore key, not a second
// routing table. A closed source cannot authorize a fresh replacement.
func (s *sqliteContent) SourceBinding(ctx context.Context, paneID, sessionID string) (HelperIdentity, error) {
	if paneID == "" || sessionID == "" {
		return HelperIdentity{}, ErrInvalidLaunch
	}
	out := HelperIdentity{SessionID: sessionID}
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(json_extract(payload,'$.host'),''),COALESCE(json_extract(payload,'$.account'),''),COALESCE(json_extract(payload,'$.generation'),'') FROM sessions WHERE id=? AND json_extract(payload,'$.pane')=? AND ended_at IS NULL`, sessionID, paneID).Scan(&out.Host, &out.Account, &out.Generation)
	if errors.Is(err, sql.ErrNoRows) {
		return HelperIdentity{}, ErrNotFound
	}
	if err != nil {
		return HelperIdentity{}, err
	}
	if !out.valid() {
		return HelperIdentity{}, ErrInvalidLaunch
	}
	return out, nil
}

func (s *sqliteContent) ByHelper(ctx context.Context, identity HelperIdentity) (Launch, error) {
	if !identity.valid() {
		return Launch{}, ErrInvalidLaunch
	}
	return scanLaunch(s.db.QueryRowContext(ctx, `SELECT `+launchColumns+` FROM pane_launches l LEFT JOIN authority_grants g ON g.launch_id=l.id AND g.execution_id IS NULL WHERE l.helper_session_id=? AND l.helper_host=? AND l.helper_account=? AND l.helper_generation=?`, identity.SessionID, identity.Host, identity.Account, identity.Generation))
}

func (s *sqliteContent) Retirement(ctx context.Context, identity HelperIdentity) (SessionRetirement, error) {
	if !identity.valid() {
		return SessionRetirement{}, ErrInvalidLaunch
	}
	var out SessionRetirement
	var pending int
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT host_session_id,host,account,generation,operation_id,cause,close_pending,created_at FROM session_retirements WHERE host=? AND account=? AND generation=? AND host_session_id=?`, identity.Host, identity.Account, identity.Generation, identity.SessionID).Scan(&out.Identity.SessionID, &out.Identity.Host, &out.Identity.Account, &out.Identity.Generation, &out.OperationID, &out.Cause, &pending, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionRetirement{}, ErrNotFound
	}
	if err != nil {
		return SessionRetirement{}, err
	}
	out.ClosePending = pending == 1
	out.CreatedAt = time.UnixMilli(created)
	return out, nil
}

func (s *sqliteContent) Head(ctx context.Context, paneID string) (Launch, error) {
	return scanLaunch(s.db.QueryRowContext(ctx, `SELECT `+launchColumns+` FROM pane_launch_heads h JOIN pane_launches l ON l.id=h.launch_id LEFT JOIN authority_grants g ON g.launch_id=l.id AND g.execution_id IS NULL WHERE h.pane_id=?`, paneID))
}

func (s *sqliteContent) Preparing(ctx context.Context) ([]Launch, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+launchColumns+` FROM pane_launches l LEFT JOIN authority_grants g ON g.launch_id=l.id AND g.execution_id IS NULL WHERE l.state='preparing' ORDER BY l.created_at,l.id LIMIT 33`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]Launch, 0, 32)
	for rows.Next() {
		if len(out) == 32 {
			return nil, ErrLaunchConflict
		}
		item, err := scanLaunch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *sqliteContent) PendingRetirements(ctx context.Context, limit int) ([]SessionRetirement, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT host_session_id,host,account,generation,operation_id,cause,close_pending,created_at FROM session_retirements WHERE close_pending=1 ORDER BY created_at,host,account,generation,host_session_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []SessionRetirement
	for rows.Next() {
		var item SessionRetirement
		var pending int
		var created int64
		if err = rows.Scan(&item.Identity.SessionID, &item.Identity.Host, &item.Identity.Account, &item.Identity.Generation, &item.OperationID, &item.Cause, &pending, &created); err != nil {
			return nil, err
		}
		item.ClosePending = pending == 1
		item.CreatedAt = time.UnixMilli(created)
		result = append(result, item)
	}
	return result, rows.Err()
}
