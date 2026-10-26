// @vitest-environment jsdom
import { cleanup } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { mountReconnectOffer, type ReconnectOfferProps } from './reconnect-offer'

const mounted: { dispose(): void }[] = []

afterEach(() => {
  while (mounted.length > 0) mounted.pop()!.dispose()
  cleanup()
})

function card(props: Partial<ReconnectOfferProps> = {}): HTMLElement {
  const target = document.createElement('div')
  document.body.append(target)
  mounted.push(
    mountReconnectOffer(target, {
      host: '',
      attempting: false,
      onReconnect: vi.fn(),
      ...props,
    }),
  )
  return target
}

describe('ReconnectOffer actions', () => {
  it('opens a new shell only when the person presses Reconnect', () => {
    const onReconnect = vi.fn()
    const target = card({ onReconnect })
    expect(onReconnect).not.toHaveBeenCalled()
    target.querySelector<HTMLButtonElement>('button')!.click()
    expect(onReconnect).toHaveBeenCalledTimes(1)
  })

  it('prevents another request while reconnecting', () => {
    const onReconnect = vi.fn()
    const target = card({ attempting: true, onReconnect })
    expect(target.querySelector('button')).toBeNull()
    expect(onReconnect).not.toHaveBeenCalled()
  })

  it('restores the action after an attempt settles and removes it on disposal', () => {
    const target = document.createElement('div')
    document.body.append(target)
    const onReconnect = vi.fn()
    const props = { host: '', attempting: true, onReconnect }
    const offer = mountReconnectOffer(target, props)
    offer.set({ ...props, attempting: false })
    target.querySelector<HTMLButtonElement>('button')!.click()
    expect(onReconnect).toHaveBeenCalledTimes(1)
    offer.dispose()
    expect(target.querySelector('button')).toBeNull()
  })
})
