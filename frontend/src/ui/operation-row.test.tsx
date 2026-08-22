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

// ── A file that has not started (nocx-hbdw4.6) ──────────────────────────
//
// A batch sends one file at a time per binding (design §4), so every file
// after the first is waiting its turn. Drop five and four of them are in
// this state — and the row has to say so without claiming any byte has
// moved, because a panel of bars at zero reads "five things are stalled"
// where the truth is "four are waiting".
describe('OperationRow before the work has started', () => {
  const QUEUED = { ...RUNNING, phase: 'queued' as OperationPhase, done: null, total: 4_000_000 }

  it('draws no progress bar at all', () => {
    const { container } = render(() => <OperationRow {...QUEUED} />)
    expect(container.querySelector('[role="progressbar"]')).toBeNull()
  })

  it('draws no percentage either — zero would be a measurement nobody took', () => {
    const { container } = render(() => <OperationRow {...QUEUED} />)
    expect(container.querySelector('.ui-operation-row__percent')).toBeNull()
    expect(row(container).textContent).not.toContain('0%')
  })

  it('says it is queued, on the row itself and not only in whatever list holds it', () => {
    // The heading a surface groups it under scrolls away and the row does
    // not, and a queued row has no summary line to identify it by either —
    // see the component's module doc for why this badge stays where
    // `written`'s was removed.
    const { container } = render(() => <OperationRow {...QUEUED} />)
    expect(row(container).textContent).toContain('Queued')
  })

  it('says what it is going to send', () => {
    const { container } = render(() => <OperationRow {...QUEUED} />)
    expect(container.querySelector('.ui-operation-row__progress')?.textContent).toBe('4.0 MB')
  })

  it('does not report itself as finished', () => {
    // It fell through into the finished branch while the split was still
    // two-way, which drew the "size · when · how long" summary for work
    // that had not started.
    const { container } = render(() => <OperationRow {...QUEUED} endedAt={null} />)
    expect(container.querySelector('.ui-operation-row__summary')).toBeNull()
    expect(row(container).textContent).not.toContain('Done')
  })

  it('keeps its cancel — a file can be taken out of the batch before its turn', () => {
    const onCancel = vi.fn()
    const view = render(() => <OperationRow {...QUEUED} onCancel={onCancel} />)
    fireEvent.click(view.getByRole('button', { name: 'Cancel notes.txt' }))
    expect(onCancel).toHaveBeenCalledTimes(1)
  })
})

describe('OperationRow while the work is live', () => {
  it('says what it is, where it is going, how far and how fast', () => {
    const { container } = render(() => <OperationRow {...RUNNING} />)
    expect(row(container).textContent).toContain('notes.txt')
    expect(row(container).textContent).toContain('/srv/data')
    expect(container.querySelector('.ui-operation-row__progress')?.textContent).toBe(
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
    expect(container.querySelector('.ui-operation-row__progress')?.textContent).toBe('4.0 MB')
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
    fireEvent.click(view.getByRole('button', { name: /^Cancel / }))
    expect(onCancel).toHaveBeenCalledTimes(1)
  })

  it('offers no cancel when the caller gave none', () => {
    // Whether stopping still means anything is the model's judgement about
    // that operation, never a rendering rule this component derives.
    const view = render(() => <OperationRow {...RUNNING} onCancel={undefined} />)
    expect(view.queryByRole('button', { name: /^Cancel / })).toBeNull()
  })

  it('follows its props — the reads are reactive, not a first render', () => {
    const [done, setDone] = createSignal<number | null>(null)
    const { container } = render(() => <OperationRow {...RUNNING} done={done()} />)
    expect(container.querySelector('.ui-operation-row__progress')?.textContent).toContain('4.0 MB')
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
    expect(view.queryByRole('button', { name: /^Cancel / })).not.toBeNull()
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
  // `written` is NOT here, and its absence is the rule: the list groups
  // finished work under its own heading, so the expected outcome needs no
  // pill repeated down every row (nocx-hbdw4.5). Its own test is below.
  const cases: Array<{ phase: OperationPhase; label: string; tone: string }> = [
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
      // Finished: no bar and no rate — nothing is still moving. What it
      // DOES carry is the finished summary, which is a different sentence.
      expect(container.querySelector('[role="progressbar"]')).toBeNull()
    })
  }

  it('marks nothing on a plain success, because the heading already said it', () => {
    // The one outcome that is not news. Everything else differs from the
    // expected end and keeps its mark; a badge on every row is also a badge
    // nobody reads.
    const { container } = render(() => <OperationRow {...RUNNING} phase="written" />)
    expect(container.querySelector('.ui-badge')).toBeNull()
    expect(container.textContent).not.toContain('Done')
    // Still finished, and still says so the ways that carry information.
    expect(container.querySelector('.ui-operation-row')?.getAttribute('data-phase')).toBe('written')
    expect(container.querySelector('[role="progressbar"]')).toBeNull()
  })

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

// ── The row's grammar in a rail-width panel (nocx-hbdw4.1) ───────────────
//
// The row shipped into a surface a few hundred pixels wide and every field
// in it ellipsised — the name, the path, and the status badge, which read
// "D…". A badge that cannot fit its own word is worse than no badge, and a
// name that yields room to a path nobody can read to the end is the row
// failing at the one thing it exists for. What a jsdom test can assert is
// the STRUCTURE the widths then follow from; the paint itself is measured in
// e2e/ops-indicator.spec.ts.
describe('OperationRow — what shares a line with what', () => {
  it('gives the name its own line, with only its mark beside it', () => {
    // A FINISHED row that IS news keeps a badge there; a running one carries
    // the percentage instead, which is the number the eye should land on
    // (nocx-hbdw4.5). Either way the line holds the name and one short mark.
    const { container } = render(() => <OperationRow {...RUNNING} phase="failed" />)
    const line = container.querySelector('.ui-operation-row__line')
    expect(line?.querySelector('.ui-operation-row__title')).not.toBeNull()
    expect(line?.querySelector('.ui-badge')).not.toBeNull()
    // The destination is NOT on that line: it is a path, it outruns any
    // panel, and beside the name it took the name's room.
    expect(line?.querySelector('.ui-operation-row__destination')).toBeNull()
  })

  it('puts the destination on its own line, whole value on hover', () => {
    const { container } = render(() => <OperationRow {...RUNNING} />)
    const dest = container.querySelector<HTMLElement>('.ui-operation-row__destination')
    expect(dest?.textContent).toBe('/srv/data')
    // What is on screen is usually an ellipsis of it, so the whole value has
    // to be reachable somewhere.
    expect(dest?.getAttribute('title')).toBe('/srv/data')
    expect(dest?.parentElement?.classList.contains('ui-operation-row__body')).toBe(true)
  })

  it('draws no destination line at all when there is none', () => {
    // An adopted transfer knows its name and not where it went.
    const { container } = render(() => <OperationRow {...RUNNING} destination="" />)
    expect(container.querySelector('.ui-operation-row__destination')).toBeNull()
  })
})

// ── What a finished row is worth reading (nocx-hbdw4.4) ─────────────────
//
// It read `appicon.png · Done · /home/dev`: a file name, a word and a path.
// No size, no when, no how long — somebody coming back to the list learnt
// nothing from it (owner, 2026-08-22).
describe('OperationRow once the work is over', () => {
  const ENDED = 1_700_000_000_000
  const FINISHED = {
    ...RUNNING,
    phase: 'written' as OperationPhase,
    done: 4_000_000,
    total: 4_000_000,
    speedBytesPerSecond: null,
    startedAt: ENDED - 14_000,
    endedAt: ENDED,
    now: ENDED + 5 * 60_000,
  }

  it('says how big it was, when it landed and how long it took', () => {
    const { container } = render(() => <OperationRow {...FINISHED} />)
    expect(container.querySelector('.ui-operation-row__summary')?.textContent).toBe(
      '4.0 MB · 5 min ago · took 14 s',
    )
  })

  it('puts the exact moment on hover, because the label ages and the clock does not', () => {
    const { container } = render(() => <OperationRow {...FINISHED} />)
    expect(container.querySelector('.ui-operation-row__summary')?.getAttribute('title')).toBe(
      new Date(ENDED).toLocaleString(),
    )
  })

  it('carries the summary on every outcome, not only on success', () => {
    // "It failed after 14 s having moved 4 MB" is the same three facts, and
    // the one a person is most likely to be asking about.
    const { container } = render(() => (
      <OperationRow {...FINISHED} phase="failed" error="permission denied" />
    ))
    expect(container.querySelector('.ui-operation-row__detail')?.textContent).toBe(
      'permission denied',
    )
    expect(container.querySelector('.ui-operation-row__summary')?.textContent).toContain(
      'took 14 s',
    )
  })

  it('draws no summary line at all for a row that knows none of it', () => {
    // An adopted transfer — the row lived in a page that was reloaded — has
    // no size, no start and no end of its own. An empty line would be a
    // claim; nothing is the truth.
    const { container } = render(() => (
      <OperationRow
        {...FINISHED}
        total={null}
        startedAt={null}
        endedAt={null}
        done={null}
        destination=""
        machine=""
      />
    ))
    expect(container.querySelector('.ui-operation-row__summary')).toBeNull()
  })

  it('follows the clock it is given, so a relative label can age', () => {
    const [now, setNow] = createSignal(ENDED)
    const { container } = render(() => <OperationRow {...FINISHED} now={now()} />)
    expect(container.querySelector('.ui-operation-row__summary')?.textContent).toContain('just now')
    setNow(ENDED + 10 * 60_000)
    expect(container.querySelector('.ui-operation-row__summary')?.textContent).toContain(
      '10 min ago',
    )
  })
})

// ── Which machine (amendment to nocx-hbdw4.4) ───────────────────────────
//
// The operations list is global: one list for every tab. A row saying
// `/var/www` is unambiguous with one connection open and meaningless with
// three.
describe('OperationRow says which machine', () => {
  it('reads the machine and the path as one fact, on one line', () => {
    const { container } = render(() => (
      <OperationRow {...RUNNING} machine="deploy@srv-01" destination="/var/www" />
    ))
    const where = container.querySelector<HTMLElement>('.ui-operation-row__destination')
    expect(where?.textContent).toBe('deploy@srv-01 · /var/www')
    // And the whole of it on hover, because what is on screen is an
    // ellipsis of it.
    expect(where?.getAttribute('title')).toBe('deploy@srv-01 · /var/www')
  })

  it('never lets the machine share the name’s line', () => {
    // The name is what the row exists to say, and the line it has is its
    // own — the machine joins the path below, not the title above.
    const { container } = render(() => (
      <OperationRow {...RUNNING} machine="deploy@srv-01" destination="/var/www" />
    ))
    const line = container.querySelector('.ui-operation-row__line')
    // The name and the percentage, and nothing about WHERE. Asserted by
    // absence rather than by the whole string, so adding a mark to the line
    // later does not fail a test about the machine.
    expect(line?.querySelector('.ui-operation-row__machine')).toBeNull()
    expect(line?.querySelector('.ui-operation-row__path')).toBeNull()
    expect(line?.textContent).toContain('notes.txt')
    expect(line?.textContent).not.toContain('deploy@srv-01')
  })

  it('says the machine even where there is no path to say it beside', () => {
    const { container } = render(() => (
      <OperationRow {...RUNNING} machine="This machine" destination="" />
    ))
    expect(container.querySelector('.ui-operation-row__destination')?.textContent).toBe(
      'This machine',
    )
  })

  it('draws nothing where it knows neither', () => {
    const { container } = render(() => <OperationRow {...RUNNING} machine="" destination="" />)
    expect(container.querySelector('.ui-operation-row__destination')).toBeNull()
  })
})
