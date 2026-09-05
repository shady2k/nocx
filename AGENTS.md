# AGENTS.md — Working rules for AI agents on `nocx`

`nocx` is a local-first, Warp-style terminal (Go backend + xterm.js frontend + Wails v3
desktop). This file is the operating contract for **any** AI agent contributing to the
repo. Read it before writing code.

Every rule here was bought by a specific failure. The failures are named in one line so
you can tell a rule from a preference; the long-form post-mortems are in `git log` for
this file.

## Read first

- [`docs/vision.md`](docs/vision.md) — what we're building, MVP scope, roadmap.
- [`docs/architecture.md`](docs/architecture.md) — the spine: invariants `AD-1`…`AD-10`,
  module boundaries, the WebSocket protocol. **The ADs are binding.**
- The backlog lives in **beads** (`br`), not in prose.
- [`frontend/src/ui/README.md`](frontend/src/ui/README.md) — before any UI element.
- [README setup](README.md#agent-tooling) — the toolchain _and_ the agent tooling
  (`br`, `cm`, the `beads-superpowers` plugin). `make init` installs none of them.

**Fresh clone:** install the tooling, then `make init`. The backlog itself comes
with the clone — `.beads/issues.jsonl` is a tracked file — and `make init` only
builds the SQLite from it. Without `br` on PATH there is no backlog to read;
with it, `br sync --import-only` is the whole bootstrap.

**The tracker is `br` (beads_rust), not `bd` (Go beads), since 2026-09-05.** The
binary is `br`, the store is SQLite plus a JSONL export, and **`br` never runs git
— that is not an oversight, it is the design.** Nothing commits, pushes or pulls
the backlog for you, and no hook stages it. `git pull` brings someone else's
backlog in as an ordinary file change, and `br` imports it before the next command
on its own (`sync.auto_import`). Sending yours out is two lines you type:

```bash
br sync --flush-only          # db -> .beads/issues.jsonl (usually already done)
git add .beads/issues.jsonl   # and commit it with the code
```

`.githooks/pre-push` warns when you are about to push code and leave the backlog
behind. It warns and never blocks, for the reason the rest of this file gives
about gates people learn to pass with `--no-verify`.

**Why `.beads/issues.jsonl` is tracked again, having been untracked on 2026-08-29.**
It was untracked because `.githooks/pre-commit` regenerated and staged it on
**every** commit, so every branch touched all 2707 lines and almost every pull
request came back CONFLICTING — and a merge driver could not fix it, being
per-clone config that GitHub's server-side mergeability check never runs. `br`
removes the cause rather than the file: there is no hook, so nothing stages it,
and `br` resolves the database of the MAIN checkout even when run from a worktree
that has its own `.beads/` in the tree. Measured 2026-09-05: an agent in a
worktree created and edited beads, `git status` there stayed empty, and the write
landed in the main checkout's file. A feature branch cannot touch it, so two
branches cannot both change it, so the conflict cannot happen. Between machines
it still can — that one is real, and `br sync --reconcile-additive` resolves it
without deleting anything.

**Nothing left of Dolt.** No `refs/dolt/data`, no `bd dolt push`/`pull`, no
`.githooks/beads-hook.sh`, no embedded database. That machinery is gone with the
defect that bought this migration (`nocx-v48vl`): a pull that could not recover a
clone which had fallen behind, a cache that grew ~210 MB per failed attempt and
never pruned, and a process that ignored SIGTERM while holding the store lock.
On disk the tracker went from 1.9 GB to about 22 MB.

**Memories are not in the tracker any more.** `br` has no `remember`, no
`memories`, no `recall`, and its import refuses a memory record outright. The 144
memories moved to **cass-memory**: they are `.cass/playbook.yaml`, a tracked file,
so they travel with the clone the way the backlog does. Read them with

```bash
cm context "<what you are about to do>" --json    # was: bd memories <keyword>
```

Two things about that file, both measured on cass-memory 0.2.14 and both easy to
get wrong. Its rules say `scope: global` rather than the obvious `workspace`,
because **`cm context` silently returns nothing for `workspace` rules** — an exact
phrase out of a memory matched 0 with `workspace` and all 144 with `global`. They
do not leak: `.cass/` is found from the working directory, so outside this repo
`cm playbook list` shows 1 rule and inside it shows 145. Nothing about them is
exempted: they are not `pinned`, so cass may deprecate them on evidence like any
other rule, which is the point of having the mechanism. (`confidenceDecayHalfLifeDays`
is a red herring for an imported memory — it decays a rule's `feedbackEvents`, and
these have none.)

**The `cm` skill's Agent Protocol is half true here, and the false half writes into
your source.** It tells an agent to do four things: run `cm context` at the start,
cite rule ids while working, leave `// [cass: helpful b-8f3a2c]` comments in the
code when a rule helps or hurts, and then "just finish — learning happens
automatically". Step one is right and is why the skill is installed. **Steps three
and four do not apply in this repository and must not be followed.**

There is no automation: no `Stop` hook, no timer, and `autoReflect` is a dead knob
— its only occurrence in the binary is the schema default. So nothing ever runs
`cm reflect`, and those `[cass: …]` markers are explicitly documented as "parsed
during reflection". Written here they would be comments no reader will ever have,
scattered through code that has to be maintained. **Do not write them.** If a rule
helped or hurt, say so in your report; a person decides whether it becomes
feedback.

**When to write one, which is the part a tool cannot tell you.** A rule earns its
place when it was BOUGHT — when something cost you a measurement, a wrong turn, or
an hour, and the next person would pay it again. Three tests, all of which must
hold:

- **It is not derivable from the repository.** Code structure, git history and what
  this file already says are not memories. "`br` resolves the main checkout's
  database from a worktree" is one; "the tracker is `br`" is not.
- **It has evidence a stranger can check.** A number, a command with its output, a
  date, a file. "Indexing is heavy" is not a rule. "1.6 GB of index in 150 s over
  8340 sessions, measured 2026-09-05" is.
- **It would change what somebody does.** If knowing it changes nothing, it is
  trivia.

The moment to write it is when you have just finished paying — not at session
close, when the detail has already gone. And write the finding, not the story: the
next reader needs the fact and its evidence, not how you arrived at it.

**Never write a memory that the code should carry instead.** If the lesson is "this
function must be called before that one", the fix is an assertion or a test, not a
rule in a playbook nobody is obliged to read.

**Adding a rule: `import --repo`, never `add`.** `cm playbook add` always writes
`~/.cass-memory/playbook.yaml`, which lives in one person's home directory — a rule
about this repository put there is invisible to everybody else. The repo playbook
has its own route:

```bash
cm playbook import rules.json --repo     # --repo targets .cass/playbook.yaml
```

`import` wants whole bullet records — `id`, `state`, `maturity` and the rest — not
the `{content, category}` pair that `add --file` accepts; it refuses the short form
with `Required: id`. `scripts/bd-memories-to-cass.py` shows the shape.

**The playbook is deliberately empty, as of 2026-09-05.** It held 144 memories
carried over from `bd remember` and they were dropped rather than curated, because
the migration had made a fifth of them false: 7 described `bd`, Dolt and the merge
slot, which were deleted the same day, and one of those told a reader that a
worktree has no database — the exact opposite of how `br` behaves. Another 12 named
Orca, replaced by herdr. And 97 of the 147 sat in category `general`, so the gap
analysis reported `debugging: 1` while debugging rules were in the pile. A store
that has to be read sceptically is worth less than an empty one.

Nothing was lost: the 144 are in `.internal/memories-export.jsonl` and the whole
file is in git history. Refill it from work, one rule at a time, by the three tests
above — and prefer none to a rule you would have to warn the next reader about.

**Growing the playbook without an LLM: `cm onboard`.** It is agent-native and costs
no API calls — `cm onboard sample --fill-gaps` picks sessions in the categories the
playbook is thin on, `cm onboard read <session> --template` hands you one to read,
and `cm onboard mark-done <session>` records it as processed. `cm onboard gaps`
names the weak categories outright. This works on the partial index we have.

**We do not run `cm reflect`, and the agent writes its own rules.** Owner's
decision, 2026-09-06: paying an LLM to read a whole transcript back and recover a
lesson the agent already held at the time is the wrong trade. The moment to write
a rule is the moment you finish paying for it — by the three tests above, through
`import --repo`.

Two measurements stand behind that, and they are the reason no automation is
wanted here rather than merely absent. `cm reflect` on this session's transcript
produced **eight rules that were three facts**, each stated two or three times;
`dedupSimilarityThreshold: 0.85` did not catch the repeats. And it wrote them to
the **global** playbook, `~/.cass-memory/playbook.yaml` — reflection has no other
destination — where they are invisible to a colleague and visible in every
unrelated repository on this machine. The repo playbook **merges** with the global
one rather than replacing it (probe rule in global, `cm playbook list` run here
returned both), so that leak is not theoretical. All ten of those rules were
deleted on 2026-09-06; the backup is `~/.cass-memory/playbook.yaml.bak-2026-09-06`.

**So `cm` needs no API key and no model here.** `cm context` and the playbook read
a file and the cass index; neither takes a provider. `OPENAI_BASE_URL`,
`OPENAI_API_KEY` and `CASS_CLI_COMMAND` are unset and nothing persists them.

If anyone ever does want reflection, the one setting that is not optional is
`CASS_CLI_COMMAND=<a binary that does not exist>`. Left unset, `cm` resolves the
Claude CLI and spends the owner's subscription silently; pointed at nothing,
`resolveCliCommand()` returns null, `availableProviders` is empty, and it fails
loudly instead. The owner has forbidden the subscription route outright — so
reflection means an OpenRouter key in a mode-600 file outside the nix store, and
that guard, before the first run.

**What of `cm` belongs in git, measured against the binary rather than its README.**
`cm` 0.2.14 resolves four files under `.cass/`: `playbook.yaml`, `blocked.log` — bullet
ids this repository refuses, not a diagnostic log — `traumas.jsonl`, dangerous
operations specific to this codebase, and `config.yaml`. All four are shared knowledge
and belong in git; only the first two exist here so far. A fifth, `context-log.jsonl`,
is this machine's usage accounting and is ignored.

Everything under `~/.cass-memory/` stays on the machine, and one of them is a secret:
`config.json` holds the LLM key. Beside it live the personal playbook, `diary/`,
`reflections/`, `embeddings/`, `usage.jsonl` and `cost/`. None of that is ours to
commit, and the repo config is not allowed to redirect any of it — `cassPath`,
`playbookPath` and `diaryDir` are refused from `.cass/config.yaml` by design.

**The upstream README names two of those files differently**, and it is wrong about
both: it says `.cass/config.json` and `.cass/blocked.yaml`, while
`strings $(command -v cm)` yields only `.cass/config.yaml` and `.cass/blocked.log`.
Believe the binary.

**Do not put an explanation inside `.cass/playbook.yaml`.** `cm` reserialises that file
from its own model every time it writes a rule, and comments do not survive it: a
fifteen-line header saying why the playbook was empty was dropped without a word the
first time a rule landed there (2026-09-06). Reasoning goes in this file; the YAML
holds rules and nothing else.

**Your dev profile is not the installed app's.** Anything you build or run from
this repo — `wails dev`, `make dev-web`, `make build`, and the Playwright suite,
which launches a backend of its own — resolves `nocx-dev` rather than `nocx`,
because the directory is chosen by the build tag and only `-tags release` picks
the shipped one (`internal/storage/appdir.go`). So a dev stand starts with no
profiles and no vault, and that is correct: before this, an e2e run wrote the
developer's real settings and reset their theme on every pass (nocx-ti8w). If
you want your real SSH profiles in the dev stand, copy them across by hand —
nothing migrates them for you, and nothing should.

**And the e2e suite gets a disposable `$HOME`.** There is one stand and
Playwright owns it (`e2e/stand.ts`), so the boundary is applied to every
backend the suite starts — the shared one and the ones individual specs raise
— by `e2e/home-isolation.ts`, which RAISES rather than warns if a caller tries
to opt out. `NOCX_E2E_HOME_DIR` travels with each of them as the record of
which home it got. There is no second path to remember: `e2e/preflight.ts`
refuses a run with `NOCX_WS_PORT` set, because nothing reads it any more and a
run that thinks it is driving a backend of its own is measuring the wrong
process. The boundary is what keeps a run off your settings, your vault
documents, your `~/.nocx` and your shell rc files.

**Run e2e in the container anyway — it is CI's environment and it is faster.**

```bash
e2e/run-in-container.sh                        # whole suite, both browsers
PW_PROJECTS=chromium e2e/run-in-container.sh e2e/sidebar.spec.ts
```

It runs the headless path — `cmd/nocx-server` plus vite — so it is about
fifteen times faster than a cold `wails dev` per spec, and it is byte-for-byte
CI's image.

**`$HOME` moves three things and a per-user OS SERVICE is a fourth class it
cannot move**, and the keystore is the one that bit us. `go-keyring` talks to
the Keychain service, not to a directory, and `app.New` used to PROBE the
system vault provider on every backend start — "a probe is a real keychain
write", said the comment doing it.

What was written here before, and what `nocx-o4hg` recorded as the cause, was
that `wails dev` re-signs the binary each run so macOS re-asks every time.
**That is wrong, and it is worth knowing why.** zalando/go-keyring shells out
to `/usr/bin/security` (`keyring_darwin.go`), so the process asking is Apple's
signed binary and OUR signature never enters a keychain ACL at all —
re-signing cannot produce a prompt. Measured on macOS instead: `$HOME` **does**
move the login keychain — `security` resolves it under `$HOME/Library/Keychains`
— and a READ under a `$HOME` with no keychain fails **silently** while a
**WRITE** raises "Keychain not found". The probe is a write. So the dialog the
owner watched every two seconds was the disposable `$HOME` having no keychain
in it, once per backend start — not a signature, and not something re-signing
or notarising would have changed.

The fix is therefore not isolation but DECLARATION (design D10): the keystore
stance is stated before anything is built, never discovered by writing to one.
A Go test that has not said whether it may reach the OS keystore is refused by
`app.New` (`nocx-o4hg`); a backend binary takes the stance from its BUILD, so
`cmd/nocx-server` compiled without `-tags nocx_login_session` has no OS
keystore to reach and the e2e suite has nothing to remember to switch off
(`nocx-nhhr`). The general rule survives its wrong explanation: `$HOME`
isolation covers DIRECTORIES and cannot cover a per-user OS service — the
keychain, the Secret Service, a launchd agent, D-Bus.

**Its failure set is not CI's, and CI is the source of truth.** The container
runs Linux WebKit at a container-default viewport; the shipped app is macOS
WKWebView. Layout-sensitive specs (scroll ownership, tab-strip roving, label
centring) fail there and pass in CI. Use it to iterate, confirm in CI, and never
"fix" a test that is only red in the container without checking which one is
lying.

## Language

**Every document in this repository is written in English.** Code and its comments,
`AGENTS.md`, `README.md`, `docs/`, `.internal/` specs, plans and briefs, commit
messages, bead titles and bodies, and anything else a second person reads. The
repository has one working language, and a file that switches is a file half the
team skims instead of reads.

**Speak to the developer in the language they used to address you.** That is a
different surface with a different audience: a conversation has exactly one reader,
and it is theirs. Answering a Russian question in English to satisfy the rule above
is a misreading of it.

The distinction is audience, not formality. If it is committed, it is English. If it
is said to the person in front of you, it is their language.

## Repository layout

- `docs/` — `vision.md`, `architecture.md`, `decisions/` (ADRs).
- `contracts/` — one JSON Schema per JSON-RPC result shape (see below).
- `AGENTS.md` — this file. `CLAUDE.md` only points here.
- Code directories follow the module map in `docs/architecture.md`.

## Code search

**`grep`, `glob` and reading the file.** That is still the answer for _does this exist, and
who calls it_ — and it beat the index on every one of those questions we measured.

> A graph index (`graphify`) was removed on 2026-08-01 after one measured session: five
> queries, zero answers, while every finding that mattered came from `grep`. It cost 91 MB
> of committed output and a hook that made a graph query mandatory before every read. Our
> questions are almost always _does this exist, and who calls it_ — exactly what `grep`
> answers. Do not reintroduce an index, or any hook in front of a file read, without
> measuring against that baseline.

**`repowise` is installed, and it is a second way in, not a ranked one.** Use
whichever fits the question: `grep` for _does this exist, and who calls it_, the MCP
tools when the question is about history, risk or how a module hangs together.

There used to be a rule here that said to ask `grep` first, always, on the strength of
one run recorded in commit `960b270e` — five questions, `repowise` two right, one
partial, one miss, one confidently wrong. **That ordering is withdrawn**, because the
evidence behind it cannot be checked: `nocx-14sbw` asked for the five questions and
both tools' answers side by side, that criterion was never ticked, and neither the
questions nor the answers were written down anywhere. A tally with no record behind
it is not the kind of evidence the rest of this file demands, and it should not have
been used to rank a tool. The bead is still open; whoever wants the ordering back
measures it and records it properly.

What stands, because it was checked rather than summarised:

- **History is what it gives that a search cannot.** `get_risk` / `repowise risk`:
  hotspot scores, defect profiles, bus factor, and CO-CHANGE PARTNERS WITH SUPPORT
  COUNTS — including pairs with no import and no structural link. Verified against
  `git log`.
- **Never take its prose as fact about the tree.** It repeats our own comments, stale
  ones included: asked whether a write path was wired, it answered from a comment that
  had been wrong since the wiring landed (`nocx-n5gr2`). Its line numbers drift by ten
  or twenty. Confirm anything it claims about code by reading the code.
- **The web Chat at `repowise serve` is broken** — its Next.js proxy on the UI port
  stalls the SSE stream the moment the agent calls a tool, so any real question hangs
  forever while the backend on the API port answers the same request fine
  (`nocx-cvd55`). Use the MCP tools.
- **`repowise decision add` is not how a decision is recorded here.** Decisions go to
  `docs/decisions/` as ADRs, and `repowise` indexes those itself — every entry in
  `repowise decision list` is derived, `source: adr` or `source: pr`, not authored in
  it. A decision written into `.repowise/` sits in a directory git does not carry,
  visible to one clone, marked `proposed` for a person who will never see it. The
  generated `.claude/CLAUDE.md` says otherwise; this file wins.

**In a worktree it needs two commands, and one hazard needs watching.** The MCP registration
is user-scope and path-less, so it resolves whichever repo you are in — do not pin a path
into it, or every worktree silently answers about `main`, stale on exactly the files you are
there to change. The agent read-hooks follow the repo path already. What does not come free:

```bash
repowise init -y        # seeds from the base checkout: ~1m47s, $0, and 359 MB of its own
repowise hook install   # post-commit sync; per worktree
```

**Do not `git add -A` in a worktree whose branch predates this.** `.repowise/` and
`.claude/CLAUDE.md` are ignored on `main` only; on an older branch they show up untracked,
and `.repowise/.env` holds an API key.

## How we work

1. Take the next task with the queue command in [What to work on next](#what-to-work-on-next).
2. Read the relevant `AD`(s) before touching a boundary.
3. **TDD**: red → green → refactor. The failing test comes first.
4. Keep it green, and **let the gate cost what it is worth.** `pre-commit` is static
   and takes seconds — formatting, linting, the ratchets, the wire contracts, the type
   checkers — so committing in small steps stays cheap. `pre-push` runs no test at
   all: it publishes the issue tracker and gets out of the way. It used to run the two
   containerized suites "scoped to what the push actually changes", and that scoping
   never fired — git gives a pre-push hook the REMOTE's sha, which for a branch the
   remote has not seen is forty zeros, so there was no range to diff and the hook ran
   everything. Every task here gets its own worktree branch, so that was every push: a
   frontend-only diff spawning `go test -race`, four minutes of two containers, three
   at once the moment a second worktree pushed too (nocx-fwsw2). The suites are still
   one command away by hand — `.githooks/containerized-tests.sh`. What catches a break
   is CI, which is the source of truth, and `make ci-full` on the merged tree, which is
   whoever integrates.
5. Update the bead; record any non-obvious decision as an ADR in `docs/decisions/`.

## Testing: five rules, each bought by a green suite over a broken product

**1. A test asserts what a user can do, not what the code currently does.** Exercise the
feature through the seam a person actually reaches — the button exists, it is enabled from
the state a user starts in, activating it reaches the client method, and the result appears
afterwards. A test written by reading the implementation cannot report a missing feature;
it can only confirm that what was written does what it was written to do.

> 2026-07-29: the connection manager shipped with **no way to create a group** — 1041
> frontend tests green, every test mounting the component and asserting what it rendered.
> `groups.create` refused every call the UI could make, because all nine backend tests
> passed an explicit id and the renderer minted none. An empty group rendered as nothing at
> all. Three defects, one shape: every unit correct, the user's task impossible.

**2. Every epic that is not a chore proves its happy path.** Name in one sentence what a
user can do that they could not before, and close the epic only when one automated check
has watched them do it end to end. Write that check when the epic is created — by the end
you know what the code does, and that is the knowledge that makes you write the test the
implementation passes. `cmd/nocx-server` runs the real backend headless (no wails, no
GTK, no display) — the SHIPPED coordinator, not a harness beside it — so there is no
excuse about the harness.

**`deadcode` and coverage are floors, never criteria. Neither can report a feature that is
missing — only that written code is used.**

> 2026-08-01: `nocx-rtg0` ("your commands survive a restart") shipped an encrypted SQLite
> store, a key lifecycle, a budget, a retention policy, a `history.query` method with a
> schema and five Settings controls. `ContentDB.Add` had **no caller outside its own
> tests** — no command was ever recorded. The acceptance criterion was "`deadcode` is
> empty", and it was: `Query` is genuinely reachable, so a reachable read path hid an
> unreachable write path in the same package. The worker had reported "production wiring is
> blocked; the store is test-reachable only"; the next round said "deadcode empty" and it
> was read as "history works". **A reported blocker becomes a bead with a dependency edge
> in the same minute, or it evaporates between rounds.**
>
> Same epic, second way: `contentkey` had tests for every failure path and none asserting
> the key is obtainable on an ordinary machine — where it never was. **For every "returns
> an error when…" there is a paired "and on a normal machine it succeeds".**

**Ask `deadcode -whylive <symbol>`, not `deadcode -filter <package>`.** The filter form is
worse than a weak check on the packages we most want to check: `deadcode`'s RTA marks every
method reached through an interface as reflection-reachable, so for `internal/content` the
filter has **always** printed nothing and cannot be made to print anything. An acceptance
criterion written on it is not merely satisfiable while the write path is dead — it is
unfalsifiable. `-whylive` answers the question actually being asked, and the contrast is
what makes it evidence:

```
deadcode -tags gtk3 -whylive '…/internal/content.sqliteContent.Submit' ./...
  → main → App.Run → … → sqliteContent.Submit        # wired
deadcode -tags gtk3 -whylive '…/internal/content.sqliteContent.AddEdge' ./...
  → "reachable only through reflection"              # not wired
```

> 2026-08-17, `nocx-rtg0.3`: the brief demanded the `-filter` run and the worker ran it,
> got the empty output the brief predicted, and then said so plainly — that the same empty
> output appeared before its commit and on a clean tree, so it was evidence of nothing. It
> used `-whylive` on a wired method and an unwired one in the same package to show the
> difference. That is the check; note the `-tags gtk3` too, without which cgo fails on Linux
> before `deadcode` reaches our code at all.

**And the blind spot is not `-filter`'s — it is RTA's, so the ratchet has it too.** Later
the same day `nocx-re6gk` measured it directly, with three probes in one run: a plain
unwired function **is** reported; the same shape **wired** from one call site is not; and a
dead **method on a type reached only through an interface** is **not reported either**.
That third case is exactly the shape `ContentDB.Add` had. So the gate catches a dead
function, and cannot catch a dead method behind a live interface — which is most of this
codebase, since AD-8 puts every module behind one.

**Therefore: `deadcode` can tell you a symbol is dead. It can never tell you a feature is
wired.** For that, name the seam and ask `-whylive` for it, or write the test that watches
a user do the thing. Rule 2's "every epic proves its happy path" is not a supplement to the
ratchet; on an interface-first codebase it is the only check that works.

**3. Test the failure paths, and state invariants as intervals.** For every external call
your code makes, there is a test where that call fails — mechanical, cheap, and the single
highest-yield check we have. For a procedure touching several stores, enumerate the partial
failures: step 3 of 5 fails — what is now true on disk, in the keychain, and in memory, and
how does the next start recover? And write invariants with **both ends**: not "`Create`
writes `PhasePrepared` before calling the provider" (a moment) but "the record exists from
before the write until metadata references the secret" (a span). If you cannot name the
closing event, you do not yet understand the invariant.

> 2026-07-30, `internal/vault`: eighteen tests, `go vet` clean, `golangci-lint` clean,
> `-race` green. An adversarial read then found ten defects, two release-blocking — a
> `Setup` that returned four times while holding its mutex, deadlocking everything after
> it, and a `Create` that deleted the journal record it had just written. None of the
> eighteen made a dependency fail; all four deadlocking returns had zero coverage. The
> criterion that named only the start of the interval bought a test that guarded only the
> start.

**4. Do not let the author of the code be the only author of its tests.** A test written by
the implementer in the same pass encodes the implementer's model, including the parts that
are wrong — the code and the test agree, and are wrong together. Cheapest fix, almost
always worth it: **write acceptance criteria as assertions rather than prose**, in the bead
itself. Expensive and reserved for code where a defect is costly (the vault, the updater,
the transport): have someone who did not write the implementation write the tests from the
spec. That second reading found eight of the ten defects above, for one round trip.

**5. The wire is a party to the contract.** Every JSON-RPC result shape is declared once,
as a JSON Schema in `contracts/`. The renderer's types are **generated** from it (committed,
never hand-edited); the Go side is **validated** against it. `additionalProperties: false`
plus an explicit `required` is what makes it exact — a schema without both is theatre.
Three checks, and the third is the point:

- `npm run contracts:check` (pre-commit) — the committed generated file matches the schema.
- `…_DTOConformsToContract` — the Go struct marshals to something the schema accepts.
- `…_OverTheWireConformsToContract` — **the real result, off the real socket**. A test that
  validates a payload the test itself built proves the struct is well-formed, not that the
  server sends it.

`contracts/` is filled in **as methods are touched** — a method you add or change gets its
schema in the same commit (`nocx-bt3w` tracks the sweep). See `contracts/README.md`.

> 2026-07-31: `vault.status` had never sent `defaultProvider`. The renderer's type declared
> it, the Vault page read it every render, and `SetDefaultProvider` wrote a value nobody
> could read back — so the page showed two providers, neither marked. Both suites were
> green: Go tests decode into anonymous structs naming the fields that test is about (there
> is no assertion for "and nothing else"), and the frontend's hand-written fixtures were
> written _from the interface_, so they contained the field because the renderer wanted it.
> The schema's first run also caught `providers` marshalling as `null` rather than `[]`.

**A soft degrade must be visible in the product, not only in a log.** History failing to
open is a `slog.Warn` while Settings goes on offering a toggle, a retention age and a budget
that govern nothing. A silent degrade the UI contradicts is how a feature that does not
exist survives a release.

## There is no "not ours"

**"Not mine, it was already broken" is not a finding, and it is never an answer.** Whoever
is looking at the broken thing owns it — a red CI job, a failing local test, a defect
noticed in passing, a bead somebody else filed three weeks ago, a feature adjacent to the
one you were sent to build. The repository has one queue and everything in it is ours.

Establishing that a defect predates your branch is still worth doing — but as the FIRST
line of the diagnosis, never as its conclusion. It tells you where the defect lives; it
does not tell you to stop. So the shape of the answer is always: name the failing
assertion or the wrong behaviour exactly, say where it lives (`git diff origin/main...HEAD
-- <path>` settles "did I bring this"), and then fix it. If a bead already owns it, work
that bead — a fifth "another occurrence" note is worth less than one line of fix.

This is about BREAKAGE YOU HAVE ENCOUNTERED, and it does not license widening the task
you were given. New work still comes off the queue in [What to work on
next](#what-to-work-on-next), and a brief still means what it says. The distinction: nobody
asked you to build the adjacent feature, and everybody expects you to fix the adjacent
thing that is broken.

**A rerun is legitimate exactly once**, to see a failure a second time. A green rerun is
not evidence the defect is gone, only that it did not fire. **A flake is a defect, not
weather:** a test that fails one run in five is reporting a real race, in the product or in
itself, and the run that passed is the one that got lucky. Never "fix" it by widening a
timeout or adding a retry — that converts a report into silence, which is the failure mode
the [testing rules](#testing-five-rules-each-bought-by-a-green-suite-over-a-broken-product)
exist to prevent.

**When you genuinely cannot fix it in this session** — the cause sits in a package another
worker is mid-flight in, or the fix is an epic — say that IN THOSE WORDS, with what you
found, what is left and what it would take, and ask. That is a report. "Not ours" is not.

> Bought on 2026-09-03 (`nocx-s6h4x`), on the third occurrence of `nocx-n26p1` and the
> second red CI in one session. Both reds were answered with a correct diagnosis, an occurrence appended to
> an existing bead, and a rerun; neither was fixed. That bead had carried an unverified
> candidate cause since 2026-08-30 — a named line, a named hypothesis and a named way to
> check it — and three sessions running read it, agreed with it, and re-filed it. The
> owner: «Я не хочу больше слышать аргумент "не наше". Это все наше. Чиним.»

## Before you fix anything

A bug report is a symptom, not a mandate to edit. Five checks, in order — skipping them is
how two agents ship two answers to one question.

1. **Already filed?** Area first — it is the only listing short enough to read whole:

   ```bash
   br list --label <area> --status all   # the closed area list is under Backlog invariants
   br search <phrase>                    # then words, for the bead filed in other words
   cm context "<keyword>" --json         # what a past session learned the hard way
   ```

   A hit is not automatically your task — read it. It may be claimed, blocked, or record
   that the behaviour is deliberate. Work the existing bead rather than opening a second.

   **Search for the behaviour, not for your name for it** — this is the same rule as
   [Look for the existing answer](#look-for-the-existing-answer-before-you-write-a-second-one),
   applied to the backlog. Duplicates here are rarely near-copies: `nocx-su4g` said "an
   empty file named `1` is tracked at the repo root" and `nocx-o8p1l` said "a tracked
   empty file named 1 sits at the repository root" — nine days apart, one defect, and the
   second filer had searched. When you cannot phrase it two ways, read the whole area
   listing instead; that is what the area label is for.

2. **Deliberate?** `docs/vision.md` and the owning epic say what is explicitly out. The
   empty "Sessions" panel is a placeholder a comment in `main.ts` declares; "fixing" it
   invents a feature nobody asked for.

3. **Which `AD`?** Check before, not after. A fix that routes PTY bytes through JSON-RPC or
   lets the backend sniff the stream is not a fix.

4. **Decided in an ADR?** `ls docs/decisions/`. Re-deciding a settled question inside a
   bugfix is how it stops being settled.

5. **Is the code reachable?** A file on `main` is not a feature in the product.
   And when you are the one PLANNING the work: a task that adds a Go package lands
   together with the wiring that makes it reachable, or its commit cannot pass the
   deadcode ratchet at all — the gate is the hook, not the brief, so a worker cannot be
   briefed out of it (`nocx-z7s6`; two commits went in with `--no-verify` before anybody
   noticed the plan had made that unavoidable).

   ```bash
   deadcode -filter 'nocx/internal/<pkg>' ./...              # unreachable from main()
   grep -rn "New<Thing>(" --include=*.go . | grep -v _test   # who constructs it?
   ```

   Read `internal/app/app.go` and confirm the thing is wired in. Then check the other
   direction too — rule 2 above: a package can be reachable and still have a dead half.

> 2026-07-26, one session, two failures. PR #11 shows closed-unmerged on GitHub while its
> commit is an ancestor of `main` — an agent reading only the PR would have rebuilt
> thousands of lines. Then, having established the files were on `main`, the next claim was
> "so the vault shipped": `deadcode` reported all 26 of its functions unreachable. The tests
> hid it, and the deferral lived only in a code comment. **A `TODO` in source is not a task
> — file the bead before you write the comment.**

### The five checks gate the brief, not the diff

If you are a coordinator writing a brief, a spec or a plan for somebody else to implement,
**the checks above are yours and they apply before you write it.** The brief is where the
architecture is decided; by the time a worker is editing files, the decision has already
been made and the checks can only confirm it. "I am not touching code" is not an exemption —
it is the moment the exemption costs the most.

A brief that crosses a boundary **names the `AD`s and ADRs it touches and what they already
decided, before it says what to build.** Checked by eye at review, like the commit-message
rule.

> 2026-08-04, one session, three times. The nocxify spec proposed `stty -echo`, parsing away
> echoed regions, and inferring stdin ownership from the byte stream — the three techniques
> ADR-0004 names and rejects, in that order, in one paragraph. Then a brief told a worker to
> shell out to `ss`/`netstat` on the **local** machine, against "Interface-first + DI" and
> against `internal/contentkey`, which is the same per-OS problem already solved in this
> repo. Then a report to the owner claimed nocx deliberately never deploys a binary to a
> remote host, while `architecture.md` defers a Tier-B remote helper, AD-2 names it a build
> target, AD-1 reserved a msg-type for its feed, and `nocx-if6` phase B is that relay.
>
> One cause each time: writing from what the conversation remembered instead of reading the
> binding document for the boundary being crossed. The owner caught all three. The third one
> would have shipped a provider seam the relay had to be forked into.

The anecdote stands; the vocabulary in it does not. Since 2026-08-31 there is no **relay** —
the remote binary is the **helper**, it owns the PTY on the host rather than augmenting a
shell beside it, and `nocx-if6` is closed as superseded. The boundary the third failure
crossed is exactly where it was; only its name changed. The live chain is
`br list --label remote-host`.

## Before you investigate: two checks that beat reasoning

**Search the memories before fighting the environment.** `cm context "<what you are
doing>" --json` costs seconds and is pull-based — nothing surfaces them for you. They
left the tracker on 2026-09-05: `br` has no memory store at all, so they live in
cass-memory, with the raw export kept in `.internal/memories-export.jsonl`.

> A session spent installing Xvfb and rebuilding NixOS twice to run Playwright ended when
> a memory lookup for `e2e` turned up the headless backend plus its port shim — a path needing
> no display, in the repo the whole time.

**When a branch behaves differently from `main`, diff it against `main` first** — before
measuring, instrumenting or theorising:

```bash
git diff origin/main...HEAD -- <path> | grep '^-'
```

> `557e87d` (52 files, +8025/−605) silently dropped one subscription, and the symptom was a
> Playwright click timing out on a visible button. Hours of geometry reasoning; the
> removed-lines diff found it in a minute.

## What to work on next

Asked to "keep going" with no further instruction, this is the whole answer:

```bash
scripts/br-queue.sh
```

It prints two lists: tasks inside epics somebody has actually taken, and standalone
bugs, which legitimately have no epic. It is a script rather than two piped
commands because `br ready` has neither `--parent` nor `--exclude-type` and its
`--json` carries no parent, so both filters are computed — children from
`br show <epic> --json` (`dependents` carries `dependency_type: "parent-child"`),
and "has no parent" from the `parent-child` edge set read once out of
`.beads/issues.jsonl`. Read the script before working around it; the reasoning is
in its header.

**If it returns nothing, that is an answer, not a bug** — every open epic's front is
occupied. Finish something in flight or take a free epic; never widen the query.

- **You may not take a task out of an epic nobody has taken.** If the epic is free, take
  the epic (`br update <epic> --assignee "$(git config user.email)" --status in_progress`),
  then come back for its children.
- **Never take work out of a blocked epic** — it is blocked because the same files are
  moving. The queue script enforces this; going around it via `br list`, `br search` or an
  ID in a document is the failure mode. If a bead is not in `br ready`, do not start it.
- **An epic is assigned, its children are claimed.** Owning an epic means seeing it to its
  DONE WHEN. Never `--claim` an epic bead as though it were a task.
- **`br ready -t epic --unassigned`** lists epics nobody owns and nothing blocks — what you
  can hand to a colleague. Do not flip an epic to `in_progress` to hide it from a task
  listing; the queue script already excludes epics.

### Backlog invariants

- **An epic blocks another only when they touch the same code** — not "this is more
  important" and not "this comes later". `br dep add <blocked-epic> <blocker-epic>`.
  Priority, not blocking, is where importance goes. Several epics available at once is
  normal and wanted.
- **`blocked` is computed, never stored.** You cannot set it; you can only add the edge.
  A blocked epic still prints as `○` — read its `DEPENDS ON` list.
- **An epic is a DAG, not a bag.** Sequence children with `blocks` so the front is ~3:
  count the epic's block in `scripts/br-queue.sh`. `br show <epic> --json` also carries
  a `rollup` of its descendants by status, which is the cheapest read of the same thing.
- **Where a bug goes.** Inside a live deliverable, a child of that epic. Arriving from
  nowhere, **no parent at all** — a standalone bug is legitimate. Filing it under the
  nearest plausible epic is what grew the two area epics that had to be split. If triage
  shows it is a symptom of something structural, it _becomes_ an epic and carries a
  `discovered-from` edge back to the bug.

> 13 of 20 epic-level edges once encoded "not yet" rather than overlap and were removed;
> before that, a bare `br ready` offered 68 issues and the queue was unusable.

### One area label, and a status that is true

Measured 2026-09-02 over 3145 beads (`nocx-e1xzh`): **only 64 of 791 open beads carried
an area label** — 479 had none at all, and another 248 had only a roadmap tag like `mvp`,
which narrows nothing. And **91 of 126 `in_progress` beads had not been touched in over a
week**, the oldest by 41 days. The second number causes the first: when `in_progress`
lies, nobody trusts status; when nobody trusts status, filing is cheaper than searching;
and every new bead makes the next search worse.

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
  strip that fails to show a badge is `ui`, not `content`. Needing two labels usually
  means it is two beads — file them.

- **A label never restates a field.** `epic` (165 beads) and `bug` (3) duplicated
  `issue_type`, which every listing already prints; `assistant-sweep-0830` (42) was a
  date. All three are deleted. `mvp` and `phase-1/2/3` stay: they are roadmap, they are
  orthogonal to area, and the rules above already govern them.

- **`in_progress` means a worker is holding it now** — not "started once", not "nearly
  done". Stopping means setting it back to `open` in the same minute, because an unheld
  bead sitting in `in_progress` is invisible to `br ready` and to every colleague looking
  for work. `nocx-viil` sat `in_progress` for three weeks with its work already shipped —
  `contracts/session.integrationChanged.schema.json`, `frontend/src/integration/status.ts`
  and the generated doc all name the bead — and a worker re-derived that entire surface
  before noticing it existed.

- **An epic's own timestamp is not its liveness**, because an epic does not move when its
  children do. Of 34 epics that looked stale by their own `updated_at`, only 12 were
  stale once the children were counted. Ask the children before believing it:

  ```bash
  br show <epic> --json | jq '.[0].rollup'
  # {"status":"in_progress","descendants":{"closed":3,"in_progress":1,"open":3}}
  ```

  `rollup` is derived, so it cannot drift from the children the way a copied
  timestamp can.

- **Close with evidence a stranger can check.** Name the file, symbol, test or commit —
  "duplicate" and "done" are not reasons. For a duplicate, name the survivor and say what
  makes them one behaviour; two beads touching one file are not duplicates, by the same
  test the epic-blocking rule uses. And re-read the tree before believing a bead's own
  notes: `nocx-6hg2w.25` and `nocx-favvl` both recorded "green but NOT committed, blocked
  on gofumpt" while their commits were already on `main`.

### Creating an epic

1. **Scope it to a deliverable, not a code area.** Can one person be handed this whole and
   finish it? "Persistence" and "Quality gates" were areas — every new bug landed in them,
   so they could never finish.
2. **Unless it is a chore, name what a user can do that they could not before, and the one
   end-to-end check that watches them do it** (rule 2 above). No such sentence means it is
   a chore — label it — or an area of code wearing an epic's clothes.
3. **A criterion that stops being false exactly once**, plus what is deliberately out.
   Enforced by review, not by the tool: `br create -t epic` accepts an epic without a
   heading) fails and creates nothing.
4. **Set the status deliberately** — `open` means free to assign.
5. **`blocks` edges only against epics whose files it collides with.**
6. **Label it** `mvp`, `phase-2`, `phase-3` or `infra`; no `mvp` epic behind a deferred one.

Prefer more, smaller epics — "handed over whole" and "large area" cannot both hold.

### Claiming on a shared backlog

Several people work this repo from their own machines against one shared Dolt database.

```bash
git pull --rebase                   # who took what since your last sync
br ready && br update <id> --claim
br sync --flush-only                 # then commit .beads/issues.jsonl and push
```

**Publish every backlog write immediately** — a create, an edit, an edge, a close — not at
session close. An unpushed bead does not exist for anybody else, and the afternoon it costs
is somebody else's. Batch your writes if you like — `br update` and `br close` take
several ids at once — then commit at the end of the batch.

**A claim is not a lock.** Two clones can claim the same bead; last write wins. The
protocol shrinks the race, it does not close it. Auto-push stays off on purpose (upstream
warns concurrent pushes to a git-protocol Dolt remote can strand history); if races become
routine the fix is a shared sql-server, not a shorter interval (`nocx-wj4`).

## Git authority

Agents have **standing authority to commit and push**. This overrides the "Conservative"
profile in the managed Beads block below — that block defers to repository instructions,
and this is one. Allowed without asking: `git commit`, `git push`, `br close`,
`br sync --flush-only`, running the gates. Branch first if you are on `main`.

**Merging a pull request always requires explicit approval** — in that session, for that
PR. Authority to commit and push is not authority to merge.

**Run the gate CI runs, not a subset of it.** `make ci-full` is every CI job, each in the
environment its job runs in — and the four names below are the whole of `ci.yml`:

```bash
make ci-full            # all four, cheapest first
make ci                 # host-side only: the macOS `backend` job + host frontend gates
./scripts/ci-linux.sh   # `backend-linux`, ubuntu-24.04, both keyring variants
./scripts/ci-frontend.sh # `frontend`, node 24 — frontend/ AND the repo root
./e2e/run-in-container.sh # `e2e`, the same image and the same command CI runs
```

`make ci` alone is **not** the gate, whatever it used to say about itself. It covered one
of four jobs, and a release attempt and its follow-up PR both came back red from a job it
had just reported green (2026-08-10). The three containerized runners are byte-for-byte
their CI counterparts in **software** — the same image, packages, Go toolchain and
command.

**The gate belongs to whoever integrates, and to nobody else.** A worker on a branch runs
the unit tests for the files it changed, and stops there. It does not run `make ci-full`,
the containerized jobs or the e2e suite — not as diligence, not "just to be sure". The
coordinator runs all four, **once, on the merged tree, before `git push` to `main`**,
every time, including when every branch that went into it was green alone.

That is not a weakening: the failure that bought this rule was a push to `main`, and there
it binds exactly as hard as before. What comes off is a cost it never bought — and the
cost is not small. 2026-08-12, three branches, each `ci-full` green on its own tree:
`nocx-qduc`, `nocx-wwz0` and `nocx-dvql`+`nocx-5uu5`, about an hour of wall clock apiece.
The merge of them was red twice, and neither defect could have been seen from any branch
because neither existed on one: a struct literal in a test that predated a new required
dependency (a nil dereference), and a test asserting a mechanism the merge had deleted,
which had been green while asserting a value no local path read. The three per-branch full
runs found nothing the merge run did not.

Three costs, all measured the same afternoon, all invisible to the worker paying them. The
containerized jobs serialize on one Docker daemon and one CPU, so parallel workers each
running four jobs finish later than the same work run in sequence. They mount
`node_modules` as named volumes with no worktree in the name, so concurrent runs break
each other's dependency tree (`nocx-x6z3`) — measured as a pre-commit hook failing on a
package another run was mid-install. And a host-side Go run used to write to the
developer's login keychain on every backend start, which on macOS is a modal dialog
apiece: a full gate per branch was also a dialog storm per branch. That one is fixed —
`New` now refuses a test binary that has not declared whether it may reach the OS keystore
(`nocx-o4hg`) — and it is left written down because the shape recurs: `$HOME` isolation
moves directories and cannot move a per-user OS service, so the next one (Secret Service,
a launchd agent, the system clipboard) will arrive the same way and be just as invisible
to the worker paying for it.

**When the merged gate goes red, send it back to the worker, do not fix it in the
coordinator.** A worker is resumable and still holds why it wrote what it wrote; the
coordinator would be re-deriving that from a diff. **Which means the worktrees stay until
that gate is green** — removing one un-resumes its worker, and the rule above then has
nobody to send anything to. Measured within the hour of writing it: the tidy-up before
`make ci-full` cost exactly that, and the reply had to be re-briefed into a fresh worker
from scratch. The exception is a defect that exists
only in the merge — the two above were exactly that, and belonged to whoever resolved it.

**They are not their counterparts in timing, and no setting will make them so.** Each of
these scripts capped itself to the runner's 4 vCPU until 2026-08-11, on the argument that
capacity was the last gap left. Two things were wrong with it. The first is the machine
underneath, and it differs per image — read the file, not this paragraph:
`.githooks/images/ci-linux` pins `--platform=linux/amd64` **to be** the runner, so on a
Mac it runs emulated; `e2e/Dockerfile` pins no platform **deliberately**, so it builds for
the host and runs native arm64 here and native amd64 on CI. Either way throttling to four
cores does not produce the runner, it produces a third machine unlike either — `nocx-2h08`
is one starved resource in `internal/transport` reporting a 30-second timeout under a
different test name in every environment, including a run that was green on the runner and
red here at the same commit. And the cap worked by keeping timing-dependent specs
reproducible, which is what kept them alive.

So the caps are off by default, and the rule that replaces them is the stronger one:
**a test may not depend on timing.** Wait on an observable state change — a frame, a
record, a DOM state — never on a duration. A spec that needs a slow machine to pass is
broken on a fast one too; it has only not been caught yet. `NOCX_CI_CPUS` and
`NOCX_E2E_CPUS` still cap on demand, for bisecting a suspected concurrency defect. That
is a debugging tool, not the gate.

**`backend` is the one job with no container, and it is the one place local and CI still
disagree.** macos-latest is the target OS, so it cannot be containerized, and a developer
Mac is not that runner: `internal/pty` hangs to its 600 s panic here while green there,
and three tests in `internal/app` and `internal/git/local` fail here and pass there
(nocx-58gq, nocx-65v6). Until those are closed, read a local `backend` red against that
list before believing it — and never the other way round: CI is still the source of truth.

**There is no merge slot any more.** It was `bd merge-slot`, it went with `bd` on
2026-09-05, and `br` has no equivalent — the owner's call not to build one. What it
was for still exists: two agents integrating at once resolve conflicts against a
`main` moving underneath both, and each resolution invalidates the other's. What
protected against that was never a lock either; this file said so — a claim is not
a lock, and the slot only shrank the race. So integrate one at a time by
arrangement, and if that turns out to cost something, file the bead with what it
cost rather than reviving the slot from memory.

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
- **`(<bead-id>)`** at the end of the subject; several when one commit closes several
  (`(nocx-u7wq.1-.5)` for a run). Ids referenced but **not** closed go in the body.
- **No bead for it?** Then there is no task — `br create` takes seconds. **Trivial?** It
  still had a reason, and it is the one nobody can explain in six months.

Checked by eye at review. If that rots, file a `commit-msg` hook rather than dropping it.

## Engineering rules (non-negotiable)

- **Interface-first + DI.** Every module behind an interface, wired at one composition
  root. Depend on abstractions, obey SRP, keep modules trivially replaceable.
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

> 2026-08-05. "Am I in an ssh context" had two derivations — `commandWord(ctx)` on the
> completion side, and `/\bssh\s+/` in the editor. They agreed for every case anyone tried,
> and disagreed on exactly one: `ssh` with no trailing space, which is the state a user is in
> when they press Tab **instead of** the space. So the suppressed surface un-suppressed
> itself at the only moment it mattered and inserted a saved host over the user's choice.
> Underneath it, a whole second suggestion surface — its own list, keys, rendering and accept
> path — had been kept alive beside the completion dropdown, which already rendered the same
> candidates as a row and a ghost. The fix was to delete it, and the bug existed only because
> it had been built rather than found.

### Before you build a UI component: read the kit

**Read [`frontend/src/ui/README.md`](frontend/src/ui/README.md) and list `frontend/src/ui/`
first.** The inventory names every component, its identity classes and its variance.

1. **Does the kit have it?** Import it. A "toggle" is `Checkbox variant="switch"`; a status
   message is `showToast`; a titled group is `Section`/`PageSection`. At 90% fit, add the
   missing variance as a typed `data-*` rather than forking.
2. **Close enough?** Extend that component in `ui/` — the kit grows by variants, not
   near-duplicates.
3. **Genuinely new?** Into `ui/`: one module, one CSS file in `styles/components/`, a
   stable identity class, a test, a row in the README table.

Never build the control **inside the surface** — a hand-rolled `<div class="st-something">`
with its own colours, a bespoke button, a "temporary" status div. Each is a second
vocabulary for one concept, which is the defect two epics (`nocx-pp3y`, `nocx-v0ai`) spent
themselves unwinding.

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
- **Transport:** one WebSocket — raw **binary** data plane + **JSON-RPC 2.0** control
  plane (AD-1).

Next risk to watch: run `br ready`.

## The tracker in one table: `bd` verbs and what replaced them

The `beads-superpowers` plugin stays — its process skills (brainstorming,
writing-plans, test-driven-development, systematic-debugging) are worth more than
its `bd` half is worth losing. But it speaks `bd`, and by its own rule repository
instructions win over skills. **This table is that instruction.** Where a skill
tells you to run a `bd` command, run the right-hand column instead. Two managed
`<!-- BEGIN BEADS INTEGRATION -->` blocks used to sit here, regenerated by `bd`;
nothing regenerates them now, and they described Dolt, so they are gone.

| the skill says                                                                                                         | run instead                                                                     |
| ---------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------- |
| `bd ready` / `list` / `show` / `create` / `update` / `close` / `reopen` / `search` / `stats` / `dep add` / `label add` | the same with `br`                                                              |
| `bd ready` with no argument, meaning "what next"                                                                       | `scripts/br-queue.sh`                                                           |
| `bd prime`                                                                                                             | nothing to run — this file is the context; `br robot-docs guide` for the CLI    |
| `bd remember`                                                                                                          | `cm playbook add "<lesson>"`                                                    |
| `bd memories <word>` / `bd recall`                                                                                     | `cm context "<what you are doing>" --json`                                      |
| `bd purge`                                                                                                             | `br delete <id>` (writes a tombstone)                                           |
| `bd batch` / `bd import -`                                                                                             | `br sync --import-only` from a file; `br update <id1> <id2> …` for bulk edits   |
| `bd export -o <file>`                                                                                                  | `br sync --flush-only` (always to `.beads/issues.jsonl`)                        |
| `bd dolt push` / `bd dolt pull`                                                                                        | `git push` / `git pull` — the JSONL is a tracked file                           |
| `bd merge-slot`                                                                                                        | nothing; it was removed deliberately                                            |
| `bd swarm validate`                                                                                                    | `br show <epic> --json \| jq '.[0].rollup'`                                     |
| `bd update <id> --claim`                                                                                               | the same with `br`; `claim_exclusive: true` refuses a claim another actor holds |
| TodoWrite / TaskCreate / markdown TODO                                                                                 | still forbidden — `br` is the tracker for all work                              |

**The official `br` skill is installed and four of its lines are wrong.** It ships
with the plugin (`beads@beads-rust`, skill `beads:br`) and is worth having, but it
carries `bd`-era leftovers that this file overrides. Checked against
`br config schema` on 0.5.10:

| the skill says                         | the truth                                                                                                                                                                                                       |
| -------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `br config get id.prefix`              | the key is `issue_prefix`                                                                                                                                                                                       |
| `br config set defaults.priority=1`    | the key is `default_priority`                                                                                                                                                                                   |
| `br config set sync.branch beads-sync` | **there is no such key** — the whole schema is `issue_prefix`, `min_hash_length`, `display.color`, `sync.auto_flush`, `sync.auto_import`, `sync.history_enabled`, `no_db`. `br` has no branch-based sync at all |
| Agent Mail `thread_id: bd-###`         | our ids are `nocx-*` and always were                                                                                                                                                                            |

Two more differences worth carrying in your head, because no rename covers them:

- **`br` never runs git.** Nothing syncs the backlog behind your back. After
  finishing work: `br sync --flush-only`, `git add .beads/issues.jsonl`, commit,
  push — the same commit as the code it describes.
- **`br` imports before it reads.** A `git pull` that changes the JSONL is picked
  up by the next `br` command on its own. If both your database and the pulled
  JSONL changed, `br sync --merge` does the three-way; `--force-db`,
  `--force-jsonl` and `--force` (newer timestamp) are the three explicit policies.
