package content

// A MIGRATED DATABASE AND A FRESH ONE MUST HOLD THE SAME SCHEMA (nocx-lmb6v.7).
//
// This is the one defect the migration epic exists to prevent, and until this
// file existed nothing in the suite could see it.
//
// `Open` walks the ladder and then runs `schemaV1` over the top of it, with
// every statement `IF NOT EXISTS`. That is deliberate — a table or an index
// that is simply NEW arrives for free — but it has a consequence: `schemaV1`
// CANNOT fix up a table a step has already rebuilt, because the table exists
// and `IF NOT EXISTS` is a no-op against it. So the 14→15 step carries its own
// copy of the `grant_scopes` DDL, frozen at 15, and the step's own comment says
// the two copies must be allowed to diverge the moment a schema 16 exists.
//
// That divergence is correct. What is not correct — and what nothing else here
// can catch — is the two disagreeing AT THE SAME VERSION. Edit `schemaV1`'s
// definition of a table some step also rebuilds, forget to mirror it into that
// step's frozen DDL, and UPGRADED users get one table shape while FRESH
// installs get another, permanently, with every test in this package green.
// There is no event afterwards that would notice: the two populations simply
// diverge, and it is found by a user whose query fails on a column their
// colleague has.
//
// WHAT THE COMPARISON NORMALISES AWAY, deliberately and exhaustively — every
// one of these is a way two CREATE statements can differ while the database
// they produce is identical:
//
//   - IDENTIFIER QUOTING. This is the reason the bead exists. The step ends in
//     `ALTER TABLE grant_scopes_migrating RENAME TO grant_scopes`, and SQLite
//     rewrites the stored DDL as `CREATE TABLE "grant_scopes"` — quoted, where
//     `schemaV1` wrote it bare. A text compare fails on that alone, and a test
//     that fails on cosmetics is a test the next person deletes, taking the
//     real check with it.
//   - `IF NOT EXISTS`. `schemaV1` is defensive everywhere; a step's rebuild
//     never is. Whether a statement guards against re-running says nothing
//     about the table it produces.
//   - WHITESPACE AND SQL COMMENTS, outside string literals. Indentation and
//     line breaks are the author's, not the engine's.
//   - CASE, outside string literals. SQL keywords and SQLite identifiers are
//     both case-insensitive, so `CHECK` versus `check` is typography.
//   - THE ORDER OF sqlite_master ROWS, which is creation order and therefore
//     records the order the migration happened to run in, not the shape.
//
// WHAT IT DELIBERATELY DOES NOT NORMALISE AWAY — this is where the test's value
// lives, because a normaliser that folded any of these would pass while the two
// populations genuinely differed, which is worse than having no test:
//
//   - THE CONTENTS OF STRING LITERALS, including their case and their spacing.
//     The 14→15 edge exists to widen a CHECK from six allowed resource kinds to
//     eight; `'content'` and `'workspace'` are string literals, so an edge that
//     widened the CHECK to the wrong set of kinds is exactly what must be
//     caught. The scanner tracks quotes for this and for nothing else.
//   - COLUMN NAMES, TYPES, ORDER, NULLABILITY, DEFAULTS, and primary-key
//     membership and position.
//   - THE SET OF OBJECTS. A table, index, view or trigger present in one
//     database and missing from the other is a difference, and so is an
//     internal index SQLite created for a constraint — losing a PRIMARY KEY in
//     a rebuild is visible as a missing `sqlite_autoindex_*` row.
//   - INDEX uniqueness, partiality, column order, direction and collation.
//   - FOREIGN KEYS: parent table, both column lists, and the ON DELETE / ON
//     UPDATE actions.
//   - CHECK constraints, STRICT and WITHOUT ROWID, none of which any pragma
//     reports at all. These are why the normalised DDL is compared as well as
//     the pragmas, and they are precisely what the 14→15 step changes.
//
// The structured facts come first because they name the difference — "column 3
// of grant_scopes" reads better than two walls of DDL — and the normalised DDL
// is the fallback that catches what the pragmas cannot express.

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"

	"github.com/shady2k/nocx/internal/log"
)

// ── the two databases ─────────────────────────────────────────────────────

// aFreshDatabase is what a new install gets: `schemaV1` and nothing else. It
// is opened and closed through the shipped `Open`, so what it holds is what a
// person who installed nocx today would have on disk.
func aFreshDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "content.db")
	db, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("Open on an empty directory: %v — a fresh install must create the current schema", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the fresh database: %v", err)
	}
	return path
}

// anUpgradedDatabase is what a person who has been using nocx gets: a real
// released schema 14 file, carried across the edge by the shipped ladder,
// through the shipped `Open`.
func anUpgradedDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "content.db")
	aReleasedSchema14Database(t, path)
	db, err := Open(context.Background(), Config{
		Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("Open over a released schema 14 database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the migrated database: %v", err)
	}
	return path
}

// ── the headline ──────────────────────────────────────────────────────────

// THE UPGRADED DATABASE AND THE FRESH ONE ARE THE SAME SHAPE.
//
// This is the paired positive for everything below: on an ordinary database,
// with the shipped ladder and no divergence anywhere, the comparison succeeds.
// Without it the negatives underneath would be satisfied by a comparator that
// reports a difference between any two files at all.
func TestAnUpgradedDatabaseAndAFreshOneHoldTheSameSchema(t *testing.T) {
	fresh := aFreshDatabase(t)
	upgraded := anUpgradedDatabase(t)

	// AT THE SAME VERSION is the precondition, not the assertion: two files
	// stamped differently are allowed to differ, and comparing them would
	// prove nothing.
	if got := rawUserVersion(t, fresh); got != schemaVersion {
		t.Fatalf("the fresh database is stamped %d, want %d", got, schemaVersion)
	}
	if got := rawUserVersion(t, upgraded); got != schemaVersion {
		t.Fatalf("the migrated database is stamped %d, want %d", got, schemaVersion)
	}

	if diff := schemaDifference(t, fresh, upgraded); diff != "" {
		t.Fatalf("a database migrated to schema %d has a different shape from one created fresh at schema %d:\n%s\n\n"+
			"Both populations are running this build. `schemaV1` runs after the walk with every statement `IF NOT EXISTS`, "+
			"so it cannot fix up a table a step already rebuilt: whatever a step's frozen DDL wrote is what an upgraded "+
			"user keeps, forever, and no later start will notice. If `schemaV1` changed, the change belongs in a NEW rung "+
			"as well — never by editing an old step's frozen DDL, which would skip every intermediate shape.",
			schemaVersion, schemaVersion, diff)
	}
}

// ── and the comparison is not vacuous ─────────────────────────────────────

// THE COMPARISON GOES RED WHEN THE SHAPES GENUINELY DIFFER, in both of the ways
// it has to: one a pragma can see, and one only the DDL can.
//
// Without this, the test above passes just as well against a comparator that
// normalises everything into the empty string — which is the failure mode the
// bead warns about, and it is silent. Each half fabricates the exact mistake
// the epic exists to prevent: a step's frozen DDL that no longer agrees with
// `schemaV1`, run through the real `migrateSchema` and then, as `Open` does,
// with `schemaV1` executed over the top of it — where `IF NOT EXISTS` declines
// to repair anything, which is the whole defect.
func TestTheSchemaComparisonReportsADivergenceRatherThanNormalisingItAway(t *testing.T) {
	fresh := aFreshDatabase(t)

	for _, tc := range []struct {
		what      string
		rebuild   string
		mentions  []string
		invisible bool // true when no pragma can see it and only the DDL can
	}{
		{
			// A COLUMN THE OTHER POPULATION DOES NOT HAVE. `PRAGMA
			// table_xinfo` sees this one.
			what: "a step that leaves a column schemaV1 never declared",
			rebuild: `CREATE TABLE grant_scopes_migrating (
  grant_id      INTEGER NOT NULL REFERENCES authority_grants(id) ON DELETE CASCADE,
  resource_kind TEXT NOT NULL CHECK (resource_kind IN
                ('environment','session','path','credential','destination','tool','content','workspace')),
  resource_id   TEXT NOT NULL,
  stray_column  TEXT,
  PRIMARY KEY (grant_id, resource_kind, resource_id)
) STRICT`,
			mentions: []string{"grant_scopes", "stray_column"},
		},
		{
			// A CHECK THAT ADMITS A DIFFERENT SET OF VALUES. No pragma
			// reports a CHECK constraint at all, so this half is what proves
			// the normalised-DDL fallback is doing work — and it is the exact
			// shape of the 14→15 edge, so it is the mistake most likely to be
			// made for real.
			what: "a step whose CHECK admits a different set of kinds from schemaV1's",
			rebuild: `CREATE TABLE grant_scopes_migrating (
  grant_id      INTEGER NOT NULL REFERENCES authority_grants(id) ON DELETE CASCADE,
  resource_kind TEXT NOT NULL CHECK (resource_kind IN
                ('environment','session','path','credential','destination','tool','content')),
  resource_id   TEXT NOT NULL,
  PRIMARY KEY (grant_id, resource_kind, resource_id)
) STRICT`,
			mentions:  []string{"grant_scopes", "workspace"},
			invisible: true,
		},
	} {
		t.Run(tc.what, func(t *testing.T) {
			diverged := filepath.Join(t.TempDir(), "content.db")
			aReleasedSchema14Database(t, diverged)
			applyTheOpenSequence(t, diverged, []migrationStep{{
				from: 14, to: 15,
				apply: func(ctx context.Context, tx *sql.Tx) error {
					return rebuildGrantScopesAs(ctx, tx, tc.rebuild)
				},
			}})

			if got := rawUserVersion(t, diverged); got != schemaVersion {
				t.Fatalf("the diverged database is stamped %d, want %d — the fixture did not migrate", got, schemaVersion)
			}
			diff := schemaDifference(t, fresh, diverged)
			if diff == "" {
				t.Fatalf("%s produced no reported difference — the comparison normalises away the very thing it exists to catch", tc.what)
			}
			for _, want := range tc.mentions {
				if !strings.Contains(diff, want) {
					t.Fatalf("the reported difference does not mention %q, so it does not name what diverged:\n%s", want, diff)
				}
			}
			if tc.invisible {
				// The claim in this file's header, checked rather than
				// asserted in prose: the structured facts genuinely cannot
				// see a CHECK, so if the DDL fallback were dropped this case
				// would go silent.
				if pragmaOnly := structuralDifference(t, fresh, diverged); pragmaOnly != "" {
					t.Fatalf("a CHECK-only divergence was visible to the pragmas after all:\n%s\n"+
						"That is good news, but the header claims otherwise — fix the header.", pragmaOnly)
				}
			}
		})
	}
}

// applyTheOpenSequence reproduces, on a raw connection, the three schema steps
// `Open` takes and in its order: the walk first, because a step edits the shape
// the PREVIOUS version left; `schemaV1` over the top, because what a step
// deliberately does not write arrives from there; and `migrateAPIRuns`, which
// owns the `api_run*` tables on a version stamp of its own rather than through
// the ladder. Doing it here rather than calling `Open` is what lets a test hand
// the walk a ladder other than the shipped one — and it has to stay in step
// with `Open`, or the fabricated divergence would be the harness's rather than
// the ladder's.
func applyTheOpenSequence(t *testing.T, path string, ladder []migrationStep) {
	t.Helper()
	ctx := context.Background()
	conn, done := rawConn(t, path)
	defer done()
	if err := migrateSchema(ctx, conn, ladder, log.NewSlogAdapter(nil)); err != nil {
		t.Fatalf("migrate the fixture: %v", err)
	}
	if _, err := conn.ExecContext(ctx, schemaV1); err != nil {
		t.Fatalf("apply schemaV1 over the migrated fixture: %v", err)
	}
	if err := migrateAPIRuns(ctx, conn); err != nil {
		t.Fatalf("apply the api-run tables over the migrated fixture: %v", err)
	}
}

// rebuildGrantScopesAs is the 14→15 edge with its frozen CREATE swapped for
// the caller's — SQLite's documented four-statement rebuild, unchanged in
// every other respect, so what the test varies is only the shape the step
// leaves behind.
func rebuildGrantScopesAs(ctx context.Context, tx *sql.Tx, create string) error {
	for _, statement := range []string{
		create,
		`INSERT INTO grant_scopes_migrating (grant_id, resource_kind, resource_id)
			SELECT grant_id, resource_kind, resource_id FROM grant_scopes`,
		`DROP TABLE grant_scopes`,
		`ALTER TABLE grant_scopes_migrating RENAME TO grant_scopes`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("rebuild grant_scopes: %w", err)
		}
	}
	return nil
}

// ── the comparison ────────────────────────────────────────────────────────

// schemaDifference reports every way the two files disagree, or "" when they
// agree. It is everything: the objects, their structured shape, and their
// normalised DDL.
func schemaDifference(t *testing.T, fresh, migrated string) string {
	t.Helper()
	return compareFacts(schemaFacts(t, fresh, true), schemaFacts(t, migrated, true))
}

// structuralDifference is the same comparison with the normalised DDL left
// out — the pragmas alone. It exists for one assertion: that a CHECK
// constraint is invisible to it.
func structuralDifference(t *testing.T, fresh, migrated string) string {
	t.Helper()
	return compareFacts(schemaFacts(t, fresh, false), schemaFacts(t, migrated, false))
}

func compareFacts(fresh, migrated map[string]string) string {
	keys := map[string]struct{}{}
	for k := range fresh {
		keys[k] = struct{}{}
	}
	for k := range migrated {
		keys[k] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)

	var report []string
	for _, k := range ordered {
		a, inFresh := fresh[k]
		b, inMigrated := migrated[k]
		switch {
		case !inMigrated:
			report = append(report, fmt.Sprintf("  %s\n    fresh:    %s\n    migrated: (absent)", k, a))
		case !inFresh:
			report = append(report, fmt.Sprintf("  %s\n    fresh:    (absent)\n    migrated: %s", k, b))
		case a != b:
			report = append(report, fmt.Sprintf("  %s\n    fresh:    %s\n    migrated: %s", k, a, b))
		}
	}
	return strings.Join(report, "\n")
}

// schemaFacts is one database's shape as a map of named facts, so a difference
// can be reported by name instead of as two walls of text.
//
// Everything in `sqlite_master` is included, the internal `sqlite_*` objects as
// well: an index SQLite created for a PRIMARY KEY or a UNIQUE constraint is
// exactly what a rebuild that dropped the constraint would be missing, and its
// row is the cheapest way to see that.
func schemaFacts(t *testing.T, path string, includeDDL bool) map[string]string {
	t.Helper()
	ctx := context.Background()
	conn, done := rawConn(t, path)
	defer done()

	facts := map[string]string{}
	type object struct{ kind, name, ddl string }
	var objects []object

	rows, err := conn.QueryContext(ctx,
		`SELECT type, name, ifnull(sql, '') FROM sqlite_master ORDER BY type, name`)
	if err != nil {
		t.Fatalf("read sqlite_master of %s: %v", path, err)
	}
	for rows.Next() {
		var o object
		if scanErr := rows.Scan(&o.kind, &o.name, &o.ddl); scanErr != nil {
			_ = rows.Close()
			t.Fatalf("scan sqlite_master of %s: %v", path, scanErr)
		}
		objects = append(objects, o)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatalf("read sqlite_master of %s: %v", path, err)
	}
	_ = rows.Close()

	for _, o := range objects {
		key := fmt.Sprintf("%s %s", o.kind, o.name)
		// An internal index has no DDL of its own; its mere presence, under
		// the name SQLite derives from the constraint, is the fact.
		ddl := "(created by a constraint, not by a statement)"
		if o.ddl != "" {
			ddl = normaliseDDL(o.ddl)
		}
		if includeDDL {
			facts[key+" · ddl"] = ddl
		} else {
			facts[key+" · exists"] = "yes"
		}
		if o.kind != "table" {
			continue
		}
		for i, column := range rawFacts(t, conn, path,
			`SELECT cid || ' ' || name || ' ' || type ||
			        ' notnull=' || "notnull" ||
			        ' default=' || ifnull(quote(dflt_value), 'none') ||
			        ' pk=' || pk || ' hidden=' || hidden
			   FROM pragma_table_xinfo(?) ORDER BY cid`, o.name) {
			facts[fmt.Sprintf("%s · column %d", key, i)] = column
		}
		for _, index := range rawFacts(t, conn, path,
			`SELECT name || ' unique=' || "unique" || ' origin=' || origin || ' partial=' || partial
			   FROM pragma_index_list(?) ORDER BY name`, o.name) {
			name := strings.Fields(index)[0]
			facts[fmt.Sprintf("%s · index %s", key, name)] = index
			for i, part := range rawFacts(t, conn, path,
				`SELECT seqno || ' ' || ifnull(name, '(an expression)') ||
				        ' desc=' || desc || ' coll=' || coll || ' key=' || key
				   FROM pragma_index_xinfo(?) ORDER BY seqno`, name) {
				facts[fmt.Sprintf("%s · index %s column %d", key, name, i)] = part
			}
		}
		for i, fk := range rawFacts(t, conn, path,
			`SELECT id || ':' || seq || ' ' || "from" || ' -> ' || "table" || '.' || ifnull("to", '(its primary key)') ||
			        ' on_update=' || on_update || ' on_delete=' || on_delete || ' match=' || match
			   FROM pragma_foreign_key_list(?) ORDER BY id, seq`, o.name) {
			facts[fmt.Sprintf("%s · foreign key %d", key, i)] = fk
		}
	}
	return facts
}

// rawFacts runs one single-column query and returns its rows in order.
func rawFacts(t *testing.T, conn *sql.Conn, path, query string, args ...any) []string {
	t.Helper()
	rows, err := conn.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("query %q on %s: %v", query, path, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if scanErr := rows.Scan(&s); scanErr != nil {
			t.Fatalf("scan %q on %s: %v", query, path, scanErr)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query %q on %s: %v", query, path, err)
	}
	return out
}

// ── the normaliser ────────────────────────────────────────────────────────

// normaliseDDL folds away the five kinds of insignificant difference named in
// this file's header and NOTHING else. Its whole design is the single piece of
// state it carries: whether the scanner is inside a string literal. Outside
// one, case, quoting, comments and whitespace are noise; inside one, every
// byte is the value a CHECK constraint or a DEFAULT is made of, and is kept
// exactly as written.
func normaliseDDL(ddl string) string {
	var b strings.Builder
	rs := []rune(ddl)
	inLiteral := false
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if inLiteral {
			b.WriteRune(c)
			if c == '\'' {
				// '' is an escaped quote and does not end the literal.
				if i+1 < len(rs) && rs[i+1] == '\'' {
					b.WriteRune('\'')
					i++
					continue
				}
				inLiteral = false
			}
			continue
		}
		switch {
		case c == '\'':
			inLiteral = true
			b.WriteRune(c)
		case c == '-' && i+1 < len(rs) && rs[i+1] == '-':
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
			b.WriteRune(' ')
		case c == '/' && i+1 < len(rs) && rs[i+1] == '*':
			i += 2
			for i+1 < len(rs) && !(rs[i] == '*' && rs[i+1] == '/') {
				i++
			}
			i++
			b.WriteRune(' ')
		case c == '"' || c == '`' || c == '[' || c == ']':
			// Identifier quoting. `ALTER TABLE ... RENAME` re-emits the name
			// quoted; a hand-written CREATE does not. The identifier itself is
			// kept — only the delimiters go.
		case unicode.IsSpace(c):
			b.WriteRune(' ')
		default:
			b.WriteRune(unicode.ToLower(c))
		}
	}
	out := strings.TrimSpace(collapseSpaces(b.String()))
	out = strings.ReplaceAll(out, "if not exists ", "")
	return squeezeAroundPunctuation(out)
}

// collapseSpaces reduces runs of spaces to one, outside string literals only —
// two spaces inside a DEFAULT are part of the value.
func collapseSpaces(s string) string {
	var b strings.Builder
	inLiteral := false
	previousWasSpace := false
	for _, c := range s {
		if inLiteral {
			b.WriteRune(c)
			if c == '\'' {
				inLiteral = false
			}
			continue
		}
		if c == '\'' {
			inLiteral = true
			previousWasSpace = false
			b.WriteRune(c)
			continue
		}
		if c == ' ' {
			if !previousWasSpace {
				b.WriteRune(c)
			}
			previousWasSpace = true
			continue
		}
		previousWasSpace = false
		b.WriteRune(c)
	}
	return b.String()
}

// squeezeAroundPunctuation drops the spaces that only separate a token from a
// bracket or a comma, so `CHECK (x IN ('a'))` and `check(x in('a'))` are one
// string. Literals are skipped for the same reason as everywhere else.
func squeezeAroundPunctuation(s string) string {
	isPunctuation := func(c byte) bool {
		return c == '(' || c == ')' || c == ',' || c == ';'
	}
	var b strings.Builder
	rs := []rune(s)
	inLiteral := false
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if inLiteral {
			b.WriteRune(c)
			if c == '\'' {
				if i+1 < len(rs) && rs[i+1] == '\'' {
					b.WriteRune('\'')
					i++
					continue
				}
				inLiteral = false
			}
			continue
		}
		if c == '\'' {
			inLiteral = true
			b.WriteRune(c)
			continue
		}
		if c == ' ' {
			if i+1 < len(rs) && rs[i+1] < 128 && isPunctuation(byte(rs[i+1])) {
				continue
			}
			emitted := b.String()
			if len(emitted) > 0 && isPunctuation(emitted[len(emitted)-1]) {
				continue
			}
		}
		b.WriteRune(c)
	}
	return b.String()
}
