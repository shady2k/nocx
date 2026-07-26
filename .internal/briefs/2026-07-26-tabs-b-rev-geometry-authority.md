# Worker brief — TABS-B revision: make the geometry authority real (bead `nocx-hklg`)

## Situation

The `TabContent` seam already exists on this branch (commits `6031e89`, `21fd7f6`) and is
good: branded identities, `AbortSignal` lifecycle, idempotent dispose, inert host, MRU close,
`managerView` deleted, application health still owned by `TerminalContent.ready`.

**One part of it is a façade, and that is your entire task.**

`viewportChanged()` at `frontend/src/terminal-content.ts:534` is a no-op — it stores the value
and returns. Its own comment admits it: _"The renderer observes its own container for resize"_.
Its only non-test call site anywhere is `terminal-content.ts:517`, which is `TerminalContent`
replaying to itself. **Nothing calls it from outside.** `ResizeObserver` and
`getBoundingClientRect` appear only in `terminal-content.ts`; `tab-strip.ts` and `tabs.ts`
contain neither.

So content still measures its own container. Design section B.5 forbids exactly that:
_"Content MUST NOT interpret container geometry itself."_ The placement layer does not hold the
authority the whole extraction exists to give it — and the configurable-placement epic
(`nocx-d3q`, vertical tabs) depends on that authority. Shipping as-is means reopening this seam
later, which the design was explicitly ordered to prevent.

Note how it escaped: an unused no-op typechecks and breaks no test. The gate was green. It is
only catchable by asking **who calls it**. Keep that in mind about your own work.

## Read first

- `/home/dev/repos/nocx/.internal/specs/2026-07-26-tab-and-settings-foundation-design.md`
  — **section B.5 in full**, and B.6 for the lifecycle rules the delivery interacts with.
- `AGENTS.md` — binding. TDD, SRP, interface-first, no compatibility shims.

## What to build

Three owners, three quantities, none reaching into another:

```
DOM/layout change
  → the presentation layer measures the allocated viewport in CSS pixels
  → TabContent.viewportChanged({ width, height, devicePixelRatio })
  → TerminalContent asks TerminalRenderer to fit that viewport
  → TerminalRenderer computes cols/rows from real cell metrics
  → TerminalContent debounces and sends the PTY resize
```

- The **presentation layer** owns the rectangle and is the only place that measures.
  `ResizeObserver` is a fine mechanism — it belongs there, not in content.
- The **renderer** stays authoritative for converting a given rectangle into cols/rows via cell
  metrics. It may still observe things placement cannot express (font loading, glyph metrics,
  device-pixel-ratio changes), but those recompute the grid **within the last authoritative
  viewport** — they never redefine the viewport.
- **`TerminalContent`** keeps its own PTY resize debounce. DOM batching and remote SIGWINCH
  throttling solve different problems; do not collapse them into one.

Remove `ResizeObserver` and `getBoundingClientRect` from `TerminalContent`. If you believe one
must stay, you must justify it explicitly in your report rather than leaving it silently.

### Delivery rules — all of these need tests

- No viewport callback before `mount` starts.
- Raw observer callbacks coalesced per animation frame; content receives the latest stable
  rectangle.
- Equal consecutive rectangles may be suppressed.
- The latest viewport is replayed after an asynchronous `mount` completes.
- No callbacks after `dispose`.
- A hidden or inactive tab is never sent a misleading zero rectangle.
- Activation delivers the current real rectangle **before** focus.

## Your acceptance test, stated as a question

**Who calls `viewportChanged`?** Your work is not done until the answer is "the presentation
layer, and a test fails if it stops doing so." Write that test. A test that only asserts
`viewportChanged` was implemented is worthless — the façade already satisfies that.

## Files you own

`frontend/src/tab-strip.ts`, `frontend/src/tabs.ts`, `frontend/src/terminal-content.ts`,
`frontend/src/tab-content.ts`, `frontend/src/connections-content.ts`,
`frontend/src/renderers/**` if the renderer's fit contract genuinely needs changing, plus all
their tests.

Do **not** touch `frontend/src/settings.ts`, `frontend/src/profiles.ts`,
`frontend/src/dispatcher.ts`, `frontend/src/ipc.ts` or anything under `internal/`. Another
worker owns the settings and transport lane on a different branch. Escalate rather than cross.

## Bootstrap

```bash
cd frontend && npm ci && cd ..
```

## Verification — required, on the FINAL state of the tree

```bash
cd frontend && npm run format:check && npm run lint && npm run typecheck && npm run test
cd .. && gofumpt -l . && golangci-lint run ./... && go test -race -count=1 ./...
```

The Playwright e2e suite is **red on `main`** (13 failures, `nocx-bw2`) and is not in the
per-commit gate. Do not run it, do not chase it, do not claim anything about it.

## Ground rules — two of these were violated last wave, read them twice

- **Do not commit, push or branch.** The coordinator owns git.
- **Do not touch the issue tracker.** No `bd` commands.
- **If you finish early, STOP and report.** Do not start adjacent or follow-up work. If you
  think adjacent work is needed, say so in your report and stop. Last wave two workers silently
  did the next task; it cost a full re-review and produced this brief.
- **Re-run the whole gate on the final state of the tree and paste the real output into your
  report.** Last wave a worker reported "tsc clean" while `tsc --noEmit` failed, because it had
  measured before its last change. A gate claim that does not match the tree is the worst
  failure mode available to you here.
- Report the file list from actual `git status --porcelain` output, pasted, not from memory.
- No `prettier --write` or `gofumpt -w` across the repo; format only what you changed.
- Report numbers, not adjectives.
- **State explicitly anything you could not verify** — including whatever `ResizeObserver` and
  real layout behaviour cannot be exercised under jsdom, and how you compensated.

---

## ADDENDUM from the coordinator — read this, it changes the shape of the task

The previous worker on this bead **did disclose** this gap, and gave a concrete technical
reason that the body of this brief failed to mention. Its report said:

> Renderer-facing viewport fitting (`fit()`) is not implemented: the renderer API has no method
> for external viewport delivery; `viewportChanged` stores and replays the value but relies on
> the renderer's internal `ResizeObserver`.

So the missing piece is not laziness — **`TerminalRenderer` has no entry point that accepts an
externally supplied rectangle.** Treat extending that interface as part of this task:

- Add an explicit fit-to-viewport entry point to the `TerminalRenderer` interface
  (`frontend/src/renderers/types.ts`) and implement it for the xterm renderer. Check what wterm
  can honour and report it — the interface is switchable by design
  (`docs/decisions/0001-xterm-js-as-vt-frontend.md`), and `nocx-au6` already records that
  `TerminalRenderer` advertises capabilities wterm does not have. Do not make that worse: if
  wterm cannot honour it, say so explicitly rather than pretending.
- Only then can `TerminalContent` stop observing its own container and become a pass-through
  from the pushed rectangle to the renderer's fit call.
- The renderer keeps its own internal observation for things placement cannot express (font
  loading, glyph metrics, device-pixel-ratio); those recompute the grid **within the last
  authoritative viewport** and never redefine it.

If, after reading the renderer code, you conclude the interface change is larger than this task
should carry, **escalate with your reasoning rather than shipping another no-op**. An honest
escalation is the correct outcome; a stub that typechecks is not.
