package content

// THE MIGRATION LADDER (nocx-lmb6v.1).
//
// content.db used to be REBUILT whenever its stamp did not match this build's
// — every table dropped, every row gone, and the store said so because
// "your history was discarded" was a fact the user was entitled to. That was
// the right trade while the file was local and disposable: lose it, reopen
// your tabs. It stops being right the moment a server on a host owns the
// tabs, names and blocks that are the ONLY copy and are shared between a
// person's machines, because there is then nowhere to restore them from and a
// schema bump ships with an ordinary update (design D1, level 2).
//
// The protocol is three rows and nothing else is legal:
//
//	onDisk == current  → open
//	onDisk <  current  → one explicit ordered step per edge, each in one
//	                     crash-safe operation, user_version updated only
//	                     AFTER that edge commits
//	onDisk >  current  → REFUSE, without modifying a byte   (nocx-7qunp)
//
// THE INVARIANT, WITH BOTH ENDS NAMED (AGENTS.md rule 3). It opens when a
// step's first statement executes inside its transaction and it closes when
// that transaction's COMMIT returns. Across that span the file on disk still
// answers `user_version = step.from` and still holds exactly the rows of
// `from`; the COMMIT is the single event that makes it answer `to` with the
// rows of `to`. There is no third state to observe. Anything that ends the
// span other than that COMMIT — a failed statement, a cancelled context, a
// crash, a kill — ends it by rollback, which restores the opening state, and
// the next start re-enters the same span at the same `from`. That is why the
// stamp is written INSIDE the step's transaction and never beside it: a stamp
// that commits separately is a moment, and a moment is precisely the window
// in which a half-migrated file exists.
//
// It holds in the presence of a WAL, which is the form a live content.db
// actually takes — a small main file and a large hot `-wal`. An uncommitted
// transaction is uncommitted frames; SQLite's recovery on the next open ends
// the span exactly as a rollback does, and `PRAGMA user_version` is a write
// to page 1 like any other, so it travels with the frames rather than around
// them. TestAnEdgeThatFailsPartWayLeavesTheDatabaseWhollyAtTheVersionItStarted
// is the assertion; it injects the failure into the real edge, after that
// edge's own DDL has run.
//
// WHAT A STEP MUST DO, AND WHAT IT MUST NOT BOTHER WITH. Open applies
// `schemaV1` — every statement of it `IF NOT EXISTS` — immediately after this
// walk, so a new TABLE and a new INDEX arrive by themselves and a step that
// wrote them again would be a second copy of the same DDL, which is the
// duplicate-owner defect AGENTS.md spends a section on. A step exists for
// exactly what `IF NOT EXISTS` cannot express: a new COLUMN in a table that
// already exists, a changed CHECK or constraint (SQLite cannot ALTER one, so
// the table is rebuilt and its rows copied), a dropped or reshaped index, and
// any backfill of data.
//
// ONE COUNTER, FOR ONE FILE — THE GRANULARITY, DECIDED (nocx-lmb6v.3,
// nocx-lmb6v.5). content.db holds three subsystems' state: the ledger, the
// layout (tabs and panes) and the api runs. They share ONE `user_version`,
// and that is a decision rather than an accident, so here is the argument.
//
// THE UNIT OF THE DECISION IS THE FILE. `Open` is handed a path and must
// answer open / migrate / refuse once, for the whole of it, before it hands
// out anything. There is one file, one handle and one caller, so there is one
// answer; a version number finer than the thing being versioned has nowhere
// to put its extra resolution.
//
// A PER-SUBSYSTEM VERSION WOULD NEED A PER-SUBSYSTEM REFUSAL, AND NOTHING CAN
// EXPRESS ONE. Suppose the layout were stamped ahead of this build and the
// ledger were not. The only honest response is to refuse the file: this
// process cannot read the layout, and `ContentDB` is a single interface with
// all three repositories hanging off it (content.go), so there is no partial
// store to hand out. "Open the ledger half" would be a store reporting itself
// healthy while a third of it is missing — the silent degrade AGENTS.md names
// as its own defect class. A refusal that has to be total is a version that
// may as well be single.
//
// THE COST nocx-lmb6v.3 NAMES IS REAL, SMALL, AND FALLS ON THE RIGHT PERSON.
// A layout-only change bumps the number for all three; the rung it adds is a
// no-op for the ledger and the api runs, which costs its author one `apply`
// that does nothing to two of them and costs a reader nothing at all. The
// other half — "an older binary refuses over a bump that touched nothing it
// owns" — is not a cost but the CORRECT answer: that binary cannot know the
// new layout is compatible with its own reads, and guessing is precisely what
// this ladder exists to stop. Refusing on a stamp it does not recognise is
// the protocol working, not the protocol being coarse.
//
// AND THREE COUNTERS WOULD BE THREE LADDERS OVER ONE SET OF TABLES, with a
// walk that has to be ordered across them the moment a foreign key crosses a
// boundary (`grant_scopes` → `authority_grants` shows how ordinary that is).
// One ordered chain over one file is the smallest thing that expresses it.
//
// So: ONE counter, and it is `user_version`. Everything in the file is inside
// it, which is why the `api_run*` tables were folded in at 15→16 and their
// private `api_run_schema` counter retired — two numbers answering one
// question agree everywhere anyone looks, and on the day they disagree
// nothing says which one is right.
//
// WHAT IS DELIBERATELY NOT HERE. No `minMigratable` floor is declared: while
// the chain is contiguous there is nothing to declare, and the floor a person
// meets is simply the first rung — `schemaLadder[0].from`, derived rather
// than written down. It is owed only if old steps are ever retired. And the
// server-to-server wire compatibility floor is NOT this and must never be
// folded into `user_version`: a peer never opens the remote database, so what
// a peer must satisfy is a handshake range, not a schema stamp (design D2).

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/log"
)

// migrationStep is ONE edge: exactly one version to the next one. Its apply
// runs inside the transaction that also carries the stamp, so it may assume
// nothing about what committed before it beyond `from` being on disk, and it
// must not commit, roll back or open a transaction of its own.
//
// The final rung also pins the exact schemaV1 text that fresh installs receive.
// Keeping that digest on the rung, rather than beside schemaVersion, makes a
// schemaV1 edit require the new rung that carries existing files to the new
// shape; a standalone digest could be updated while the ladder stayed still.
//
// apply is a FIELD rather than a method on a named type because that is the
// seam a test injects a failure through: a ladder is an ordinary slice, so a
// test can hand `migrateSchema` a step that runs the real edge's DDL and then
// returns an error, which is the one shape a mid-edge crash takes from the
// database's point of view. No build tag, no hook, no exported test-only
// entry point.
type migrationStep struct {
	from         int
	to           int
	apply        func(ctx context.Context, tx *sql.Tx) error
	schemaDigest string
}

// schemaLadder is the chain, ascending, one edge per rung, ending at
// schemaVersion. Add a rung in the same commit that changes schemaV1 and
// bumps schemaVersion — TestTheLadderIsAContiguousChainEndingAtTheCurrentSchema
// fails if the three ever disagree.
//
// It starts at 14 and not at 1, and that is a statement rather than an
// oversight: 14 is the last shape released before migrations existed at all,
// and the versions below it were minted while the file was explicitly
// disposable — two branches even shipped the same number 15 for two different
// shapes before merging (see testdata/schema_v14.sql). A database stamped
// below the first rung is REFUSED, never rebuilt: rows this build cannot read
// are not this build's to destroy.
var schemaLadder = []migrationStep{
	{from: 14, to: 15, apply: migrateGrantScopeKinds14to15},
	{from: 15, to: 16, apply: migrateRetireTheAPIRunCounter15to16, schemaDigest: "4688f8fcbae121444ed4726726fc598737220fd4fd09bc428e3230c13cfe3cd9"},
}

// validateLadder validates the shipped ladder against the current schema.
func validateLadder(ladder []migrationStep) error {
	return validateLadderForSchema(ladder, schemaVersion, schemaV1)
}

// validateLadderForSchema is parameterized so the gate's paired positive can
// prove that a changed schema passes when a new final rung carries its digest.
// Production always calls validateLadder with the compiled-in schemaVersion and
// schemaV1.
func validateLadderForSchema(ladder []migrationStep, version int, schema string) error {
	for i, step := range ladder {
		if step.to != step.from+1 {
			return fmt.Errorf("content: migration ladder: step %d spans %d→%d; one edge is one version", i, step.from, step.to)
		}
		if step.apply == nil {
			return fmt.Errorf("content: migration ladder: step %d→%d has nothing to apply", step.from, step.to)
		}
		if i > 0 && step.from != ladder[i-1].to {
			return fmt.Errorf("content: migration ladder: a database at %d has no step; the chain jumps from %d to %d",
				ladder[i-1].to, ladder[i-1].to, step.from)
		}
	}
	if len(ladder) > 0 && ladder[len(ladder)-1].to != version {
		return fmt.Errorf("content: migration ladder ends at %d but this build creates schema %d",
			ladder[len(ladder)-1].to, version)
	}
	if len(ladder) > 0 {
		got := sha256.Sum256([]byte(schema))
		gotHex := hex.EncodeToString(got[:])
		if ladder[len(ladder)-1].schemaDigest != gotHex {
			return fmt.Errorf("content: migration ladder: schemaV1 changed without a migration rung; add a new rung and update its schema digest for schema %d (got %s, want %s)",
				version, gotHex, ladder[len(ladder)-1].schemaDigest)
		}
	}
	return nil
}

// schemaShapeDigests pins the complete SQLite catalog for the historical
// shapes this build migrates. The catalog includes every table and index
// definition, so a stamp cannot make a different set of columns or
// constraints look like the version it names.
var schemaShapeDigests = map[int]string{
	14: "761c706c342c6df9456353abe9e52b5151bd4c7c9c597d6be78bc5705f111b0c",
	15: "067063ecdc151e3d344a7a122f4e678c21dbabfad1f848a60cf3d46e9b702a3d",
}

func validateOnDiskSchemaShape(ctx context.Context, conn *sql.Conn, version int) error {
	expected, ok := schemaShapeDigests[version]
	if !ok {
		return nil
	}
	found, err := sqliteSchemaShapeDigest(ctx, conn)
	if err != nil {
		return fmt.Errorf("content: inspect schema %d shape: %w", version, err)
	}
	if found != expected {
		return fmt.Errorf("content: refusing database stamped schema %d: expected schema %d shape %s, found shape %s; stamp and contents disagree, and no rows were discarded",
			version, version, expected, found)
	}
	return nil
}

func sqliteSchemaShapeDigest(ctx context.Context, conn *sql.Conn) (string, error) {
	rows, err := conn.QueryContext(ctx, `SELECT type, name, tbl_name, COALESCE(sql, '')
		FROM sqlite_master
		WHERE name NOT LIKE 'sqlite_%'
		ORDER BY type, name`)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()

	var shape strings.Builder
	for rows.Next() {
		var typ, name, table, sqlText string
		if err := rows.Scan(&typ, &name, &table, &sqlText); err != nil {
			return "", err
		}
		shape.WriteString(typ)
		shape.WriteString(`\x00`)
		shape.WriteString(name)
		shape.WriteString(`\x00`)
		shape.WriteString(table)
		shape.WriteString(`\x00`)
		shape.WriteString(sqlText)
		shape.WriteString(`\x00`)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(shape.String()))
	return hex.EncodeToString(sum[:]), nil
}

// migrateSchema brings the file up to schemaVersion, or refuses it. It is the
// whole of the protocol above and the only place user_version is read for a
// decision.
func migrateSchema(ctx context.Context, conn *sql.Conn, ladder []migrationStep, logger log.Logger) error {
	if err := validateLadder(ladder); err != nil {
		return err
	}
	var onDisk int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&onDisk); err != nil {
		return fmt.Errorf("content: read schema version: %w", err)
	}
	if err := validateOnDiskSchemaShape(ctx, conn, onDisk); err != nil {
		return err
	}
	if onDisk == schemaVersion {
		return nil
	}
	// DIRECTION DECIDES (nocx-7qunp). An older binary's only legal answer to
	// a newer file is a visible refusal: migrations are one-way, so it can
	// neither read those rows nor roll them back, and rebuilding them was
	// the older binary DESTROYING what a newer one wrote. The unknown-table
	// check this used to sit beside could never catch it — it compared
	// NAMES, and a newer schema that changes COLUMNS inside familiar tables
	// presents a name set this build recognises completely.
	//
	// The message is the product's, not the log's: Open's error becomes the
	// `detail` of the history.status degrade at the composition root and the
	// Settings notice prints it, so it names the version to update TO rather
	// than merely reporting a mismatch.
	//
	// It promises the ROWS and not the bytes, deliberately. This function
	// touches nothing — the test asserts byte identity across a refusal —
	// but Open around it cannot promise that and no reordering of its
	// pragmas would change it: our store runs in WAL, so a file a newer nocx
	// has been using is a small main file plus a large `-wal`, and CLOSING
	// the handle checkpoints that WAL into the main file with no
	// journal_mode pragma of ours involved anywhere. Only never opening the
	// database at all could deliver byte identity. The checkpoint is
	// schema-blind page copying, so the honest promise — no row was
	// discarded and the stamp still names the build that wrote them — holds
	// on every path.
	if onDisk > schemaVersion {
		return fmt.Errorf("content: refusing to open a database written by schema %d: this build understands schema %d, and rebuilding it would discard rows it cannot read — update nocx to a build that understands schema %d; no rows were discarded",
			onDisk, schemaVersion, onDisk)
	}
	// THE SAME REFUSAL, FOR THE FILES THE STAMP CANNOT SPEAK FOR YET. Before
	// 16 the `api_run*` tables were versioned by a counter of their own, so a
	// file written then says "what shape am I in" twice and the two halves
	// are independent: a database stamped 14 or 15 can carry api-run tables
	// from any build at all. This is the second half of that answer, and it
	// is read HERE — beside the other refusals, before a single step runs —
	// because the old code asked it LAST, after the walk had migrated and
	// stamped the file, and a refusal that has already rewritten what it
	// refuses is not a refusal.
	//
	// It is not a second counter kept alive. It is the retirement's
	// precondition: 15→16 drops `api_run_schema`, and dropping a counter
	// whose value this build does not understand would erase the evidence
	// that the file is ahead. From 16 on no file carries the table and this
	// clause is inert, which is what makes it a rung's precondition rather
	// than a rule.
	if err := refuseAPIRunTablesFromANewerBuild(ctx, conn); err != nil {
		return err
	}
	// A file with no user tables is a CREATION, not a migration: version 0
	// with nothing in it is what a fresh install looks like, and Open stamps
	// it after schemaV1 runs. Anything else stamped below the ladder is a
	// database whose shape this build has no step for — a file written
	// before the chain began, or not a ContentDB at all. Refused, and told
	// what to do with it: moving it aside is the only action that is not
	// destruction, and this build must not be the process that decides the
	// fate of rows it cannot read.
	empty, err := hasNoUserTables(ctx, conn)
	if err != nil {
		return err
	}
	if empty {
		return nil
	}
	if len(ladder) == 0 {
		return fmt.Errorf("content: refusing to open a database written by schema %d: this build creates schema %d and carries no migration steps at all — move the file aside to start a fresh history; no rows were discarded",
			onDisk, schemaVersion)
	}
	if onDisk < ladder[0].from {
		return fmt.Errorf("content: refusing to open a database written by schema %d: this build creates schema %d and its migration chain starts at schema %d, so no step carries a schema %d file forward — move the file aside to start a fresh history; no rows were discarded",
			onDisk, schemaVersion, ladder[0].from, onDisk)
	}
	// FOREIGN KEYS OFF FOR THE WALK, and it is not a relaxation — it is
	// SQLite's own documented procedure for changing a table's shape
	// (create the new one, copy, drop, rename), which cannot be run with
	// enforcement on because the intermediate states legitimately violate
	// it. What replaces the enforcement is `PRAGMA foreign_key_check`
	// INSIDE each step's transaction, below: a step that leaves a dangling
	// reference fails and rolls back rather than committing a database the
	// engine would have refused.
	//
	// It is set OUTSIDE the transactions because `PRAGMA foreign_keys` is a
	// no-op inside one, and restored on every path — this is the store's one
	// connection (nocx-4p3l2) and everything after this depends on the ON
	// that Open set.
	if _, offErr := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); offErr != nil {
		return fmt.Errorf("content: suspend foreign keys for the migration: %w", offErr)
	}
	defer func() {
		if _, onErr := conn.ExecContext(ctx, "PRAGMA foreign_keys=ON"); onErr != nil && logger != nil {
			logger.Warn("content: foreign keys could not be restored after the migration", "error", onErr)
		}
	}()
	for _, step := range ladder {
		if step.from < onDisk {
			continue
		}
		if err := applyStep(ctx, conn, step); err != nil {
			return err
		}
		if logger != nil {
			logger.Info("content: schema migrated", "from", step.from, "to", step.to)
		}
	}
	return nil
}

// applyStep is the crash-safe operation the protocol names: ONE transaction
// carrying the edge's own work, the integrity check that stands in for the
// suspended foreign keys, and the stamp — in that order, committed together.
func applyStep(ctx context.Context, conn *sql.Conn, step migrationStep) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("content: begin migration %d→%d: %w", step.from, step.to, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := step.apply(ctx, tx); err != nil {
		return fmt.Errorf("content: migration %d→%d: %w", step.from, step.to, err)
	}
	if err := foreignKeysIntact(ctx, tx); err != nil {
		return fmt.Errorf("content: migration %d→%d: %w", step.from, step.to, err)
	}
	// THE STAMP, INSIDE THE STEP'S OWN TRANSACTION. `PRAGMA user_version` is
	// an ordinary write to page 1, so it commits and rolls back with the
	// statements above it; that is the entire mechanism behind the interval
	// at the top of this file.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", step.to)); err != nil {
		return fmt.Errorf("content: stamp schema %d: %w", step.to, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("content: commit migration %d→%d: %w", step.from, step.to, err)
	}
	return nil
}

// foreignKeysIntact is the compensation for the suspended enforcement: it
// reports the first violation the walk introduced, by table and rowid, so a
// step that copies rows badly fails loudly inside its own transaction instead
// of committing a database nothing can read consistently afterwards.
func foreignKeysIntact(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("foreign key check: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		var table, parent sql.NullString
		var rowid, fkid sql.NullInt64
		if scanErr := rows.Scan(&table, &rowid, &parent, &fkid); scanErr != nil {
			return fmt.Errorf("foreign key check reported a violation: %w", scanErr)
		}
		return fmt.Errorf("foreign key violation left in %q (row %d) pointing at %q",
			table.String, rowid.Int64, parent.String)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("foreign key check: %w", err)
	}
	return nil
}

// hasNoUserTables distinguishes a fresh file from a database written by some
// other shape. It is the one question the stamp cannot answer, because a
// brand-new file and a file from before the stamp existed both read 0.
func hasNoUserTables(ctx context.Context, conn *sql.Conn) (bool, error) {
	var n int
	err := conn.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&n)
	if err != nil {
		return false, fmt.Errorf("content: probe schema: %w", err)
	}
	return n == 0, nil
}

// migrateGrantScopeKinds14to15 widens grant_scopes.resource_kind to admit the
// two kinds schema 15 added — `content` and `workspace` (nocx-cxjej.1, a
// grant naming a note rather than every note).
//
// It is a TABLE REBUILD because the widening is a CHECK constraint and SQLite
// has no ALTER for one: the new table is created beside the old, the rows are
// copied column by column, the old is dropped and the new renamed. All four
// statements are in the caller's transaction, so the pair of tables is never
// a state anything else can observe. The rename leaves the stored DDL text
// quoted (`CREATE TABLE "grant_scopes"`), which is cosmetic — the shape,
// the constraint and the primary key are what a database is judged by, and
// the test judges them by what the table now accepts and still refuses.
//
// A file that reaches here without the table at all is not a real schema 14
// database, and the step is a no-op rather than an error: schemaV1 runs right
// after this walk and creates grant_scopes in the current shape, which is the
// same place a fresh install gets it from.
//
// THE DDL BELOW IS SCHEMA 15'S AND IS FROZEN AT IT. It duplicates schemaV1's
// grant_scopes today because 15 is the current version, and the two must be
// allowed to diverge the moment 16 exists: this statement is what a schema 14
// file becomes on its way through 15, not what the current build creates. A
// later edge that changes this table again writes its own rebuild, starting
// from the shape here. Editing this one to follow schemaV1 would skip every
// intermediate step's work — which is the one mistake a ladder exists to make
// impossible.
func migrateGrantScopeKinds14to15(ctx context.Context, tx *sql.Tx) error {
	var present int
	if err := tx.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name='grant_scopes'").Scan(&present); err != nil {
		return fmt.Errorf("probe grant_scopes: %w", err)
	}
	if present == 0 {
		return nil
	}
	statements := []string{
		`CREATE TABLE grant_scopes_migrating (
  grant_id      INTEGER NOT NULL REFERENCES authority_grants(id) ON DELETE CASCADE,
  resource_kind TEXT NOT NULL CHECK (resource_kind IN
                ('environment','session','path','credential','destination','tool','content','workspace')),
  resource_id   TEXT NOT NULL,
  PRIMARY KEY (grant_id, resource_kind, resource_id)
) STRICT`,
		`INSERT INTO grant_scopes_migrating (grant_id, resource_kind, resource_id)
			SELECT grant_id, resource_kind, resource_id FROM grant_scopes`,
		`DROP TABLE grant_scopes`,
		`ALTER TABLE grant_scopes_migrating RENAME TO grant_scopes`,
	}
	for i, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("widen grant_scopes.resource_kind, statement %d: %w", i+1, err)
		}
	}
	return nil
}

// apiRunCounterFinalVersion is the only value `api_run_schema.version` ever
// held. The api-run tables shipped once, at 1, and were never changed again
// before 15→16 folded them into this ladder — so a file claiming anything
// above it was written by a build ahead of this one, and a file at or below it
// is an ordinary released database.
const apiRunCounterFinalVersion = 1

// refuseAPIRunTablesFromANewerBuild is the `onDisk > current` refusal for the
// quarter of this file that used not to be covered by it (nocx-lmb6v.5).
//
// Its message is the ladder's rather than the feature's, deliberately: it
// names what to do about the file and promises the rows, because Open's error
// becomes the `detail` of the history.status degrade at the composition root
// and is what the Settings notice prints. The one it replaced said "version 2
// is newer than supported version 1" from inside a step that had already let
// the file be migrated and stamped.
//
// A file with no `api_run_schema` table has nothing to say here — either it is
// from 16 or later, where the table does not exist, or it is a fresh file, or
// it is old enough that the api runs never ran. All three are ordinary.
func refuseAPIRunTablesFromANewerBuild(ctx context.Context, conn *sql.Conn) error {
	var present int
	if err := conn.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name='api_run_schema'").Scan(&present); err != nil {
		return fmt.Errorf("content: probe the api-run schema counter: %w", err)
	}
	if present == 0 {
		return nil
	}
	var version int
	err := conn.QueryRowContext(ctx, "SELECT version FROM api_run_schema WHERE id = 1").Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("content: read the api-run schema counter: %w", err)
	}
	if version > apiRunCounterFinalVersion {
		return fmt.Errorf("content: refusing to open a database whose api-run tables were written by api-run schema %d: "+
			"this build understands api-run schema %d, and it cannot read a shape it has no step for — "+
			"update nocx to a build that understands it; no rows were discarded",
			version, apiRunCounterFinalVersion)
	}
	return nil
}

// migrateRetireTheAPIRunCounter15to16 is the fold itself: the `api_run*`
// tables stop being versioned by a private counter and become ordinary tables
// of this file, governed by `user_version` like everything else.
//
// The edge has nothing to do to the tables THEMSELVES — schema 16 wants them
// in exactly the shape the old third step created, and `schemaV1` now carries
// that DDL, so a fresh file gets them from there and a file that already has
// them keeps them untouched. All that changes is who answers for them, and the
// only physical trace of the old answer is the counter table, which is dropped
// here. Leaving it would be worse than untidy: nothing would own it, and an
// upgraded database would differ permanently from a fresh one in a way
// `schemaV1` could never repair, since `IF NOT EXISTS` cannot remove a table
// (nocx-lmb6v.7).
//
// `IF EXISTS` because both populations reach this rung: a released 15 database
// has the table, and a schema 14 file that has just come across the rung below
// this one may not — the old third step ran on every open, but a fixture, or a
// build that crashed before it, need not have one.
//
// Whether the counter's VALUE is one this build understands is decided before
// the walk starts (refuseAPIRunTablesFromANewerBuild), not here: a step that
// refused inside its own transaction would refuse correctly, but only after
// the rungs below it had already committed.
func migrateRetireTheAPIRunCounter15to16(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS api_run_schema`); err != nil {
		return fmt.Errorf("retire the api-run schema counter: %w", err)
	}
	return nil
}
