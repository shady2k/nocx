// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@solidjs/testing-library'
import { IconButton, type IconButtonProps } from './icon-button'

afterEach(() => cleanup())

function subject(overrides?: Partial<IconButtonProps>) {
  const props: IconButtonProps = {
    ariaLabel: 'test button',
    onClick: vi.fn(),
    children: '\u00d7',
    ...overrides,
  }
  return render(() => <IconButton {...props} />)
}

describe('IconButton', () => {
  it('renders with base class ui-icon-button', () => {
    subject()
    const btn = screen.getByRole('button')
    expect(btn.classList.contains('ui-icon-button')).toBe(true)
  })

  it('has no extra classes beyond ui-icon-button', () => {
    subject()
    const btn = screen.getByRole('button')
    expect(btn.classList.length).toBe(1)
    expect(btn.classList[0]).toBe('ui-icon-button')
  })

  it('renders aria-label', () => {
    subject({ ariaLabel: 'Close tab' })
    const btn = screen.getByRole('button', { name: 'Close tab' })
    expect(btn).toBeTruthy()
  })

  it('defaults data-size to md', () => {
    subject()
    const btn = screen.getByRole('button')
    expect(btn.getAttribute('data-size')).toBe('md')
  })

  it('sets data-size from prop', () => {
    subject({ size: 'sm' })
    const btn = screen.getByRole('button')
    expect(btn.getAttribute('data-size')).toBe('sm')
  })

  it('sets aria-selected when selected', () => {
    subject({ selected: true })
    const btn = screen.getByRole('button')
    expect(btn.getAttribute('aria-selected')).toBe('true')
  })

  it('omits aria-selected when not selected', () => {
    subject()
    const btn = screen.getByRole('button')
    expect(btn.hasAttribute('aria-selected')).toBe(false)
  })

  it('omits data-appearance by default — the transparent affordance (round 20)', () => {
    subject()
    const btn = screen.getByRole('button')
    expect(btn.hasAttribute('data-appearance')).toBe(false)
  })

  it('omits data-appearance when appearance is explicitly default (round 20)', () => {
    subject({ appearance: 'default' })
    const btn = screen.getByRole('button')
    expect(btn.hasAttribute('data-appearance')).toBe(false)
  })

  it('sets data-appearance to primary — the accent-filled submit register (round 20)', () => {
    subject({ appearance: 'primary' })
    const btn = screen.getByRole('button')
    expect(btn.getAttribute('data-appearance')).toBe('primary')
  })

  it('sets data-rail-indicator when railIndicator is true', () => {
    subject({ railIndicator: true, selected: true })
    const btn = screen.getByRole('button')
    expect(btn.getAttribute('data-rail-indicator')).toBe('true')
  })

  it('omits data-rail-indicator when railIndicator is not set', () => {
    subject()
    const btn = screen.getByRole('button')
    expect(btn.hasAttribute('data-rail-indicator')).toBe(false)
  })

  it('calls onClick when clicked', () => {
    const onClick = vi.fn()
    subject({ onClick })
    const btn = screen.getByRole('button')
    fireEvent.click(btn)
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('sets disabled attribute', () => {
    subject({ disabled: true })
    const btn = screen.getByRole('button')
    expect(btn.hasAttribute('disabled')).toBe(true)
  })

  it('sets title', () => {
    subject({ title: 'Close' })
    const btn = screen.getByRole('button')
    expect(btn.getAttribute('title')).toBe('Close')
  })

  it('defaults title to empty string', () => {
    subject()
    const btn = screen.getByRole('button')
    expect(btn.getAttribute('title')).toBe('')
  })

  it('sets tabIndex', () => {
    subject({ tabIndex: -1 })
    const btn = screen.getByRole('button')
    expect(btn.tabIndex).toBe(-1)
  })

  it('has role button for accessibility', () => {
    subject()
    const btn = screen.getByRole('button')
    expect(btn).toBeTruthy()
  })

  it('is natively keyboard-activatable (Enter/Space handled by browser)', () => {
    subject()
    const btn = screen.getByRole('button')
    expect(btn.tagName).toBe('BUTTON')
    expect(btn.getAttribute('type')).toBe('button')
  })
})
