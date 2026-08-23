// @vitest-environment jsdom
//
// SegmentedControl's per-option `disabled` (nocx-708q.2) — the variance the
// Files panel's Names/Contents toggle needed, added to the kit rather than
// forked into the surface.
//
// It is a different thing from the group's own `disabled`, and the
// difference is the whole reason it exists: the group's says the choice is
// unavailable, this one says the choice is real and one of its answers is
// not built yet. That shape has to be renderable, or a panel showing a
// promised mode has only two options — hide it, and present half the
// intention as the whole of it, or offer it live, and ship a control that
// does nothing.
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it } from 'vitest'
import { createSignal } from 'solid-js'

import { SegmentedControl } from './segmented-control'

afterEach(cleanup)

function mount(options: Parameters<typeof SegmentedControl>[0]['options'], initial = 'a') {
  const [value, setValue] = createSignal(initial)
  const chosen: string[] = []
  render(() => (
    <SegmentedControl
      options={options}
      value={value()}
      onChange={(v) => {
        chosen.push(v)
        setValue(v)
      }}
      ariaLabel="Test group"
    />
  ))
  const segments = () => [
    ...document.querySelectorAll<HTMLButtonElement>('.ui-segmented-control__option'),
  ]
  return { segments, chosen, value }
}

const AB_WITH_B_DISABLED = [
  { value: 'a', label: 'A' },
  { value: 'b', label: 'B', disabled: true, title: 'B is not built yet' },
]

describe('a single disabled segment', () => {
  it('is disabled while the rest of the group stays live', () => {
    const c = mount(AB_WITH_B_DISABLED)
    expect(c.segments()[0].disabled).toBe(false)
    expect(c.segments()[1].disabled).toBe(true)
  })

  it('carries its reason on the title, so it is not a dead control', () => {
    const c = mount(AB_WITH_B_DISABLED)
    expect(c.segments()[1].title).toBe('B is not built yet')
  })

  it('cannot be chosen by clicking', () => {
    const c = mount(AB_WITH_B_DISABLED)
    c.segments()[1].click()
    expect(c.chosen).toEqual([])
    expect(c.value()).toBe('a')
  })

  it('is SKIPPED by the arrow keys rather than being landed on', () => {
    // A roving selection that lands on a value the control will not accept
    // is a keyboard user hitting the same wall with no way to tell why.
    const c = mount([
      { value: 'a', label: 'A' },
      { value: 'b', label: 'B', disabled: true, title: 'why' },
      { value: 'c', label: 'C' },
    ])
    fireEvent.keyDown(c.segments()[0], { key: 'ArrowRight' })
    expect(c.value()).toBe('c')
    fireEvent.keyDown(c.segments()[0], { key: 'ArrowRight' })
    expect(c.value()).toBe('a')
  })

  it('gives the tab stop to the first SELECTABLE segment when nothing is chosen', () => {
    // Otherwise the group's one tab stop is a button Tab cannot focus, and
    // the whole control drops out of the keyboard order.
    const c = mount(
      [
        { value: 'a', label: 'A', disabled: true, title: 'why' },
        { value: 'b', label: 'B' },
      ],
      'nothing-selected',
    )
    expect(c.segments()[0].tabIndex).toBe(-1)
    expect(c.segments()[1].tabIndex).toBe(0)
  })

  it('an arrow with every segment disabled does nothing at all', () => {
    const c = mount(
      [
        { value: 'a', label: 'A', disabled: true, title: 'why' },
        { value: 'b', label: 'B', disabled: true, title: 'why' },
      ],
      'nothing-selected',
    )
    fireEvent.keyDown(c.segments()[0], { key: 'ArrowRight' })
    expect(c.chosen).toEqual([])
  })

  it('the group`s own disabled still disables every segment', () => {
    // The two are independent, and widening one must not have narrowed the
    // other.
    render(() => (
      <SegmentedControl
        options={[
          { value: 'a', label: 'A' },
          { value: 'b', label: 'B' },
        ]}
        value="a"
        onChange={() => {}}
        disabled
      />
    ))
    const segments = [
      ...document.querySelectorAll<HTMLButtonElement>('.ui-segmented-control__option'),
    ]
    expect(segments.every((s) => s.disabled)).toBe(true)
  })
})
