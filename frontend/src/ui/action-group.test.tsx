// @vitest-environment jsdom
/**
 * ActionGroup tests — the kit's "these actions belong together" (nocx-gycwo).
 */
import { describe, it, expect, afterEach } from 'vitest'
import { cleanup, render } from '@solidjs/testing-library'
import { ActionGroup } from './action-group'
import { Button } from './button'

describe('ActionGroup', () => {
  afterEach(cleanup)

  it('is a named group carrying the kit identity', () => {
    const { container } = render(() => (
      <ActionGroup ariaLabel="Allow this action">
        <Button onClick={() => {}}>Allow once</Button>
      </ActionGroup>
    ))
    const group = container.querySelector('.ui-action-group')!
    expect(group.getAttribute('role')).toBe('group')
    expect(group.getAttribute('aria-label')).toBe('Allow this action')
  })

  it('does not draw the label — the name is for the accessibility tree only', () => {
    const { container } = render(() => (
      <ActionGroup ariaLabel="Allow this action">
        <Button onClick={() => {}}>Allow once</Button>
      </ActionGroup>
    ))
    expect(container.textContent).toBe('Allow once')
  })

  it('keeps its children in order, each its own tab stop', () => {
    const { container } = render(() => (
      <ActionGroup ariaLabel="Allow this action">
        <Button onClick={() => {}}>Allow once</Button>
        <Button onClick={() => {}}>Allow always</Button>
      </ActionGroup>
    ))
    const buttons = Array.from(container.querySelectorAll('.ui-button'))
    expect(buttons.map((b) => b.textContent)).toEqual(['Allow once', 'Allow always'])
    // A `toolbar` would make these one tab stop; peer decisions are not that.
    for (const b of buttons) expect(b.getAttribute('tabindex')).toBeNull()
  })
})
