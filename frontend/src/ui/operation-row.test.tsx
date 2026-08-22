// @vitest-environment jsdom
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { OperationRow } from './operation-row'
import type { OperationPhase } from './operation'

afterEach(cleanup)

function row(container: HTMLElement): HTMLElement {
  const el = container.querySelector<HTMLElement>('.ui-operation-row')
  if (!el) throw new Error('no operation row')
  return el
}

const RUNNING = {
  kind: 'upload' as const,
  title: 'notes.txt',
  destination: '/srv/data',
  phase: 'running' as OperationPhase,
  done: 1_000_000,
  total: 4_000_000,
  speedBytesPerSecond: 500_000,
}

describe('OperationRow while the work is live', () => {
  it('says what it is, where it is going, how far and how fast', () => {
    const { container } = render(() => <OperationRow {...RUNNING} />)
    expect(row(container).textContent).toContain('notes.txt')
    expect(row(container).textContent).toContain('/srv/data')
    expect(container.querySelector('.ui-operation-row__detail')?.textContent).toBe(
      '1.0 MB of 4.0 MB · 500.0 kB/s',
    )
  })

  it('draws a determinate bar at the fraction actually done', () => {
    const { container } = render(() => <OperationRow {...RUNNING} />)
    const bar = container.querySelector('[role="progressbar"]')
    expect(bar?.getAttribute('aria-valuenow')).toBe('25')
    // Never a spinner: a 20-minute transfer must not put permanent motion
    // in somebody's peripheral vision.
    expect(container.querySelector('.ui-spinner')).toBeNull()
  })

  it('names the size alone while nothing has been observed', () => {
    // `done: null` is the ABSENCE of a measurement, not zero — "0 B of
    // 400 MB" claims nothing has happened, and the truth is that nothing
    // has been seen.
    const { container } = render(() => (
      <OperationRow {...RUNNING} done={null} speedBytesPerSecond={null} />
    ))
    expect(container.querySelector('.ui-operation-row__detail')?.textContent).toBe('4.0 MB')
    expect(container.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('0')
  })

  it('draws no bar of a size that cannot be divided', () => {
    // An empty file is a file, and 0/0 must not paint a full bar or a
    // frozen one.
    const { container } = render(() => <OperationRow {...RUNNING} done={0} total={0} />)
    expect(container.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('0')
  })

  it('offers the cancel the caller supplied, and calls it', () => {
    const onCancel = vi.fn()
    const view = render(() => <OperationRow {...RUNNING} onCancel={onCancel} />)
    fireEvent.click(view.getByRole('button', { name: 'Cancel' }))
    expect(onCancel).toHaveBeenCalledTimes(1)
  })

  it('offers no cancel when the caller gave none', () => {
    // Whether stopping still means anything is the model's judgement about
    // that operation, never a rendering rule this component derives.
    const view = render(() => <OperationRow {...RUNNING} onCancel={undefined} />)
    expect(view.queryByRole('button', { name: 'Cancel' })).toBeNull()
  })

  it('follows its props — the reads are reactive, not a first render', () => {
    const [done, setDone] = createSignal<number | null>(null)
    const { container } = render(() => <OperationRow {...RUNNING} done={done()} />)
    expect(container.querySelector('.ui-operation-row__detail')?.textContent).toContain('4.0 MB')
    setDone(2_000_000)
    expect(container.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe(
      '50',
    )
  })
})

describe('OperationRow on the unsettled phase', () => {
  it('keeps the cancel and says we are waiting, never that it failed', () => {
    // The renderer lost sight of the work; the backend may be finishing it
    // this moment and cancelling still reaches it. A row that said "failed"
    // would announce it dead and take away the only control that stops it.
    const onCancel = vi.fn()
    const view = render(() => (
      <OperationRow
        {...RUNNING}
        phase="unsettled"
        error="the connection dropped"
        onCancel={onCancel}
      />
    ))
    expect(row(view.container).textContent).toContain('Waiting for the server')
    expect(row(view.container).textContent).not.toContain('Failed')
    expect(view.queryByRole('button', { name: 'Cancel' })).not.toBeNull()
    expect(view.container.querySelector('[role="progressbar"]')).not.toBeNull()
    // The reason is hover detail, not a sentence in the row.
    expect(view.container.querySelector('.ui-badge')?.getAttribute('title')).toBe(
      'the connection dropped',
    )
  })
})

describe('OperationRow owns the outcome vocabulary', () => {
  // A surface passes the wire's phase and never a label or a tone — the
  // same rule FileStatusRow follows for the git status letters. All four
  // terminal phases, because a phase nothing renders is a phase that
  // renders as nothing.
  const cases: Array<{ phase: OperationPhase; label: string; tone: string }> = [
    { phase: 'written', label: 'Done', tone: 'success' },
    { phase: 'skipped', label: 'Skipped', tone: 'neutral' },
    { phase: 'cancelled', label: 'Cancelled', tone: 'neutral' },
    { phase: 'failed', label: 'Failed', tone: 'danger' },
  ]

  for (const c of cases) {
    it(`renders ${c.phase} as "${c.label}" in the ${c.tone} tone`, () => {
      const { container } = render(() => <OperationRow {...RUNNING} phase={c.phase} />)
      const badge = container.querySelector('.ui-badge')
      expect(badge?.textContent).toBe(c.label)
      expect(badge?.getAttribute('data-tone')).toBe(c.tone)
      // Finished: no bar, no numbers, nothing still moving.
      expect(container.querySelector('[role="progressbar"]')).toBeNull()
    })
  }

  it('reads a cancellation as neutral and never as danger', () => {
    // A cancelled transfer's underlying error is a context cancellation,
    // and a person who pressed cancel has not had something go wrong.
    const { container } = render(() => <OperationRow {...RUNNING} phase="cancelled" />)
    expect(container.querySelector('.ui-badge')?.getAttribute('data-tone')).not.toBe('danger')
  })

  it('shows a failure’s reason under its name', () => {
    const { container } = render(() => (
      <OperationRow {...RUNNING} phase="failed" error="permission denied" />
    ))
    expect(container.querySelector('.ui-operation-row__detail')?.textContent).toBe(
      'permission denied',
    )
  })

  it('carries the phase on the row, so a check can read it without reading words', () => {
    const { container } = render(() => <OperationRow {...RUNNING} phase="written" />)
    expect(row(container).getAttribute('data-phase')).toBe('written')
  })
})
