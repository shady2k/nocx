import { describe, expect, it } from 'vitest'
import { captureLiveSelection, createCellModel, type CellModel } from './cell-model'
import type { Row, SessionFrame, Style } from './generated/session.frame'
import { frameOf, styleOf, wireRowOf } from './painter/fixtures'

function screen(revision: number, rows: Row[] = [{ text: 'abc' }, { text: 'def' }]): SessionFrame {
  return { ...frameOf(revision, [], undefined, { cols: 6, rows: rows.length, revision: 1 }), rows }
}

function apply(model: CellModel, frame: SessionFrame) {
  const result = model.apply(frame)
  if (!result.ok) throw new Error(`Unexpected refusal: ${result.refusal.reason}`)
  return result.snapshot
}

function styledRow(): Row {
  return wireRowOf([
    ['界', 2, true, styleOf({ attributes: 1 })],
    ['', 3, false, styleOf({ attributes: 1 })],
    ['é', 1, true],
  ])
}

describe('immutable row sharing', () => {
  it.each(['marks', 'runs'] as const)('distinguishes absent %s from an empty array', (field) => {
    const model = createCellModel()
    const first = apply(model, screen(1, [{ text: 'x' }]))
    const row: Row = { text: 'x', [field]: [] }
    const result = model.apply(screen(2, [row]))
    if (field === 'runs') {
      expect(result).toEqual({
        ok: false,
        refusal: { reason: 'malformed-row', row: 0, detail: 'run-partition' },
      })
      expect(model.current()).toBe(first)
    } else {
      expect(result.ok).toBe(true)
      if (!result.ok) throw new Error('Expected valid empty marks')
      expect(result.snapshot.rows[0]).not.toBe(first.rows[0])
      expect(apply(model, screen(3, [row])).rows[0]).toBe(result.snapshot.rows[0])
    }
  })

  it('preserves exact packed RGB values in owned comparison storage', () => {
    const row = (blue: number) =>
      wireRowOf([
        [
          'x',
          1,
          true,
          styleOf({ foreground: { kind: 2, palette: 0, rgb: { r: 255, g: 255, b: blue } } }),
        ],
      ])
    const model = createCellModel()
    const first = apply(model, screen(1, [row(254)]))
    const identical = apply(model, screen(2, [row(254)]))
    expect(identical.rows[0]).toBe(first.rows[0])
    const changed = apply(model, screen(3, [row(255)]))
    expect(changed.rows[0]).not.toBe(first.rows[0])
    expect(changed.cellAt(0, 0)?.style.foreground.rgb.b).toBe(255)
    expect(first.cellAt(0, 0)?.style.foreground.rgb.b).toBe(254)
    expect(apply(model, screen(4, [row(255)])).rows[0]).toBe(changed.rows[0])
  })

  it('keeps default runs distinct from explicit style tuples with zero fields', () => {
    const model = createCellModel()
    const first = apply(model, screen(1, [{ text: 'x', runs: [[0, 1]] }]))
    const second = apply(model, screen(2, [{ text: 'x', runs: [[[0, 0, 0, 0, 0], 1]] }]))
    expect(second.rows[0]).not.toBe(first.rows[0])
    expect(second.cellAt(0, 0)?.style).toEqual(first.cellAt(0, 0)?.style)
    const third = apply(model, screen(3, [{ text: 'x', runs: [[[0, 0, 0, 0, 0], 1]] }]))
    expect(third.rows[0]).toBe(second.rows[0])
    expect(apply(model, screen(4, [{ text: 'x', runs: [[0, 1]] }])).rows[0]).not.toBe(third.rows[0])
  })

  it('shares identical rows while advancing cursor, revision and every frame state fact', () => {
    const model = createCellModel()
    const first = apply(model, screen(1))
    const next = screen(2)
    next.cursor = { x: 2, y: 1, visible: true }
    next.state = {
      ...next.state,
      bracketedPaste: true,
      mouseTracking: true,
      pointerShape: 'wait',
      title: 'working',
      cwd: { host: 'server', path: '/work' },
    }
    const second = apply(model, next)
    expect(second).not.toBe(first)
    expect(second.revision).toBe(2)
    expect(second.cursor).toEqual(next.cursor)
    expect(second.state).toEqual(next.state)
    for (let r = 0; r < first.rows.length; r++) {
      expect(second.rowAt(r)).toBe(first.rowAt(r))
      expect(second.rowAt(r)?.cells).toBe(first.rowAt(r)?.cells)
      expect(Object.isFrozen(second.rowAt(r)?.cells)).toBe(true)
    }
    next.state.cwd!.path = '/mutated'
    expect(second.state.cwd?.path).toBe('/work')
    expect(Object.isFrozen(second.state.cwd)).toBe(true)
    expect(first.cursor.visible).toBe(false)
    expect(first.state.title).toBeNull()
  })

  it('keeps pinned snapshots and selections unchanged through sparse and full replacement', () => {
    const model = createCellModel()
    const first = apply(model, screen(1))
    const selection = captureLiveSelection(
      'pane',
      first,
      { row: 0, offset: 0 },
      { row: 1, offset: 3 },
    )
    const sparse = apply(model, screen(2, [{ text: 'abc' }, { text: 'new' }]))
    expect(sparse.rows[0]).toBe(first.rows[0])
    expect(sparse.rows[1]).not.toBe(first.rows[1])
    const full = apply(model, screen(3, [{ text: 'all' }, { text: 'newer' }]))
    expect(full.rows[0]).not.toBe(sparse.rows[0])
    expect(full.rows[1]).not.toBe(sparse.rows[1])
    expect(selection.copy()).toBe('abc\ndef')
    expect(selection.anchor.revision).toBe(1)
    expect(first.cellAt(1, 0)?.grapheme).toBe('d')
    expect(sparse.cellAt(1, 0)?.grapheme).toBe('n')
    expect(Object.isFrozen(full.rows[0])).toBe(true)
    expect(Object.isFrozen(full.cellAt(0, 0))).toBe(true)
  })

  it.each([
    [
      'text',
      (row: Row) => {
        row.text = '語é'
      },
    ],
    [
      'mark width',
      (row: Row) => {
        row.marks![0][2] = 1
        row.runs![0][1] = 1
      },
    ],
    [
      'mark position',
      (row: Row) => {
        row.marks![0][0] = 1
        row.marks![1][0] = 0
      },
    ],
    [
      'mark codepoints',
      (row: Row) => {
        row.marks![0][1] = 2
        row.marks![1][1] = 1
      },
    ],
    [
      'run length',
      (row: Row) => {
        row.runs![0][1] = 1
        row.runs![1][1] = 2
      },
    ],
    [
      'style tuple',
      (row: Row) => {
        ;(row.runs![0][0] as Exclude<Style, 0>)[3] = 2
      },
    ],
    [
      'wrap',
      (row: Row) => {
        row.wrap = true
      },
    ],
    [
      'continuation',
      (row: Row) => {
        row.continuation = true
      },
    ],
  ] as const)('owns comparison facts when an old wire input changes its %s', (_name, mutate) => {
    const model = createCellModel()
    const input = screen(1, [styledRow()])
    const first = apply(model, input)
    mutate(input.rows[0])
    input.revision = 2
    const second = apply(model, input)
    const independent = apply(createCellModel(), input)
    expect(second.rows[0]).not.toBe(first.rows[0])
    expect(second.rows[0].cells).toEqual(independent.rows[0].cells)
    expect(second.rows[0].wrap).toBe(independent.rows[0].wrap)
    expect(second.rows[0].continuation).toBe(independent.rows[0].continuation)
    expect(first.cellAt(0, 0)?.grapheme).toBe('界')
    expect(first.cellAt(0, 0)?.width).toBe(2)
    expect(first.cellAt(0, 0)?.style.attributes).toBe(1)
    expect(first.rows[0].wrap).toBe(false)
    expect(first.rows[0].continuation).toBe(false)
    const third = apply(model, screen(3, [styledRow()]))
    expect(third.rows[0]).not.toBe(second.rows[0])
    expect(third.rows[0].cells).toEqual(first.rows[0].cells)
  })

  it('does not let mutation of old wire arrays poison equality for fresh unchanged input', () => {
    const model = createCellModel()
    const input = screen(1, [styledRow()])
    const first = apply(model, input)
    input.rows[0].marks![0][2] = 4
    input.rows[0].runs![0][1] = 99
    ;(input.rows[0].runs![0][0] as Exclude<Style, 0>)[0] = 42
    const second = apply(model, screen(2, [styledRow()]))
    expect(second.rows[0]).toBe(first.rows[0])
  })

  it.each([
    ['foreground', styleOf({ foreground: { kind: 1, palette: 2, rgb: { r: 0, g: 0, b: 0 } } })],
    ['background', styleOf({ background: { kind: 2, palette: 0, rgb: { r: 12, g: 34, b: 56 } } })],
    [
      'underline color',
      styleOf({ underlineColor: { kind: 1, palette: 5, rgb: { r: 0, g: 0, b: 0 } } }),
    ],
    ['attributes', styleOf({ attributes: 2 })],
    ['underline', styleOf({ underline: 3 })],
  ] as const)('decodes a %s-only change with identical text', (_name, style) => {
    const model = createCellModel()
    const original = styleOf({ attributes: 1 })
    const changed = styleOf({ ...style, attributes: style.attributes || 1 })
    const first = apply(model, screen(1, [wireRowOf([['x', 1, true, original]])]))
    const second = apply(model, screen(2, [wireRowOf([['x', 1, true, changed]])]))
    expect(second.rows[0]).not.toBe(first.rows[0])
    expect(second.cellAt(0, 0)?.style).toEqual(changed)
    const third = apply(model, screen(3, [wireRowOf([['x', 1, true, changed]])]))
    expect(third.rows[0]).toBe(second.rows[0])
    const fourth = apply(model, screen(4, [wireRowOf([['x', 1, true]])]))
    expect(fourth.cellAt(0, 0)?.style).toEqual(styleOf())
  })

  it('distinguishes absent marks and runs from explicit widths and styled blank cells', () => {
    const model = createCellModel()
    const first = apply(model, screen(1, [{ text: '界' }]))
    const wide = apply(
      model,
      screen(2, [
        wireRowOf([
          ['界', 2, true],
          ['', 3, false],
        ]),
      ]),
    )
    expect(wide.rows[0]).not.toBe(first.rows[0])
    expect(wide.cellAt(0, 1)?.width).toBe(3)
    const blank = apply(model, screen(3, [wireRowOf([['', 1, false, styleOf({ attributes: 4 })]])]))
    expect(blank.cellAt(0, 0)?.hasText).toBe(false)
    expect(blank.cellAt(0, 0)?.style.attributes).toBe(4)
    const narrow = apply(model, screen(4, [{ text: '界' }]))
    expect(narrow.cellAt(0, 0)?.width).toBe(1)
    expect(narrow.cellAt(0, 1)?.width).toBe(1)
  })

  it('uses the new wrap and continuation without changing a captured selection', () => {
    const model = createCellModel()
    const first = apply(model, screen(1))
    const selection = captureLiveSelection(
      'pane',
      first,
      { row: 0, offset: 0 },
      { row: 1, offset: 3 },
    )
    const second = apply(
      model,
      screen(2, [
        { text: 'abc', wrap: true },
        { text: 'def', continuation: true },
      ]),
    )
    expect(second.rows[0]).not.toBe(first.rows[0])
    expect(second.rows[1]).not.toBe(first.rows[1])
    expect(second.rows[1].continuation).toBe(true)
    expect(
      captureLiveSelection('pane', second, { row: 0, offset: 0 }, { row: 1, offset: 3 }).copy(),
    ).toBe('abcdef')
    expect(selection.copy()).toBe('abc\ndef')
  })

  it.each([
    [
      'columns',
      (frame: SessionFrame) => {
        frame.geometry.cols++
      },
    ],
    [
      'row count',
      (frame: SessionFrame) => {
        frame.geometry.rows++
        frame.rows.push({ text: '' })
      },
    ],
    [
      'cell width',
      (frame: SessionFrame) => {
        frame.geometry.cellWidthPx++
      },
    ],
    [
      'cell height',
      (frame: SessionFrame) => {
        frame.geometry.cellHeightPx++
      },
    ],
    [
      'geometry revision',
      (frame: SessionFrame) => {
        frame.geometry.revision++
      },
    ],
    [
      'buffer kind',
      (frame: SessionFrame) => {
        frame.state.bufferKind = 'alternate'
      },
    ],
    [
      'buffer instance',
      (frame: SessionFrame) => {
        frame.state.bufferInstance = '2'
      },
    ],
    [
      'absolute row position',
      (frame: SessionFrame) => {
        frame.state.baseRow++
      },
    ],
  ] as const)('invalidates sharing across changed %s', (_name, mutate) => {
    const model = createCellModel()
    const first = apply(model, screen(1))
    const next = screen(2)
    mutate(next)
    const second = apply(model, next)
    expect(second.rows[0]).not.toBe(first.rows[0])
    expect(second.rows[1]).not.toBe(first.rows[1])
    expect(second.rows[0].cells).toHaveLength(next.geometry.cols)
    expect(second.geometry).toEqual(next.geometry)
    expect(second.state).toEqual(next.state)
    const third = apply(model, { ...next, revision: 3 })
    expect(third.rows[0]).toBe(second.rows[0])
  })

  it('never reuses a row at another grid index', () => {
    const model = createCellModel()
    const first = apply(model, screen(1))
    const second = apply(model, screen(2, [{ text: 'def' }, { text: 'abc' }]))
    expect(second.rows[0]).not.toBe(first.rows[1])
    expect(second.rows[0].index).toBe(0)
    expect(second.rows[1].index).toBe(1)
    expect(second.cellAt(0, 0)?.grapheme).toBe('d')
  })

  it.each(['marks', 'run-partition', 'column-count', 'state', 'stale'] as const)(
    'keeps snapshot and comparison facts atomic on a later-row %s rejection',
    (failure) => {
      const model = createCellModel()
      const first = apply(model, screen(5))
      const rejected = screen(failure === 'stale' ? 5 : 6, [{ text: 'new' }, { text: 'def' }])
      rejected.cursor.visible = true
      rejected.state.title = 'rejected'
      if (failure === 'marks') rejected.rows[1].marks = [[8, 1, 2]]
      if (failure === 'run-partition') rejected.rows[1].runs = [[0, 2]]
      if (failure === 'column-count') rejected.rows[1].text = 'too long'
      if (failure === 'state') rejected.state.baseRow = -1
      const result = model.apply(rejected)
      expect(result.ok).toBe(false)
      expect(model.current()).toBe(first)
      const same = apply(model, screen(6))
      expect(same.rows[0]).toBe(first.rows[0])
      expect(same.rows[1]).toBe(first.rows[1])
      expect(same.cursor.visible).toBe(false)
      expect(same.state.title).toBeNull()
      const changed = apply(model, screen(7, [{ text: 'new' }, { text: 'def' }]))
      expect(changed.cellAt(0, 0)?.grapheme).toBe('n')
      expect(changed.rows[0]).not.toBe(first.rows[0])
      expect(changed.rows[1]).toBe(first.rows[1])
    },
  )
})
