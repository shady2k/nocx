# The import ask: one gesture, and a place already answered — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** A person drags a Postman export onto the import dialog and presses Import — the export's path and a destination under nocx's own collections folder are already filled in, and the terminal underneath does not also react to the drop.

**Architecture:** The window drop already works and needs no new backend capability — `SourceTicketStore.Dropped` takes a local-tab branch that mints nothing and answers with the file's absolute path, which is exactly what `api.import.postman` consumes. Three things are added around it: `files.dropped` learns to say WHICH drop target was hit (so the dialog and the terminal pane stop sharing one gesture), the kit gains a `DropZone` so the dialog does not hand-roll a second one, and the destination field is prefilled from the backend's `defaultRoot` on open instead of only after a file is chosen.

**Tech Stack:** Go 1.x (`internal/transport`, `internal/session`), Solid + TypeScript (`frontend/src/api`, `frontend/src/files`, `frontend/src/ui`), JSON Schema contracts in `contracts/` with generated renderer types, Vitest, Playwright.

**Spec:** `.internal/specs/2026-08-23-import-collection-ask-design.md`

## Global Constraints

- **Every Go command needs `-tags gtk3` on Linux.** Without it cgo fails on webkit before reaching our code: `go test -tags gtk3 ./internal/transport/...`.
- **Frontend commands run from `frontend/`**, except the repo-root gates.
- **Contracts:** a changed result/notification shape means editing `contracts/*.schema.json`, then `npm run contracts` to regenerate `frontend/src/generated/*.ts` (committed, never hand-edited), and `npm run contracts:check` must be green. Every schema keeps `additionalProperties: false` and an explicit `required`.
- **Kit rule:** a surface may PLACE a kit component (`flex`, `margin`, `width`, `order`, `align-self`, `position`) and may never REPAINT it (`background`, `border`, `color`, `font-*`, `padding`, `box-shadow`). No raw `<div class="st-…">` controls.
- **Greenfield:** no backward-compatibility shims. When `data-file-drop-target` gains a value, both sides set one — the empty string is not kept as a legacy case.
- **The worker runs the unit tests for the files it changed and stops there.** `make ci-full`, the containerized jobs and the e2e suite belong to whoever integrates.
- **Commit subject format:** `<type>(<scope>): <imperative subject, lower case, no full stop> (<bead-id>)`, body in prose explaining what was wrong and why this way.
- **Existing identities are not renamed:** `api-import-postman-file`, `api-import-postman-dest`, `api-import-curl`.

---

## File Structure

**Created**

- `frontend/src/ui/drop-zone.tsx` — the kit's drop surface. Renders a container carrying the drop-target attributes and a `data-drop-active` state; owns none of the drop's meaning.
- `frontend/src/ui/drop-zone.test.tsx` — its unit test.
- `frontend/src/styles/components/drop-zone.css` — its paint, and the only place it is painted.
- `e2e/api-import.spec.ts` — what the harness can actually watch: the ask opens on our folder, a typed path imports, and no drop target is advertised where there is no Wails.

**Modified**

- `contracts/files.dropped.schema.json` — one new required field, `target`.
- `frontend/src/generated/files.dropped.ts` — regenerated.
- `internal/transport/ws_upload_source.go` — read the target's name; carry it in the emit.
- `internal/transport/ws_upload_source_test.go` — its tests.
- `internal/transport/ws_contract_test.go` — the over-the-wire conformance.
- `frontend/src/terminal-content.ts:3735,3743` — the pane declares `data-file-drop-target="terminal"`.
- `frontend/src/files/terminal-drop.ts:358` — filter on the target's name as well as the session.
- `frontend/src/panes.ts` — `anyLocalSession()`, a live answer to "which local session is open".
- `frontend/src/main.tsx:442-446,567` — the latch goes; the watch port and the drop capability read the live answer, and the drop capability exists only where `hasWailsWebview()` holds.
- `frontend/src/api/api-client.ts` — a third optional capability, `NativeDropPort`.
- `frontend/src/api/api-content.ts` — holds it beside the two pickers and forwards it.
- `frontend/src/api/api-test-fixtures.ts` — the harness learns the drop and the ask's fields.
- `frontend/src/api/import-dialogs.tsx` — the dialog wears the drop zone; `ready()` refuses the bare root.
- `frontend/src/api/api-pane.tsx:498-503` — `askForImport` prefills from `defaultRoot`.
- `frontend/src/ui/README.md` — the `DropZone` row.

**Two things the plan sent back into the spec rather than deciding quietly**, both found writing Tasks 6 and 7: the drop target is the dialog's BODY, not the `<dialog>` element (the kit does not forward arbitrary `data-*`, and reaching past it would be the repaint rule in another form); and the drop capability is gated on `hasWailsWebview()` **and** a live local session, because the dev stand and the e2e harness have real sessions and no Wails, so a session-only gate lights up under a drag and delivers nothing. The spec now says both.

---

### Task 1: One live answer to "which local session is open"

**Files:**

- Modify: `frontend/src/panes.ts` (beside `activeOrigin()`, `panes.ts:1978`)
- Modify: `frontend/src/main.tsx:442-446` (delete the latch), `frontend/src/main.tsx:567`
- Test: `frontend/src/panes.test.ts`

**Interfaces:**

- Produces: `PaneManager.anyLocalSession(): string | null` — the session id of some OPEN local pane, or null. Read at call time, never cached.

**Why:** `main.tsx:442` latches the first local session ever seen and never clears it (`localSessionId`). `SourceTicketStore.Dropped` requires `data-session-id` to name a session the registry says is open, and refuses with `errDropTabNotOpen` otherwise. A latched id survives its tab, so a drop would fail naming a tab the person had closed. The watch port (`CollectionWatchPort.localSession`, `api-client.ts:318`) reads the same latch and has the same defect — so this replaces the latch for BOTH rather than adding a second answer beside it.

**Acceptance Criteria:**

- `anyLocalSession()` returns a local pane's session id while one is open.
- `anyLocalSession()` returns null once the last local pane is gone, with no re-render or event needed to make it true.
- `anyLocalSession()` returns null when the only panes are non-local (a viewer, a remote tab).
- `localSessionId` and `latchLocalSession` no longer exist in `main.tsx`.

- [ ] **Step 1: Write the failing test**

In `frontend/src/panes.test.ts`, beside the existing `activeOrigin` tests:

```ts
describe('anyLocalSession', () => {
  it('answers a local pane that is open, and null once it is gone', () => {
    const { tm } = mountPaneManager()
    const local = tm.newPane()
    stubOrigin(local, { sessionId: 'a'.repeat(32), kind: 'local', cwd: '/' })

    expect(tm.anyLocalSession()).toBe('a'.repeat(32))

    tm.closePane(local.id)
    // Read at call time: nothing was re-rendered and nothing was notified,
    // and the answer is still right. That is the whole point — the latch it
    // replaces stayed true after its tab was gone.
    expect(tm.anyLocalSession()).toBeNull()
  })

  it('answers null when no pane is local', () => {
    const { tm } = mountPaneManager()
    const remote = tm.newPane()
    stubOrigin(remote, { sessionId: 'b'.repeat(32), kind: 'remote', cwd: '/' })
    expect(tm.anyLocalSession()).toBeNull()
  })
})
```

Use whatever `mountPaneManager` / origin-stubbing helper the neighbouring `activeOrigin` tests in that file already use — do not invent a second harness.

- [ ] **Step 2: Run the test and watch it fail**

```bash
cd frontend && npx vitest run src/panes.test.ts -t anyLocalSession
```

Expected: FAIL — `tm.anyLocalSession is not a function`.

- [ ] **Step 3: Implement it**

In `frontend/src/panes.ts`, immediately after `activeOrigin()`:

```ts
  /** The session id of SOME open local pane, or null.
   *
   *  Read at call time and never latched. It replaces a signal in the
   *  composition root that held the first local session ever seen and was
   *  never cleared: a gesture that names a session — a window drop, a
   *  files.open for a collection watch — is refused by the backend when that
   *  session is not open, and the refusal then named a tab the person had
   *  closed and was not thinking about.
   *
   *  ACTIVE is deliberately not the question. The collection watch and the
   *  import drop both want "a local session this window can address", and the
   *  tab in front is not that question — the workbench pane is usually the one
   *  in front while either happens. */
  anyLocalSession(): string | null {
    for (const pane of this.panes) {
      const origin = pane.content.activeOrigin?.()
      if (origin && origin.kind === 'local') return origin.sessionId
    }
    return null
  }
```

- [ ] **Step 4: Run the test and watch it pass**

```bash
cd frontend && npx vitest run src/panes.test.ts -t anyLocalSession
```

Expected: PASS, 2 tests.

- [ ] **Step 5: Delete the latch and rewire the composition root**

In `frontend/src/main.tsx`, delete the signal and its updater (`main.tsx:442-446`) — the whole block from `const [localSessionId, setLocalSessionId] = createSignal<string | null>(null)` through the `latchLocalSession()` call, and the `latchLocalSession()` line inside `tm.onActivePaneChange`.

Then at `main.tsx:567`, inside the `createApiWorkbenchServices` watch port:

```ts
        localSession: () => tm.anyLocalSession(),
```

- [ ] **Step 6: Typecheck and run the suites the change touches**

```bash
cd frontend && npx tsc --noEmit -p tsconfig.json && npx vitest run src/panes.test.ts src/api/
```

Expected: tsc silent; all tests pass. If tsc names a remaining `localSessionId` reference, that reference is a second reader of the latch and must be moved to `tm.anyLocalSession()` too.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/panes.ts frontend/src/panes.test.ts frontend/src/main.tsx
git commit -m "refactor(frontend): which local session is open is answered at call time, not latched (<bead-id>)"
```

---

### Task 2: `files.dropped` says which target was dropped on

**Files:**

- Modify: `contracts/files.dropped.schema.json`
- Modify: `internal/transport/ws_upload_source.go:151-155` (the attr constants), `:445` (`Dropped`), `:556` (`EmitFilesDropped`)
- Regenerate: `frontend/src/generated/files.dropped.ts`
- Test: `internal/transport/ws_upload_source_test.go`

**Interfaces:**

- Consumes: nothing from Task 1.
- Produces:
  - Go const `dropTargetAttr = "data-file-drop-target"`.
  - `DropHost.EmitFilesDropped(sessionID, target string, picks []SourcePick) error` — the signature gains `target` as its second parameter.
  - Wire field `target: string` on `files.dropped`, `required`, non-empty.

**Why:** `files.dropped` is addressed by session, and every subscriber filters on the session alone (`terminal-drop.ts:358`). A dialog carrying a local tab's session id would therefore receive the drop together with that tab's pane, and the pane inserts the path at the prompt. Two surfaces owning one input, resolved by evaluation order. `Dropped` already receives EVERY attribute of the target element and reads exactly one of them, so the name of the target is already in hand and simply is not carried.

**Acceptance Criteria:**

- `Dropped` refuses a target whose `data-file-drop-target` is empty or absent, mints nothing, and emits nothing.
- The emitted notification carries `target` equal to that attribute's value.
- The schema declares `target` `required`, with `additionalProperties: false` unchanged.
- `npm run contracts:check` is green with the regenerated file committed.

- [ ] **Step 1: Write the failing Go tests**

In `internal/transport/ws_upload_source_test.go`, beside the existing `Dropped` tests:

```go
// The drop target's NAME is what separates two surfaces that share one
// session. Without it the import dialog and the terminal pane of the same
// local tab both act on one gesture, and which one wins is evaluation order.
func TestDropped_CarriesTheTargetName(t *testing.T) {
	host := &fakeDropHost{kind: session.KindLocal, open: true}
	store := NewSourceTicketStore(host, testLogger(t))
	file := writeTempFile(t, "acme.postman_collection.json", "{}")

	err := store.Dropped([]string{file}, map[string]string{
		"data-session-id":        strings.Repeat("a", 32),
		"data-file-drop-target":  "api-import",
	})
	if err != nil {
		t.Fatalf("Dropped: %v", err)
	}
	if host.target != "api-import" {
		t.Errorf("emitted target = %q, want %q", host.target, "api-import")
	}
}

// A target that names nothing is refused the way a drop with no session
// already is: a notification nobody can attribute is one every subscriber
// must guess about.
func TestDropped_RefusesATargetThatNamesNothing(t *testing.T) {
	host := &fakeDropHost{kind: session.KindLocal, open: true}
	store := NewSourceTicketStore(host, testLogger(t))
	file := writeTempFile(t, "acme.postman_collection.json", "{}")

	for name, attrs := range map[string]map[string]string{
		"absent": {"data-session-id": strings.Repeat("a", 32)},
		"empty": {
			"data-session-id":       strings.Repeat("a", 32),
			"data-file-drop-target": "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			host.emitted = 0
			if err := store.Dropped([]string{file}, attrs); err == nil {
				t.Fatal("Dropped succeeded with a target that names nothing")
			}
			if host.emitted != 0 {
				t.Errorf("emitted %d notifications; a refused drop emits none", host.emitted)
			}
		})
	}
}
```

Match the existing helper names in that file (`fakeDropHost`, `writeTempFile`, `testLogger`) rather than adding new ones; if `fakeDropHost` has no `target` or `emitted` field yet, add them to its `EmitFilesDropped` recorder.

- [ ] **Step 2: Run them and watch them fail**

```bash
go test -tags gtk3 -count=1 -run 'TestDropped_CarriesTheTargetName|TestDropped_RefusesATargetThatNamesNothing' ./internal/transport/
```

Expected: FAIL — the compile error `too many arguments` or `host.target undefined`, then the refusal test failing because a nameless target succeeds today.

- [ ] **Step 3: Add the constant and the refusal**

In `internal/transport/ws_upload_source.go`, in the const block at `:151`:

```go
	// dropSessionAttr is the attribute the drop target carries to say which
	// tab it is. Wails delivers every attribute of the element that carries
	// data-file-drop-target; this is the one we read.
	dropSessionAttr = "data-session-id"
	// dropTargetAttr is that element's own attribute, and it carries a NAME
	// rather than being a bare marker. It is what separates two surfaces
	// that legitimately share a session: the terminal pane of a local tab
	// and the API workbench's import ask both name that tab, and without a
	// target name one drop reaches both — whichever acts first wins, which
	// is not a rule anybody wrote down.
	dropTargetAttr = "data-file-drop-target"
)
```

And in the refusals block:

```go
	errDropNamesNoTarget = errors.New("the drop target does not name itself")
```

In `Dropped`, immediately after the `sessionID` checks:

```go
	target := attrs[dropTargetAttr]
	if target == "" {
		return errDropNamesNoTarget
	}
```

- [ ] **Step 4: Carry it through the emit**

Change the `DropHost` interface's method and `WSServer`'s implementation:

```go
func (s *WSServer) EmitFilesDropped(sessionID, target string, picks []SourcePick) error {
	rx := s.getRx(session.ID(sessionID))
	if rx == nil {
		return errDropNoRenderer
	}
	wconn, _ := rx.getSubscriber()
	if wconn == nil {
		return errDropNoRenderer
	}
	return wconn.TryNotify("files.dropped", mustMarshal(map[string]any{
		"sessionId": sessionID,
		"target":    target,
		"sources":   picks,
	}))
}
```

and the call site inside `Dropped`:

```go
	if err := s.emit.EmitFilesDropped(sessionID, target, picks); err != nil {
```

- [ ] **Step 5: Run the tests and watch them pass**

```bash
go test -tags gtk3 -count=1 -run TestDropped ./internal/transport/
```

Expected: PASS, including the pre-existing `Dropped` tests — update any of them that construct `attrs` without a target, since a nameless target is now refused.

- [ ] **Step 6: Declare it on the wire**

In `contracts/files.dropped.schema.json`, change `required` and add the property:

```json
  "required": ["sessionId", "target", "sources"],
```

```json
    "target": {
      "description": "WHICH drop target the files landed on: the value of the drop element's data-file-drop-target attribute, non-empty. The session above says which tab; this says which surface OF that tab, and the two are different questions the moment more than one surface can legitimately name one session — the terminal pane inserts a dropped path at its prompt (D9) while the API workbench's import ask reads it as the document to import, and both belong to the local tab. Without this, one gesture reached both and the winner was evaluation order. A drop whose target names nothing is refused rather than delivered to everybody, for the same reason a drop that names no session is: a notification nobody can attribute is one every subscriber has to guess about.",
      "type": "string",
      "minLength": 1
    },
```

- [ ] **Step 7: Regenerate, verify, run the contract test**

```bash
cd frontend && npm run contracts && npm run contracts:check
cd .. && go test -tags gtk3 -count=1 -run 'FilesDropped.*Contract' ./internal/transport/
```

Expected: `contracts:check` silent; the conformance tests pass. If `..._OverTheWireConformsToContract` for `files.dropped` does not exist yet, add it in `internal/transport/ws_contract_test.go` beside its siblings — the real notification off the real socket, validated against the schema, which is the only test that can report a field the server never sends.

- [ ] **Step 8: Commit**

```bash
git add contracts/files.dropped.schema.json frontend/src/generated/files.dropped.ts \
        internal/transport/ws_upload_source.go internal/transport/ws_upload_source_test.go \
        internal/transport/ws_contract_test.go
git commit -m "feat(transport): a drop says which target it landed on, not only which tab (<bead-id>)"
```

---

### Task 3: The terminal pane names itself and acts only on its own drops

**Files:**

- Modify: `frontend/src/terminal-content.ts:3735,3743`
- Modify: `frontend/src/files/terminal-drop.ts:358`
- Test: `frontend/src/files/terminal-drop.test.ts`

**Interfaces:**

- Consumes: `FilesDropped.target` from Task 2.
- Produces: the literal `'terminal'` as the pane's drop-target name. Task 6 uses `'api-import'` for the dialog.

**Acceptance Criteria:**

- The pane element carries `data-file-drop-target="terminal"` while it is a drop target.
- A `files.dropped` for the pane's session with `target: 'terminal'` is handled as before.
- A `files.dropped` for the pane's session with any other target is ignored — no prompt insert, no upload.

- [ ] **Step 1: Write the failing test**

In `frontend/src/files/terminal-drop.test.ts`, beside the existing native-drop tests:

```ts
it('ignores a drop on another surface of the same session', () => {
  const inserted: string[] = []
  const { emitDropped } = mountTerminalDrop({
    origin: { sessionId: SESSION, kind: 'local', cwd: '/work' },
    insert: (t) => inserted.push(t),
  })

  // The same tab, a different surface — the import ask. The pane must not
  // type the export's path at the person's prompt because they dropped it
  // into a dialog that happens to name this tab.
  emitDropped({
    sessionId: SESSION,
    target: 'api-import',
    sources: [{ sourceTicket: '', name: 'acme.json', size: 2, localPath: '/work/acme.json' }],
  })
  expect(inserted).toEqual([])

  emitDropped({
    sessionId: SESSION,
    target: 'terminal',
    sources: [{ sourceTicket: '', name: 'acme.json', size: 2, localPath: '/work/acme.json' }],
  })
  expect(inserted).toHaveLength(1)
})
```

Use the file's existing mount helper and `SESSION` constant rather than new ones.

- [ ] **Step 2: Run it and watch it fail**

```bash
cd frontend && npx vitest run src/files/terminal-drop.test.ts -t 'another surface'
```

Expected: FAIL — `expect(inserted).toEqual([])` receives one entry, because the filter is on the session alone.

- [ ] **Step 3: Filter on the target too**

In `frontend/src/files/terminal-drop.ts`, replace the filter at `:358`:

```ts
  /** The native half. Filtered by session AND by target: every pane
   *  subscribes and exactly one of them was dropped on — and since
   *  nocx-cx442 a session can have more than one drop surface, because the
   *  import ask names the local tab too. Session alone had the pane typing
   *  an export's path at the prompt because somebody dropped it in a
   *  dialog. */
  const unsubDropped = deps.services.subscribeDropped((p) => {
    if (p.sessionId !== origin()?.sessionId) return
    if (p.target !== TERMINAL_DROP_TARGET) return
```

and export the name from the module that owns it, at the top of `terminal-drop.ts`:

```ts
/** This surface's name in `data-file-drop-target`. Exported so the element
 *  that carries the attribute and the subscriber that filters on it read one
 *  constant — two string literals is how they drift apart. */
export const TERMINAL_DROP_TARGET = 'terminal'
```

- [ ] **Step 4: Put the name on the element**

In `frontend/src/terminal-content.ts`, import the constant and change `:3735`:

```ts
pane.setAttribute('data-file-drop-target', TERMINAL_DROP_TARGET)
```

`:3743`'s `removeAttribute` is unchanged.

- [ ] **Step 5: Run the suites**

```bash
cd frontend && npx vitest run src/files/ src/terminal-content.test.ts
```

Expected: PASS. Existing tests that emit a `files.dropped` fixture without `target` will now be filtered out — give each the target it means, which for the pane's own tests is `TERMINAL_DROP_TARGET`.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/files/terminal-drop.ts frontend/src/files/terminal-drop.test.ts frontend/src/terminal-content.ts
git commit -m "fix(frontend): the terminal pane acts on its own drops, not on every drop of its tab (<bead-id>)"
```

---

### Task 4: `DropZone` joins the kit

**Files:**

- Create: `frontend/src/ui/drop-zone.tsx`, `frontend/src/ui/drop-zone.test.tsx`, `frontend/src/styles/components/drop-zone.css`
- Modify: `frontend/src/style.css` (the `@import` beside the other component sheets), `frontend/src/ui/README.md`

**Interfaces:**

- Produces:

```ts
export interface DropZoneProps {
  /** This surface's name in `data-file-drop-target`. */
  target: string
  /** The session the drop belongs to, or null. Null draws NO drop target —
   *  and so does a caller that never passes one, which is how a build with
   *  no native drop at all says so. */
  sessionId: string | null
  /** What the zone says while a drag is over it. */
  hint: string
  children: JSX.Element
}
export function DropZone(props: DropZoneProps): JSX.Element
```

**Why:** `frontend/src/ui/` has no drop zone — `file-input.tsx` is a picker. `terminal-drop.ts` hand-rolls its own dragover/dragleave with a `data-drop-active` dataset on the pane. A second hand-rolled one in the dialog is two vocabularies for one concept.

**Acceptance Criteria:**

- With a session id, the container carries `data-file-drop-target="<target>"` and `data-session-id="<sessionId>"`.
- With `sessionId: null`, NEITHER attribute is present — and the children still render.
- `dragover` sets `data-drop-active`; `dragleave` and `drop` clear it.
- The hint is not in the accessibility tree as a control — it is text, not a button.
- `ui-drop-zone` has a row in `frontend/src/ui/README.md`.

- [ ] **Step 1: Write the failing test**

`frontend/src/ui/drop-zone.test.tsx`:

```tsx
// @vitest-environment jsdom
import { describe, it, expect, afterEach } from 'vitest'
import { render, fireEvent } from '@solidjs/testing-library'
import { DropZone } from './drop-zone'

const SESSION = 'a'.repeat(32)

describe('DropZone', () => {
  afterEach(() => document.body.replaceChildren())

  it('declares the target and the session it belongs to', () => {
    const { container } = render(() => (
      <DropZone target="api-import" sessionId={SESSION} hint="Drop an export here">
        <input data-testid="child" />
      </DropZone>
    ))
    const zone = container.querySelector<HTMLElement>('.ui-drop-zone')!
    expect(zone.dataset.fileDropTarget).toBe('api-import')
    expect(zone.dataset.sessionId).toBe(SESSION)
    expect(container.querySelector('[data-testid="child"]')).not.toBeNull()
  })

  it('declares NO drop target without a session, and still renders its children', () => {
    // A target naming no session is refused by the backend, so advertising
    // one is advertising a gesture that cannot work. Absence is the
    // capability — the same rule the dialog pickers already follow.
    const { container } = render(() => (
      <DropZone target="api-import" sessionId={null} hint="Drop an export here">
        <input data-testid="child" />
      </DropZone>
    ))
    const zone = container.querySelector<HTMLElement>('.ui-drop-zone')!
    expect(zone.hasAttribute('data-file-drop-target')).toBe(false)
    expect(zone.hasAttribute('data-session-id')).toBe(false)
    expect(container.querySelector('[data-testid="child"]')).not.toBeNull()
  })

  it('marks itself active while a drag is over it, and stops when it leaves', () => {
    const { container } = render(() => (
      <DropZone target="api-import" sessionId={SESSION} hint="Drop an export here">
        <span />
      </DropZone>
    ))
    const zone = container.querySelector<HTMLElement>('.ui-drop-zone')!
    fireEvent.dragOver(zone)
    expect(zone.dataset.dropActive).toBe('')
    fireEvent.dragLeave(zone)
    expect(zone.dataset.dropActive).toBeUndefined()
  })
})
```

- [ ] **Step 2: Run it and watch it fail**

```bash
cd frontend && npx vitest run src/ui/drop-zone.test.tsx
```

Expected: FAIL — `Cannot find module './drop-zone'`.

- [ ] **Step 3: Write the component**

`frontend/src/ui/drop-zone.tsx`:

```tsx
// A surface that accepts a native window drop, as the KIT's.
//
// It owns the AFFORDANCE and the two attributes the backend reads, and
// nothing about what a drop MEANS: `data-file-drop-target` names the
// surface, `data-session-id` names the tab, Wails hands Go every attribute
// of the element the drop landed on, and the answer comes back as a
// `files.dropped` notification the caller subscribes to. So there is no
// `onDrop` here — the drop does not become a DOM event with a path in it,
// and a callback would be a promise this component cannot keep.
//
// NO SESSION DRAWS NO TARGET. `SourceTicketStore.Dropped` refuses a target
// that names no open session, so advertising a drop surface without one
// advertises a gesture that will be refused. Absence is the capability,
// which is the rule the dialog's pickers already follow.

import { Show, createSignal } from 'solid-js'
import type { JSX } from 'solid-js'

export interface DropZoneProps {
  /** This surface's name in `data-file-drop-target`. */
  target: string
  /** The session the drop belongs to, or null — null draws NO drop target. */
  sessionId: string | null
  /** What the zone says while a drag is over it. */
  hint: string
  children: JSX.Element
}

export function DropZone(props: DropZoneProps) {
  const [active, setActive] = createSignal(false)
  const live = () => props.sessionId !== null

  return (
    <div
      class="ui-drop-zone"
      data-file-drop-target={live() ? props.target : undefined}
      data-session-id={props.sessionId ?? undefined}
      data-drop-active={active() ? '' : undefined}
      onDragOver={(e: DragEvent) => {
        if (!live()) return
        // Preventing the default is what makes this a drop target at all;
        // without it the engine treats the drag as a navigation.
        e.preventDefault()
        setActive(true)
      }}
      onDragLeave={() => setActive(false)}
      onDrop={() => setActive(false)}
    >
      {props.children}
      <Show when={live() && active()}>
        <div class="ui-drop-zone__hint" aria-hidden="true">
          {props.hint}
        </div>
      </Show>
    </div>
  )
}
```

- [ ] **Step 4: Write its paint**

`frontend/src/styles/components/drop-zone.css`:

```css
/* DropZone — the kit's drop surface. The overlay is drawn only while a drag
   is over it: a permanent dashed box in a dialog that already carries fields
   and a footer is a third thing competing for the same column. */
.ui-drop-zone {
  position: relative;
  display: contents;
}

.ui-drop-zone[data-drop-active] {
  display: block;
}

.ui-drop-zone__hint {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  border: 1px dashed var(--color-accent);
  border-radius: var(--radius-md);
  background: var(--color-surface-overlay);
  color: var(--color-text);
  font-size: var(--font-size-sm);
  pointer-events: none;
}
```

Use the token names this repo's other component sheets already use — read one neighbour in `frontend/src/styles/components/` first and match it. `npm run lint` runs `check-css-colors.mjs`, which refuses a colour literal.

Add the import to `frontend/src/style.css` beside the other `components/` sheets, in the file's existing order.

- [ ] **Step 5: Run the test and watch it pass**

```bash
cd frontend && npx vitest run src/ui/drop-zone.test.tsx
```

Expected: PASS, 3 tests.

- [ ] **Step 6: Add the README row**

In `frontend/src/ui/README.md`, in the main component table, in the row order the table already uses:

```
| **DropZone**           | `drop-zone.tsx`  | `ui-drop-zone`, `ui-drop-zone__hint` | a surface that accepts a native window drop: it carries `data-file-drop-target` (this surface's NAME) and `data-session-id` (the tab), which is what the Wails side reads off the dropped-on element, and marks itself `data-drop-active` while a drag is over it. It has no `onDrop` and cannot have one — in the Wails window the drop never becomes a DOM event carrying a path, so the answer arrives as a `files.dropped` notification the caller subscribes to. `sessionId: null` draws NEITHER attribute: the backend refuses a target naming no open session, so advertising the gesture would advertise a refusal. |
```

- [ ] **Step 7: Gates and commit**

```bash
cd frontend && npm run lint && npx prettier --check src/ui/README.md src/ui/drop-zone.tsx
```

```bash
git add frontend/src/ui/drop-zone.tsx frontend/src/ui/drop-zone.test.tsx \
        frontend/src/styles/components/drop-zone.css frontend/src/style.css frontend/src/ui/README.md
git commit -m "feat(ui): the kit gains a drop surface, so the second one is not hand-rolled (<bead-id>)"
```

---

### Task 5: The destination is proposed on open, and the bare root is not an answer

**Files:**

- Modify: `frontend/src/api/api-pane.tsx:498-503` (`askForImport`)
- Modify: `frontend/src/api/import-dialogs.tsx` (`PostmanImportDialogProps`, `ready()`, the destination `TextField`)
- Test: `frontend/src/api/api-workbench.test.tsx`

**Interfaces:**

- Consumes: `store.defaultRoot(): string` (`api-store.ts:236`), `proposedDestination(defaultRoot, exportPath)` (`api-paths.ts:54`) — both already exist.
- Produces: `PostmanImportDialogProps.defaultRoot: string` — the root the ask must not accept as a whole answer.

**Why:** `nocx-6hg2w.14` put `defaultRoot` on the wire and made the destination prefill AFTER a file is chosen. Before that, both fields are empty and the destination's placeholder says `/work/acme-api` — an arbitrary path rather than ours. Prefilling on open also creates a value that would only ever be refused, so `ready()` has to learn about it.

**Acceptance Criteria:**

- Opening the ask with `defaultRoot: "/data/collections"` renders `/data/collections/` in `#api-import-postman-dest`.
- With that value unedited, Import is DISABLED.
- Typing a last segment enables Import.
- Opening with `defaultRoot: ""` renders an empty destination and NO placeholder on that field.
- A destination the person edited is not overwritten when a source is then chosen — the `nocx-6hg2w.14` rule, re-asserted because this moves the code that honours it.

- [ ] **Step 1: Write the failing tests**

In `frontend/src/api/api-workbench.test.tsx`, beside the existing import tests:

```tsx
describe('the import ask proposes our folder', () => {
  it('opens with the default root already in the destination, and Import disabled', async () => {
    const h = await mountWorkbench({ defaultRoot: '/data/collections' })
    await h.openImportAsk()

    const dest = h.field('api-import-postman-dest')
    expect(dest.value).toBe('/data/collections/')
    // The root names a folder that certainly exists, so submitting it could
    // only come back "a folder is already there" about the collections root
    // rather than about anything the person chose.
    expect(h.importButton().disabled).toBe(true)

    fireEvent.input(dest, { target: { value: '/data/collections/acme' } })
    expect(h.importButton().disabled).toBe(false)
  })

  it('proposes nothing on a build with no default location', async () => {
    const h = await mountWorkbench({ defaultRoot: '' })
    await h.openImportAsk()

    const dest = h.field('api-import-postman-dest')
    expect(dest.value).toBe('')
    expect(dest.getAttribute('placeholder')).toBeNull()
  })

  it('does not overwrite a destination the person edited', async () => {
    const h = await mountWorkbench({ defaultRoot: '/data/collections' })
    await h.openImportAsk()

    fireEvent.input(h.field('api-import-postman-dest'), { target: { value: '/work/mine' } })
    await h.chooseExport('/downloads/acme.postman_collection.json')

    expect(h.field('api-import-postman-dest').value).toBe('/work/mine')
  })
})
```

Use the file's existing mount/fixture helpers (`api-test-fixtures.ts` already takes a `defaultRoot`, `api-test-fixtures.ts:362`) rather than new ones; add `openImportAsk`, `field`, `importButton` and `chooseExport` helpers there if the file does not already have equivalents.

- [ ] **Step 2: Run them and watch them fail**

```bash
cd frontend && npx vitest run src/api/api-workbench.test.tsx -t 'proposes our folder'
```

Expected: FAIL — the destination is `''` on open.

- [ ] **Step 3: Prefill on open**

In `frontend/src/api/api-pane.tsx`, replace `askForImport` (`:498`):

```ts
const askForImport = (): void => {
  setPostmanFile('')
  // OUR FOLDER, before anything is chosen. `proposedDestination` completes
  // this to <defaultRoot>/<stem> the moment a source is named, but until
  // then the field said nothing at all and its placeholder said
  // /work/acme-api — an arbitrary path rather than the place this product
  // keeps collections, which is the same place `Create` next door puts one
  // without asking (nocx-cx442).
  //
  // Written through the signal rather than through `onDest`, so it does
  // not set `destTyped`: the surface proposing a value is not the person
  // having said one, and a later pick must still be able to complete it.
  const root = store.defaultRoot()
  setPostmanDest(root === '' ? '' : `${root.replace(/[\\/]+$/, '')}/`)
  setDestTyped(false)
  setImportRefused('')
  setImporting(true)
}
```

- [ ] **Step 4: Teach the ask that the root alone is not an answer**

In `frontend/src/api/import-dialogs.tsx`, add the prop to `PostmanImportDialogProps`:

```ts
/** The place collections go, as the backend gave it, or '' where this
 *  build has none. The ask needs it to recognise its own proposal: the
 *  field opens holding it, and the root by itself names a folder that is
 *  already there. */
defaultRoot: string
```

and replace `ready()`:

```ts
/** The root the ask proposed, with or without its trailing separator —
 *  the two values `askForImport` can leave in the field before anybody
 *  has said anything. */
const isBareRoot = (value: string): boolean => {
  if (props.defaultRoot === '') return false
  const root = props.defaultRoot.replace(/[\\/]+$/, '')
  return value === root || value === `${root}/`
}

// A blank was already refused here because a call that could only be
// refused is a round trip spent to learn what the form knew. The prefill
// introduces a second value of exactly that kind: the collections root
// certainly exists, so Import on it comes back "a folder is already
// there" about a folder the person never chose.
const ready = () => {
  const dest = props.dest.trim()
  return props.file.trim() !== '' && dest !== '' && !isBareRoot(dest) && !props.busy
}
```

Delete `placeholder="/work/acme-api"` from the destination `TextField`. The source field's placeholder stays — that field is still empty on open.

- [ ] **Step 5: Pass the prop**

In `frontend/src/api/api-pane.tsx`, in the `<PostmanImportDialog>` element (`:996`):

```tsx
          defaultRoot={store.defaultRoot()}
```

- [ ] **Step 6: Run the tests and watch them pass**

```bash
cd frontend && npx vitest run src/api/ && npx tsc --noEmit -p tsconfig.json
```

Expected: PASS; tsc silent.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/api/api-pane.tsx frontend/src/api/import-dialogs.tsx frontend/src/api/api-workbench.test.tsx
git commit -m "feat(api,ui): the import ask opens with our collections folder already in it (<bead-id>)"
```

---

### Task 6: The ask accepts a drop

**Files:**

- Modify: `frontend/src/api/import-dialogs.tsx` (wear the `DropZone`)
- Modify: `frontend/src/api/api-client.ts` (`ApiWorkbenchServices` gains the optional drop capability)
- Modify: `frontend/src/api/api-content.ts:33-62` (hold it beside the pickers, forward it)
- Modify: `frontend/src/api/api-pane.tsx` (a new optional prop; subscribe; refuse a multi-file drop)
- Modify: `frontend/src/main.tsx` (bind it, gated on the Wails runtime)
- Test: `frontend/src/api/api-workbench.test.tsx`

**Interfaces:**

- Consumes: `DropZone` (Task 4), `tm.anyLocalSession()` (Task 1), `FilesDropped.target` (Task 2), `chooseExport(path)` (`api-pane.tsx:440`, unchanged), `hasWailsWebview()` (`frontend/src/wails-runtime.ts:58`).
- Produces:
  - `API_IMPORT_DROP_TARGET = 'api-import'`, exported from `import-dialogs.tsx`.
  - `ApiWorkbenchServices.nativeDrop?: NativeDropPort`, where

```ts
export interface NativeDropPort {
  /** The local session a drop on this surface belongs to, read at call
   *  time, or null when this window has none. */
  session(): string | null
  /** files.dropped — the Wails window drop. Returns the unsubscribe. */
  subscribe(handler: (p: FilesDropped) => void): () => void
}
```

- `ApiPaneProps.nativeDrop?: NativeDropPort`, forwarded by `ApiContent`.

**Why the capability is a whole optional port and not a session string.** `ApiPane`
already receives `openDirectory` and `openFile` as separate optional props, each
with the comment saying why: either can be absent on its own, and one signal for
both would draw a control the build cannot honour. The native drop is a third
capability of exactly that kind, and it is absent for a reason the session cannot
express — **there is no Wails runtime**. `make dev-web` and the e2e harness both
have real local sessions and no Wails, so a target gated on the session alone
lights up under a drag and then delivers nothing, which is a promise the surface
cannot keep.

**Acceptance Criteria:**

- With `nativeDrop` present and its `session()` non-null, the ask renders
  `data-file-drop-target="api-import"` carrying that session id.
- Without `nativeDrop` at all, the ask renders no `data-file-drop-target`, and the
  field and picker still work.
- With `nativeDrop` present but `session()` null, likewise no drop target.
- A `files.dropped` with `target: 'api-import'` and one source fills the export
  field with its `localPath` and proposes the destination — the same result
  `chooseExport` gives.
- A `files.dropped` with `target: 'terminal'` leaves both fields untouched.
- A multi-file drop leaves both fields unchanged and renders a refusal in the
  destination field's validation slot.

- [ ] **Step 1: Write the failing tests**

In `frontend/src/api/api-workbench.test.tsx`:

```tsx
describe('the import ask accepts a drop', () => {
  it('fills both fields from one gesture', async () => {
    const h = await mountWorkbench({ defaultRoot: '/data/collections', dropSession: SESSION })
    await h.openImportAsk()

    h.emitDropped({
      sessionId: SESSION,
      target: 'api-import',
      sources: [
        {
          sourceTicket: '',
          name: 'acme.postman_collection.json',
          size: 12,
          localPath: '/downloads/acme.postman_collection.json',
        },
      ],
    })

    expect(h.field('api-import-postman-file').value).toBe('/downloads/acme.postman_collection.json')
    expect(h.field('api-import-postman-dest').value).toBe('/data/collections/acme')
  })

  it('ignores a drop meant for the terminal', async () => {
    const h = await mountWorkbench({ defaultRoot: '/data/collections', dropSession: SESSION })
    await h.openImportAsk()

    h.emitDropped({
      sessionId: SESSION,
      target: 'terminal',
      sources: [{ sourceTicket: '', name: 'a.json', size: 2, localPath: '/downloads/a.json' }],
    })

    expect(h.field('api-import-postman-file').value).toBe('')
  })

  it('refuses several files with a sentence, and changes nothing', async () => {
    // One import makes one collection; N collections is N destinations,
    // which is a different question.
    const h = await mountWorkbench({ defaultRoot: '/data/collections', dropSession: SESSION })
    await h.openImportAsk()

    h.emitDropped({
      sessionId: SESSION,
      target: 'api-import',
      sources: [
        { sourceTicket: '', name: 'a.json', size: 2, localPath: '/downloads/a.json' },
        { sourceTicket: '', name: 'b.json', size: 2, localPath: '/downloads/b.json' },
      ],
    })

    expect(h.field('api-import-postman-file').value).toBe('')
    expect(h.field('api-import-postman-dest').value).toBe('/data/collections/')
    expect(h.destError()).toMatch(/one export/i)
  })

  // The two absences fail independently, so they are two tests. A build with
  // no Wails still has local sessions — make dev-web and the e2e harness both
  // do — so a target gated on the session alone would light up under a drag
  // there and then deliver nothing.
  it('draws no drop target where there is no native drop at all', async () => {
    const h = await mountWorkbench({ defaultRoot: '/data/collections', nativeDrop: undefined })
    await h.openImportAsk()

    expect(h.dialogBody().querySelector('[data-file-drop-target]')).toBeNull()
    fireEvent.input(h.field('api-import-postman-file'), { target: { value: '/downloads/a.json' } })
    expect(h.field('api-import-postman-file').value).toBe('/downloads/a.json')
  })

  it('draws no drop target when this window has no local session', async () => {
    const h = await mountWorkbench({ defaultRoot: '/data/collections', dropSession: null })
    await h.openImportAsk()

    expect(h.dialogBody().querySelector('[data-file-drop-target]')).toBeNull()
  })
})
```

Add `dropSession` / `nativeDrop` / `emitDropped` / `destError` / `dialogBody` to the
fixtures in `frontend/src/api/api-test-fixtures.ts` beside the existing
`defaultRoot` support (`api-test-fixtures.ts:362`) — one harness, not a second.

- [ ] **Step 2: Run them and watch them fail**

```bash
cd frontend && npx vitest run src/api/api-workbench.test.tsx -t 'accepts a drop'
```

Expected: FAIL — no drop target is rendered and `emitDropped` reaches nothing.

- [ ] **Step 3: Declare the capability**

In `frontend/src/api/api-client.ts`, beside `DirectoryPicker` and `FilePicker`:

```ts
/**
 * The native window drop, as the workbench needs it.
 *
 * A THIRD optional capability beside the two pickers, and separate from them
 * for the reason they are separate from each other: each can be absent on its
 * own. This one is absent whenever there is no Wails runtime — `make dev-web`
 * and the e2e harness — and that is a different question from whether this
 * window has a local session, which is why the port answers the session and
 * the composition root decides whether the port exists at all.
 *
 * There is no `onDrop` and cannot be one: in the Wails window the drop never
 * becomes a DOM event, so the answer arrives as a `files.dropped`
 * notification.
 */
export interface NativeDropPort {
  /** The local session a drop belongs to, READ AT CALL TIME — a latched id
   *  outlives its tab, and the backend refuses a target naming a session it
   *  does not have open. */
  session(): string | null
  /** files.dropped. Returns the unsubscribe. */
  subscribe(handler: (p: FilesDropped) => void): () => void
}
```

Add `nativeDrop?: NativeDropPort` to `ApiWorkbenchServices`, and to
`createApiWorkbenchServices`'s parameter list and returned object, in the same
`...(x ? { x } : {})` style the other optional capabilities already use
(`api-client.ts:440-443`).

- [ ] **Step 4: Forward it through the pane content**

In `frontend/src/api/api-content.ts`, beside the two pickers (`:33-62`):

```ts
  /** The native window drop, when this build has one. Held beside the
   *  pickers and never merged with them: no Wails means no drop, and that is
   *  independent of whether either picker exists. */
  private readonly nativeDrop?: NativeDropPort
```

```ts
this.nativeDrop = services.nativeDrop
```

```ts
          nativeDrop: this.nativeDrop,
```

and in `frontend/src/api/api-pane.tsx`, add to `ApiPaneProps` (`:63`):

```ts
  /**
   * The native window drop, when the build has one.
   *
   * Absent wherever there is no Wails runtime, which is every `make dev-web`
   * run and the whole e2e harness — a browser drop delivers `File` objects
   * with no location, and `api.import.postman` takes a path. So the ask draws
   * no drop surface there rather than one that highlights and delivers
   * nothing.
   */
  nativeDrop?: NativeDropPort
```

with the same `eslint-disable-next-line solid/reactivity` read the neighbours use:

```ts
// eslint-disable-next-line solid/reactivity -- injected dependency, never replaced
const nativeDrop = props.nativeDrop
```

- [ ] **Step 5: Bind it at the composition root**

In `frontend/src/main.tsx`, in the `createApiWorkbenchServices(...)` call, beside
the other capabilities:

```ts
      // The native drop, and BOTH halves of "is there one" are decided here
      // because only the composition root knows both: the Wails runtime is a
      // property of this build (wails-runtime.ts), and the open local session
      // is the pane manager's to answer. The workbench is handed the
      // capability or it is handed nothing.
      //
      // Bound off the ONE upload surface's services rather than a second
      // subscription to files.dropped — two subscribers to one notification
      // is two owners of when it has been handled.
      hasWailsWebview()
        ? {
            session: () => tm.anyLocalSession(),
            subscribe: (handler: (p: FilesDropped) => void) =>
              uploadServices.subscribeDropped(handler),
          }
        : undefined,
```

`uploadServices` is the `UploadServices` instance `uploadSurfaceFor(dispatcher)`
was built from — read `main.tsx` around the `uploadSurface` construction and use
that same object; if it is not held in a local, hold it in one rather than
constructing a second client.

- [ ] **Step 6: Wear the drop zone**

In `frontend/src/api/import-dialogs.tsx`, export the name and wrap the fields:

```tsx
/** This ask's name in `data-file-drop-target`. Exported so the element that
 *  carries it and the subscriber that filters on it read one constant — two
 *  string literals is how they drift apart. */
export const API_IMPORT_DROP_TARGET = 'api-import'
```

Add to `PostmanImportDialogProps`:

```ts
/** The local session a drop on this ask belongs to, or null — null draws no
 *  drop target, and so does a build whose composition root passed no drop
 *  capability at all. */
dropSession: string | null
```

and wrap the two `TextField`s in the dialog body:

```tsx
<DropZone
  target={API_IMPORT_DROP_TARGET}
  sessionId={props.dropSession}
  hint="Drop a Postman export here"
>
  {/* the two existing TextFields, unchanged */}
</DropZone>
```

- [ ] **Step 7: Subscribe, and refuse a multi-file drop**

In `frontend/src/api/api-pane.tsx`, after `chooseExport` is defined:

```ts
// THE DROP, answered as the same gesture the picker already answers: it
// calls `chooseExport`, so the export path and the proposed destination are
// one code path rather than two that agree until they do not.
//
// Filtered by TARGET as well as session: the local tab's terminal pane is a
// drop surface of the same session, and the session alone cannot tell the
// two apart (nocx-cx442).
if (nativeDrop) {
  onCleanup(
    nativeDrop.subscribe((p) => {
      if (p.target !== API_IMPORT_DROP_TARGET) return
      // A drop that arrives while the ask is closed belongs to nobody: the
      // target only exists while it is open, so this is a stale delivery.
      if (!untrack(importing)) return
      if (p.sources.length > 1) {
        // One import makes one collection, and N collections is N
        // destinations — a different question, and not one this ask can
        // answer by guessing which of them was meant.
        setImportRefused('Drop one export at a time — an import makes one collection.')
        return
      }
      const path = p.sources[0]?.localPath
      // No path means the drop was minted rather than described — a remote
      // tab. Nothing here can read a ticket.
      if (path === undefined || path === '') return
      setImportRefused('')
      chooseExport(path)
    }),
  )
}
```

and pass the session into the ask (`api-pane.tsx:996`):

```tsx
          dropSession={nativeDrop?.session() ?? null}
```

Import `API_IMPORT_DROP_TARGET` from `./import-dialogs`, `NativeDropPort` from
`./api-client`, and `untrack` / `onCleanup` from `solid-js` if not already
imported.

- [ ] **Step 8: Run the tests and watch them pass**

```bash
cd frontend && npx vitest run src/api/ && npx tsc --noEmit -p tsconfig.json && npm run lint
```

Expected: PASS; tsc silent; lint green.

- [ ] **Step 9: Commit**

```bash
git add frontend/src/api/import-dialogs.tsx frontend/src/api/api-client.ts \
        frontend/src/api/api-content.ts frontend/src/api/api-pane.tsx \
        frontend/src/api/api-test-fixtures.ts frontend/src/api/api-workbench.test.tsx \
        frontend/src/main.tsx
git commit -m "feat(api,ui): a dropped export fills the import ask in one gesture (<bead-id>)"
```

---

### Task 7: What the e2e can actually watch, and what it cannot

**Files:**

- Create: `e2e/api-import.spec.ts`
- Read first: `e2e/drop-gesture.ts`, `e2e/local-drop.spec.ts`

**READ THIS BEFORE WRITING THE SPEC — it is the thing most likely to be got wrong.**

`e2e/drop-gesture.ts` performs a **browser** drop: it builds a `DataTransfer` and
dispatches `DragEvent`s (`drop-gesture.ts:35`). It is NOT the Wails window drop.
The harness runs `cmd/devharness` plus vite and has no Wails at all, and the
native drop is not a DOM event — Wails hands Go the absolute paths directly, and
`SourceTicketStore.Dropped` is deliberately unreachable over JSON-RPC (R2: the
wire may never mint a source).

**So no Playwright gesture can produce a native drop, and this task must not
pretend otherwise.** Do not reach for `page.dragAndDrop`, do not add a test-only
JSON-RPC method, and do not add a seam to `devharness` to call `Dropped` — a test
hook in production code that mints sources is exactly the thing R2 forbids.

What covers the drop is three checks meeting at the contract: the Go test
(Task 2), `..._OverTheWireConformsToContract` for `files.dropped` (Task 2), and
the frontend tests (Tasks 3 and 6). **The remaining gap — nothing watches a human
perform the native gesture — is filed as a bead in this task's last step, not
left implied.** `nocx-9le.5.23` is the precedent: "the local-tab drop has no
end-to-end check, and it is the half that broke twice."

**Acceptance Criteria:**

- With no Wails runtime the ask draws NO drop target, and typing the export's
  path plus pressing Import puts the collection in the tree.
  **This criterion was FALSE when it was written and is true only because of
  nocx-vkp9d.** `api.collections.list` answers OPEN folders and
  `api.import.postman` registered nothing, so the import stopped at the disk and
  the person had to name the destination again in the panel's other ask. The
  worker writing this spec asserted disk arrival instead and said so rather than
  weakening the assertion quietly; the owner then decided the import should open
  what it wrote. Left written down because the plan asserting a behaviour the
  product did not have is exactly the shape rule 1 warns about — a criterion read
  off intent rather than off the code.
- The ask opens with `<defaultRoot>/` in the destination and Import disabled until
  it grows a last segment.
- A bead exists for the uncovered native gesture.

- [ ] **Step 1: Read the neighbours**

```bash
sed -n '1,80p' e2e/drop-gesture.ts && sed -n '1,60p' e2e/local-drop.spec.ts
```

Take the workbench-opening steps and the tree locator from whichever existing
`e2e/*.spec.ts` already opens the API workbench. Every selector used below must
already exist in another spec or be one this plan added — do not invent one.

- [ ] **Step 2: Write the spec**

`e2e/api-import.spec.ts`:

```ts
import { test, expect } from '@playwright/test'

// The harness has no Wails, which is not a limitation of this test but the
// state every contributor develops in — so this IS the dev-stand path, and
// the ask must be fully usable without a native drop.
test('the ask opens on our collections folder and imports a typed path', async ({ page }) => {
  // …open the API workbench and the import ask (see the neighbouring spec)…

  const dest = page.locator('#api-import-postman-dest')
  await expect(dest).toHaveValue(/\/collections\/$/)
  await expect(page.getByRole('button', { name: 'Import' })).toBeDisabled()

  await page.locator('#api-import-postman-file').fill(exportPath)
  await expect(dest).toHaveValue(/\/collections\/acme$/)
  await expect(page.getByRole('button', { name: 'Import' })).toBeEnabled()

  await page.getByRole('button', { name: 'Import' }).click()
  await expect(page.getByRole('treeitem', { name: /acme/ })).toBeVisible()
})

test('no Wails runtime means no drop target, not a dead one', async ({ page }) => {
  // A drop surface that highlights under a drag and then delivers nothing has
  // already promised. Absence is the capability.
  // …open the API workbench and the import ask…
  await expect(page.locator('[data-file-drop-target="api-import"]')).toHaveCount(0)
})
```

`exportPath` is a Postman v2.1 export the spec writes to the harness's own disk
before opening the ask — `e2e/local-drop.spec.ts` reads and writes the harness
disk the same way; follow it rather than inventing a fixture path.

- [ ] **Step 3: Run it in the container**

```bash
PW_PROJECTS=chromium e2e/run-in-container.sh e2e/api-import.spec.ts
```

Expected: both tests pass. The container runs Linux WebKit at a container-default
viewport and its failure set is not CI's — if a failure is layout-sensitive
rather than about the ask, confirm in CI before "fixing" the test.

- [ ] **Step 4: File the gap**

```bash
bd create "Nothing watches a person perform the native window drop" -t task -p 2 --body '## Acceptance Criteria

- An automated check exercises SourceTicketStore.Dropped through to a renderer
  acting on files.dropped, without adding a test hook to production code that
  can mint a source (R2).
- The check covers both drop surfaces of one local session: the terminal pane
  and the API import ask.

Context: the e2e harness has no Wails, and the native drop is not a DOM event —
e2e/drop-gesture.ts performs a BROWSER drop. So the native gesture is covered
only by a Go test, a contract test and a frontend test meeting at
contracts/files.dropped.schema.json, and nothing watches the two ends joined.
nocx-9le.5.23 is the precedent: the local-tab drop had no end-to-end check and
it was the half that broke twice.' --silent
```

- [ ] **Step 5: Commit**

```bash
git add e2e/api-import.spec.ts
git commit -m "test(e2e): the import ask opens on our folder and imports without a native drop (<bead-id>)"
```

---

## Task ordering

```
Task 2 (contract: target) ──→ Task 3 (terminal filters on it)

Task 1 (live local session) ─┐
Task 4 (kit DropZone) ───────┼─→ Task 6 (ask takes a drop)
Task 5 (prefill) ────────────┘

Task 5 ──→ Task 7 (e2e: what the harness can watch)
Task 6 ──→ Task 7
```

`bd dep add` edges: 3 blocked by 2; 6 blocked by 1, 4 and 5; 7 blocked by 5 and 6.

Two notes on why these edges and not others. Tasks 5 and 6 both touch `import-dialogs.tsx` and `api-pane.tsx`, so 5 blocks 6 rather than running beside it. Task 7 is NOT blocked by 3: its assertions are the dev-stand path and the absent drop target, neither of which the terminal's filter touches — 3's own assertion lives in `terminal-drop.test.ts`, where the notification can actually be produced.

Tasks 2+3 and tasks 1+4+5 are two independent fronts and can be worked in parallel.

## Deliberately not in this plan

Both are filed, not dropped:

- **`nocx-ikte5`** — a drop target that is not a tab has no address. This plan has the import ask borrow a local tab's session id, which is a symptom fix; `EmitFilesDropped` addresses by session deliberately and giving a non-session target an address is a real design question.
- **`nocx-c892h`** — folding the terminal pane's hand-rolled affordance onto `ui/drop-zone`. Task 3 gives the pane a target NAME but leaves its dragover painting where it is; the pane half is entangled with the pane lifecycle and with the DOM half of the browser drop.
- **Paste and URL as import sources** — `nocx-2cm98`, phase-2, under the OpenAPI epic `nocx-ttrlr`.
