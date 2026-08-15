// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { EditableRowList, type EditableRowListProps } from './row-list'

afterEach(() => cleanup())

const rows = ['alpha', 'beta']

function subject(overrides?: Partial<EditableRowListProps<string>>) {
  const props: EditableRowListProps<string> = {
    rows,
    renderRow: (row) => <span>{row()}</span>,
    onRemove: vi.fn(),
    onAdd: vi.fn(),
    addLabel: 'Add row',
    removeLabel: (i) => `Remove row ${i + 1}`,
    ariaLabel: 'Rows',
    ...overrides,
  }
  return render(() => <EditableRowList {...props} />)
}

describe('EditableRowList row identity', () => {
  // The rows are CONTROLLED and the caller replaces the row object on every
  // edit (`updateModel` maps to a new object). The row's DOM must survive the
  // keystroke: focus, IME composition, text selection and any open browser
  // popup live on that DOM, and rebuilding it on every letter is the defect
  // this test exists to hold closed (fix-kit-rowlist).
  it('keeps a row input alive when the caller replaces the row object on edit', () => {
    const onRemove = vi.fn()
    const onAdd = vi.fn()
    const Harness = () => {
      const [rows, setRows] = createSignal([{ name: 'a' }])
      return (
        <EditableRowList
          rows={rows()}
          ariaLabel="Rows"
          addLabel="Add row"
          removeLabel={(i) => `Remove row ${i + 1}`}
          onRemove={onRemove}
          onAdd={onAdd}
          renderRow={(row) => (
            <input
              id="row-0-name"
              value={row().name}
              onInput={(e) => {
                const v = e.currentTarget.value
                setRows((prev) => prev.map((r, i) => (i === 0 ? { ...r, name: v } : r)))
              }}
            />
          )}
        />
      )
    }
    const { container } = render(() => <Harness />)
    const before = container.querySelector('#row-0-name')
    expect(before, 'row input exists').toBeTruthy()
    fireEvent.input(before!, { target: { value: 'd' } })
    expect(container.querySelector('#row-0-name') === before).toBe(true)
    expect((before as HTMLInputElement).value).toBe('d')
  })
})

describe('EditableRowList', () => {
  it('renders one row per entry with the caller field content', () => {
    subject()
    const list = screen.getByRole('list', { name: 'Rows' })
    expect(list).toBeTruthy()
    expect(screen.getByText('alpha')).toBeTruthy()
    expect(screen.getByText('beta')).toBeTruthy()
    const items = list.querySelectorAll('.ui-row-list__row')
    expect(items.length).toBe(2)
  })

  it('removes a row by index with its own accessible name', () => {
    const onRemove = vi.fn()
    subject({ onRemove })
    fireEvent.click(screen.getByRole('button', { name: 'Remove row 2' }))
    expect(onRemove).toHaveBeenCalledWith(1)
    expect(onRemove).toHaveBeenCalledTimes(1)
  })

  it('adds a row from the foot control', () => {
    const onAdd = vi.fn()
    subject({ onAdd })
    fireEvent.click(screen.getByRole('button', { name: 'Add row' }))
    expect(onAdd).toHaveBeenCalledTimes(1)
  })

  it('shows the empty message only when there are no rows', () => {
    subject({ rows: [], emptyLabel: 'Nothing here yet' })
    expect(screen.getByText('Nothing here yet')).toBeTruthy()
    // The add control is still there — an empty list is how you start one.
    expect(screen.getByRole('button', { name: 'Add row' })).toBeTruthy()
  })

  it('renders a group error through the kit field-error identity, at the foot', () => {
    subject({ error: 'Add at least one model' })
    const list = screen.getByRole('list', { name: 'Rows' })
    const err = list.querySelector('.ui-field-error')
    expect(err).toBeTruthy()
    expect(err?.textContent).toBe('Add at least one model')
    expect(err?.getAttribute('role')).toBe('alert')
    // Below the add control, never between the rows and their affordance.
    const add = list.querySelector('.ui-row-list__add')
    expect(add?.nextElementSibling).toBe(err)
  })

  it('renders no error element when there is none', () => {
    subject()
    expect(screen.getByRole('list', { name: 'Rows' }).querySelector('.ui-field-error')).toBeNull()
  })

  it('hides the empty message when rows exist', () => {
    subject({ emptyLabel: 'Nothing here yet' })
    expect(screen.queryByText('Nothing here yet')).toBeNull()
  })

  it('disables both affordances when disabled', () => {
    const onRemove = vi.fn()
    const onAdd = vi.fn()
    subject({ onRemove, onAdd, disabled: true })
    fireEvent.click(screen.getByRole('button', { name: 'Remove row 1' }))
    fireEvent.click(screen.getByRole('button', { name: 'Add row' }))
    expect(onRemove).not.toHaveBeenCalled()
    expect(onAdd).not.toHaveBeenCalled()
  })

  it('names its identities from the kit vocabulary', () => {
    subject({ emptyLabel: 'Nothing here yet', rows: [] })
    const list = screen.getByRole('list', { name: 'Rows' })
    expect(list.getAttribute('class')).toBe('ui-row-list')
    // An empty list renders the empty message and the add control only.
    expect(list.querySelectorAll('.ui-row-list__row').length).toBe(0)
  })
})
