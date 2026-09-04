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
	"sort"
	"strings"

	"github.com/ncruces/go-sqlite3/driver"
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
	preflight    func(ctx context.Context, conn *sql.Conn) error
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
	{from: 15, to: 16, apply: migrateRetireTheAPIRunCounter15to16, preflight: refuseAPIRunTablesFromANewerBuild},
	{from: 16, to: 17, apply: migrateTerminationReasons16to17, schemaDigest: "a1f0aa167b6dfaae4fd0d2e374e3406b096ea658ded032b1f88874a40a7b35e9"},
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
	if len(ladder) == 0 {
		return errors.New("content: migration ladder: empty ladder; no rows were discarded")
	}
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
	if version >= 16 {
		found := false
		for _, step := range ladder {
			if step.from != 15 || step.to != 16 {
				continue
			}
			found = true
			if step.preflight == nil {
				return errors.New("content: migration ladder: 15→16 retirement rung has no api-run preflight")
			}
			break
		}
		if !found {
			return errors.New("content: migration ladder: 15→16 retirement rung is missing its api-run preflight")
		}
	}
	return nil
}

func validateOnDiskSchemaShapeFor(ctx context.Context, conn *sql.Conn, version, currentVersion int, ladder []migrationStep) error {
	expected, ok := schemaShapeDigests[version]
	var expectedObjects map[string]struct{}
	if !ok && version == currentVersion {
		var err error
		expected, expectedObjects, err = currentSchemaShape(ctx)
		if err != nil {
			return fmt.Errorf("content: inspect current schema shape: %w", err)
		}
		ok = true
	}
	if !ok {
		if len(ladder) > 0 && ladder[0].from >= schemaLadder[0].from {
			for _, step := range ladder {
				if step.from == version && step.from < currentVersion {
					return fmt.Errorf("content: migration ladder: no expected schema shape for migratable schema %d; add its pinned shape before migrating", version)
				}
			}
		}
		return nil
	}
	if expectedObjects == nil {
		expectedObjects = historicalSchemaObjectNames[version]
	}
	foundObjects, err := sqliteSchemaObjects(ctx, conn)
	if err != nil {
		return fmt.Errorf("content: inspect schema %d shape: %w", version, err)
	}
	found := schemaObjectNames(foundObjects)
	foundDigest := schemaObjectsDigest(foundObjects)
	if foundDigest == expected {
		return nil
	}
	difference := schemaShapeDifference(expectedObjects, found)
	if difference == "" {
		difference = "table/index definitions differ"
	}
	return fmt.Errorf("content: refusing database stamped schema %d: expected schema %d shape %s, found shape %s; %s; stamp and contents disagree, and no rows were discarded",
		version, version, expected, foundDigest, difference)
}

// schemaShapeDigests pins the complete SQLite catalog for the historical
// shapes this build migrates. The current shape is derived from schemaV1 at
// runtime so it has one source of truth rather than a second digest constant.
var schemaShapeDigests = map[int]string{
	14: "302e4e2479855b3aa0abdce4a9ecb0f3c5a8af7f06ad102f9a9049e6818fd4c2",
	15: "75eb0aea40034a9db5c8f19648215e638234a9e9f0031a5a7275e2d8af7c3ff4",
	16: "014fa0face729e1f650b37d3d9ac7abb2d68490c98c3c6c48d6d74490702687f",
}

var historicalSchemaObjectNames = map[int]map[string]struct{}{
	14: schema14ObjectNames(),
	15: schema15ObjectNames(),
	16: schema16ObjectNames(),
}

func schema14ObjectNames() map[string]struct{} {
	names := []string{
		"table:workspaces", "table:tabs", "table:panes", "table:sessions",
		"table:environments", "table:environment_observations", "table:entries",
		"table:edges", "table:executions", "table:authority_grants",
		"table:grant_scopes", "table:grant_effects", "table:artifacts",
		"table:artifact_chunks", "table:ledger_sequence",
		"table:retention_watermark", "table:api_run_schema", "table:api_runs",
		"table:api_run_artifacts", "table:api_run_artifact_chunks",
		"index:tabs_by_workspace", "index:tabs_by_parent", "index:panes_by_tab",
		"index:entries_by_env", "index:entries_by_status", "index:entries_open",
		"index:entries_by_session", "index:entries_by_pane", "index:edges_by_to",
		"index:executions_by_entry", "index:artifacts_by_entry",
		"index:artifacts_by_execution", "index:observations_by_env",
		"index:entries_capture_key", "index:api_runs_by_request",
		"index:api_run_artifacts_by_run",
		"index:sqlite_autoindex_api_run_artifact_chunks_1",
		"index:sqlite_autoindex_api_run_artifacts_1",
		"index:sqlite_autoindex_artifact_chunks_1",
		"index:sqlite_autoindex_artifacts_1",
		"index:sqlite_autoindex_authority_grants_1",
		"index:sqlite_autoindex_edges_1",
		"index:sqlite_autoindex_entries_1",
		"index:sqlite_autoindex_entries_2",
		"index:sqlite_autoindex_entries_3",
		"index:sqlite_autoindex_environment_observations_1",
		"index:sqlite_autoindex_environments_1",
		"index:sqlite_autoindex_grant_effects_1",
		"index:sqlite_autoindex_grant_scopes_1",
		"index:sqlite_autoindex_panes_1",
		"index:sqlite_autoindex_sessions_1",
		"index:sqlite_autoindex_tabs_1",
		"index:sqlite_autoindex_workspaces_1",
	}
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		result[name] = struct{}{}
	}
	return result
}

func schema15ObjectNames() map[string]struct{} {
	result := schema14ObjectNames()
	result["table:session_output"] = struct{}{}
	result["table:session_output_chunks"] = struct{}{}
	result["index:sqlite_autoindex_session_output_1"] = struct{}{}
	result["index:sqlite_autoindex_session_output_chunks_1"] = struct{}{}
	return result
}

// Schema 16 is 15 without the api-run counter: the 15→16 rung retired the
// private `api_run_schema` table, and the `api_run*` tables it used to version
// became ordinary tables of this file. Nothing else moved, which is why the
// two lists differ by one name.
func schema16ObjectNames() map[string]struct{} {
	result := schema15ObjectNames()
	delete(result, "table:api_run_schema")
	return result
}

type sqliteSchemaObject struct {
	typ, name, table, sql string
}

func currentSchemaShape(ctx context.Context) (string, map[string]struct{}, error) {
	db, err := driver.Open(":memory:")
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = conn.Close() }()
	if _, execErr := conn.ExecContext(ctx, schemaV1); execErr != nil {
		return "", nil, execErr
	}
	objects, err := sqliteSchemaObjects(ctx, conn)
	if err != nil {
		return "", nil, err
	}
	return schemaObjectsDigest(objects), schemaObjectNames(objects), nil
}

// Tables and indexes are the schema objects this ladder owns. Triggers are
// runtime behavior, not part of schemaV1, so they are deliberately excluded.
func sqliteSchemaObjects(ctx context.Context, conn *sql.Conn) ([]sqliteSchemaObject, error) {
	rows, err := conn.QueryContext(ctx, `SELECT type, name, tbl_name, COALESCE(sql, '')
		FROM sqlite_master
		WHERE type IN ('table', 'index')
		ORDER BY type, name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var objects []sqliteSchemaObject
	for rows.Next() {
		var object sqliteSchemaObject
		if err := rows.Scan(&object.typ, &object.name, &object.table, &object.sql); err != nil {
			return nil, err
		}
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return objects, nil
}

func schemaObjectNames(objects []sqliteSchemaObject) map[string]struct{} {
	names := make(map[string]struct{}, len(objects))
	for _, object := range objects {
		names[object.typ+":"+object.name] = struct{}{}
	}
	return names
}

// The DDL is NORMALISED before it is digested, and schema_shape_normalise.go
// carries the argument: a table rebuild re-emits the table's name quoted, so a
// verbatim digest refused every database that had ever been migrated on its
// next open. What normalisation folds away cannot change the database the
// statement produces; everything a shape is judged by survives it.
func schemaObjectsDigest(objects []sqliteSchemaObject) string {
	var shape strings.Builder
	for _, object := range objects {
		shape.WriteString(object.typ)
		shape.WriteString(`\x00`)
		shape.WriteString(object.name)
		shape.WriteString(`\x00`)
		shape.WriteString(object.table)
		shape.WriteString(`\x00`)
		shape.WriteString(normaliseDDL(object.sql))
		shape.WriteString(`\x00`)
	}
	sum := sha256.Sum256([]byte(shape.String()))
	return hex.EncodeToString(sum[:])
}

func schemaShapeDifference(expected, found map[string]struct{}) string {
	var missing, extra []string
	for name := range expected {
		if _, ok := found[name]; !ok {
			missing = append(missing, schemaObjectLabel(name))
		}
	}
	for name := range found {
		if _, ok := expected[name]; !ok {
			extra = append(extra, schemaObjectLabel(name))
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	var differences []string
	if len(missing) > 0 {
		differences = append(differences, "missing "+strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		differences = append(differences, "extra "+strings.Join(extra, ", "))
	}
	return strings.Join(differences, "; ")
}

func schemaObjectLabel(key string) string {
	kind, name, ok := strings.Cut(key, ":")
	if !ok {
		return fmt.Sprintf("%q", key)
	}
	return fmt.Sprintf("%s %q", kind, name)
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
	if err := validateOnDiskSchemaShapeFor(ctx, conn, onDisk, schemaVersion, ladder); err != nil {
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
	// A rung's preflight is evaluated before any pending rung applies. This
	// keeps refusal clauses attached to the edge whose old shape they protect,
	// while still checking a file at 14 before the 14→15 transaction starts.
	for _, step := range ladder {
		if step.from < onDisk || step.preflight == nil {
			continue
		}
		if err := step.preflight(ctx, conn); err != nil {
			return err
		}
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

// migrateTerminationReasons16to17 widens executions.termination_reason to
// admit `answer-revoked` — the reason a run gets when a person takes back a
// standing answer and chooses to stop the work running under it
// (nocx-4yjwk.7).
//
// WHY THIS EXISTS AT ALL, and it is the general lesson rather than this
// column's. The vocabulary is closed by the DATABASE and not only by the Go
// constants, and the two halves are not wired to each other by the compiler.
// A reason added to `content.TerminationReason` and not here does not fail
// where it was written: it fails at the terminal close of a real run, where
// terminalize logs a warning and returns, the run never reaches a terminal
// state, no `agent.runState` is sent, and the startup sweep repairs it as
// `interrupted` at the next start. The person watching sees a run that streams
// forever. TestEveryTerminationReasonGoCanNameTheDatabaseAccepts is what turns
// that into a failing test in the commit that adds the constant.
//
// It is a TABLE REBUILD for the reason 14→15 was: the widening is a CHECK and
// SQLite has no ALTER for one. SQLite's documented four statements — create
// beside, copy, drop, rename — all inside the caller's transaction, so the pair
// of tables is never a state anything else can observe, and `foreign_key_check`
// runs over the result before the stamp commits.
//
// THE PARTIAL FAILURES, ENUMERATED, because a rebuild is four statements and
// the interval has to hold across all of them. Statement 1 fails: nothing has
// changed. Statement 2 fails — a row the new CHECK refuses, or the disk fills
// mid-copy — the extra table exists but is uncommitted. Statement 3 fails: both
// tables exist, uncommitted. Statement 4 fails: `executions` is GONE and only
// the new table exists, uncommitted. Every one of those ends the same way,
// because none of them is committed and `applyStep` rolls the transaction back:
// the file still answers `user_version = 16` and still holds schema 16's rows,
// including the executions the copy had begun to duplicate. The next start
// finds a database at 16, walks this rung again from the beginning, and there
// is no repair step and nothing for a person to do. That is the whole reason
// the stamp is written inside this transaction rather than beside it.
//
// The dependents are unaffected by the drop: `authority_grants.execution_id`
// and `artifacts.execution_id` reference `executions(id)`, the ids are copied
// unchanged, and foreign keys are suspended for the walk with
// `PRAGMA foreign_key_check` inside the transaction standing in for them — so a
// rebuild that lost a row fails here instead of committing a database nothing
// can read consistently.
//
// A file that reaches here without the table is not a real schema 16 database
// and the step is a no-op, exactly as 14→15 is: `schemaV1` runs right after the
// walk and creates `executions` in the current shape, which is where a fresh
// install gets it from too.
//
// THE DDL BELOW IS SCHEMA 17'S AND IS FROZEN AT IT. It duplicates schemaV1's
// `executions` today because 17 is the current version, and the two must be
// allowed to diverge the moment 18 exists — this statement is what a schema 16
// file BECOMES on its way through 17, not what the current build creates.
// TestAnUpgradedDatabaseAndAFreshOneHoldTheSameSchema is what keeps them equal
// while they are supposed to be equal.
func migrateTerminationReasons16to17(ctx context.Context, tx *sql.Tx) error {
	var present int
	if err := tx.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name='executions'").Scan(&present); err != nil {
		return fmt.Errorf("probe executions: %w", err)
	}
	if present == 0 {
		return nil
	}
	statements := []string{
		`CREATE TABLE executions_migrating (
  id                  INTEGER PRIMARY KEY,
  entry_id            TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  lane                TEXT,
  attempt             INTEGER NOT NULL DEFAULT 1,
  environment_obs_id  INTEGER NOT NULL REFERENCES environment_observations(id),
  lease_deadline      INTEGER,
  inactivity_deadline INTEGER,
  interactivity       TEXT NOT NULL DEFAULT 'none'
                      CHECK (interactivity IN ('none','stdin','tty','awaiting-takeover')),
  process_group       TEXT,
  started_at          INTEGER,
  ended_at            INTEGER,
  termination_reason  TEXT CHECK (termination_reason IN
                      ('completed','failed','timeout','transport-gone','user-killed','agent-declined','interrupted','inactivity','output-budget','answer-revoked')),
  executor            TEXT,
  state               TEXT CHECK (state IN
                      ('prepared','streaming','awaiting_approval','completed','cancelled','failed','interrupted')),
  payload             TEXT NOT NULL DEFAULT '{}'
) STRICT`,
		`INSERT INTO executions_migrating
			(id, entry_id, lane, attempt, environment_obs_id, lease_deadline, inactivity_deadline,
			 interactivity, process_group, started_at, ended_at, termination_reason, executor, state, payload)
			SELECT id, entry_id, lane, attempt, environment_obs_id, lease_deadline, inactivity_deadline,
			 interactivity, process_group, started_at, ended_at, termination_reason, executor, state, payload
			FROM executions`,
		`DROP TABLE executions`,
		`ALTER TABLE executions_migrating RENAME TO executions`,
		// The rebuild drops the table and takes its indexes with it. schemaV1
		// re-creates this one after the walk, but only because it is `IF NOT
		// EXISTS` against a table that no longer has it — recreating it here
		// keeps the shape whole INSIDE the transaction, so `foreign_key_check`
		// and the stamp commit over a database that is already schema 17 and
		// not one waiting for schemaV1 to finish it.
		`CREATE INDEX IF NOT EXISTS executions_by_entry ON executions(entry_id, attempt)`,
	}
	for i, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("widen executions.termination_reason, statement %d: %w", i+1, err)
		}
	}
	return nil
}
