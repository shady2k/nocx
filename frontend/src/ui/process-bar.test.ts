// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { createProcessBar, updateProcessBar } from './process-bar'

function actions() {
  return {
    onSendInput: vi.fn(),
    onStop: vi.fn(),
    onInterrupt: vi.fn(),
  }
}

describe('createProcessBar — the DOM contract', () => {
  it('renders "Process running" and the three controls, each reaching its own callback', () => {
    const acts = actions()
    const el = createProcessBar(acts)
    expect(el.className).toBe('ui-process-bar')
    expect(el.textContent).toContain('Process running')

    const sendInput = el.querySelector<HTMLButtonElement>('[data-control="send-input"]')
    const interrupt = el.querySelector<HTMLButtonElement>('[data-control="interrupt"]')
    const stop = el.querySelector<HTMLButtonElement>('[data-control="stop"]')
    expect(sendInput?.textContent).toBe('Send input')
    expect(interrupt?.textContent).toBe('Interrupt')
    expect(stop?.textContent).toBe('Stop')

    sendInput?.click()
    expect(acts.onSendInput).toHaveBeenCalledTimes(1)
    interrupt?.click()
    expect(acts.onInterrupt).toHaveBeenCalledTimes(1)
    stop?.click()
    expect(acts.onStop).toHaveBeenCalledTimes(1)

    // Stop and Interrupt are never the same handler — the decision record's
    // semantic-mismatch fix (`Stop Ctrl+C` is not accurate). Confirms the
    // three calls above landed on three DISTINCT mocks, not one shared one.
    expect(acts.onStop).not.toBe(acts.onInterrupt)
  })

  it('the Ctrl+C hint sits beside Interrupt, not inside Stop', () => {
    const el = createProcessBar(actions())
    const stop = el.querySelector<HTMLButtonElement>('[data-control="stop"]')
    expect(stop?.textContent).toBe('Stop')
    expect(el.querySelector('.ui-process-bar__hint')?.textContent).toBe('Ctrl+C')
    const interrupt = el.querySelector<HTMLButtonElement>('[data-control="interrupt"]')
    expect(interrupt?.title).toBe('Ctrl+C')
  })

  it('updateProcessBar disables Send input alone when there is no writable target', () => {
    const el = createProcessBar(actions())
    updateProcessBar(el, { inputAvailable: false })
    expect(el.querySelector<HTMLButtonElement>('[data-control="send-input"]')?.disabled).toBe(true)
    // Stop/Interrupt stay enabled: signalActiveCommand refuses on its own
    // when nothing is running, so this component does not duplicate that
    // check (process-bar.ts's own comment on updateProcessBar).
    expect(el.querySelector<HTMLButtonElement>('[data-control="stop"]')?.disabled).toBe(false)
    expect(el.querySelector<HTMLButtonElement>('[data-control="interrupt"]')?.disabled).toBe(false)

    updateProcessBar(el, { inputAvailable: true })
    expect(el.querySelector<HTMLButtonElement>('[data-control="send-input"]')?.disabled).toBe(false)
  })
})
