// @vitest-environment jsdom

import { describe, expect, it } from 'vitest'
import type { LedgerBlockRowsLine, Mark } from '../generated/ledger.blockRows'
import { styleOf, wireRowOf, type CellSpec } from '../painter/fixtures'
import { CommandSnapshotStore } from '../command-snapshot'
import { DEFAULT_SNAPSHOT, type TerminalSnapshot } from './serializer'
import { restoredBlock } from './restored-block'
import {
  blockColumnsOf,
  loadedRowRanges,
  mergeStoredBlockRows,
  missingRowRanges,
  nextPageForRows,
  streamRowAtVisualOffset,
  paintStoredRows,
  repaintStoredRows,
  paintUnreadableRows,
  parseStoredBlockRows,
  storedBlockRowsForSelection,
  storedBlockRowsText,
  type StoredBlockRows,
} from './block-rows'
import { blockOutputText } from './blocks'
import type { RunMetric } from './run-geometry'

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

  it("writes the artifact's own sealed state onto the block", () => {
    const block = document.createElement('article')
    const opts = { metric: null, palette: DEFAULT_SNAPSHOT }
    paintStoredRows(block, { ...stored, sealed: true }, opts)
    expect(block.dataset.rowsSealed).toBe('true')
    paintStoredRows(block, { ...stored, sealed: false }, opts)
    expect(block.dataset.rowsSealed).toBe('false')
    paintStoredRows(block, { ...stored, sealed: undefined }, opts)
    expect(block.dataset.rowsSealed).toBeUndefined()
  })

  it('repaints loaded output without moving the live region or losing a read-failure notice', () => {
    const container = document.createElement('div')
    const block = document.createElement('article')
    block.dataset.entryId = 'font-repaint-block'
    container.appendChild(block)
    paintStoredRows(
      block,
      { ...stored, artifactVersion: 'font-artifact' },
      {
        metric: null,
        palette: DEFAULT_SNAPSHOT,
      },
    )
    const live = document.createElement('div')
    block.appendChild(live)
    paintUnreadableRows(block)
    const notice = block.querySelector('[data-output-unreadable]')
    const text = block.querySelector('.cmd-output')!.textContent
    const output = block.querySelector<HTMLElement>('.cmd-output')!
    output.scrollLeft = 37

    repaintStoredRows(container, { metric: null, palette: DEFAULT_SNAPSHOT })

    expect(block.querySelector('.cmd-output')!.textContent).toBe(text)
    expect(block.querySelector('.cmd-output')).toBe(output)
    expect(output.scrollLeft).toBe(37)
    expect([...block.children]).toEqual([block.querySelector('.cmd-output'), live, notice])
    expect(
      block.querySelector('[data-artifact-version]')?.getAttribute('data-artifact-version'),
    ).toBe('font-artifact')
    expect(block.querySelector('[data-output-unreadable]')).toBe(notice)
  })

  it('carries the whole artifact coordinates a page reports (nocx-t7xhe)', () => {
    const body = [
      JSON.stringify({ from: 10, row: wireRowOf(okRow) }),
      JSON.stringify({ from: 11, row: wireRowOf(abcRow) }),
    ].join('\n')
    const parsed = parseStoredBlockRows(
      body,
      { truncated: null, payload: { rowCount: 25 }, state: 'sealed' },
      'artifact-1',
      {
        firstRow: 0,
        lastRow: 24,
        endRow: 25,
        rowCount: 25,
        retainedRanges: [{ from: 0, to: 25 }],
        rangeStart: 10,
        rangeCount: 2,
        hasMoreBefore: true,
        hasMoreAfter: true,
      },
    )
    expect(parsed.firstRow).toBe(0)
    expect(parsed.lastRow).toBe(24)
    expect(parsed.rowCount).toBe(25)
    expect(parsed.lines.map((line) => line.from)).toEqual([10, 11])
  })

  it('reserves the block height around a page before the rest of its rows arrive', () => {
    const block = document.createElement('article')
    block.dataset.entryId = 'block-page'
    paintStoredRows(
      block,
      {
        ...stored,
        lines: [
          { from: 10, row: wireRowOf(okRow) },
          { from: 11, row: wireRowOf(abcRow) },
        ],
        artifactVersion: 'artifact-page',
        firstRow: 0,
        lastRow: 24,
        rowCount: 25,
      },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )
    const spacers = [...block.querySelectorAll<HTMLElement>('.cmd-output-spacer')]
    expect(spacers.map((el) => el.dataset.rows)).toEqual(['10', '13'])
    expect(spacers[0].style.height).toContain('calc(var(--term-cell-height, 1.2em) * 10)')
    expect(spacers[1].style.height).toContain('calc(var(--term-cell-height, 1.2em) * 13)')
    expect(block.querySelectorAll('.term-grid-row')).toHaveLength(2)
  })

  it('reserves a block whose first page has not arrived at all', () => {
    const block = document.createElement('article')
    block.dataset.entryId = 'block-unloaded'
    paintStoredRows(
      block,
      {
        lines: [],
        droppedRows: 0,
        lostRows: 0,
        truncated: null,
        artifactVersion: 'artifact-unloaded',
        firstRow: 0,
        lastRow: 4999,
        rowCount: 5000,
        sealed: true,
      },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )
    const grid = block.querySelector<HTMLElement>('.cmd-output')!
    expect(grid).not.toBeNull()
    expect(grid.querySelectorAll('.term-grid-row')).toHaveLength(0)
    const spacer = grid.querySelector<HTMLElement>('.cmd-output-spacer')!
    expect(spacer.dataset.rows).toBe('5000')
    expect(spacer.style.height).toContain('* 5000)')
    // An unloaded block printed nothing is a different fact from a block
    // whose page has not arrived; the second must not say the first.
    expect(block.querySelector('[data-output-empty]')).toBeNull()
  })

  it('tracks loaded rows as ranges, so an interior hole is still missing', () => {
    const loaded = loadedRowRanges([{ from: 0 }, { from: 1 }, { from: 4800 }, { from: 4801 }])
    expect(loaded).toEqual([
      { from: 0, to: 2 },
      { from: 4800, to: 4802 },
    ])
    expect(missingRowRanges([{ from: 0, to: 5000 }], loaded)).toEqual([
      { from: 2, to: 4800 },
      { from: 4802, to: 5000 },
    ])
  })

  it('fetches an interior hole when the reader reaches it, not because a tail is loaded', () => {
    const retained = [{ from: 0, to: 5000 }]
    const loaded = [
      { from: 0, to: 200 },
      { from: 4800, to: 5000 },
    ]
    expect(nextPageForRows(retained, loaded, 4800, 200)).toBeNull()
    const page = nextPageForRows(retained, loaded, 2500, 200)!
    expect(page.fromRow).toBeGreaterThanOrEqual(200)
    expect(page.fromRow + page.rowLimit).toBeLessThanOrEqual(4800)
    expect(page.rowLimit).toBe(200)
  })

  it('reserves a disjoint page set at the artifact height, not at the pages between them', () => {
    const block = document.createElement('article')
    block.dataset.entryId = 'disjoint'
    paintStoredRows(
      block,
      {
        ...stored,
        lines: [
          { from: 0, row: wireRowOf(okRow) },
          { from: 1, row: wireRowOf(abcRow) },
          { from: 4800, row: wireRowOf(okRow) },
          { from: 4801, row: wireRowOf(abcRow) },
        ],
        artifactVersion: 'artifact-disjoint',
        firstRow: 0,
        lastRow: 4999,
        endRow: 5000,
        rowCount: 5000,
        retainedRanges: [{ from: 0, to: 5000 }],
        sealed: true,
      },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )
    expect(block.querySelectorAll('.term-grid-row')).toHaveLength(4)
    expect(block.querySelectorAll('.cmd-output-gap')).toHaveLength(0)
    const spacer = block.querySelector<HTMLElement>('.cmd-output-spacer')!
    expect(spacer.dataset.rows).toBe('4798')
    expect(spacer.dataset.logicalLine).toBe('2')
    expect(block.querySelector<HTMLElement>('.cmd-output')!.dataset.rowsLoaded).toBe('4')
  })

  it('keeps the reservation and the rows when a page is entirely blank', () => {
    const block = document.createElement('article')
    block.dataset.entryId = 'blank-page'
    const body = Array.from({ length: 200 }, (_, i) =>
      JSON.stringify({ from: i, row: { text: '' } }),
    ).join('\n')
    const parsed = parseStoredBlockRows(
      body,
      {
        truncated: null,
        payload: { rowCount: 5000, endRow: 5000, retainedRanges: [{ from: 0, to: 5000 }] },
        state: 'sealed',
      },
      'artifact-blank',
      {
        firstRow: 0,
        lastRow: 4999,
        endRow: 5000,
        rowCount: 5000,
        retainedRanges: [{ from: 0, to: 5000 }],
        rangeStart: 0,
        rangeCount: 200,
        hasMoreBefore: false,
        hasMoreAfter: true,
      },
    )
    paintStoredRows(block, parsed, { metric: null, palette: DEFAULT_SNAPSHOT })
    const output = block.querySelector<HTMLElement>('.cmd-output')
    expect(output).not.toBeNull()
    expect(output!.querySelectorAll('.term-grid-row')).toHaveLength(200)
    expect(output!.querySelector<HTMLElement>('.cmd-output-spacer')!.dataset.rows).toBe('4800')
  })

  it('maps a compressed capped gap to the first row of the tail, not to its visual offset', () => {
    const rows: StoredBlockRows = {
      lines: [],
      droppedRows: 4400,
      lostRows: 0,
      truncated: 'cap',
      sealed: true,
      firstRow: 0,
      lastRow: 4999,
      endRow: 5000,
      rowCount: 600,
      retainedRanges: [
        { from: 0, to: 300 },
        { from: 4700, to: 5000 },
      ],
      reserve: true,
    }
    expect(streamRowAtVisualOffset(rows, 0)).toBe(0)
    expect(streamRowAtVisualOffset(rows, 299)).toBe(299)
    expect(streamRowAtVisualOffset(rows, 300)).toBe(4700)
    expect(streamRowAtVisualOffset(rows, 301)).toBe(4700)
    expect(streamRowAtVisualOffset(rows, 600)).toBe(4999)
  })

  it('reserves a capped block from its retained runs, one notice on the hole', () => {
    const block = document.createElement('article')
    block.dataset.entryId = 'capped'
    paintStoredRows(
      block,
      {
        ...stored,
        lines: [],
        artifactVersion: 'artifact-capped',
        firstRow: 0,
        lastRow: 4999,
        endRow: 5000,
        rowCount: 600,
        retainedRanges: [
          { from: 0, to: 300 },
          { from: 4700, to: 5000 },
        ],
        sealed: true,
        truncated: 'cap',
        droppedRows: 4400,
      },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )
    const spacers = [...block.querySelectorAll<HTMLElement>('.cmd-output-spacer')]
    expect(spacers.map((el) => el.dataset.rows)).toEqual(['300', '300'])
    expect(spacers.map((el) => el.dataset.logicalLine)).toEqual(['0', '4700'])
    expect(block.querySelectorAll('.cmd-output-gap')).toHaveLength(1)
    expect(block.querySelector('[data-output-empty]')).toBeNull()
    expect(block.querySelector<HTMLElement>('.cmd-output')!.dataset.rowsCount).toBe('600')
  })

  it("keeps a running block's body element when it has geometry but no lines yet", () => {
    const block = document.createElement('article')
    paintStoredRows(
      block,
      {
        lines: [],
        droppedRows: 0,
        lostRows: 0,
        truncated: null,
        sealed: false,
        firstRow: 0,
        lastRow: 4999,
        endRow: 5000,
        rowCount: 5000,
        retainedRanges: [{ from: 0, to: 5000 }],
        reserve: false,
      },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )
    const output = block.querySelector<HTMLElement>('.cmd-output')
    expect(output).not.toBeNull()
    expect(output!.dataset.rowsLoaded).toBe('0')
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

  it('replaces a restored eviction sentence with the rows that are its real body', () => {
    // A command whose body is a rows artifact answers `body: null` to the
    // restore, so the block is built saying "Output is no longer kept"; the
    // artifact's rows are then the body. Keeping both leaves a person
    // reading real output and its own obituary side by side, and leaves two
    // `.cmd-output` elements where the product has one body.
    const container = document.createElement('div')
    const block = restoredBlock(
      {
        id: 1,
        command: 'make test',
        cwd: '/repo',
        location: '',
        durationMs: 1200,
        exitCode: 0,
        status: 'success',
        body: null,
        author: 'shell',
        kind: 'command',
        entryId: 'entry-evicted',
      },
      DEFAULT_SNAPSHOT,
      () => container,
      () => {},
      new CommandSnapshotStore(),
    )
    expect(block.querySelector('.cmd-output-evicted')).not.toBeNull()

    paintStoredRows(
      block,
      { ...stored, artifactVersion: 'v1' },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )

    const outputs = [...block.querySelectorAll('.cmd-output')]
    expect(outputs).toHaveLength(1)
    expect(outputs[0].textContent).toContain('ok')
    expect(outputs[0].classList.contains('cmd-output-evicted')).toBe(false)
    expect(block.dataset.outputEvicted).toBeUndefined()
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

  describe('paged retention', () => {
    const options = { metric: null, palette: DEFAULT_SNAPSHOT, reserve: false }
    const page = (
      from: number,
      count: number,
      retainedRanges: { from: number; to: number }[],
      artifactVersion = 'paged-artifact',
    ): StoredBlockRows =>
      parseStoredBlockRows(
        Array.from({ length: count }, (_, index) =>
          JSON.stringify({ from: from + index, row: { text: `row ${from + index}` } }),
        ).join('\n'),
        {
          state: 'open',
          truncated: retainedRanges.length > 1 ? 'cap' : null,
          payload: {
            retainedRanges,
            rowCount: retainedRanges.reduce((total, range) => total + range.to - range.from, 0),
            endRow: retainedRanges[retainedRanges.length - 1]?.to ?? from,
          },
        },
        artifactVersion,
      )
    const rowNumbers = (block: HTMLElement) =>
      [...block.querySelectorAll<HTMLElement>('.term-grid-row')].map((el) =>
        Number(el.dataset.logicalLine),
      )

    it('removes evicted loaded rows from a running body and selection while keeping its head', () => {
      const block = document.createElement('article')
      block.dataset.entryId = 'capped-running'
      const previous = page(0, 4, [{ from: 0, to: 4 }])
      paintStoredRows(block, previous, options)
      const head = block.querySelector('.term-grid-row')
      const next = page(8, 2, [
        { from: 0, to: 2 },
        { from: 8, to: 10 },
      ])
      const merged = mergeStoredBlockRows(previous, next)
      paintStoredRows(block, merged, options)

      expect(merged.lines.map((line) => line.from)).toEqual([0, 1, 8, 9])
      expect(rowNumbers(block)).toEqual([0, 1, 8, 9])
      expect(block.querySelector('.term-grid-row')).toBe(head)
      expect(block.querySelectorAll('.cmd-output-gap')).toHaveLength(1)
      expect(block.querySelector('.cmd-output-gap')?.textContent).toContain('6')
      expect(storedBlockRowsForSelection(block, 'paged-artifact')?.lines).toEqual(merged.lines)
      expect(storedBlockRowsText(merged)).not.toContain('row 2')
      expect(previous.lines.map((line) => line.from)).toEqual([0, 1, 2, 3])
    })

    it('bounds cached and painted rows across many cap-crossing batches', () => {
      const block = document.createElement('article')
      block.dataset.entryId = 'capped-batches'
      let current: StoredBlockRows | undefined
      for (let from = 0; from < 400; from += 2) {
        const end = from + 2
        const ranges =
          end <= 8
            ? [{ from: 0, to: end }]
            : [
                { from: 0, to: 4 },
                { from: end - 4, to: end },
              ]
        current = mergeStoredBlockRows(current, page(from, 2, ranges))
        paintStoredRows(block, current, options)
        const expected =
          end <= 8
            ? Array.from({ length: end }, (_, index) => index)
            : [0, 1, 2, 3, end - 4, end - 3, end - 2, end - 1]
        expect(current.lines.map((line) => line.from)).toEqual(expected)
        expect(rowNumbers(block)).toEqual(expected)
        expect(block.querySelectorAll('.cmd-output-gap')).toHaveLength(end > 8 ? 1 : 0)
        expect(storedBlockRowsForSelection(block, 'paged-artifact')?.lines).toHaveLength(
          Math.min(8, end),
        )
      }
    })

    it('preserves loaded pages and unloaded retained ranges, replacing only overlapping rows', () => {
      const ranges = [{ from: 0, to: 100 }]
      const previous = mergeStoredBlockRows(page(0, 2, ranges), page(50, 2, ranges))
      const next = page(98, 2, ranges)
      const merged = mergeStoredBlockRows(previous, next)
      expect(merged.lines.map((line) => line.from)).toEqual([0, 1, 50, 51, 98, 99])
      expect(merged.retainedRanges).toEqual(ranges)
      expect(missingRowRanges(merged.retainedRanges!, loadedRowRanges(merged.lines))).toEqual([
        { from: 2, to: 50 },
        { from: 52, to: 98 },
      ])
      const replacement = { from: 50, row: { text: 'updated' } }
      expect(mergeStoredBlockRows(merged, { ...next, lines: [replacement] }).lines[2]).toBe(
        replacement,
      )
      const block = document.createElement('article')
      paintStoredRows(block, merged, { ...options, reserve: true })
      expect(
        [...block.querySelectorAll<HTMLElement>('.cmd-output-spacer')].map((el) => el.dataset.rows),
      ).toEqual(['48', '46'])
      expect(block.querySelectorAll('.cmd-output-gap')).toHaveLength(0)
    })

    it('honours explicitly empty retained ranges rather than resurrecting previous geometry', () => {
      const block = document.createElement('article')
      block.dataset.entryId = 'fully-evicted'
      const previous = page(0, 4, [{ from: 0, to: 4 }])
      paintStoredRows(block, previous, options)
      const next = page(4, 0, [])
      const merged = mergeStoredBlockRows(previous, next)
      paintStoredRows(block, merged, options)
      expect(merged.lines).toEqual([])
      expect(merged.retainedRanges).toEqual([])
      expect(merged.rowCount).toBe(0)
      expect(merged.endRow).toBe(4)
      expect(block.querySelector('.cmd-output')).toBeNull()
      expect(storedBlockRowsForSelection(block, 'paged-artifact')?.lines).toEqual([])
    })

    it('replaces a different artifact identity and keeps geometry-less legacy reads additive', () => {
      const previous = page(0, 4, [{ from: 0, to: 4 }])
      const next = page(8, 2, [{ from: 8, to: 10 }], 'replacement-artifact')
      expect(mergeStoredBlockRows(previous, next)).toBe(next)
      expect(mergeStoredBlockRows(undefined, next)).toBe(next)
      expect(mergeStoredBlockRows({ ...previous, artifactVersion: undefined }, next)).toBe(next)
      const legacy = { ...previous, lines: [{ from: 8, row: { text: 'legacy' } }] }
      delete legacy.retainedRanges
      const merged = mergeStoredBlockRows(previous, legacy)
      expect(merged.lines.map((line) => line.from)).toEqual([0, 1, 2, 3, 8])
      expect(merged.retainedRanges).toEqual(previous.retainedRanges)
    })
  })

  describe('incremental retained output', () => {
    const options = { metric: null, palette: DEFAULT_SNAPSHOT }
    const artifact = (
      lines: readonly LedgerBlockRowsLine[],
      artifactVersion = 'v1',
    ): StoredBlockRows => ({ ...stored, lines, artifactVersion })
    const line = (from: number, text: string): LedgerBlockRowsLine => ({ from, row: { text } })
    const rowsOf = (block: HTMLElement) => [
      ...block.querySelectorAll<HTMLElement>('.term-grid-row'),
    ]

    it('keeps selected text, links and scroll position while warming only appended rows', () => {
      const block = document.createElement('article')
      block.dataset.entryId = 'growing'
      document.body.appendChild(block)
      const text = 'see docs/architecture.md:101'
      paintStoredRows(block, artifact([line(40, text)]), options)
      const output = block.querySelector<HTMLElement>('.cmd-output')!
      const first = rowsOf(block)[0]
      const link = first.querySelector('.term-link')!
      const selectedText = link.firstChild!
      const selection = window.getSelection()!
      const range = document.createRange()
      range.setStart(selectedText, 0)
      range.setEnd(selectedText, 4)
      selection.removeAllRanges()
      selection.addRange(range)
      output.scrollLeft = 19
      const warmed: string[] = []
      try {
        paintStoredRows(block, artifact([line(40, text), line(41, 'new')], 'v2'), {
          ...options,
          palette: { ...DEFAULT_SNAPSHOT, palette: [...DEFAULT_SNAPSHOT.palette] },
          warm: (candidates) => {
            for (const candidate of candidates) warmed.push(candidate.chars)
          },
        })
        expect(block.querySelector('.cmd-output')).toBe(output)
        expect(rowsOf(block)[0]).toBe(first)
        expect(first.querySelector('.term-link')).toBe(link)
        expect(selection.anchorNode).toBe(selectedText)
        expect(selection.toString()).toBe('docs')
        expect(output.scrollLeft).toBe(19)
        expect(rowsOf(block)[1].textContent?.trimEnd()).toBe('new')
        expect(warmed).toEqual(['n', 'e', 'w'])
        expect(first.dataset.artifactVersion).toBe('v2')
        expect(storedBlockRowsForSelection(block, 'v1')).toBeNull()
        expect(storedBlockRowsForSelection(block, 'v2')?.lines).toHaveLength(2)
      } finally {
        selection.removeAllRanges()
        block.remove()
      }
    })

    it('reconciles by ledger position through prefix eviction, replacement and insertion', () => {
      const block = document.createElement('article')
      paintStoredRows(block, artifact([line(10, 'aaa'), line(11, 'bbb'), line(13, 'ddd')]), options)
      const [evicted, retained, changed] = rowsOf(block)
      const output = block.querySelector('.cmd-output')
      paintStoredRows(
        block,
        artifact([line(11, 'bbb'), line(12, 'ccc'), line(13, 'new')], 'v2'),
        options,
      )
      expect(block.querySelector('.cmd-output')).toBe(output)
      expect(rowsOf(block).map((row) => row.textContent)).toEqual(['bbb', 'ccc', 'new'])
      expect(rowsOf(block).map((row) => row.dataset.logicalLine)).toEqual(['11', '12', '13'])
      expect(rowsOf(block)[0]).toBe(retained)
      expect(block.contains(evicted)).toBe(false)
      expect(block.contains(changed)).toBe(false)
    })

    it('updates compact marks, runs and wrap facts without repainting an unaffected line', () => {
      const block = document.createElement('article')
      block.dataset.entryId = 'compact'
      const keeper = line(2, 'xxxx')
      paintStoredRows(block, artifact([line(1, 'ab'), keeper]), options)
      const retained = rowsOf(block)[1]
      const variants: LedgerBlockRowsLine['row'][] = [
        { text: 'ab', marks: [[0, 1, 2]] },
        { text: 'ab', marks: [[0, 1, 2]], runs: [[[0, 257 + 0x010203, 0, 0, 0], 3]] },
        { text: 'ab', marks: [[0, 1, 2]], runs: [[[0, 257 + 0x010203, 0, 0, 0], 3]], wrap: true },
        {
          text: 'ab',
          marks: [[0, 1, 2]],
          runs: [[[0, 257 + 0x010203, 0, 0, 0], 3]],
          continuation: true,
        },
      ]
      for (const [index, row] of variants.entries()) {
        const version = `compact-${index}`
        paintStoredRows(block, artifact([{ from: 1, row }, line(2, 'xxxx')], version), options)
        expect(rowsOf(block)[1]).toBe(retained)
        expect(rowsOf(block)[0].textContent).toBe('ab ')
        expect(storedBlockRowsForSelection(block, version)?.lines[0].row).toEqual(row)
        if (row.runs)
          expect(rowsOf(block)[0].querySelector('span')?.style.background).toBe('rgb(1, 2, 3)')
      }
    })

    it('repads all lines when the widest retained row grows or is evicted', () => {
      const block = document.createElement('article')
      paintStoredRows(block, artifact([line(1, 'a'), line(2, 'bb')]), options)
      const output = block.querySelector('.cmd-output')
      paintStoredRows(block, artifact([line(1, 'a'), line(2, 'bb'), line(3, 'cccc')]), options)
      expect(block.querySelector('.cmd-output')).toBe(output)
      expect(rowsOf(block).map((row) => row.textContent)).toEqual(['a   ', 'bb  ', 'cccc'])
      paintStoredRows(block, artifact([line(1, 'a'), line(2, 'bb')]), options)
      expect(rowsOf(block).map((row) => row.textContent)).toEqual(['a ', 'bb'])
    })

    it('updates notices and artifact metadata without replacing retained rows', () => {
      const block = document.createElement('article')
      block.dataset.entryId = 'metadata'
      paintStoredRows(block, artifact([line(1, 'abc')]), options)
      const row = rowsOf(block)[0]
      paintUnreadableRows(block)
      paintStoredRows(block, { ...artifact([line(1, 'abc')], 'v2'), lostRows: 2 }, options)
      expect(rowsOf(block)[0]).toBe(row)
      expect(row.dataset.artifactVersion).toBe('v2')
      expect(block.querySelector('[data-output-unreadable]')).toBeNull()
      expect(block.querySelector('[data-output-incomplete]')?.textContent).toContain('lost 2 times')
      paintStoredRows(block, artifact([line(1, 'abc')], 'v3'), options)
      expect(rowsOf(block)[0]).toBe(row)
      expect(block.querySelector('[data-output-incomplete]')).toBeNull()
      paintStoredRows(block, { ...stored, lines: [line(1, 'abc')] }, options)
      expect(rowsOf(block)[0]).toBe(row)
      expect(row.dataset.artifactVersion).toBeUndefined()
      expect(storedBlockRowsForSelection(block, 'v3')).toBeNull()
    })

    it('drops old paint state on empty or malformed reads and recovers on later output', () => {
      const block = document.createElement('article')
      paintStoredRows(block, artifact([line(1, 'abc')]), options)
      paintStoredRows(block, { ...artifact([]), sealed: true }, options)
      expect(block.querySelector('.cmd-output')).toBeNull()
      expect(block.querySelector('[data-output-empty]')).not.toBeNull()
      paintUnreadableRows(block)
      paintStoredRows(block, artifact([line(1, 'abc')]), options)
      expect(rowsOf(block)[0].textContent).toBe('abc')
      expect(block.querySelector('[data-output-empty], [data-output-unreadable]')).toBeNull()
      paintStoredRows(block, artifact([{ from: 1, row: { text: 'abc', runs: [[0, 1]] } }]), options)
      // A malformed row paints no rectangle, but the body and its height stay:
      // removing the output element collapsed the reservation under the reader
      // (ADR-ywzcpbqe).
      expect(block.querySelector('.cmd-output')).not.toBeNull()
      expect(block.querySelectorAll('.term-grid-row')).toHaveLength(0)
      paintStoredRows(block, artifact([line(1, 'abc')]), options)
      expect(rowsOf(block)[0].textContent).toBe('abc')
    })

    it('compares metric values and palette colors, and refreshes its state after font repaint', () => {
      const container = document.createElement('div')
      const block = document.createElement('article')
      block.dataset.entryId = 'style-repaint'
      container.appendChild(block)
      const metric = (signature: string, advance: number): RunMetric => ({
        cellWidth: 10,
        defaultSpacing: 0,
        signature,
        advanceOf: () => advance,
      })
      const colored: LedgerBlockRowsLine = {
        from: 1,
        row: { text: 'abc', runs: [[[2, 0, 0, 0, 0], 3]] },
      }
      const initial = artifact([colored])
      paintStoredRows(block, initial, { ...options, metric: metric('font-a', 8) })
      const first = rowsOf(block)[0]
      paintStoredRows(block, artifact([colored], 'v2'), {
        metric: metric('font-a', 8),
        palette: { ...DEFAULT_SNAPSHOT, palette: [...DEFAULT_SNAPSHOT.palette] },
      })
      expect(rowsOf(block)[0]).toBe(first)
      expect(first.querySelector('span')?.style.letterSpacing).toBe('2px')
      repaintStoredRows(container, { ...options, metric: metric('font-b', 6) })
      const repainted = rowsOf(block)[0]
      expect(repainted.querySelector('span')?.style.letterSpacing).toBe('4px')
      paintStoredRows(block, artifact([colored, line(2, 'new')], 'v3'), {
        ...options,
        metric: metric('font-b', 6),
      })
      expect(rowsOf(block)[0]).toBe(repainted)
      const colors: [...TerminalSnapshot['palette']] = [...DEFAULT_SNAPSHOT.palette]
      colors[1] = '#abcdef'
      const palette = { ...DEFAULT_SNAPSHOT, palette: colors }
      paintStoredRows(block, artifact([colored]), { palette, metric: metric('font-b', 6) })
      expect(rowsOf(block)[0].querySelector('span')?.style.color).toBe('rgb(171, 205, 239)')
    })
  })

  it('preserves painted whitespace, links and procedural ink without needing DOM paint', () => {
    const rows: StoredBlockRows = {
      ...stored,
      lines: [
        { from: 10, row: { text: ' 界x ', marks: [[1, 1, 2]] } },
        { from: 11, row: { text: 'next' } },
        { from: 12, row: { text: 'docs/a.md' } },
        {
          from: 13,
          row: wireRowOf([
            ['█', 1, true],
            ['', 1, false],
            ['a', 1, true],
            ['', 4, false],
          ]),
        },
      ],
      lostRows: 2,
    }
    const expected = [
      ` 界x ${' '.repeat(4)}`,
      `next${' '.repeat(5)}`,
      'docs/a.md',
      `█ a${' '.repeat(6)}`,
    ].join('\n')
    expect(storedBlockRowsText(rows)).toBe(expected)
    const block = document.createElement('article')
    paintStoredRows(block, rows, {
      metric: {
        cellWidth: 10,
        defaultSpacing: 0,
        pitch: 20,
        signature: 'text-equivalence',
        advanceOf: (_chars, columns) => columns * 10,
      },
      palette: DEFAULT_SNAPSHOT,
    })
    expect(block.querySelector('.term-link')?.textContent).toBe('docs/a.md')
    expect(block.querySelector('[data-procedural] .term-cell-glyph')?.textContent).toBe('█')
    expect(blockOutputText(block)).toBe(expected)
    expect(storedBlockRowsText(rows)).toBe(blockOutputText(block))
  })

  // THE CUT IS WHERE A PERSON SEES IT (nocx-ipuy3). A stored block holds the
  // rows the store kept, and when the per-command limit dropped a middle
  // stretch, the rows either side of the hole are still here with a gap in
  // their absolute row numbers. That gap is the only place in the product
  // that says WHERE the output was cut, so the one quiet sentence a person
  // reads belongs there — at the place they scroll to — instead of a card
  // above the pane announcing a session-level accounting nobody can point at.
  // The owner's decision of 2026-11-08 removed that card altogether.
  describe('the cut a command’s stored rows carry (nocx-ipuy3)', () => {
    const rowOf = (text: string): LedgerBlockRowsLine['row'] =>
      wireRowOf([...text].map((char) => [char, 1, true] as CellSpec))

    const cutStored = (over: Partial<StoredBlockRows> = {}): StoredBlockRows => ({
      lines: [
        { from: 0, row: rowOf('before') },
        { from: 1, row: rowOf('cut-here') },
        { from: 900, row: rowOf('after') },
      ],
      droppedRows: 898,
      lostRows: 0,
      truncated: 'cap',
      sealed: true,
      ...over,
    })

    const gapsOf = (block: HTMLElement): HTMLElement[] => [
      ...block.querySelectorAll<HTMLElement>('.cmd-output-gap'),
    ]

    it('puts one quiet line between the rows either side of the cut', () => {
      const block = document.createElement('article')

      paintStoredRows(block, cutStored(), { metric: null, palette: DEFAULT_SNAPSHOT })

      const [gap] = gapsOf(block)
      expect(gapsOf(block)).toHaveLength(1)
      expect(gap.previousElementSibling?.textContent).toBe('cut-here')
      expect(gap.nextElementSibling?.textContent?.trim()).toBe('after')
    })

    it("names this session's size limit and the rows it took", () => {
      const block = document.createElement('article')

      paintStoredRows(block, cutStored(), { metric: null, palette: DEFAULT_SNAPSHOT })

      expect(gapsOf(block)[0].textContent).toBe(
        "Earlier output was not kept: this session's size limit dropped 898 rows here.",
      )
    })

    it('says one row when the cut took one', () => {
      const block = document.createElement('article')

      paintStoredRows(
        block,
        cutStored({
          lines: [
            { from: 0, row: rowOf('before') },
            { from: 2, row: rowOf('after') },
          ],
          droppedRows: 1,
        }),
        { metric: null, palette: DEFAULT_SNAPSHOT },
      )

      expect(gapsOf(block)[0].textContent).toBe(
        "Earlier output was not kept: this session's size limit dropped 1 row here.",
      )
    })

    it('says only that the rows were not kept when the store has named no cause yet', () => {
      // An artifact is open while its command runs, and the verdict that it
      // was the cap arrives with the seal. The rows are already gone, so the
      // line is already at the cut; it may not blame a limit the store has
      // not named.
      const block = document.createElement('article')

      paintStoredRows(block, cutStored({ truncated: null, sealed: false }), {
        metric: null,
        palette: DEFAULT_SNAPSHOT,
      })

      expect(gapsOf(block)[0].textContent).toBe('898 rows of earlier output were not kept here.')
    })

    it('does not blame the size limit for a stream that overflowed', () => {
      const block = document.createElement('article')

      paintStoredRows(block, cutStored({ truncated: 'gap', droppedRows: 0 }), {
        metric: null,
        palette: DEFAULT_SNAPSHOT,
      })

      expect(gapsOf(block)[0].textContent).toBe('898 rows of earlier output were not kept here.')
    })

    it('shows no line at all in a block whose rows are whole', () => {
      const block = document.createElement('article')

      paintStoredRows(
        block,
        { ...stored, sealed: true },
        {
          metric: null,
          palette: DEFAULT_SNAPSHOT,
        },
      )

      expect(gapsOf(block)).toHaveLength(0)
    })

    it('never puts a line before a block’s first row, which may start past zero', () => {
      // A block begins where its command began; rows that departed before it
      // belonged to no block, and the gap before the first row is not a cut
      // in this command's output.
      const block = document.createElement('article')

      paintStoredRows(
        block,
        { ...stored, sealed: true },
        {
          metric: null,
          palette: DEFAULT_SNAPSHOT,
        },
      )

      expect(stored.lines[0].from).toBeGreaterThan(0)
      expect(gapsOf(block)).toHaveLength(0)
    })

    it('keeps the line at the cut while later rows arrive and the cut widens', () => {
      const block = document.createElement('article')
      paintStoredRows(block, cutStored(), { metric: null, palette: DEFAULT_SNAPSHOT })
      const painted = gapsOf(block)[0]

      paintStoredRows(
        block,
        cutStored({
          lines: [
            { from: 0, row: rowOf('before') },
            { from: 1, row: rowOf('cut-here') },
            { from: 1900, row: rowOf('after') },
            { from: 1901, row: rowOf('later') },
          ],
          droppedRows: 1898,
        }),
        { metric: null, palette: DEFAULT_SNAPSHOT },
      )

      const [gap] = gapsOf(block)
      expect(gapsOf(block)).toHaveLength(1)
      expect(gap).toBe(painted)
      expect(gap.previousElementSibling?.textContent).toBe('cut-here')
      expect(gap.textContent).toBe(
        "Earlier output was not kept: this session's size limit dropped 1898 rows here.",
      )
    })

    it('drops the line with the rows when a later read holds a whole body', () => {
      const block = document.createElement('article')
      paintStoredRows(block, cutStored(), { metric: null, palette: DEFAULT_SNAPSHOT })
      expect(gapsOf(block)).toHaveLength(1)

      paintStoredRows(
        block,
        { ...stored, sealed: true },
        {
          metric: null,
          palette: DEFAULT_SNAPSHOT,
        },
      )

      expect(gapsOf(block)).toHaveLength(0)
    })

    it('keeps the line out of a copy of the output, which is the program’s text', () => {
      const block = document.createElement('article')

      paintStoredRows(block, cutStored(), { metric: null, palette: DEFAULT_SNAPSHOT })

      const copied = blockOutputText(block)
      expect(copied).toContain('cut-here')
      expect(copied).toContain('after')
      expect(copied).not.toContain('Earlier output was not kept')
    })

    it('is the block’s body for a read with no rows at all, never a stray line', () => {
      const block = document.createElement('article')

      paintStoredRows(block, cutStored({ lines: [], truncated: 'cap' }), {
        metric: null,
        palette: DEFAULT_SNAPSHOT,
      })

      expect(gapsOf(block)).toHaveLength(0)
    })
  })

  it('uses the same missing-output sentences only when no readable rows exist', () => {
    const missing: StoredBlockRows = {
      ...stored,
      lines: [],
      droppedRows: 3,
      truncated: 'cap',
      lostRows: 2,
    }
    const block = document.createElement('article')
    paintStoredRows(block, missing, { metric: null, palette: DEFAULT_SNAPSHOT })
    expect(storedBlockRowsText(missing)).toBe(blockOutputText(block))
    expect(storedBlockRowsText(missing)).toContain('3 rows are missing')
    expect(storedBlockRowsText(missing)).toContain('lost 2 times')
    expect(storedBlockRowsText({ ...stored, lines: [], sealed: true })).toBe('')
    expect(storedBlockRowsText({ ...stored, lines: [] })).toBe('')
  })

  // The causes reach the card separately (ledger.get carries the artifact's
  // truncated reason and the payload summary's two counts), so the card says
  // WHY rows are missing, one cause at a time, each with its count where the
  // store counted one (nocx-zg3k3.5.5). A cause that arrives later — rows
  // lost while the coordinator was away, say — is one more table entry in
  // block-rows.ts, not a second derivation.
  // THE CAP IS SAID AT THE CUT WHEN THERE IS ONE (nocx-ipuy3). The notice
  // under the block is for a body that has no rows left to carry the line;
  // a block whose rows do carry it states the cap once, at the place.
  it('names the cap cause with its count and the setting that raises it, when no rows are left', () => {
    const block = document.createElement('article')

    paintStoredRows(
      block,
      { ...stored, lines: [], droppedRows: 3, truncated: 'cap', sealed: true },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )

    expect(block.querySelector('[data-output-incomplete]')?.textContent).toBe(
      'Output incomplete: 3 rows are missing — the output passed the history output limit. Raise the history.outputCapKB setting to keep more.',
    )
  })

  it('says the cap only at the cut, and never twice, when the block kept rows', () => {
    const block = document.createElement('article')

    paintStoredRows(
      block,
      {
        ...stored,
        lines: [
          { from: 0, row: wireRowOf(okRow) },
          { from: 900, row: wireRowOf(abcRow) },
        ],
        droppedRows: 899,
        truncated: 'cap',
        sealed: true,
      },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )

    expect(block.querySelector('[data-output-incomplete]')).toBeNull()
    expect(block.querySelector('.cmd-output-gap')?.textContent).toBe(
      "Earlier output was not kept: this session's size limit dropped 899 rows here.",
    )
  })

  it('names the cap cause without a count while the block is still open and holds no rows', () => {
    const block = document.createElement('article')

    paintStoredRows(
      block,
      { ...stored, lines: [], truncated: 'cap' },
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

  it('names two causes at once, each with its own count, when no rows are left', () => {
    const block = document.createElement('article')

    paintStoredRows(
      block,
      { ...stored, lines: [], droppedRows: 3, lostRows: 2, truncated: 'cap', sealed: true },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )

    const text = block.querySelector('[data-output-incomplete]')?.textContent ?? ''
    expect(text).toContain('3 rows are missing')
    expect(text).toContain('the output passed the history output limit')
    expect(text).toContain('output was lost 2 times')
  })

  it('keeps naming a loss at the block when a cut carries the cap', () => {
    const block = document.createElement('article')

    paintStoredRows(
      block,
      {
        ...stored,
        lines: [
          { from: 0, row: wireRowOf(okRow) },
          { from: 900, row: wireRowOf(abcRow) },
        ],
        droppedRows: 899,
        lostRows: 2,
        truncated: 'cap',
        sealed: true,
      },
      { metric: null, palette: DEFAULT_SNAPSHOT },
    )

    const text = block.querySelector('[data-output-incomplete]')?.textContent ?? ''
    expect(text).toContain('output was lost 2 times')
    expect(text).not.toContain('history output limit')
    expect(block.querySelector('.cmd-output-gap')?.textContent).toContain(
      "this session's size limit dropped 899 rows here",
    )
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
    // The states a block can be read in (nocx-zg3k3.5.5): complete, capture
    // refused, incomplete with EACH cause, evicted, empty, unreadable. Each
    // must carry visible words, and no two may share them — a person staring
    // at any block must be able to tell which state it is in without opening
    // anything else. The cap's words are AT ITS CUT now (nocx-ipuy3), so a
    // cap state is painted with the rows a cut leaves.
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
    const cutRows = [
      { from: 12, row: wireRowOf(okRow) },
      { from: 900, row: wireRowOf(abcRow) },
    ] satisfies LedgerBlockRowsLine[]
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
      ['cap', paint({ lines: cutRows, droppedRows: 887, truncated: 'cap' })],
      ['lost', paint({ lostRows: 2 })],
      ['gap', paint({ truncated: 'gap' })],
      ['suppressed', paint({ truncated: 'suppressed' })],
      ['two causes', paint({ lines: cutRows, droppedRows: 887, lostRows: 2, truncated: 'cap' })],
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

  // THE CACHE'S LIFETIME (nocx-3an1s.1.6). Selection resolves rows by the
  // block element that painted them, and by the artifact version those rows
  // carry. Both halves were once a module-global Map keyed by block id plus
  // version with no deletion at all: a closed tab's rows stayed reachable, and
  // every newer read of the same block left the older artifact beside it.
  describe('the stored rows selection resolves', () => {
    it('forgets a superseded artifact version when the newer one paints', () => {
      const block = document.createElement('article')
      block.dataset.entryId = 'versioned-block'
      paintStoredRows(
        block,
        { ...stored, artifactVersion: 'artifact-v4' },
        { metric: null, palette: DEFAULT_SNAPSHOT },
      )
      expect(storedBlockRowsForSelection(block, 'artifact-v4')).not.toBeNull()

      paintStoredRows(
        block,
        { ...stored, artifactVersion: 'artifact-v5' },
        { metric: null, palette: DEFAULT_SNAPSHOT },
      )

      // A pinned lookup for the replaced artifact answers nothing: serving it
      // would map a logical row onto columns of an artifact that no longer
      // describes this block.
      expect(storedBlockRowsForSelection(block, 'artifact-v4')).toBeNull()
      expect(storedBlockRowsForSelection(block, 'artifact-v5')?.artifactVersion).toBe('artifact-v5')
    })

    it('keeps the rows with the element that painted them, never with the block id alone', () => {
      const painted = document.createElement('article')
      painted.dataset.entryId = 'shared-block'
      const other = document.createElement('article')
      other.dataset.entryId = 'shared-block'

      paintStoredRows(
        painted,
        { ...stored, artifactVersion: 'artifact-v1' },
        { metric: null, palette: DEFAULT_SNAPSHOT },
      )

      expect(storedBlockRowsForSelection(painted, 'artifact-v1')).not.toBeNull()
      // The visual freeze replaces a block's element; the replacement is its
      // own owner and has painted nothing until it does. A lookup by id alone
      // would hand it the discarded element's rows.
      expect(storedBlockRowsForSelection(other, 'artifact-v1')).toBeNull()
    })
  })
})
