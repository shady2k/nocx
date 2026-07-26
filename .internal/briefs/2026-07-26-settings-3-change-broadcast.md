# Worker brief — SETTINGS-3: settings change broadcast (bead `nocx-lsws`)

## Situation

Your two prerequisites already landed on this branch:

- `fb90d47` — `settings.getSnapshot` returns `{ values, overridden, revision }`. The revision is
  in-memory (`internal/settings/settings.go:495`) and bumps only after a successful mutation
  (`797`, `809`).
- `608ad61` — a control-plane `Dispatcher` (`frontend/src/dispatcher.ts`) now owns request-ID
  allocation, response correlation and notification routing. The old arrangement — `WSClient`
  and `ProfileClient` both listening to every message, with `ProfileClient` starting its IDs at
  100000 to dodge collisions — is gone.

What is missing is the thing those two exist for: **when a setting changes, nothing tells
anyone.**

## Read first

- `/home/dev/repos/nocx/.internal/specs/2026-07-26-tab-and-settings-foundation-design.md`
  — **section A.4 in full**, plus A.1 for the revision semantics you build on.
- `AGENTS.md` — binding. Note AD-1 (one WebSocket: binary data plane + JSON-RPC control plane)
  and AD-7 (server-authoritative state) in `docs/architecture.md`.

## What to build

A backend-originated notification over the existing JSON-RPC control plane. The precedent is
already there: the `exit` notification is handled as a method with no `id`
(`frontend/src/ipc.ts` — find the current site, the file was refactored by `608ad61`).

```json
{
  "jsonrpc": "2.0",
  "method": "settings.changed",
  "params": { "revision": 42, "keys": ["clipboard.osc52Suppressed"] }
}
```

Requirements:

1. **Emitted only after the store operation succeeded.** The event reports committed state, not
   write intent.
2. **Emitted by the settings application service after mutation, not by each WebSocket
   handler.** Otherwise `set`, `reset`, secret set/delete, import and future migration paths
   diverge. This is the whole point — put it where it cannot be forgotten.
3. **Plural `keys`**, so a batch or import operation does not produce a notification storm.
4. **Broadcast to all connected clients.** The server currently tracks per-session subscribers
   only, so this needs a connection registry with a safe unregister on disconnect. That is real
   work; do not fake it with a single stored connection.
5. **Client treats it as invalidation, not as data.** Expected successor revision → refresh; a
   gap → fetch a full snapshot; reconnect → fetch a full snapshot; duplicate or older revision →
   ignore. At this scale refetching the whole small snapshot is correct and far less error-prone
   than carrying values inside notifications. Do not put values in the notification.
6. **Secrets:** a secret change may name the key — presence is already exposed to authorized
   frontend code through `settings.secretExists` — but must never carry a value. Add a test
   proving no secret value can travel in the notification.
7. A frontend `SettingsObserver` seam built on the `Dispatcher`'s typed subscription, which
   owners subscribe to. `SettingsViewImpl` is one consumer; do not make it the only possible one.

A frontend-only event bus is **rejected** and you should not reintroduce one: it leaves every
client except the writer stale, and creates a second owner of a truth the backend already owns —
the failure mode recorded in `nocx-aok`.

## Explicitly not in this task

Do not wire any live-applying consumer (tab placement, font, theme, keybindings, copy-on-select).
Those are separate beads. Your job ends at "the notification exists, is broadcast correctly, and
a frontend observer seam consumes it."

## Files you own

`internal/settings/**`, `internal/transport/**`, `frontend/src/dispatcher.ts`,
`frontend/src/ipc.ts`, `frontend/src/profiles.ts`, a new settings-observer module, plus all their
tests.

Do **not** touch `frontend/src/tabs.ts`, `frontend/src/tab-strip.ts`,
`frontend/src/tab-content.ts`, `frontend/src/terminal-content.ts`,
`frontend/src/connections-content.ts` or `frontend/src/main.ts`'s tab wiring. Another worker owns
the tab lane on a different branch. Escalate rather than cross.

## Bootstrap

```bash
cd frontend && npm ci && cd ..
```

## Verification — required, on the FINAL state of the tree

```bash
gofumpt -l .
golangci-lint run ./...
go test -race -count=1 ./...
cd frontend && npm run format:check && npm run lint && npm run typecheck && npm run test
```

Multi-client transport tests matter most here: prove that two connected clients both receive the
notification, and that a disconnected client's registration is removed without leaking or
panicking.

The Playwright e2e suite is **red on `main`** (13 failures, `nocx-bw2`) and is not in the
per-commit gate. Do not run it, do not chase it, do not claim anything about it.

## Ground rules — two of these were violated last wave, read them twice

- **Do not commit, push or branch.** The coordinator owns git.
- **Do not touch the issue tracker.** No `bd` commands.
- **If you finish early, STOP and report.** Do not start adjacent or follow-up work. If you
  think adjacent work is needed, say so in your report and stop. Last wave two workers silently
  did the next task; it cost a full re-review.
- **Re-run the whole gate on the final state of the tree and paste the real output into your
  report.** Last wave a worker reported "tsc clean" while `tsc --noEmit` failed, because it had
  measured before its last change. A gate claim that does not match the tree is the worst
  failure mode available to you here.
- Report the file list from actual `git status --porcelain` output, pasted, not from memory.
- No `prettier --write` or `gofumpt -w` across the repo; format only what you changed.
- Report numbers, not adjectives.
- **State explicitly anything you could not verify.**
