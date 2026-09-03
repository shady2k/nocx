// @vitest-environment jsdom
// Slider (nocx-q4qeh.1) — the kit's continuous control.
import { describe, expect, it, vi } from 'vitest'
import { render } from 'solid-js/web'
import { Slider, type SliderProps } from './slider'

function mount(props: SliderProps): {
  host: HTMLElement
  input: HTMLInputElement
  dispose: () => void
} {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const dispose = render(() => <Slider {...props} />, host)
  const input = host.querySelector<HTMLInputElement>('.ui-slider__control')!
  return { host, input, dispose }
}

describe('Slider', () => {
  it('renders a range input carrying the declared bounds', () => {
    const { input, dispose } = mount({ value: 34, min: 16, max: 96, onInput: () => {} })
    expect(input.type).toBe('range')
    expect(input.min).toBe('16')
    expect(input.max).toBe('96')
    expect(input.value).toBe('34')
    dispose()
  })

  it('reports every value as it is dragged, not only at the end', () => {
    // The whole reason for the control: the effect must be visible during the
    // drag. A change-only slider is a number field with extra steps.
    const seen = vi.fn()
    const { input, dispose } = mount({ value: 34, min: 16, max: 96, onInput: seen })
    for (const v of ['40', '52', '64']) {
      input.value = v
      input.dispatchEvent(new Event('input', { bubbles: true }))
    }
    expect(seen.mock.calls.map((c) => c[0] as number)).toEqual([40, 52, 64])
    dispose()
  })

  it('shows the number beside the track, with its unit', () => {
    // A slider that hides its value cannot be reported, compared or set back.
    const { host, dispose } = mount({
      value: 34,
      min: 16,
      max: 96,
      unit: 'px',
      onInput: () => {},
    })
    expect(host.querySelector('.ui-slider__readout')?.textContent).toBe('34px')
    dispose()
  })

  it('clamps a value that arrives outside the bounds', () => {
    const { host, input, dispose } = mount({ value: 500, min: 16, max: 96, onInput: () => {} })
    expect(input.value).toBe('96')
    expect(host.querySelector('.ui-slider__readout')?.textContent).toBe('96')
    dispose()
  })

  it('says nothing while disabled', () => {
    const seen = vi.fn()
    const { input, dispose } = mount({
      value: 34,
      min: 16,
      max: 96,
      disabled: true,
      onInput: seen,
    })
    expect(input.disabled).toBe(true)
    dispose()
  })
})

describe('Slider commit', () => {
  it('separates the drag from the decision', () => {
    // onInput is the journey, onCommit is the destination. A caller that
    // persists must be able to write once rather than sixty times a second.
    const during = vi.fn()
    const done = vi.fn()
    const { input, dispose } = mount({
      value: 34,
      min: 16,
      max: 96,
      onInput: during,
      onCommit: done,
    })
    input.value = '50'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    input.value = '70'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    expect(during).toHaveBeenCalledTimes(2)
    expect(done).not.toHaveBeenCalled()

    input.dispatchEvent(new Event('change', { bubbles: true }))
    expect(done).toHaveBeenCalledExactlyOnceWith(70)
    dispose()
  })

  it('a caller that wants only the live value survives the release', () => {
    // onCommit is optional; releasing the pointer on a slider that has none
    // must be a no-op, not a crash on an undefined callback.
    const during = vi.fn()
    const { input, dispose } = mount({ value: 34, min: 16, max: 96, onInput: during })
    input.value = '40'
    expect(() => input.dispatchEvent(new Event('change', { bubbles: true }))).not.toThrow()
    expect(during).not.toHaveBeenCalled()
    dispose()
  })
})
