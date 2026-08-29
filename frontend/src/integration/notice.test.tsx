// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { mountIntegrationNotice } from './notice'
import type { OutputRecording, OutputRecordingSource } from './status'
import type { SessionIntegrationChanged } from '../generated/session.integrationChanged'

// A test asserts what a user can do (AGENTS.md rule 1): the card exists, the
// actions on it are reachable from the state the user starts in, activating
// one reaches the handler, and the result appears afterwards. Everything here
// goes through mountIntegrationNotice, which is the seam the tab uses — the
// card's position in the pane is part of what the user gets, and a test that
// rendered the component into a bare div could not see it.

const TIMED_OUT: SessionIntegrationChanged = {
  sessionId: 's1',
  instanceId: '0123456789abcdef0123456789abcdef',
  sessionEpoch: 1,
  status: 'conventional',
  reason: 'handshake-timeout',
  shell: '/opt/homebrew/bin/bash',
}

const ZSH_TIMED_OUT: SessionIntegrationChanged = { ...TIMED_OUT, shell: '/bin/zsh' }

/** A recording source a test can move, because the fact it carries is a
 *  SETTING and settings are changed while a card is up. It is the same shape
 *  HistoryStatusStore satisfies in the product: what is true now, plus a way
 *  to be told when that stops being true. */
function recordingSource(initial: OutputRecording = 'unknown'): OutputRecordingSource & {
  set(next: OutputRecording): void
} {
  let current = initial
  const listeners = new Set<() => void>()
  return {
    outputRecording: () => current,
    subscribe(listener) {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    set(next) {
      current = next
      for (const listener of [...listeners]) listener()
    },
  }
}

let dispose: (() => void) | null = null
let pane: HTMLElement | null = null

afterEach(() => {
  dispose?.()
  dispose = null
  pane?.remove()
  pane = null
  // A dialog left open in the top layer outlives the component's root.
  document.querySelectorAll('dialog').forEach((d) => d.remove())
})

/** A stand-in for the tab's pane: the terminal is already in it, exactly as
 *  it is when a status arrives seconds into a session. */
function mount(over: Partial<Parameters<typeof mountIntegrationNotice>[1]> = {}) {
  pane = document.createElement('div')
  const terminal = document.createElement('div')
  terminal.className = 'scrollback-layout'
  pane.appendChild(terminal)
  document.body.appendChild(pane)
  const props = {
    fact: TIMED_OUT,
    // Nothing known about recording unless a test says otherwise: that is
    // the state before history.status answers, and it is what keeps every
    // test written before this seam asserting exactly what it asserted.
    recording: recordingSource(),
    copy: vi.fn(() => Promise.resolve()),
    onSuppressShell: vi.fn(),
    onDismiss: vi.fn(),
    ...over,
  }
  dispose = mountIntegrationNotice(pane, props)
  return { props, pane, terminal }
}

/** What the user can see and reach right now. Every dialog's markup is in
 *  the document whether it is open or not (`showModal` is what makes one
 *  visible), so a test that queried the whole document would pass on a panel
 *  nobody opened — and would then be unable to report the two-clicks defect
 *  it was written for. The innermost open dialog is the surface with the
 *  user's attention; with none open that is the card in the pane. */
const surface = (): ParentNode => {
  const open = [...document.querySelectorAll('dialog[open]')]
  return open[open.length - 1] ?? pane!.querySelector('.ui-status-card') ?? pane!
}

const button = (label: string): HTMLButtonElement => {
  const found = [...surface().querySelectorAll('button')].find(
    (b) => (b.textContent ?? '').trim() === label,
  )
  if (!found) throw new Error(`no button labelled ${label} on the visible surface`)
  return found
}

const buttonLabels = (): string[] =>
  [...surface().querySelectorAll('button')].map((b) => (b.textContent ?? '').trim())

const visibleText = (): string => surface().textContent ?? ''

const codeBlockText = (): string => surface().querySelector('.ui-code-block')?.textContent ?? ''

/** The chain of facts, as the user can read it on the surface in front of
 *  them. Scoped like everything else here: the chain used to live in a
 *  dialog of its own and a document-wide query could not tell the two
 *  surfaces apart. */
const chainTexts = (): string[] =>
  [...surface().querySelectorAll('.ui-marker-list__text')].map((n) => (n.textContent ?? '').trim())

describe('the degraded-session card', () => {
  it('says what happened, in the agreed words, with no program named', () => {
    const { pane } = mount()
    const card = pane.querySelector('.ui-status-card')
    expect(card).not.toBeNull()
    expect(card!.getAttribute('data-tone')).toBe('warning')
    expect(pane.querySelector('.ui-status-card__title')!.textContent).toBe('Not integrated')
    expect(pane.querySelector('.ui-status-card__desc')!.textContent).toBe(
      'Your shell did not answer nocx in time, so this tab is a plain terminal.',
    )
  })

  // It is a kit component placed by the surface, never a hand-rolled div
  // with its own colours — the defect two epics spent themselves unwinding.
  it('is the kit StatusCard, not a private one', () => {
    const { pane } = mount()
    expect(pane.querySelector('.ui-status-card')).not.toBeNull()
    expect(pane.querySelector('.nocx-integration-notice')).not.toBeNull()
  })

  // The owner's composition, measured on the installed build (nocx-aimo):
  // the remedy, the permanent silence, and the close. Details is gone —
  // it was a second surface holding what belongs behind the remedy.
  it('offers three actions and Details is not one of them', () => {
    mount()
    expect(buttonLabels()).toEqual(['How to fix', "Don't show again for this shell", '×'])
  })
})

// ── the pane says it is recording and not blocking (nocx-22k1c.3) ─────────
//
// Asserted on the card in the pane, which is what a user can see without
// opening anything. The backend records every session's output whether it is
// integrated or not (nocx-22k1c.1), so a plain tab that says nothing about it
// leaves the person to conclude the run is being thrown away.

/** The card's own sentence, as the person reads it — the one string that is
 *  in front of them without their opening anything. */
const cardDescription = (): string =>
  pane?.querySelector('.ui-status-card__desc')?.textContent ?? ''

describe("what the card says about this session's output", () => {
  it('says output is recorded and blocks are not, beside the reason', () => {
    mount({ recording: recordingSource('recorded') })
    expect(cardDescription()).toContain('did not answer nocx in time')
    expect(cardDescription()).toContain('still being recorded')
    expect(cardDescription()).toContain('command blocks')
  })

  it('says the opposite when there is nowhere to record to', () => {
    mount({ recording: recordingSource('not-recorded') })
    expect(cardDescription()).toContain('not being recorded')
    expect(cardDescription()).not.toContain('still being recorded')
  })

  // The interval this seam covers has both ends: it opens when the card is
  // raised and closes when the card is taken down, and the setting that
  // governs recording can be changed anywhere inside it. A snapshot taken at
  // the first instant would leave the card contradicting the settings screen
  // the person just used — the silent-degrade shape, drawn by us.
  it('follows the setting while the card is up', () => {
    const source = recordingSource('recorded')
    mount({ recording: source })
    expect(cardDescription()).toContain('still being recorded')
    source.set('not-recorded')
    expect(cardDescription()).toContain('not being recorded')
  })

  // The negative acceptance. A card is raised only for a degraded session,
  // so an integrated one has nothing to say — and the sentence about
  // recording must not become a thing every tab wears.
  it('draws no card at all for a session that integrated', () => {
    const { pane } = mount({
      fact: { ...TIMED_OUT, status: 'integrated', reason: undefined },
      recording: recordingSource('recorded'),
    })
    expect(pane.querySelector('.ui-status-card')).toBeNull()
    expect(pane.textContent ?? '').not.toContain('recorded')
  })

  // A source that goes away must not take the card with it: the unsubscribe
  // runs on dispose, and a listener still holding the dead component would
  // be the leak this test exists to catch.
  it('stops listening to the setting when the card goes', () => {
    const source = recordingSource('recorded')
    mount({ recording: source })
    dispose!()
    dispose = null
    // No throw, and nothing repainted into a disposed root.
    expect(() => source.set('not-recorded')).not.toThrow()
  })
})

// ── the three actions are three different things (nocx-aimo) ──────────────
//
// Written down as tests because the next reader will be tempted to collapse
// them: the cross takes this card away now, "Don't show again for this
// shell" takes every card for this shell away for good, and neither touches
// the tab's mark — the mark is the state of the session, not a notification.

describe('what each action on the card does', () => {
  it('the cross takes this card away and promises nothing further', () => {
    const { props } = mount()
    button('×').click()
    expect(props.onDismiss).toHaveBeenCalledOnce()
    expect(props.onSuppressShell).not.toHaveBeenCalled()
  })

  it('silences this shell when the user asks it to, from the card itself', () => {
    const { props } = mount()
    button("Don't show again for this shell").click()
    expect(props.onSuppressShell).toHaveBeenCalledOnce()
    expect(props.onDismiss).not.toHaveBeenCalled()
  })
})

// ── where the card sits (nocx-rzvq) ───────────────────────────────────────
//
// Measured on the installed build: the card floated over the terminal and
// covered the first prompt line. A card that hides the thing it describes is
// worse than the toast it replaced. jsdom computes no layout, so this pins
// what jsdom CAN see — the DOM order that puts the card above the terminal,
// and the stylesheet contract that keeps it from lifting out of the flow.

describe('where the card sits', () => {
  it('goes above the terminal, not over it', () => {
    const { pane, terminal } = mount()
    const notice = pane.querySelector('.nocx-integration-notice')!
    expect(pane.firstElementChild).toBe(notice)
    // DOCUMENT_POSITION_FOLLOWING: the terminal comes after the card, so the
    // card takes its space from the top of the pane instead of covering it.
    expect(notice.compareDocumentPosition(terminal) & Node.DOCUMENT_POSITION_FOLLOWING).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    )
  })

  it('gives the pane back to the terminal when it is dismissed', () => {
    const { pane, terminal } = mount()
    dispose!()
    dispose = null
    expect(pane.querySelector('.nocx-integration-notice')).toBeNull()
    expect(pane.firstElementChild).toBe(terminal)
  })

  // The stylesheet half of the same guarantee: an absolutely positioned or
  // z-indexed card is out of the flow, and out of the flow is exactly what
  // covered the prompt line.
  it('declares no rule that could lift it out of the flow', () => {
    const css = readFileSync(
      resolve(import.meta.dirname ?? '.', '../styles/surfaces/integration-notice.css'),
      'utf8',
    ).replace(/\/\*[\s\S]*?\*\//g, '')
    const open = css.indexOf('.nocx-integration-notice')
    expect(open).toBeGreaterThanOrEqual(0)
    const block = css.slice(css.indexOf('{', open) + 1, css.indexOf('}', open))
    expect(block).not.toMatch(/position\s*:/)
    expect(block).not.toMatch(/z-index\s*:/)
    // It never gives up its own height to the terminal: a card squeezed to
    // nothing is the overlay defect wearing a different hat.
    expect(block).toMatch(/flex\s*:\s*0\s+0\s+auto/)
  })
})

// ── the fix (nocx-0mqs) ───────────────────────────────────────────────────

describe('the fix', () => {
  // The owner's measurement: reaching the fix took two clicks through the
  // Details dialog. It is now the card's own action.
  it('is one click from the card', () => {
    mount()
    // Nothing is on screen before the click: the panels exist in the DOM
    // whether they are open or not, so "visible" is what this asserts.
    expect(codeBlockText()).toBe('')
    button('How to fix').click()
    expect(codeBlockText()).toContain('NOCX_SHELL_INTEGRATION')
  })

  // THE defect, from the user's side: a zsh session was shown a bash command
  // line and told about ~/.bashrc, neither of which exists in that session.
  it('is written for the shell the session is actually running', () => {
    mount({ fact: ZSH_TIMED_OUT })
    button('How to fix').click()
    const shown = visibleText()
    expect(shown).toContain('/bin/zsh')
    expect(shown).toContain('~/.zshrc')
    expect(shown).not.toMatch(/\bbash\b/)
  })

  it('copies the same lines the user was shown', () => {
    const { props } = mount({ fact: ZSH_TIMED_OUT })
    button('How to fix').click()
    const shown = codeBlockText()
    button('Copy').click()
    expect(props.copy).toHaveBeenCalledOnce()
    expect((props.copy as ReturnType<typeof vi.fn>).mock.calls[0][0]).toBe(shown)
  })

  // What nocx observed belongs where the user is acting on it, labelled a
  // guess in the sentence — the same sentence the Details chain shows, from
  // the same function, so the two cannot claim it with different force.
  it('says what nocx saw, labelled as a guess', () => {
    mount({ fact: { ...ZSH_TIMED_OUT, detail: { observedProcess: 'some-tui' } } })
    button('How to fix').click()
    const shown = visibleText()
    expect(shown).toContain('some-tui')
    expect(shown.toLowerCase()).toContain('guess')
  })

  it('does not offer itself for a reason nocx cannot advise on', () => {
    mount({ fact: { ...TIMED_OUT, reason: 'remote-command' } })
    expect(buttonLabels()).not.toContain('How to fix')
  })

  // …and the reader is not left with a card and nothing to open. A reason
  // with no honest remedy still has a chain of facts and an explanation, and
  // they are behind the same single dialog under the label that is true for
  // it.
  it('leaves the facts reachable for a reason nocx cannot advise on', () => {
    mount({ fact: { ...TIMED_OUT, reason: 'remote-command' } })
    button('What happened').click()
    expect(chainTexts().length).toBeGreaterThan(0)
  })

  // "Apply the fix for me" is nocx-cqkg and is deliberately NOT here: a
  // button that edits the user's startup files needs a backup and a diff
  // they approve first, which is a bead of its own.
  it('offers no apply-it-for-me action', () => {
    mount()
    button('How to fix').click()
    const labels = buttonLabels().map((l) => l.toLowerCase())
    expect(labels.some((l) => l.includes('apply') || l.includes('fix it for me'))).toBe(false)
  })
})

// ── what Details used to hold (nocx-aimo) ─────────────────────────────────
//
// The owner's measurement: the fact chain, the explanation and the remedy
// were split across two dialogs, so reading what nocx knew and reading what
// to do about it were different journeys. There is one dialog now, and
// everything is one click from the card.

describe('the facts behind the remedy', () => {
  const openFix = () => button('How to fix').click()

  it('shows the chain of facts, starting with the shell nocx actually started', () => {
    mount()
    openFix()
    const items = chainTexts()
    expect(items[0]).toBe('nocx started /opt/homebrew/bin/bash')
    expect(items.some((t) => t.includes('plain terminal'))).toBe(true)
    expect(items.some((t) => t.includes('never answered'))).toBe(true)
  })

  it('labels the observed process as a guess, in the sentence itself', () => {
    mount({ fact: { ...TIMED_OUT, detail: { observedProcess: 'some-tui' } } })
    openFix()
    const guess = chainTexts().find((t) => t.includes('some-tui'))
    expect(guess).toBeDefined()
    expect(guess!.toLowerCase()).toContain('guess')
  })

  it('omits the guess entirely when the backend observed nothing', () => {
    mount()
    openFix()
    expect(chainTexts().some((t) => t.toLowerCase().includes('guess'))).toBe(false)
  })

  // One surface, not two: nothing anywhere offers a second way in.
  it('leaves no Details surface behind it', () => {
    mount()
    expect(document.body.textContent).not.toContain('Details')
    openFix()
    expect(document.body.textContent).not.toContain('Details')
  })
})

// ── the name the process table gave (nocx-aimo) ───────────────────────────
//
// Measured by the owner on the installed build: `zsh (kiro-cli-te` — a name
// cut off mid-word, which reads as a defect in nocx. It is the kernel's
// fixed-width p_comm, and that width is exactly why the observation carries
// no path, no arguments and none of the user's own text. So the product says
// the name may be short, rather than pretending it is whole.

describe('the observed name, when the process table had no room for it', () => {
  /** Sixteen characters — everything darwin's p_comm can hold. */
  const FILLED = 'zsh (kiro-cli-te'

  it('does not present a name that fills the field as if it were complete', () => {
    mount({ fact: { ...TIMED_OUT, detail: { observedProcess: FILLED } } })
    button('How to fix').click()
    const guess = chainTexts().find((t) => t.includes(FILLED))!
    expect(guess).toContain(`${FILLED}…`)
    expect(guess).toContain('cut short')
  })

  it('says nothing of the kind about a name that plainly fits', () => {
    mount({ fact: { ...TIMED_OUT, detail: { observedProcess: 'some-tui' } } })
    button('How to fix').click()
    const guess = chainTexts().find((t) => t.includes('some-tui'))!
    expect(guess).not.toContain('…')
    expect(guess).not.toContain('cut short')
  })
})

// ── the explanation (nocx-qs68) ───────────────────────────────────────────

describe('Learn more', () => {
  // It used to open a GitHub blob URL, which 404s on a rename, a
  // default-branch change or an aeroplane. The explanation now ships in the
  // build, so it resolves wherever the app runs.
  it('explains integration inside the app', () => {
    mount()
    button('How to fix').click()
    button('Learn more').click()
    expect(visibleText()).toContain('An integrated tab knows where each command starts and ends')
  })

  it('opens no browser and needs no network', () => {
    const { props } = mount()
    expect(Object.keys(props)).not.toContain('openUrl')
    button('How to fix').click()
    button('Learn more').click()
    expect(document.querySelector('a[href]')).toBeNull()
  })
})
