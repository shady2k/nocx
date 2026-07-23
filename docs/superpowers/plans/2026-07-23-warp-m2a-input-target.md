# Warp Command Experience — M2a: InputTarget registry + ShellInputTarget — Implementation Plan

> **For agentic workers:** implement this plan task-by-task, TDD. Steps use `- [ ]`.

**Goal:** A pluggable `InputTarget` registry and a first `ShellInputTarget` that submits a command to the PTY via the ADR-0004 atomic-handoff transport (one bracketed paste + CR). Standalone module — NOT wired into `tabs.ts` yet (that is the editor milestone).

**Architecture:** ADR-0004 §3 — the editor is a passive surface; behaviour comes from a registered `InputTarget`. This milestone delivers the registry + shell target as an isolated, unit-tested module. `ShellInputTarget` is decoupled from `session`/renderer via an injected `send(data)` sink.

**Tech Stack:** TypeScript (strict), vitest.

## Global Constraints

- **Disjoint files only.** This worker creates ONLY `frontend/src/input-target.ts` and `frontend/src/input-target.test.ts`. Do not modify any other file (another worker is editing the tree in parallel).
- **Do NOT run `npm install`, `npm run build`, or the full suite** — dependencies are already installed and the coordinator runs all verification at the end. You MAY run your own single test file: `cd frontend && npm run test -- input-target.test.ts`.
- **Commit only your own files** with explicit paths. If `git commit` reports `.git/index.lock` contention, wait 2s and retry.
- **AD-6:** the target holds submission policy only; it does not touch the clipboard or parse OSC.
- TypeScript strict must pass for your files.

---

### Task 1: InputTarget types + registry

**Files:**
- Create: `frontend/src/input-target.ts`
- Test: `frontend/src/input-target.test.ts`

**Interfaces:**
- Produces:
  ```ts
  export interface SubmitContext { readonly targetId: string }
  export interface InputTarget {
    readonly id: string
    readonly label: string
    submit(doc: string, ctx: SubmitContext): Promise<void>
  }
  export interface InputTargetRegistry {
    register(target: InputTarget): void
    setActive(id: string): void
    active(): InputTarget
  }
  export function createRegistry(): InputTargetRegistry
  ```
  Semantics: first `register` becomes active by default; `setActive('unknown')` throws; `active()` before any register throws.

**Acceptance Criteria:**
- `register` then `active()` returns that target; the first registered target is active by default.
- `setActive` switches the active target; `setActive` with an unknown id throws.
- `active()` with no registered targets throws a clear error.

- [ ] **Step 1: Write the failing test**

```ts
// frontend/src/input-target.test.ts
import { describe, it, expect, vi } from 'vitest'
import { createRegistry, type InputTarget } from './input-target'

const fake = (id: string): InputTarget => ({
  id, label: id, submit: vi.fn(async () => {}),
})

describe('InputTargetRegistry', () => {
  it('first registered target is active by default', () => {
    const r = createRegistry()
    r.register(fake('shell'))
    expect(r.active().id).toBe('shell')
  })
  it('setActive switches; unknown id throws', () => {
    const r = createRegistry()
    r.register(fake('shell'))
    r.register(fake('agent'))
    r.setActive('agent')
    expect(r.active().id).toBe('agent')
    expect(() => r.setActive('nope')).toThrow()
  })
  it('active() with no targets throws', () => {
    expect(() => createRegistry().active()).toThrow()
  })
})
```

- [ ] **Step 2: Run to verify failure**

Run: `cd frontend && npm run test -- input-target.test.ts`
Expected: FAIL — `Cannot find module './input-target'`.

- [ ] **Step 3: Implement types + registry**

```ts
// frontend/src/input-target.ts
// Pluggable input targets (ADR-0004 §3). The editor is a passive surface; a
// registered InputTarget decides where a submitted document goes. New kinds
// (shell now, LLM agent later) are added by registering a target, never by
// editing the editor.
export interface SubmitContext {
  readonly targetId: string
}

export interface InputTarget {
  readonly id: string
  readonly label: string
  submit(doc: string, ctx: SubmitContext): Promise<void>
}

export interface InputTargetRegistry {
  register(target: InputTarget): void
  setActive(id: string): void
  active(): InputTarget
}

export function createRegistry(): InputTargetRegistry {
  const targets = new Map<string, InputTarget>()
  let activeId: string | undefined
  return {
    register(target) {
      targets.set(target.id, target)
      if (activeId === undefined) activeId = target.id
    },
    setActive(id) {
      if (!targets.has(id)) throw new Error(`input-target: unknown id ${id}`)
      activeId = id
    },
    active() {
      if (activeId === undefined) throw new Error('input-target: no target registered')
      return targets.get(activeId)!
    },
  }
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd frontend && npm run test -- input-target.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/input-target.ts frontend/src/input-target.test.ts
git commit -m "feat(input): InputTarget registry (nocx-4ff.2)"
```

---

### Task 2: ShellInputTarget — atomic-handoff submit

**Files:**
- Modify: `frontend/src/input-target.ts` (add `ShellInputTarget`)
- Test: `frontend/src/input-target.test.ts` (add)

**Interfaces:**
- Consumes: `InputTarget`, `SubmitContext` (Task 1).
- Produces:
  ```ts
  export class ShellInputTarget implements InputTarget {
    readonly id = 'shell'
    readonly label = 'Shell'
    constructor(send: (data: string) => void)
    submit(doc: string): Promise<void>
  }
  ```
  `submit` transmits the document as ONE bracketed paste followed by CR — the ADR-0004 §2 atomic-handoff transport — via the injected `send`. No `stty`, no per-key echo.

**Acceptance Criteria:**
- `submit('echo hi')` calls `send` exactly once with `"\x1b[200~echo hi\x1b[201~\r"`.
- A multi-line document is transmitted verbatim inside the one bracketed paste (newlines preserved), still followed by a single CR.

- [ ] **Step 1: Write the failing test**

```ts
// frontend/src/input-target.test.ts  (add)
import { ShellInputTarget } from './input-target'

describe('ShellInputTarget', () => {
  it('submits one bracketed paste + CR', async () => {
    const send = vi.fn()
    const t = new ShellInputTarget(send)
    await t.submit('echo hi', { targetId: 'shell' })
    expect(send).toHaveBeenCalledTimes(1)
    expect(send).toHaveBeenCalledWith('\x1b[200~echo hi\x1b[201~\r')
  })
  it('preserves a multi-line document inside the paste', async () => {
    const send = vi.fn()
    await new ShellInputTarget(send).submit('a\nb', { targetId: 'shell' })
    expect(send).toHaveBeenCalledWith('\x1b[200~a\nb\x1b[201~\r')
  })
})
```

- [ ] **Step 2: Run to verify failure**

Run: `cd frontend && npm run test -- input-target.test.ts`
Expected: FAIL — `ShellInputTarget` not exported.

- [ ] **Step 3: Implement `ShellInputTarget`**

```ts
// frontend/src/input-target.ts  (append)
const PASTE_START = '\x1b[200~'
const PASTE_END = '\x1b[201~'

// ShellInputTarget routes a submitted document to the active PTY using the
// ADR-0004 §2 atomic handoff: the editor hides itself (caller's job), then the
// whole command is sent as ONE bracketed paste followed by CR. zle/readline
// paints the accepted command once as the committed transcript — no per-key
// echo, no stty, no readline mirroring.
export class ShellInputTarget implements InputTarget {
  readonly id = 'shell'
  readonly label = 'Shell'
  constructor(private readonly send: (data: string) => void) {}

  async submit(doc: string): Promise<void> {
    this.send(`${PASTE_START}${doc}${PASTE_END}\r`)
  }
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd frontend && npm run test -- input-target.test.ts`
Expected: PASS (both suites).

- [ ] **Step 5: Commit**

```bash
git add frontend/src/input-target.ts frontend/src/input-target.test.ts
git commit -m "feat(input): ShellInputTarget atomic-handoff submit (nocx-4ff.2)"
```

---

## Not in this milestone (tracked, not cut)
- Wiring the registry/target into `tabs.ts` and the editor — Milestone (`nocx-4ff.3`).
- `complete?()` / `history?()` optional members — deferred (YAGNI) until the editor needs them (`nocx-4ff.6`).
- Gating submit on `PROMPT_READY` — enforced at the call site in the editor milestone, using the M1 state machine.
