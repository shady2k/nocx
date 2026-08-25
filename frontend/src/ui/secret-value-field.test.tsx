// @vitest-environment jsdom
//
// The one property this component exists to keep: THE VALUE PASSES THROUGH.
// Every case below is really asking where the value ends up — handed to the
// caller once, and in no field, no attribute and no signal afterwards.
import { describe, expect, it, afterEach, vi } from 'vitest'
import { render, cleanup, fireEvent } from '@solidjs/testing-library'
import { SecretValueField } from './secret-value-field'

afterEach(() => cleanup())

const VALUE = 'sk-live-9f2c4e7a11b3d8'

function mount(over: Partial<Parameters<typeof SecretValueField>[0]> = {}) {
  return render(() => (
    <SecretValueField
      id="secret-value"
      ariaLabel="The value"
      placeholder="Paste it"
      title="Store the value"
      onSubmit={over.onSubmit ?? vi.fn().mockResolvedValue(undefined)}
      disabled={over.disabled}
      actionLabel={over.actionLabel}
    />
  ))
}

const input = (): HTMLInputElement =>
  document.querySelector<HTMLInputElement>('#secret-value') as HTMLInputElement

const action = (): HTMLButtonElement =>
  document.querySelector<HTMLButtonElement>('.ui-secret-value-field button') as HTMLButtonElement

describe('the field a secret value is typed into', () => {
  it('hands the value over once and empties itself afterwards', async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined)
    mount({ onSubmit })

    fireEvent.input(input(), { target: { value: VALUE } })
    fireEvent.click(action())

    await vi.waitFor(() => expect(onSubmit).toHaveBeenCalledWith(VALUE))
    expect(onSubmit).toHaveBeenCalledTimes(1)
    await vi.waitFor(() => expect(input().value).toBe(''))
    // …and no byte of it is left anywhere in the markup either: a value can
    // ride an attribute no text assertion would look at.
    expect(document.body.innerHTML).not.toContain(VALUE)
  })

  it('is a password field, because somebody may be looking at the screen', () => {
    mount()
    expect(input().type).toBe('password')
  })

  it('a refusal keeps what was typed and says why', async () => {
    // The opposite of the clear above, and the reason the clear is on success
    // only: emptying the field on a refusal costs the person a value they may
    // not have anywhere else.
    const { container } = mount({ onSubmit: vi.fn().mockRejectedValue(new Error('sealed')) })

    fireEvent.input(input(), { target: { value: VALUE } })
    fireEvent.click(action())

    await vi.waitFor(() => expect(container.textContent).toContain('sealed'))
    expect(input().value).toBe(VALUE)
  })

  it('the action is refused while there is nothing to store', () => {
    mount()
    expect(action().disabled).toBe(true)
    fireEvent.input(input(), { target: { value: 'x' } })
    expect(action().disabled).toBe(false)
  })

  it('a caller that cannot address the value refuses the write, not the typing', () => {
    // `disabled` is the surface saying "there is nowhere to put this yet" —
    // the field still takes the paste, because the reason is one the person
    // can still fix without losing what they pasted.
    const onSubmit = vi.fn().mockResolvedValue(undefined)
    mount({ disabled: true, onSubmit })

    fireEvent.input(input(), { target: { value: VALUE } })
    expect(input().value).toBe(VALUE)
    expect(action().disabled).toBe(true)
    fireEvent.click(action())
    expect(onSubmit).not.toHaveBeenCalled()
  })
})
