// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { CommandEditor } from './editor'

/** An editor with no callbacks. */
function emptyEditor(): CommandEditor {
  return new CommandEditor({
    submit: vi.fn(),
    cancel: vi.fn(),
  })
}

describe('recovery action chip (nocx-atyf.2)', () => {
  let editor: CommandEditor

  beforeEach(() => {
    editor = emptyEditor()
    document.body.appendChild(editor.root)
  })

  it('shows nothing in the healthy state — the chip is hidden', () => {
    editor.setRecoveryAction(null, vi.fn())
    const chip = editor.root.querySelector<HTMLElement>('[data-control="recovery"]')
    expect(chip).not.toBeNull()
    expect(chip!.style.display).toBe('none')
  })

  it('renders the recovery action label when set', () => {
    const onClick = vi.fn()
    editor.setRecoveryAction('Enable command editor', onClick)
    const chip = editor.root.querySelector<HTMLElement>('[data-control="recovery"]')
    expect(chip).not.toBeNull()
    expect(chip!.style.display).not.toBe('none')
    expect(chip!.textContent).toBe('Enable command editor')
  })

  it('clicking the chip performs the action directly, with no popover', () => {
    const onClick = vi.fn()
    editor.setRecoveryAction('Retry integration', onClick)
    const chip = editor.root.querySelector<HTMLElement>('[data-control="recovery"]')!
    chip.click()
    expect(onClick).toHaveBeenCalledOnce()
  })

  it('setting null hides the chip again', () => {
    const onClick = vi.fn()
    editor.setRecoveryAction('Restore command editor', onClick)
    let chip = editor.root.querySelector<HTMLElement>('[data-control="recovery"]')
    expect(chip!.style.display).not.toBe('none')

    editor.setRecoveryAction(null, vi.fn())
    chip = editor.root.querySelector<HTMLElement>('[data-control="recovery"]')
    expect(chip).not.toBeNull()
    expect(chip!.style.display).toBe('none')
  })
})
