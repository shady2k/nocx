// @vitest-environment jsdom

import { describe, expect, it } from 'vitest'
import type { LedgerBlockRowsLine, Mark } from '../generated/ledger.blockRows'
import { styleOf, wireRowOf, type CellSpec } from '../painter/fixtures'
import { DEFAULT_SNAPSHOT } from './serializer'
import { paintStoredRows, parseStoredBlockRows, type StoredBlockRows } from './block-rows'

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
  it('paints backend rows through the shared row painter', () => {
    const block = document.createElement('article')

    paintStoredRows(block, stored, { metric: null, palette: DEFAULT_SNAPSHOT })

    expect([...block.querySelectorAll('.term-grid-row')].map((el) => el.textContent)).toEqual([
      'ok\n',
      'abc',
    ])
    expect(block.querySelector('.cmd-output')?.classList.contains('cmd-output-evicted')).toBe(false)
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

  it('reports the count when the stored rows are incomplete', () => {
    const block = document.createElement('article')

    paintStoredRows(
      block,
      { ...stored, droppedRows: 2, lostRows: 1, truncated: 'cap' },
      {
        metric: null,
        palette: DEFAULT_SNAPSHOT,
      },
    )

    expect(block.textContent).toContain('Output incomplete: 3 rows missing')
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

    // The spacer paints as a space, same as every blank cell (paint-row.ts's
    // `chars: cell.hasText ? cell.grapheme : ' '`): the wide cluster's own
    // column carries the glyph, the second carries the space its spacer is.
    expect(block.querySelector('.term-grid-row')?.textContent).toBe('汉 ')
  })
})
