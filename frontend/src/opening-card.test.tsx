// @vitest-environment jsdom
//
// The pane's stated wait (nocx-q9od5). What is asserted is that the card
// reports what the caller told it and nothing it was not told: a title the
// open names, the byte count when the backend reported one, the host always,
// and a Cancel that is the call's own withdrawal.
import { cleanup } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { mountOpeningCard, type OpeningCardState } from './opening-card'

const active: (() => void)[] = []

afterEach(() => {
  while (active.length > 0) active.pop()!()
  cleanup()
  document.body.innerHTML = ''
})

function subject(initial: OpeningCardState) {
  const host = document.createElement('div')
  document.body.append(host)
  const card = mountOpeningCard(host, initial)
  active.push(() => card.dispose())
  const text = (): string => host.textContent ?? ''
  return { host, card, text }
}

describe('the pane states what a slow open is doing', () => {
  it('names the open, the host and no byte count it was not given', () => {
    const { text } = subject({
      title: 'Opening your terminal',
      host: 'ana@build-box',
      onCancel: () => {},
    })
    expect(text()).toContain('Opening your terminal')
    expect(text()).toContain('ana@build-box')
    // NOTHING WAS REPORTED, SO NOTHING IS CLAIMED: a card that drew "0 B of
    // 15.3 MB" from silence would report a transfer nobody had started.
    expect(text()).not.toContain(' MB')
    expect(text()).toContain('Cancel')
  })

  it('states the transfer once the backend reports one, and keeps the host', () => {
    const { card, text } = subject({
      title: 'Opening your terminal',
      host: 'ana@build-box',
      onCancel: () => {},
    })
    card.set({
      title: 'Copying the nocx helper',
      host: 'ana@build-box',
      progress: { done: 3_200_000, total: 15_300_000 },
      onCancel: () => {},
    })
    expect(text()).toContain('Copying the nocx helper')
    expect(text()).toContain('3.2 MB of 15.3 MB')
    expect(text()).toContain('ana@build-box')
  })

  it('cancels the open it is about, and only that', () => {
    const cancel = vi.fn()
    const { host } = subject({ title: 'Opening your terminal', host: 'h', onCancel: cancel })
    const button = host.querySelector('button')
    if (button === null) throw new Error('the card drew no Cancel')
    button.click()
    expect(cancel).toHaveBeenCalledTimes(1)
  })
})
