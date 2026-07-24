# Warp editable command-input block — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Steps within tasks use checkbox (`- [ ]`) syntax for human readability.
>
> **Bead mapping — DO NOT create new beads.** The epic and all six tasks already exist under epic **`nocx-4ff`**. Claim the existing bead per task; do not import duplicates:
> `T1→nocx-4ff.11 · T2→nocx-4ff.12 · T3→nocx-4ff.10 · T4→nocx-4ff.9 · T5→nocx-4ff.4 · T6→nocx-4ff.13`.
> Order is enforced by this plan, not by bead deps: implement T1→T6 in sequence.

**Goal:** Ship a freely-editable DOM command-input block that appears at a clean local zsh/bash prompt, hands off atomically to the PTY on Enter, and fails open to a normal terminal everywhere else — by fixing two state-machine bugs, threading a per-session `enhanced` flag with frontend-readiness gating, adding a native-mode escape, verifying raw routing after submit, then enabling `ENHANCED_INPUT` with hardening.

**Architecture:** ADR-0004 (input-ownership state machine + passive editor behind a pluggable `InputTarget`) and ADR-0006 (marker-only prompt). The editor, state machine and shell OSC-133/marker-only scripts are already built; this plan is the safe-enable glue. Single continuous xterm owns output (AD-6); the backend stays byte-blind.

**Tech Stack:** TypeScript + xterm.js + vitest (frontend); Go 1.26 + creack/pty (backend, exec tests); zsh/bash embedded scripts.

## Global Constraints

- **Fail-open is an invariant (ADR-0004 §1, ADR-0006 §5):** missing/malformed markers → `RAW` + visible prompt. Never trap the user in an invisible prompt.
- **Single-owner (AD-6):** the backend never parses OSC; the editor + state machine sit above the renderer boundary. The renderer only reports facts.
- **Only the top-level session owns input** (nested/SSH shells fail open to raw).
- **Bump `const version` in `internal/shellintegration/scripts.go`** whenever a `scripts/nocx.*` file changes (currently `"3"`) — `EnsureInstalled` rewrites on version change.
- Frontend tests run from `frontend/`: `npx vitest run src/<file>`. Go tests: `go test ./internal/...`.
- Keep the atomic-handoff order: editor hides **before** send (ADR-0004 §2).

---

### Task 1: Fix input-state `B,B` ownership latch (`nocx-4ff.11`)

**Files:**
- Modify: `frontend/src/input-state.ts:77-84` (the `case 'B'` branch of `reduce`)
- Test: `frontend/src/input-state.test.ts`

**Interfaces:**
- Consumes: nothing new.
- Produces: unchanged `reduce(m: Machine, e: InputEvent): Machine` signature. Tightened semantics: `owned` becomes `true` on `B` **only** when the machine is already at a *trusted* `PROMPT_READY` (i.e. a clean `A` preceded this `B`). This also means an untrusted resync-`A`→`B` grants **no** ownership (fail-open to raw), which is the safe behavior.

**Acceptance Criteria:**
- `B,B` from `RAW` never sets `owned:true` (no `A` ever seen).
- A clean `A`→`B` still sets `owned:true`; a single `B` from `RAW` stays `owned:false`.
- `owned` is `false` in every non-`PROMPT_READY` state.

- [ ] **Step 1: Write the failing test** — add to the `A→B ownership gate` describe block in `frontend/src/input-state.test.ts`:

```ts
it('B,B from RAW never latches ownership without an A (nocx-4ff.11)', () => {
  const b1 = reduce(initialMachine(), { type: 'marker', kind: 'B' })
  expect(b1.owned).toBe(false)
  const b2 = reduce(b1, { type: 'marker', kind: 'B' })
  expect(b2.owned).toBe(false) // currently FAILS: reduce grants owned on the 2nd B
})

it('an untrusted resync A→B does not grant ownership (fail-open)', () => {
  const running = [
    { type: 'marker', kind: 'A' } as InputEvent,
    { type: 'marker', kind: 'C' } as InputEvent,
  ].reduce(reduce, initialMachine())
  const a = reduce(running, { type: 'marker', kind: 'A' }) // untrusted PROMPT_READY
  expect(a.trusted).toBe(false)
  expect(reduce(a, { type: 'marker', kind: 'B' }).owned).toBe(false)
})
```

- [ ] **Step 2: Run to verify failure**

Run: `cd frontend && npx vitest run src/input-state.test.ts`
Expected: FAIL — `b2.owned` is `true`.

- [ ] **Step 3: Implement the fix** — in `frontend/src/input-state.ts`, change the `case 'B'` branch so ownership requires a trusted prompt:

```ts
case 'B':
  // B grants DOM ownership ONLY when a clean A already put us at a trusted
  // prompt (ADR-0006 §4). Gating on `trusted` closes the B,B latch: a B that
  // merely re-enters PROMPT_READY without a preceding A stays owned:false.
  return {
    state: 'PROMPT_READY',
    trusted: m.state === 'PROMPT_READY' && m.trusted,
    owned: m.state === 'PROMPT_READY' && m.trusted,
  }
```

- [ ] **Step 4: Run to verify pass**

Run: `cd frontend && npx vitest run src/input-state.test.ts`
Expected: PASS (all existing cases still green — clean `A`→`B` still yields `owned:true`).

- [ ] **Step 5: Commit**

```bash
git add frontend/src/input-state.ts frontend/src/input-state.test.ts
git commit -m "fix(input): B,B no longer latches ownership without an A (nocx-4ff.11)"
```

---

### Task 2: Dispatch `{submit}` + refocus grid on Enter (`nocx-4ff.12`)

**Problem:** `editor.ts` Enter calls `actions.submit(doc)` but nothing dispatches `{type:'submit'}` to the state machine, so `owned` stays stuck and PS2-continuation keys go nowhere. Extract the submit orchestration into a tiny tested helper and wire it in `tabs.ts`.

**Files:**
- Create: `frontend/src/submit.ts`
- Create: `frontend/src/submit.test.ts`
- Modify: `frontend/src/tabs.ts:332-336` (the `CommandEditor` `submit` callback)

**Interfaces:**
- Produces: `submitCommand(doc: string, deps: SubmitDeps): void` where
  `interface SubmitDeps { dispatchSubmit(): void; focusGrid(): void; sendDoc(doc: string): void }`.
  Ordering contract: `dispatchSubmit()` → `focusGrid()` → `sendDoc()`.
- Consumes in `tabs.ts`: `this.inputState.dispatch`, `renderer.focus`, `this.shellTarget!.submit`.

**Acceptance Criteria:**
- After submit: the machine received `{type:'submit'}` (leaves `owned:false`), the grid was refocused, and the doc was sent — in that order.
- The atomic-handoff order is preserved (editor already hid itself in `editor.ts` before the callback runs).

- [ ] **Step 1: Write the failing test** — `frontend/src/submit.test.ts`:

```ts
import { describe, it, expect } from 'vitest'
import { submitCommand } from './submit'

describe('submitCommand', () => {
  it('dispatches submit, refocuses the grid, then sends — in order (nocx-4ff.12)', () => {
    const calls: string[] = []
    submitCommand('echo hi', {
      dispatchSubmit: () => calls.push('dispatch'),
      focusGrid: () => calls.push('focus'),
      sendDoc: (d) => calls.push(`send:${d}`),
    })
    expect(calls).toEqual(['dispatch', 'focus', 'send:echo hi'])
  })
})
```

- [ ] **Step 2: Run to verify failure**

Run: `cd frontend && npx vitest run src/submit.test.ts`
Expected: FAIL — cannot resolve `./submit`.

- [ ] **Step 3: Implement** — `frontend/src/submit.ts`:

```ts
// Submit orchestration for the command editor (ADR-0004 §2, nocx-4ff.12).
// The ordering is the fix: tell the state machine we left the prompt so
// ownership clears, refocus the grid so PS2 continuation + running-program
// keys reach the PTY, THEN send the bracketed-paste handoff.
export interface SubmitDeps {
  dispatchSubmit(): void
  focusGrid(): void
  sendDoc(doc: string): void
}

export function submitCommand(doc: string, deps: SubmitDeps): void {
  deps.dispatchSubmit()
  deps.focusGrid()
  deps.sendDoc(doc)
}
```

Then wire it in `frontend/src/tabs.ts` — replace the `submit` callback at lines 332-336:

```ts
      this.editor = new CommandEditor({
        submit: (doc: string) => {
          submitCommand(doc, {
            dispatchSubmit: () => this.inputState.dispatch({ type: 'submit' }),
            focusGrid: () => renderer.focus(),
            sendDoc: (d) => void this.shellTarget!.submit(d),
          })
        },
      })
```

Add the import at the top of `tabs.ts` (next to the other `./` imports):

```ts
import { submitCommand } from './submit'
```

- [ ] **Step 4: Run to verify pass**

Run: `cd frontend && npx vitest run src/submit.test.ts && npx tsc --noEmit`
Expected: PASS; typecheck clean.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/submit.ts frontend/src/submit.test.ts frontend/src/tabs.ts
git commit -m "fix(input): dispatch {submit} + refocus grid before send (nocx-4ff.12)"
```

---

### Task 3: Thread per-session `enhanced` + readiness ordering (`nocx-4ff.10`)

**Problem:** `enhanced` is a process-global (`localPTYFactory.enhancedInput`, hardcoded `false`). Make it per-session, carried on `pty.Config`, requested by the frontend — and set up the tab's input-owning machinery **before** `openSession` so the shell never paints an invisible marker-only prompt before the editor exists.

**Files:**
- Modify: `internal/pty/pty.go:16` (add `Enhanced bool` to `Config`)
- Modify: `internal/session/session.go:27` (add `Enhanced bool` to `Config`) and `:110` (pass it into `pty.Config`)
- Modify: `internal/transport/ws.go:276` (add `Enhanced bool` to `openParams`) and `:425` (pass into `session.Config`)
- Modify: `internal/app/app.go:56-59` (`NewPTY` uses `cfg.Enhanced`; drop the global `enhancedInput` field)
- Modify: `frontend/src/ipc.ts:504` + `:516` (`openSession(cols, rows, enhanced)`; add to params)
- Modify: `frontend/src/tabs.ts` (reorder `start()`: create editor + wire `inputState`/`onCommandMarker` listeners **before** `openSession`; call `openSession(cols, rows, ENHANCED_INPUT)`)
- Test: `internal/session/session_test.go`, `internal/transport/ws_test.go`, `frontend/src/ipc.test.ts` (create if absent)

**Interfaces:**
- Produces (Go): `pty.Config.Enhanced bool`; `session.Config.Enhanced bool`; `app` factory calls `f.shint.ActivationEnv(cfg.Enhanced)`.
- Produces (TS): `WSClient.openSession(cols: number, rows: number, enhanced: boolean): Promise<SessionHandle>` sending `params: { cols, rows, xpixel: 0, ypixel: 0, enhanced }`.
- Consumes: `ShellIntegration.ActivationEnv(enhanced bool) []string` (already exists, `shellintegration.go:187`).

**Acceptance Criteria:**
- `Reg.Open` with `Config.Enhanced=true` constructs a `pty.Config` whose `Enhanced` is `true`.
- The app factory calls `ActivationEnv(cfg.Enhanced)` (per-session), not the removed global.
- `openParams` unmarshals `enhanced`.
- Frontend requests `enhanced` from `openSession` and the editor + marker listeners are wired before the session opens (no invisible-prompt gap).

- [ ] **Step 1: Write the failing Go test** — in `internal/session/session_test.go`, add a factory that captures the `pty.Config` and assert `Enhanced` threads:

```go
func TestOpenThreadsEnhancedIntoPTYConfig(t *testing.T) {
	var got pty.Config
	ptf := ptyFactoryFunc(func(_ context.Context, cfg pty.Config) (pty.Pty, error) {
		got = cfg
		return newFakePTY(), nil // reuse this file's existing fake PTY helper
	})
	reg := New(testLogger(t), ptf)
	if _, err := reg.Open(context.Background(), Config{
		Kind: KindLocal, Cols: 80, Rows: 24, Enhanced: true,
	}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !got.Enhanced {
		t.Fatalf("pty.Config.Enhanced = false, want true")
	}
}
```

(If no `ptyFactoryFunc`/`newFakePTY` helper exists in the test file, add a minimal adapter mirroring the file's existing `.NewPTY()` fakes.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/session/ -run TestOpenThreadsEnhanced`
Expected: FAIL — `session.Config` and `pty.Config` have no `Enhanced` field (compile error).

- [ ] **Step 3: Implement the Go plumbing**

`internal/pty/pty.go` — add to `Config`:
```go
	// Enhanced requests the marker-only prompt env (ADR-0006) for this session.
	Enhanced bool
```
`internal/session/session.go` — add to `Config`:
```go
	Enhanced bool
```
and in `Reg.Open` (the `pty.Config{...}` literal at ~L110) add:
```go
		Enhanced: cfg.Enhanced,
```
`internal/transport/ws.go` — add to `openParams`:
```go
	Enhanced bool `json:"enhanced"`
```
and in `handleOpen`'s `session.Config{...}` literal (~L425):
```go
		Enhanced: params.Enhanced,
```
`internal/app/app.go` — make the factory per-session; replace lines 50-59:
```go
type localPTYFactory struct {
	log   log.Logger
	shint shellintegration.ShellIntegration
}

func (f *localPTYFactory) NewPTY(_ context.Context, cfg pty.Config) (pty.Pty, error) {
	env := f.shint.ActivationEnv(cfg.Enhanced)
	return pty.NewLocal(f.log, cfg, pty.WithExtraEnv(env))
}
```
and drop the `enhancedInput` field from the `&localPTYFactory{...}` literal at `app.go:33`.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/... && go build ./...`
Expected: PASS; build clean.

- [ ] **Step 5: Write + pass the ws param test** — in `internal/transport/ws_test.go`:

```go
func TestOpenParamsUnmarshalsEnhanced(t *testing.T) {
	var p openParams
	if err := json.Unmarshal([]byte(`{"cols":80,"rows":24,"enhanced":true}`), &p); err != nil {
		t.Fatal(err)
	}
	if !p.Enhanced {
		t.Fatalf("openParams.Enhanced = false, want true")
	}
}
```
Run: `go test ./internal/transport/ -run TestOpenParamsUnmarshalsEnhanced` → PASS.

- [ ] **Step 6: Frontend — thread `enhanced` through `openSession`** — `frontend/src/ipc.ts:504`:

```ts
  openSession(cols: number, rows: number, enhanced: boolean): Promise<SessionHandle> {
```
and the request params (L516):
```ts
        params: { cols, rows, xpixel: 0, ypixel: 0, enhanced },
```
Add `frontend/src/ipc.test.ts` (or extend the existing one) asserting the sent frame carries `enhanced` — mirror the file's existing WS-send test pattern; assert the serialized JSON contains `"enhanced":true` when `openSession(80, 24, true)` is called.

Run: `cd frontend && npx vitest run src/ipc.test.ts` → PASS.

- [ ] **Step 7: Frontend — reorder `start()` for readiness (no invisible-prompt gap)** — in `frontend/src/tabs.ts` `start()`, move the editor creation + `inputState`/`onCommandMarker`/`onChange` wiring to run **before** `await this.client.openSession(...)`, and pass the flag:

```ts
      // Wire input ownership BEFORE opening the session, so a marker-only
      // (invisible) prompt can never paint before the editor exists (nocx-4ff.10).
      this.shellTarget = new ShellInputTarget((data: string) => this.session!.send(data))
      this.editor = new CommandEditor({
        submit: (doc: string) => {
          submitCommand(doc, {
            dispatchSubmit: () => this.inputState.dispatch({ type: 'submit' }),
            focusGrid: () => renderer.focus(),
            sendDoc: (d) => void this.shellTarget!.submit(d),
          })
        },
      })
      this.editor.mount(this.pane)
      // ...attach inputState.onChange + renderer.onCommandMarker/onBufferChange here...

      const session = await this.client.openSession(this.cols, this.rows, ENHANCED_INPUT)
```

Note: `ShellInputTarget`'s `send` must reference the session lazily (`this.session!.send`) since it is now created before `session` is assigned; assign `this.session = session` right after open.

- [ ] **Step 8: Verify + commit**

Run: `cd frontend && npx tsc --noEmit && npx vitest run` then `go test ./internal/... && go build ./...`
Expected: all green.

```bash
git add internal/pty/pty.go internal/session/session.go internal/session/session_test.go \
        internal/transport/ws.go internal/transport/ws_test.go internal/app/app.go \
        frontend/src/ipc.ts frontend/src/ipc.test.ts frontend/src/tabs.ts
git commit -m "feat(input): per-session enhanced flag + readiness-gated marker-only spawn (nocx-4ff.10)"
```

---

### Task 4: Native-mode escape — always restore a visible prompt (`nocx-4ff.9`)

**Problem:** If markers break while a marker-only prompt is active, the user faces an invisible prompt with no editor. Provide a state-independent escape: a keybinding that latches the tab to raw + hides the editor, and asks the shell to restore a visible prompt for future prompts.

**Files:**
- Create: `frontend/src/native-mode.ts` (+ `frontend/src/native-mode.test.ts`)
- Modify: `frontend/src/tabs.ts` (add `private nativeMode = false`; gate `onChange`; add keybinding)
- Modify: `internal/shellintegration/scripts/nocx.zsh`, `internal/shellintegration/scripts/nocx.bash` (add `__nocx_native_mode` restore function); bump `version` in `scripts.go`
- Test: `frontend/src/native-mode.test.ts`, `internal/shellintegration/scripts_exec_test.go`

**Interfaces:**
- Produces (TS): `shouldShowEditor(owned: boolean, nativeMode: boolean): boolean` = `owned && !nativeMode`.
- Produces (shell): `__nocx_native_mode` — removes the marker-only suppressor and sets a minimal visible fallback prompt (`PS1='%~ %# '` zsh / `PS1='\w \$ '` bash), unsets `NOCX_PROMPT_MODE`.

**Acceptance Criteria:**
- `shouldShowEditor` returns `false` whenever `nativeMode` is true, regardless of `owned`.
- Invoking the escape hides the editor, focuses the grid, and writes the restore invocation to the PTY.
- After `__nocx_native_mode` runs in a marker-only shell, the next prompt is **visible** (a hostile-clobber-style exec test asserts a non-empty visible prompt returns).

- [ ] **Step 1: Write the failing TS test** — `frontend/src/native-mode.test.ts`:

```ts
import { describe, it, expect } from 'vitest'
import { shouldShowEditor } from './native-mode'

describe('shouldShowEditor', () => {
  it('shows only when owned and not in native mode', () => {
    expect(shouldShowEditor(true, false)).toBe(true)
    expect(shouldShowEditor(true, true)).toBe(false) // escape latched
    expect(shouldShowEditor(false, false)).toBe(false)
  })
})
```

- [ ] **Step 2: Run to verify failure**

Run: `cd frontend && npx vitest run src/native-mode.test.ts`
Expected: FAIL — cannot resolve `./native-mode`.

- [ ] **Step 3: Implement** — `frontend/src/native-mode.ts`:

```ts
// Native-mode escape (ADR-0004 §1, nocx-4ff.9). A tab latched into native mode
// never shows the editor again this session, no matter what markers arrive —
// the state-independent guarantee that the user is never trapped.
export const NATIVE_RESTORE = '__nocx_native_mode\r'

export function shouldShowEditor(owned: boolean, nativeMode: boolean): boolean {
  return owned && !nativeMode
}
```

In `frontend/src/tabs.ts`: add `private nativeMode = false`, change the `onChange` handler to `if (shouldShowEditor(m.owned, this.nativeMode)) this.editor!.show()` else hide+focus, and add a keybinding on the pane (`Ctrl/Cmd+Shift+.`) that sets `this.nativeMode = true`, hides the editor, focuses the grid, and `this.session?.send(NATIVE_RESTORE)`.

- [ ] **Step 4: Run to verify pass**

Run: `cd frontend && npx vitest run src/native-mode.test.ts && npx tsc --noEmit`
Expected: PASS; typecheck clean.

- [ ] **Step 5: Add the shell restore function** — append to `internal/shellintegration/scripts/nocx.zsh`:

```zsh
# Native-mode escape (nocx-4ff.9): drop the marker-only overlay and restore a
# visible prompt on the next precmd. Called by nocx when the user hits escape.
__nocx_native_mode() {
    add-zsh-hook -d precmd __nocx_marker_only_prompt 2>/dev/null
    unset NOCX_PROMPT_MODE
    PROMPT='%~ %# '
    PS1='%~ %# '
}
```

and to `internal/shellintegration/scripts/nocx.bash`:

```bash
# Native-mode escape (nocx-4ff.9): restore a visible prompt.
__nocx_native_mode() {
    unset NOCX_PROMPT_MODE
    PS1='\w \$ '
}
```

Bump `const version` in `internal/shellintegration/scripts.go` (e.g. `"4"`).

- [ ] **Step 6: Write + pass the exec test** — in `internal/shellintegration/scripts_exec_test.go`, spawn a marker-only zsh, run `__nocx_native_mode`, then a command, and assert a visible prompt (e.g. contains `%` / the cwd glyph) reappears. Mirror the existing `runInteractiveZsh` helper.

Run: `go test ./internal/shellintegration/... -run NativeMode` → PASS.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/native-mode.ts frontend/src/native-mode.test.ts frontend/src/tabs.ts \
        internal/shellintegration/scripts/nocx.zsh internal/shellintegration/scripts/nocx.bash \
        internal/shellintegration/scripts.go internal/shellintegration/scripts_exec_test.go
git commit -m "feat(input): native-mode escape restores a visible prompt from any state (nocx-4ff.9)"
```

---

### Task 5: Verify raw-input routing after submit (`nocx-4ff.4`)

**Note:** With Task 2 dispatching `{submit}`, `RUNNING_RAW` → `owned:false` → `onChange` hides the editor and focuses the grid, so keys already flow `renderer.onData → session.send → PTY`. This task **proves** it across interactive programs and locks it with tests; it adds fixes only if a case fails.

**Files:**
- Modify: `frontend/src/tabs.ts` (only if a case reveals the editor stealing input)
- Test: `frontend/src/input-state.test.ts` (editor-visibility invariant), `e2e/enhanced-input.spec.ts` (create)

**Acceptance Criteria:**
- The editor is hidden in every non-owned state (`RUNNING_RAW`, `ALT_SCREEN`, `RAW`).
- Manual matrix passes: `read`, python3 REPL, node REPL, `less`, `vim`, `htop`, a `sudo`/password prompt, `Ctrl-C`, `Ctrl-D` — typed input reaches the program; the editor never appears mid-program.

- [ ] **Step 1: Write the invariant test** — add to `frontend/src/input-state.test.ts`:

```ts
import { shouldShowEditor } from './native-mode'
it('editor is hidden in every non-owned state (nocx-4ff.4)', () => {
  const running = [
    { type: 'marker', kind: 'A' } as InputEvent,
    { type: 'marker', kind: 'B' } as InputEvent,
    { type: 'submit' } as InputEvent, // -> RUNNING_RAW, owned:false
  ].reduce(reduce, initialMachine())
  expect(running.owned).toBe(false)
  expect(shouldShowEditor(running.owned, false)).toBe(false)
  const alt = reduce(running, { type: 'buffer', buffer: 'alternate' })
  expect(shouldShowEditor(alt.owned, false)).toBe(false)
})
```

Run: `cd frontend && npx vitest run src/input-state.test.ts` → PASS (confirms the invariant already holds after Tasks 1-2).

- [ ] **Step 2: Add an e2e for `read`** — `e2e/enhanced-input.spec.ts`, mirroring the existing `e2e/*.spec.ts` harness: launch the app, at a prompt type `read x; echo "got:$x"`, submit, then type `hello`↵ and assert `got:hello` appears (input reached the running `read`, not the editor).

Run: `cd frontend && npx playwright test e2e/enhanced-input.spec.ts` → PASS.

- [ ] **Step 3: Run the manual matrix** — build the app (`wails dev` or the project's run recipe), and with `ENHANCED_INPUT` temporarily on locally, verify each program in the Acceptance matrix. Record results in the bead. File a bug bead for any case where the editor steals input.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/input-state.test.ts e2e/enhanced-input.spec.ts
git commit -m "test(input): raw-routing invariant + read e2e after submit (nocx-4ff.4)"
```

---

### Task 6: Enable `ENHANCED_INPUT` + hardening (`nocx-4ff.13`)

**Gate:** Tasks 3 (readiness) and 4 (escape) MUST be merged first — they are the ADR-required safety controls before the flag goes on.

**Files:**
- Modify: `frontend/src/tabs.ts:22` (`ENHANCED_INPUT = true`)
- Modify: `internal/shellintegration/shellintegration.go:199` (`newSessionID` fail-closed)
- Modify: `internal/shellintegration/scripts/nocx.zsh`, `nocx.bash` (nested-session gate); bump `version`
- Test: `internal/shellintegration/shellintegration_test.go`, `internal/shellintegration/scripts_exec_test.go`

**Acceptance Criteria:**
- `newSessionID` fails **closed**: on `crypto/rand` error it returns `("", false)` and `ActivationEnv(true)` then omits `NOCX_PROMPT_MODE`/`NOCX_SESSION_ID` (enhanced disabled, not a predictable id).
- A nested integrated shell (env already carries `NOCX_SESSION_ID`) does **not** re-install the marker-only overlay — it stays visible; only the top-level session owns input.
- Default-on: a clean local zsh/bash prompt shows the editable block; unintegrated/nested shells fail open.
- zsh source-first + bash array/p10k exec tests pass.

- [ ] **Step 1: Write the failing fail-closed test** — `internal/shellintegration/shellintegration_test.go`:

```go
func TestActivationEnvFailsClosedOnRandError(t *testing.T) {
	restore := swapRandReader(errReader{}) // helper: replaces the package rand source with one that errors
	defer restore()
	enh := New(testLogger(t)).ActivationEnv(true)
	joined := strings.Join(enh, "\n")
	if strings.Contains(joined, "NOCX_PROMPT_MODE") || strings.Contains(joined, "NOCX_SESSION_ID") {
		t.Fatalf("enhanced env must be omitted when session id cannot be generated: %v", enh)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/shellintegration/ -run FailsClosed`
Expected: FAIL — current `newSessionID` returns the constant `"nocx"` and still emits the enhanced vars.

- [ ] **Step 3: Implement fail-closed** — in `shellintegration.go`, make id generation signal failure and have `ActivationEnv` drop enhanced on failure:

```go
func newSessionID() (string, bool) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", false
	}
	return hex.EncodeToString(b[:]), true
}

func (s *Impl) ActivationEnv(enhanced bool) []string {
	env := []string{activationEnvVar + "=1"}
	if enhanced {
		sid, ok := newSessionID()
		if !ok {
			s.log.Warn("shellintegration: session id unavailable; disabling enhanced prompt (fail-closed)")
			return env
		}
		env = append(env, promptModeEnvVar+"="+promptModeMarkerOnly, sessionIDEnvVar+"="+sid)
	}
	return env
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/shellintegration/...`
Expected: PASS.

- [ ] **Step 5: Nested-session gate** — in `nocx.zsh` / `nocx.bash`, guard the marker-only overlay install so a shell that inherits a `NOCX_SESSION_ID` it did not itself create keeps a visible prompt. Concretely, record the owning id at install (`__nocx_owned_session="$NOCX_SESSION_ID"`) and only arm `__nocx_marker_only_prompt` when `NOCX_PROMPT_MODE=marker-only` AND this is the top-level session (no pre-existing marker from a parent). Add an exec test spawning a nested shell and asserting its prompt stays visible. Bump `version`.

Run: `go test ./internal/shellintegration/... -run Nested` → PASS.

- [ ] **Step 6: Flip the flag** — `frontend/src/tabs.ts:22`:

```ts
// Enabled: readiness gating (nocx-4ff.10) + native-mode escape (nocx-4ff.9) landed.
const ENHANCED_INPUT = true
```

- [ ] **Step 7: Full verification**

Run: `cd frontend && npx tsc --noEmit && npx vitest run` then `go test ./... && go build ./...`
Then drive the app (project run recipe) and confirm: editable block at a clean local zsh prompt; typing edits mid-line; Enter runs once; `vim`/`htop` hide the editor; escape restores a visible prompt.
Expected: all green + observed working.

- [ ] **Step 8: Commit**

```bash
git add frontend/src/tabs.ts internal/shellintegration/shellintegration.go \
        internal/shellintegration/shellintegration_test.go \
        internal/shellintegration/scripts/nocx.zsh internal/shellintegration/scripts/nocx.bash \
        internal/shellintegration/scripts.go internal/shellintegration/scripts_exec_test.go
git commit -m "feat(input): enable ENHANCED_INPUT + fail-closed id + nested-session gate (nocx-4ff.13)"
```

---

## Definition of done (whole plan)

- All six task commits landed; `go test ./...` + `npx vitest run` + `npx tsc --noEmit` green.
- Manual: editable command block works at a clean local zsh/bash prompt; interactive programs and password prompts work after submit; the escape always restores a visible prompt.
- Deferred (tracked, not cut): editor chrome (`nocx-4ff` follow-ups), output-block decorations (`nocx-4ff.5`), history/completion (`nocx-4ff.6`), agent mode (`nocx-4ff.7`), SSH enhanced negotiation (ADR-0006 §6).
