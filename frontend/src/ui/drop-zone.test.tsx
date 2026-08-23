// @vitest-environment jsdom
import { readFileSync } from 'node:fs'
import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, fireEvent } from '@solidjs/testing-library'
import { DropZone } from './drop-zone'

const SESSION = 'a'.repeat(32)
const DROP_ZONE_CSS = readFileSync('src/styles/components/drop-zone.css', 'utf8')

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

  // ── The affordance is PERMANENT ─────────────────────────────────────────
  //
  // It used to be drawn on dragover only, and the ask then looked exactly as
  // it had before the drop existed: the owner opened the finished thing in
  // the Wails window and said the import had not changed (nocx-9hb5g). A
  // gesture nobody can discover is a gesture nobody performs.
  it('draws the region at rest — icon, gesture and picker, with no drag', () => {
    const onPick = vi.fn()
    const { container } = render(() => (
      <DropZone
        target="api-import"
        sessionId={SESSION}
        hint="Drop an export here"
        pickLabel="Or select a file"
        onPick={onPick}
      >
        <input data-testid="child" />
      </DropZone>
    ))
    const region = container.querySelector<HTMLElement>('.ui-drop-zone__region')
    expect(region).not.toBeNull()
    expect(region!.querySelector('.ui-drop-zone__icon svg')).not.toBeNull()
    expect(region!.querySelector('.ui-drop-zone__hint')?.textContent).toBe('Drop an export here')
    expect(region!.querySelector('button')?.textContent).toBe('Or select a file')
    // No drag has happened, and the region is on screen anyway.
    expect(
      container.querySelector<HTMLElement>('.ui-drop-zone')!.dataset.dropActive,
    ).toBeUndefined()
  })

  it('draws the region FIRST, above whatever it wraps', () => {
    // The ask's two fields are its children; the region is the first thing in
    // the body, not a footnote under the form.
    const { container } = render(() => (
      <DropZone
        target="api-import"
        sessionId={SESSION}
        hint="Drop an export here"
        pickLabel="Or select a file"
        onPick={vi.fn()}
      >
        <input data-testid="child" />
      </DropZone>
    ))
    const zone = container.querySelector<HTMLElement>('.ui-drop-zone')!
    expect(zone.firstElementChild?.className).toBe('ui-drop-zone__region')
  })

  it('draws no picker control when the caller has no picker to offer', () => {
    // `dialog.openFile` is Wails and can be missing on its own — the drop and
    // the picker are two capabilities, and the region draws the half it has.
    const { container } = render(() => (
      <DropZone target="api-import" sessionId={SESSION} hint="Drop an export here">
        <input data-testid="child" />
      </DropZone>
    ))
    expect(container.querySelector('.ui-drop-zone__region')).not.toBeNull()
    expect(container.querySelector('.ui-drop-zone__region button')).toBeNull()
  })

  it('reaches the caller"s picker when its control is activated', () => {
    const onPick = vi.fn()
    const { container } = render(() => (
      <DropZone
        target="api-import"
        sessionId={SESSION}
        hint="Drop an export here"
        pickLabel="Or select a file"
        onPick={onPick}
      >
        <input data-testid="child" />
      </DropZone>
    ))
    fireEvent.click(container.querySelector('.ui-drop-zone__region button')!)
    expect(onPick).toHaveBeenCalledTimes(1)
  })

  it('declares NO drop target without a session, and still renders its children', () => {
    // A target naming no session is refused by the backend, so advertising
    // one is advertising a gesture that cannot work. Absence is the
    // capability — the same rule the dialog pickers already follow.
    const { container } = render(() => (
      <DropZone
        target="api-import"
        sessionId={null}
        hint="Drop an export here"
        pickLabel="Or select a file"
        onPick={vi.fn()}
      >
        <input data-testid="child" />
      </DropZone>
    ))
    const zone = container.querySelector<HTMLElement>('.ui-drop-zone')!
    expect(zone.hasAttribute('data-file-drop-target')).toBe(false)
    expect(zone.hasAttribute('data-session-id')).toBe(false)
    // Nothing of the region, not even its icon or its picker: where the drop
    // cannot arrive the affordance is not drawn at all.
    expect(zone.querySelector('.ui-drop-zone__region')).toBeNull()
    expect(zone.querySelector('.ui-drop-zone__icon')).toBeNull()
    expect(zone.querySelector('button')).toBeNull()
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

  // jsdom loads no CSS, so the attribute above is only half the claim: a
  // stylesheet that did not distinguish the two states would leave the region
  // looking identical under a drag and the test would still be green.
  it('paints the drag state differently from rest — the stylesheet says so', () => {
    expect(DROP_ZONE_CSS).toMatch(/\.ui-drop-zone__region\s*\{/)
    expect(DROP_ZONE_CSS).toMatch(/\[data-drop-active\][^{]*\.ui-drop-zone__region\s*\{/)
  })

  // The wrapper is in the DOM whether or not the capability is: `display:
  // contents` is what keeps it from becoming an empty gap between the
  // dialog's body and its fields where no region is drawn.
  it('takes no space of its own where it draws no region — the stylesheet says so', () => {
    expect(DROP_ZONE_CSS).toMatch(/\.ui-drop-zone\s*\{[^}]*display:\s*contents/)
    // Keyed on `data-drop-live` — the attribute that says a region is drawn
    // — rather than on the NATIVE target, which a browser drop never names.
    expect(DROP_ZONE_CSS).toMatch(/\.ui-drop-zone\[data-drop-live\]\s*\{[^}]*display:\s*flex/)
  })
})

// ── The browser half ──────────────────────────────────────────────────────
//
// nocx-1gfbw. The region was drawn only where a NATIVE drop could arrive —
// a Wails runtime and an open local session — and the owner opened the ask
// at localhost:5180 (`make dev-web`) to find no drop region at all. A
// browser drop carries `File` objects with BYTES, which reach any backend,
// so the browser is not the degraded case: it is the general one (spec §1a).
//
// The two halves never both act on one gesture. Inside the webview the drop
// has already gone to Go, which is describing it for `files.dropped`, so
// acting on the DOM event too would answer the same gesture twice —
// terminal-drop.ts states the rule. `native` is how the caller says it is in
// such a window: the kit may not import `hasWailsWebview()` (the dependency
// direction rule), and the composition root has already read it once when it
// decided whether there is a native drop capability at all.

/** A DataTransfer jsdom does not have, carrying the files a browser drop
 *  delivers. */
function filesTransfer(files: File[]): DataTransfer {
  return { types: ['Files'], files } as unknown as DataTransfer
}

function drop(el: HTMLElement, transfer: DataTransfer): void {
  const e = new Event('drop', { bubbles: true, cancelable: true }) as DragEvent
  Object.defineProperty(e, 'dataTransfer', { value: transfer })
  el.dispatchEvent(e)
}

function exportFile(name: string): File {
  return new File(['{"info":{}}'], name, { type: 'application/json' })
}

describe('DropZone, the browser half', () => {
  afterEach(() => document.body.replaceChildren())

  it('draws its region with NO session and NO Wails, when the caller takes files', () => {
    // The case that was missing entirely: `make dev-web` has neither, and it
    // is where every contributor and every browser user is.
    const { container } = render(() => (
      <DropZone target="api-import" sessionId={null} hint="Drop an export here" onFiles={vi.fn()}>
        <input data-testid="child" />
      </DropZone>
    ))
    const zone = container.querySelector<HTMLElement>('.ui-drop-zone')!
    expect(zone.querySelector('.ui-drop-zone__region')).not.toBeNull()
    expect(zone.querySelector('.ui-drop-zone__hint')?.textContent).toBe('Drop an export here')
    // And it claims NO native target: those two attributes are what Wails
    // reads off the dropped-on element, and there is no Wails here.
    expect(zone.hasAttribute('data-file-drop-target')).toBe(false)
    expect(zone.hasAttribute('data-session-id')).toBe(false)
    expect(container.querySelector('[data-testid="child"]')).not.toBeNull()
  })

  it('hands the caller the file a DOM drop carried', () => {
    const onFiles = vi.fn()
    const { container } = render(() => (
      <DropZone target="api-import" sessionId={null} hint="Drop an export here" onFiles={onFiles}>
        <input data-testid="child" />
      </DropZone>
    ))
    const zone = container.querySelector<HTMLElement>('.ui-drop-zone')!
    const file = exportFile('acme.postman_collection.json')
    drop(zone, filesTransfer([file]))
    expect(onFiles).toHaveBeenCalledTimes(1)
    expect(onFiles).toHaveBeenCalledWith([file])
  })

  it('hands over EVERY file of a multi-file drop — the caller owns the refusal', () => {
    // One import makes one collection, and the sentence that says so is the
    // caller's: the native half already refuses several in exactly one
    // place, and a second refusal here would be a second owner of one rule.
    const onFiles = vi.fn()
    const { container } = render(() => (
      <DropZone target="api-import" sessionId={null} hint="Drop an export here" onFiles={onFiles}>
        <input data-testid="child" />
      </DropZone>
    ))
    const a = exportFile('a.json')
    const b = exportFile('b.json')
    drop(container.querySelector<HTMLElement>('.ui-drop-zone')!, filesTransfer([a, b]))
    expect(onFiles).toHaveBeenCalledWith([a, b])
  })

  it('IGNORES the DOM drop in a native window — Go already has that gesture', () => {
    // Acting on both would answer one drop twice: once as a path through
    // `files.dropped` and once as bytes through here.
    const onFiles = vi.fn()
    const { container } = render(() => (
      <DropZone
        target="api-import"
        sessionId={SESSION}
        hint="Drop an export here"
        native
        onFiles={onFiles}
      >
        <input data-testid="child" />
      </DropZone>
    ))
    drop(
      container.querySelector<HTMLElement>('.ui-drop-zone')!,
      filesTransfer([exportFile('acme.json')]),
    )
    expect(onFiles).not.toHaveBeenCalled()
  })

  it('draws no region and keeps its children where NEITHER half can answer', () => {
    // No session (no native drop to route) and no `onFiles` (the caller
    // cannot take bytes): the affordance would promise a gesture nothing
    // could deliver.
    const { container } = render(() => (
      <DropZone target="api-import" sessionId={null} hint="Drop an export here">
        <input data-testid="child" />
      </DropZone>
    ))
    const zone = container.querySelector<HTMLElement>('.ui-drop-zone')!
    expect(zone.querySelector('.ui-drop-zone__region')).toBeNull()
    expect(zone.hasAttribute('data-file-drop-target')).toBe(false)
    expect(container.querySelector('[data-testid="child"]')).not.toBeNull()
  })

  it('draws no region in a native window with no session', () => {
    // `onFiles` is not a capability there: the DOM drop is ignored where the
    // window hands drops to the backend, so a region drawn on it would
    // advertise nothing.
    const { container } = render(() => (
      <DropZone
        target="api-import"
        sessionId={null}
        hint="Drop an export here"
        native
        onFiles={vi.fn()}
      >
        <input data-testid="child" />
      </DropZone>
    ))
    expect(container.querySelector('.ui-drop-zone__region')).toBeNull()
  })

  it('offers the kit file input as its picker where there is no native one', () => {
    // The other way to answer the same question, in the currency this build
    // has: `dialog.openFile` yields a PATH and exists only in the Wails
    // window; the kit's own input yields the FILE, which is what a browser
    // can give and what any backend can read.
    const onFiles = vi.fn()
    const { container } = render(() => (
      <DropZone target="api-import" sessionId={null} hint="Drop an export here" onFiles={onFiles}>
        <input data-testid="child" />
      </DropZone>
    ))
    const input = container.querySelector<HTMLInputElement>('.ui-file-input__native')
    expect(input).not.toBeNull()

    const file = exportFile('acme.postman_collection.json')
    Object.defineProperty(input!, 'files', { value: [file] })
    fireEvent.change(input!)
    // THE SAME CALLBACK the drop reaches: one derivation of "here is the
    // export", so the two controls cannot come to differ.
    expect(onFiles).toHaveBeenCalledWith([file])
  })

  it('prefers the caller"s native picker over the kit input when it has one', () => {
    // Two pickers would be two controls for one question. `onPick` is the
    // Wails picker and wins where it exists.
    const onPick = vi.fn()
    const { container } = render(() => (
      <DropZone
        target="api-import"
        sessionId={SESSION}
        hint="Drop an export here"
        pickLabel="Or select a file"
        native
        onPick={onPick}
        onFiles={vi.fn()}
      >
        <input data-testid="child" />
      </DropZone>
    ))
    expect(container.querySelector('.ui-file-input')).toBeNull()
    fireEvent.click(container.querySelector('.ui-drop-zone__region button')!)
    expect(onPick).toHaveBeenCalledTimes(1)
  })
})
