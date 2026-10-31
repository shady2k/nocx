// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { createCommandBlockFrame, setCommandBlockFrameVariant } from './command-block-frame'

describe('CommandBlockFrame running variance', () => {
  it('keeps frozen layout as the default and explicitly opts running headers in', () => {
    const frozen = createCommandBlockFrame()
    expect(frozen.header.hasAttribute('data-variant')).toBe(false)
    const running = createCommandBlockFrame({ variant: 'running' })
    expect(running.header.dataset.variant).toBe('running')
    expect(running.header.classList.contains('ui-command-block-frame')).toBe(true)
    setCommandBlockFrameVariant(running.header, undefined)
    expect(running.header.hasAttribute('data-variant')).toBe(false)
  })
})
