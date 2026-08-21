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

describe('TextField number variance: unit and caption slot (nocx-w7h.7)', () => {
  it('renders the unit as a suffix inside the control', () => {
    const { container } = subject({ type: 'number', value: 4096, unit: 'MiB' })
    const unit = container.querySelector('.ui-text-field__unit')
    expect(unit?.textContent).toBe('MiB')
    const control = container.querySelector('.ui-text-field__control')
    expect(control?.contains(unit)).toBe(true) // one thing with the value
  })

  it('renders no unit suffix when none is given', () => {
    const { container } = subject({ type: 'number', value: 4096 })
    expect(container.querySelector('.ui-text-field__unit')).toBeNull()
  })

  it('renders the caption beneath the control, in its own slot', () => {
    const { container } = subject({ type: 'number', value: 4096, caption: '64 – 1048576 MiB' })
    const caption = container.querySelector('.ui-text-field__caption')
    expect(caption?.textContent).toBe('64 – 1048576 MiB')
    const control = container.querySelector('.ui-text-field__control')
    expect(control?.contains(caption)).toBe(false) // beneath, not inside
  })

  it('the error replaces the caption in the same slot — one element, no jump', () => {
    const { container } = subject({
      type: 'number',
      value: 5000,
      caption: '64 – 1048576 MiB',
      error: 'Must be at most 1048576 MiB',
    })
    const caption = container.querySelector('.ui-text-field__caption')
    expect(caption?.textContent).toBe('Must be at most 1048576 MiB')
    expect(caption?.getAttribute('data-tone')).toBe('error')
    expect(caption?.getAttribute('role')).toBe('alert')
    // Exactly one slot element — the caption and the error never coexist.
    expect(container.querySelectorAll('.ui-text-field__caption').length).toBe(1)
    // Field must not render a second error alongside the slot.
    expect(container.querySelector('.ui-field-error')).toBeNull()
  })

  it('without a caption, the error still renders through Field as before', () => {
    const { container } = subject({ error: 'Required' })
    expect(container.querySelector('.ui-field-error')?.textContent).toBe('Required')
    expect(container.querySelector('.ui-text-field__caption')).toBeNull()
  })

  // "No jump" is a claim about the box, and jsdom has no boxes — so the
  // property is pinned as the structure that causes the jump: a captioned
  // field must render the SAME element tree in both states. Measured in a
  // real browser on 2026-08-01, going out of range wrapped the control in a
  // Field that was not there before and the field grew 48.7px → 52.7px.
  it('a captioned field keeps the same structure when its value goes out of range', () => {
    const ok = subject({ type: 'number', value: 4096, caption: '64 – 1048576 MiB' })
    const bad = subject({
      type: 'number',
      value: 1,
      caption: '64 – 1048576 MiB',
      error: 'Must be at least 64 MiB',
    })
    const shape = (c: Element) =>
      [...c.querySelectorAll('*')].map((e) => e.tagName + '.' + (e.className || '')).join(' > ')
    expect(shape(bad.container)).toBe(shape(ok.container))
    expect(bad.container.querySelector('.ui-field')).toBeNull()
  })

  it('a captioned field aligns its caption to the column it is told to follow', () => {
    const start = subject({ type: 'number', value: 1, caption: '0 – 10' })
    expect(
      start.container.querySelector('.ui-text-field__caption')?.getAttribute('data-align'),
    ).toBe('start')
    const end = subject({ type: 'number', value: 1, caption: '0 – 10', captionAlign: 'end' })
    expect(end.container.querySelector('.ui-text-field__caption')?.getAttribute('data-align')).toBe(
      'end',
    )
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

/**
 * The commit gesture (nocx-gihi6). A field whose value is WRITTEN rather than
 * merely validated cannot write per keystroke — the Agent policy page's scope
 * field is checked by `ParseEffectPolicy`, which rejects a non-absolute path,
 * so a half-typed `/w` would be a refused write and a toast on every character
 * of `/workspace`. Blur and Enter are the same gesture ("I am done with this
 * value") and the kit says so once, rather than every caller pairing `onBlur`
 * with a hand-rolled keydown.
 */
describe('TextField commit gesture', () => {
  it('commits on blur, with the field value', () => {
    const onCommit = vi.fn()
    subject({ value: '/workspace', onCommit })
    fireEvent.blur(screen.getByRole('textbox'))
    expect(onCommit).toHaveBeenCalledWith('/workspace')
  })

  it('commits on Enter without waiting for focus to leave', () => {
    const onCommit = vi.fn()
    subject({ value: '/workspace', onCommit })
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter' })
    expect(onCommit).toHaveBeenCalledWith('/workspace')
  })

  it('does not commit on any other key', () => {
    const onCommit = vi.fn()
    subject({ value: '/w', onCommit })
    const input = screen.getByRole('textbox')
    fireEvent.keyDown(input, { key: 'w' })
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(onCommit).not.toHaveBeenCalled()
  })

  it('does not commit per keystroke', () => {
    const onCommit = vi.fn()
    const onInput = vi.fn()
    subject({ value: '', onCommit, onInput })
    fireEvent.input(screen.getByRole('textbox'), { target: { value: '/w' } })
    expect(onInput).toHaveBeenCalledTimes(1)
    expect(onCommit).not.toHaveBeenCalled()
  })

  it('leaves onBlur alone: a caller using both gets both', () => {
    const onBlur = vi.fn()
    const onCommit = vi.fn()
    subject({ value: 'x', onBlur, onCommit })
    fireEvent.blur(screen.getByRole('textbox'))
    expect(onBlur).toHaveBeenCalledWith('x')
    expect(onCommit).toHaveBeenCalledWith('x')
  })

  it('commits a multiline value on blur, and never on Enter — Enter is a newline there', () => {
    const onCommit = vi.fn()
    subject({ value: 'a note', multiline: true, onCommit })
    const area = screen.getByRole('textbox')
    fireEvent.keyDown(area, { key: 'Enter' })
    expect(onCommit).not.toHaveBeenCalled()
    fireEvent.blur(area)
    expect(onCommit).toHaveBeenCalledWith('a note')
  })
})
