-- SCHEMA 14, VERBATIM. This is `schemaV1` as it stood in the tree at
-- `const schemaVersion = 14` — the last released shape before the current
-- one — lifted unmodified so a test can fabricate a database a real build
-- wrote rather than one a test invented.
--
-- Both branches that bumped 14 to 15 (aea3e505 widening the grant_scopes
-- resource-kind CHECK, 6960ad30 adding the session_output pair) were cut from
-- the same 14, and their two copies of this text are byte-identical, so
-- "schema 14" names one shape and not two.
--
-- IT IS FROZEN. A fixture that is edited to keep up with the current schema
-- stops being a fixture: the migration it feeds would be tested against the
-- shape it produces instead of the shape it starts from. When schema 15 is
-- no longer the version below the current one, this file stays exactly as it
-- is and a schema_v15.sql joins it.
-- The layout chain (nocx-isoph.1, tabs-panes-and-blocks §3): workspace → tab
-- → pane. A workspace is FLAT — it has no column naming another workspace, so
-- nesting is unrepresentable rather than merely unused; depth comes from
-- lineage, which lives on the tab.
--
-- EVERY ID HERE IS MINTED BY THE FRONTEND AND IS UNTRUSTED (§7), so all three
-- tables carry a digest: the store's own hash of what the create asked for,
-- bound to the id (nocx-isoph.2). It is what tells a RETRY of a create — the
-- socket dropped and the answer was lost, which is why AD-9 exists — from an
-- id being reused for something else. The first replays and returns the row
-- that is already there; the second is refused. The client never sends it and
-- never sees it: a value the client could supply would bind nothing.
--
-- The default value is for the rows the BACKEND mints — the fallback
-- workspace the ledger ensures for a session nobody recorded, which no
-- frontend create ever asked for and which therefore matches no digest.
CREATE TABLE IF NOT EXISTS workspaces (
  id           TEXT PRIMARY KEY,           -- client-minted UUIDv7
  name         TEXT NOT NULL,
  colour       TEXT,                       -- NULL: the default workspace, and
                                           -- any row the backend minted
  position     INTEGER NOT NULL DEFAULT 0, -- switcher order
  created_at   INTEGER NOT NULL,           -- backend wall clock, display only
  payload      TEXT NOT NULL DEFAULT '{}', -- sparse extension only
  digest       TEXT NOT NULL DEFAULT ''    -- the create key's content binding
) STRICT;

-- A tab is the strip entry and what the user decorates (§4.5). What is here
-- is what the tab OWNS; the activity indicator, the attention indicator and
-- the label are computed from its panes and are deliberately absent — a
-- column for any of them would give one fact two owners, and they diverge the
-- first time a pane is dragged elsewhere.
--
-- parent_id is the LINEAGE edge and only that (§4.2): who spawned whom,
-- provenance, immutable, never set by hand, admitted by internal/lineage. The
-- display grouping — "A, B and C are shown together" — is the tab's other
-- edge; it is symmetric, has no host and therefore no row (§4.3), and it
-- arrives with drag (nocx-8m2x6). It must never be folded onto this column.
--
-- ON DELETE SET NULL on parent_id, matching artifacts.derived_from and for
-- the same reason: the link going null is the honest "provenance lost" state.
-- CASCADE would delete an independent tab the user still has open; RESTRICT
-- would make a tab that ever spawned another undeletable, and §4.4 removes
-- tabs automatically the moment their last pane leaves.
--
-- closed_at IS THE WINDOW (nocx-l21ib.4). NULL means the tab is in the
-- window; a timestamp means it left. A tab is never deleted, because
-- entries.pane_id is ON DELETE SET NULL and panes.tab_id is ON DELETE
-- CASCADE, so deleting one tab permanently unhooked every block its panes
-- had printed — an ordinary Cmd-W forgot a session's work. Every read that
-- feeds the window filters closed_at IS NULL, and that read IS the window
-- set.
--
-- workspace_id is therefore NULLABLE with ON DELETE SET NULL, which
-- workspaces being the ONE row still deleted forces: under the previous
-- CASCADE, deleting a workspace took its closed tabs and then their panes,
-- which is exactly what the marking exists to prevent. Same shape and same
-- reason as parent_id above — the link going null is the honest "the
-- container this row remembers is gone" state. The invariant that replaces
-- the NOT NULL is the CHECK at the foot of the table: an OPEN tab is always
-- in a workspace; a CLOSED tab may have outlived its own.
CREATE TABLE IF NOT EXISTS tabs (
  id           TEXT PRIMARY KEY,           -- client-minted UUIDv7: UNTRUSTED
  workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL,
  parent_id    TEXT REFERENCES tabs(id) ON DELETE SET NULL
               CHECK (parent_id IS NULL OR parent_id != id), -- no self-parent
  name         TEXT,                       -- NULL: nobody named it (§4.5)
  colour       TEXT,                       -- NULL: never decorated
  position     INTEGER NOT NULL DEFAULT 0, -- strip order
  pinned       INTEGER NOT NULL DEFAULT 0 CHECK (pinned IN (0,1)),
  layout       TEXT NOT NULL DEFAULT 'row' CHECK (layout IN ('row','column')),
  seen_at      INTEGER,                    -- the seen-mark; NULL = never seen
  closed_at    INTEGER,                    -- NULL: in the window
  digest       TEXT NOT NULL DEFAULT '',   -- the create key's content binding
  CHECK (closed_at IS NOT NULL OR workspace_id IS NOT NULL)
) STRICT;

-- A pane is the DURABLE IDENTITY (§5): it outlives its shell, its tab and the
-- application, and its blocks are found by its id after a restart.
--
-- PANES DO NOT NEST. tab_id is the pane's only edge, so a pane whose parent is
-- a pane cannot be written down at all. The cost is stated rather than hidden:
-- no asymmetric layouts, ever, until §5's decision is revisited deliberately.
--
-- size_share is the MEMBER's property; the direction is the SET's and lives on
-- the tab. That split is why the tab needed a row and the display group did
-- not.
--
-- closed_at is the tab's column one rung down and means the same thing: NULL
-- is "in the window", and a pane that leaves is marked rather than deleted so
-- the blocks anchored to it (entries.pane_id) keep their anchor. tab_id keeps
-- its CASCADE and it is now unreachable — a tab row is never deleted either —
-- which is why the mark has to be written on BOTH tables in one transaction
-- rather than left to the foreign key.
CREATE TABLE IF NOT EXISTS panes (
  id         TEXT PRIMARY KEY,             -- client-minted UUIDv7: UNTRUSTED
  tab_id     TEXT NOT NULL REFERENCES tabs(id) ON DELETE CASCADE,
  cwd        TEXT NOT NULL,
  kind       TEXT NOT NULL CHECK (kind IN ('local','ssh')),
  endpoint   TEXT,                         -- canonical user@host:port; NULL local
  size_share REAL NOT NULL DEFAULT 1.0 CHECK (size_share > 0),
  closed_at  INTEGER,                      -- NULL: in the window
  digest     TEXT NOT NULL DEFAULT ''      -- the create key's content binding
) STRICT;

CREATE TABLE IF NOT EXISTS sessions (
  id           TEXT PRIMARY KEY,           -- server-authoritative (AD-7)
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  started_at   INTEGER NOT NULL,
  ended_at     INTEGER,
  payload      TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE TABLE IF NOT EXISTS environments (
  id          TEXT PRIMARY KEY,            -- derived from facets, never from a session
  kind        TEXT NOT NULL CHECK (kind IN ('local','ssh','container','unknown')),
  endpoint    TEXT,                        -- canonical user@host:port; NULL for local
  profile_id  TEXT,
  first_seen  INTEGER NOT NULL,            -- backend wall clock
  payload     TEXT NOT NULL DEFAULT '{}'   -- identity facets (sparse extension)
) STRICT;

CREATE TABLE IF NOT EXISTS environment_observations (
  id             INTEGER PRIMARY KEY,      -- row identity an execution pins
  environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  version        INTEGER NOT NULL,         -- per-environment ascending
  observed_at    INTEGER NOT NULL,         -- backend wall clock
  confidence     TEXT NOT NULL DEFAULT '{}', -- JSON per-facet: asserted|derived|unknown
  criticality    TEXT NOT NULL CHECK (criticality IN ('routine','sensitive','critical')),
  payload        TEXT NOT NULL DEFAULT '{}', -- facet values: branch, containerId, privilege, …
  UNIQUE (environment_id, version)
) STRICT;

CREATE TABLE IF NOT EXISTS entries (
  id              TEXT PRIMARY KEY,        -- client-minted UUIDv7: UNTRUSTED idempotency key
  ingest_seq      INTEGER NOT NULL UNIQUE, -- backend monotonic; commit order, NOT causality
  client          TEXT NOT NULL,           -- binds the idempotency key to a client
  digest          TEXT NOT NULL,           -- payload digest binding the idempotency key
  environment_id  TEXT NOT NULL REFERENCES environments(id),
  -- THE TWO EDGES (design §6.1), and neither does the other's work.
  --
  -- pane_id is the ANCHOR: durable, frontend-minted, and what makes restore
  -- possible. A user works in a tab, so they expect to see what they did
  -- there — the session is a fact ABOUT a block, not its home. Nullable and
  -- ON DELETE SET NULL for the same reason session_id is: a closed pane is
  -- not restored, its blocks stay in recall (which is scoped by environment
  -- and directory, never by pane), and nothing is left pointing at a row
  -- that is gone. It was ABSENT until nocx-rtg0.28, which is why every
  -- command recorded before that produced a block nothing could re-attach.
  --
  -- session_id is PROVENANCE: which pipe it ran in, null once that pipe is
  -- gone. A session dies with the backend (D5), and Open is what makes that
  -- true of the rows as well — see dropDeadSessions.
  pane_id         TEXT REFERENCES panes(id) ON DELETE SET NULL,
  session_id      TEXT REFERENCES sessions(id) ON DELETE SET NULL,
  -- THE TREE (ADR-0040, amending ADR-0039). Everything drawn in the
  -- scrollback is an entry, and entries form ONE ordered tree: parent_id is
  -- containment and pos orders siblings. NULL parent is a top-level block,
  -- whose order stays ingest_seq — the design's total order (ADR-0019 §2) is
  -- unchanged and the tree does not replace it.
  --
  -- This is a COLUMN and no longer an edge. It was a caused-by row in
  -- edges carrying {pos, at}, and an edge cannot say "one parent" — the
  -- table would take a second one and the reader would have to pick. The
  -- database says it now. edges keeps the relations that genuinely are not
  -- a tree (rerun-of, supersedes, cites, in-span, references).
  --
  -- ON DELETE SET NULL, not CASCADE, for the reason pane_id and session_id
  -- above give in as many words: the container this row remembers is gone,
  -- the block is not, and it must not be left pointing at a row that is not
  -- there. A tool call whose turn was evicted is still a command that ran.
  --
  -- UNIQUE (parent_id, pos) is the seat, and it is the database's job
  -- rather than a writer's: two children at one position is a drawing order
  -- with two answers, and the store refuses it instead of picking. SQLite
  -- counts NULLs as distinct in a unique index, so every top-level block
  -- (NULL, NULL) coexists — the constraint binds siblings only.
  parent_id       TEXT REFERENCES entries(id) ON DELETE SET NULL,
  pos             INTEGER,
  cwd             TEXT NOT NULL,
  -- text is one run of assistant prose (ADR-0040): a thing that was
  -- PRINTED, not attempted. Its shape is declared by the CHECK at the foot
  -- of the table rather than left to convention, because the objection to
  -- prose living in a table built around intent → attempt → outcome is real.
  -- Left implicit it becomes "for text this column is NULL and that one does
  -- not apply", which is how a table rots.
  kind            TEXT NOT NULL CHECK (kind IN ('shell','ask','action','text','frame')),
  -- source is the IMMEDIATE subject that submitted the content or the
  -- intent this entry represents. Initiation is NOT transitive: the
  -- assistant was set going by a person, and the command the assistant ran
  -- was submitted by the assistant — if initiation chained, every row in
  -- the tree would be 'user' and the column would say nothing. Approval
  -- does not change it: a call the assistant proposed stays 'assistant'
  -- after a person allows it, because the person authorised somebody
  -- else's intent, they did not submit it. (No backticks in here: this DDL
  -- is a Go raw string literal, and one would end it.)
  source          TEXT NOT NULL CHECK (source IN ('user','assistant')),
  intent          TEXT NOT NULL,
  phase           TEXT NOT NULL CHECK (phase IN ('open','bound','closed')),
  status          TEXT NOT NULL CHECK (status IN ('pending','running','success','failure','interrupted','unknown')),
  submitted_at    INTEGER NOT NULL,        -- backend wall clock, display only
  -- The terminal facts, written by FinishExecution — see the header note on
  -- the two clocks (nocx-rtg0.23).
  started_at      INTEGER,                 -- renderer wall clock at submit
  ended_at        INTEGER,                 -- backend wall clock at the close
  duration_ms     INTEGER,                 -- measured by whoever ran the clock
  sensitivity     TEXT NOT NULL DEFAULT 'normal' CHECK (sensitivity IN ('normal','sensitive')),
  -- capture_key is the renderer's idempotency key for a FRAME capture
  -- (nocx-f4s5): the backend mints the frame entry's id, so the untrusted
  -- key gets its own column, unique where present — a replay of the same
  -- capture returns the original frame id, and two captures can never
  -- share a key. NULL for every non-frame entry.
  capture_key     TEXT,
  payload         TEXT NOT NULL DEFAULT '{}', -- kind payload, sparse extension only
  UNIQUE (parent_id, pos),
  -- The seat is the database's. SQLite counts NULLs as distinct in a unique
  -- index, so UNIQUE(parent_id, pos) constrains SIBLINGS only — a top-level
  -- block (parent_id NULL, pos NULL) never collides with another root. But
  -- it also does NOT constrain a root that holds a seat: SQLite counts
  -- (NULL, n) as distinct from every other row, so a root with a pos slips
  -- past the unique index and claims a seat nothing is ordered by (top-level
  -- order is ingest_seq, and roots hold no seat). That is a drawing order
  -- with a dead seat — the store refuses it.
  CHECK (parent_id IS NOT NULL OR pos IS NULL),
  -- The text shape, stated once and enforced by the engine: a run of prose
  -- sits INSIDE a block (parent_id, pos), says nothing about an intent
  -- (intent = ''), and has no execution to wait for or judge — it was
  -- printed, so it is born closed and successful. Every clause is refused
  -- separately; a row that satisfies four of the five is not a text block.
  CHECK (kind <> 'text' OR (
           parent_id IS NOT NULL AND pos IS NOT NULL AND
           intent = '' AND phase = 'closed' AND status = 'success'))
) STRICT;

-- What is left here is what is genuinely NOT a tree (ADR-0040). caused-by
-- is retired with its {pos, at} payload: containment is entries.parent_id
-- now, and the database guarantees the one parent an edge never could. These
-- five are relations between blocks that each already have a home.
CREATE TABLE IF NOT EXISTS edges (
  from_id TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  to_id   TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  rel     TEXT NOT NULL CHECK (rel IN ('rerun-of','supersedes','cites','in-span','references')),
  -- payload is the edge's sparse extension: for a references edge it is
  -- the region JSON (design §5 — references carry region coordinates).
  payload TEXT NOT NULL DEFAULT '{}',
  PRIMARY KEY (from_id, to_id, rel)
) STRICT;

CREATE TABLE IF NOT EXISTS executions (
  id                  INTEGER PRIMARY KEY,
  entry_id            TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  lane                TEXT,                -- agent lane; NULL for a human's shell
  attempt             INTEGER NOT NULL DEFAULT 1,
  environment_obs_id  INTEGER NOT NULL REFERENCES environment_observations(id),
  lease_deadline      INTEGER,             -- wall clock, renewable, bounded ceiling
  inactivity_deadline INTEGER,             -- silence is a different failure from slowness
  interactivity       TEXT NOT NULL DEFAULT 'none'
                      CHECK (interactivity IN ('none','stdin','tty','awaiting-takeover')),
  process_group       TEXT,
  started_at          INTEGER,
  ended_at            INTEGER,
  termination_reason  TEXT CHECK (termination_reason IN
                      ('completed','failed','timeout','transport-gone','user-killed','agent-declined','interrupted','inactivity','output-budget')),
  executor            TEXT,                -- executor identity
  -- state is the ASSISTANT RUN state the renderer draws (design §7):
  -- prepared | streaming | awaiting_approval | completed | cancelled |
  -- failed | interrupted. NULL on executions that are not agent runs (a
  -- frame capture), so the startup sweep — every non-terminal run becomes
  -- interrupted — never touches them.
  state               TEXT CHECK (state IN
                      ('prepared','streaming','awaiting_approval','completed','cancelled','failed','interrupted')),
  payload             TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE TABLE IF NOT EXISTS authority_grants (
  id           INTEGER PRIMARY KEY,
  execution_id INTEGER NOT NULL UNIQUE REFERENCES executions(id) ON DELETE CASCADE,
  version      INTEGER NOT NULL,
  issued_at    INTEGER NOT NULL,           -- backend wall clock
  expires_at   INTEGER NOT NULL,           -- expiring: a grant is not a toggle
  -- policy is the decision MATRIX as JSON (ADR-0020 §7 as amended
  -- 2026-08-16); the CHECK replaced the old preset enum, and the column
  -- stays SQLite's discipline in a weaker form: a grant whose policy is
  -- not even JSON cannot be recorded.
  policy       TEXT NOT NULL CHECK (json_valid(policy)),
  payload      TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE TABLE IF NOT EXISTS grant_scopes (
  grant_id      INTEGER NOT NULL REFERENCES authority_grants(id) ON DELETE CASCADE,
  resource_kind TEXT NOT NULL CHECK (resource_kind IN
                ('environment','session','path','credential','destination','tool')),
  resource_id   TEXT NOT NULL,
  PRIMARY KEY (grant_id, resource_kind, resource_id)
) STRICT;

CREATE TABLE IF NOT EXISTS grant_effects (
  grant_id INTEGER NOT NULL REFERENCES authority_grants(id) ON DELETE CASCADE,
  effect   TEXT NOT NULL CHECK (effect IN
            ('observe','mutate-reversible','mutate-destructive','privilege-change',
             'disclose','cross-boundary','delegate')),
  PRIMARY KEY (grant_id, effect)
) STRICT;

-- AN ARTIFACT BELONGS TO ITS BLOCK (ADR-0040). entry_id is the OWNER: it is
-- what a body is a body OF, and it is NOT NULL because a body with no block
-- is nothing a reader could ever draw. It cascades, so DeleteEntry still
-- takes the bodies with it and eviction still frees what it accounts for.
--
-- execution_id is now PROVENANCE and nullable: WHICH ATTEMPT produced this
-- body, when there was an attempt. A text block has a body and no attempt —
-- it was printed, not run — so the column is honestly empty there rather
-- than pointing at an execution invented to hold it. ON DELETE SET NULL for
-- the reason derived_from below and entries.pane_id above both give: the
-- link going null is the honest "provenance lost" state, and the body's own
-- home is entry_id.
--
-- This does NOT collapse the executions table into entries. An execution is an
-- ATTEMPT and there are several per entry by design (ADR-0020 decision 4:
-- an approved retry is attempt 2 of the same intent, never a new intent) —
-- which attempt printed a body is exactly what this column still answers.
CREATE TABLE IF NOT EXISTS artifacts (
  id              TEXT PRIMARY KEY,        -- client-minted UUIDv7
  entry_id        TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  execution_id    INTEGER REFERENCES executions(id) ON DELETE SET NULL,
  media_type      TEXT NOT NULL CHECK (media_type IN
                  ('application/vt','text/plain','text/markdown','application/json')),
  derived_from    TEXT REFERENCES artifacts(id) ON DELETE SET NULL,
  state           TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open','sealed')),
  byte_len        INTEGER NOT NULL DEFAULT 0, -- logical content bytes (question 6)
  pinned          INTEGER NOT NULL DEFAULT 0, -- eviction-exempt (question 4)
  truncated       TEXT CHECK (truncated IN ('cap','gap','suppressed')),
  capture_method  TEXT NOT NULL DEFAULT 'none'
                  CHECK (capture_method IN ('terminal-cells','raw-output','serialized-html','none')),
  capture_version INTEGER NOT NULL DEFAULT 1,
  terminal_cols   INTEGER,
  terminal_rows   INTEGER,
  stream          TEXT CHECK (stream IN ('stdout','stderr','combined')),
  byte_offset     INTEGER,                 -- capture provenance: stream position
  byte_end        INTEGER,
  encoding        TEXT NOT NULL DEFAULT 'utf-8',
  gaps            TEXT NOT NULL DEFAULT '[]', -- JSON [{start,end,reason}]
  payload         TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE TABLE IF NOT EXISTS artifact_chunks (
  artifact_id TEXT NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
  seq         INTEGER NOT NULL,
  body        BLOB NOT NULL,               -- append-only; never one BLOB
  PRIMARY KEY (artifact_id, seq)
) STRICT;

CREATE TABLE IF NOT EXISTS ledger_sequence (
  id   INTEGER PRIMARY KEY CHECK (id = 1), -- exactly one row
  next INTEGER NOT NULL
) STRICT;
INSERT INTO ledger_sequence (id, next) VALUES (1, 0)
  ON CONFLICT(id) DO NOTHING;  -- schemaV1 re-runs on every open; the seed must be idempotent.
                               -- next=0: the first Submit increments to ingest_seq 1.

-- The retention watermark (nocx-rtg0.12, design §5.4): what eviction removed
-- and how far this store's knowledge is now incomplete. It exists because
-- coverage CANNOT be computed from the rows that remain — once eviction has
-- deleted them there is nothing left to count, and MIN(ended_at) over the
-- survivors reports a partial store as a whole one. Written in the SAME
-- transaction as the deletion it accounts for; see retention.go for why this
-- is one accumulating row rather than a journal of passes.
CREATE TABLE IF NOT EXISTS retention_watermark (
  id              INTEGER PRIMARY KEY CHECK (id = 1), -- exactly one row
  evicted_count   INTEGER NOT NULL DEFAULT 0, -- entries EVER evicted; monotonic
  horizon         INTEGER,                    -- newest instant removed; complete only after it
  last_evicted_at INTEGER                     -- wall clock of the last pass that removed something
) STRICT;
INSERT INTO retention_watermark (id, evicted_count, horizon, last_evicted_at)
  VALUES (1, 0, NULL, NULL)
  ON CONFLICT(id) DO NOTHING;  -- idempotent for the same reason as the sequence seed above.
-- The layout chain is read by parent: a workspace's tabs in strip order, a
-- tab's panes. tabs_by_parent is what keeps ON DELETE SET NULL — and the
-- lineage walk — from scanning the strip.
CREATE INDEX IF NOT EXISTS tabs_by_workspace     ON tabs(workspace_id, position);
CREATE INDEX IF NOT EXISTS tabs_by_parent        ON tabs(parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS panes_by_tab          ON panes(tab_id);
CREATE INDEX IF NOT EXISTS entries_by_env        ON entries(environment_id, cwd, ingest_seq DESC);
CREATE INDEX IF NOT EXISTS entries_by_status     ON entries(status, ingest_seq DESC);
CREATE INDEX IF NOT EXISTS entries_open          ON entries(phase) WHERE phase != 'closed';
CREATE INDEX IF NOT EXISTS entries_by_session    ON entries(session_id);
-- Restore reads one pane's blocks, newest first, and that is the whole
-- access pattern the anchor exists for (design §8).
CREATE INDEX IF NOT EXISTS entries_by_pane       ON entries(pane_id, ingest_seq DESC) WHERE pane_id IS NOT NULL;
-- A block's children are read by parent in pos order, and there is
-- deliberately no index here for it: UNIQUE (parent_id, pos) on the table
-- already IS that index. A second one over the same two columns would cost
-- every insert and answer nothing the first cannot.
CREATE INDEX IF NOT EXISTS edges_by_to           ON edges(to_id);
CREATE INDEX IF NOT EXISTS executions_by_entry   ON executions(entry_id, attempt);
-- A block's bodies are read by the block: the owning column is what the
-- restore and the detail read reach for. artifacts_by_execution stays for
-- the provenance question ("what did THIS attempt print") and is partial,
-- because a text block's row has nothing to say to it.
CREATE INDEX IF NOT EXISTS artifacts_by_entry     ON artifacts(entry_id);
CREATE INDEX IF NOT EXISTS artifacts_by_execution ON artifacts(execution_id) WHERE execution_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS observations_by_env   ON environment_observations(environment_id, version DESC);
-- The frame idempotency replay check is an index lookup, never a scan: one
-- capture_key per frame (nocx-f4s5).
CREATE UNIQUE INDEX IF NOT EXISTS entries_capture_key ON entries(capture_key) WHERE capture_key IS NOT NULL;
