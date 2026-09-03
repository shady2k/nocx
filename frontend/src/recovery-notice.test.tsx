// @vitest-environment jsdom
//
// The reclaimed pane's missing-output notice (nocx-fz4qa).
//
// A test asserts what a user can do (AGENTS.md rule 1): the card exists in
// the pane the person is looking at, it names how much is gone and which of
// the two things took it, and it is dismissable. Everything here goes
// through mountRecoveryNotice — the seam the tab uses — because where the
// card sits is part of what the user gets, and a test that rendered the
// component into a bare div could not see that.
//
// The negative is half of this file: a reclaim with no hole must draw
// NOTHING. A notice that appears always means nothing.
import { describe, it, expect, vi, afterEach } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { mountRecoveryNotice, recoveryAccount } from './recovery-notice'
import type { SessionRecovery } from './ipc'

const SIZE = { cols: 80, rows: 24, xpixel: 0, ypixel: 0 }

function recovery(over: Partial<SessionRecovery> = {}): SessionRecovery {
  return { bytes: 4096, gaps: [], size: SIZE, ...over }
}

let dispose: (() => void) | null = null
let pane: HTMLElement | null = null

afterEach(() => {
  dispose?.()
  dispose = null
  pane?.remove()
  pane = null
})

/** A stand-in for the tab's pane with the terminal already in it — the state
 *  a pane is in the instant a reclaim resolves. */
function mount(rec: SessionRecovery, onDismiss = vi.fn()) {
  pane = document.createElement('div')
  const terminal = document.createElement('div')
  terminal.className = 'scrollback-layout'
  pane.appendChild(terminal)
  document.body.appendChild(pane)
  dispose = mountRecoveryNotice(pane, { recovery: rec, onDismiss })
  return { pane, terminal, onDismiss }
}

const card = () => document.querySelector('.ui-status-card')
const title = () => document.querySelector('.ui-status-card__title')?.textContent ?? ''
const desc = () => document.querySelector('.ui-status-card__desc')?.textContent ?? ''

describe('a reclaimed pane says what is missing (nocx-fz4qa)', () => {
  it('says nothing at all when the reclaim recovered a whole recording', () => {
    mount(recovery({ gaps: [] }))
    expect(dispose).toBeNull()
    expect(card()).toBeNull()
    expect(document.querySelector('.nocx-recovery-notice')).toBeNull()
  })

  it('says nothing for a hole of no bytes', () => {
    mount(recovery({ gaps: [{ start: 500, end: 500, reason: 'cap' }] }))
    expect(card()).toBeNull()
  })

  it('names the retention bound when that is what dropped the bytes', () => {
    mount(recovery({ gaps: [{ start: 0, end: 2_000_000, reason: 'cap' }] }))
    expect(card()).not.toBeNull()
    expect(title()).toContain('2.0 MB')
    expect(desc()).toContain('size limit')
    expect(desc()).not.toContain('never recorded')
  })

  it('names the unrecorded stretch when nothing kept it', () => {
    mount(recovery({ gaps: [{ start: 0, end: 4000, reason: 'unrecorded' }] }))
    expect(title()).toContain('4.0 kB')
    expect(desc()).toContain('never recorded')
    expect(desc()).not.toContain('size limit')
  })

  it('separates the two when a reclaim hit both', () => {
    mount(
      recovery({
        gaps: [
          { start: 0, end: 1000, reason: 'cap' },
          { start: 3000, end: 5000, reason: 'unrecorded' },
        ],
      }),
    )
    // Three numbers, and each one is answerable: the whole hole, then the
    // half each owner is responsible for.
    expect(title()).toContain('3.0 kB')
    expect(desc()).toContain('1.0 kB')
    expect(desc()).toContain('size limit')
    expect(desc()).toContain('2.0 kB')
    expect(desc()).toContain('never recorded')
  })

  it('carries a reason it does not recognise rather than inventing one', () => {
    mount(recovery({ gaps: [{ start: 0, end: 1000, reason: 'suppressed' }] }))
    expect(card()).not.toBeNull()
    expect(desc()).toContain('suppressed')
  })

  it('is taken away by the user, and tells the pane so', () => {
    const { onDismiss } = mount(recovery({ gaps: [{ start: 0, end: 1000, reason: 'cap' }] }))
    const cross = [...document.querySelectorAll('button')].find(
      (b) => b.getAttribute('aria-label') === 'Dismiss',
    )
    expect(cross).toBeDefined()
    cross!.click()
    expect(onDismiss).toHaveBeenCalledTimes(1)
  })

  // The kit, not a second vocabulary for one concept. Two epics in this repo
  // were spent unwinding hand-rolled controls inside surfaces.
  it('is the kit StatusCard, placed in the pane flow above the terminal', () => {
    const { pane: host, terminal } = mount(
      recovery({ gaps: [{ start: 0, end: 1000, reason: 'cap' }] }),
    )
    const holder = host.querySelector('.nocx-recovery-notice')
    expect(holder).not.toBeNull()
    expect(holder!.querySelector('.ui-status-card')).not.toBeNull()
    // In the flow, ahead of the terminal — a message about the session must
    // not hide the session.
    expect(
      holder!.compareDocumentPosition(terminal) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
  })

  // Placement only. A surface may place a kit component and may never
  // repaint it (frontend/src/ui/README.md).
  it('repaints nothing: its stylesheet places and no more', () => {
    const css = readFileSync(
      resolve(import.meta.dirname ?? '.', './styles/surfaces/recovery-notice.css'),
      'utf8',
    )
    const rules = css.replace(/\/\*[\s\S]*?\*\//g, '')
    for (const forbidden of [
      'background',
      'border',
      'color',
      'font-',
      'padding',
      'box-shadow',
      'position',
      'z-index',
    ]) {
      expect(rules).not.toContain(forbidden)
    }
  })
})

describe('recoveryAccount (nocx-fz4qa)', () => {
  it('is null for a pane that was never reclaimed', () => {
    expect(recoveryAccount(null)).toBeNull()
  })

  it('adds up every hole of the same kind', () => {
    const account = recoveryAccount(
      recovery({
        gaps: [
          { start: 0, end: 100, reason: 'cap' },
          { start: 300, end: 500, reason: 'cap' },
        ],
      }),
    )
    expect(account).toEqual({ missing: 300, dropped: 300, unrecorded: 0, other: 0, reasons: [] })
  })

  it('ignores a range that runs backwards rather than subtracting it', () => {
    expect(
      recoveryAccount(recovery({ gaps: [{ start: 900, end: 100, reason: 'cap' }] })),
    ).toBeNull()
  })
})
