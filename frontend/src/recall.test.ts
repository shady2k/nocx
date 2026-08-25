// @vitest-environment jsdom
// Recall overlay (design §8.10): the history palette above the prompt —
// oldest at the top, newest at the bottom, the newest row selected on open,
// so the first Up gives the command you just ran. Written as a user reaching
// the feature — the editor is real, the keys land on it, and the overlay is
// wired through the same arbiter the terminal uses. The rule (brief
// nocx-w7h.5 reversed the v4 one): navigating previews the selected command
// INTO the editor, and Enter executes what you can see through the editor's
// own submit path — the ordinary "type a command and press Enter", with the
// typing done for you. Esc restores the draft, caret and scroll exactly; Up
// is caret movement first.
//
// The query crosses the control plane (nocx-rtg0.13): open() is async, so
// every test that opens the panel waits for the observable it asserts — the
// panel's own settled content — never for a duration.
import { describe, it, expect, vi } from 'vitest'
import { EditorView, keymap } from '@codemirror/view'
import { defaultKeymap } from '@codemirror/commands'
import { CommandEditor, type EditorActions } from './editor'
import {
  RecallOverlay,
  formatDuration,
  queryLedgerHistory,
  relativeTime,
  withSessionText,
  scrollTopToReveal,
  type RecallQuery,
  type RecallScope,
} from './recall'
import { CommandLedger } from './command-ledger'
import type { HistoryEntry, HistoryQuery } from './generated/history.query'

const viewOf = (ed: CommandEditor): EditorView => (ed as unknown as { view: EditorView }).view

/** Dispatch a keydown exactly where a user's keystroke lands. Returns the
 *  event so tests can observe whether the handler consumed it. */
const key = (view: EditorView, init: KeyboardEventInit) => {
  const ev = new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init })
  view.contentDOM.dispatchEvent(ev)
  return ev
}

/** Wait until the overlay has settled out of its loading state: the answer
 *  to the opening rung (and any open-time climb) landed and the panel
 *  rendered. Waits on the panel's own content — the loading state is the
 *  '…' count — so the wait is for the observable, not for a duration. */
async function settled(container: HTMLElement): Promise<void> {
  await vi.waitFor(() => {
    const text = panelOf(container).textContent ?? ''
    expect(text).not.toContain('…')
  })
}

// Entries carry wall-clock epoch milliseconds (the units the store
// persists and the panel renders against — nocx-rtg0.16).
function mkEntry(command: string, endedAt: number | null = 1_750_000_000_000): HistoryEntry {
  return {
    id: `${command}-${endedAt}`,
    command,
    cwd: '~',
    host: '',
    status: 'success',
    maskedCount: 0,
    maskedKinds: [],
    endedAt,
  }
}

function mkQuery(
  commands: string[],
  source: 'store' | 'session' = 'session',
): (scope: RecallScope) => Promise<HistoryQuery> {
  return (scope) =>
    Promise.resolve({
      entries: commands.map((c, i) => mkEntry(c, 1000 - i)),
      scope,
      exhausted: true,
      source,
      // The session/ledger answer states its own horizon: the oldest entry.
      coverage: commands.length > 0 ? 1000 - (commands.length - 1) : null,
    })
}

function emptyQuery(
  source: HistoryQuery['source'] = 'session',
): (scope: RecallScope) => Promise<HistoryQuery> {
  return (scope) => Promise.resolve({ entries: [], scope, exhausted: true, source, coverage: null })
}

/** A query that actually narrows on `text`, the way the store does — for the
 *  typing-narrows tests, where a static fixture would ignore the filter. */
function filteringQuery(
  commands: string[],
  source: HistoryQuery['source'] = 'session',
): (scope: RecallScope, text?: string) => Promise<HistoryQuery> {
  return (scope, text) => {
    const needle = text ?? ''
    const matched = commands.filter((c) => c.toLowerCase().includes(needle.toLowerCase()))
    return Promise.resolve({
      entries: matched.map((c, i) => mkEntry(c, 1000 - i)),
      scope,
      exhausted: true,
      source,
      coverage: matched.length > 0 ? 1000 - (matched.length - 1) : null,
    })
  }
}

function setupRecall(opts: { query?: RecallQuery; actions?: Partial<EditorActions> }) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const submit = opts.actions?.submit ?? vi.fn()
  const actions: EditorActions = { submit, cancel: vi.fn(), ...opts.actions }
  const ed = new CommandEditor(actions, [keymap.of([...defaultKeymap])])
  ed.mount(container)
  const view = viewOf(ed)
  const recall = new RecallOverlay({ editor: ed, query: opts.query ?? emptyQuery() })
  recall.mount(ed.root)
  ed.setKeyArbiter((e) => recall.handleKey(e))
  // The terminal-content wiring: Up at the top of a draft opens recall.
  actions.onUpAtTop = () => {
    void recall.open('directory')
  }
  return { container, ed, view, recall, submit }
}

const panelOf = (container: HTMLElement): HTMLElement => {
  const p = container.querySelector<HTMLElement>('.ui-floating-panel[data-variant="recall"]')
  if (!p) throw new Error('recall panel not mounted')
  return p
}

/** The query the search field carries — the field's text (the caret is
 *  aria-hidden and carries no text, so textContent is the value). */
const fieldValue = (container: HTMLElement): string =>
  container.querySelector<HTMLElement>('.ui-floating-panel__search .ui-search-field__input')
    ?.textContent ?? ''

describe('recall: Enter takes the command, it does not run it', () => {
  it('Enter while navigating leaves the command in the line and submits nothing', async () => {
    // Decided twice: v4 said take-never-run, nocx-w7h.5 made the
    // empty-filter Enter execute the previewed command, and the owner
    // reversed it back on 2026-08-19 while using it. Choosing from a list
    // and running are two decisions, and one keystroke must not make both.
    const { container, ed, view, recall, submit } = setupRecall({
      query: mkQuery(['rm -rf build']),
    })
    ed.show()

    // Empty draft, caret at top: Up opens recall; the row is previewed.
    key(view, { key: 'ArrowUp' })
    await settled(container)
    expect(recall.isOpen).toBe(true)
    expect(ed.getDoc()).toBe('rm -rf build')

    key(view, { key: 'Enter' })
    expect(recall.isOpen).toBe(false)
    expect(ed.getDoc()).toBe('rm -rf build')
    expect(submit).not.toHaveBeenCalled()
  })

  it('previewing a MASKED row reports its redaction spans to the host', async () => {
    // ADR-0021's named consequence, made structural this round: the durable
    // text is the masked one, so `curl -H "Bearer sk-p...7890"` looks real
    // and cannot work. The overlay's job is to REPORT the row's redaction
    // spans every time it places text in the editor; the refusal lives in
    // the host's beforeSubmit seam (the composition wires it), so a
    // recalled masked command never reaches the submit path there.
    const onDocContent = vi.fn()
    const container = document.createElement('div')
    document.body.appendChild(container)
    const submit = vi.fn()
    const ed = new CommandEditor({ submit, cancel: vi.fn() }, [keymap.of([...defaultKeymap])])
    ed.mount(container)
    const view = viewOf(ed)
    const masked: HistoryEntry = {
      ...mkEntry('curl -H "Bearer sk-p...7890"'),
      maskedCount: 1,
      redactions: [{ kind: 'openai', start: 16, end: 27, prefix: 'sk-p', suffix: '7890' }],
    }
    const recall = new RecallOverlay({
      editor: ed,
      query: () =>
        Promise.resolve({
          entries: [masked],
          scope: 'directory' as const,
          exhausted: true,
          source: 'store' as const,
          coverage: null,
        }),
      onDocContent,
    })
    recall.mount(ed.root)
    ed.setKeyArbiter((e) => recall.handleKey(e))
    ed.show()
    await recall.open('directory')
    await settled(container)

    // The preview placed the masked command AND reported its spans — the
    // host registers them as unresolved chips and refuses to submit.
    expect(onDocContent).toHaveBeenCalledWith('curl -H "Bearer sk-p...7890"', [
      { kind: 'openai', start: 16, end: 27, prefix: 'sk-p', suffix: '7890' },
    ])
    expect(ed.getDoc()).toBe('curl -H "Bearer sk-p...7890"')

    // Esc restores the draft and clears the spans (the draft is the user's
    // own text — nothing is unresolved in it).
    key(view, { key: 'Escape' })
    expect(recall.isOpen).toBe(false)
    expect(onDocContent).toHaveBeenLastCalledWith('', [])
    expect(submit).not.toHaveBeenCalled()
  })

  it('a MASKED row entered through the filter inserts and reports its spans', async () => {
    const onDocContent = vi.fn()
    const container = document.createElement('div')
    document.body.appendChild(container)
    const submit = vi.fn()
    const ed = new CommandEditor({ submit, cancel: vi.fn() }, [keymap.of([...defaultKeymap])])
    ed.mount(container)
    const masked: HistoryEntry = {
      ...mkEntry('curl -H "Bearer sk-p...7890"'),
      maskedCount: 1,
      redactions: [{ kind: 'openai', start: 16, end: 27, prefix: 'sk-p', suffix: '7890' }],
    }
    const recall = new RecallOverlay({
      editor: ed,
      query: (scope, text) =>
        Promise.resolve({
          entries: text ? [masked] : [],
          scope,
          exhausted: true,
          source: 'store' as const,
          coverage: null,
        }),
      onDocContent,
    })
    recall.mount(ed.root)
    ed.setKeyArbiter((e) => recall.handleKey(e))
    ed.show()
    await recall.open('directory')
    await settled(container)

    // The typed search hands the input to the field; Enter inserts the row
    // (never executes) and reports the spans.
    for (const ch of 'curl') key(viewOf(ed), { key: ch })
    // The filter's answer is async: wait for the row to render, then the
    // Enter INSERTS (never executes) and reports the spans.
    await vi.waitFor(() => {
      expect(panelOf(container).querySelectorAll('.ui-collection-row').length).toBe(1)
    })
    key(viewOf(ed), { key: 'Enter' })
    await settled(container)
    expect(submit).not.toHaveBeenCalled()
    expect(ed.getDoc()).toBe('curl -H "Bearer sk-p...7890"')
    expect(onDocContent).toHaveBeenCalledWith('curl -H "Bearer sk-p...7890"', [
      { kind: 'openai', start: 16, end: 27, prefix: 'sk-p', suffix: '7890' },
    ])
  })

  it('Esc after previewing restores the draft and sends nothing', async () => {
    const { container, ed, view, recall, submit } = setupRecall({
      query: mkQuery(['rm -rf build']),
    })
    ed.show()
    ed.insertText('git s')
    key(view, { key: 'ArrowUp' })
    await settled(container)
    expect(ed.getDoc()).toBe('rm -rf build') // previewed

    key(view, { key: 'Escape' })
    expect(recall.isOpen).toBe(false)
    expect(ed.getDoc()).toBe('git s') // the draft, not the preview, is back
    expect(submit).not.toHaveBeenCalled() // and nothing was sent
  })

  it('Tab takes the command into the line, runs nothing, and is not passed on', async () => {
    // The third exit finally has a key. It also has to be CONSUMED: left to
    // fall through it reached the editor and the completion dropdown opened
    // over the recalled command, offering to complete a directory inside it.
    const { container, ed, view, recall, submit } = setupRecall({
      query: mkQuery(['rm -rf build']),
    })
    ed.show()
    ed.insertText('git s')
    key(view, { key: 'ArrowUp' })
    await settled(container)

    const consumed = recall.handleKey(
      new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }),
    )
    expect(consumed).toBe(true)
    expect(recall.isOpen).toBe(false)
    // The recalled command is the new draft — Esc is the key that puts a
    // draft back, and it is deliberately the only one.
    expect(ed.getDoc()).toBe('rm -rf build')
    expect(submit).not.toHaveBeenCalled()
  })

  it('typing while navigating narrows the filter — the panel owns printable keys', async () => {
    const { container, ed, view, recall } = setupRecall({ query: mkQuery(['docker compose up']) })
    ed.show()
    ed.insertText('git s')
    key(view, { key: 'ArrowUp' })
    await settled(container)
    expect(recall.isOpen).toBe(true)
    expect(ed.getDoc()).toBe('docker compose up') // previewed
    // A printable key is the filter (nocx-ms7v), never a keystroke for the
    // editor under the panel: it is consumed, the overlay stays up, and the
    // field carries the needle.
    const ev = key(view, { key: 'd' })
    expect(ev.defaultPrevented).toBe(true)
    expect(recall.isOpen).toBe(true)
    expect(fieldValue(container)).toBe('d')
    // A non-empty filter hands the input to the field (brief search-ui §3):
    // the row is highlighted but NOT previewed, so the line keeps the draft
    // — the screen must not hold the query and somebody else's command at
    // once. The draft is restored when the narrowed answer lands (async,
    // like every re-query); the preview returns when the filter clears.
    await vi.waitFor(() => expect(ed.getDoc()).toBe('git s'))
  })

  it('deleting while navigating keeps the previewed command as the new draft', async () => {
    const { container, ed, view, recall } = setupRecall({ query: mkQuery(['docker compose up']) })
    ed.show()
    key(view, { key: 'ArrowUp' })
    await settled(container)
    expect(ed.getDoc()).toBe('docker compose up')
    // Backspace lands ON the preview (CM6 runs its deletion on the keydown,
    // unlike text insertion which jsdom cannot synthesize): the overlay is
    // gone and the kept command carries the edit — 'docker compose u'.
    key(view, { key: 'Backspace' })
    expect(recall.isOpen).toBe(false)
    expect(ed.getDoc()).toBe('docker compose u')
  })

  it('a caret move while navigating keeps the previewed command as the new draft', async () => {
    const { container, ed, view, recall } = setupRecall({ query: mkQuery(['docker compose up']) })
    ed.show()
    key(view, { key: 'ArrowUp' })
    await settled(container)
    expect(ed.getDoc()).toBe('docker compose up')
    // CM6 owns the caret afterwards (it consumes the arrow key for movement),
    // which is exactly the point: the overlay released the line, preview kept.
    key(view, { key: 'ArrowRight' })
    expect(recall.isOpen).toBe(false)
    expect(ed.getDoc()).toBe('docker compose up')
  })

  it('Ctrl-C while recall is open dismisses it and never interrupts the shell', async () => {
    const cancel = vi.fn()
    const { container, ed, view, recall } = setupRecall({
      query: mkQuery(['ls']),
      actions: { cancel },
    })
    ed.show()
    key(view, { key: 'ArrowUp' })
    await settled(container)
    expect(recall.isOpen).toBe(true)
    key(view, { key: 'c', ctrlKey: true })
    expect(recall.isOpen).toBe(false)
    expect(ed.getDoc()).toBe('') // the draft (empty) was restored
    expect(cancel).not.toHaveBeenCalled() // no \x03 went to the shell
  })
})

describe('recall: typing narrows the rung (nocx-ms7v)', () => {
  it('a printable key is consumed into the filter and only matching rows remain', async () => {
    const { container, view } = setupRecall({
      query: filteringQuery(['make deploy', 'make test', 'git status']),
    })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    expect(panelOf(container).textContent).toContain('3 results')
    // Type "make t": the narrowing answers are async and each keystroke
    // supersedes the previous — the settled panel is the last one's.
    for (const ch of ['m', 'a', 'k', 'e', ' ', 't']) key(view, { key: ch })
    await vi.waitFor(() => {
      const text = panelOf(container).textContent ?? ''
      expect(fieldValue(container)).toBe('make t')
      expect(text).toContain('1 result')
      expect(text).toContain('make test')
      expect(text).not.toContain('make deploy')
      expect(text).not.toContain('git status')
    })
  })

  it('backspace trims the filter, re-widening the rung', async () => {
    const { container, view } = setupRecall({
      query: filteringQuery(['make deploy', 'make test', 'git status']),
    })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    for (const ch of ['m', 'a', 'k', 'e', ' ', 't']) key(view, { key: ch })
    await vi.waitFor(() => expect(fieldValue(container)).toBe('make t'))
    key(view, { key: 'Backspace' }) // "make t" → "make "
    await vi.waitFor(() => {
      const text = panelOf(container).textContent ?? ''
      expect(fieldValue(container)).toBe('make ')
      expect(text).toContain('2 results')
      expect(text).toContain('make deploy')
    })
    key(view, { key: 'Backspace' }) // "make " → "make"
    await vi.waitFor(() => expect(fieldValue(container)).toBe('make'))
    for (let i = 0; i < 4; i++) key(view, { key: 'Backspace' }) // back to no filter
    await vi.waitFor(() => expect(panelOf(container).textContent).toContain('3 results'))
  })

  it('a filter with no matches says so and restores the draft to the editor', async () => {
    const { container, ed, view, recall } = setupRecall({
      query: filteringQuery(['make deploy', 'git status']),
    })
    ed.show()
    ed.insertText('git s')
    key(view, { key: 'ArrowUp' })
    await settled(container)
    expect(ed.getDoc()).toBe('make deploy') // the newest row is previewed
    key(view, { key: 'z' })
    await vi.waitFor(() => {
      const text = panelOf(container).textContent ?? ''
      expect(text).toContain('no matches for "z"')
    })
    // The empty rung state means "the editor holds the draft", never a
    // stale preview of a command the filter removed.
    expect(ed.getDoc()).toBe('git s')
    expect(recall.isOpen).toBe(true)
    key(view, { key: 'Backspace' }) // clears the filter — the rung returns
    await vi.waitFor(() => {
      const text = panelOf(container).textContent ?? ''
      expect(text).not.toContain('no matches')
      expect(text).toContain('make deploy')
    })
  })

  it('Esc with an active filter restores the ORIGINAL draft, not the preview', async () => {
    const { container, ed, view, recall } = setupRecall({
      query: filteringQuery(['make deploy', 'git status']),
    })
    ed.show()
    ed.insertText('git s')
    key(view, { key: 'ArrowUp' })
    await settled(container)
    key(view, { key: 'm' })
    await vi.waitFor(() => expect(fieldValue(container)).toBe('m'))
    key(view, { key: 'Escape' })
    expect(recall.isOpen).toBe(false)
    expect(ed.getDoc()).toBe('git s') // the draft captured at open, exactly
  })

  it('the filter rides the ladder climb — shift+Up keeps narrowing', async () => {
    const { container, view } = setupRecall({
      query: (scope, text) => {
        const needle = text ?? ''
        const pool =
          scope === 'directory'
            ? ['make deploy', 'make test', 'git status']
            : ['make deploy', 'make test', 'git status', 'docker build']
        const matched = pool.filter((c) => c.toLowerCase().includes(needle.toLowerCase()))
        return Promise.resolve({
          entries: matched.map((c, i) => mkEntry(c, 1000 - i)),
          scope,
          exhausted: true,
          source: 'session',
          coverage: matched.length > 0 ? 1000 - (matched.length - 1) : null,
        })
      },
    })
    key(view, { key: 'ArrowUp' })
    await settled(container) // 3 rows on the directory rung: no open-time climb
    expect(panelOf(container).textContent).toContain('this directory')
    key(view, { key: 'm' })
    await vi.waitFor(() => expect(fieldValue(container)).toBe('m'))
    key(view, { key: 'ArrowUp', shiftKey: true }) // widen to host
    await vi.waitFor(() => {
      const text = panelOf(container).textContent ?? ''
      expect(text).toContain('this host')
      expect(fieldValue(container)).toBe('m') // the filter survived the climb
      expect(text).toContain('make deploy')
      expect(text).not.toContain('docker build') // still narrowed
      expect(text).not.toContain('git status')
    })
  })
})

describe('recall: a search hands the input to the field (brief search-ui)', () => {
  it('Enter with a non-empty filter INSERTS without running; the second Enter runs it', async () => {
    const { container, ed, view, recall, submit } = setupRecall({
      query: filteringQuery(['rm -rf build', 'ls', 'git status']),
    })
    ed.show()
    ed.insertText('git s')
    key(view, { key: 'ArrowUp' })
    await settled(container)
    expect(ed.getDoc()).toBe('rm -rf build') // empty filter: previewed
    key(view, { key: 'r' }) // filter 'r' — only rm -rf build survives
    await vi.waitFor(() => expect(fieldValue(container)).toBe('r'))
    expect(ed.getDoc()).toBe('git s') // the draft is back: not previewed
    key(view, { key: 'Enter' })
    // Inserted, NOT executed: the panel closed, the command sits in the
    // line, and nothing went out over the submit path.
    expect(recall.isOpen).toBe(false)
    expect(ed.getDoc()).toBe('rm -rf build')
    expect(submit).not.toHaveBeenCalled()
    // The second Enter — now an empty-filter Enter with the command visible
    // — is the reviewed run, through the editor's own submit path.
    key(view, { key: 'Enter' })
    expect(submit).toHaveBeenCalledTimes(1)
    expect(submit).toHaveBeenCalledWith('rm -rf build')
  })

  it('with a non-empty filter, arrows highlight but never preview; clearing the filter resumes the preview', async () => {
    const { container, ed, view } = setupRecall({
      query: filteringQuery(['make deploy', 'make test', 'git status']),
    })
    ed.show()
    ed.insertText('git s')
    key(view, { key: 'ArrowUp' })
    await settled(container)
    expect(ed.getDoc()).toBe('make deploy') // empty filter: newest row previewed
    key(view, { key: 'm' })
    await vi.waitFor(() => expect(fieldValue(container)).toBe('m'))
    expect(ed.getDoc()).toBe('git s') // the draft is back — no preview
    key(view, { key: 'ArrowUp' }) // older match
    expect(ed.getDoc()).toBe('git s') // still the draft; only the highlight moved
    // Backspace clears the filter; the preview resumes from the highlighted
    // row — the selection survived the narrowing.
    key(view, { key: 'Backspace' })
    await vi.waitFor(() => expect(fieldValue(container)).toBe(''))
    expect(ed.getDoc()).toBe('make test')
  })

  it('bolds the matched substring in every surviving row; an empty filter bolds nothing', async () => {
    const { container, view } = setupRecall({
      query: filteringQuery(['make deploy', 'git commit', 'git status']),
    })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    // Empty filter: rows are plain text — no <strong> anywhere.
    expect(container.querySelectorAll('mark.ui-floating-panel__match')).toHaveLength(0)
    key(view, { key: 'g' })
    await vi.waitFor(() => expect(fieldValue(container)).toBe('g'))
    let matches = Array.from(container.querySelectorAll('mark.ui-floating-panel__match'))
    expect(matches).toHaveLength(2) // git commit, git status
    expect(matches.every((m) => m.textContent === 'g')).toBe(true)
    for (const ch of ['i', 't']) key(view, { key: ch })
    await vi.waitFor(() => expect(fieldValue(container)).toBe('git'))
    matches = Array.from(container.querySelectorAll('mark.ui-floating-panel__match'))
    expect(matches).toHaveLength(2)
    expect(matches.every((m) => m.textContent === 'git')).toBe(true)
    expect(panelOf(container).textContent).not.toContain('make deploy')
  })

  it('the field is the panel bottom edge, above the footer, with the coverage on the same row', async () => {
    const now = Date.now()
    const { container, view } = setupRecall({
      query: (scope) =>
        Promise.resolve({
          entries: [mkEntry('ls', now - 2 * 86_400_000)],
          scope,
          exhausted: true,
          source: 'store',
          coverage: now - 21 * 86_400_000,
        }),
    })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    const panel = panelOf(container)
    const search = container.querySelector<HTMLElement>('.ui-floating-panel__search')
    expect(search).not.toBeNull()
    // The search row sits after the list (and detail) and before the footer.
    const children = Array.from(panel.children)
    const listIdx = children.findIndex((c) => c.classList.contains('ui-floating-panel__list'))
    const searchIdx = children.findIndex((c) => c.classList.contains('ui-floating-panel__search'))
    const footerIdx = children.findIndex((c) => c.classList.contains('ui-floating-panel__footer'))
    expect(listIdx).toBeGreaterThanOrEqual(0)
    expect(searchIdx).toBeGreaterThan(listIdx)
    expect(searchIdx).toBeLessThan(footerIdx)
    // The coverage rides the same row at its right-hand end, a property of
    // the search — not a second line of chrome.
    expect(search?.querySelector('.ui-search-field')).not.toBeNull()
    expect(search?.querySelector('.ui-floating-panel__coverage')?.textContent).toContain(
      'oldest entry',
    )
  })

  it('the footer names the Enter action, and it is the same one on both sides of a search', async () => {
    const { container, view } = setupRecall({
      query: filteringQuery(['make deploy', 'git status']),
    })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    expect(panelOf(container).textContent).toContain('↵ to insert')
    expect(panelOf(container).textContent).not.toContain('↵ to execute')
    key(view, { key: 'g' })
    await vi.waitFor(() => expect(fieldValue(container)).toBe('g'))
    expect(panelOf(container).textContent).toContain('↵ to insert')
    expect(panelOf(container).textContent).not.toContain('↵ to execute')
  })
})

describe('recall: the coverage line (nocx-ms7v)', () => {
  it('renders the answer horizon when the store states one', async () => {
    const now = Date.now()
    const { container, view } = setupRecall({
      query: (scope) =>
        Promise.resolve({
          entries: [mkEntry('ls', now - 2 * 86_400_000)],
          scope,
          exhausted: true,
          source: 'store',
          coverage: now - 21 * 86_400_000, // the oldest retained entry: 3 weeks
        }),
    })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    expect(panelOf(container).textContent).toContain('oldest entry 3 weeks ago')
  })

  it('no coverage line when the answer has no horizon', async () => {
    const { container, view } = setupRecall({ query: emptyQuery('store') })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    expect(panelOf(container).textContent).not.toContain('oldest entry')
  })
})

describe("recall: the detail pane shows the selected row's facts", () => {
  it('renders exit code, cwd, duration and last ran from the entry', async () => {
    const now = Date.now()
    const { container, view } = setupRecall({
      query: (scope) =>
        Promise.resolve({
          entries: [
            {
              id: '7',
              command: 'make deploy',
              cwd: '/srv/api',
              host: '',
              status: 'failure',
              maskedCount: 0,
              maskedKinds: [],
              exitCode: 2,
              startedAt: now - 130_000,
              endedAt: now - 120_000,
            },
          ],
          scope,
          exhausted: true,
          source: 'store',
          coverage: now - 3 * 86_400_000,
        }),
    })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    const detail = container.querySelector<HTMLElement>('.ui-floating-panel__detail')
    expect(detail).not.toBeNull()
    const text = detail?.textContent ?? ''
    expect(text).toContain('exit code')
    expect(text).toContain('2')
    expect(text).toContain('cwd')
    expect(text).toContain('/srv/api')
    expect(text).toContain('duration')
    expect(text).toContain('10s')
    expect(text).toContain('last ran')
    expect(text).toContain('2 minutes ago')
  })

  it('unknowns render as — and a running entry as running, never as zero or 1970', async () => {
    const { container, view } = setupRecall({
      query: (scope) =>
        Promise.resolve({
          entries: [
            {
              id: '7',
              command: 'sleep 5',
              cwd: '',
              host: '',
              status: 'running',
              maskedCount: 0,
              maskedKinds: [],
              endedAt: null, // startedAt absent too: never observed
            },
          ],
          scope,
          exhausted: true,
          source: 'store',
          coverage: null,
        }),
    })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    const detail = container.querySelector<HTMLElement>('.ui-floating-panel__detail')
    const items = Array.from(detail?.querySelectorAll('.ui-floating-panel__detail-item') ?? [])
    // Term and value are separate spans in one flex column, so textContent
    // joins them without whitespace — assert each item, not the joined text.
    expect(items[0]?.textContent).toContain('exit code')
    expect(items[0]?.textContent).toContain('—')
    expect(items[1]?.textContent).toContain('cwd')
    expect(items[1]?.textContent).toContain('—')
    expect(items[2]?.textContent).toContain('duration')
    expect(items[2]?.textContent).toContain('running')
    expect(items[3]?.textContent).toContain('last ran')
    expect(items[3]?.textContent).toContain('running')
    const text = detail?.textContent ?? ''
    expect(text).not.toContain('1970')
    expect(text).not.toContain('0s')
  })
})

describe('recall: formatDuration', () => {
  it('renders the human span: ms, tenths of a second, minutes, hours', () => {
    expect(formatDuration(0)).toBe('0ms')
    expect(formatDuration(800)).toBe('800ms')
    expect(formatDuration(2_300)).toBe('2.3s')
    expect(formatDuration(10_000)).toBe('10s')
    expect(formatDuration(61_600)).toBe('1m 2s')
    expect(formatDuration(3_599_000)).toBe('59m 59s')
    expect(formatDuration(3_600_000)).toBe('1h 0m')
  })

  it('a skewed clock never renders a negative duration', () => {
    expect(formatDuration(-500)).toBe('0ms')
  })
})
describe('recall: Esc restores the draft, caret and scroll exactly', () => {
  it('restores text, selection and scroll after navigating', async () => {
    const { container, ed, view, recall } = setupRecall({ query: mkQuery(['one', 'two']) })
    ed.show()
    ed.insertText('line one\nline two')
    // The user had a selection and a scroll offset when recall opened.
    view.dispatch({ selection: { anchor: 2, head: 5 } })
    ed.setScrollTop(37)

    // The explicit shortcut opens recall from anywhere — even line 2.
    key(view, { key: 'r', ctrlKey: true })
    await settled(container)
    expect(recall.isOpen).toBe(true)
    key(view, { key: 'ArrowDown' }) // at the newest (bottom): stays
    key(view, { key: 'ArrowUp' }) // older
    expect(ed.getDoc()).toBe('two') // previewing the older row

    key(view, { key: 'Escape' })
    expect(recall.isOpen).toBe(false)
    expect(ed.getDoc()).toBe('line one\nline two')
    expect(ed.getSelection()).toEqual({ from: 2, to: 5 })
    expect(ed.getScrollTop()).toBe(37)
  })
})

describe("recall: oldest at the top, newest at the bottom (Warp's model)", () => {
  it('renders oldest at the top and selects the newest (bottom) row on open', async () => {
    // mkQuery lists commands newest-first — the wire order; the renderer
    // reverses for display.
    const { container, view } = setupRecall({ query: mkQuery(['newest', 'middle', 'oldest']) })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    const rows = container.querySelectorAll<HTMLElement>('.ui-collection-row')
    expect(rows.length).toBe(3)
    expect(rows[0]?.textContent).toContain('oldest') // oldest at the top
    expect(rows[2]?.textContent).toContain('newest') // newest at the bottom
    const selected = container.querySelector<HTMLElement>(
      '.ui-collection-row[data-selected="true"]',
    )
    expect(selected?.textContent).toContain('newest') // the bottom row
  })

  it('Up from the bottom moves to the previous (older) command', async () => {
    const { container, view } = setupRecall({ query: mkQuery(['newest', 'middle', 'oldest']) })
    key(view, { key: 'ArrowUp' }) // opens with the newest (bottom) selected
    await settled(container)
    key(view, { key: 'ArrowUp' }) // older
    const selected = container.querySelector<HTMLElement>(
      '.ui-collection-row[data-selected="true"]',
    )
    expect(selected?.textContent).toContain('middle')
  })

  it('Down at the bottom stays on the newest command', async () => {
    const { container, view } = setupRecall({ query: mkQuery(['newest', 'middle', 'oldest']) })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    key(view, { key: 'ArrowDown' })
    const selected = container.querySelector<HTMLElement>(
      '.ui-collection-row[data-selected="true"]',
    )
    expect(selected?.textContent).toContain('newest')
  })

  it('a single row is selected and previewed on open', async () => {
    const { ed, container, view } = setupRecall({ query: mkQuery(['only']) })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    const selected = container.querySelector<HTMLElement>(
      '.ui-collection-row[data-selected="true"]',
    )
    expect(selected?.textContent).toContain('only')
    expect(ed.getDoc()).toBe('only')
  })
})

describe('recall: arrows navigate, widening is its own key (v8)', () => {
  const twelve = Array.from({ length: 12 }, (_, i) => `c${i + 1}`) // c1 newest

  it('Up and Down walk every entry of a twelve-result rung', async () => {
    const { container, ed, view } = setupRecall({ query: mkQuery(twelve) })
    key(view, { key: 'ArrowUp' }) // opens on the newest (bottom) row
    await settled(container)
    expect(ed.getDoc()).toBe('c1')
    // Hold Up past the visible window to the top of the rung.
    for (let i = 0; i < 11; i++) key(view, { key: 'ArrowUp' })
    expect(ed.getDoc()).toBe('c12') // the oldest entry — all 12 reachable
    // Down returns through everything Up passed.
    for (let i = 0; i < 11; i++) key(view, { key: 'ArrowDown' })
    expect(ed.getDoc()).toBe('c1')
  })

  it('Up at the oldest entry stops: no widen, no teleport', async () => {
    const { container, ed, view } = setupRecall({ query: mkQuery(['c1', 'c2', 'c3']) })
    key(view, { key: 'ArrowUp' }) // opens on c1 (newest)
    await settled(container)
    key(view, { key: 'ArrowUp' }) // c2
    key(view, { key: 'ArrowUp' }) // c3 (oldest, display top)
    key(view, { key: 'ArrowUp' }) // must stop
    expect(ed.getDoc()).toBe('c3') // selection unchanged
    expect(panelOf(container).textContent).toContain('this directory') // no widen
  })

  it('the widen key (shift+Up) preserves the selected command across rungs', async () => {
    const { container, ed, view } = setupRecall({
      query: (scope) => {
        const entries =
          scope === 'directory'
            ? [mkEntry('c1'), mkEntry('c2'), mkEntry('c3')]
            : [mkEntry('c1'), mkEntry('c2'), mkEntry('c3'), mkEntry('c4')]
        return Promise.resolve({
          entries,
          scope,
          exhausted: true,
          source: 'session',
          coverage: null,
        })
      },
    })
    key(view, { key: 'ArrowUp' }) // c1 (newest, bottom)
    await settled(container)
    key(view, { key: 'ArrowUp' }) // c2
    key(view, { key: 'ArrowUp', shiftKey: true }) // widen
    // The wider rung's answer lands async; wait for its own observable — the
    // rung badge names the wider rung.
    await vi.waitFor(() => {
      expect(panelOf(container).textContent).toContain('this host')
    })
    expect(ed.getDoc()).toBe('c2') // the same command, not either end
  })
})

describe('recall: the selected row is fully inside the list box after any move (v9 §1)', () => {
  const rect = (el: HTMLElement, top: number, bottom: number): void => {
    Object.defineProperty(el, 'getBoundingClientRect', {
      configurable: true,
      value: () => ({
        top,
        bottom,
        left: 0,
        right: 0,
        width: 0,
        height: bottom - top,
        x: 0,
        y: 0,
        toJSON: () => ({}),
      }),
    })
  }

  it('a row straddling the bottom edge scrolls up so it is fully visible', () => {
    const list = document.createElement('div')
    const row = document.createElement('div')
    list.scrollTop = 64
    rect(list, 397, 691)
    rect(row, 689, 721) // 2px visible at the bottom edge
    expect(scrollTopToReveal(list, row)).toBe(94) // 64 + (721 - 691)
  })

  it('a row poking above the top edge scrolls down so it is fully visible', () => {
    const list = document.createElement('div')
    const row = document.createElement('div')
    list.scrollTop = 64
    rect(list, 397, 691)
    rect(row, 380, 412) // pokes 17px above the top
    expect(scrollTopToReveal(list, row)).toBe(47) // 64 - (397 - 380)
  })

  it('a fully visible row leaves the scroll position untouched', () => {
    const list = document.createElement('div')
    const row = document.createElement('div')
    list.scrollTop = 64
    rect(list, 397, 691)
    rect(row, 561, 593) // fully inside
    expect(scrollTopToReveal(list, row)).toBe(64)
  })
})

describe('recall: Up is caret movement first (design §8.10 v6)', () => {
  it('Up on line 2 of a two-line draft does not open recall (caret movement first)', () => {
    const onUpAtTop = vi.fn()
    const { ed, view, recall } = setupRecall({ query: mkQuery(['one']), actions: { onUpAtTop } })
    ed.show()
    ed.insertText('line one\nline two')
    const lineOf = (pos: number) => view.state.doc.lineAt(pos).number
    expect(lineOf(ed.getSelection().from)).toBe(2)

    const ev = key(view, { key: 'ArrowUp' })
    // The boundary we own: recall stays closed and onUpAtTop does not fire —
    // the key belongs to the editor's caret movement. (CM6's Up command runs
    // and consumes the key even in jsdom, where no layout exists for it to
    // actually move the caret; the movement itself is not observable here.)
    expect(recall.isOpen).toBe(false)
    expect(onUpAtTop).not.toHaveBeenCalled()
    expect(ev.defaultPrevented).toBe(true) // CM6's own Up command handled it
  })
  it('Up on line 1 of a MULTI-LINE draft does not open recall — the paste is not at risk', () => {
    // Recall previews over the draft, so one stray Up on a pasted block would
    // put somebody else's command where twenty lines were. `git ` + Up (below)
    // still works: a one-line draft costs a retype, and Esc brings it back.
    const onUpAtTop = vi.fn()
    const { ed, view, recall } = setupRecall({ query: mkQuery(['one']), actions: { onUpAtTop } })
    ed.show()
    // Caret on line 1 — "no further upward movement" holds, and before this
    // change that was the whole condition.
    ed.replaceDoc('curl https://api.example.com \\\n  -H "Content-Type: application/json"', 0, 0)
    key(view, { key: 'ArrowUp' })
    expect(recall.isOpen).toBe(false)
    expect(onUpAtTop).not.toHaveBeenCalled()
    expect(ed.getDoc()).toContain('curl https://api.example.com')
  })

  it('Up on a single-line draft still opens recall — the everyday gesture', async () => {
    const { container, ed, view, recall } = setupRecall({ query: mkQuery(['one']) })
    ed.show()
    ed.insertText('git s')
    key(view, { key: 'ArrowUp' })
    await settled(container)
    expect(recall.isOpen).toBe(true)
  })

  it('Up on an empty draft opens recall', async () => {
    const { container, view, recall } = setupRecall({ query: mkQuery(['one']) })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    expect(recall.isOpen).toBe(true)
  })

  it('the explicit shortcut (Ctrl-R) opens recall from anywhere', async () => {
    const { container, ed, view, recall } = setupRecall({ query: mkQuery(['one']) })
    ed.show()
    ed.insertText('line one\nline two') // caret on line 2
    key(view, { key: 'r', ctrlKey: true })
    await settled(container)
    expect(recall.isOpen).toBe(true)
    expect(ed.getDoc()).toBe('one')
  })
})

describe('recall: what the panel says', () => {
  it("with source 'session' the panel says what it is showing", async () => {
    const { container, view } = setupRecall({ query: mkQuery(['one'], 'session') })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    const text = panelOf(container).textContent ?? ''
    expect(text).toContain('this session only')
  })

  it("with source 'store' the panel does not say 'this session only'", async () => {
    const { container, view } = setupRecall({ query: mkQuery(['one'], 'store') })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    const text = panelOf(container).textContent ?? ''
    expect(text).not.toContain('this session only') // the store answered
    expect(text).toContain('one')
  })

  it('empty history opens an overlay that says it is empty', async () => {
    const { container, view, recall } = setupRecall({ query: emptyQuery() })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    expect(recall.isOpen).toBe(true)
    const text = panelOf(container).textContent ?? ''
    expect(text).toContain('no history yet')
    // It can still be dismissed.
    key(view, { key: 'Escape' })
    expect(recall.isOpen).toBe(false)
  })

  it('an empty directory rung opens on the first rung that has rows', async () => {
    const { container, view, recall } = setupRecall({
      query: (scope) =>
        Promise.resolve(
          scope === 'directory'
            ? { entries: [], scope, exhausted: true, source: 'session', coverage: null }
            : {
                entries: [mkEntry('ls /tmp'), mkEntry('pwd'), mkEntry('whoami')],
                scope,
                exhausted: true,
                source: 'session',
                coverage: null,
              },
        ),
    })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    expect(recall.isOpen).toBe(true)
    const text = panelOf(container).textContent ?? ''
    expect(text).not.toContain('no history yet') // never the near-empty rung
    expect(text).toContain('this host') // the rung widened and is named
    expect(text).toContain('ls /tmp') // and the rows appeared with it
  })

  it('a directory with one match opens on a wider rung, not on the near-empty one', async () => {
    const { container, view } = setupRecall({
      query: (scope) =>
        Promise.resolve(
          scope === 'directory'
            ? {
                entries: [mkEntry('ls')],
                scope,
                exhausted: true,
                source: 'session',
                coverage: null,
              }
            : {
                entries: [mkEntry('ls'), mkEntry('make deploy'), mkEntry('git status')],
                scope,
                exhausted: true,
                source: 'session',
                coverage: null,
              },
        ),
    })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    const text = panelOf(container).textContent ?? ''
    expect(text).toContain('this host') // the rung is named on screen
    expect(text).toContain('make deploy') // and its rows are there
  })

  it('a rung with a useful page stays on that rung', async () => {
    const { container, view } = setupRecall({
      query: (scope) =>
        Promise.resolve({
          entries:
            scope === 'directory'
              ? [mkEntry('ls'), mkEntry('git status'), mkEntry('make')]
              : [mkEntry('ls'), mkEntry('git status'), mkEntry('make'), mkEntry('x')],
          scope,
          exhausted: true,
          source: 'session',
          coverage: null,
        }),
    })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    const text = panelOf(container).textContent ?? ''
    expect(text).toContain('this directory')
  })

  it('the widen key is shown when the rung can widen, and not at the top rung', async () => {
    const narrow = {
      entries: [mkEntry('ls')],
      scope: 'directory' as const,
      exhausted: true,
      source: 'session' as const,
      coverage: null,
    }
    const { container, view } = setupRecall({
      query: (scope) =>
        Promise.resolve({
          entries:
            scope === 'directory'
              ? narrow.entries
              : scope === 'host'
                ? narrow.entries
                : [mkEntry('ls'), mkEntry('make'), mkEntry('git')],
          scope,
          exhausted: true,
          source: 'session',
          coverage: null,
        }),
    })
    key(view, { key: 'ArrowUp' }) // open-time climb: directory 1, host 1 → everywhere
    await settled(container)
    const text = panelOf(container).textContent ?? ''
    expect(text).toContain('everywhere') // the top rung is named
    expect(text).not.toContain('shift+↑ widen') // nothing wider to promise

    const second = setupRecall({ query: mkQuery(['one', 'two', 'three']) })
    key(second.view, { key: 'ArrowUp' })
    await settled(second.container)
    expect(panelOf(second.container).textContent).toContain('shift+↑ widen')
  })
})

describe('recall: the footer and the labels say what the keys do', () => {
  it('all hints are one line: the key groups in one footer, real gaps between them', async () => {
    const { container, view } = setupRecall({ query: mkQuery(['one', 'two', 'three']) })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    const footer = container.querySelector<HTMLElement>('.ui-floating-panel__footer')
    expect(footer).not.toBeNull()
    // The key groups are siblings of one footer element — the CSS lays them
    // out on one line with a real gap (white-space: nowrap; display: flex),
    // so no hint gets its own row and none can wrap apart from the others.
    const groups = footer?.querySelectorAll<HTMLElement>(':scope > span') ?? []
    expect(groups.length).toBe(4)
    expect(groups[0]?.textContent).toBe('↵ to insert')
    expect(groups[1]?.textContent).toBe('↑ ↓ to navigate')
    expect(groups[2]?.textContent).toBe('shift+↑ widen')
    expect(groups[3]?.textContent).toBe('esc to dismiss')
    expect(footer?.querySelector('br')).toBeNull()
  })

  it('Enter is labelled as taking the command into the line, never as running it', async () => {
    const { container, view } = setupRecall({
      query: mkQuery(['rm -rf build', 'ls', 'git status']),
    })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    const text = panelOf(container).textContent ?? ''
    expect(text).toContain('↵ to insert')
    expect(text).not.toContain('↵ to execute')
    expect(text).not.toContain('fill the line')
  })

  it('the empty panel does not promise execution', async () => {
    const { container, view } = setupRecall({ query: emptyQuery() })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    const text = panelOf(container).textContent ?? ''
    expect(text).toContain('no history yet')
    expect(text).not.toContain('↵ to execute') // nothing to execute
  })
})

describe('recall: relative time', () => {
  it('endedAt null renders as running, never as the epoch', async () => {
    const { container, view } = setupRecall({
      query: (scope) =>
        Promise.resolve({
          entries: [mkEntry('sleep 5', null)],
          scope,
          exhausted: true,
          source: 'session',
          coverage: null,
        }),
    })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    const time = container.querySelector<HTMLElement>('.ui-floating-panel__time')
    expect(time?.textContent).toBe('running')
    expect(time?.textContent).not.toBe('1970')
  })

  it('renders the screenshot cases: just now, 21 hours ago, 1 week ago', () => {
    const now = 1_000_000_000
    expect(relativeTime(now - 30_000, now)).toBe('just now')
    expect(relativeTime(now - 21 * 3_600_000, now)).toBe('21 hours ago')
    expect(relativeTime(now - 7 * 86_400_000, now)).toBe('1 week ago')
  })

  it('renders a stored row against the wall clock, not the page clock (nocx-rtg0.16)', async () => {
    // The panel's clock must be the same wall clock the store persists:
    // a stored endedAt two minutes before now must read "2 minutes ago".
    // On the old performance.now() clock the diff of an epoch timestamp
    // against page-load milliseconds clamped to zero and every row read
    // "just now" — and rows stamped by the ledger's own wrong clock read
    // as 1970. Asserting an exact label makes the clock path observable.
    const now = Date.now()
    const { container, view } = setupRecall({
      query: (scope) =>
        Promise.resolve({
          entries: [mkEntry('echo wall-clock', now - 2 * 60_000)],
          scope,
          exhausted: true,
          source: 'store',
          coverage: null,
        }),
    })
    key(view, { key: 'ArrowUp' })
    await settled(container)
    const time = container.querySelector<HTMLElement>('.ui-floating-panel__time')
    expect(time?.textContent).toBe('2 minutes ago')
  })
})

describe('withSessionText: this session comes back as it was run (nocx-xkve.4)', () => {
  const page = (entries: HistoryEntry[]): HistoryQuery => ({
    entries,
    scope: 'directory',
    exhausted: true,
    source: 'store',
    coverage: null,
  })
  const storeRow = (over: Partial<HistoryEntry> & { id: string }): HistoryEntry => ({
    command: 'curl -H "Authorization: Bearer sk-p...7249"',
    cwd: '/a',
    host: 'h1',
    status: 'success',
    startedAt: 1000,
    endedAt: 2000,
    maskedCount: 1,
    maskedKinds: ['openai'],
    ...over,
  })

  /** A ledger holding one command that actually RAN: the app-owned submit
   *  stamps startedAt at open (ADR-0024 §5) — the match key. */
  const ran = (command: string, cwd: string, host: string): CommandLedger => {
    const ledger = new CommandLedger({ now: () => 1000 })
    ledger.open(command, cwd, host, () => undefined, 'shell')
    return ledger
  }

  it('replaces the masked text with what the command was RUN with', () => {
    const ledger = ran('curl -H "Authorization: Bearer sk-or-v1-realkeyhere"', '/a', 'h1')
    const got = withSessionText(page([storeRow({ id: '1' })]), ledger)
    expect(got.entries[0].command).toBe('curl -H "Authorization: Bearer sk-or-v1-realkeyhere"')
    // The mask facts go with the mask: "1 secret masked" over unmasked text
    // is the same class of lie the masking exists to prevent.
    expect(got.entries[0].maskedCount).toBe(0)
    expect(got.entries[0].maskedKinds).toEqual([])
  })

  // The redactions go with the mask facts, and this is not cosmetic. The
  // spans are offsets into the MASKED command, so leaving them attached to
  // the longer session text drew the unresolved chip across an arbitrary
  // slice of it — over `sk-or-v1-c1`, with the remaining fifty characters
  // of the key sitting beside it in plain sight, and the command refusing
  // to run because a chip was still in the line. Cleared, the row is the
  // command as it was run, and it runs again.
  it('clears the redactions with the mask, so the recalled command runs', () => {
    const ledger = ran('curl -H "Authorization: Bearer sk-or-v1-realkeyhere"', '/a', 'h1')
    const masked = storeRow({
      id: '1',
      redactions: [{ kind: 'openai', start: 32, end: 44, prefix: 'sk-o', suffix: '7249' }],
    })
    const got = withSessionText(page([masked]), ledger)
    expect(got.entries[0].command).toBe('curl -H "Authorization: Bearer sk-or-v1-realkeyhere"')
    expect(got.entries[0].maskedCount).toBe(0)
    expect(got.entries[0].redactions).toEqual([])
  })

  // A row from an EARLIER session has no ledger record to match, so it comes
  // back masked with its spans intact — that is the case the unresolved
  // chip and the resolution flow exist for.
  it('leaves a row with no session record masked, spans intact', () => {
    const ledger = ran('something else entirely', '/a', 'h1')
    const masked = storeRow({
      id: '1',
      startedAt: 999,
      redactions: [{ kind: 'openai', start: 32, end: 44, prefix: 'sk-o', suffix: '7249' }],
    })
    const got = withSessionText(page([masked]), ledger)
    expect(got.entries[0].command).toBe('curl -H "Authorization: Bearer sk-p...7249"')
    expect(got.entries[0].redactions).toHaveLength(1)
  })

  it('leaves rows from other sessions alone — those are the durable, masked ones', () => {
    const ledger = ran('something else', '/a', 'h1')
    // Same directory, different moment: not this session's row.
    const older = storeRow({ id: '9', startedAt: 500, endedAt: 600 })
    const got = withSessionText(page([older]), ledger)
    expect(got.entries[0].command).toBe(older.command)
    expect(got.entries[0].maskedCount).toBe(1)
  })

  it('matches on the directory and host too, never on the moment alone', () => {
    const ledger = ran('real one', '/a', 'h1')
    const elsewhere = storeRow({ id: '2', cwd: '/b' })
    expect(withSessionText(page([elsewhere]), ledger).entries[0].command).toBe(elsewhere.command)
    const otherHost = storeRow({ id: '3', host: 'h2' })
    expect(withSessionText(page([otherHost]), ledger).entries[0].command).toBe(otherHost.command)
  })

  it('no ledger, or nothing to replace, returns the page untouched', () => {
    const p = page([storeRow({ id: '4' })])
    expect(withSessionText(p, null)).toBe(p)
    expect(withSessionText(p, ran('unrelated', '/zzz', 'nobody'))).toBe(p)
  })
})

describe('queryLedgerHistory: the session fallback behind the generated types', () => {
  it('maps the ledger newest-first, filtered to the ladder rung', () => {
    const now = () => 1000
    const ledger = new CommandLedger({ now })
    ledger.open('first', '/a', 'h1', () => undefined, 'shell')
    ledger.open('second', '/b', 'h1', () => undefined, 'shell')
    ledger.open('third', '/a', 'h2', () => undefined, 'shell')

    const dir = queryLedgerHistory(ledger, 'directory', '/a', 'h1')
    expect(dir.entries.map((e) => e.command)).toEqual(['first'])
    expect(dir.source).toBe('session')

    const host = queryLedgerHistory(ledger, 'host', '/a', 'h1')
    expect(host.entries.map((e) => e.command)).toEqual(['second', 'first'])

    const everywhere = queryLedgerHistory(ledger, 'everywhere', '/a', 'h1')
    expect(everywhere.entries.map((e) => e.command)).toEqual(['third', 'second', 'first'])
  })

  it('filters by text the same way the store does, case-insensitively', () => {
    const now = () => 1000
    const ledger = new CommandLedger({ now })
    ledger.open('make deploy', '/a', 'h1', () => undefined, 'shell')
    ledger.open('git status', '/a', 'h1', () => undefined, 'shell')
    ledger.open('MAKE PROD', '/a', 'h1', () => undefined, 'shell')

    const filtered = queryLedgerHistory(ledger, 'everywhere', '/a', 'h1', 'make')
    expect(filtered.entries.map((e) => e.command)).toEqual(['MAKE PROD', 'make deploy'])

    const miss = queryLedgerHistory(ledger, 'everywhere', '/a', 'h1', 'zzz')
    expect(miss.entries).toEqual([])

    const none = queryLedgerHistory(ledger, 'everywhere', '/a', 'h1', '')
    expect(none.entries).toHaveLength(3) // empty filter is no filter
  })
  it('carries startedAt and states no horizon — nothing completes in the severed world', () => {
    const now = () => 1000
    const ledger = new CommandLedger({ now })
    // The app-owned submit stamps the attempt start (ADR-0024 §5); nothing
    // completes a record in the severed world, so no endedAt exists and the
    // horizon stays null.
    ledger.open('first', '/a', 'h1', () => undefined, 'shell')
    ledger.open('second', '/a', 'h1', () => undefined, 'shell')
    ledger.open('third', '/a', 'h2', () => undefined, 'shell')

    const dir = queryLedgerHistory(ledger, 'directory', '/a', 'h1')
    expect(dir.coverage).toBe(null) // nothing completed, session-wide
    expect(dir.entries[0]?.startedAt).toBe(1000)

    // The rung narrows rows, never the horizon: the everywhere answer sees
    // the same (absent) oldest entry the directory rung does.
    const everywhere = queryLedgerHistory(ledger, 'everywhere', '/a', 'h1')
    expect(everywhere.coverage).toBe(null)
  })
  it('a ledger with nothing completed states no horizon', () => {
    const now = () => 1000
    const ledger = new CommandLedger({ now })
    ledger.open('only', '/a', 'h1', () => undefined, 'shell') // still running
    const q = queryLedgerHistory(ledger, 'everywhere', '/a', 'h1')
    expect(q.coverage).toBe(null)
  })
})

// ── Durable history is not running (nocx-rtg0.15) ───────────────────────
//
// Three states of `source`, not two. Before this the panel showed the same
// "no history yet / commands you run will appear here" whether the store had
// answered with nothing or there was no store to answer from — so a terminal
// that is keeping nothing looked exactly like a brand new one, and the
// description was a promise nothing behind it could keep.
describe('recall: the store is not there', () => {
  it('an unavailable store reads as not being kept, never as "no history yet"', async () => {
    const { container, ed, view } = setupRecall({ query: emptyQuery('unavailable') })
    ed.show()
    key(view, { key: 'ArrowUp' })
    await settled(container)

    const panel = panelOf(container)
    const title = panel.querySelector<HTMLElement>('.ui-empty-state__title')?.textContent
    const desc = panel.querySelector<HTMLElement>('.ui-empty-state__desc')?.textContent
    expect(title).toBe('history is not being kept')
    expect(desc).toBe('see Settings → History for why')
    // The promise that used to be made here.
    expect(panel.textContent).not.toContain('commands you run will appear here')
  })

  it('an empty store still reads as "no history yet" — the two must not be one', async () => {
    const { container, ed, view } = setupRecall({ query: emptyQuery('session') })
    ed.show()
    key(view, { key: 'ArrowUp' })
    await settled(container)

    const panel = panelOf(container)
    expect(panel.querySelector<HTMLElement>('.ui-empty-state__title')?.textContent).toBe(
      'no history yet',
    )
    expect(panel.querySelector<HTMLElement>('.ui-empty-state__desc')?.textContent).toBe(
      'commands you run will appear here',
    )
  })

  it('the header badge names which of the two it is', async () => {
    const unavailable = setupRecall({ query: emptyQuery('unavailable') })
    unavailable.ed.show()
    key(unavailable.view, { key: 'ArrowUp' })
    await settled(unavailable.container)
    const gone = panelOf(unavailable.container).querySelector<HTMLElement>(
      '.ui-floating-panel__source',
    )
    expect(gone?.textContent).toBe('not being kept')
    expect(gone?.dataset.tone).toBe('danger')

    const empty = setupRecall({ query: emptyQuery('session') })
    empty.ed.show()
    key(empty.view, { key: 'ArrowUp' })
    await settled(empty.container)
    const session = panelOf(empty.container).querySelector<HTMLElement>(
      '.ui-floating-panel__source',
    )
    expect(session?.textContent).toBe('this session only')
    expect(session?.dataset.tone).toBe('warning')
  })

  it('a store that answered carries no source badge at all', async () => {
    const { container, ed, view } = setupRecall({ query: emptyQuery('store') })
    ed.show()
    key(view, { key: 'ArrowUp' })
    await settled(container)
    expect(panelOf(container).querySelector<HTMLElement>('.ui-floating-panel__source')).toBeNull()
  })
})
