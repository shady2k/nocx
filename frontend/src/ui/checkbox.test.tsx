// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@solidjs/testing-library'
import { createSignal, untrack } from 'solid-js'
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

  // Two identities with different duties (§3.1): the row and the box. Asserting them
  // separately is the point — before this transaction neither element had a class at
  // all, so every boolean rendered as native platform chrome outside the one scoped
  // subtree, and no test could have told the difference.
  it('names the row and the box separately', () => {
    subject({ label: 'test' })
    const row = screen.getByText('test').parentElement
    expect(row?.getAttribute('class')).toBe('ui-checkbox')
    expect(row?.getAttribute('data-variant')).toBe('checkbox')
    expect(row?.querySelector('input')?.getAttribute('class')).toBe('ui-checkbox__control')
  })

  it('selects the switch shape by attribute, not by a class the caller remembers', () => {
    subject({ variant: 'switch', label: 'test' })
    const row = screen.getByText('test').parentElement
    expect(row?.getAttribute('data-variant')).toBe('switch')
    expect(row?.getAttribute('class')).toBe('ui-checkbox')
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

  // THE CONTROL SHOWS WHAT THE BACKEND HOLDS, NOT WHERE THE FINGER LEFT IT
  // (nocx-845y4).
  //
  // `checked` is bound to the caller's state, so a write that FAILS changes
  // nothing for Solid to re-run and the DOM keeps the position the drag left.
  // The person then reads a switch that says one thing and a toast that says
  // the opposite, and the switch is the louder of the two.
  //
  // It is settled here rather than in each surface because it is a property of
  // any controlled Checkbox whose write can fail — and there are two such
  // surfaces already (the Skills row and the skill tab).
  it('snaps back when a synchronous handler does not change the state', () => {
    const onChange = vi.fn() // a write that refused: the state never moves
    subject({ checked: false, onChange })
    const cb = screen.getByRole('checkbox')
    fireEvent.click(cb)
    expect(onChange).toHaveBeenCalledWith(true)
    expect(cb).toHaveProperty('checked', false)
  })

  it('waits for an async handler and then shows what the state holds', async () => {
    let settle: (() => void) | undefined
    const onChange = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          settle = resolve
        }),
    )
    subject({ checked: false, onChange })
    const cb = screen.getByRole('checkbox')
    fireEvent.click(cb)
    // In flight the switch stays where the person put it: the write may yet
    // succeed, and snapping back first would read as the control ignoring them.
    expect(cb).toHaveProperty('checked', true)
    settle?.()
    await Promise.resolve()
    await Promise.resolve()
    expect(cb).toHaveProperty('checked', false)
  })

  it('leaves the control alone when the state did move', () => {
    const [checked, setChecked] = createSignal(false)
    render(() => (
      <Checkbox
        checked={checked()}
        onChange={(v) => {
          setChecked(v)
        }}
      />
    ))
    const cb = screen.getByRole('checkbox')
    fireEvent.click(cb)
    expect(untrack(checked)).toBe(true)
    expect(cb).toHaveProperty('checked', true)
  })

  it('is a native checkbox with keyboard support (Space handled by browser)', () => {
    subject()
    const cb = screen.getByRole('checkbox')
    expect(cb.tagName).toBe('INPUT')
    expect(cb.getAttribute('type')).toBe('checkbox')
  })
})
