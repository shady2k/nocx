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
