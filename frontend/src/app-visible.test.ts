// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { createAppVisibility } from './app-visible'
import { setDocumentHidden } from './test-support/document-visibility'

describe('application visibility', () => {
  it('stops listening after destroy, and repeated destroy is harmless', () => {
    const restoreInitial = setDocumentHidden(false)
    try {
      const visibility = createAppVisibility()
      expect(visibility.visible()).toBe(true)

      const restoreHidden = setDocumentHidden(true)
      try {
        document.dispatchEvent(new Event('visibilitychange'))
        expect(visibility.visible()).toBe(false)
      } finally {
        restoreHidden()
      }

      const restoreShown = setDocumentHidden(false)
      try {
        document.dispatchEvent(new Event('visibilitychange'))
        expect(visibility.visible()).toBe(true)
      } finally {
        restoreShown()
      }

      visibility.destroy()
      const afterDestroy = visibility.visible()
      const restoreAfterDestroy = setDocumentHidden(true)
      try {
        document.dispatchEvent(new Event('visibilitychange'))
        expect(visibility.visible()).toBe(afterDestroy)
      } finally {
        restoreAfterDestroy()
      }

      expect(() => visibility.destroy()).not.toThrow()
    } finally {
      restoreInitial()
    }
  })
})
