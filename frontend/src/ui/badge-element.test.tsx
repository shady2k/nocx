// @vitest-environment jsdom
import { cleanup, render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it } from 'vitest'
import { assertSameShape } from '../test-support/element-shape'
import { Badge, type BadgeProps } from './badge'
import { createBadge, type BadgeElementOptions } from './badge-element'

afterEach(cleanup)

/** Every variance Badge has. A variance added to badge.tsx and not here is
 *  a parity hole — add the row. */
const CASES: Array<[string, BadgeElementOptions, BadgeProps]> = [
  ['default', { text: 'agent' }, { children: 'agent' }],
  ['info', { text: 'agent', tone: 'info' }, { children: 'agent', tone: 'info' }],
  ['success', { text: 'ok', tone: 'success' }, { children: 'ok', tone: 'success' }],
  ['warning', { text: 'w', tone: 'warning' }, { children: 'w', tone: 'warning' }],
  ['danger', { text: 'd', tone: 'danger' }, { children: 'd', tone: 'danger' }],
  ['solid', { text: '3', variant: 'solid' }, { children: '3', variant: 'solid' }],
  ['truncate', { text: 'long', truncate: true }, { children: 'long', truncate: true }],
  ['title', { text: 'x', title: 'why' }, { children: 'x', title: 'why' }],
  ['testid', { text: 'x', testId: 't' }, { children: 'x', 'data-testid': 't' }],
]

function solid(props: BadgeProps): Element {
  const { container } = render(() => <Badge {...props} />)
  return container.firstElementChild!
}

describe('createBadge is the same element Badge renders', () => {
  it.each(CASES)('%s', (_name, vanilla, props) => {
    assertSameShape(createBadge(vanilla), solid(props))
  })

  it('the comparison fails when the two emitters disagree', () => {
    expect(() =>
      assertSameShape(
        createBadge({ text: 'x', tone: 'info' }),
        solid({ children: 'x', tone: 'warning' }),
      ),
    ).toThrow(/emitters disagree/)
  })
})
