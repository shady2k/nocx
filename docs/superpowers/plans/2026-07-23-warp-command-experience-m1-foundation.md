# Warp Command Experience — Milestone 1: Input-ownership foundation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent nocx-4ff.1`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** A tested, fail-open input-ownership state machine (`RAW / PROMPT_READY / RUNNING_RAW / ALT_SCREEN`) driven by one OSC 133 handler that snapshots cursor facts, observed per tab — no user-facing input change yet.

**Architecture:** `XtermRenderer` owns a single `registerOscHandler(133)` that parses the marker, snapshots the cursor (absolute line, column, active buffer) at parse time, and fans the enriched event out to N subscribers. A pure reducer in `input-state.ts` turns marker/buffer/submit/reset/exit events into the four-state machine with explicit validation and resync. A thin controller wired in `tabs.ts` subscribes per tab and exposes the current state (observe-only this milestone).

**Tech Stack:** TypeScript (strict), xterm.js `^5.5.0` (`allowProposedApi: true` already set), vitest `^3.2.7`.

## Global Constraints

- **AD-6 (renderer is byte-blind):** the renderer surfaces parsed OSC facts and holds no policy; the backend never parses OSC. Keep policy above the renderer boundary.
- **ADR-0004 fail-open invariant:** absent/malformed/nested markers MUST degrade to a plain working terminal in state `RAW`; never trap input.
- **Enhanced state ONLY from markers/alt-buffer:** never infer "process reading stdin" from bytes or termios.
- **Untrusted shell output:** any text derived from the terminal is untrusted — never `innerHTML`; this milestone renders no such text but the rule stands for consumers.
- **TypeScript strict:** `cd frontend && npm run typecheck` (`tsc --noEmit`) MUST pass. Tests: `cd frontend && npm run test` (`vitest run`).
- **Setup:** `frontend/node_modules` is not installed in a fresh worktree — run `cd frontend && npm install` once before the first test run.

---

### Task 1: Single OSC 133 handler with cursor snapshot + fan-out

**Files:**
- Modify: `frontend/src/renderers/types.ts` (enrich the marker event + callback type)
- Modify: `frontend/src/renderers/xterm.ts:268-274` (`onCommandMarker`), plus mount/dispose
- Test: `frontend/src/renderers/xterm.test.ts`

**Interfaces:**
- Consumes: existing `parseOsc133(payload): CommandMarker | null` (`xterm.ts:89`), `CommandMarker { kind:'A'|'B'|'C'|'D'; exitCode?: number }` (`types.ts`).
- Produces:
  ```ts
  // types.ts
  export interface CommandMarkerEvent extends CommandMarker {
    line: number              // absolute buffer line: baseY + cursorY
    col: number               // cursorX
    buffer: 'normal' | 'alternate'
  }
  export type CommandMarkerCallback = (event: CommandMarkerEvent) => void
  ```
  `onCommandMarker(cb: CommandMarkerCallback): void` — registers ONE parser handler lazily on first subscribe, appends `cb` to a subscriber list, fans out to all. Registration disposed in `dispose()`.

**Acceptance Criteria:**
- Exactly one OSC 133 parser handler is registered regardless of how many `onCommandMarker` subscribers there are.
- Each subscriber receives `{kind, exitCode?, line, col, buffer}` with the cursor snapshot taken at parse time.
- Disposing the renderer disposes the OSC 133 registration.
- `tabs.ts`'s existing `onCommandMarker` usage (reads `.kind` / `.exitCode`) still compiles and runs.

- [ ] **Step 1: Write the failing test** — two subscribers both receive one enriched event per marker (`term.write` is async, so wait for the fan-out, don't assert synchronously).

```ts
// frontend/src/renderers/xterm.test.ts  (add)
import { describe, it, expect, vi } from 'vitest'
import { XtermRenderer } from './xterm'
import type { CommandMarkerEvent } from './types'

describe('onCommandMarker fan-out', () => {
  it('fans out one enriched event per marker to every subscriber', async () => {
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    const a = vi.fn()
    let resolveDone: () => void
    const done = new Promise<void>((res) => { resolveDone = res })
    const b = vi.fn<[CommandMarkerEvent]>(() => resolveDone())
    r.onCommandMarker(a)
    r.onCommandMarker(b)

    // Drive an OSC 133;D;0 through the real parser; write() is async.
    r.write('\x1b]133;D;0\x07')
    await done

    expect(a).toHaveBeenCalledTimes(1)
    expect(b).toHaveBeenCalledTimes(1)
    const ev = a.mock.calls[0][0] as CommandMarkerEvent
    expect(ev.kind).toBe('D')
    expect(ev.exitCode).toBe(0)
    expect(ev.buffer).toBe('normal')
    expect(typeof ev.line).toBe('number')
    expect(typeof ev.col).toBe('number')
    r.dispose()
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd frontend && npm run test -- xterm.test.ts`
Expected: FAIL — current `onCommandMarker` fires `{kind, exitCode?}` without `line/col/buffer`, and registers a fresh handler per call.

- [ ] **Step 3: Implement the enriched type**

```ts
// frontend/src/renderers/types.ts — replace the CommandMarkerCallback block
export interface CommandMarkerEvent extends CommandMarker {
  line: number
  col: number
  buffer: 'normal' | 'alternate'
}
// The VT frontend snapshots the cursor at OSC-133 parse time and fans the
// enriched event out to every subscriber. xterm.js only; @wterm/dom never fires.
export type CommandMarkerCallback = (event: CommandMarkerEvent) => void
```

- [ ] **Step 4: Implement the single fan-out handler**

```ts
// frontend/src/renderers/xterm.ts
// add fields on XtermRenderer:
private commandMarkerSubs: CommandMarkerCallback[] = []
private osc133Disposable?: { dispose(): void }

// replace onCommandMarker (was lines 268-274):
onCommandMarker(cb: CommandMarkerCallback): void {
  this.commandMarkerSubs.push(cb)
  if (this.osc133Disposable || !this.term) return
  this.osc133Disposable = this.term.parser.registerOscHandler(133, (data: string) => {
    const marker = parseOsc133(data)
    if (marker && this.term) {
      const buf = this.term.buffer.active
      const event: CommandMarkerEvent = {
        ...marker,
        line: buf.baseY + buf.cursorY,
        col: buf.cursorX,
        buffer: buf.type,
      }
      for (const sub of this.commandMarkerSubs) sub(event)
    }
    return false
  })
}
```

Then in `dispose()` add: `this.osc133Disposable?.dispose(); this.osc133Disposable = undefined; this.commandMarkerSubs = []`. Import `CommandMarkerEvent` in the types import block at the top of `xterm.ts`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd frontend && npm run test -- xterm.test.ts && npm run typecheck`
Expected: PASS; typecheck clean (tabs.ts still compiles — `CommandMarkerEvent extends CommandMarker`).

- [ ] **Step 6: Commit**

```bash
git add frontend/src/renderers/types.ts frontend/src/renderers/xterm.ts frontend/src/renderers/xterm.test.ts
git commit -m "feat(render): single OSC 133 handler with cursor snapshot + fan-out (nocx-4ff.1)"
```

---

### Task 2: Pure state-machine reducer — the clean cycle

**Files:**
- Create: `frontend/src/input-state.ts`
- Test: `frontend/src/input-state.test.ts`

**Interfaces:**
- Consumes: nothing (pure module).
- Produces:
  ```ts
  export type InputState = 'RAW' | 'PROMPT_READY' | 'RUNNING_RAW' | 'ALT_SCREEN'
  export type InputEvent =
    | { type: 'marker'; kind: 'A' | 'B' | 'C' | 'D' }
    | { type: 'buffer'; buffer: 'normal' | 'alternate' }
    | { type: 'submit' }
    | { type: 'reset' }
    | { type: 'exit' }
  export interface Machine { state: InputState; trusted: boolean }
  export function initialMachine(): Machine
  export function reduce(m: Machine, e: InputEvent): Machine
  ```
  `trusted` = the current prompt→C→D cycle arrived through a clean `A → (B) → C` path; consumers (blocks/re-run) gate on it later. `PROMPT_READY` is the ONLY state in which nocx may own keyboard input; all others route raw to the PTY.

**Acceptance Criteria:**
- Clean cycle: from `RAW`, `A` → `PROMPT_READY` (trusted), `C` → `RUNNING_RAW`, `D` → `RAW`, next `A` → `PROMPT_READY`.
- `B` in `PROMPT_READY` is idempotent (stays `PROMPT_READY`, trusted).
- `buffer:'alternate'` → `ALT_SCREEN` from any state; `buffer:'normal'` → `RAW`.
- `submit` from `PROMPT_READY` → `RUNNING_RAW`.
- `reduce` is pure (returns a new object; never mutates input).

- [ ] **Step 1: Write the failing tests**

```ts
// frontend/src/input-state.test.ts
import { describe, it, expect } from 'vitest'
import { initialMachine, reduce, type Machine, type InputEvent } from './input-state'

const run = (evs: InputEvent[], m: Machine = initialMachine()) =>
  evs.reduce(reduce, m)

describe('input-state clean cycle', () => {
  it('walks RAW → PROMPT_READY → RUNNING_RAW → RAW', () => {
    const a = reduce(initialMachine(), { type: 'marker', kind: 'A' })
    expect(a).toEqual({ state: 'PROMPT_READY', trusted: true })
    const b = reduce(a, { type: 'marker', kind: 'B' })
    expect(b).toEqual({ state: 'PROMPT_READY', trusted: true })
    const c = reduce(b, { type: 'marker', kind: 'C' })
    expect(c.state).toBe('RUNNING_RAW')
    const d = reduce(c, { type: 'marker', kind: 'D' })
    expect(d.state).toBe('RAW')
    const a2 = reduce(d, { type: 'marker', kind: 'A' })
    expect(a2.state).toBe('PROMPT_READY')
  })

  it('alt-buffer wins from any state and normal returns to RAW', () => {
    const alt = run([{ type: 'marker', kind: 'A' }, { type: 'buffer', buffer: 'alternate' }])
    expect(alt.state).toBe('ALT_SCREEN')
    const back = reduce(alt, { type: 'buffer', buffer: 'normal' })
    expect(back.state).toBe('RAW')
  })

  it('submit from PROMPT_READY enters RUNNING_RAW', () => {
    const pr = reduce(initialMachine(), { type: 'marker', kind: 'A' })
    expect(reduce(pr, { type: 'submit' }).state).toBe('RUNNING_RAW')
  })

  it('does not mutate its input', () => {
    const m = initialMachine()
    reduce(m, { type: 'marker', kind: 'A' })
    expect(m).toEqual({ state: 'RAW', trusted: false })
  })
})
```

- [ ] **Step 2: Run to verify failure**

Run: `cd frontend && npm run test -- input-state.test.ts`
Expected: FAIL — `Cannot find module './input-state'`.

- [ ] **Step 3: Implement the reducer (clean cycle only)**

```ts
// frontend/src/input-state.ts
// Input-ownership state machine (ADR-0004 §1). nocx owns keyboard input ONLY in
// PROMPT_READY; every other state routes keys raw to the PTY. Enhanced states are
// reachable ONLY from OSC 133 markers and the xterm alt-buffer event — never
// inferred from bytes/termios. Fail-open: anything unexpected falls back to RAW.
export type InputState = 'RAW' | 'PROMPT_READY' | 'RUNNING_RAW' | 'ALT_SCREEN'

export type InputEvent =
  | { type: 'marker'; kind: 'A' | 'B' | 'C' | 'D' }
  | { type: 'buffer'; buffer: 'normal' | 'alternate' }
  | { type: 'submit' }
  | { type: 'reset' }
  | { type: 'exit' }

export interface Machine {
  state: InputState
  // The current prompt→C→D cycle reached C through a clean A/B. Consumers gate
  // block actions (e.g. re-run) on this; anomalies clear it (Task 3).
  trusted: boolean
}

export function initialMachine(): Machine {
  return { state: 'RAW', trusted: false }
}

export function reduce(m: Machine, e: InputEvent): Machine {
  switch (e.type) {
    case 'buffer':
      return e.buffer === 'alternate'
        ? { state: 'ALT_SCREEN', trusted: false }
        : { state: 'RAW', trusted: false }
    case 'reset':
    case 'exit':
      return { state: 'RAW', trusted: false }
    case 'submit':
      return { state: 'RUNNING_RAW', trusted: m.trusted }
    case 'marker':
      switch (e.kind) {
        case 'A':
          return { state: 'PROMPT_READY', trusted: true }
        case 'B':
          return { state: 'PROMPT_READY', trusted: m.trusted }
        case 'C':
          return { state: 'RUNNING_RAW', trusted: m.trusted }
        case 'D':
          return { state: 'RAW', trusted: m.trusted }
      }
  }
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd frontend && npm run test -- input-state.test.ts && npm run typecheck`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/input-state.ts frontend/src/input-state.test.ts
git commit -m "feat(input): pure input-ownership reducer, clean cycle (nocx-4ff.1)"
```

---

### Task 3: Reducer hardening — validation, resync, nesting, malformed

**Files:**
- Modify: `frontend/src/input-state.ts` (validation branches in `reduce`)
- Test: `frontend/src/input-state.test.ts` (add cases)

**Interfaces:**
- Consumes / Produces: same signatures as Task 2. Behaviour is refined; `trusted` now clears on anomalies.

**Acceptance Criteria:**
- `C` without a preceding clean `A` (orphan or nested) → `RUNNING_RAW` but `trusted:false`.
- `D` while not `RUNNING_RAW` (orphan, e.g. empty Enter) → state unchanged, no throw.
- `A` while `RUNNING_RAW` (previous command interrupted / nested prompt) → `PROMPT_READY` but `trusted:false`.
- `B` while not `PROMPT_READY` → `PROMPT_READY`, `trusted:false`.
- Any `D` carrying no exit code is a marker-only completion; the reducer does not invent success (state handling only — exit code lives on the event, Task 1).

- [ ] **Step 1: Write the failing tests**

```ts
// frontend/src/input-state.test.ts  (add)
describe('input-state hardening / resync', () => {
  it('orphan C (no prior A) is RUNNING_RAW but untrusted', () => {
    const c = reduce(initialMachine(), { type: 'marker', kind: 'C' })
    expect(c).toEqual({ state: 'RUNNING_RAW', trusted: false })
  })

  it('orphan D (empty Enter, not running) leaves state unchanged', () => {
    const pr = reduce(initialMachine(), { type: 'marker', kind: 'A' }) // PROMPT_READY
    const d = reduce(pr, { type: 'marker', kind: 'D' })
    expect(d.state).toBe('PROMPT_READY')
  })

  it('A interrupting a running command yields untrusted PROMPT_READY', () => {
    const running = run([{ type: 'marker', kind: 'A' }, { type: 'marker', kind: 'C' }])
    const a = reduce(running, { type: 'marker', kind: 'A' })
    expect(a).toEqual({ state: 'PROMPT_READY', trusted: false })
  })

  it('B without a prompt is untrusted PROMPT_READY', () => {
    const b = reduce(initialMachine(), { type: 'marker', kind: 'B' })
    expect(b).toEqual({ state: 'PROMPT_READY', trusted: false })
  })
})
```

- [ ] **Step 2: Run to verify failure**

Run: `cd frontend && npm run test -- input-state.test.ts`
Expected: FAIL — current reducer treats `C` as trusted, `D` unconditionally → `RAW`, `B` always trusted.

- [ ] **Step 3: Implement the validating `marker` branch**

```ts
// frontend/src/input-state.ts — replace the `case 'marker'` block
    case 'marker':
      switch (e.kind) {
        case 'A':
          // Fresh prompt. Trusted only when we arrived cleanly (from RAW after a
          // finished command or from initial). An A that interrupts RUNNING_RAW
          // (no D, or a nested prompt) is a resync: PROMPT_READY but untrusted.
          return { state: 'PROMPT_READY', trusted: m.state !== 'RUNNING_RAW' }
        case 'B':
          // B only means "input ready" when we are already at a prompt.
          return { state: 'PROMPT_READY', trusted: m.state === 'PROMPT_READY' && m.trusted }
        case 'C':
          // Command start. Trusted only if a clean prompt preceded it; an orphan
          // or nested C runs raw but disables downstream actions.
          return { state: 'RUNNING_RAW', trusted: m.state === 'PROMPT_READY' && m.trusted }
        case 'D':
          // Finished — only meaningful while a command is running. Orphan D
          // (e.g. empty Enter emits D with no preceding C) is ignored.
          return m.state === 'RUNNING_RAW' ? { state: 'RAW', trusted: m.trusted } : m
      }
```

- [ ] **Step 4: Run to verify pass**

Run: `cd frontend && npm run test -- input-state.test.ts && npm run typecheck`
Expected: PASS (both Task 2 and Task 3 suites green).

- [ ] **Step 5: Commit**

```bash
git add frontend/src/input-state.ts frontend/src/input-state.test.ts
git commit -m "feat(input): validate/resync markers — orphan, nested, malformed (nocx-4ff.1)"
```

---

### Task 4: Per-tab controller wired into tabs (observe-only)

**Files:**
- Modify: `frontend/src/input-state.ts` (add `InputStateController`)
- Modify: `frontend/src/tabs.ts` (construct per tab; feed marker/buffer/reset/exit; log transitions)
- Test: `frontend/src/input-state.test.ts` (controller against a fake feed)

**Interfaces:**
- Consumes: `reduce`, `initialMachine`, `Machine`, `InputEvent` (Tasks 2-3); `renderer.onCommandMarker`, `renderer.onBufferChange` (`types.ts`); `session.onReset`, `session.onExit` (existing in `tabs.ts`).
- Produces:
  ```ts
  export class InputStateController {
    get state(): InputState
    get trusted(): boolean
    dispatch(e: InputEvent): void         // returns nothing; updates internal Machine
    onChange(cb: (m: Machine) => void): void
  }
  ```

**Acceptance Criteria:**
- Feeding a marker/buffer/reset/exit sequence drives `controller.state` exactly as `reduce` would.
- `onChange` fires only when the state or trusted flag actually changes.
- Each `Tab` owns one controller; a `133 D` marker still updates `_lastExitCode` (unchanged behaviour) AND the controller.
- No keyboard-input behaviour changes in this milestone (observe-only): typing still routes raw to the PTY exactly as before.

- [ ] **Step 1: Write the failing test**

```ts
// frontend/src/input-state.test.ts  (add)
import { InputStateController } from './input-state'

describe('InputStateController', () => {
  it('tracks state and fires onChange only on real changes', () => {
    const c = new InputStateController()
    const seen: string[] = []
    c.onChange((m) => seen.push(m.state))
    c.dispatch({ type: 'marker', kind: 'A' }) // -> PROMPT_READY
    c.dispatch({ type: 'marker', kind: 'B' }) // stays PROMPT_READY (no change)
    c.dispatch({ type: 'marker', kind: 'C' }) // -> RUNNING_RAW
    expect(c.state).toBe('RUNNING_RAW')
    expect(seen).toEqual(['PROMPT_READY', 'RUNNING_RAW'])
  })
})
```

- [ ] **Step 2: Run to verify failure**

Run: `cd frontend && npm run test -- input-state.test.ts`
Expected: FAIL — `InputStateController` not exported.

- [ ] **Step 3: Implement the controller**

```ts
// frontend/src/input-state.ts  (append)
export class InputStateController {
  private machine = initialMachine()
  private subs: Array<(m: Machine) => void> = []

  get state(): InputState { return this.machine.state }
  get trusted(): boolean { return this.machine.trusted }

  dispatch(e: InputEvent): void {
    const next = reduce(this.machine, e)
    if (next.state === this.machine.state && next.trusted === this.machine.trusted) {
      this.machine = next
      return
    }
    this.machine = next
    for (const cb of this.subs) cb(next)
  }

  onChange(cb: (m: Machine) => void): void { this.subs.push(cb) }
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd frontend && npm run test -- input-state.test.ts`
Expected: PASS.

- [ ] **Step 5: Wire the controller into `tabs.ts` (observe-only)**

In the tab's renderer-wiring block (near `renderer.onCommandMarker`, `tabs.ts:270`), add a controller field on the tab and feed events. Import `InputStateController` and `log` (existing logger). Replace the existing `onCommandMarker` handler body to ALSO dispatch:

```ts
// tabs.ts — field on the Tab class
private inputState = new InputStateController()

// in the wiring block:
this.inputState.onChange((m) =>
  console.debug('nocx: input-state', m.state, 'trusted=', m.trusted))

renderer.onCommandMarker((marker) => {
  this.inputState.dispatch({ type: 'marker', kind: marker.kind })
  if (marker.kind === 'D' && marker.exitCode !== undefined) {
    this._lastExitCode = marker.exitCode
  }
})
renderer.onBufferChange((type) => {
  this._bufferType = type
  this.inputState.dispatch({ type: 'buffer', buffer: type })
})
session.onReset(() => {
  renderer.reset()
  this.inputState.dispatch({ type: 'reset' })
})
session.onExit((sid: string) => {
  console.log('nocx: session exited:', sid)
  this.inputState.dispatch({ type: 'exit' })
})
```

(Adapt to the exact existing `session.onReset` / `session.onExit` call sites in `tabs.ts` — fold the `dispatch` into them rather than duplicating handlers.)

- [ ] **Step 6: Verify build, tests, and the app**

Run: `cd frontend && npm run test && npm run typecheck && npm run build`
Expected: all green.

Manual (in the app): open a shell, run `ls`, then `false`, then `vim` and `:q`. In the devtools console observe transitions: `PROMPT_READY` at the prompt, `RUNNING_RAW` while `ls`/`false` run, back to `PROMPT_READY`, and `ALT_SCREEN` while `vim` is open. In a shell WITHOUT integration (`NOCX_SHELL_INTEGRATION` unset), no transitions occur and typing works normally (fail-open).

- [ ] **Step 7: Commit**

```bash
git add frontend/src/input-state.ts frontend/src/input-state.test.ts frontend/src/tabs.ts
git commit -m "feat(input): per-tab input-state controller, observe-only wiring (nocx-4ff.1)"
```

---

## What this milestone deliberately does NOT do (tracked, not cut)

- **No keyboard ownership / editor** — `PROMPT_READY` is observed but does not yet capture input (Milestone 4, `nocx-4ff.3`).
- **No enhanced-mode empty prompt** — shell still shows the visible prompt (Milestone 2, `nocx-5mn.4` remainder).
- **No block decorations / gutter** — deferred to Milestone 6 after the gutter-geometry spike (`nocx-4ff.5`).
- **Marker line/col snapshot is produced but not yet consumed** — blocks will use it for command-row anchoring.

## Self-review pointers (for the executor)

- Confirm `tabs.ts` has exactly one `onCommandMarker`, one `onBufferChange`, one `onReset`, one `onExit` after the edit — do not leave duplicated handlers.
- Confirm `dispose()` disposes the OSC 133 registration (Task 1) so a closed tab leaks no parser handler.
