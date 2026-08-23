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
    expect(DROP_ZONE_CSS).toMatch(
      /\.ui-drop-zone\[data-file-drop-target\]\s*\{[^}]*display:\s*flex/,
    )
  })
})
