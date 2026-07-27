// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@solidjs/testing-library'
import { Checkbox, type CheckboxProps } from './checkbox'

afterEach(() => cleanup())

function subject(overrides?: Partial<CheckboxProps>) {
  const props: CheckboxProps = {
    checked: false,
    onChange: vi.fn(),
    ...overrides,
  }
  return render(() => <Checkbox {...props} />)
}

describe('Checkbox', () => {
  it('renders an unchecked checkbox by default', () => {
    subject()
    const cb = screen.getByRole('checkbox')
    expect(cb).toHaveProperty('checked', false)
  })

  it('renders a checked checkbox', () => {
    subject({ checked: true })
    const cb = screen.getByRole('checkbox')
    expect(cb).toHaveProperty('checked', true)
  })

  it('calls onChange with checked state', () => {
    const onChange = vi.fn()
    subject({ onChange })
    fireEvent.click(screen.getByRole('checkbox'))
    expect(onChange).toHaveBeenCalledWith(true)
  })

  it('renders a label text when provided', () => {
    subject({ label: 'Modified only' })
    expect(screen.getByText('Modified only')).toBeTruthy()
  })

  it('sets class on the wrapper', () => {
    subject({ class: 'my-filter', label: 'test' })
    const label = screen.getByText('test').parentElement
    expect(label?.getAttribute('class')).toBe('my-filter')
  })

  it('sets aria-label when no visible label', () => {
    subject({ ariaLabel: 'Show passwords' })
    expect(screen.getByLabelText('Show passwords')).toBeTruthy()
  })

  it('sets disabled attribute', () => {
    subject({ disabled: true })
    const cb = screen.getByRole('checkbox')
    expect(cb).toHaveProperty('disabled', true)
  })

  it('does not toggle onChange when disabled', () => {
    const onChange = vi.fn()
    subject({ disabled: true, onChange, checked: false })
    const cb = screen.getByRole('checkbox')
    cb.click()
    expect(onChange).not.toHaveBeenCalled()
  })

  it('is a native checkbox with keyboard support (Space handled by browser)', () => {
    subject()
    const cb = screen.getByRole('checkbox')
    expect(cb.tagName).toBe('INPUT')
    expect(cb.getAttribute('type')).toBe('checkbox')
  })
})
