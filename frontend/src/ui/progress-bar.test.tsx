// @vitest-environment jsdom
import { cleanup, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it } from 'vitest'
import { ProgressBar } from './progress-bar'

afterEach(cleanup)

function bar(container: HTMLElement): HTMLElement {
  const el = container.querySelector<HTMLElement>('.ui-progress-bar')
  if (!el) throw new Error('no progress bar')
  return el
}

function fill(container: HTMLElement): HTMLElement {
  const el = container.querySelector<HTMLElement>('.ui-progress-bar__fill')
  if (!el) throw new Error('no fill')
  return el
}

describe('ProgressBar', () => {
  it('announces the fraction as a percentage with the caller’s name', () => {
    const { container } = render(() => <ProgressBar value={0.42} ariaLabel="Uploading notes.txt" />)
    expect(bar(container).getAttribute('role')).toBe('progressbar')
    expect(bar(container).getAttribute('aria-label')).toBe('Uploading notes.txt')
    expect(bar(container).getAttribute('aria-valuemin')).toBe('0')
    expect(bar(container).getAttribute('aria-valuemax')).toBe('100')
    expect(bar(container).getAttribute('aria-valuenow')).toBe('42')
    expect(fill(container).style.width).toBe('42%')
  })

  it('follows its value — the read is reactive, not a first-render snapshot', () => {
    const [v, setV] = createSignal(0)
    const { container } = render(() => <ProgressBar value={v()} ariaLabel="Uploading" />)
    expect(fill(container).style.width).toBe('0%')
    setV(0.75)
    expect(fill(container).style.width).toBe('75%')
    expect(bar(container).getAttribute('aria-valuenow')).toBe('75')
  })

  it('clamps rather than refusing a value outside the range', () => {
    // A byte count that briefly overshoots its declared total is a
    // measurement, not a reason to stop drawing; a bar 140% wide would run
    // out of its own track. A negative one has the same shape.
    const over = render(() => <ProgressBar value={1.4} ariaLabel="over" />)
    expect(fill(over.container).style.width).toBe('100%')
    expect(bar(over.container).getAttribute('aria-valuenow')).toBe('100')
    cleanup()

    const under = render(() => <ProgressBar value={-3} ariaLabel="under" />)
    expect(fill(under.container).style.width).toBe('0%')
  })

  it('reads a non-number as no progress rather than as an empty bar attribute', () => {
    // 0/0 is NaN, and the aggregate over operations whose sizes are all
    // zero produces exactly that. `width: NaN%` is an invalid declaration
    // the engine drops, which leaves the LAST width painted — a bar frozen
    // at whatever it happened to be.
    const { container } = render(() => <ProgressBar value={Number.NaN} ariaLabel="nan" />)
    expect(fill(container).style.width).toBe('0%')
    expect(bar(container).getAttribute('aria-valuenow')).toBe('0')
  })

  it('takes a size, and defaults to the quiet one', () => {
    // The variance exists because a 3 px bar between a title and a line of
    // numbers reads as a rule separating them rather than as the row's main
    // fact (nocx-hbdw4.5). The DEFAULT is unchanged, which is what makes it
    // safe for the callers that predate it.
    const { container, unmount } = render(() => <ProgressBar value={0.5} ariaLabel="x" />)
    expect(container.querySelector('.ui-progress-bar')?.getAttribute('data-size')).toBe('sm')
    unmount()

    const wide = render(() => <ProgressBar value={0.5} ariaLabel="x" size="md" />)
    expect(wide.container.querySelector('.ui-progress-bar')?.getAttribute('data-size')).toBe('md')
  })
})
