// @vitest-environment jsdom

import { describe, it, expect, vi } from 'vitest'
import {
  restoredBlock,
  restoredTurn,
  bodyToHTML,
  type RestoredCause,
  type RestoredTurnFacts,
} from './restored-block'
import type { RunningBlockActions } from './blocks'
import { DEFAULT_SNAPSHOT, serializeRange, serializeRangeSGR } from './serializer'
import { BufferLine, lineWith, XTERM_CM_P16 } from './test-helpers'
import { CommandSnapshotStore } from '../command-snapshot'

const S = DEFAULT_SNAPSHOT
const store = () => new CommandSnapshotStore()
const container = () => document.createElement('div')

const facts = (over: Partial<Parameters<typeof restoredBlock>[0]> = {}) => ({
  id: 1,
  command: 'make test',
  cwd: '/repo',
  location: '',
  durationMs: 1200,
  exitCode: 0,
  status: 'success' as const,
  body: 'all good',
  author: 'shell' as const,
  kind: 'command' as const,
  entryId: 'entry-1',
  ...over,
})

describe('a block built from the store', () => {
  it('renders the stored rows exactly as the live path rendered them', () => {
    const lines = [
      lineWith(
        { chars: 'o', fg: 2, fgMode: XTERM_CM_P16, bgMode: 0 },
        { chars: 'k', fg: 2, fgMode: XTERM_CM_P16, bgMode: 0 },
      ),
      new BufferLine('second', false),
    ]
    const getLine = (y: number) => lines[y]
    expect(bodyToHTML(S, serializeRangeSGR(getLine, 0, 1))).toBe(serializeRange(S, getLine, 0, 1))
  })

  it('says it is restored, in the DOM a gate can read', () => {
    const el = restoredBlock(facts(), S, container, () => {}, store())
    expect(el.dataset.restored).toBe('true')
    expect(el.classList.contains('cmd-block')).toBe(true)
    expect(el.dataset.entryId).toBe('entry-1')
  })

  it('offers mark and unmark for a restored block using its durable entry id', () => {
    let granted = false
    const toggleGrant = vi.fn(() => {
      granted = !granted
    })
    const actions: RunningBlockActions = {
      stop: vi.fn(),
      isActive: () => false,
      isGranted: () => granted,
      toggleGrant,
    }
    const el = restoredBlock(facts(), S, container, () => {}, store(), actions)
    document.body.append(el)
    try {
      el.querySelector<HTMLButtonElement>('.cmd-overflow-btn')!.click()
      const mark = document.querySelector<HTMLButtonElement>(
        '.cmd-overflow-menu-item[data-action="grant"]',
      )
      expect(mark?.textContent).toBe('Ask about this block')
      mark?.click()
      expect(toggleGrant).toHaveBeenCalledWith(el)
      expect(el.dataset.entryId).toBe('entry-1')

      el.querySelector<HTMLButtonElement>('.cmd-overflow-btn')!.click()
      const unmark = document.querySelector<HTMLButtonElement>(
        '.cmd-overflow-menu-item[data-action="grant"]',
      )
      expect(unmark?.textContent).toBe('Unmark')
    } finally {
      document.querySelectorAll('.cmd-overflow-menu').forEach((menu) => menu.remove())
      el.remove()
    }
  })

  it('carries the command, the directory and the outcome', () => {
    const el = restoredBlock(
      facts({ exitCode: 2, status: 'failure' }),
      S,
      container,
      () => {},
      store(),
    )
    expect(el.textContent).toContain('make test')
    expect(
      el.querySelector('.cmd-block-failure, [data-status="failure"]') ?? el.outerHTML,
    ).toBeTruthy()
    expect(el.outerHTML).toContain('repo')
  })

  it('says the output is GONE when it is gone, and says nothing when there was none', () => {
    // The two states a restored block must not confuse. Retention evicts
    // bodies while their entries stay (ADR-0019 §7), so "no artifact" is a
    // hole to name; a command that printed nothing is not.
    const evicted = restoredBlock(facts({ body: null }), S, container, () => {}, store())
    expect(evicted.dataset.outputEvicted).toBe('true')
    expect(evicted.textContent).toContain('Output is no longer kept')

    const silent = restoredBlock(facts({ body: '' }), S, container, () => {}, store())
    expect(silent.dataset.outputEvicted).toBeUndefined()
    expect(silent.textContent).not.toContain('Output is no longer kept')
  })

  it('paints with the CURRENT theme, which is why the body keeps SGR', () => {
    const red = restoredBlock(facts({ body: '\u001b[31mred' }), S, container, () => {}, store())
    expect(red.outerHTML).toContain(String(S.palette[1]))
  })

  // The badge half of nocx-4em1z, asserted through the seam a person reaches
  // it by: the entry says the assistant submitted the command, so the
  // restored block says so too. Before this, restoredBlock omitted the
  // argument entirely and the parameter defaulted to 'shell' — the block
  // came back looking like something the person had typed.
  it('paints the agent badge on a command the assistant ran', () => {
    const el = restoredBlock(facts({ author: 'agent' }), S, container, () => {}, store())
    const badge = el.querySelector('.ui-badge')
    expect(badge?.textContent).toBe('agent')
  })

  it("a person's own command carries no author mark at all", () => {
    const el = restoredBlock(facts(), S, container, () => {}, store())
    expect(el.querySelector('.ui-badge')).toBeNull()
  })

  // ── a restored assistant turn (nocx-4em1z) ──────────────────────────────
  //
  // The owner's report was that every dialogue vanished from a restored tab.
  // A turn is one entry — the question is its header, the answer is its body
  // — so it comes back as the block it was, in the ask grammar, drawn by the
  // SAME renderer that draws a live answer.
  it('comes back as an ask block with the question in its header', () => {
    const el = restoredBlock(
      facts({
        kind: 'ask',
        author: 'agent',
        command: 'what does this do?',
        body: 'It lists files.',
      }),
      S,
      container,
      () => {},
      store(),
    )
    expect(el.dataset.blockKind).toBe('ask')
    expect(el.querySelector('.cmd-header-text')?.textContent).toBe('what does this do?')
    expect(el.dataset.restored).toBe('true')
  })

  it("draws the answer as prose, through the answer body's own renderer", () => {
    // Through restoredTurn, which is the ONE builder of a turn's prose: the
    // block builder draws the pieces it is handed, and the projection that
    // produces them is the turn's (nocx-9sqii).
    const [el] = restoredTurn(
      facts({
        kind: 'ask',
        author: 'agent',
        command: 'summarise',
        body: '## Findings\n- run `ls`\n',
        id: undefined,
      }),
      S,
      () => 1,
      container,
      () => {},
      store(),
      () => null,
    )
    // The ask kind's body class — the wrap policy lives there, and a restored
    // answer must wrap exactly as a live one does.
    const body = el.querySelector('.cmd-output-ask')
    expect(body).not.toBeNull()
    // And the markdown is painted, not printed: the heading is a heading and
    // the inline code is code (ui/answer-markdown.ts owns the grammar).
    expect(body?.querySelector('[data-md="h2"]')).not.toBeNull()
    expect(body?.querySelector('.ui-md-code')?.textContent).toBe('ls')
  })

  it('keeps a fenced block in the command grammar, as the live answer does', () => {
    const [el] = restoredTurn(
      facts({
        kind: 'ask',
        author: 'agent',
        command: 'show me',
        body: '```\nls -la\n```\n',
        id: undefined,
      }),
      S,
      () => 1,
      container,
      () => {},
      store(),
      () => null,
    )
    expect(el.querySelector('.cmd-output-code')).not.toBeNull()
  })

  it('restored fenced answers keep the kit copy control and copy code only', async () => {
    const copied: string[] = []
    const previousClipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
    const el = restoredBlock(
      facts({
        kind: 'ask',
        author: 'agent',
        body: 'before\n```bash\nls -la\n```\nafter',
      }),
      S,
      container,
      () => {},
      store(),
    )
    document.body.appendChild(el)
    try {
      Object.defineProperty(navigator, 'clipboard', {
        configurable: true,
        value: {
          writeText: vi.fn((text: string) => {
            copied.push(text)
            return Promise.resolve()
          }),
        },
      })
      const button = el.querySelector<HTMLButtonElement>('.cmd-output-code .ui-icon-button')
      expect(button).not.toBeNull()
      button!.click()
      await vi.waitFor(() => expect(copied).toEqual(['ls -la']))
    } finally {
      el.remove()
      if (previousClipboard) {
        Object.defineProperty(navigator, 'clipboard', previousClipboard)
      } else {
        Reflect.deleteProperty(navigator, 'clipboard')
      }
    }
  })

  it('carries its entry id, so Copy output reads the stored answer', () => {
    const el = restoredBlock(
      facts({ kind: 'ask', author: 'agent', entryId: 'entry-9', body: 'hi' }),
      S,
      container,
      () => {},
      store(),
    )
    expect(el.dataset.entryId).toBe('entry-9')
  })

  it('says the answer is gone rather than pretending the model said nothing', () => {
    // The FACT is what gates the sentence: a turn whose prose retention
    // took answers ledger.get with proseEvicted true, and the block says
    // so once.
    const el = restoredBlock(
      facts({ kind: 'ask', author: 'agent', body: null, proseEvicted: true }),
      S,
      container,
      () => {},
      store(),
    )
    expect(el.querySelector('[data-prose-evicted="true"]')?.textContent).toContain(
      'Output is no longer kept',
    )
    expect(el.dataset.outputEvicted).toBe('true')
  })

  it('an EMPTY ask that never streamed a word says nothing of the sort', () => {
    // The paired positive behind the gate: `body` is null for a turn that
    // never streamed a word too, and nothing was lost — the sentence must
    // not be one that is always shown for an empty body.
    const el = restoredBlock(
      facts({ kind: 'ask', author: 'agent', body: null, proseEvicted: false }),
      S,
      container,
      () => {},
      store(),
    )
    expect(el.querySelector('[data-prose-evicted="true"]')).toBeNull()
    expect(el.textContent).not.toContain('Output is no longer kept')
  })
  const turn = (causes: RestoredCause[], over: Partial<RestoredTurnFacts> = {}) => {
    let n = 100
    return restoredTurn(
      {
        command: 'what went wrong?',
        cwd: '/repo',
        location: '',
        durationMs: 1200,
        exitCode: null,
        status: 'success',
        body: 'line 3 is wrong',
        author: 'agent',
        kind: 'ask',
        entryId: 'turn-1',
        causes,
        ...over,
      },
      S,
      () => n++,
      container,
      () => {},
      store(),
      (cause) =>
        cause.entryId === 'gone'
          ? null
          : restoredBlock(
              facts({
                id: n++,
                command: `cmd ${cause.entryId}`,
                author: 'agent',
                entryId: cause.entryId,
              }),
              S,
              container,
              () => {},
              store(),
            ),
    )
  }

  it('a turn with no causes is ONE block with its prose', () => {
    const [el] = turn([])
    expect(el.querySelector('[data-answer-body]')?.textContent).toContain('line 3 is wrong')
  })

  it('the question appears exactly once, however many blocks the turn caused', () => {
    // The `continued` badge and the repeated header are gone with the
    // fragments (ADR-0040): a restored turn says its question once, like a
    // live one.
    const [el] = turn([
      { entryId: 'cmd-1', kind: 'shell', source: 'user' },
      { entryId: 'cmd-2', kind: 'shell', source: 'user' },
    ])
    const headers = Array.from(el.querySelectorAll('.cmd-header-text')).map(
      (h) => h.textContent ?? '',
    )
    expect(headers.filter((h) => h === 'what went wrong?')).toHaveLength(1)
    expect(el.querySelector('[data-turn-continuation]')).toBeNull()
  })

  it('the blocks it caused are drawn inside the turn, in the seat order the ledger gave', () => {
    // ADR-0040: the turn CARRIES its children. A restored turn is ONE
    // block whose `.cmd-children` hold the same seats the store recorded,
    // exactly as the live path draws them.
    const [el] = turn([
      { entryId: 'cmd-1', kind: 'shell', source: 'user' },
      { entryId: 'cmd-2', kind: 'shell', source: 'user' },
    ])
    const box = el.querySelector(':scope > .cmd-children')
    expect(box).not.toBeNull()
    expect(Array.from(box!.querySelectorAll('.cmd-header-text')).map((h) => h.textContent)).toEqual(
      ['cmd cmd-1', 'cmd cmd-2'],
    )
  })

  it('a DANGLING cause costs the turn that block and nothing else', () => {
    // The command is older than the page limit, or retention took it.
    // Nothing is invented to stand in for it.
    const [el] = turn([
      { entryId: 'gone', kind: 'shell', source: 'user' },
      { entryId: 'cmd-2', kind: 'shell', source: 'user' },
    ])
    const box = el.querySelector(':scope > .cmd-children')!
    expect(Array.from(box.querySelectorAll('.cmd-header-text')).map((h) => h.textContent)).toEqual([
      'cmd cmd-2',
    ])
  })

  it('a turn whose prose is gone says so once and keeps every child block', () => {
    // Retention takes bodies and leaves entries (ADR-0019 §7). The blocks a
    // turn caused are entries of their own and survive the loss of the
    // prose; the run's loss is the turn's sentence, said ONCE.
    const [el] = turn([{ entryId: 'cmd-1', kind: 'shell', source: 'user' }], {
      body: null,
      proseEvicted: true,
    })
    const notices = el.querySelectorAll('[data-prose-evicted="true"]')
    expect(notices).toHaveLength(1)
    expect(notices[0].textContent).toBe('Output is no longer kept')
    // The children are all still there.
    const box = el.querySelector(':scope > .cmd-children')!
    expect(Array.from(box.querySelectorAll('.cmd-header-text')).map((h) => h.textContent)).toEqual([
      'cmd cmd-1',
    ])
  })

  it('a turn whose prose is intact says nothing of the sort', () => {
    // The paired positive: without it, the sentence above could be one that
    // is always shown. A whole turn carries its prose and no notice.
    const [el] = turn([{ entryId: 'cmd-1', kind: 'shell', source: 'user' }], {
      proseEvicted: false,
    })
    expect(el.querySelector('[data-prose-evicted="true"]')).toBeNull()
    expect(el.textContent).not.toContain('Output is no longer kept')
    const box = el.querySelector(':scope > .cmd-children')!
    expect(Array.from(box.querySelectorAll('.cmd-header-text')).map((h) => h.textContent)).toEqual([
      'cmd cmd-1',
    ])
  })

  // ── the turn's terminal chip, restored (nocx-hoeq3) ──────────────────────
  //
  // A restored turn used to show no terminal chip AT ALL, and the reason was
  // that the header derived it from the exit code: an agent entry is not a
  // shell arm, so `ledger.query` sends `exitCode: null` for one
  // (content.ShellExitCodeOf — "a non-shell entry never had one"). The live
  // turn meanwhile grew a `completed` chip from a second construction in the
  // answer flow's close. Two views of one turn, disagreeing about whether it
  // finished.
  //
  // The chip is the KIND's now: an ask block reads its terminal word from the
  // block's status, which is what a turn's outcome actually is.
  it('a restored turn says it completed, from its status and never from an exit code', () => {
    const [el] = turn([{ entryId: 'cmd-1', kind: 'shell', source: 'user' }])
    expect(el.querySelector('.cmd-header-exit')?.textContent).toBe('completed')
    expect(el.querySelector('.cmd-header-exit')?.className).toBe(
      'nocx-chip nocx-chip-ok cmd-header-exit cmd-header-exit-ok',
    )
    expect(el.querySelector('.cmd-header-duration')?.textContent).toBe('1.2s')
  })

  it('a turn that failed says so in its own word, not in the shell\u2019s', () => {
    const [el] = turn([], { status: 'failure' })
    expect(el.querySelector('.cmd-header-exit')?.textContent).toBe('failed')
    expect(el.querySelector('.cmd-header-exit')?.className).toBe(
      'nocx-chip nocx-chip-fail cmd-header-exit cmd-header-exit-fail',
    )
  })

  it('a turn handed an exit code still speaks its own vocabulary, never \u201cok\u201d', () => {
    // The store cannot send one today, and the point is that the header does
    // not depend on that staying true: an answer is not a command's output
    // (nocx-ex636), so the ask kind's chip never reads the shell's code.
    const [el] = turn([], { exitCode: 0 })
    expect(el.querySelector('.cmd-header-exit')?.textContent).toBe('completed')
  })
})
