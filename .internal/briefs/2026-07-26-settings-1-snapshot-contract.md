# Worker brief — SETTINGS-1 (bead `nocx-r2hf`)

## Read first

- Design: `/home/dev/repos/nocx/.internal/specs/2026-07-26-tab-and-settings-foundation-design.md`
  — read **sections A.1 and A.2** in full, plus the "three-bucket rule" section.
- `AGENTS.md` in your worktree — the engineering rules are binding, especially TDD,
  interface-first, and **no backward-compatibility shims**.
- Full task statement: run `bd show nocx-r2hf` — no, you must NOT touch beads. The bead body is
  reproduced below instead.

## The task

Replace `settings.getAll` with `settings.getSnapshot`, returning:

```ts
interface SettingsSnapshot {
  values: Record<string, unknown> // effective, non-secret (as today)
  overridden: string[] // non-secret keys that have a STORED override
  revision: number // monotonic per backend instance
}
```

Why each field:

- `overridden` is the fact the wire currently drops. Export/import is already filed
  (`nocx-6ek.3`) and needs it — otherwise an exported profile silently pins every default and
  freezes the user against future default changes.
- `revision` is an **in-memory instance epoch**, bumped only after a _successful_
  `set` / `reset` / `secretSet` / `secretDelete`. It is **not persisted**. Clients always
  fetch a full snapshot on connect, so a counter that resets on restart is harmless. Do not
  persist it and do not invent an `instanceId` unless you find a concrete need — if you do,
  say so rather than adding it silently.

**Secret keys must be absent from BOTH `values` and `overridden`.** Including them turns the
snapshot into an existence oracle. Presence stays available only through the existing
`settings.secretExists`. Add a test that proves a secret-class key appears in neither array.

Rejected alternative you should not "improve" the design back into: per-key
`{value, source}`. `source` invites profiles, policies, workspace overrides, environment
overrides and inheritance — none of which exist. A membership set states exactly what the
registry knows today.

**Rename, do not add an alias.** `getAll` is deleted. `AGENTS.md` forbids compatibility
shims; this is greenfield. Its name is also already misleading (it excludes secrets).

## Also fix, in the same code path (bead `nocx-q07f`, P1)

`frontend/src/settings.ts:319-330` — `saveSetting()` awaits `setSetting()` and then calls
`rerenderRow(key)`, but **never writes the saved value into `this.values`**. `rerenderRow`
rebuilds the control through `renderControl`, whose renderers read `this.values[decl.key]`
(`renderToggle` uses `input.checked = !!this.values[decl.key]`). So a **successful** save
re-renders from the pre-save value and visually snaps back, for toggle, text, number and
select alike.

Secondary defect in the same method: on a validation **failure** the rerender also discards
the value the user typed, so they have to retype from scratch. Preserve the rejected input.

Write a failing test for each of these two before fixing them.

## Files you own

- `internal/settings/**`
- `internal/transport/` — only the settings JSON-RPC handlers
- `frontend/src/profiles.ts` — only the settings client methods
- `frontend/src/settings.ts` and `frontend/src/settings.test.ts`
- any new Go or TS test files you need

Do **not** touch `frontend/src/tabs.ts`, `frontend/src/ipc.ts`, `frontend/src/main.ts`,
`frontend/src/style.css`, or anything under `frontend/src/renderers/` or
`frontend/src/scrollback/`. Another worker owns the tab files, and the control-plane
dispatcher (`ipc.ts`) is a separate task. If you believe you must cross that boundary,
**escalate instead of crossing it**.

Do not change **where** settings are mounted. The move out of the sidebar panel is a
different task.

## Invariant that must survive

The settings screen is **generated** from declarations. After your change there must still be
**no literal setting key anywhere in the frontend**, and adding a setting must still be one
`MustRegister*` call in Go with zero frontend changes. If your change makes the frontend know
a specific key, it is wrong.

## Bootstrap in your worktree

```bash
cd frontend && npm ci && cd ..
```

Go module cache is shared, so Go needs nothing.

## Verification — run all of it, in your own worktree

You are in an isolated worktree, so whole-project gates are safe here and are **required**:

```bash
gofumpt -l .                      # must print nothing
golangci-lint run ./...
go test -race -count=1 ./...
cd frontend && npm run format:check && npm run lint && npm run typecheck && npm run test
```

**Baseline before blame.** The Playwright e2e suite is **red on `main`** — 13 tests fail
(`nocx-bw2`) and Playwright is not in the per-commit gate. Do not run it, do not chase it,
and do not claim anything about it. The Go and vitest suites should be green at your base
commit; if one fails, prove whether it is pre-existing before attributing it to your change:

```bash
git stash -u && <run the gate> && git stash pop
```

## Ground rules

- **Do not commit. Do not push. Do not create a branch.** The coordinator owns git.
- **Do not touch the issue tracker.** No `bd` commands at all — the coordinator owns beads.
- Do not run `prettier --write` or `gofumpt -w` across the repo. Format only the files you
  changed.
- TDD: red → green → refactor. Write the failing test first, for both the contract change and
  the two defects.
- Report **numbers, not adjectives**: test counts before and after, every lint suppression you
  added with its justification, and every problem you spotted and deliberately left alone.
- If a gate fails for a reason you did not cause, say so with the evidence, do not paper over it.
- **State explicitly anything you could not verify.** Silence there will be read as "nothing to
  report", and that has burned us before.

## When you are done

Report through `worker_done` exactly as your dispatch preamble instructs, including the
`taskId`, `dispatchId` and coordinator handle it gave you. In the body include:

1. Files changed, with the shape of the change.
2. The new RPC contract as it is actually implemented (field names and types).
3. Gate output — the real numbers.
4. Anything you could not verify, and anything you deliberately left.
