package content

// The worker_checkouts repository (nocx-xn63t.1.4). A thin wrapper around
// *sqliteContent rather than methods on the shared receiver, for the reason
// skillCheckSqlite's own header states: List/All and Delete over one shape
// of key here would collide with the repositories that already claim those
// verb names over another shape on *sqliteContent, and Go has no method
// overloading. It shares the one writer and the one handle — s.run and s.db
// below are the shared ones — so only the method set moves.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type workerCheckoutSqlite struct {
	s *sqliteContent
}

var _ WorkerCheckoutRepository = (*workerCheckoutSqlite)(nil)

// WorkerCheckouts returns the worker-checkout repository.
func (s *sqliteContent) WorkerCheckouts() WorkerCheckoutRepository {
	return &workerCheckoutSqlite{s: s}
}

// Put records the checkout a spawn just created, rewriting a row that is
// already there for the same (repo key, path).
func (r *workerCheckoutSqlite) Put(ctx context.Context, co WorkerCheckout) error {
	if co.RepoKey == "" || co.Path == "" {
		return errors.New("content: worker checkout: repo key and path are both the primary key; neither may be empty")
	}
	return r.s.run(ctx, func(ctx context.Context) error {
		_, execErr := r.s.db.ExecContext(ctx, `INSERT INTO worker_checkouts
			(repo_key, path, branch, base, name, task, created_at, last_used_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(repo_key, path) DO UPDATE SET
				branch       = excluded.branch,
				base         = excluded.base,
				name         = excluded.name,
				task         = excluded.task,
				created_at   = excluded.created_at,
				last_used_at = excluded.last_used_at`,
			co.RepoKey, co.Path, co.Branch, co.Base, co.Name, co.Task,
			co.CreatedAt, co.LastUsedAt)
		if execErr != nil {
			return fmt.Errorf("content: worker checkout: put %q: %w", co.Path, execErr)
		}
		return nil
	})
}

// Touch moves last_used_at forward to at, for the one row the path names.
// The comparison lives in the statement, so the row wins over the clock:
// whatever the caller's wall clock did, the stored time never moves back.
// A path no row carries matches nothing and changes nothing.
func (r *workerCheckoutSqlite) Touch(ctx context.Context, path string, at int64) error {
	if path == "" {
		return errors.New("content: worker checkout: touch needs a path")
	}
	return r.s.run(ctx, func(ctx context.Context) error {
		_, execErr := r.s.db.ExecContext(ctx,
			`UPDATE worker_checkouts SET last_used_at = ? WHERE path = ? AND last_used_at < ?`,
			at, path, at)
		if execErr != nil {
			return fmt.Errorf("content: worker checkout: touch %q: %w", path, execErr)
		}
		return nil
	})
}

// List returns every row of one repository's key.
func (r *workerCheckoutSqlite) List(ctx context.Context, repoKey string) ([]WorkerCheckout, error) {
	rows, err := r.s.db.QueryContext(ctx, `SELECT repo_key, path, branch, base, name, task, created_at, last_used_at
		FROM worker_checkouts WHERE repo_key = ?`, repoKey)
	if err != nil {
		return nil, fmt.Errorf("content: worker checkout: list %q: %w", repoKey, err)
	}
	defer func() { _ = rows.Close() }()
	return scanWorkerCheckouts(rows)
}

// All returns every row regardless of repository.
func (r *workerCheckoutSqlite) All(ctx context.Context) ([]WorkerCheckout, error) {
	rows, err := r.s.db.QueryContext(ctx, `SELECT repo_key, path, branch, base, name, task, created_at, last_used_at
		FROM worker_checkouts`)
	if err != nil {
		return nil, fmt.Errorf("content: worker checkout: list all: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanWorkerCheckouts(rows)
}

// Delete drops the named rows of one repository. Idempotent.
func (r *workerCheckoutSqlite) Delete(ctx context.Context, repoKey string, paths []string) error {
	return r.s.run(ctx, func(ctx context.Context) error {
		for _, path := range paths {
			if _, execErr := r.s.db.ExecContext(ctx,
				`DELETE FROM worker_checkouts WHERE repo_key = ? AND path = ?`, repoKey, path); execErr != nil {
				return fmt.Errorf("content: worker checkout: delete %q: %w", path, execErr)
			}
		}
		return nil
	})
}

func scanWorkerCheckouts(rows *sql.Rows) ([]WorkerCheckout, error) {
	var out []WorkerCheckout
	for rows.Next() {
		var co WorkerCheckout
		if err := rows.Scan(&co.RepoKey, &co.Path, &co.Branch, &co.Base, &co.Name, &co.Task,
			&co.CreatedAt, &co.LastUsedAt); err != nil {
			return nil, fmt.Errorf("content: worker checkout: scan: %w", err)
		}
		out = append(out, co)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("content: worker checkout: rows: %w", err)
	}
	return out, nil
}
