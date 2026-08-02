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

  // The wrapper always carries `ui-text-field` — it is what stacks the label
  // above the input and owns the gap between them. Without it the label sat
  // inline against the input wherever a caller passed no class of its own.
  // A caller's class is added alongside, never instead.
  it('carries the kit base class on the wrapper', () => {
    subject({ label: 'Port' })
    const label = screen.getByText('Port')
    const wrapper = label.closest('.ui-text-field')
    expect(wrapper).toBeTruthy()
    expect(wrapper?.getAttribute('class')).toBe('ui-text-field')
  })

  // The input is the element that carries the appearance, and until T4 it was the
  // one part of this component with no class at all — so its rules could only be
  // reached through an ancestor, and three surfaces re-implemented them instead.
  // The wrapper's identity says nothing about the input's; they are separate
  // duties and the gate has to see both (§3.1).
  it('names the input, not only the wrapper', () => {
    subject({ label: 'Port' })
    const input = screen.getByRole('textbox')
    expect(input.getAttribute('class')).toBe('ui-text-field__input')
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

  // ── Multiline (textarea) variance ──────────────────────────────────
  it('renders a textarea when multiline is set', () => {
    subject({ multiline: true, value: 'key content' })
    const input = screen.getByRole('textbox')
    expect(input.tagName).toBe('TEXTAREA')
  })

  it('existing typed input still renders an input element', () => {
    const { container } = subject({ type: 'text', value: 'hello' })
    const input = container.querySelector('input')
    expect(input, 'Should be an INPUT element').toBeTruthy()
    expect(input!.tagName).toBe('INPUT')
  })

  it('preserves newlines in multiline value', () => {
    const keyContent =
      '-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n-----END PRIVATE KEY-----'
    const newlineCount = (keyContent.match(/\n/g) || []).length
    const { container } = subject({ multiline: true, value: keyContent })
    const textarea = container.querySelector('textarea')
    expect(textarea).not.toBeNull()
    expect(textarea!.value).toBe(keyContent)
    expect((textarea!.value.match(/\n/g) || []).length).toBe(newlineCount)
    expect(newlineCount).toBeGreaterThan(0)
  })

  it('renders label on multiline variant', () => {
    subject({ multiline: true, value: '', label: 'Private Key' })
    const label = document.querySelector('label')
    expect(label?.textContent?.trim()).toBe('Private Key')
  })

  it('renders description on multiline variant', () => {
    const desc = 'Paste your private key'
    subject({ multiline: true, value: '', description: desc })
    expect(screen.getByText(desc)).toBeTruthy()
  })

  it('renders error text on multiline variant', () => {
    subject({ multiline: true, value: '', error: 'Invalid key' })
    expect(screen.getByText('Invalid key')).toBeTruthy()
    const textarea = screen.getByRole('textbox')
    expect(textarea.getAttribute('aria-invalid')).toBe('true')
  })
})

describe('composition with Field', () => {
  // TextField's label is optional and Field's was not, so the composition
  // originally carried `label={props.label!}` — an assertion silencing a case
  // that genuinely occurs. The result was <label for="x"></label>: an empty
  // label bound to the control, which announces it as unlabelled. That is a
  // worse outcome than the duplication the composition removed, so it is
  // pinned here rather than left to review (nocx-uxs5.5).
  it('emits no label element when there is no label to show', () => {
    const { container } = render(() => (
      <TextField id="cred-x" value="x" error="Required" onInput={() => {}} />
    ))
    expect(container.querySelector('label')).toBeNull()
    expect(container.querySelector('.ui-field-error')?.textContent).toBe('Required')
  })

  it('still labels the control when a label is given', () => {
    const { container } = render(() => <TextField id="cred-y" label="Name" value="y" />)
    const label = container.querySelector('label')
    expect(label?.getAttribute('for')).toBe('cred-y')
    expect(label?.textContent?.trim()).toBe('Name')
  })
})
