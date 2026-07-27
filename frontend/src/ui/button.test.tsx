// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@solidjs/testing-library'
import { Button, type ButtonProps } from './button'

afterEach(() => cleanup())

function subject(overrides?: Partial<ButtonProps>) {
  const props: ButtonProps = {
    onClick: vi.fn(),
    children: 'Click me',
    ...overrides,
  }
  return render(() => <Button {...props} />)
}

describe('Button', () => {
  it('renders the label text', () => {
    subject()
    expect(screen.getByText('Click me')).toBeTruthy()
  })

  it('calls onClick when clicked', () => {
    const onClick = vi.fn()
    subject({ onClick })
    fireEvent.click(screen.getByText('Click me'))
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('is natively keyboard-activatable (Enter/Space handled by browser)', () => {
    subject()
    const btn = screen.getByText('Click me')
    // Native <button> handles Enter/Space activation — test that it's a real button
    expect(btn.tagName).toBe('BUTTON')
    expect(btn.getAttribute('type')).toBe('button')
  })

  it('sets the class attribute', () => {
    subject({ class: 'my-btn' })
    const btn = screen.getByText('Click me')
    expect(btn.getAttribute('class')).toBe('my-btn')
  })

  it('defaults type to button', () => {
    subject()
    const btn = screen.getByText('Click me')
    expect(btn.getAttribute('type')).toBe('button')
  })

  it('respects explicit type', () => {
    subject({ type: 'submit' })
    const btn = screen.getByText('Click me')
    expect(btn.getAttribute('type')).toBe('submit')
  })

  it('sets disabled attribute', () => {
    subject({ disabled: true })
    const btn = screen.getByText('Click me')
    expect(btn.getAttribute('disabled')).not.toBeNull()
  })

  it('does not call onClick when disabled', () => {
    const onClick = vi.fn()
    subject({ disabled: true, onClick })
    const btn = screen.getByText('Click me')
    btn.click()
    expect(onClick).not.toHaveBeenCalled()
  })

  it('sets title', () => {
    subject({ title: 'Tooltip text' })
    const btn = screen.getByText('Click me')
    expect(btn.getAttribute('title')).toBe('Tooltip text')
  })

  it('sets aria-label', () => {
    subject({ ariaLabel: 'Dismiss', children: '✕' })
    expect(screen.getByLabelText('Dismiss')).toBeTruthy()
  })

  it('renders default variant with no extra class', () => {
    subject()
    const btn = screen.getByText('Click me')
    expect(btn.classList.contains('ui-btn-primary')).toBe(false)
    expect(btn.classList.contains('ui-btn-danger')).toBe(false)
  })

  it('applies primary variant class', () => {
    subject({ variant: 'primary' })
    const btn = screen.getByText('Click me')
    expect(btn.classList.contains('ui-btn-primary')).toBe(true)
  })

  it('applies danger variant class', () => {
    subject({ variant: 'danger' })
    const btn = screen.getByText('Click me')
    expect(btn.classList.contains('ui-btn-danger')).toBe(true)
  })

  it('applies close variant class', () => {
    subject({ variant: 'close' })
    const btn = screen.getByText('Click me')
    expect(btn.classList.contains('ui-btn-close')).toBe(true)
  })

  it('combines variant and custom class', () => {
    subject({ variant: 'primary', class: 'my-btn' })
    const btn = screen.getByText('Click me')
    expect(btn.classList.contains('ui-btn-primary')).toBe(true)
    expect(btn.classList.contains('my-btn')).toBe(true)
  })

  it('has role button for accessibility', () => {
    subject()
    expect(screen.getByRole('button')).toBeTruthy()
  })

  it('is focusable via tab', () => {
    subject()
    const btn = screen.getByText('Click me')
    expect(btn.getAttribute('tabindex')).toBeNull() // natively focusable
  })
})
