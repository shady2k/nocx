package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// skillCheckSqlite is a thin wrapper around *sqliteContent rather than the
// shared writer itself — the shape api_run_sqlite.go, ledger.go and
// reconcile_sqlite.go all use, where the repository's methods live directly
// on *sqliteContent and SkillChecks() would just `return s`. That shape does
// not compile here: APIRunRepository already claims Get(ctx, int64) and
// Delete(ctx, int64) on *sqliteContent (api_run_sqlite.go), Go has no method
// overloading, and SkillCheckRepository needs Get(ctx, string) and
// Delete(ctx, string) — same names, incompatible signatures, one receiver.
//
// The alternative considered and rejected was renaming this repository's
// methods — GetByName, DeleteByName — to dodge the collision. That trades a
// real defect (two owners of one name) for a cosmetic one: every other
// repository in this file calls the read "Get" and the removal "Delete",
// so a caller reading SkillCheckRepository next to APIRunRepository would
// have to remember which repository earned the honest verb and which one
// was renamed around a receiver it happens to share. The wrapper keeps the
// interface's vocabulary and pays the cost in one extra type instead.
//
// It still shares the one writer and the one db handle — s.run and s.db
// below are the shared ones — so there is exactly one writer goroutine and
// one open handle, unchanged from every other repository here; only the
// method set moves, not the resources underneath it. This is the first
// repository in the file that could not sit on the shared receiver — the
// next one that wants Get/Delete on a non-int64 key hits the same wall and
// should reach for the same fix.
type skillCheckSqlite struct {
	s *sqliteContent
}

var _ SkillCheckRepository = (*skillCheckSqlite)(nil)

// SkillChecks returns the skill-check repository.
func (s *sqliteContent) SkillChecks() SkillCheckRepository { return &skillCheckSqlite{s: s} }

// Put replaces the check for check.Name. The three lists are normalised to
// [] before marshalling — never nil — so that Get's own normalisation on
// the way out makes the round trip exact regardless of what the caller
// happened to pass in.
func (r *skillCheckSqlite) Put(ctx context.Context, check SkillCheck) error {
	if check.Name == "" {
		return errors.New("content: skill check: name is empty")
	}
	readPaths, err := json.Marshal(nonNil(check.Read))
	if err != nil {
		return fmt.Errorf("content: skill check: encode read paths: %w", err)
	}
	omitted, err := json.Marshal(nonNil(check.Omitted))
	if err != nil {
		return fmt.Errorf("content: skill check: encode omitted: %w", err)
	}
	findings, err := json.Marshal(nonNil(check.Findings))
	if err != nil {
		return fmt.Errorf("content: skill check: encode findings: %w", err)
	}
	return r.s.run(ctx, func(ctx context.Context) error {
		_, execErr := r.s.db.ExecContext(ctx, `INSERT INTO skill_checks
			(name, provenance, verdict, report, role, endpoint, model, digest,
			 checked_at, read_paths, omitted, findings, max_bytes)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET
				provenance = excluded.provenance,
				verdict    = excluded.verdict,
				report     = excluded.report,
				role       = excluded.role,
				endpoint   = excluded.endpoint,
				model      = excluded.model,
				digest     = excluded.digest,
				checked_at = excluded.checked_at,
				read_paths = excluded.read_paths,
				omitted    = excluded.omitted,
				findings   = excluded.findings,
				max_bytes  = excluded.max_bytes`,
			check.Name, check.Provenance, check.Verdict, check.Report, check.Role,
			check.Endpoint, check.Model, check.Digest, check.CheckedAt,
			string(readPaths), string(omitted), string(findings), check.MaxBytes)
		if execErr != nil {
			return fmt.Errorf("content: skill check: put %q: %w", check.Name, execErr)
		}
		return nil
	})
}

// Get returns found=false and a nil error when there is no row for name —
// "nobody has checked this" is a true answer, not a failure to distinguish
// from a broken store.
func (r *skillCheckSqlite) Get(ctx context.Context, name string) (SkillCheck, bool, error) {
	var (
		check                        SkillCheck
		readPaths, omitted, findings string
	)
	check.Name = name
	err := r.s.db.QueryRowContext(ctx, `SELECT provenance, verdict, report, role, endpoint,
		model, digest, checked_at, read_paths, omitted, findings, max_bytes
		FROM skill_checks WHERE name = ?`, name).Scan(
		&check.Provenance, &check.Verdict, &check.Report, &check.Role, &check.Endpoint,
		&check.Model, &check.Digest, &check.CheckedAt, &readPaths, &omitted, &findings, &check.MaxBytes)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SkillCheck{}, false, nil
		}
		return SkillCheck{}, false, fmt.Errorf("content: skill check: get %q: %w", name, err)
	}
	if err := json.Unmarshal([]byte(readPaths), &check.Read); err != nil {
		return SkillCheck{}, false, fmt.Errorf("content: skill check: decode read paths: %w", err)
	}
	if err := json.Unmarshal([]byte(omitted), &check.Omitted); err != nil {
		return SkillCheck{}, false, fmt.Errorf("content: skill check: decode omitted: %w", err)
	}
	if err := json.Unmarshal([]byte(findings), &check.Findings); err != nil {
		return SkillCheck{}, false, fmt.Errorf("content: skill check: decode findings: %w", err)
	}
	check.Read = nonNil(check.Read)
	check.Omitted = nonNil(check.Omitted)
	check.Findings = nonNil(check.Findings)
	return check, true, nil
}

// Delete is idempotent: removing a check that does not exist is not an
// error, the same rule DeleteEntry and DeleteWorkspace already keep.
func (r *skillCheckSqlite) Delete(ctx context.Context, name string) error {
	return r.s.run(ctx, func(ctx context.Context) error {
		if _, err := r.s.db.ExecContext(ctx, `DELETE FROM skill_checks WHERE name = ?`, name); err != nil {
			return fmt.Errorf("content: skill check: delete %q: %w", name, err)
		}
		return nil
	})
}
