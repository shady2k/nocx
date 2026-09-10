# AGENTS.md — Working rules for AI agents on `nocx`

`nocx` is a local-first, Warp-style terminal (Go backend + xterm.js frontend + Wails v3
desktop). This file is the operating contract for **any** AI agent contributing to the
repo. Read it before writing code.

Every rule here was bought by a failure. The failures themselves are not repeated here —
they are in `git log` for this file, where they cost nothing to carry. What is here is the
rule and the reason it is not a preference.

## Read first

- [`docs/vision.md`](docs/vision.md) — what we're building, MVP scope, roadmap.
- [`docs/architecture.md`](docs/architecture.md) — the spine: invariants `AD-1`…`AD-10`,
  module boundaries, the WebSocket protocol. **The ADs are binding.**
- [`frontend/src/ui/README.md`](frontend/src/ui/README.md) — before any UI element.
- [README setup](README.md#agent-tooling) — the toolchain and the agent tooling.
  `make init` installs none of it.

The backlog lives in **beads** (`br`), not in prose.

**Fresh clone:** install the tooling, then `make init`. The backlog comes with the clone —
`.beads/issues.jsonl` is tracked — and `make init` only builds the SQLite from it.

## Language

**Every document in this repository is written in English.** Code and its comments, this
file, `README.md`, `docs/`, `.internal/` specs, plans and briefs, commit messages, bead
titles and bodies, and anything else a second person reads. The repository has one working
language, and a file that switches is a file half the team skims instead of reads.

**Speak to the developer in the language they used to address you.** A conversation has
exactly one reader and it is theirs. Answering a Russian question in English to satisfy the
rule above is a misreading of it.

The distinction is audience, not formality. If it is committed, it is English. If it is
said to the person in front of you, it is their language.

## Repository layout

- `docs/` — `vision.md`, `architecture.md`, `decisions/` (ADRs).
- `contracts/` — one JSON Schema per JSON-RPC result shape.
- `AGENTS.md` — this file. `CLAUDE.md` only points here.
- Code directories follow the module map in `docs/architecture.md`.

## The tracker: `br`

`br` is a single binary over SQLite plus a tracked JSONL export, and **`br` never runs git
— that is the design, not an oversight.** Nothing commits, pushes or pulls the backlog for
you, and no hook stages it. A `git pull` that changes the JSONL is imported by the next
`br` command on its own. Sending yours out is two lines you type:

```bash
br sync --flush-only          # db -> .beads/issues.jsonl
git add .beads/issues.jsonl   # and commit it with the code
```

`.githooks/pre-push` warns when you are about to push code and leave the backlog behind. It
warns and never blocks, for the same reason the gates elsewhere in this file are kept worth
passing: a gate people learn to skip with `--no-verify` protects nothing.

`br` resolves the database of the **main checkout** even when run from a worktree that has
its own `.beads/` in the tree. So a feature branch cannot touch the JSONL, and two branches
cannot conflict on it. Between machines they still can — `br sync --reconcile-additive`
resolves that without deleting anything. If your database and a pulled JSONL both changed,
`br sync --merge` does the three-way; `--force-db`, `--force-jsonl` and `--force` are the
explicit policies.

**Publish every backlog write immediately** — a create, an edit, an edge, a close — not at
session close. An unpushed bead does not exist for anybody else, and the afternoon it costs
is somebody else's. Batch your writes if you like (`br update` and `br close` take several
ids), then commit at the end of the batch.

**A claim is not a lock.** Two clones can claim the same bead; last write wins. The
protocol shrinks the race, it does not close it.

**There is no merge slot**, deliberately. What it was for still exists: two agents
integrating at once resolve conflicts against a `main` moving underneath both, and each
resolution invalidates the other's. A lock never protected against that either. So
integrate one at a time by arrangement, and if that costs something, file the bead saying
what it cost rather than reviving the slot.

**TodoWrite, TaskCreate and markdown TODO lists are forbidden.** `br` is the tracker for
all work, including your own checklists.

## Recall: `deja`, not the tracker

`br` has no memory store, and neither does this repository any more. What every agent here
already produces is a session transcript on disk, and `deja` indexes those — claude, omp,
codex and pi in one machine-wide index at `~/.cache/deja`. Reading it is pull-based:

```bash
deja "<what you are about to do>"          # across every agent, every worktree
deja --harness omp --since 30d "<query>"   # narrow it
deja fix "<error text>"                    # what was run after this error before
deja how "<what>"                          # commands this machine actually ran
```

**The recall path writes nothing, so nothing conflicts.** Six workers in six worktrees
produce six transcripts and one reader sees all of them. This is the whole reason it
replaced a git-tracked playbook: a rule committed on a branch reaches only that branch, and
by measurement it reached 3 worktrees out of 45.

**The mechanism is shared, the database is not.** That is the owner's decision, and it
settles what everything below is for. Everyone working in this repository — maintainers and
contributors alike — sets deja up the same way, because the setup is part of the contract
and lives in git: the install line, the hooks, the trust policy. What each of them then
accumulates is theirs. Nobody's index or notes reach anybody else, there is no team store
and no shared memory to keep in step, and a clone arrives with the instructions and an
empty index that fills from that machine's own history.

The consequence is worth stating plainly, because it is the whole reason this file still
exists: **nothing you write into deja can be relied on by a colleague.** Recall is how you
stop re-deriving what you personally already worked out. A rule the next person must follow
is not that; it goes in this file, where a pull request makes somebody read it.

**Writing to it.** `remember` is the sixth MCP tool, alongside recall, context, blame, fix
and how. It appends one line to `~/.local/share/deja/notes.jsonl` — timestamp, project,
text, tags — searchable at the next index pass, and then surfacing like any other session,
scoped to its project. Use it as you see fit: it is your database. Two properties to know
rather than obey. A note reaches another machine only if you run `deja sync` yourself,
which is your machines and not the team's. And with the hooks on, a note is injected at the
start of every session in that project, so a wrong one keeps arriving until you delete it —
by hand or with `deja forget`.

`deja promote <id> --state accepted|rejected|superseded|stale` marks a record deja already
holds rather than writing a new one, and it too changes only your own copy. Upstream #2976
is open — recall can hand back a session's older fact after the same session reversed it —
and `superseded` is the lever against that.

**Installed with `--auto`, so recall does not wait to be asked.** Five hooks per agent:

| event              | what it does                                                               |
| ------------------ | -------------------------------------------------------------------------- |
| `SessionStart`     | `hook-context` — a new window opens knowing the project's recent decisions |
| `UserPromptSubmit` | `hook-prompt` — a prompt that matches earlier work pulls it in             |
| `PreToolUse`       | `hook-tool` — one line on what this command or file already has            |
| `PostToolUse`      | `hook-tool-after` — after a command fails, what was run after it last time |
| `PreCompact`       | `hook-precompact` — memory goes in before the context is squeezed          |

```bash
deja install --auto --no-index    # MCP and hooks everywhere; --no-index keeps the index
deja uninstall --auto             # and it is reversible
```

`--no-index` matters: without it the install rebuilds, which is minutes on this corpus.

**Its registration lives in two places, like repowise's.** `deja install --all` writes
per-agent config under `$HOME` — this machine only. The tracked `.mcp.json` at the repo
root is what a fresh clone gets, and what every non-Claude-Code agent reads. Both name the
server `deja`, and Claude keys MCP servers by name, so the two collapse rather than
double-register. Both are path-less on purpose: the index is machine-wide.

**A worktree needs no setup.** The index is at `~/.cache/deja`, outside git, so a worktree
answers the moment it is cut — no per-worktree init, unlike repowise below. Verified from a
worktree that had never seen deja.

**Settle the trust policy BEFORE the first `deja sync`, because the default is open.** Sync
is peer-to-peer over your own ssh — `deja sync ssh <host>` records the peer, bare `deja
sync` then exchanges with all of them, incrementally by watermark. What arrives is another
machine's indexed sessions, and recall injects into agents, so an imported session is text
your agent may act on. The guide says imported memory "stays searchable but never injects
itself". **That is not what the binary does**: with no `~/.config/deja/policy.json` it
reports "every origin activates everywhere", and `internal/policy` says so outright —
"Defaults allow everything, matching prior behavior". Believe the binary, as with the paths
elsewhere in this file.

So this machine carries the policy the guide describes, and a new machine needs it written
before it pairs with anything:

```json
{
  "activations": {
    "mcp": { "local": true, "imported": false },
    "auto": { "local": true, "imported": false }
  }
}
```

`deja doctor` must then print `search local+imported`, `mcp local-only`, `auto local-only`.
Check it, because **a malformed policy file fails open**: any parse error falls back to
allow-everything and only doctor complains. Egress is derived rather than declared —
content leaves the machine only if all three activations pass it — which is what stops a
box refusing a session to its own agent while shipping the same text to an embedding
endpoint.

**A transcript is evidence of what somebody did, not of what is true now.** It carries the
wrong turn as faithfully as the fix, and the fix may be three sessions later. Read the date,
and confirm against the tree before acting on it.

**Durable rules go in this file, not in a recall index.** If a lesson is worth carrying, it
is worth reviewing, and this file is what gets read. Three tests, all of which must hold:

- **Not derivable from the repository.** Code structure, git history and what this file
  already says are not rules.
- **Evidence a stranger can check** — a number, a command with its output, a date, a file.
  "Indexing is heavy" is not a rule.
- **It would change what somebody does.** Otherwise it is trivia.

And **never write down what the code should carry instead** — if the lesson is "this must be
called before that", the fix is an assertion or a test.

## Code search

**`grep`, `glob` and reading the file** is the answer for _does this exist, and who calls
it_.

**`repowise` is installed, and it is not a second `grep`.** That ordering is measured, not
asserted: `nocx-14sbw` put both against five questions we actually ask. With a live
embedder repowise answered 2 correctly, 1 partially, missed 1 honestly, and got 1
confidently wrong by repeating a stale comment in our own code (`nocx-n5gr2`). `grep`
answered all five in under 0.1 s. With the embedder dead it scored 0 of 5. So `grep`,
`glob` and reading the file remain the answer for _does this exist, and who calls it_, and
the MCP tools are for the questions underneath that: history, risk, and how a module hangs
together.

- **History is what it gives that a search cannot.** `get_risk` / `repowise risk`: hotspot
  scores, defect profiles, bus factor, and co-change partners with support counts —
  including pairs with no import and no structural link.
- **Never take its prose as fact about the tree.** It repeats our own comments, stale ones
  included, and its line numbers drift. Confirm anything it claims about code by reading
  the code.
- **The web Chat at `repowise serve` is broken** — the UI proxy stalls the stream the
  moment the agent calls a tool. Use the MCP tools.
- **`repowise decision add` is not how a decision is recorded here.** Decisions are ADRs in
  `docs/decisions/`, which repowise indexes by itself. A decision authored into `.repowise/`
  sits in a directory git does not carry. The generated `.claude/CLAUDE.md` says otherwise;
  this file wins.

**The semantic half is off unless a dimension override is set, and nothing says so out
loud.** repowise's `_DIMS` table declares `google/gemini-embedding-001` at 768 while the
model returns 3072, so every vector fails its width check, none is written, and retrieval
falls back to BM25 over the generated pages — which is the configuration that scored 0 of 5
above. The override is `REPOWISE_EMBEDDING_DIMS=3072`, and it is needed **twice**: once for
`repowise reindex`, which writes the vectors, and once in the environment of the MCP
server, which embeds the QUERY. The server gets it from the tracked `.mcp.json`; a Claude
Code session also carries it in `~/.claude/settings.json`. The symptom when it is missing
is inside the tool result rather than on the surface — `retrieval_degraded: ["embed"]` with
`confidence: "low"` — so an agent reads a hedged answer and learns not to ask again.

**Check it with `repowise doctor`, and read the right row.** `Coordinator drift` is the one
that tells the truth. `SQL ↔ Vector Store: in sync` passes while both sides are empty, and
that pair was once read the wrong way round here: the drift counter was written off as the
broken check while it was reporting 3699 pages against 0 vectors, correctly. After the
override the same row reads `SQL=3854, Vector=3854, Drift=0.0%`, and `SQL ↔ Vector Store`
is the one that goes red mid-rebuild.

**The MCP registration is path-less on purpose, and it lives in two places.** Path-less so
it resolves whichever repo you are in — pin a path and every worktree silently answers
about `main`, stale on exactly the files you are there to change. The two places are not
interchangeable: `~/.claude.json` is user-scope and **only Claude Code reads it**, while
the tracked `.mcp.json` at the repo root is what every other agent reads. A new agent gets
repowise by being added to `.mcp.json`.

**Nothing in git turns repowise on.** `.mcp.json` names the server and carries the
embedding override, and that is all it can do: the index in `.repowise/`, the post-commit
sync in `.githooks/post-commit` and the read hooks in `~/.claude/settings.json` are
gitignored or machine-scoped, so they are per-clone and opt-in. A fresh clone has the
registration and no index — which is the "no index" answer described below, not a broken
connection.

**In a worktree the index does not come free, and we build it on demand** — when somebody
is about to work there, not in advance. Budget several minutes and a few hundred megabytes.

```bash
REPOWISE_SKIP_EDITOR_SETUP=1 repowise init -y --no-prose --no-editor-setup
repowise hook install   # post-commit sync; per worktree
```

`--no-editor-setup` keeps `init` from writing `.claude/CLAUDE.md`, `.vscode/mcp.json` and a
managed `AGENTS.md` into a branch that may not ignore them; `--no-prose` keeps it free of
model spend.

**An unindexed worktree still STARTS the MCP server**, and then every tool answers "no
index" — which reads as a broken connection rather than as a missing index. Know the
difference before you debug it. **An empty `.repowise/` is not an index either:** an init
that never finished leaves a database with no pages, and it behaves exactly as if the
directory were absent. Check `repowise status` for the page count, not the directory.

**Distill only rewrites a BARE recognized command.** The hook wraps `go test …`,
`npm ci`, `git log …`, `make …` and their kind, and by design never touches a compound
command — `cd somewhere && go test ./...` passes through raw, and so does an unrecognized
spelling of a known tool. So write the command plainly and pass the directory as an
argument where the tool supports one; a habitual `cd … &&` prefix spends the whole saving.

**Do not `git add -A` in a worktree whose branch predates this.** `.repowise/` and
`.claude/CLAUDE.md` are ignored on `main` only; on an older branch they show up untracked,
and `.repowise/.env` holds an API key.

## Your machine is not the product's

**Your dev profile is not the installed app's.** Anything you build or run from this repo —
`make dev`, `make dev-web`, `make build`, and the Playwright suite, which launches a backend
of its own — resolves `nocx-dev` rather than `nocx`, because the directory is chosen by the
build tag and only `-tags release` picks the shipped one
(`internal/storage/appdir.go`). So a dev stand starts with no profiles and no vault, and
that is correct: before it, an e2e run wrote the developer's real settings and reset their
theme on every pass. If you want your real SSH profiles in the dev stand, copy them across
by hand — nothing migrates them for you, and nothing should.

**A dev build logs everything, and that includes what you typed.** The default level is
the BUILD's (`internal/log`'s `DefaultLevel`, split on the `release` tag), so a dev stand
writes debug from its first line without being asked — the owner's decision of 2026-09-10,
because a debug line nobody can reach without a restart is a debug line nobody writes.
The data plane's arrival log (`internal/transport/ws.go`) is one of those lines, and that
plane carries exactly what the person at the keyboard pressed: every password typed into a
running `ssh` for a host nocx holds no credentials for. `log.Sensitive` still redacts it in
a shipped build, chosen by the build tag and not by the level, so nothing a user runs is
affected. What is affected is `~/.local/share/nocx-dev/nocx.log` on YOUR machine:
**it is not a file to paste into an issue, a bead or a pull request.** Quote the lines you
need.

**The e2e suite gets a disposable `$HOME`.** There is one stand and Playwright owns it
(`e2e/stand.ts`), so the boundary is applied to every backend the suite starts — the shared
one and the ones individual specs raise — by `e2e/home-isolation.ts`, which RAISES rather
than warns if a caller tries to opt out. There is no second path to remember:
`e2e/preflight.ts` refuses a run with `NOCX_WS_PORT` set, because nothing reads it any more
and a run that thinks it is driving a backend of its own is measuring the wrong process.
The boundary is what keeps a run off your settings, your vault documents and your shell rc
files.

**`$HOME` moves directories and cannot move a per-user OS service** — the keychain, the
Secret Service, a launchd agent, D-Bus. `go-keyring` talks to the Keychain service, not to
a directory, and a probe of the system vault provider is a real keychain **write**: under a
disposable `$HOME` with no keychain in it, a read fails silently while a write raises, once
per backend start.

The fix is therefore not isolation but DECLARATION (design D10): the keystore stance is
stated before anything is built, never discovered by writing to one. A Go test that has not
said whether it may reach the OS keystore is refused by `app.New`; a backend binary takes
the stance from its BUILD, so `cmd/nocx-server` compiled without `-tags nocx_login_session`
has no OS keystore to reach and the e2e suite has nothing to remember to switch off. Expect
the next per-user service to arrive the same way.

**Run e2e in the container — it is CI's environment and it is far faster** than a cold
desktop build per spec, because it runs the headless path (`cmd/nocx-server` plus vite).

```bash
e2e/run-in-container.sh                        # whole suite, both browsers
PW_PROJECTS=chromium e2e/run-in-container.sh e2e/sidebar.spec.ts
```

**Its failure set is not CI's, and CI is the source of truth.** The container runs Linux
WebKit at a container-default viewport; the shipped app is macOS WKWebView. Layout-sensitive
specs fail there and pass in CI. Use it to iterate, confirm in CI, and never "fix" a test
that is only red in the container without working out which one is lying.

## How we work

1. Take the next task with the queue command in [What to work on next](#what-to-work-on-next).
2. Read the relevant `AD`(s) before touching a boundary.
3. **TDD**: red → green → refactor. The failing test comes first.
4. Keep it green, and let the gate cost what it is worth. `pre-commit` is static and takes
   seconds — formatting, linting, the ratchets, the wire contracts, the type checkers — so
   committing in small steps stays cheap. `pre-push` runs no test at all: it publishes the
   issue tracker and gets out of the way. It used to run the containerized suites "scoped to
   what the push actually changes", and that scoping could never fire, because git hands a
   pre-push hook the remote's sha and a branch the remote has not seen has none. So every
   push ran everything, on every worktree. The suites are one command away by hand
   (`.githooks/containerized-tests.sh`); what catches a break is CI and the merged-tree gate.
5. Update the bead; record any non-obvious decision as an ADR in `docs/decisions/`.

## Testing: five rules, each bought by a green suite over a broken product

**1. A test asserts what a user can do, not what the code currently does.** Exercise the
feature through the seam a person actually reaches — the button exists, it is enabled from
the state a user starts in, activating it reaches the client method, and the result appears
afterwards. A test written by reading the implementation cannot report a missing feature; it
can only confirm that what was written does what it was written to do.

> The connection manager once shipped with no way to create a group, behind a fully green
> frontend suite in which every test mounted the component and asserted what it rendered.
> Every unit was correct and the user's task was impossible.

**2. Every epic that is not a chore proves its happy path.** Name in one sentence what a
user can do that they could not before, and close the epic only when one automated check
has watched them do it end to end. Write that check when the epic is created — by the end
you know what the code does, and that is the knowledge that makes you write the test the
implementation passes. `cmd/nocx-server` runs the real backend headless — the shipped
coordinator, not a harness beside it — so there is no excuse about the harness.

**`deadcode` and coverage are floors, never criteria. Neither can report a feature that is
missing — only that written code is used.**

> An epic called "your commands survive a restart" shipped an encrypted store, a key
> lifecycle, a retention policy, a query method with a schema and a page of Settings
> controls. Its `Add` had no caller outside its own tests: no command was ever recorded. The
> acceptance criterion was "`deadcode` is empty", and it was — a reachable read path hid an
> unreachable write path in the same package. A worker had reported "production wiring is
> blocked"; the next round said "deadcode empty" and it was read as "history works".
> **A reported blocker becomes a bead with a dependency edge in the same minute, or it
> evaporates between rounds.**
>
> Same epic, second way: the key package had a test for every failure path and none
> asserting the key is obtainable on an ordinary machine — where it never was. **For every
> "returns an error when…" there is a paired "and on a normal machine it succeeds".**

**Ask `deadcode -whylive <symbol>`, not `deadcode -filter <package>`.** `deadcode`'s RTA
marks every method reached through an interface as reflection-reachable, so on the packages
we most want to check the filter form has always printed nothing and cannot be made to
print anything. A criterion written on it is not merely satisfiable while the write path is
dead — it is unfalsifiable. `-whylive` answers the question actually being asked, and the
contrast is what makes it evidence:

```
deadcode -tags gtk3 -whylive '…/internal/content.sqliteContent.Submit' ./...
  → main → App.Run → … → sqliteContent.Submit        # wired
deadcode -tags gtk3 -whylive '…/internal/content.sqliteContent.AddEdge' ./...
  → "reachable only through reflection"              # not wired
```

Note `-tags gtk3`, without which cgo fails on Linux before `deadcode` reaches our code.

**The blind spot is RTA's, so the ratchet has it too.** Measured with three probes in one
run: a plain unwired function **is** reported; the same shape wired from one call site is
not; and a dead **method on a type reached only through an interface** is **not reported
either**. That third case is most of this codebase, since AD-8 puts every module behind an
interface.

**Therefore: `deadcode` can tell you a symbol is dead. It can never tell you a feature is
wired.** For that, name the seam and ask `-whylive` for it, or write the test that watches a
user do the thing. On an interface-first codebase, rule 2 is not a supplement to the
ratchet; it is the only check that works.

**3. Test the failure paths, and state invariants as intervals.** For every external call
your code makes, there is a test where that call fails — mechanical, cheap, and the single
highest-yield check we have. For a procedure touching several stores, enumerate the partial
failures: a middle step fails — what is now true on disk, in the keychain and in memory,
and how does the next start recover? And write invariants with **both ends**: not "`Create`
writes `PhasePrepared` before calling the provider" (a moment) but "the record exists from
before the write until metadata references the secret" (a span). If you cannot name the
closing event, you do not yet understand the invariant.

> `internal/vault` was clean under `go vet`, `golangci-lint` and `-race`, with a full suite
> behind it. An adversarial read then found ten defects, two release-blocking — a `Setup`
> that returned while holding its mutex, deadlocking everything after it, and a `Create`
> that deleted the journal record it had just written. No test made a dependency fail, and
> every deadlocking return had zero coverage. A criterion that named only the start of the
> interval bought a test that guarded only the start.

**4. Do not let the author of the code be the only author of its tests.** A test written by
the implementer in the same pass encodes the implementer's model, including the parts that
are wrong — the code and the test agree, and are wrong together. Cheapest fix, almost always
worth it: **write acceptance criteria as assertions rather than prose**, in the bead itself.
Expensive and reserved for code where a defect is costly (the vault, the updater, the
transport): have someone who did not write the implementation write the tests from the spec.
That second reading found most of the vault defects above, for one round trip.

**5. The wire is a party to the contract.** Every JSON-RPC result shape is declared once, as
a JSON Schema in `contracts/`. The renderer's types are **generated** from it (committed,
never hand-edited); the Go side is **validated** against it. `additionalProperties: false`
plus an explicit `required` is what makes it exact — a schema without both is theatre. Three
checks, and the third is the point:

- `npm run contracts:check` (pre-commit) — the committed generated file matches the schema.
- `…_DTOConformsToContract` — the Go struct marshals to something the schema accepts.
- `…_OverTheWireConformsToContract` — **the real result, off the real socket**. A test that
  validates a payload the test itself built proves the struct is well-formed, not that the
  server sends it.

`contracts/` is filled in **as methods are touched** — a method you add or change gets its
schema in the same commit. See `contracts/README.md`.

> `vault.status` had never sent `defaultProvider`. The renderer's type declared it, the
> Vault page read it every render, and the setter wrote a value nobody could read back. Both
> suites were green: Go tests decode into anonymous structs naming the fields that test is
> about, with no assertion for "and nothing else", and the frontend's hand-written fixtures
> were written _from the interface_, so they contained the field because the renderer wanted
> it.

**A soft degrade must be visible in the product, not only in a log.** A store failing to
open is a `slog.Warn` while Settings goes on offering a toggle, a retention age and a budget
that govern nothing. A silent degrade the UI contradicts is how a feature that does not
exist survives a release.

## There is no "not ours"

**"Not mine, it was already broken" is not a finding, and it is never an answer.** Whoever
is looking at the broken thing owns it — a red CI job, a failing local test, a defect
noticed in passing, a bead somebody else filed weeks ago, a feature adjacent to the one you
were sent to build. The repository has one queue and everything in it is ours.

Establishing that a defect predates your branch is still worth doing — but as the FIRST
line of the diagnosis, never as its conclusion. It tells you where the defect lives; it
does not tell you to stop. So the shape of the answer is always: name the failing assertion
or the wrong behaviour exactly, say where it lives (`git diff origin/main...HEAD -- <path>`
settles "did I bring this"), and then fix it. If a bead already owns it, work that bead —
another "occurrence" note is worth less than one line of fix.

This is about BREAKAGE YOU HAVE ENCOUNTERED, and it does not license widening the task you
were given. New work still comes off the queue, and a brief still means what it says. The
distinction: nobody asked you to build the adjacent feature, and everybody expects you to
fix the adjacent thing that is broken.

**A rerun is legitimate exactly once**, to see a failure a second time. A green rerun is not
evidence the defect is gone, only that it did not fire. **A flake is a defect, not weather:**
a test that fails occasionally is reporting a real race, in the product or in itself, and
the run that passed is the one that got lucky. Never "fix" it by widening a timeout or
adding a retry — that converts a report into silence, which is the failure mode the testing
rules exist to prevent.

**When you genuinely cannot fix it in this session** — the cause sits in a package another
worker is mid-flight in, or the fix is an epic — say that IN THOSE WORDS, with what you
found, what is left and what it would take, and ask. That is a report. "Not ours" is not.

## Before you fix anything

A bug report is a symptom, not a mandate to edit. Five checks, in order — skipping them is
how two agents ship two answers to one question.

1. **Already filed?** Area first — it is the only listing short enough to read whole:

   ```bash
   br list --label <area> --status all
   br search <phrase>                    # then words, for the bead filed in other words
   deja "<keyword>"                      # what a past session already hit
   ```

   A hit is not automatically your task — read it. It may be claimed, blocked, or record
   that the behaviour is deliberate. Work the existing bead rather than opening a second.

   **Search for the behaviour, not for your name for it.** Duplicates here are rarely
   near-copies — the same defect gets filed twice in two paraphrases, by a filer who did
   search. When you cannot phrase it two ways, read the whole area listing instead; that is
   what the area label is for.

2. **Deliberate?** `docs/vision.md` and the owning epic say what is explicitly out. The
   empty "Sessions" panel is a placeholder a comment in `main.ts` declares; "fixing" it
   invents a feature nobody asked for.

3. **Which `AD`?** Check before, not after. A fix that routes PTY bytes through JSON-RPC or
   lets the backend sniff the stream is not a fix.

4. **Decided in an ADR?** `ls docs/decisions/`. Re-deciding a settled question inside a
   bugfix is how it stops being settled.

   **An accepted ADR is never edited. A change is a NEW record that supersedes it.**
   The owner's rule, 2026-09-10. An ADR is evidence of what was decided and why, at a
   date — edit it and the evidence is gone, while every citation written against it now
   points at a decision nobody took. The index already carries the spelling
   (`Superseded by ADR-NNNN`, and `Accepted (§7 superseded by ADR-0027)` where only a
   section moved), so this costs one row. The new record names what it supersedes and
   why the old answer stopped holding; citations elsewhere — `contracts/`, code
   comments, protocol docs — move to the new number in the same commit.

   This overrules the practice visible in the tree: ADR-0024 carries two `## Amendment`
   sections written into the record itself. Do not copy them. They are what the rule
   was made against.

5. **Is the code reachable?** A file on `main` is not a feature in the product.

   ```bash
   deadcode -filter 'nocx/internal/<pkg>' ./...              # unreachable from main()
   grep -rn "New<Thing>(" --include=*.go . | grep -v _test   # who constructs it?
   ```

   Read `internal/app/app.go` and confirm the thing is wired in. Then check the other
   direction too — rule 2 above: a package can be reachable and still have a dead half.

   When you are the one PLANNING the work, this check has a second edge: a task that adds a
   Go package lands together with the wiring that makes it reachable, or its commit cannot
   pass the deadcode ratchet at all. The gate is the hook, not the brief, so a worker cannot
   be briefed out of it — a plan that separates them forces `--no-verify`.

> A closed-unmerged pull request whose commit was already an ancestor of `main` nearly had
> an agent rebuild thousands of lines. Then, the files being on `main` having been
> established, the next claim was "so the vault shipped" — while `deadcode` reported every
> one of its functions unreachable. The tests hid it and the deferral lived only in a code
> comment. **A `TODO` in source is not a task — file the bead before you write the comment.**

### The five checks gate the brief, not the diff

If you are a coordinator writing a brief, a spec or a plan for somebody else to implement,
**the checks above are yours and they apply before you write it.** The brief is where the
architecture is decided; by the time a worker is editing files the decision has been made,
and the checks can only confirm it. "I am not touching code" is not an exemption — it is
the moment the exemption costs the most.

A brief that crosses a boundary **names the `AD`s and ADRs it touches and what they already
decided, before it says what to build.** Checked by eye at review, like the commit-message
rule.

> One session produced three of these. A spec proposed the three techniques ADR-0004 names
> and rejects, in the order it rejects them. A brief told a worker to shell out to per-OS
> network tools on the local machine, against "Interface-first + DI" and against a package
> that already solves that per-OS problem here. A report to the owner claimed nocx never
> deploys a binary to a remote host, while `architecture.md` defers exactly that helper and
> an `AD` names it a build target. One cause each time: writing from what the conversation
> remembered instead of reading the binding document for the boundary being crossed.

## Before you investigate: two checks that beat reasoning

**Search the transcripts before fighting the environment.** `deja "<what you are doing>"`
costs seconds and reads what every agent on this machine already tried.

> A session spent installing Xvfb and rebuilding the OS twice to run Playwright ended when a
> lookup turned up the headless backend — a path needing no display, in the repo the whole
> time.

**When a branch behaves differently from `main`, diff it against `main` first** — before
measuring, instrumenting or theorising:

```bash
git diff origin/main...HEAD -- <path> | grep '^-'
```

> A large refactor silently dropped one subscription, and the symptom was a Playwright click
> timing out on a visible button. Hours of geometry reasoning; the removed-lines diff found
> it in a minute.

**And when a gate fails only for you, suspect how you launched it before you suspect the
code.** A long run wants detaching, and the reflex is `nohup` — which sets SIGHUP to
`SIG_IGN`. That disposition survives `exec`, and Go deliberately preserves signals ignored
at program entry, so it reaches the test binary, the shell an `internal/pty` test spawns
and everything that shell runs. `LocalPty.Close` then cannot hang its program up; the
program keeps the slave open; the master's `Read` never returns; Go defers the real
`close(2)` behind that in-flight read, so the kernel's own last-close hangup never fires
either — and the package deadlocks to its ten-minute panic. **Detach with `setsid` alone.**
It does not touch signal dispositions.

`grep SigIgn /proc/<pid>/status` settles it in one line: bit 0 set means SIGHUP is ignored
in that process, and the mask is inherited, so reading it on a leaked child names the
ancestor that did it.

> 2026-09-06 (`nocx-pibr3`). Three `make ci` runs red on `internal/pty`, 600 s each, on
> `origin/main` and on a branch alike — while the package was green run by hand every
> time. Two causes were written down and committed before the third was measured: five
> leaked `tail -f` from five runs all carried `SigIgn 0x1`, survived an explicit
> `kill -HUP` and died instantly on `kill -TERM`. Removing `nohup` and changing nothing
> else: `ok internal/pty 1.510s`. A worker had already been dispatched to fix the pty.

## What to work on next

Asked to "keep going" with no further instruction, this is the whole answer:

```bash
scripts/br-queue.sh
```

It prints two lists: tasks inside epics somebody has actually taken, and standalone bugs,
which legitimately have no epic. It is a script rather than two piped commands because
`br ready` can filter on neither parent nor issue type, so both filters are computed. Read
the script before working around it; the reasoning is in its header.

**If it returns nothing, that is an answer, not a bug** — every open epic's front is
occupied. Finish something in flight or take a free epic; never widen the query.

- **You may not take a task out of an epic nobody has taken.** If the epic is free, take
  the epic (`br update <epic> --assignee "$(git config user.email)" --status in_progress`),
  then come back for its children.
- **Never take work out of a blocked epic** — it is blocked because the same files are
  moving. The queue script enforces this; going around it via `br list`, `br search` or an
  id in a document is the failure mode. If a bead is not in `br ready`, do not start it.
- **An epic is assigned, its children are claimed.** Owning an epic means seeing it to its
  DONE WHEN. Never `--claim` an epic bead as though it were a task.
- **`br ready -t epic --unassigned`** lists epics nobody owns and nothing blocks — what you
  can hand to a colleague. Do not flip an epic to `in_progress` to hide it from a task
  listing; the queue script already excludes epics.

### Backlog invariants

- **An epic blocks another only when they touch the same code** — not "this is more
  important" and not "this comes later". `br dep add <blocked-epic> <blocker-epic>`.
  Priority, not blocking, is where importance goes. Several epics available at once is
  normal and wanted. Most edges that ever had to be removed encoded "not yet"; before that,
  a bare `br ready` was unusable.
- **`blocked` is computed, never stored.** You cannot set it; you can only add the edge. A
  blocked epic still prints as `○` — read its `DEPENDS ON` list.
- **An epic is a DAG, not a bag.** Sequence children with `blocks` so only a few are ready
  at once. `br show <epic> --json` carries a `rollup` of its descendants by status, which is
  the cheapest read of the same thing.
- **Where a bug goes.** Inside a live deliverable, a child of that epic. Arriving from
  nowhere, **no parent at all** — a standalone bug is legitimate. Filing it under the
  nearest plausible epic is what grew the area epics that had to be split. If triage shows
  it is a symptom of something structural, it _becomes_ an epic and carries a
  `discovered-from` edge back to the bug.

### One area label, and a status that is true

When `in_progress` lies, nobody trusts status; when nobody trusts status, filing is cheaper
than searching; and every new bead makes the next search worse. Both rules below exist to
stop that loop.

- **Every bead carries exactly one area label, from this list and no other.** It is the
  module map in [`docs/architecture.md`](docs/architecture.md), then the areas that map
  predates:

  `transport` `session` `pty` `ssh` `shellintegration` `settings` `terminal` `ui`
  `ui-kit` — the module map itself, with `config` under its code name `settings`, and the
  frontend's `ipc` folded into `transport` because one protocol has one owner.

  `remote-host` `assistant` `api` `connmgr` `vault` `sandbox` `lifecycle` `content`
  `workspace` `files` `git` `editor` `update` `coordinator` `notify` `pets` `e2e`
  `infra` `docs` — the rest of the tree. `remote-host` keeps that name, not `helper`,
  because this file and `architecture.md` both cite `br list --label remote-host`.

  Label by the area that **owns the behaviour**, not every area the bead touches: a tab
  strip that fails to show a badge is `ui`, not `content`. Needing two labels usually means
  it is two beads — file them.

- **A label never restates a field.** Labels duplicating `issue_type`, and labels that were
  really a date, have been deleted. `mvp` and `phase-1/2/3` stay: they are roadmap, they are
  orthogonal to area, and the rules above already govern them.

- **`in_progress` means a worker is holding it now** — not "started once", not "nearly
  done". Stopping means setting it back to `open` in the same minute, because an unheld bead
  sitting in `in_progress` is invisible to `br ready` and to every colleague looking for
  work. A bead once sat held with its work already shipped and named in the tree, and a
  worker re-derived that entire surface before noticing it existed.

- **An epic's own timestamp is not its liveness**, because an epic does not move when its
  children do. Ask the children before believing it:

  ```bash
  br show <epic> --json | jq '.[0].rollup'
  ```

  `rollup` is derived, so it cannot drift from the children the way a copied timestamp can.

- **Close with evidence a stranger can check.** Name the file, symbol, test or commit —
  "duplicate" and "done" are not reasons. For a duplicate, name the survivor and say what
  makes them one behaviour; two beads touching one file are not duplicates, by the same test
  the epic-blocking rule uses. And re-read the tree before believing a bead's own notes:
  beads have recorded work as blocked while their commits were already on `main`.

### Creating an epic

1. **Scope it to a deliverable, not a code area.** Can one person be handed this whole and
   finish it? "Persistence" and "Quality gates" were areas — every new bug landed in them,
   so they could never finish.
2. **Unless it is a chore, name what a user can do that they could not before, and the one
   end-to-end check that watches them do it** (rule 2 above). No such sentence means it is a
   chore — label it — or an area of code wearing an epic's clothes.
3. **A criterion that stops being false exactly once**, plus what is deliberately out.
   Enforced by review, not by the tool.
4. **Set the status deliberately** — `open` means free to assign.
5. **`blocks` edges only against epics whose files it collides with.**
6. **Label it** `mvp`, `phase-2`, `phase-3` or `infra`; no `mvp` epic behind a deferred one.

Prefer more, smaller epics — "handed over whole" and "large area" cannot both hold.

## Git authority

Agents have **standing authority to commit and push**. Allowed without asking: `git commit`,
`git push`, `br close`, `br sync --flush-only`, running the gates. Branch first if you are
on `main`.

**Merging a pull request always requires explicit approval** — in that session, for that PR.
Authority to commit and push is not authority to merge.

**Run the gate CI runs, not a subset of it.** `make ci-full` is every CI job, each in the
environment its job runs in — and these are the whole of `ci.yml`:

```bash
make ci-full             # all of them, cheapest first
make ci                  # host-side only: the macOS `backend` job + host frontend gates
./scripts/ci-linux.sh    # `backend-linux`, both keyring variants
./scripts/ci-frontend.sh # `frontend` — frontend/ AND the repo root
./e2e/run-in-container.sh # `e2e`, the same image and the same command CI runs
```

`make ci` alone is **not** the gate, whatever it used to say about itself: it covers one job
of several, and a release attempt once came back red from a job it had just reported green.
The containerized runners are byte-for-byte their CI counterparts in **software** — the same
image, packages, Go toolchain and command.

**The gate belongs to whoever integrates, and to nobody else.** A worker on a branch runs
the unit tests for the files it changed and stops there. It does not run `make ci-full`, the
containerized jobs or the e2e suite — not as diligence, not "just to be sure". The
coordinator runs all of them, **once, on the merged tree, before `git push` to `main`**,
every time, including when every branch that went into it was green alone.

That is not a weakening: the failure that bought the rule was a push to `main`, and there it
binds exactly as hard as before. What comes off is a cost it never bought. Branches that
were each green alone have merged red, on defects that existed on no branch — a test struct
literal predating a new required dependency, a test asserting a mechanism the merge had
deleted — and the per-branch full runs found nothing the merge run did not.

Three costs, all invisible to the worker paying them. The containerized jobs serialize on
one Docker daemon, so parallel workers each running the full set finish later than the same
work run in sequence. They mount `node_modules` as named volumes with no worktree in the
name, so concurrent runs break each other's dependency tree. And a host-side Go run used to
write to the developer's login keychain on every backend start, which on macOS is a modal
dialog apiece.

**When the merged gate goes red, send it back to the worker, do not fix it in the
coordinator.** A worker is resumable and still holds why it wrote what it wrote; the
coordinator would be re-deriving that from a diff. **Which means the worktrees stay until
that gate is green** — removing one un-resumes its worker, and the rule then has nobody to
send anything to. The exception is a defect that exists only in the merge; that belongs to
whoever resolved it.

**The runners are not their CI counterparts in timing, and no setting will make them so.**
The machine underneath differs per image — read the file, not this paragraph:
`.githooks/images/ci-linux` pins the runner's platform, so on a Mac it runs emulated;
`e2e/Dockerfile` pins none deliberately, so it builds native for whatever host it is on.
Throttling to the runner's core count does not produce the runner, it produces a third
machine unlike either.

So the caps are off by default, and the rule that replaces them is the stronger one: **a
test may not depend on timing.** Wait on an observable state change — a frame, a record, a
DOM state — never on a duration. A spec that needs a slow machine to pass is broken on a
fast one too; it has only not been caught yet. `NOCX_CI_CPUS` and `NOCX_E2E_CPUS` still cap
on demand, for bisecting a suspected concurrency defect. That is a debugging tool, not the
gate.

**`backend` is the one job with no container, and the one place local and CI still
disagree.** macOS is the target, so it cannot be containerized, and a developer Mac is not
that runner: `internal/pty` hangs here while green there, and a handful of tests in
`internal/app` and `internal/git/local` fail here and pass there. Until those are closed,
read a local `backend` red against that list before believing it — and never the other way
round: CI is still the source of truth.

### Every commit names its bead

```
<type>(<scope>): <imperative subject, lower case, no full stop> (<bead-id>)

<body: what was wrong, what changed, and why this way rather than the obvious
alternative. Wrap at 80. Prose, not bullets — a bullet list records what you did
and loses the reasoning, which is the only part worth keeping.>

Co-Authored-By: ...
```

- **`<type>`** — `feat`, `fix`, `refactor`, `test`, `docs`, `build`, `chore`, `perf`.
- **`<scope>`** — the module (`frontend`, `pty`, `ssh`, `session`, `transport`, `spec`,
  `beads`). Omit only when the change is genuinely repo-wide.
- **`(<bead-id>)`** at the end of the subject; several when one commit closes several. Ids
  referenced but **not** closed go in the body.
- **No bead for it?** Then there is no task — `br create` takes seconds. **Trivial?** It
  still had a reason, and it is the one nobody can explain in six months.

Checked by eye at review. If that rots, file a `commit-msg` hook rather than dropping it.

## Engineering rules (non-negotiable)

- **Interface-first + DI.** Every module behind an interface, wired at one composition root.
  Depend on abstractions, obey SRP, keep modules trivially replaceable.
- **Quality gates from every commit** — format, lint, test. Go and TypeScript held to the
  same bar.
- **Observability:** structured logging via `log/slog` behind the logging interface — no
  ad-hoc `fmt.Println`.
- **Clean-only:** no backward-compatibility shims (greenfield — break and refactor freely),
  no dead code, no quick-win hacks. YAGNI.
- **Respect the spine.** Never wrap PTY bytes in JSON-RPC (AD-1); the backend never sniffs
  the byte stream (AD-6); session-id is server-authoritative (AD-7). If an `AD` is wrong,
  change it in `docs/architecture.md` deliberately rather than routing around it.

### Look for the existing answer before you write a second one

**Before you add logic, find out whether the codebase already answers that question, and
extend that answer instead.** A second implementation of one concept is not duplication you
can clean up later — it is a regression with a delay fuse, because the two agree everywhere
you look and disagree somewhere you did not.

This is AD-8 stated as a working habit rather than a module boundary: one owner per
behaviour, and the owner is whoever already has it. It applies to a predicate, a derivation,
a table or a surface just as much as to a package.

Three questions, before the first line:

1. **Does something already decide this?** `grep` for the concept, not your name for it —
   "is this an ssh context", "which command is this token under", "may this be integrated".
   The existing answer is often two words away under a different word.
2. **Can it be extended?** A table that grows by addition, a parameter, one more variant.
   Extending keeps one truth; adding keeps two and hopes they stay in step.
3. **If it genuinely cannot**, say in the code why the existing one did not fit — the next
   person needs to know it was considered, not guess that it was missed.

**Two surfaces may never own the same input.** If a key, a position or a document state can
be claimed by two components, that is the defect, whichever one wins by evaluation order:
the loser goes on advertising what it can no longer deliver.

> "Am I in an ssh context" had two derivations, one on the completion side and one in the
> editor. They agreed for every case anyone tried and disagreed on exactly one: `ssh` with
> no trailing space, which is the state a user is in when they press Tab **instead of** the
> space. So the suppressed surface un-suppressed itself at the only moment it mattered and
> inserted a saved host over the user's choice. Underneath it, a whole second suggestion
> surface had been kept alive beside the completion dropdown, which already rendered the
> same candidates. The fix was to delete it, and the bug existed only because it had been
> built rather than found.

### Before you build a UI component: read the kit

**Read [`frontend/src/ui/README.md`](frontend/src/ui/README.md) and list `frontend/src/ui/`
first.** The inventory names every component, its identity classes and its variance.

1. **Does the kit have it?** Import it. A "toggle" is `Checkbox variant="switch"`; a status
   message is `showToast`; a titled group is `Section`/`PageSection`. At near-fit, add the
   missing variance as a typed `data-*` rather than forking.
2. **Close enough?** Extend that component in `ui/` — the kit grows by variants, not
   near-duplicates.
3. **Genuinely new?** Into `ui/`: one module, one CSS file in `styles/components/`, a stable
   identity class, a test, a row in the README table.

Never build the control **inside the surface** — a hand-rolled `<div>` with its own colours,
a bespoke button, a "temporary" status div. Each is a second vocabulary for one concept,
which is the defect two whole epics spent themselves unwinding.

A surface may **place** a kit component (`flex`, `margin`, `width`, `order`, `align-self`,
`position`) and may never **repaint** it (`background`, `border`, `color`, `font-*`,
`padding`, `box-shadow`). Wanting to repaint means the component is missing a variant — add
it there. If the kit is genuinely wrong, change the kit deliberately, the way an `AD` gets
changed.

## Stack

- **Backend:** Go — `pty`, `ssh` (`golang.org/x/crypto/ssh`), `session`, `transport`,
  `settings`. One core, multiple build targets.
- **Frontend:** xterm.js (WebGL) + TypeScript, CodeMirror 6 for the editor. Terminal render
  state lives here (AD-6) — [ADR-0001](docs/decisions/0001-xterm-js-as-vt-frontend.md).
- **Desktop shell:** Wails v3 (macOS first).
- **Transport:** one WebSocket — raw **binary** data plane + **JSON-RPC 2.0** control plane
  (AD-1).

## This file wins over a skill

The `beads-superpowers` plugin is installed for its process skills — brainstorming,
writing-plans, test-driven-development, systematic-debugging — and they are worth having.
Its tracker half is not: those skills were written for an older tracker under a different
binary name, and by the plugin's own rule repository instructions win over skills.
**Translate every tracker command in a skill to `br`.** Three do not survive a rename:
"what next" is `scripts/br-queue.sh`, recall is `deja` and not the tracker at all, and
export is `br sync --flush-only` to `.beads/issues.jsonl`.

The official `br` skill is installed too and carries the same kind of leftovers — config
keys that do not exist in `br config schema`, and an id prefix that was never ours. Believe
the binary over any skill, and this file over both.

<!-- REPOWISE_DISTILL:START — Do not edit below this line. Auto-generated by Repowise. -->

### Output Distillation

- Prefer `repowise distill <cmd>` for noisy commands — test runs, builds, `git status`/`log`/`diff`, searches, file listings. It runs the command unchanged (exit code preserved) and prints a compact, errors-first rendering; every error line survives.
- Output may contain a marker like `[repowise#a1b2c3d4e5f6: 230 lines omitted (~6.1k tokens); restore: repowise expand a1b2c3d4e5f6]`. The omitted content is fully preserved — run `repowise expand <ref>` to retrieve it, or `repowise expand <ref> -q <regex>` for just the matching lines.
- Never re-run a command to see omitted output; expand the marker instead.
- For structure-level questions about a large indexed file ("what's in here", "which function handles X"), `get_context(["path"], include=["skeleton"])` returns the file with bodies elided — every signature plus the bodies of the most central symbols — at a fraction of the cost of a full Read.

<!-- REPOWISE_DISTILL:END -->
