// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { Select, type SelectProps } from './select'

afterEach(() => cleanup())

const options = [
  { value: 'alice', label: 'Alice (alice@github)' },
  { value: 'bob', label: 'Bob (bob@corp)' },
]

function subject(overrides?: Partial<SelectProps>) {
  const props: SelectProps = {
    value: '',
    onChange: vi.fn(),
    options,
    ...overrides,
  }
  return render(() => <Select {...props} />)
}

describe('Select', () => {
  it('renders all options', () => {
    subject()
    expect(screen.getByText('Alice (alice@github)')).toBeTruthy()
    expect(screen.getByText('Bob (bob@corp)')).toBeTruthy()
  })

  it('marks the matching option as selected', () => {
    subject({ value: 'bob' })
    const sel = screen.getByRole<HTMLSelectElement>('combobox')
    expect(sel.value).toBe('bob')
  })

  it('calls onChange when selection changes', () => {
    const onChange = vi.fn()
    subject({ onChange, value: 'alice' })
    const sel = screen.getByRole('combobox')
    fireEvent.change(sel, { target: { value: 'bob' } })
    expect(onChange).toHaveBeenCalledWith('bob')
  })

  it('renders a placeholder option when provided', () => {
    subject({ placeholder: '— None —', placeholderValue: '' })
    expect(screen.getByText('— None —')).toBeTruthy()
  })

  // `cm-field` was a connections-surface class, and asserting it here meant the kit's
  // Select was named by one of its consumers. Outside that surface's subtree it had no
  // rules at all and rendered as native platform chrome (§3.1).
  it('names itself', () => {
    subject()
    const sel = screen.getByRole('combobox')
    expect(sel.getAttribute('class')).toBe('ui-select')
  })

  it('sets disabled attribute', () => {
    subject({ disabled: true })
    const sel = screen.getByRole('combobox')
    expect(sel).toHaveProperty('disabled', true)
  })

  it('uses the supplied accessible label', () => {
    subject({ ariaLabel: 'Filter by application' })
    expect(screen.getByRole('combobox', { name: 'Filter by application' })).toBeTruthy()
  })

  it('is natively keyboard-operable (Arrow keys, typeahead)', () => {
    subject()
    const sel = screen.getByRole('combobox')
    expect(sel.tagName).toBe('SELECT')
  })

  // The vault pickers load their options asynchronously AFTER the bound value
  // is known. A controlled select whose options land late must re-apply the
  // selection, or the bound secret reads as "None" (b5bu).
  it('keeps the value selected when options arrive after the value', () => {
    function Harness() {
      const [opts, setOpts] = createSignal<{ value: string; label: string }[]>([])
      return (
        <>
          <button onClick={() => setOpts([{ value: 'secrow:bound', label: 'Bound secret' }])}>
            load
          </button>
          <Select value="secrow:bound" onChange={vi.fn()} options={opts()} />
        </>
      )
    }
    render(() => <Harness />)
    const sel = screen.getByRole<HTMLSelectElement>('combobox')
    expect(sel.value).toBe('')
    fireEvent.click(screen.getByText('load'))
    expect(sel.value).toBe('secrow:bound')
  })
})
