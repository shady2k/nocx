// @vitest-environment jsdom
//
// The third state, said out loud (nocx-k6p18.5).
//
// A test asserts what a user can do (AGENTS.md rule 1): the card exists in the
// pane the person is looking at, it says the blocks are neither running nor
// finished, it says WHY nobody could check, and it is dismissable. Everything
// goes through mountUnreconciledNotice — the seam the tab uses — because where
// the card sits is part of what the user gets.
//
// The negative is half of this file: a page whose rows were all judged must
// draw NOTHING, which is nearly every page. A notice that appears always means
// nothing.
import { describe, it, expect, vi, afterEach } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import {
  mountUnreconciledNotice,
  unreconciledAccount,
  type UnreconciledCause,
  type UnreconciledRow,
} from './unreconciled-notice'

const row = (cause: UnreconciledCause | null): UnreconciledRow => ({ unreconciled: cause })

let dispose: (() => void) | null = null
let pane: HTMLElement | null = null

afterEach(() => {
  dispose?.()
  dispose = null
  pane?.remove()
  pane = null
})

/** A stand-in for the tab's pane with the terminal already in it — the state a
 *  pane is in the instant a restore resolves. */
function mount(rows: UnreconciledRow[], onDismiss = vi.fn()) {
  pane = document.createElement('div')
  const terminal = document.createElement('div')
  terminal.className = 'scrollback-layout'
  pane.appendChild(terminal)
  document.body.appendChild(pane)
  dispose = mountUnreconciledNotice(pane, { rows, onDismiss })
  return { pane, terminal, onDismiss }
}

const card = () => document.querySelector('.ui-status-card')
const title = () => document.querySelector('.ui-status-card__title')?.textContent ?? ''
const desc = () => document.querySelector('.ui-status-card__desc')?.textContent ?? ''

describe('a restored tab says which blocks nobody could check (nocx-k6p18.5)', () => {
  it('says nothing at all when every row was judged', () => {
    mount([row(null), row(null)])
    expect(dispose).toBeNull()
    expect(card()).toBeNull()
    expect(document.querySelector('.nocx-unreconciled-notice')).toBeNull()
  })

  it('says nothing for a pane with no past at all', () => {
    mount([])
    expect(card()).toBeNull()
  })

  // The sentence the whole state exists for: NEITHER, in those words. A card
  // that said "these commands failed" or "these are still running" would be
  // the two lies this change removes, one from each end.
  it('says the blocks are neither running nor finished', () => {
    mount([row('hostUnreachable')])
    expect(card()).not.toBeNull()
    expect(title()).toContain('neither running nor finished')
    expect(title()).toContain('One command')
  })

  it('counts the blocks, so a person knows how much is in doubt', () => {
    mount([row('hostUnreachable'), row('hostUnreachable'), row(null)])
    expect(title()).toContain('2 commands')
  })

  // Each cause is a different sentence, and two of them name something a
  // person can act on. Asserted per cause rather than once, for the same
  // reason the backend asserts the verdict per failure mode: the value of
  // the third state IS the cause.
  const sentences: Array<[UnreconciledCause, string]> = [
    ['notYetAsked', 'has not been able to check'],
    ['ambiguousInventory', 'multiple inventories claim'],
    ['noInventory', 'nothing on this host can be asked'],
    ['connectionRefused', 'refused the connection'],
    ['timedOut', 'did not answer in time'],
    ['hostUnreachable', 'has not been reachable since nocx restarted'],
    ['vaultSealed', 'the vault is locked'],
  ]
  for (const [cause, sentence] of sentences) {
    it(`says why for ${cause}`, () => {
      mount([row(cause)])
      expect(desc()).toContain(sentence)
      // And it never claims the session ended: "may still be running" is the
      // honest half, and it is in every sentence.
      expect(desc()).toContain('may still be running')
    })
  }

  it('says both when two causes are in one pane, rather than picking one', () => {
    mount([row('vaultSealed'), row('hostUnreachable')])
    expect(desc()).toContain('vault is locked')
    expect(desc()).toContain('has not been reachable')
  })

  it('is taken away by the user, and tells the pane so', () => {
    const { onDismiss } = mount([row('hostUnreachable')])
    const cross = [...document.querySelectorAll('button')].find(
      (b) => b.getAttribute('aria-label') === 'Dismiss',
    )
    expect(cross).toBeDefined()
    cross!.click()
    expect(onDismiss).toHaveBeenCalledTimes(1)
  })

  // The kit, not a second vocabulary for one concept.
  it('is the kit StatusCard, placed in the pane flow above the terminal', () => {
    const { pane: host, terminal } = mount([row('hostUnreachable')])
    const holder = host.querySelector('.nocx-unreconciled-notice')
    expect(holder).not.toBeNull()
    expect(holder!.querySelector('.ui-status-card')).not.toBeNull()
    expect(
      holder!.compareDocumentPosition(terminal) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
  })

  // Placement only. A surface may place a kit component and may never repaint
  // it (frontend/src/ui/README.md).
  it('repaints nothing: its stylesheet places and no more', () => {
    const css = readFileSync(
      resolve(import.meta.dirname ?? '.', './styles/surfaces/unreconciled-notice.css'),
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

describe('unreconciledAccount (nocx-k6p18.5)', () => {
  it('is null when nothing is awaiting a verdict', () => {
    expect(unreconciledAccount([row(null)])).toBeNull()
  })

  it('de-duplicates the causes and keeps the order they arrived in', () => {
    expect(
      unreconciledAccount([row('vaultSealed'), row('hostUnreachable'), row('vaultSealed')]),
    ).toEqual({ blocks: 3, causes: ['vaultSealed', 'hostUnreachable'] })
  })
})
