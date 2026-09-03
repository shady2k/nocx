-- THE API-RUN TABLES AS THE TWO-COUNTER BUILDS CREATED THEM, VERBATIM.
--
-- Lifted unmodified out of `migrateAPIRuns` (internal/content/api_run_sqlite.go)
-- as it stood at `const schemaVersion = 15` — the last shape written before
-- nocx-lmb6v.5 folded these tables into the migration ladder. It is the
-- fragment of a released database that no code creates any more, which is
-- exactly why a fixture has to carry it: `api_run_schema` is the second
-- version counter this build exists to retire, and a test cannot fabricate the
-- file it has to retire out of code that no longer writes it.
--
-- IT IS FROZEN, for the reason schema_v14.sql gives: a fixture edited to keep
-- up with the current schema stops being a fixture, because the migration
-- would then be tested against the shape it produces rather than the shape it
-- starts from.
--
-- The seeded version is left to the caller: 1 is what every released build
-- wrote, and a value above it is the file this build must REFUSE.
CREATE TABLE IF NOT EXISTS api_run_schema (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	version INTEGER NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS api_runs (
	id              INTEGER PRIMARY KEY,
	collection_path  TEXT NOT NULL,
	request_rel_path TEXT NOT NULL,
	repeated_from    INTEGER REFERENCES api_runs(id) ON DELETE SET NULL,
	method           TEXT NOT NULL,
	url              TEXT NOT NULL,
	outcome          TEXT NOT NULL CHECK (outcome IN ('pending','answered','failed','stopped')),
	request_spans    TEXT NOT NULL DEFAULT '[]',
	metadata         TEXT NOT NULL DEFAULT '{}',
	started_at       INTEGER NOT NULL,
	ended_at         INTEGER,
	logical_bytes    INTEGER NOT NULL DEFAULT 0 CHECK (logical_bytes >= 0)
) STRICT;
CREATE TABLE IF NOT EXISTS api_run_artifacts (
	id          INTEGER PRIMARY KEY,
	run_id      INTEGER NOT NULL REFERENCES api_runs(id) ON DELETE CASCADE,
	kind        TEXT NOT NULL CHECK (kind IN ('request','response-text','response-raw')),
	byte_len    INTEGER NOT NULL DEFAULT 0 CHECK (byte_len >= 0),
	chunk_count INTEGER NOT NULL DEFAULT 0 CHECK (chunk_count >= 0),
	UNIQUE (run_id, kind)
) STRICT;
CREATE TABLE IF NOT EXISTS api_run_artifact_chunks (
	artifact_id INTEGER NOT NULL REFERENCES api_run_artifacts(id) ON DELETE CASCADE,
	seq         INTEGER NOT NULL CHECK (seq >= 1),
	body        BLOB NOT NULL,
	PRIMARY KEY (artifact_id, seq)
) STRICT;
CREATE INDEX IF NOT EXISTS api_runs_by_request
	ON api_runs(collection_path, request_rel_path, started_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS api_run_artifacts_by_run
	ON api_run_artifacts(run_id, kind);
