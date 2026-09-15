# The status screen, stage 1: every project's roadmap and live progress

- **Epic:** `nocx-ss9gh.1` (stage 1 of the milestone `nocx-ss9gh`)
- **Brainstorm:** `nocx-ss9gh.1.2`, with the owner, 2026-09-15. Every decision below was
  made there; the bead's comments carry the reasoning in the order it was reached.
- **Mockups:** [`2026-09-15-status-screen-mockups/`](2026-09-15-status-screen-mockups/README.md)
- **Stage 2** (agents on the screen, the live wave) is `nocx-ss9gh.2` and is not specified here,
  except where stage 1 must leave room for it (§10).

## 1. What a user can do that they could not

Open one tab in nocx and see, for every project they work on and on whichever host it
lives, where each milestone stands: its stages, what is done, what is not yet broken down,
what is stuck and what blocks it — and watch it change within seconds of a `br` write,
without reloading.

### DONE WHEN

One end-to-end check is green: a headless backend (`cmd/nocx-server`) opens a pane in a
temporary git repository that has a `br` backlog with a milestone and two stages; the
status screen lists that project with the milestone's stage progress; `br close` of a task
in that repository moves the progress and adds a "closed" event to the feed within one
poll interval, with no reload; clicking the stage opens its graph.

### Deliberately out

- Which agent works on a task, conversations, the wave timeline (stage 2, `nocx-ss9gh.2`).
- Editing the backlog from the screen. The screen only reads.
- "Origin has backlog changes this host has not pulled" (needs `git fetch` on the host; its
  own task).
- Hosts without the helper. The screen is not offered for them at all (owner's decision).
- Keeping the project list identical on both of the owner's laptops. That arrives with one
  backend on the VM (`nocx-xn63t.2`, related, not blocking) and is not built separately.
- A canvas. Rejected with the owner: no pan/zoom surface, no draggable nodes.

## 2. What this crosses, and what those documents already decided

- **AD-1** — one WebSocket, JSON-RPC control plane. The screen's methods and one
  notification are control-plane JSON-RPC; nothing new rides the data plane.
- **AD-2** — one Go codebase. Running `br` on a remote host is code compiled into the helper
  build, the same way git is; no second implementation.
- **AD-5 / ADR-0034** — the helper is installed by consent, per machine. The screen depends on
  it for remote hosts and never deploys it.
- **AD-6** — the backend never sniffs the byte stream. Project discovery reads the command
  ledger's recorded `cwd`, which comes from shell-integration facts, not from terminal bytes.
- **AD-8** — every module behind an interface, wired at one composition root. A new
  `tracker` port with one `br` adapter; behaviour varies by implementation, not by flags.
- **ADR-0011, ADR-0018, ADR-0055** — one encrypted `content.db`; schema changes migrate or
  refuse. The two small tables this adds (§6) follow ADR-0055.
- **ADR-0014 and `frontend/src/ui/README.md`** — the screen is built from the kit (Page,
  PageRail, TreeRow, CollectionView, ProgressBar, Badge, StatusCard, EmptyState, Tabs).
  The graph is the one genuinely new component; it goes into `ui/` with its own identity
  class, CSS file, test and README row.
- **`contracts/README.md`, AGENTS.md testing rule 5** — every result shape is a JSON Schema,
  checked on the Go struct and off the real socket.
- **The existing answer for remote git:** `internal/git` splits `spawn` (argv and parsing,
  linked only by code that runs git), `local`, `helper` (the client over the helper) and
  `registry` (which one serves a host). The tracker follows that shape rather than a second
  way of reaching a host.
- **The existing answer for "where the user worked":** the ledger in `internal/content`
  records host, environment and `cwd` for every command. Projects are derived from it (§5);
  no registration list is kept.
- **Change notification:** `notify.feed.changed` carries only a revision and the renderer
  re-reads. `status.changed` has the same shape.
- **`nocx-lg6r` (2026-07-26, won't do)** decided against a generated roadmap _file_ in the
  repository. This is a screen in the product, not a file; the decision is not reopened.

## 3. Vocabulary

The tracker port speaks beans' vocabulary (`hmans/beans`, `ValidParentTypes`):
**milestone → epic → feature → task / bug / chore**. In `br`:

| port                      | `br`                                                                                                                        |
| ------------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| milestone                 | an epic carrying the label `milestone` (decided 2026-09-15; `nocx-ss9gh.1.3` adds it to AGENTS.md and to the status script) |
| stage                     | a direct child epic of a milestone                                                                                          |
| epic                      | any other epic, at any depth                                                                                                |
| feature, task, bug, chore | the issue types of the same names                                                                                           |
| not decomposed            | an open epic whose non-deferred children are none — the size is unknown, never "0 of 0"                                     |

Epics without a milestone ancestor are counted per project as "outside any milestone". In
nocx today that is most of them, and the number is shown, not hidden.

## 4. Reading `br`

### 4.1 Commands, never files

The adapter runs `br` with `--json` on the host that holds the repository. It never opens
`br`'s database (its engine is `frankensqlite` with its own sidecars) and never parses
`.beads/issues.jsonl` itself.

| need                                          | command                                                                                                             |
| --------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| freshness and change marker                   | `br sync --status --json` (`jsonl_content_hash`, `last_export_time`, `dirty_count`, `db_newer`, `workspace_health`) |
| what changed since the last mark              | `br list --status all --sort updated_at --limit N --json`, newest first, until older than the mark                  |
| one issue: parent, typed relations, rollup    | `br show <id> --json`                                                                                               |
| a dependency graph                            | `br graph <id> [--dependencies] --json` (nodes and edges)                                                           |
| the whole backlog's components, once per load | `br graph --all --json`                                                                                             |
| stale claims                                  | `br coordination status --json` (per-claim classification and thresholds)                                           |
| blocked issues                                | `br blocked --json`                                                                                                 |
| milestone roots                               | `br list --label milestone --status all --json`                                                                     |

The set is closed in code, command and flags together. `br capabilities --json` classifies
`list`, `show`, `blocked`, `coordination` and `schema` as `read`, but `sync` and `graph` as
`mixed` (checked with br 0.5.10), so the class of a subcommand is not enough on its own. The
rule is instead: each entry is a command plus the flags that make it read-only
(`sync --status` is documented as "read-only"; `graph` without `--dot` only renders), and a
test runs the whole set in a scratch workspace and asserts that `br sync --status` reports
the same `jsonl_content_hash` and `dirty_count` before and after — no write happened. A
future edit cannot add a write without failing it.

Measured on the nocx backlog (3777 issues): `br sync --status --json` and a one-row
`br list` each take 0.09 s.

### 4.2 Contract checks

Every `br` response the adapter decodes is validated against `br schema <target>` in tests,
so a `br` upgrade that changes a shape fails a test rather than rendering an empty screen.
At runtime a response that does not decode is a visible tracker error (§8), never a partial
model.

### 4.3 Change detection

Per project location, a poll:

1. `br sync --status --json`. Hash unchanged → nothing.
2. Hash changed → `br list` by `updated_at` back to the previous mark; `br show` for those
   ids; `br graph` for the milestone they sit under if an open view shows it.
3. Compare the new id set with the previous one: an id that disappeared is a deletion. (A
   deleted issue is absent from `br list --status all`; it does change the hash.)
4. Build the next snapshot completely, then publish it as a new revision. A read that fails
   halfway publishes nothing.

Measured in a scratch `br` workspace, 2026-09-15: every write changes the hash — create,
dependency add and remove, comment, delete. Adding or removing a dependency bumps
`updated_at` of the dependent issue only, which is where the edge is stored, so step 2 sees
it. `dirty_count > 0` or `db_newer: true` means the export is behind the database; the data
is still shown, with that stated (§8).

Cadence: every 5 s per location while any client has read a `status.*` method in the last
60 s, every 60 s otherwise. The backend learns "a view is open" from those reads, not from a
second subscription call. One request at a time per location; a poll never overlaps the
previous one.

### 4.4 Where `br` runs

`internal/tracker` mirrors `internal/git`:

- `tracker` — the port: `Tracker` with `Status`, `Changed`, `Show`, `Graph`, `Milestones`,
  `Stale`, `Blocked`, in the §3 vocabulary.
- `tracker/brspawn` — argv for the closed command set and decoding of `br` JSON. Linked only
  by code that runs `br`: the local adapter and the helper build.
- `tracker/local` — runs `br` on this machine.
- `tracker/helper` — the client over the helper. It sends a named operation and typed
  arguments; the helper builds argv. The client never builds argv.
- `tracker/registry` — chooses local or helper for a location's host, as
  `internal/git/registry` does, and refuses a host with no helper.

The helper gains the `br` operations under the same rules as its git operations. A host
whose `br` is missing answers "no br", which is a result, not an error path.

## 5. Which projects appear

There is no list to maintain. Projects come from where the owner already works.

1. **Candidates** — distinct `(host, cwd)` pairs from the command ledger in `content.db`.
2. **Resolution** — for each pair, on that host (locally or through the helper): the git
   root, whether `.beads` exists, the normalized origin
   (`github.com/owner/repo`). Pairs on a host with no helper are skipped.
3. **A project** is an origin. A **location** is (host, repository path) for that origin. One
   project can have several locations.
4. **Source of a card** — the location on the coordinator's host when one is known (stage 2
   supplies that), otherwise the location worked on most recently. The card names its source
   host and lists the other locations.
5. **Divergence** — when two locations report different `jsonl_content_hash` values, the card
   says so and on which hosts. nocx never merges backlogs; `git pull` and
   `br sync --reconcile-additive` remain the agents' and the owner's.
6. **Forgetting** — a location is dropped only when its host confirms the repository is gone.
   A project is forgotten only when every location it was seen at is gone. An unreachable
   host or a missing helper never drops anything; the location stays, marked.
7. **Hiding** — the owner can hide a project; it stays hidden until shown again.
8. **Manual add** — "Add project" takes a host and a path, for a repository never opened
   through nocx. It is the same location record, marked as added by hand.

## 6. What nocx stores

In `content.db`, under ADR-0055:

- `status_locations` — origin, host, path, first seen, last seen, how found (ledger / manual),
  last confirmed state (present / gone / unreachable / no helper / no br).
- `status_hidden` — origin, hidden at.

Nothing about issues is stored. Snapshots and the event feed live in backend memory; after a
restart the first read rebuilds the snapshot and the feed starts again. `br` keeps no change
history, and the screen does not invent one.

## 7. The contract

Every method has a params and a result schema in `contracts/`; the renderer's types are
generated from them.

| method                                          | params                                                 | result                                                                                                                                                                                                                                        |
| ----------------------------------------------- | ------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `status.overview.read`                          | —                                                      | per visible project: origin, source location and its state, other locations, divergence, tracker health, milestones (title, tasks closed/total, stages done/total, not decomposed, in progress, stale), count outside any milestone, revision |
| `status.tree.read`                              | project, parent id (absent = the project's milestones) | the children of one node: id, title, type, status, rollup, not decomposed, blocked-by count, has children                                                                                                                                     |
| `status.graph.read`                             | project, node id                                       | the node's level: nodes (with their stage when outside the node's stage), `blocks` edges, the upstream chain of the requested node                                                                                                            |
| `status.events.read`                            | project (optional), after sequence                     | events: sequence, time, project, issue id and title, kind (created, taken, closed, reopened, deferred, deleted, blocked, unblocked)                                                                                                           |
| `status.projects.hide` / `status.projects.show` | origin                                                 | —                                                                                                                                                                                                                                             |
| `status.projects.add`                           | host, path                                             | the location, or why it was refused (no helper, not a git repository, no br)                                                                                                                                                                  |
| notification `status.changed`                   | —                                                      | project, revision                                                                                                                                                                                                                             |

`status.changed` carries no data; the renderer re-reads what it has open, as it does for
`notify.feed.changed`.

## 8. When something fails

Every external call has a failure path with a test and a paired "on a normal machine it
succeeds" test.

| what happened                                        | what the user sees                                                                                              |
| ---------------------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| `br` returns an error, or JSON that fails its schema | the card says "the tracker answered outside its contract" with the error; the last snapshot stays, marked stale |
| `dirty_count > 0` or `db_newer`                      | "the tracker's export is behind its database"; data shown                                                       |
| host unreachable                                     | the location stays: "host unreachable since HH:MM"; data stale; polling continues and recovers by itself        |
| helper gone from a host                              | the location stays: "no helper on this host"                                                                    |
| repository removed from one host                     | that location is dropped; the card uses the others                                                              |
| removed from every host                              | the project leaves the screen                                                                                   |
| found in the ledger, no `br` there                   | not shown                                                                                                       |
| a read fails halfway                                 | no new revision; the previous one stays                                                                         |

## 9. Security

- Read-only by construction: the closed command-and-flags set of §4.1, proven by running it
  against a scratch workspace whose `br sync --status` does not change.
- The helper builds argv from a named operation and typed arguments; issue ids never become
  shell text.
- The helper runs `br` only inside a repository path that came from the ledger or from the
  owner's manual add.
- Titles and descriptions are rendered as text, never as HTML.

## 10. The screen

Layout chosen by the owner from the mockups:

1. **Overview** — a card per project (round 1, variant 1): milestones with stage progress,
   in progress / stale / not decomposed, outside any milestone, recent events, source host
   and locations.
2. **Roadmap tree** (round 1, variant 2) — a project rail and an expandable tree, loaded one
   level at a time.
3. **Stage screen** (round 2, combined) — stages on the left, the stage graph in the centre
   ("follow the blocker", round 2, graph 1: one automatic left-to-right layout, one level,
   drill-down by click, breadcrumbs back, nodes outside the stage dashed and labelled with
   their stage, closed epics collapsed). The right column is where stage 2's live wave goes;
   in stage 1 it shows this stage's backlog events.

Dependency tiers (graph 2) and agent cards (timeline 3) are not built.

The graph layout algorithm is a dependency on a layout library chosen at planning time;
it must load within the CSP and must not bring a second component model into the Solid app.

## 11. Tests

1. **End to end** — the DONE WHEN check (§1), on the headless backend with a real `br`.
2. **Failure paths** — one per row of §8, each with its paired success test.
3. **Contracts** — `_DTOConformsToContract` and `_OverTheWireConformsToContract` for every
   method; `br` responses validated against `br schema`.
4. **Derivation** — table tests for milestone, stage, not decomposed, outside any milestone,
   blocked outside the stage, and event kinds, on backlogs built by running the real `br`
   in a temporary workspace, never on hand-written JSON.
5. **Discovery and forgetting** — two hosts with one origin: removing one location keeps the
   project; removing both forgets it; an unreachable host drops nothing.
6. **Read-only** — every entry of the command set runs against a scratch `br` workspace, and
   `jsonl_content_hash` and `dirty_count` from `br sync --status` are unchanged afterwards.
7. **Renderer** — through what a user reaches: the overview lists a project from its initial
   state, clicking a stage opens its graph, a new revision updates the card.

## 12. Filed alongside

- `nocx-ss9gh.1.3` — the `milestone` label in AGENTS.md and in the status script.
- `nocx-ss9gh.2.3` — what orchestration must record for the live wave.
- `nocx-ss9gh.1.1` — AGENTS.md states that workers never write the tracker.
- To file with the plan: "origin has backlog changes this host has not pulled".
