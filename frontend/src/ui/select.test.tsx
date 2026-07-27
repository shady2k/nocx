// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@solidjs/testing-library'
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

  it('sets class on the select', () => {
    subject({ class: 'cm-field' })
    const sel = screen.getByRole('combobox')
    expect(sel.getAttribute('class')).toBe('cm-field')
  })

  it('sets disabled attribute', () => {
    subject({ disabled: true })
    const sel = screen.getByRole('combobox')
    expect(sel).toHaveProperty('disabled', true)
  })

  it('is natively keyboard-operable (Arrow keys, typeahead)', () => {
    subject()
    const sel = screen.getByRole('combobox')
    expect(sel.tagName).toBe('SELECT')
  })
})
