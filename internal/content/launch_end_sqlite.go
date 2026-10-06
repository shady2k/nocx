package content

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *sqliteContent) End(ctx context.Context, identity HelperIdentity) error {
	if !identity.valid() {
		return ErrInvalidLaunch
	}
	return s.run(ctx, func(ctx context.Context) error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		result, err := tx.ExecContext(ctx, `UPDATE pane_launches SET state='ended',updated_at=? WHERE helper_session_id=? AND helper_host=? AND helper_account=? AND helper_generation=? AND state='active'`, time.Now().UnixMilli(), identity.SessionID, identity.Host, identity.Account, identity.Generation)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count > 1 {
			return ErrLaunchConflict
		}
		if count == 1 {
			return tx.Commit()
		}
		var state LaunchState
		err = tx.QueryRowContext(ctx, `SELECT state FROM pane_launches WHERE helper_session_id=? AND helper_host=? AND helper_account=? AND helper_generation=? LIMIT 1`, identity.SessionID, identity.Host, identity.Account, identity.Generation).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if state == LaunchEnded || state == LaunchFailed {
			return nil
		}
		return ErrLaunchConflict
	})
}
