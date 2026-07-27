// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@solidjs/testing-library'
import { SearchField, type SearchFieldProps } from './search-field'

afterEach(() => cleanup())

function subject(overrides?: Partial<SearchFieldProps>) {
  const props: SearchFieldProps = {
    value: '',
    onInput: vi.fn(),
    ...overrides,
  }
  return render(() => <SearchField {...props} />)
}

describe('SearchField', () => {
  it('renders a search input', () => {
    subject()
    const input = screen.getByRole('searchbox')
    expect(input).toHaveProperty('type', 'search')
  })

  it('sets the value', () => {
    subject({ value: 'font' })
    const input = screen.getByRole('searchbox')
    expect(input).toHaveProperty('value', 'font')
  })

  it('calls onInput on each keystroke', () => {
    const onInput = vi.fn()
    subject({ onInput })
    const input = screen.getByRole('searchbox')
    fireEvent.input(input, { target: { value: 'term' } })
    expect(onInput).toHaveBeenCalledWith('term')
  })

  it('sets placeholder', () => {
    subject({ placeholder: 'Search settings…' })
    const input = screen.getByPlaceholderText('Search settings…')
    expect(input).toBeTruthy()
  })

  it('sets aria-label', () => {
    subject({ ariaLabel: 'Search settings' })
    expect(screen.getByLabelText('Search settings')).toBeTruthy()
  })

  it('sets class', () => {
    subject({ class: 'st-search-input' })
    const input = screen.getByRole('searchbox')
    expect(input.getAttribute('class')).toBe('st-search-input')
  })

  it('sets disabled attribute', () => {
    subject({ disabled: true })
    const input = screen.getByRole('searchbox')
    expect(input).toHaveProperty('disabled', true)
  })

  it('is focusable via tab', () => {
    subject()
    const input = screen.getByRole('searchbox')
    expect(input.getAttribute('tabindex')).toBeNull() // natively focusable
  })
})
