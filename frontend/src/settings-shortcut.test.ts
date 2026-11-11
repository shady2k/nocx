// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { IntentInput } from './intent-input'
import { mountSettingsShortcut } from './settings-shortcut'

describe('Settings shortcut ownership', () => {
  it.each(['metaKey', 'ctrlKey'] as const)(
    'opens Settings without delivering %s comma to the terminal',
    (modifier) => {
      const openSettings = vi.fn()
      const emit = vi.fn()
      const input = new IntentInput({ emit })
      document.body.append(input.element)
      const dispose = mountSettingsShortcut(openSettings)
      try {
        const event = new KeyboardEvent('keydown', {
          key: ',',
          code: 'Comma',
          [modifier]: true,
          bubbles: true,
          cancelable: true,
        })
        input.element.dispatchEvent(event)
        expect(openSettings).toHaveBeenCalledOnce()
        expect(event.defaultPrevented).toBe(true)
        expect(emit).not.toHaveBeenCalled()
      } finally {
        dispose()
        input.destroy()
        input.element.remove()
      }
    },
  )
})
