// @vitest-environment jsdom

import { describe, expect, it } from 'vitest'
import type { LedgerBlockRowsLine, Row, Run, Style } from '../generated/ledger.blockRows'
import { DEFAULT_SNAPSHOT } from './serializer'
import { paintStoredRows, parseStoredBlockRows, type StoredBlockRows } from './block-rows'

const style: Style = {
  foreground: { kind: 0, palette: 0, rgb: { r: 0, g: 0, b: 0 } },
  background: { kind: 0, palette: 0, rgb: { r: 0, g: 0, b: 0 } },
  underlineColor: { kind: 0, palette: 0, rgb: { r: 0, g: 0, b: 0 } },
  attributes: 0,
  underline: 0,
}

const row: Row = {
  cells: [
    ['o', 1, true],
    ['k', 1, true],
    ['\n', 1, true],
  ],
  runs: [[style, 3] as Run],
  wrap: false,
  continuation: false,
}

const stored: StoredBlockRows = {
  lines: [
    { from: 12, row },
    {
      from: 13,
      row: {
        ...row,
        cells: [
          ['a', 1, true],
          ['b', 1, true],
          ['c', 1, true],
        ],
      },
    },
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
    const styledTail: Style = {
      ...style,
      background: { kind: 2, palette: 0, rgb: { r: 1, g: 2, b: 3 } },
    }
    const mixed: StoredBlockRows = {
      lines: [
        {
          from: 20,
          row: {
            ...row,
            cells: [['a', 1, true]],
            runs: [[style, 1] as Run],
          },
        },
        {
          from: 21,
          row: {
            ...row,
            cells: [
              ['b', 1, true],
              ['', 1, false],
            ],
            runs: [[style, 1] as Run, [styledTail, 1] as Run],
          },
        },
        {
          from: 22,
          row: {
            ...row,
            cells: [
              ['c', 1, true],
              ['d', 1, true],
              ['e', 1, true],
            ],
          },
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

  it('rejects an empty run partition before padding malformed stored rows', () => {
    const malformed = parseStoredBlockRows(
      [
        {
          from: 20,
          row: { cells: [], runs: [], wrap: false, continuation: false },
        },
        { from: 21, row },
      ]
        .map((line) => JSON.stringify(line))
        .join('\n'),
      { truncated: null, payload: {} },
    )
    const block = document.createElement('article')

    expect(() =>
      paintStoredRows(block, malformed, { metric: null, palette: DEFAULT_SNAPSHOT }),
    ).toThrow('stored row has no style runs')
  })
})
