# Warp Command Experience — M3b: DOM editor + atomic handoff (frontend) — Implementation Plan

> **For agentic workers:** implement task-by-task, TDD. Steps use `- [ ]`. Binding: **ADR-0004** (§2 atomic handoff, §3 pluggable editor) and **ADR-0006** (`A→B` ownership; fail-open). Read both first.

**Goal:** Behind an OFF-by-default feature flag, nocx's DOM `<textarea>` editor owns keyboard input **only** after a valid `A→B` prompt sequence, and on Enter submits the command to the PTY via the M2a `ShellInputTarget` atomic handoff (hide-before-send → one bracketed paste → CR). Everything else routes raw to the PTY. Fully fail-open.

**Architecture:** The editor is a passive DOM surface (ADR-0004 §3). Keyboard routing is by **focus**: when owned, the textarea is shown+focused and captures keys; otherwise the xterm has focus and keys flow `onData → session.send` as today. Ownership is gated on `A→B` (ADR-0006 §4). Submit routes through `ShellInputTarget` (built in M2a: `input-target.ts`).

**Tech Stack:** TypeScript (strict), vitest, xterm.js.

## Global Constraints

- **Disjoint files only.** This worker touches ONLY: `frontend/src/input-state.ts` (+ its test), `frontend/src/editor.ts` (+ test, new), `frontend/src/tabs.ts`, `frontend/src/style.css`. Another worker is editing `internal/**` (Go/shell) in parallel — do NOT touch it.
- **Do NOT run `npm install` / `npm run build` / the full suite.** Deps are installed; coordinator verifies at the end. You MAY run single test files (`npm run test -- input-state.test.ts`, `npm run test -- editor.test.ts`).
- **Commit only your own files** with explicit paths; on `.git/index.lock` wait 2s and retry.
- **Feature flag OFF by default:** the editor must NEVER show unless `ENHANCED_INPUT` is explicitly enabled. With it off, typing behaves exactly as today (fail-open).
- **Reuse M2a:** import `ShellInputTarget`, `createRegistry` from `./input-target` — do not reinvent submission.

---

### Task 1: `A → B` ownership gate in the state machine

**Files:**
- Modify: `frontend/src/input-state.ts`
- Test: `frontend/src/input-state.test.ts`

**Interfaces:**
- Produces: `Machine` gains `owned: boolean`. `owned` is true ONLY after `A` then `B` (in that order) — `A` alone leaves `owned:false`. `C`/`D`/`submit`/`buffer`/`reset`/`exit` clear `owned`. `owned` is the sole authorization for DOM keyboard capture (ADR-0006 §4).
- `initialMachine()` → `{state:'RAW', trusted:false, owned:false}`.

**Acceptance Criteria:**
- `A` → `owned:false` (state `PROMPT_READY`). `A` then `B` → `owned:true`.
- `B` without a preceding `A` (from non-`PROMPT_READY`) → `owned:false`.
- `C`/`submit`/`buffer:'alternate'`/`reset`/`exit` all set `owned:false`.

- [ ] **Step 1: Write the failing tests**

```ts
// input-state.test.ts (add)
describe('A→B ownership gate', () => {
  it('A alone does not grant ownership; A then B does', () => {
    const a = reduce(initialMachine(), { type: 'marker', kind: 'A' })
    expect(a.owned).toBe(false)
    const b = reduce(a, { type: 'marker', kind: 'B' })
    expect(b.owned).toBe(true)
    expect(b.state).toBe('PROMPT_READY')
  })
  it('B without a prompt does not grant ownership', () => {
    expect(reduce(initialMachine(), { type: 'marker', kind: 'B' }).owned).toBe(false)
  })
  it('C, submit, alt-buffer, reset clear ownership', () => {
    const owned = [{ type:'marker', kind:'A' }, { type:'marker', kind:'B' }]
      .reduce(reduce, initialMachine())
    expect(owned.owned).toBe(true)
    expect(reduce(owned, { type: 'marker', kind: 'C' }).owned).toBe(false)
    expect(reduce(owned, { type: 'submit' }).owned).toBe(false)
    expect(reduce(owned, { type: 'buffer', buffer: 'alternate' }).owned).toBe(false)
    expect(reduce(owned, { type: 'reset' }).owned).toBe(false)
  })
})
```

- [ ] **Step 2: Run to verify failure** — `npm run test -- input-state.test.ts` FAIL (`owned` undefined).

- [ ] **Step 3: Implement.** Add `owned: boolean` to `Machine`; set it in `reduce`:
  - `initialMachine`: `owned:false`.
  - `buffer`/`reset`/`exit`/`submit`: `owned:false`.
  - marker `A`: `{state:'PROMPT_READY', trusted: m.state !== 'RUNNING_RAW', owned:false}`.
  - marker `B`: owned only when arriving from a real prompt-start: `owned: m.state === 'PROMPT_READY'` (i.e. an `A` preceded it); keep `trusted` logic; state `PROMPT_READY`.
  - marker `C`: `owned:false` (command started).
  - marker `D`: `owned:false` (add to the returned object; the orphan-`D` branch returns `m` unchanged, which already has `owned:false` in practice — keep it `m`).
  Update every returned object in `reduce` to include `owned` (TypeScript strict will flag any missing).

- [ ] **Step 4: Run to verify pass** — `npm run test -- input-state.test.ts` PASS (all prior cases too — update earlier `toEqual({state,trusted})` assertions to include `owned` where they now fail).

- [ ] **Step 5: Commit**

```bash
git add frontend/src/input-state.ts frontend/src/input-state.test.ts
git commit -m "feat(input): A→B ownership gate for DOM keyboard capture (nocx-4ff.1, ADR-0006)"
```

---

### Task 2: `CommandEditor` DOM surface + atomic-handoff submit

**Files:**
- Create: `frontend/src/editor.ts`
- Test: `frontend/src/editor.test.ts`
- Modify: `frontend/src/style.css` (editor chrome)

**Interfaces:**
- Produces:
  ```ts
  export interface EditorActions { submit: (doc: string) => void }
  export class CommandEditor {
    constructor(actions: EditorActions)
    mount(container: HTMLElement): void
    show(): void          // display + focus the textarea
    hide(): void          // clear focus + display:none
    get isVisible(): boolean
    dispose(): void
  }
  ```
  Behaviour: Enter (no Shift) → capture the value, **clear + hide the editor FIRST** (atomic handoff, ADR-0004 §2), then call `actions.submit(doc)`. Shift+Enter inserts a newline (native textarea). The editor holds only text — no PTY/session/clipboard knowledge.

**Acceptance Criteria:**
- Pressing Enter with `echo hi` calls `actions.submit('echo hi')` exactly once, and the editor is hidden and cleared **before** submit is called (assert order).
- Shift+Enter does NOT submit (inserts newline).
- `hide()`/`show()` toggle `isVisible`; a fresh editor is not visible.

- [ ] **Step 1: Write the failing test**

```ts
// editor.test.ts
import { describe, it, expect, vi } from 'vitest'
import { CommandEditor } from './editor'

const setup = () => {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const order: string[] = []
  const submit = vi.fn((doc: string) => order.push(`submit:${doc}`))
  const ed = new CommandEditor({ submit })
  ed.mount(container)
  const ta = container.querySelector('textarea')!
  return { ed, ta, submit, order }
}
const enter = (ta: HTMLTextAreaElement, shift = false) =>
  ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', shiftKey: shift, bubbles: true, cancelable: true }))

describe('CommandEditor', () => {
  it('Enter hides+clears before submit (atomic handoff)', () => {
    const { ed, ta, submit, order } = setup()
    ed.show(); ta.value = 'echo hi'
    // record hide via a spy on visibility at submit time
    submit.mockImplementation((doc: string) => order.push(`visible@submit:${ed.isVisible}|${doc}`))
    enter(ta)
    expect(submit).toHaveBeenCalledWith('echo hi')
    expect(order[0]).toBe('visible@submit:false|echo hi') // hidden BEFORE submit
    expect(ta.value).toBe('')
  })
  it('Shift+Enter does not submit', () => {
    const { ed, ta, submit } = setup()
    ed.show(); ta.value = 'x'
    enter(ta, true)
    expect(submit).not.toHaveBeenCalled()
  })
  it('starts hidden; show/hide toggle isVisible', () => {
    const { ed } = setup()
    expect(ed.isVisible).toBe(false)
    ed.show(); expect(ed.isVisible).toBe(true)
    ed.hide(); expect(ed.isVisible).toBe(false)
  })
})
```

- [ ] **Step 2: Run to verify failure** — `npm run test -- editor.test.ts` FAIL (module missing).

- [ ] **Step 3: Implement `editor.ts`**

```ts
// frontend/src/editor.ts
// Passive DOM command editor (ADR-0004 §3). Holds text + selection only; a
// registered action decides where a submit goes. Keyboard routing to/from the
// PTY is by FOCUS: while shown the textarea captures keys; while hidden the
// xterm has focus and keys flow to the PTY as usual.
export interface EditorActions {
  submit: (doc: string) => void
}

export class CommandEditor {
  private root: HTMLElement
  private ta: HTMLTextAreaElement

  constructor(private readonly actions: EditorActions) {
    this.root = document.createElement('div')
    this.root.className = 'nocx-editor'
    this.root.style.display = 'none'
    this.ta = document.createElement('textarea')
    this.ta.className = 'nocx-editor-input'
    this.ta.rows = 1
    this.ta.spellcheck = false
    this.ta.autocapitalize = 'off'
    this.ta.addEventListener('keydown', this.onKeydown)
    this.root.appendChild(this.ta)
  }

  mount(container: HTMLElement): void { container.appendChild(this.root) }

  private onKeydown = (e: KeyboardEvent): void => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      const doc = this.ta.value
      // Atomic handoff (ADR-0004 §2): hide + clear BEFORE sending, so the
      // committed command is painted once by the shell, not echoed twice.
      this.ta.value = ''
      this.hide()
      this.actions.submit(doc)
    }
  }

  show(): void { this.root.style.display = ''; this.ta.focus() }
  hide(): void { this.ta.blur(); this.root.style.display = 'none' }
  get isVisible(): boolean { return this.root.style.display !== 'none' }
  dispose(): void { this.ta.removeEventListener('keydown', this.onKeydown); this.root.remove() }
}
```

Add minimal styles to `style.css` (`.nocx-editor` / `.nocx-editor-input` — a single-line bar; precise prompt-line positioning is follow-on):

```css
.nocx-editor { position: absolute; left: 0; right: 0; bottom: 0; padding: 4px 8px; background: #1a1b26; }
.nocx-editor-input { width: 100%; background: transparent; color: #c0caf5; border: none; outline: none;
  font-family: inherit; font-size: inherit; resize: none; }
```

- [ ] **Step 4: Run to verify pass** — `npm run test -- editor.test.ts` PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/editor.ts frontend/src/editor.test.ts frontend/src/style.css
git commit -m "feat(input): CommandEditor DOM surface + atomic-handoff submit (nocx-4ff.3, ADR-0004)"
```

---

### Task 3: Wire editor into tabs behind an OFF-by-default flag

**Files:**
- Modify: `frontend/src/tabs.ts`
- Test: manual/app (no unit test — DOM+session wiring; verified by coordinator at the end)

**Interfaces:**
- Consumes: `InputStateController` (M1), `CommandEditor` (Task 2), `ShellInputTarget` (M2a), `session.send`, `renderer.focus()`.

**Acceptance Criteria:**
- With `ENHANCED_INPUT` false (default), no editor ever appears; typing is unchanged (fail-open).
- With it true: when `inputState.owned` becomes true, the editor shows+focuses; on any non-owned state it hides and the renderer regains focus. Enter submits via `ShellInputTarget` (bracketed paste + CR). No double echo (editor hides before send).

- [ ] **Step 1: Implement the wiring**

```ts
// tabs.ts — module const near the top (read from import.meta.env later)
const ENHANCED_INPUT = false // ADR-0006: OFF by default; flip only after the
                             // native-mode escape + readiness gating land.

// in the tab wiring block, after the renderer is mounted and session exists:
const shellTarget = new ShellInputTarget((data: string) => session.send(data))
const editor = new CommandEditor({
  submit: (doc: string) => { void shellTarget.submit(doc, { targetId: 'shell' }) },
})
editor.mount(this.pane)

this.inputState.onChange((m) => {
  console.debug('nocx: input-state', m.state, 'trusted=', m.trusted, 'owned=', m.owned)
  if (!ENHANCED_INPUT) return
  if (m.owned) editor.show()
  else { editor.hide(); renderer.focus() }
})
```

Import `CommandEditor` from `./editor` and `ShellInputTarget` from `./input-target`. Ensure `editor.dispose()` is called wherever the tab/renderer is disposed.

- [ ] **Step 2: Verify (coordinator, at end)** — `npm run typecheck` clean; with `ENHANCED_INPUT=false` the app behaves exactly as today. Manually flipping it to `true` in a dev build: at a prompt the editor bar appears+focuses, typing + Enter runs the command once (no double echo), running/alt states hide the editor. (Full in-WebView visual pass is a coordinator step.)

- [ ] **Step 3: Commit**

```bash
git add frontend/src/tabs.ts
git commit -m "feat(input): wire CommandEditor behind OFF-by-default ENHANCED_INPUT flag (nocx-4ff.3/.4)"
```

---

## REQUIRED before the `ENHANCED_INPUT` flag may be enabled (tracked, NOT cut — ADR-0006 §5)

These are safety-critical and must land before the flag defaults on. File as follow-up beads; do NOT enable the flag without them:
1. **State-independent native-mode escape** that RESTORES a visible shell prompt (a one-shot control op telling the shell to leave marker-only mode) — not merely routing keys raw to an empty prompt.
2. **Frontend-readiness gating** — request an enhanced (`NOCX_PROMPT_MODE=marker-only`) PTY only after editor + marker handler + input router + transport are initialized; else spawn baseline.
3. **Precise prompt-line positioning** of the editor (MVP uses a bottom bar) and multi-line growth.
4. **`reset()`/dispose** hardening: clear editor + ownership on `renderer.reset()` (reattach) so a resync never leaves the editor shown over a stale buffer.
