// @vitest-environment jsdom
import { cleanup, render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it } from 'vitest'

import { ConnectionIndicator } from './connection-indicator'

const mark = (c: HTMLElement) => c.querySelector('.ui-connection-indicator')

afterEach(() => cleanup())

describe('ConnectionIndicator', () => {
  // The healthy state is ABSENT, not green. A mark that is always on is a mark
  // nobody reads, and it would sit over the user's terminal forever to say
  // nothing.
  it('renders nothing while the host is reachable', () => {
    const { container } = render(() => <ConnectionIndicator condition="reachable" />)
    expect(mark(container)).toBeNull()
  })

  it('states the condition in words, not only in colour', () => {
    const { container } = render(() => <ConnectionIndicator condition="unreachable" />)
    const el = mark(container)
    expect(el).not.toBeNull()
    expect(el?.getAttribute('data-condition')).toBe('unreachable')
    // The sentence is the accessible name AND the tooltip: a coloured glyph
    // alone is unreadable to a screen reader and ambiguous to everyone else.
    expect(el?.getAttribute('aria-label')).toMatch(/stopped answering/i)
    expect(el?.getAttribute('title')).toBe(el?.getAttribute('aria-label'))
  })

  // A slow host is not a lost one, and the two must not read alike: one is a
  // warning about the link, the other says work is gone.
  it('distinguishes slow from lost', () => {
    const slow = render(() => <ConnectionIndicator condition="slow" />)
    const lost = render(() => <ConnectionIndicator condition="lost" />)
    expect(mark(slow.container)?.getAttribute('aria-label')).toMatch(/slowly/i)
    expect(mark(lost.container)?.getAttribute('aria-label')).toMatch(/gone/i)
    expect(mark(slow.container)?.getAttribute('aria-label')).not.toBe(
      mark(lost.container)?.getAttribute('aria-label'),
    )
  })

  // The evidence rides the tooltip, never the screen: a number that changes
  // every thirty seconds in the corner of the eye is noise.
  it('carries the measurement in the tooltip and not as visible text', () => {
    const { container } = render(() => (
      <ConnectionIndicator condition="slow" roundTripMs={870} observedAgo="4 s ago" />
    ))
    const el = mark(container)
    expect(el?.getAttribute('title')).toContain('870 ms')
    expect(el?.getAttribute('title')).toContain('4 s ago')
    expect(el?.textContent ?? '').not.toContain('870')
  })

  // Absent and zero are the same statement — "nothing measured one" — and a
  // probe that never answered must not be rendered as an instant reply.
  it('omits a measurement it does not have', () => {
    const none = render(() => <ConnectionIndicator condition="unreachable" />)
    const zero = render(() => <ConnectionIndicator condition="unreachable" roundTripMs={0} />)
    expect(mark(none.container)?.getAttribute('title')).not.toMatch(/\bms\b/)
    expect(mark(zero.container)?.getAttribute('title')).not.toMatch(/\bms\b/)
  })
})
