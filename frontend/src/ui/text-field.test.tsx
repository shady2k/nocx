// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@solidjs/testing-library'
import { TextField, type TextFieldProps } from './text-field'

afterEach(() => cleanup())

function subject(overrides?: Partial<TextFieldProps>) {
  const props: TextFieldProps = {
    value: '',
    ...overrides,
  }
  return render(() => <TextField {...props} />)
}

describe('TextField', () => {
  it('renders a text input by default', () => {
    subject()
    const input = screen.getByRole('textbox')
    expect(input).toHaveProperty('type', 'text')
  })

  it('sets the value', () => {
    subject({ value: 'hello' })
    const input = screen.getByRole('textbox')
    expect(input).toHaveProperty('value', 'hello')
  })

  it('calls onInput on each keystroke', () => {
    const onInput = vi.fn()
    subject({ onInput })
    const input = screen.getByRole('textbox')
    fireEvent.input(input, { target: { value: 'x' } })
    expect(onInput).toHaveBeenCalledWith('x')
  })

  it('renders a password input', () => {
    subject({ type: 'password', value: 'secret' })
    const input = screen.getByDisplayValue('secret')
    expect(input).toHaveProperty('type', 'password')
  })

  it('renders a number input', () => {
    subject({ type: 'number', value: 22 })
    const input = screen.getByDisplayValue('22')
    expect(input).toHaveProperty('type', 'number')
  })

  it('sets min and max on number inputs', () => {
    subject({ type: 'number', min: 1, max: 65535, value: 0 })
    const input = screen.getByDisplayValue('0')
    expect(input).toHaveProperty('min', '1')
    expect(input).toHaveProperty('max', '65535')
  })

  it('renders a label when provided', () => {
    subject({ label: 'Host' })
    expect(screen.getByText('Host')).toBeTruthy()
  })

  it('sets placeholder', () => {
    subject({ placeholder: 'Enter name…' })
    const input = screen.getByPlaceholderText('Enter name…')
    expect(input).toBeTruthy()
  })

  it('sets class on the wrapper', () => {
    subject({ class: 'cm-field', label: 'Port' })
    const wrapper = screen.getByText('Port').parentElement
    expect(wrapper?.getAttribute('class')).toBe('cm-field')
  })

  it('sets disabled attribute', () => {
    subject({ disabled: true })
    const input = screen.getByRole('textbox')
    expect(input).toHaveProperty('disabled', true)
  })

  it('renders description text', () => {
    subject({ description: 'Port number between 1 and 65535' })
    expect(screen.getByText('Port number between 1 and 65535')).toBeTruthy()
  })

  it('renders error text and sets aria-invalid', () => {
    subject({ error: 'Invalid port' })
    expect(screen.getByText('Invalid port')).toBeTruthy()
    const input = screen.getByRole('textbox')
    expect(input.getAttribute('aria-invalid')).toBe('true')
  })

  it('wires aria-describedby from description', () => {
    subject({ id: 'port', description: 'Range 1-65535' })
    const input = screen.getByRole('textbox')
    const descId = input.getAttribute('aria-describedby')
    expect(descId).toMatch(/port__desc/)
  })

  it('wires aria-describedby from error', () => {
    subject({ id: 'port', error: 'Required' })
    const input = screen.getByRole('textbox')
    const descId = input.getAttribute('aria-describedby')
    expect(descId).toMatch(/port__error/)
  })

  it('wires label for attribute to input id', () => {
    subject({ id: 'host', label: 'Host' })
    const label = screen.getByText('Host')
    expect(label.getAttribute('for')).toBe('host')
  })

  it('is focusable via tab', () => {
    subject()
    const input = screen.getByRole('textbox')
    expect(input.getAttribute('tabindex')).toBeNull() // natively focusable
  })

  it('sets required attribute', () => {
    subject({ required: true })
    const input = screen.getByRole('textbox')
    expect(input).toHaveProperty('required', true)
  })
})
