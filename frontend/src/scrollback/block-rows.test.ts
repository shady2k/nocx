// @vitest-environment jsdom

import { describe, expect, it } from 'vitest'
import type { LedgerBlockRowsLine, Mark } from '../generated/ledger.blockRows'
import { styleOf, wireRowOf, type CellSpec } from '../painter/fixtures'
import { CommandSnapshotStore } from '../command-snapshot'
import { DEFAULT_SNAPSHOT } from './serializer'
import { restoredBlock } from './restored-block'
import {
  blockColumnsOf,
  paintStoredRows,
  paintUnreadableRows,
  parseStoredBlockRows,
  type StoredBlockRows,
} from './block-rows'
import { blockOutputText } from './blocks'

const PLAIN = styleOf()

const okRow: CellSpec[] = [
  ['o', 1, true],
  ['k', 1, true],
  ['\n', 1, true],
]
const abcRow: CellSpec[] = [
  ['a', 1, true],
  ['b', 1, true],
  ['c', 1, true],
]

const stored: StoredBlockRows = {
  lines: [
    { from: 12, row: wireRowOf(okRow) },
    { from: 13, row: wireRowOf(abcRow) },
  ] satisfies LedgerBlockRowsLine[],
  droppedRows: 0,
  lostRows: 0,
  truncated: null,
}

describe('stored block rows', () => {
  it('carries the immutable artifact version and logical line onto painted card rows', () => {
    const block = document.createElement('article')
    block.dataset.entryId = 'block-7'

    paintStoredRows(
      block,
      { ...stored, artifactVersion: 'artifact-v4' },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )

    const row = block.querySelector<HTMLElement>('.term-grid-row')!
    expect(row.dataset.blockId).toBe('block-7')
    expect(row.dataset.artifactVersion).toBe('artifact-v4')
    expect(row.dataset.logicalLine).toBe('12')
  })

  it('paints backend rows through the shared row painter', () => {
    const block = document.createElement('article')

    paintStoredRows(block, stored, { metric: null, palette: DEFAULT_SNAPSHOT })

    expect([...block.querySelectorAll('.term-grid-row')].map((el) => el.textContent)).toEqual([
      'ok\n',
      'abc',
    ])
    expect(block.querySelector('.cmd-output')?.classList.contains('cmd-output-evicted')).toBe(false)
  })

  it("warms cell-fit with every row's cells before painting a single one (nocx-2v80t.3.18)", () => {
    // boxOf is a pure cache read (cell-fit.ts) — nothing warmed before the
    // paint pass means no glyph is ever boxed, whatever the metric says.
    // This is the wiring that keeps that from regressing silently: warm
    // must fire, with the candidates the rows actually carry, and it must
    // fire BEFORE any row paints so the read that follows hits a warm cache.
    const block = document.createElement('article')
    const glyphRow: CellSpec[] = [
      ['⬢', 1, true],
      ['x', 1, true],
    ]
    const seen: Array<{ chars: string; width: number }> = []
    let warmedBeforePaint = false
    paintStoredRows(
      block,
      { ...stored, lines: [{ from: 0, row: wireRowOf(glyphRow) }] },
      {
        metric: null,
        palette: DEFAULT_SNAPSHOT,
        warm: (candidates) => {
          for (const c of candidates) seen.push({ chars: c.chars, width: c.width })
          warmedBeforePaint = block.querySelector('.term-grid-row') === null
        },
      },
    )
    expect(seen).toEqual([
      { chars: '⬢', width: 1 },
      { chars: 'x', width: 1 },
    ])
    expect(warmedBeforePaint).toBe(true)
  })

  it('turns a path reference in a painted row into a clickable link (nocx-2v80t.3.18)', () => {
    // The retired outputHtml path decorated links once, at freeze, on the
    // HTML string it injected (blocks.ts) — dead since block bodies come
    // from stored rows and that string is always ''. Nothing replaced the
    // call, so a stored block's paths and urls stopped being links.
    const text = 'see docs/architecture.md:101 for more'
    const row: CellSpec[] = [...text].map((ch) => [ch, 1, true] as CellSpec)
    const block = document.createElement('article')

    paintStoredRows(
      block,
      { ...stored, lines: [{ from: 0, row: wireRowOf(row) }] },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )

    const link = block.querySelector<HTMLElement>('.term-link')
    expect(link?.textContent).toBe('docs/architecture.md:101')
    expect(block.querySelector('.cmd-output')?.textContent).toBe(text)
  })

  it('names the widest line as the column count every row shares', () => {
    // 'ok\n' is 3 cells wide, 'abc' is 3 too, but a block whose lines differ
    // (nocx-2v80t.3.18) must report the WIDEST one: every row is padded out
    // to it, so that is the width the drift instrument has to check against.
    expect(blockColumnsOf(stored.lines)).toBe(3)
    expect(blockColumnsOf([{ from: 0, row: wireRowOf(abcRow) }])).toBe(3)
    expect(blockColumnsOf([])).toBe(0)
  })

  it('pads mixed trimmed rows while preserving a styled trailing cell', () => {
    const styledTail = styleOf({ background: { kind: 2, palette: 0, rgb: { r: 1, g: 2, b: 3 } } })
    const mixed: StoredBlockRows = {
      lines: [
        { from: 20, row: wireRowOf([['a', 1, true, PLAIN]]) },
        {
          from: 21,
          row: wireRowOf([
            ['b', 1, true, PLAIN],
            ['', 1, false, styledTail],
          ]),
        },
        {
          from: 22,
          row: wireRowOf([
            ['c', 1, true, PLAIN],
            ['d', 1, true, PLAIN],
            ['e', 1, true, PLAIN],
          ]),
        },
      ],
      droppedRows: 0,
      lostRows: 0,
      truncated: null,
    }
    const block = document.createElement('article')

    paintStoredRows(block, mixed, { metric: null, palette: DEFAULT_SNAPSHOT })

    const rows = [...block.querySelectorAll('.term-grid-row')]
    expect(rows.map((el) => el.textContent)).toEqual(['a  ', 'b  ', 'cde'])
    expect(rows[1].querySelector('span')?.getAttribute('style')).toContain(
      'background: rgb(1, 2, 3)',
    )
  })

  // The causes reach the card separately (ledger.get carries the artifact's
  // truncated reason and the payload summary's two counts), so the card says
  // WHY rows are missing, one cause at a time, each with its count where the
  // store counted one (nocx-zg3k3.5.5). A cause that arrives later — rows
  // lost while the coordinator was away, say — is one more table entry in
  // block-rows.ts, not a second derivation.
  it('names the cap cause with its count and the setting that raises it', () => {
    const block = document.createElement('article')

    paintStoredRows(
      block,
      { ...stored, droppedRows: 3, truncated: 'cap', sealed: true },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )

    expect(block.querySelector('[data-output-incomplete]')?.textContent).toBe(
      'Output incomplete: 3 rows are missing — the output passed the history output limit. Raise the history.outputCapKB setting to keep more.',
    )
  })

  it('names the cap cause without a count while the block is still open', () => {
    const block = document.createElement('article')

    paintStoredRows(
      block,
      { ...stored, truncated: 'cap' },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )

    expect(block.querySelector('[data-output-incomplete]')?.textContent).toBe(
      'Output incomplete: the output passed the history output limit (the history.outputCapKB setting).',
    )
  })

  it('names the loss cause by losses counted, never as rows, apart from the cap', () => {
    // The store's lostRows sums what rode the wire's one loss field: the
    // runtime's struck feeds count ONE per feed (a feed may have carried
    // hundreds of rows — the emulator's ABI cannot count what a prune
    // took), and any rows a helper drop states into the same field would
    // be exact rows. No reader can tell which, so the card claims
    // LOSSES, never a row count (nocx-zg3k3.5.9). The tail stays
    // source-neutral: the same stored field is documented to carry exact
    // helper-drop rows too, so no producer is named as the limitation.
    const block = document.createElement('article')

    paintStoredRows(
      block,
      { ...stored, lostRows: 2, sealed: true },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )

    expect(block.querySelector('[data-output-incomplete]')?.textContent).toBe(
      'Output incomplete: output was lost 2 times before it could be captured; this count is not a row count.',
    )
  })

  it('does not render a one-feed loss as one row (nocx-zg3k3.5.9)', () => {
    // One struck feed stores lostRows=1: a feed whose prune took any
    // number of rows. The card must not read it as "1 row".
    const block = document.createElement('article')

    paintStoredRows(
      block,
      { ...stored, lostRows: 1, sealed: true },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )

    const text = block.querySelector('[data-output-incomplete]')?.textContent ?? ''
    expect(text).toBe(
      'Output incomplete: output was lost once before it could be captured; this count is not a row count.',
    )
    expect(text).not.toContain('1 row')
  })

  it('names rows lost while the server was unavailable as its own cause (nocx-zg3k3.5.3)', () => {
    // The store adds a coordinator-unavailable loss to lostRows AND to its
    // own unavailableRows share, so a real payload carries it in both.
    const block = document.createElement('article')

    paintStoredRows(
      block,
      { ...stored, lostRows: 4, unavailableRows: 4, sealed: true },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )

    expect(block.querySelector('[data-output-incomplete]')?.textContent).toBe(
      "Output incomplete: 4 rows were lost while nocx's server was unavailable.",
    )
  })

  it("states one loss once when the server's absence accounts for part of lostRows", () => {
    const block = document.createElement('article')

    paintStoredRows(
      block,
      { ...stored, lostRows: 6, unavailableRows: 4, sealed: true },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )

    const text = block.querySelector('[data-output-incomplete]')?.textContent ?? ''
    expect(text).toContain('output was lost 2 times before it could be captured')
    expect(text).toContain("4 rows were lost while nocx's server was unavailable")
    expect(text).not.toContain('lost 6 times')
  })

  it('says nothing about server unavailability when the store carries none', () => {
    const block = document.createElement('article')

    paintStoredRows(block, { ...stored, sealed: true }, { metric: null, palette: DEFAULT_SNAPSHOT })

    expect(block.querySelector('[data-output-incomplete]')).toBeNull()
  })

  it('names the overflowed stream as its own cause', () => {
    const block = document.createElement('article')

    paintStoredRows(
      block,
      { ...stored, truncated: 'gap', sealed: true },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )

    expect(block.querySelector('[data-output-incomplete]')?.textContent).toBe(
      'Output incomplete: the output stream overflowed, so part of it could not be kept.',
    )
  })

  it('names a refused capture, which has no count because capture never ran', () => {
    const block = document.createElement('article')

    paintStoredRows(
      block,
      { ...stored, truncated: 'suppressed', sealed: true },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )

    expect(block.querySelector('[data-output-incomplete]')?.textContent).toBe(
      'Output incomplete: capture was refused by policy, so nothing was kept.',
    )
  })

  it('names two causes at once, each with its own count', () => {
    const block = document.createElement('article')

    paintStoredRows(
      block,
      { ...stored, droppedRows: 3, lostRows: 2, truncated: 'cap', sealed: true },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )

    const text = block.querySelector('[data-output-incomplete]')?.textContent ?? ''
    expect(text).toContain('3 rows are missing')
    expect(text).toContain('the output passed the history output limit')
    expect(text).toContain('output was lost 2 times')
  })

  it('shows no missing-rows notice for a complete block (paired positive)', () => {
    const block = document.createElement('article')

    paintStoredRows(block, stored, { metric: null, palette: DEFAULT_SNAPSHOT })

    expect(block.querySelector('[data-output-incomplete]')).toBeNull()
  })

  it('says a sealed block that holds no rows printed nothing, and says it only once sealed', () => {
    const open = document.createElement('article')
    paintStoredRows(
      open,
      { ...stored, lines: [], sealed: false },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )
    expect(open.querySelector('[data-output-empty]')).toBeNull()

    const sealedEmpty = document.createElement('article')
    paintStoredRows(
      sealedEmpty,
      { ...stored, lines: [], sealed: true },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )
    expect(sealedEmpty.querySelector('[data-output-empty]')?.textContent).toBe(
      'This command printed no output.',
    )
    expect(sealedEmpty.querySelector('[data-output-incomplete]')).toBeNull()
  })

  it('replaces the empty statement when a later read carries rows', () => {
    const block = document.createElement('article')
    paintStoredRows(
      block,
      { ...stored, lines: [], sealed: true },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )

    paintStoredRows(block, { ...stored, sealed: true }, { metric: null, palette: DEFAULT_SNAPSHOT })

    expect(block.querySelector('[data-output-empty]')).toBeNull()
    expect(block.querySelector('[data-output-incomplete]')).toBeNull()
    expect([...block.querySelectorAll('.term-grid-row')].map((el) => el.textContent)).toEqual([
      'ok\n',
      'abc',
    ])
  })

  it('keeps an incomplete notice readable as the block output when no rows painted', () => {
    // The agent-run completion and Copy output read the block's output
    // (blockOutputText). With no rows painted, the incomplete notice IS
    // what the block holds — a run whose output never arrived must not
    // answer '' and read as one that printed nothing (nocx-2v80t.3.27).
    const block = document.createElement('article')
    paintStoredRows(
      block,
      { ...stored, lines: [], droppedRows: 3, truncated: 'cap', sealed: true },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )

    expect(blockOutputText(block)).toContain('3 rows are missing')
  })

  it('renders every readable state with its own words, none an unexplained empty body', () => {
    // The states a card can be read in (nocx-zg3k3.5.5): complete, capture
    // refused, incomplete with EACH cause, evicted, empty, unreadable. Each
    // must carry visible words, and no two may share them — a person staring
    // at any card must be able to tell which state it is in without opening
    // anything else.
    const paint = (over: Partial<StoredBlockRows>): string => {
      const block = document.createElement('article')
      paintStoredRows(
        block,
        { ...stored, sealed: true, ...over },
        { metric: null, palette: DEFAULT_SNAPSHOT },
      )
      const text = block.textContent ?? ''
      expect(text.trim(), `state ${JSON.stringify(over)} must render words`).not.toBe('')
      return text
    }
    const unreadable = document.createElement('article')
    paintUnreadableRows(unreadable)
    const evicted = restoredBlock(
      {
        id: 1,
        command: 'make test',
        cwd: '/repo',
        location: '',
        durationMs: 1200,
        exitCode: 0,
        status: 'success' as const,
        body: null,
        author: 'shell' as const,
        kind: 'command' as const,
        entryId: 'entry-1',
      },
      DEFAULT_SNAPSHOT,
      () => document.createElement('div'),
      () => {},
      new CommandSnapshotStore(),
    )

    const states: ReadonlyArray<readonly [string, string]> = [
      ['complete', paint({})],
      ['cap', paint({ droppedRows: 3, truncated: 'cap' })],
      ['lost', paint({ lostRows: 2 })],
      ['gap', paint({ truncated: 'gap' })],
      ['suppressed', paint({ truncated: 'suppressed' })],
      ['two causes', paint({ droppedRows: 3, lostRows: 2, truncated: 'cap' })],
      ['empty', paint({ lines: [] })],
      ['unreadable', unreadable.textContent ?? ''],
      ['evicted', evicted.textContent ?? ''],
    ]
    expect(new Set(states.map(([, text]) => text)).size).toBe(states.length)
  })

  it('rejects malformed JSONL instead of inventing local output', () => {
    expect(() => parseStoredBlockRows('{"from":12}', { truncated: null, payload: {} })).toThrow(
      'stored block row is malformed',
    )
  })

  it('paints nothing for a line whose run does not partition its own text, rather than inventing a rectangle', () => {
    const malformed = parseStoredBlockRows(
      [
        // text 'ab' (two positions) but a run declaring only one column —
        // the same malformed-row refusal createCellModel().apply gives a
        // live frame, read through the stored-block path.
        { from: 20, row: { text: 'ab', runs: [[0, 1]] } },
      ]
        .map((line) => JSON.stringify(line))
        .join('\n'),
      { truncated: null, payload: {} },
    )
    const block = document.createElement('article')

    paintStoredRows(block, malformed, { metric: null, palette: DEFAULT_SNAPSHOT })

    expect(block.querySelectorAll('.term-grid-row')).toHaveLength(0)
  })

  it('paints a stored wide cluster from its own mark, the spacer folded out of the text', () => {
    // [position 0, 1 codepoint, width 2]: the wire's own vocabulary for "the
    // cluster at position 0 is wide" — text carries only the one codepoint,
    // never the spacer.
    const wideMark: Mark = [0, 1, 2]
    const line: LedgerBlockRowsLine = { from: 30, row: { text: '汉', marks: [wideMark] } }
    const block = document.createElement('article')

    paintStoredRows(
      block,
      { lines: [line], droppedRows: 0, lostRows: 0, truncated: null },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )

    // The wide cluster stands in both its columns (span 2), so its spacer
    // paints nothing: this test once expected '汉 ', which painted the row
    // three columns wide and put every later cell one column right of the
    // model (review of nocx-zg3k3.2, 2026-09-28; paint-row.ts skips it).
    expect(block.querySelector('.term-grid-row')?.textContent).toBe('汉')
  })
})
