// @vitest-environment jsdom
import { describe, expect, it, afterEach } from 'vitest'
import { render, cleanup } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { ToggleMatrix, type ToggleMatrixProps } from './toggle-matrix'
import { Checkbox } from './checkbox'

afterEach(() => cleanup())

const rows = [
  { id: 'r1', label: 'Row one' },
  { id: 'r2', label: 'Row two' },
]
const columns = [
  { id: 'c1', label: 'Column one' },
  { id: 'c2', label: 'Column two' },
]

function subject(overrides?: Partial<ToggleMatrixProps>) {
  const props: ToggleMatrixProps = {
    ariaLabel: 'Matrix',
    rows,
    columns,
    renderCell: (row, column) => (
      <Checkbox
        variant="switch"
        checked={false}
        ariaLabel={`${row.label} → ${column.label}`}
        onChange={() => {}}
      />
    ),
    ...overrides,
  }
  return render(() => <ToggleMatrix {...props} />)
}

describe('ToggleMatrix', () => {
  it('renders one cell per row × column, with both headers associated', () => {
    const { container } = subject()

    const table = container.querySelector<HTMLTableElement>('table.ui-toggle-matrix')!
    expect(table).toBeTruthy()
    expect(table.getAttribute('aria-label')).toBe('Matrix')

    const columnHeaders = Array.from(
      table.querySelectorAll<HTMLElement>('.ui-toggle-matrix__column'),
    )
    expect(columnHeaders.map((h) => h.textContent)).toEqual(['Column one', 'Column two'])
    // scope is what associates a header with its cells; without it a screen
    // reader announces a grid of identically-named switches.
    expect(columnHeaders.every((h) => h.getAttribute('scope') === 'col')).toBe(true)

    const rowHeaders = Array.from(
      table.querySelectorAll<HTMLElement>('.ui-toggle-matrix__row-header'),
    )
    expect(rowHeaders.map((h) => h.textContent)).toEqual(['Row one', 'Row two'])
    expect(rowHeaders.every((h) => h.getAttribute('scope') === 'row')).toBe(true)

    expect(table.querySelectorAll('.ui-toggle-matrix__cell').length).toBe(4)
    expect(table.querySelectorAll('input[type="checkbox"]').length).toBe(4)
  })

  it('draws an empty cell where the caller offers no control', () => {
    const { container } = subject({
      renderCell: (row, column) =>
        row.id === 'r2' && column.id === 'c1' ? null : (
          <Checkbox variant="switch" checked={false} ariaLabel="cell" onChange={() => {}} />
        ),
    })

    const cells = Array.from(container.querySelectorAll<HTMLElement>('.ui-toggle-matrix__cell'))
    expect(cells.length).toBe(4)
    expect(container.querySelectorAll('input[type="checkbox"]').length).toBe(3)
    // The cell is still there, so the grid keeps its shape; it just holds
    // nothing, which is what "the impossible choice is absent" looks like.
    const empty = cells.find((c) => c.dataset.column === 'c1' && c.dataset.row === 'r2')!
    expect(empty.textContent).toBe('')
  })

  it('hides an axis the caller has filtered away without dropping its cells', () => {
    const { container } = subject({
      rowHidden: (row) => row.id === 'r2',
      columnHidden: (column) => column.id === 'c1',
    })

    // Every cell is still in the DOM — a hidden axis is filtered from view,
    // not removed, so an id inside it stays addressable.
    expect(container.querySelectorAll('.ui-toggle-matrix__cell').length).toBe(4)

    const hiddenRow = container.querySelector<HTMLElement>('.ui-toggle-matrix__row[data-row="r2"]')!
    expect(hiddenRow.dataset.hidden).toBe('true')
    expect(
      container.querySelector<HTMLElement>('.ui-toggle-matrix__row[data-row="r1"]')!.dataset.hidden,
    ).toBeUndefined()

    // A hidden COLUMN is its header plus its cell in every row.
    const hiddenColumn = container.querySelector<HTMLElement>(
      '.ui-toggle-matrix__column[data-column="c1"]',
    )!
    expect(hiddenColumn.dataset.hidden).toBe('true')
    const c1Cells = Array.from(
      container.querySelectorAll<HTMLElement>('.ui-toggle-matrix__cell[data-column="c1"]'),
    )
    expect(c1Cells.length).toBe(2)
    expect(c1Cells.every((c) => c.dataset.hidden === 'true')).toBe(true)
    const c2Cells = Array.from(
      container.querySelectorAll<HTMLElement>('.ui-toggle-matrix__cell[data-column="c2"]'),
    )
    expect(c2Cells.every((c) => c.dataset.hidden === undefined)).toBe(true)
  })

  // Visibility is a predicate precisely so that filtering does not rebuild the
  // grid. A search box that disposed the control it was narrowing towards
  // would take focus, and any in-flight interaction, with it.
  it('keeps a cell control alive when only the axis visibility changes', () => {
    const Harness = () => {
      const [hidden, setHidden] = createSignal(false)
      return (
        <>
          <ToggleMatrix
            ariaLabel="Matrix"
            rows={rows}
            columns={columns}
            columnHidden={(column) => hidden() && column.id === 'c1'}
            renderCell={(row, column) => <span id={`cell-${row.id}-${column.id}`} />}
          />
          <span
            id="flip"
            onClick={() => {
              setHidden(true)
            }}
          />
        </>
      )
    }
    const { container } = render(() => <Harness />)
    const before = container.querySelector('#cell-r1-c1')
    expect(before).toBeTruthy()
    container.querySelector<HTMLElement>('#flip')!.click()
    expect(
      container
        .querySelector('.ui-toggle-matrix__cell[data-column="c1"]')!
        .getAttribute('data-hidden'),
    ).toBe('true')
    expect(container.querySelector('#cell-r1-c1')).toBe(before)
  })

  it('adds a row and a column as soon as the caller declares one', () => {
    const Harness = () => {
      const [extra, setExtra] = createSignal(false)
      return (
        <>
          <ToggleMatrix
            ariaLabel="Matrix"
            rows={extra() ? [...rows, { id: 'r3', label: 'Row three' }] : rows}
            columns={extra() ? [...columns, { id: 'c3', label: 'Column three' }] : columns}
            renderCell={() => <Checkbox checked={false} ariaLabel="cell" onChange={() => {}} />}
          />
          <span
            id="grow"
            onClick={() => {
              setExtra(true)
            }}
          />
        </>
      )
    }
    const { container } = render(() => <Harness />)
    expect(container.querySelectorAll('.ui-toggle-matrix__cell').length).toBe(4)
    container.querySelector<HTMLElement>('#grow')!.click()
    expect(container.querySelectorAll('.ui-toggle-matrix__cell').length).toBe(9)
    expect(
      Array.from(container.querySelectorAll('.ui-toggle-matrix__row-header')).map(
        (h) => h.textContent,
      ),
    ).toEqual(['Row one', 'Row two', 'Row three'])
  })
})
