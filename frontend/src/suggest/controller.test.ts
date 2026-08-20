// @vitest-environment jsdom
// CompletionController — the lifecycle contract (design §8.7, §8.9.2, §8.9.4):
// Tab opens the dropdown, zero candidates is a state the product shows (the
// honest empty row — never silence), first results render as they arrive, a
// late arrival never moves the selection, a keystroke aborts, one provider's
// error never kills the others, the latency budget gates a TYPING query's
// open decision but an explicit Tab's open intent survives it, and ghost
// text accepts only under every §8.7 precondition.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { EditorView } from '@codemirror/view'
import {
  CompletionController,
  ghostAcceptable,
  ghostTail,
  LATENCY_BUDGET_MS,
  type CompletionEditor,
} from './controller'
import { CommandEditor } from '../editor'
import { CompletionDropdown } from '../ui/completion-dropdown'
import type { Candidate } from './candidate'
import type { SuggestionProvider, SuggestContext, EmptyReason } from './providers'
import { setDecisionTracing } from '../log'

/** The editor's internal CM6 view — reached only to seed selections and to
 *  read rendered decorations (the ghost span), the way editor.test.ts does. */
const viewOf = (ed: CommandEditor): EditorView => (ed as unknown as { view: EditorView }).view

/** Dispatch a keydown exactly where a user's keystroke lands. */
const keyOn = (view: EditorView, init: KeyboardEventInit) =>
  view.contentDOM.dispatchEvent(
    new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }),
  )

// ── fakes ────────────────────────────────────────────────────────────────

class FakeEditor implements CompletionEditor {
  doc: string
  caret = 0
  applied: Array<{ from: number; to: number; text: string }> = []
  constructor(doc = '') {
    this.doc = doc
    this.caret = doc.length
  }
  getDoc(): string {
    return this.doc
  }
  getSelection(): { from: number; to: number } {
    return { from: this.caret, to: this.caret }
  }
  applyReplacement(from: number, to: number, text: string): void {
    this.doc = this.doc.slice(0, from) + text + this.doc.slice(to)
    this.caret = from + text.length
    this.applied.push({ from, to, text })
  }
  /** A user keystroke: mutate the doc like the editor's input path would. */
  type(ch: string): void {
    this.doc = this.doc.slice(0, this.caret) + ch + this.doc.slice(this.caret)
    this.caret += ch.length
  }
}

interface Deferred {
  resolve: (c: Candidate[]) => void
  reject: (e: unknown) => void
  promise: Promise<Candidate[]>
}
const deferred = (): Deferred => {
  let resolve!: (c: Candidate[]) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<Candidate[]>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { resolve, reject, promise }
}

/** A provider whose deliveries the test controls one at a time. */
const manualProvider = (
  id: string,
  applicable: boolean,
): { provider: SuggestionProvider; next: () => Deferred } => {
  const queue: Deferred[] = []
  const provider: SuggestionProvider = {
    id,
    targetId: 'shell',
    applicable: () => applicable,
    suggest: () => {
      const d = deferred()
      queue.push(d)
      return d.promise.then((candidates) => ({ candidates }))
    },
  }
  return { provider, next: () => queue.shift()! }
}

/** A provider that answers instantly. `emptyReason` lets a test make the
 *  provider answer NOTHING with a specific explanation. */
const instantProvider = (
  id: string,
  make: (ctx: SuggestContext) => Candidate[],
  applicable = true,
  emptyReason?: EmptyReason,
): SuggestionProvider => ({
  id,
  targetId: 'shell',
  applicable: () => applicable,
  suggest: (ctx) => Promise.resolve({ candidates: make(ctx), emptyReason }),
})

/** A provider that always answers nothing, with the given reason. */
const emptyProvider = (id: string, emptyReason?: EmptyReason): SuggestionProvider => ({
  id,
  targetId: 'shell',
  applicable: () => true,
  suggest: () => Promise.resolve({ candidates: [], emptyReason }),
})

const cand = (over: Partial<Candidate> & { id: string }): Candidate => ({
  targetId: 'shell',
  providerId: 'p',
  displayText: over.id,
  insertText: over.id,
  replacement: { from: 0, to: 5 },
  matchRanges: [{ from: 0, to: 5 }],
  source: 'command',
  eligibleForGhostText: true,
  ...over,
})

interface Rig {
  editor: FakeEditor
  dropdown: CompletionDropdown
  controller: CompletionController
  mount: HTMLElement
}

const rig = (opts: {
  providers: SuggestionProvider[]
  latencyBudgetMs?: number
  recallIsOpen?: () => boolean
  editorDoc?: string
  acceptSnippet?: (snippetId: string) => void
}): Rig => {
  const editor = new FakeEditor(opts.editorDoc ?? 'git sta')
  const container = document.createElement('div')
  document.body.appendChild(container)
  const dropdown = new CompletionDropdown({ onHover: () => {}, onPick: () => {} })
  const controller = new CompletionController({
    providers: opts.providers,
    dropdown,
    env: () => ({ isLocal: true, cwd: '/repo', host: '' }),
    recallIsOpen: opts.recallIsOpen,
    latencyBudgetMs: opts.latencyBudgetMs,
    acceptSnippet: opts.acceptSnippet,
    now: () => 1_750_000_000_000,
  })
  controller.attach(editor, container)
  return { editor, dropdown, controller, mount: container }
}

const key = (k: string, init: KeyboardEventInit = {}): KeyboardEvent =>
  new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true, ...init })

/** The row the selection currently sits on. */
const selectedRow = (dropdown: CompletionDropdown) =>
  dropdown.root.querySelector('.ui-floating-panel__row[data-selected="true"]')

/** The honest empty row, when the panel shows one. */
const emptyRow = (dropdown: CompletionDropdown) =>
  dropdown.root.querySelector('.ui-floating-panel__row[data-empty="true"]')

/** Flush microtasks and zero-delay timers, under fake timers or real. */
const flush = async () => {
  if (vi.isFakeTimers()) {
    await vi.advanceTimersByTimeAsync(0)
  } else {
    await new Promise((r) => setTimeout(r, 0))
  }
}

beforeEach(() => {
  vi.useFakeTimers()
})
afterEach(() => {
  vi.useRealTimers()
})

// ── opening ──────────────────────────────────────────────────────────────

describe('opening', () => {
  it('Tab opens the dropdown with the first results', async () => {
    const { dropdown, controller } = rig({
      providers: [instantProvider('a', () => [cand({ id: 'git status' })])],
    })
    controller.open()
    await flush()
    expect(dropdown.isOpen).toBe(true)
    expect(dropdown.root.querySelectorAll('.ui-floating-panel__row')).toHaveLength(1)
  })

  it('with zero candidates Tab opens the honest empty row — never silence', async () => {
    const { dropdown, controller } = rig({
      providers: [emptyProvider('a')],
    })
    controller.open()
    await flush()
    expect(dropdown.isOpen).toBe(true)
    expect(emptyRow(dropdown)).not.toBeNull()
    expect(emptyRow(dropdown)?.textContent).toContain('No matches')
    // One row, not selectable, no footer hints (nothing to insert or cycle).
    expect(dropdown.root.querySelectorAll('.ui-floating-panel__row')).toHaveLength(1)
    expect(emptyRow(dropdown)?.getAttribute('aria-selected')).toBe('false')
    expect(dropdown.root.querySelector('.ui-floating-panel__footer')).toBeNull()
  })

  it('the empty row names the directory when the fs provider knows it', async () => {
    const { dropdown, controller } = rig({
      providers: [emptyProvider('fs', { kind: 'dirs-only-empty', dir: 'Downloads' })],
    })
    controller.open()
    await flush()
    expect(emptyRow(dropdown)?.textContent).toContain('No subdirectories in Downloads')
  })

  it('a pending command snapshot is named, not hidden', async () => {
    const { dropdown, controller } = rig({
      providers: [
        emptyProvider('cmd', { kind: 'command-names', state: 'running', ageMs: 0, reason: '' }),
      ],
    })
    controller.open()
    await flush()
    expect(emptyRow(dropdown)?.textContent).toContain('Command names are still loading')
  })

  // Assertion 36. Each of the five discovery states maps to a DISTINCT,
  // user-visible string. Distinct enum values do not count: the whole defect
  // was five states rendering as one sentence — "command names are still
  // loading" — which is true for exactly one of them and tells a user whose
  // scan failed to wait for something that is never coming.
  it('each of the five discovery states renders a different sentence', async () => {
    const states = [
      { state: 'running' as const, ageMs: 0, reason: '' },
      { state: 'ready' as const, ageMs: 0, reason: '' },
      { state: 'stale' as const, ageMs: 90_000, reason: '' },
      { state: 'timed-out' as const, ageMs: 0, reason: 'the scan did not finish' },
      { state: 'failed' as const, ageMs: 0, reason: 'remote host refused the exec' },
    ]
    const rendered: string[] = []
    for (const s of states) {
      const { dropdown, controller } = rig({
        providers: [emptyProvider('cmd', { kind: 'command-names', ...s })],
      })
      controller.open()
      await flush()
      const text = emptyRow(dropdown)?.textContent ?? ''
      expect(text, `state ${s.state} rendered nothing`).not.toBe('')
      rendered.push(text)
      controller.dismiss()
    }
    expect(new Set(rendered).size, `two states render the same row: ${rendered.join(' | ')}`).toBe(
      5,
    )

    // And each says the thing that is true of it, not a generic sentence.
    expect(rendered[0]).toContain('still loading')
    expect(rendered[1]).toContain('No command names match')
    expect(rendered[2]).toContain('out of date')
    expect(rendered[2]).toContain('2 min')
    expect(rendered[3]).toContain('timed out')
    expect(rendered[4]).toContain('remote host refused the exec')
  })

  it('the most specific reason wins when several providers answer nothing', async () => {
    const { dropdown, controller } = rig({
      providers: [
        emptyProvider('history', { kind: 'no-match' }),
        emptyProvider('cmd', { kind: 'command-names', state: 'running', ageMs: 0, reason: '' }),
        emptyProvider('fs', { kind: 'dirs-only-empty', dir: 'Downloads' }),
      ],
    })
    controller.open()
    await flush()
    expect(emptyRow(dropdown)?.textContent).toContain('No subdirectories in Downloads')
  })
  it('erasing the document closes the panel — a footer-only panel is unreachable', async () => {
    // The provider declines on an empty line exactly like the shipped set
    // (history and fs both do; the command provider answers nothing for an
    // empty token), so the erase query settles with zero candidates.
    const { editor, dropdown, controller } = rig({
      providers: [
        instantProvider('a', (ctx) => (ctx.doc.trim() === '' ? [] : [cand({ id: 'x' })])),
      ],
    })
    controller.open()
    await flush()
    expect(dropdown.isOpen).toBe(true)

    // The user erases everything: the fresh query has an empty document and
    // nothing to complete, so the panel must CLOSE — not hang showing a
    // footer with no rows.
    editor.doc = ''
    editor.caret = 0
    controller.onDocChanged()
    await flush()
    expect(dropdown.isOpen).toBe(false)
    expect(dropdown.root.querySelectorAll('.ui-floating-panel__row')).toHaveLength(0)
  })

  it('an empty line opens nothing (every shipped provider declines)', async () => {
    const { dropdown, controller } = rig({
      providers: [
        {
          id: 'a',
          targetId: 'shell',
          applicable: (ctx) => ctx.doc.trim() !== '',
          suggest: () => Promise.resolve({ candidates: [cand({ id: 'x' })] }),
        },
      ],
      editorDoc: '',
    })
    controller.open()
    await flush()
    expect(dropdown.isOpen).toBe(false)
  })

  it('an inapplicable provider is not consulted', async () => {
    const suggested = vi.fn()
    const { controller } = rig({
      providers: [instantProvider('a', suggested, false)],
    })
    controller.open()
    await flush()
    expect(suggested).not.toHaveBeenCalled()
  })

  it('erasing the document while the empty row is showing closes it too', async () => {
    const { editor, dropdown, controller } = rig({
      providers: [emptyProvider('a')],
    })
    controller.open()
    await flush()
    expect(dropdown.isOpen).toBe(true)
    expect(emptyRow(dropdown)).not.toBeNull()

    editor.doc = ''
    editor.caret = 0
    controller.onDocChanged()
    await flush()
    expect(dropdown.isOpen).toBe(false)
  })

  it('typing with the dropdown closed never opens it (the ghost is the typing surface)', async () => {
    const { editor, dropdown, controller } = rig({
      providers: [instantProvider('a', () => [cand({ id: 'x' })])],
    })
    editor.type('t')
    controller.onDocChanged()
    await flush()
    // Candidates exist (the ghost shows), but the dropdown stays closed —
    // only Tab may open it.
    expect(dropdown.isOpen).toBe(false)
  })

  it('typing with the dropdown closed never opens the empty row either', async () => {
    const { editor, dropdown, controller } = rig({
      providers: [emptyProvider('a')],
    })
    editor.type('z')
    controller.onDocChanged()
    await flush()
    expect(dropdown.isOpen).toBe(false)
  })
})

// ── streaming, merging, selection ────────────────────────────────────────

describe('streaming and selection', () => {
  it('first results render as they arrive — a slow provider is not waited for', async () => {
    const slow = manualProvider('slow', true)
    const { dropdown, controller } = rig({
      providers: [slow.provider, instantProvider('fast', () => [cand({ id: 'fast-cand' })])],
    })
    controller.open()
    await flush()
    // The fast provider's batch arrived first; the dropdown is already open.
    expect(dropdown.isOpen).toBe(true)
    const rows = dropdown.root.querySelectorAll('.ui-floating-panel__row')
    expect(rows).toHaveLength(1)
    expect(rows[0].textContent).toContain('fast-cand')
  })

  it('a late arrival merges in and may not move the selection off the candidate', async () => {
    const slow = manualProvider('slow', true)
    const { dropdown, controller } = rig({
      providers: [slow.provider, instantProvider('fast', () => [cand({ id: 'first' })])],
    })
    controller.open()
    await flush()
    expect(dropdown.isOpen).toBe(true)

    // User moves the selection down (the list will grow beneath it).
    expect(controller.handleKey(key('ArrowDown'))).toBe(true)

    // The slow provider lands late: the new row appends, the selected
    // candidate stays selected.
    slow.next().resolve([cand({ id: 'second' })])
    await flush()
    const selected = dropdown.root.querySelector('.ui-floating-panel__row[data-selected="true"]')
    expect(selected?.textContent).toContain('first')
  })

  it('the same candidate from two providers dedups by id', async () => {
    const { dropdown, controller } = rig({
      providers: [
        instantProvider('p1', () => [cand({ id: 'dup' })]),
        instantProvider('p2', () => [cand({ id: 'dup' })]),
      ],
    })
    controller.open()
    await flush()
    expect(dropdown.root.querySelectorAll('.ui-floating-panel__row')).toHaveLength(1)
  })

  it('one provider error does not kill the others', async () => {
    const failing = manualProvider('fail', true)
    const { dropdown, controller } = rig({
      providers: [failing.provider, instantProvider('ok', () => [cand({ id: 'ok-cand' })])],
    })
    controller.open()
    await flush()
    failing.next().reject(new Error('boom'))
    await flush()
    expect(dropdown.isOpen).toBe(true)
    expect(dropdown.root.textContent).toContain('ok-cand')
  })

  it('a keystroke aborts: batches from the old query are dropped', async () => {
    const slow = manualProvider('slow', true)
    const { editor, dropdown, controller } = rig({ providers: [slow.provider] })
    controller.open()
    await flush()

    // The user types before the slow provider answers.
    editor.type('x')
    controller.onDocChanged()
    await flush()

    // The old query's delivery arrives late — dropped, never rendered.
    slow.next().resolve([cand({ id: 'stale' })])
    await flush()
    expect(dropdown.isOpen).toBe(false)
    expect(dropdown.root.textContent).not.toContain('stale')
  })

  it('a keystroke while the dropdown is open re-queries and resets the selection', async () => {
    const slow = manualProvider('slow', true)
    const { editor, dropdown, controller } = rig({
      providers: [slow.provider, instantProvider('fast', () => [cand({ id: 'one' })])],
    })
    controller.open()
    await flush()
    expect(controller.handleKey(key('ArrowDown'))).toBe(true)
    expect(
      dropdown.root.querySelector('.ui-floating-panel__row[data-selected="true"]')?.textContent,
    ).toContain('one')

    editor.type('x')
    controller.onDocChanged()
    await flush()
    // The fresh query's first batch replaces the list; selection resets.
    expect(dropdown.isOpen).toBe(true)
    expect(
      dropdown.root.querySelector('.ui-floating-panel__row[data-selected="true"]')?.textContent,
    ).toContain('one')
  })

  it('a typing query that finds nothing while the panel is open shows the honest row', async () => {
    const { editor, dropdown, controller } = rig({
      providers: [instantProvider('a', () => [])],
    })
    // Tab opens with candidates…
    controller.open()
    await flush()
    expect(dropdown.isOpen).toBe(true)
    expect(emptyRow(dropdown)).not.toBeNull()
    // …the user keeps typing; the row is replaced by candidates when they
    // arrive, and stays the honest "No matches" while they do not.
    editor.type('x')
    controller.onDocChanged()
    await flush()
    expect(dropdown.isOpen).toBe(true)
    expect(emptyRow(dropdown)?.textContent).toContain('No matches')
  })

  it('the latency budget gates the open decision of a TYPING query', async () => {
    vi.useRealTimers()
    const slow = manualProvider('slow', true)
    const { editor, dropdown, controller } = rig({
      providers: [slow.provider],
      latencyBudgetMs: 50,
    })
    // A typing query (not Tab): nothing arrived within the budget, so the
    // dropdown stays closed and the late answer is discarded — a dropdown
    // must not flash open under the fingers.
    editor.type('t')
    controller.onDocChanged()
    await new Promise((r) => setTimeout(r, 80))
    slow.next().resolve([cand({ id: 'late' })])
    await flush()
    expect(dropdown.isOpen).toBe(false)
  })

  it('a late batch for an explicit Tab opens the dropdown after the budget — the open intent survives', async () => {
    vi.useRealTimers()
    const slow = manualProvider('slow', true)
    const { dropdown, controller } = rig({
      providers: [slow.provider],
      latencyBudgetMs: 50,
    })
    controller.open()
    // Nothing within the budget; the Tab query is willing to wait.
    await new Promise((r) => setTimeout(r, 80))
    expect(dropdown.isOpen).toBe(false)
    slow.next().resolve([cand({ id: 'late' })])
    await flush()
    // The late batch still opens the dropdown — Tab asked for the list.
    expect(dropdown.isOpen).toBe(true)
    expect(dropdown.root.textContent).toContain('late')
  })

  it('a first result inside the budget opens the dropdown', async () => {
    vi.useRealTimers()
    const slow = manualProvider('slow', true)
    const { dropdown, controller } = rig({ providers: [slow.provider], latencyBudgetMs: 200 })
    controller.open()
    await new Promise((r) => setTimeout(r, 20))
    slow.next().resolve([cand({ id: 'in-time' })])
    await flush()
    expect(dropdown.isOpen).toBe(true)
  })

  it('the path rung holds across async batches — paths stay above history whatever the arrival order', async () => {
    const history = manualProvider('history', true)
    const fs = manualProvider('fs', true)
    const { dropdown, controller } = rig({ providers: [history.provider, fs.provider] })
    controller.open()
    await flush()

    // History lands FIRST — the arrival order that used to bury the paths.
    history.next().resolve([
      cand({
        id: 'hist:cd x',
        providerId: 'history',
        source: 'history',
        insertText: 'cd x',
        replacement: { from: 0, to: 3 },
      }),
    ])
    await flush()
    // The path candidate lands late.
    fs.next().resolve([
      cand({
        id: 'fs:dir',
        providerId: 'fs',
        source: 'path',
        kind: 'directory',
        insertText: 'dir/',
        replacement: { from: 3, to: 3 },
      }),
    ])
    await flush()

    const rows = dropdown.root.querySelectorAll('.ui-floating-panel__row')
    expect(rows[0].textContent).toContain('fs:dir')
    expect(rows[1].textContent).toContain('cd x')
  })
})

// ── keyboard ─────────────────────────────────────────────────────────────

describe('keyboard', () => {
  const open = async (controller: CompletionController) => {
    controller.open()
    await flush()
  }

  it('Enter accepts the selected candidate into the line', async () => {
    const { editor, dropdown, controller } = rig({
      providers: [
        instantProvider('a', (ctx) => [
          cand({
            id: 'git status',
            insertText: 'git status',
            replacement: { from: 0, to: ctx.token.to },
          }),
        ]),
      ],
      editorDoc: 'git sta',
    })
    await open(controller)
    const e = key('Enter')
    expect(controller.handleKey(e)).toBe(true)
    expect(e.defaultPrevented).toBe(true)
    expect(editor.doc).toBe('git status')
    expect(dropdown.isOpen).toBe(false)
  })

  it('Enter on a stale list falls through (submits the line instead)', async () => {
    const slow = manualProvider('slow', true)
    const { editor, controller } = rig({ providers: [slow.provider] })
    controller.open()
    await flush()
    // Doc moves on before the query answers (programmatic paste path).
    editor.type('x')
    const e = key('Enter')
    expect(controller.handleKey(e)).toBe(false)
    expect(e.defaultPrevented).toBe(false)
    expect(editor.doc).toBe('git stax')
  })

  it('arrows navigate and wrap', async () => {
    const { dropdown, controller } = rig({
      providers: [
        instantProvider('a', () => [cand({ id: 'a1' }), cand({ id: 'a2' }), cand({ id: 'a3' })]),
      ],
    })
    await open(controller)
    expect(controller.handleKey(key('ArrowDown'))).toBe(true)
    expect(
      dropdown.root.querySelector('.ui-floating-panel__row[data-selected="true"]')?.textContent,
    ).toContain('a2')
    expect(controller.handleKey(key('ArrowUp'))).toBe(true)
    expect(
      dropdown.root.querySelector('.ui-floating-panel__row[data-selected="true"]')?.textContent,
    ).toContain('a1')
    // Wrap up from the top lands on the last row.
    expect(controller.handleKey(key('ArrowUp'))).toBe(true)
    expect(
      dropdown.root.querySelector('.ui-floating-panel__row[data-selected="true"]')?.textContent,
    ).toContain('a3')
  })

  it('Tab cycles to the next candidate when the dropdown is open — accept stays Enter', async () => {
    const { editor, dropdown, controller } = rig({
      providers: [
        instantProvider('a', () => [cand({ id: 'a1' }), cand({ id: 'a2' }), cand({ id: 'a3' })]),
      ],
      editorDoc: 'git sta',
    })
    await open(controller)
    // First Tab: next candidate selected, nothing inserted, dropdown stays.
    expect(controller.handleKey(key('Tab'))).toBe(true)
    expect(selectedRow(dropdown)?.textContent).toContain('a2')
    expect(editor.doc).toBe('git sta')
    expect(dropdown.isOpen).toBe(true)
    // Wrap: Pane past the last row returns to the first.
    expect(controller.handleKey(key('Tab'))).toBe(true)
    expect(selectedRow(dropdown)?.textContent).toContain('a3')
    expect(controller.handleKey(key('Tab'))).toBe(true)
    expect(selectedRow(dropdown)?.textContent).toContain('a1')
    expect(editor.doc).toBe('git sta')
  })

  // Report 1 (owner): "Type `cd`, press Tab — the panel closes instead of
  // moving to the next candidate." Not reproducible headlessly (the e2e
  // suite presses Tab against the real editor, arbiter and providers and
  // the panel stays open), and reading the controller shows no close path
  // in the Tab branch: `move` only re-renders an open list. The invariant
  // is pinned here at the seam that was in doubt — Tab on an open panel
  // NEVER closes it, and with ONE candidate (nowhere to move) the selection
  // stays put. Closing on a key that means "next" is never right.
  it('report 1: Pane on an open panel never closes it — cd + Tab + Tab, command position', async () => {
    const { editor, dropdown, controller } = rig({
      providers: [
        // The command snapshot answers `cd`; history answers one whole-line
        // row — the owner's shell shape (the fs provider is not applicable
        // in command position for a bare word).
        instantProvider('command', (ctx) =>
          ctx.doc.trim() === '' ? [] : [cand({ id: 'cmd:cd', source: 'command' })],
        ),
        instantProvider('history', (ctx) =>
          ctx.doc.trim() === ''
            ? []
            : [
                cand({
                  id: 'hist:cd /home/dev/x',
                  displayText: 'cd /home/dev/x',
                  insertText: 'cd /home/dev/x',
                  replacement: { from: 0, to: ctx.doc.length },
                  matchRanges: [{ from: 0, to: ctx.doc.length }],
                  source: 'history',
                }),
              ],
        ),
      ],
      editorDoc: '',
    })
    editor.type('c')
    controller.onDocChanged()
    editor.type('d')
    controller.onDocChanged()
    await flush()
    expect(dropdown.isOpen).toBe(false) // typing never opens the dropdown

    controller.open() // Tab #1 through the real seam
    await flush()
    expect(dropdown.isOpen).toBe(true)
    const rowsBefore = dropdown.root.querySelectorAll('.ui-floating-panel__row').length
    expect(rowsBefore).toBeGreaterThan(0)

    expect(controller.handleKey(key('Tab'))).toBe(true) // Tab #2 — consumed
    await flush()
    expect(dropdown.isOpen).toBe(true)
    expect(selectedRow(dropdown)).not.toBeNull()
  })

  it('report 1: with a single candidate Tab stays put — the panel stays open', async () => {
    const { editor, dropdown, controller } = rig({
      providers: [
        instantProvider('command', (ctx) =>
          ctx.doc.trim() === '' ? [] : [cand({ id: 'cmd:cd', source: 'command' })],
        ),
        instantProvider('history', () => []),
      ],
      editorDoc: '',
    })
    editor.type('c')
    controller.onDocChanged()
    editor.type('d')
    controller.onDocChanged()
    await flush()

    controller.open()
    await flush()
    expect(dropdown.isOpen).toBe(true)
    expect(dropdown.root.querySelectorAll('.ui-floating-panel__row')).toHaveLength(1)

    expect(controller.handleKey(key('Tab'))).toBe(true)
    await flush()
    expect(dropdown.isOpen).toBe(true)
    expect(selectedRow(dropdown)?.textContent).toContain('cd')
  })
  it('the first Tab SETTLES on the ghosted candidate — a history-first batch must not steal the selection', async () => {
    // The reported case: `cd ` shows the ghost `Downloads/`; the user presses
    // Tab to take it, and the dropdown must open with THAT row selected —
    // not move to the next folder. The regression is the arrival order: if
    // the Tab query's HISTORY batch lands before the path batch, the open
    // selection must still be the ghosted path (the old code opened on the
    // first batch and preserveSelection then kept the wrong row when the
    // paths landed).
    const history = manualProvider('history', true)
    const fs = manualProvider('fs', true)
    const dirCand = (id: string, text: string): Candidate => ({
      targetId: 'shell',
      providerId: 'fs',
      id,
      displayText: text,
      insertText: text,
      replacement: { from: 3, to: 3 },
      matchRanges: [],
      source: 'path',
      kind: 'directory',
      eligibleForGhostText: true,
    })
    const histCand = (id: string, text: string): Candidate => ({
      targetId: 'shell',
      providerId: 'history',
      id,
      displayText: text,
      insertText: text,
      replacement: { from: 0, to: 3 },
      matchRanges: [],
      source: 'history',
      eligibleForGhostText: true,
    })
    const { editor, dropdown, controller } = rig({
      providers: [history.provider, fs.provider],
      editorDoc: 'cd ',
    })
    // A typing query anchors the ghost: both batches land, the argument
    // rung puts Downloads/ on top — that is what the ghost shows.
    controller.onDocChanged()
    fs.next().resolve([dirCand('fs:Downloads', 'Downloads/'), dirCand('fs:go', 'go/')])
    history.next().resolve([histCand('hist:cd Downloads', 'cd Downloads')])
    await flush()
    expect(dropdown.isOpen).toBe(false) // typing never opens the panel

    // Tab, through the real seam: the arbiter declines while closed, and
    // the editor's onTab calls open() (terminal-content.ts wiring).
    expect(controller.handleKey(key('Tab'))).toBe(false)
    controller.open()
    // The Tab query's HISTORY batch lands first — the arrival order that
    // used to open the panel on a whole-line row.
    history.next().resolve([histCand('hist:cd Downloads', 'cd Downloads')])
    await flush()
    // The dropdown opened with the ghosted PATH candidate selected — not
    // the history row, and not advanced to the next folder.
    expect(dropdown.isOpen).toBe(true)
    expect(selectedRow(dropdown)?.textContent).toContain('Downloads/')
    expect(editor.doc).toBe('cd ') // nothing inserted, only selected

    // The late path batch lands; the selection stays on the ghosted row.
    fs.next().resolve([dirCand('fs:Downloads', 'Downloads/'), dirCand('fs:go', 'go/')])
    await flush()
    expect(selectedRow(dropdown)?.textContent).toContain('Downloads/')

    // The SECOND Tab moves — the first one settled, the ones after cycle.
    expect(controller.handleKey(key('Tab'))).toBe(true)
    expect(selectedRow(dropdown)?.textContent).toContain('go/')
  })

  it('Shift+Tab cycles back', async () => {
    const { dropdown, controller } = rig({
      providers: [
        instantProvider('a', () => [cand({ id: 'a1' }), cand({ id: 'a2' }), cand({ id: 'a3' })]),
      ],
    })
    await open(controller)
    // From the top, Shift+Tab wraps to the last row.
    expect(controller.handleKey(key('Tab', { shiftKey: true }))).toBe(true)
    expect(selectedRow(dropdown)?.textContent).toContain('a3')
    expect(controller.handleKey(key('Tab', { shiftKey: true }))).toBe(true)
    expect(selectedRow(dropdown)?.textContent).toContain('a2')
  })

  it('Escape closes exactly the dropdown — one surface per press', async () => {
    const { editor, dropdown, controller } = rig({
      providers: [instantProvider('a', () => [cand({ id: 'x' })])],
    })
    await open(controller)
    const e = key('Escape')
    expect(controller.handleKey(e)).toBe(true)
    expect(dropdown.isOpen).toBe(false)
    expect(editor.doc).toBe('git sta') // the draft is untouched
  })

  it('the empty row is never selectable: arrows pass through, Enter submits, Esc closes, Tab re-asks', async () => {
    const { editor, dropdown, controller } = rig({
      providers: [emptyProvider('a')],
    })
    await open(controller)
    expect(emptyRow(dropdown)).not.toBeNull()

    // Arrows are not consumed — nothing to navigate, the caret moves.
    const down = key('ArrowDown')
    expect(controller.handleKey(down)).toBe(false)
    expect(down.defaultPrevented).toBe(false)
    // No row ever carries the selected variance.
    expect(selectedRow(dropdown)).toBeNull()

    // Enter is not consumed — it falls through to the editor's submit.
    const enter = key('Enter')
    expect(controller.handleKey(enter)).toBe(false)
    expect(enter.defaultPrevented).toBe(false)

    // Esc closes exactly the panel.
    const esc = key('Escape')
    expect(controller.handleKey(esc)).toBe(true)
    expect(dropdown.isOpen).toBe(false)

    // A Tab with the panel closed re-asks (falls through to the editor's
    // onTab, which calls open() again) — it is not swallowed as a cycle.
    const tab = key('Tab')
    expect(controller.handleKey(tab)).toBe(false)
    expect(editor.doc).toBe('git sta')
  })

  it('a plain key falls through so the keystroke can re-query', async () => {
    const { controller } = rig({
      providers: [instantProvider('a', () => [cand({ id: 'x' })])],
    })
    await open(controller)
    expect(controller.handleKey(key('t'))).toBe(false)
  })

  it('recall open: the dropdown dismisses and never consumes', () => {
    const { dropdown, controller } = rig({
      providers: [instantProvider('a', () => [cand({ id: 'x' })])],
      recallIsOpen: () => true,
    })
    controller.open()
    // Even with candidates queued, recall owns the surface.
    expect(controller.handleKey(key('ArrowDown'))).toBe(false)
    expect(dropdown.isOpen).toBe(false)
  })
})

describe('ownsArrows — the bare-arrow ownership decision (nocx-mlm7)', () => {
  const open = async (controller: CompletionController) => {
    controller.open()
    await flush()
  }

  it('answers true for a bare ArrowUp/ArrowDown while the dropdown is open with a selectable list', async () => {
    const { controller } = rig({
      providers: [instantProvider('a', () => [cand({ id: 'a1' }), cand({ id: 'a2' })])],
    })
    await open(controller)
    expect(controller.ownsArrows(key('ArrowDown'))).toBe(true)
    expect(controller.ownsArrows(key('ArrowUp'))).toBe(true)
  })

  it('answers false when the dropdown is closed, showing the empty row, or the key is modified', async () => {
    const { controller } = rig({
      providers: [instantProvider('a', () => [cand({ id: 'a1' }), cand({ id: 'a2' })])],
    })
    // Closed: recall's bare-Up gesture owns the key (up at the top of a
    // single-line draft opens recall — the arbiter's tail).
    expect(controller.ownsArrows(key('ArrowUp'))).toBe(false)

    await open(controller)
    // Modified arrows are not the dropdown's navigation keys (shift+Up is
    // recall's widen; chorded arrows are editor shortcuts).
    expect(controller.ownsArrows(key('ArrowUp', { shiftKey: true }))).toBe(false)
    expect(controller.ownsArrows(key('ArrowUp', { ctrlKey: true }))).toBe(false)
    // Other keys are not arrows.
    expect(controller.ownsArrows(key('Enter'))).toBe(false)
  })

  it('answers false for the empty row — it owns nothing, by its own contract', async () => {
    const { controller } = rig({
      providers: [emptyProvider('a')],
    })
    await open(controller)
    expect(controller.ownsArrows(key('ArrowDown'))).toBe(false)
    expect(controller.ownsArrows(key('ArrowUp'))).toBe(false)
  })
})

// ── ghost text ───────────────────────────────────────────────────────────

// The ghost and the dropdown row are two renderings of ONE candidate, so the
// contract is not "they look alike" but "typed + ghost is exactly what accept
// inserts". Stated as the invariant rather than as the `~` case, because the
// case is only the cheapest way to violate it.
describe('ghost tail', () => {
  it('typed + ghost is always exactly the insertion', () => {
    for (const [insert, typed] of [
      ['Documents/', 'Doc'],
      ['git status', 'git sta'],
      ['repos/meshynet/bin/', 'repos/mesh'],
      ['Downloads/', ''],
    ] as const) {
      const tail = ghostTail(insert, typed, typed === '' ? ' ' : typed.slice(-1))
      expect(typed + (tail ?? '')).toBe(insert)
    }
  })

  it('declines rather than overlapping what is on screen', () => {
    // The owner's `cd ~ocuments/`: the typed text is not a prefix of the
    // insertion, so slicing by length drew over the `D`.
    expect(ghostTail('Documents/', '~', '~')).toBeNull()
    // Same lie, one letter smaller: a case-insensitive match.
    expect(ghostTail('Documents/', 'doc', 'c')).toBeNull()
    // Nothing left to add is nothing to draw.
    expect(ghostTail('Documents/', 'Documents/', '/')).toBeNull()
  })

  it('an empty token previews at a word start, never after a closing quote', () => {
    // `cd ` — the case the owner asked for.
    expect(ghostTail('Downloads/', '', ' ')).toBe('Downloads/')
    // Start of the line: nothing before the caret at all.
    expect(ghostTail('Downloads/', '', '')).toBe('Downloads/')
    // The pasted `-d '{…}'`: the cwd listing must not attach itself to the
    // end of a JSON body.
    expect(ghostTail('Downloads/', '', "'")).toBeNull()
    expect(ghostTail('Downloads/', '', ')')).toBeNull()
  })
})

describe('ghost text', () => {
  const ghostEditor = (doc: string) => {
    const e = new FakeEditor(doc)
    return e
  }

  it('Right at the line end accepts the top-ranked candidate', async () => {
    const editor = ghostEditor('git sta')
    const container = document.createElement('div')
    document.body.appendChild(container)
    const dropdown = new CompletionDropdown({ onHover: () => {}, onPick: () => {} })
    const controller = new CompletionController({
      providers: [
        instantProvider('h', () => [
          cand({
            id: 'hist:git status',
            insertText: 'git status',
            replacement: { from: 0, to: 7 },
          }),
        ]),
      ],
      dropdown,
      env: () => ({ isLocal: true, cwd: '/repo', host: '' }),
      now: () => 1_750_000_000_000,
    })
    controller.attach(editor, container)
    controller.onDocChanged()
    await flush()

    const e = key('ArrowRight')
    expect(controller.handleKey(e)).toBe(true)
    expect(editor.doc).toBe('git status')
    expect(dropdown.isOpen).toBe(false)
  })

  // The owner's report: with `cd Downloads/` ghosted, Right inserted it and
  // the panel shut. A directory is not an answer, it is a step — the walk
  // must continue where it was, not have to be restarted with another Tab.
  it('accepting a directory continues the walk; accepting a file ends it', async () => {
    const mk = async (kind: 'directory' | 'file', insertText: string) => {
      const editor = ghostEditor('cd Down')
      const container = document.createElement('div')
      document.body.appendChild(container)
      const dropdown = new CompletionDropdown({ onHover: () => {}, onPick: () => {} })
      const controller = new CompletionController({
        providers: [
          instantProvider('p', () => [
            cand({ id: `path:${insertText}`, insertText, kind, replacement: { from: 3, to: 7 } }),
          ]),
        ],
        dropdown,
        env: () => ({ isLocal: true, cwd: '/repo', host: '' }),
        now: () => 1_750_000_000_000,
      })
      controller.attach(editor, container)
      controller.onDocChanged()
      await flush()
      expect(controller.handleKey(key('ArrowRight'))).toBe(true)
      await flush()
      return { editor, dropdown }
    }

    const dir = await mk('directory', 'Downloads/')
    expect(dir.editor.doc).toBe('cd Downloads/')
    expect(dir.dropdown.isOpen).toBe(true)

    const file = await mk('file', 'Downloads')
    expect(file.editor.doc).toBe('cd Downloads')
    expect(file.dropdown.isOpen).toBe(false)
  })

  it('Enter takes the directory and STOPS; Right takes it and keeps walking', async () => {
    // Two keys took the row and did exactly the same thing, while the footer
    // named them apart. They are apart now, and the reason is that a walk
    // needs a way out that is not Escape: Enter ends it with the path in the
    // line, and the next Enter runs the command — the same shape the recall
    // overlay has (first Enter takes, second runs).
    const mk = async (press: 'Enter' | 'ArrowRight') => {
      const editor = ghostEditor('cd Down')
      const container = document.createElement('div')
      document.body.appendChild(container)
      const dropdown = new CompletionDropdown({ onHover: () => {}, onPick: () => {} })
      const controller = new CompletionController({
        providers: [
          // Two rows, so the list is up and Enter has something to accept —
          // a single match is taken by Tab before a list ever opens.
          instantProvider('p', () => [
            cand({
              id: 'path:Downloads/',
              insertText: 'Downloads/',
              kind: 'directory',
              replacement: { from: 3, to: 7 },
            }),
            cand({
              id: 'path:Downstream/',
              insertText: 'Downstream/',
              kind: 'directory',
              replacement: { from: 3, to: 7 },
            }),
          ]),
        ],
        dropdown,
        env: () => ({ isLocal: true, cwd: '/repo', host: '' }),
        now: () => 1_750_000_000_000,
      })
      controller.attach(editor, container)
      controller.onDocChanged()
      await flush()
      if (press === 'Enter') {
        // Enter is the list's key, and the list is opened by Tab — a typing
        // query only ghosts (the panel must not flash open under the
        // fingers). `open()` is what the editor's Tab calls.
        controller.open()
        await flush()
      }
      expect(controller.handleKey(key(press))).toBe(true)
      await flush()
      return { editor, dropdown }
    }

    const walked = await mk('ArrowRight')
    expect(walked.editor.doc).toBe('cd Downloads/')
    expect(walked.dropdown.isOpen).toBe(true)

    const stopped = await mk('Enter')
    expect(stopped.editor.doc).toBe('cd Downloads/')
    expect(stopped.dropdown.isOpen).toBe(false)
  })

  it('End accepts at line end, but stays a caret movement mid-line', async () => {
    const editor = ghostEditor('git sta')
    const container = document.createElement('div')
    document.body.appendChild(container)
    const dropdown = new CompletionDropdown({ onHover: () => {}, onPick: () => {} })
    const controller = new CompletionController({
      providers: [
        instantProvider('h', () => [
          cand({
            id: 'hist:git status',
            insertText: 'git status',
            replacement: { from: 0, to: 7 },
          }),
        ]),
      ],
      dropdown,
      env: () => ({ isLocal: true, cwd: '/repo', host: '' }),
      now: () => 1_750_000_000_000,
    })
    controller.attach(editor, container)
    controller.onDocChanged()
    await flush()

    // Mid-line: the caret is not at the end of the line, so End must move
    // the caret, not accept.
    editor.doc = 'git sta and more'
    editor.caret = 7
    controller.onDocChanged()
    await flush()
    const e = key('End')
    expect(controller.handleKey(e)).toBe(false)
    expect(editor.doc).toBe('git sta and more')
  })

  it('a stale async suggestion is discarded, never applied', async () => {
    const slow = manualProvider('slow', true)
    const editor = ghostEditor('git')
    const container = document.createElement('div')
    document.body.appendChild(container)
    const dropdown = new CompletionDropdown({ onHover: () => {}, onPick: () => {} })
    const controller = new CompletionController({
      providers: [slow.provider],
      dropdown,
      env: () => ({ isLocal: true, cwd: '/repo', host: '' }),
      now: () => 1_750_000_000_000,
    })
    controller.attach(editor, container)
    controller.onDocChanged()
    await flush()

    // The user types more before the suggestion lands.
    editor.type('x')
    controller.onDocChanged()
    await flush()

    slow.next().resolve([cand({ id: 'stale', replacement: { from: 0, to: 3 } })])
    await flush()
    expect(controller.handleKey(key('ArrowRight'))).toBe(false)
    expect(editor.doc).toBe('gitx')
  })

  it('an entry marked sensitive is never eligible for ghost text', async () => {
    const editor = ghostEditor('secret ')
    editor.caret = 7
    const container = document.createElement('div')
    document.body.appendChild(container)
    const dropdown = new CompletionDropdown({ onHover: () => {}, onPick: () => {} })
    const controller = new CompletionController({
      providers: [
        instantProvider('h', () => [
          cand({
            id: 's',
            insertText: 'sensitive',
            replacement: { from: 0, to: 7 },
            eligibleForGhostText: false,
          }),
        ]),
      ],
      dropdown,
      env: () => ({ isLocal: true, cwd: '/repo', host: '' }),
      now: () => 1_750_000_000_000,
    })
    controller.attach(editor, container)
    controller.onDocChanged()
    await flush()
    expect(controller.handleKey(key('ArrowRight'))).toBe(false)
    expect(editor.doc).toBe('secret ')
  })

  it('Right with a non-empty selection never accepts', async () => {
    const editor = ghostEditor('git sta')
    const container = document.createElement('div')
    document.body.appendChild(container)
    const dropdown = new CompletionDropdown({ onHover: () => {}, onPick: () => {} })
    const controller = new CompletionController({
      providers: [
        instantProvider('h', () => [
          cand({
            id: 'hist:git status',
            insertText: 'git status',
            replacement: { from: 0, to: 7 },
          }),
        ]),
      ],
      dropdown,
      env: () => ({ isLocal: true, cwd: '/repo', host: '' }),
      now: () => 1_750_000_000_000,
    })
    controller.attach(editor, container)
    controller.onDocChanged()
    await flush()
    // A mouse selection: the ghost precondition (empty selection) fails.
    editor.caret = 7
    const fakeSel = { from: 2, to: 7 }
    const origGet = editor.getSelection.bind(editor)
    editor.getSelection = () => fakeSel
    expect(controller.handleKey(key('ArrowRight'))).toBe(false)
    editor.getSelection = origGet
  })
})

describe('ghostAcceptable — the ONE rule behind draw and accept (nocx-mlm7)', () => {
  const base = {
    candidate: cand({
      id: 'hist:git status',
      insertText: 'git status',
      replacement: { from: 0, to: 7 },
    }),
    boxQueryDoc: 'git sta',
    queryDoc: 'git sta',
    doc: 'git sta',
    caret: 7,
    selectionEmpty: true,
  }

  it('accepts when every §8.7 precondition holds', () => {
    expect(ghostAcceptable(base)).toEqual({ ok: true })
  })

  it('the check the accept path alone used to make is IN the rule: a box matching the live doc but not the current query is refused (query-moved)', () => {
    // The drift the render path had drifted away from: box.queryDoc ===
    // view doc (the draw path's only revision check) while !== the
    // controller's queryDoc (canAcceptGhost's extra check) — so a ghost was
    // drawn that Right silently refused. One predicate puts the check in
    // both paths; the draw path must refuse exactly where accept refuses.
    expect(ghostAcceptable({ ...base, queryDoc: 'git status' })).toEqual({
      ok: false,
      condition: 'query-moved',
    })
  })

  it('names every failing condition — the trace says WHY a ghost accept was refused', () => {
    expect(ghostAcceptable({ ...base, candidate: null })).toEqual({
      ok: false,
      condition: 'no-candidate',
    })
    expect(
      ghostAcceptable({ ...base, candidate: cand({ id: 's', eligibleForGhostText: false }) }),
    ).toEqual({ ok: false, condition: 'not-ghost-eligible' })
    expect(ghostAcceptable({ ...base, doc: 'git stax' })).toEqual({
      ok: false,
      condition: 'doc-changed',
    })
    expect(ghostAcceptable({ ...base, selectionEmpty: false })).toEqual({
      ok: false,
      condition: 'selection-nonempty',
    })
    expect(ghostAcceptable({ ...base, caret: 3 })).toEqual({
      ok: false,
      condition: 'caret-off-replacement',
    })
    // The whole line is the query doc; the caret sits at the token end,
    // mid-line — Right would be a caret movement, never an accept.
    expect(
      ghostAcceptable({
        ...base,
        boxQueryDoc: 'git sta and more',
        queryDoc: 'git sta and more',
        doc: 'git sta and more',
        caret: 7,
      }),
    ).toEqual({ ok: false, condition: 'mid-line' })
  })
})

describe('ghost draw and accept are one rule (nocx-mlm7)', () => {
  // The draw path needs a real CM6 view (the ghost ViewPlugin), so this
  // harness mounts the controller's extensions into a real CommandEditor —
  // the same shape editor.test.ts uses. The editor seam and the view are
  // the SAME document here, which is what makes draw ⇔ accept comparable.
  const rigView = () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const dropdown = new CompletionDropdown({ onHover: () => {}, onPick: () => {} })
    const controller = new CompletionController({
      providers: [
        instantProvider('h', () => [
          cand({
            id: 'hist:git status',
            insertText: 'git status',
            replacement: { from: 0, to: 7 },
          }),
        ]),
      ],
      dropdown,
      env: () => ({ isLocal: true, cwd: '/repo', host: '' }),
      latencyBudgetMs: 0,
      now: () => 1_750_000_000_000,
    })
    const ed = new CommandEditor(
      { submit: vi.fn(), cancel: vi.fn(), onTab: () => controller.open() },
      controller.extensions(),
    )
    ed.mount(container)
    controller.attach(ed, container)
    ed.setKeyArbiter((e) => controller.handleKey(e))
    ed.show()
    return { ed, controller, dropdown, container, view: viewOf(ed) }
  }

  it('a drawn ghost is exactly what Right accepts — the draw and accept paths consult the same predicate', async () => {
    const { ed, controller, view } = rigView()
    ed.insertText('git sta')
    controller.onDocChanged()
    await flush()
    const ghost = () => view.contentDOM.querySelector('.nocx-editor-ghost')
    expect(ghost()).not.toBeNull()
    expect(ghost()?.textContent).toBe('tus')
    // The key the ghost advertises takes exactly what is drawn.
    keyOn(view, { key: 'ArrowRight' })
    expect(ed.getDoc()).toBe('git status')
  })

  it('a ghost that Right/End would refuse is never drawn — the mid-line caret draws nothing and accepts nothing', async () => {
    const { ed, controller, view } = rigView()
    ed.insertText('git sta')
    // The caret sits MID-LINE before the query: the shared predicate
    // refuses (caret !== replacement.to), so nothing may draw and Right
    // must fall through to the editor's caret movement, never insert.
    view.dispatch({ selection: { anchor: 3 } })
    controller.onDocChanged()
    await flush()
    expect(view.contentDOM.querySelector('.nocx-editor-ghost')).toBeNull()
    keyOn(view, { key: 'ArrowRight' })
    expect(ed.getDoc()).toBe('git sta')
  })
})

describe('ghost refusal tracing (nocx-mlm7)', () => {
  it('a refused ghost accept names the failing condition — and only when decision tracing is on', async () => {
    const editor = new FakeEditor('git sta')
    const container = document.createElement('div')
    document.body.appendChild(container)
    const dropdown = new CompletionDropdown({ onHover: () => {}, onPick: () => {} })
    const controller = new CompletionController({
      providers: [
        instantProvider('h', () => [
          cand({
            id: 'hist:git status',
            insertText: 'git status',
            replacement: { from: 0, to: 7 },
          }),
        ]),
      ],
      dropdown,
      env: () => ({ isLocal: true, cwd: '/repo', host: '' }),
      now: () => 1_750_000_000_000,
    })
    controller.attach(editor, container)
    controller.onDocChanged()
    await flush()
    const debug = vi.spyOn(console, 'debug').mockImplementation(() => {})
    try {
      // Tracing is OFF by default: the refusal emits nothing at all — the
      // per-keystroke hot-path guarantee.
      editor.caret = 3
      expect(controller.handleKey(key('ArrowRight'))).toBe(false)
      expect(debug).not.toHaveBeenCalled()
      // Tracing ON: the trace names the condition that refused the accept.
      setDecisionTracing(true)
      controller.handleKey(key('ArrowRight'))
      const calls = debug.mock.calls.map((c) => c.join(' '))
      expect(
        calls.some(
          (c) => c.includes('nocx:decide ghost-refused') && c.includes('caret-off-replacement'),
        ),
      ).toBe(true)
    } finally {
      setDecisionTracing(false)
      debug.mockRestore()
    }
  })
})

// The budget constant is exported and used by the wiring.
void LATENCY_BUDGET_MS

describe('accepting a snippet row (nocx-nlhe)', () => {
  const snippetCandidate = (over: Partial<Candidate> = {}): Candidate =>
    cand({
      id: 'snippet:a',
      providerId: 'snippet',
      source: 'snippet',
      snippetId: 'a',
      displayText: 'deploy',
      insertText: 'deploy',
      eligibleForGhostText: false,
      replacement: { from: 4, to: 7 },
      ...over,
    })

  const snippetProviderStub = (c: Candidate = snippetCandidate()): SuggestionProvider => ({
    id: 'snippet',
    targetId: 'shell',
    applicable: () => true,
    suggest: () => ({ candidates: [c] }),
  })

  it('does NOT insert the row text: the body is resolved by the accept seam', async () => {
    const accepted: string[] = []
    const { editor, controller } = rig({
      providers: [snippetProviderStub()],
      editorDoc: 'git dep',
      acceptSnippet: (id) => accepted.push(id),
    })
    controller.open()
    await flush()
    controller.handleKey(key('Enter'))

    // The token is cleared and NOTHING else is written here — the resolved
    // text arrives through the fire path, which reads cwd and branch at
    // that moment and asks for any ask: fields (design §8, §10.2).
    expect(editor.applied).toEqual([{ from: 4, to: 7, text: '' }])
    expect(accepted).toEqual(['a'])
  })

  it('with no accept seam wired it inserts nothing at all, rather than the title', async () => {
    // A half-wired build must not put the row's LABEL into the line: the
    // title is not the phrase, and a person would submit it.
    const { editor, controller } = rig({
      providers: [snippetProviderStub()],
      editorDoc: 'git dep',
    })
    controller.open()
    await flush()
    controller.handleKey(key('Enter'))

    expect(editor.applied).toEqual([])
  })

  it('Tab does not auto-apply a sole snippet candidate', async () => {
    // The unique-completion path finishes a WORD. A snippet is a whole
    // phrase behind a title, and applying one because it was the only row
    // would fire a saved command the person never chose.
    const accepted: string[] = []
    const { editor, controller, dropdown } = rig({
      providers: [snippetProviderStub()],
      editorDoc: 'git dep',
      acceptSnippet: (id) => accepted.push(id),
    })
    // open() IS the Tab path: it is what the editor's keymap calls, and it
    // is where the unique-completion shortcut lives.
    controller.open()
    await flush()
    controller.open()
    await flush()

    expect(editor.applied).toEqual([])
    expect(accepted).toEqual([])
    // It stays offered — the row is still there to be chosen deliberately.
    expect(dropdown.root.querySelectorAll('.ui-floating-panel__row').length).toBe(1)
  })
})
