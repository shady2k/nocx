// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { createButton } from './button-element'

describe('createButton', () => {
  it('prevents activation when disabled', () => {
    const onClick = vi.fn<(event: MouseEvent) => void>()
    createButton({ label: 'x', disabled: true, onClick }).click()
    expect(onClick).not.toHaveBeenCalled()
  })
})
