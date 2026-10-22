# Brief — nocx-3779: the editor must say which machine Enter will reach

You are a supervised worker. Read this whole file before touching anything.

## Ground rules

- **Do not commit, push, or create a branch.** The coordinator integrates.
- **Do not touch `bd`.** Beads lives in a Dolt database git does not carry.
- **Do not run repo-wide gates.** **Do run**, from `frontend/`:
  `./node_modules/.bin/tsc --noEmit`, `npx eslint src/`, and
  `npm test -- --run` scoped to the files you touched. The type-check is not
  optional: vitest transpiles without type-checking, so a green suite can sit
  on top of a file that does not compile.
- You own `frontend/src/editor.ts`, `frontend/src/terminal-content.ts`,
  `frontend/src/style.css` and their tests. No other worker is live.
- Report **numbers, not adjectives**.
- Heartbeat at every phase change (bottom of this file).

## Baseline

`npm test` is green: 100 files, 1766 tests, ~19s. Say the numbers before and
after in your report.

## The gap

A finished block tells you where it ran. The prompt you are about to press
Enter on does not.

- `frontend/src/scrollback/blocks.ts:157` renders a `cmd-header-location` chip
  (`<user>@<lan-ip>`) into every finished block's header.
- The string comes from `terminal-content.ts:862`, which calls `locationLine()`
  and passes it to `blockManager.setLocation(...)` — **only when `sshOpts` is
  set**, so a local session correctly gets an empty string.
- `frontend/src/editor.ts:253-259` builds the live prompt's chrome row from
  `cwdChip` and `timeChip` and nothing else.

So the user reads the host on every command they have **already** run, and sees
only a folder icon on the one whose destination still matters. The owner hit
this on the first working day of SSH blocks.

## What to build

The prompt's chrome row gains the same location chip, fed by the same source of
truth. Do not compute the string a second way — if `locationLine()` is not
reachable from where the editor is updated, route the existing value rather than
recomputing it. Two places deriving "which host" independently is how they start
disagreeing.

Read `frontend/src/ui/README.md` and use the kit. The block header's chip is
`nocx-chip nocx-chip-muted`; match it. A surface may **place** a kit component
and may never **repaint** it — no new colours, borders or fonts in the editor's
CSS for this.

**A local session must not grow a chip.** No `localhost`, no `local`, no empty
bordered box. Nothing extra: the absence is the information.

## The hard part is the failure edge, not the chip

`.internal/specs/2026-08-03-nocxify-design.md` §8.2 is tracked and in this
worktree. Read it. Its binding sentence:

> when markers stop, the rail says _inner context unknown_ immediately. It must
> never keep rendering the last trusted cwd as though it were current.

That is the whole point of the feature. A chip that keeps showing
`<user>@<lan-ip>` after the shell stopped answering is worse than no chip,
because it is confidently wrong next to an irreversible action — and running the
right command on the wrong machine is precisely what `nocx-uahp` exists to
prevent.

`frontend/src/input-state.ts` already models ownership and trust
(`PROMPT_READY`, `RUNNING_RAW`, `trusted`). Use what is there. When the editor
is shown without trusted markers, the chip must render an explicit unknown
state — **not** be hidden, because an absent chip reads as "local", which is a
different lie.

## Explicitly out of scope

The full per-facet context rail from §8.2 — profile, jump route, privilege,
integration tier, each with its own confidence. That waits on `nocx-uahp`'s
environment identity, and inventing a parallel model here would have to be
unwound later. Location plus its honest unknown state is the whole task.

## Test first

Red before green. `frontend/src/scrollback/blocks.test.ts` shows how a chip is
asserted in this codebase; `editor.test.ts` covers the editor. Assert:

- an SSH session's prompt shows the location chip with the same text the block
  header would show;
- a local session's prompt has **no** location chip at all;
- when trust is lost, the chip renders the unknown state and does **not** keep
  the previous host;
- the chip uses the kit's identity classes rather than bespoke ones.

Note honestly in your report that jsdom does not compute layout, so none of
these tests prove the chip is positioned correctly — that is a separate visual
concern (`nocx-a44m` records the same limitation for the block header).

## When you are done

```bash
orca orchestration send --type worker_done --subject "<one-line status>" \
  --body "<what changed, test counts before/after, how the unknown state is reached, anything you could not verify>" \
  --task-id <TASK_ID> --dispatch-id <DISPATCH_ID> --outcome succeeded \
  --files-modified "<paths>" --json
```

`--outcome failed` if you did not finish.

```bash
orca orchestration send --type heartbeat --subject alive \
  --task-id <TASK_ID> --dispatch-id <DISPATCH_ID> --phase "<reading|red|green|verifying>" --json
```
