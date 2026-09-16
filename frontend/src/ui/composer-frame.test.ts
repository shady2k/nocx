// @vitest-environment jsdom
//
// ComposerFrame (spec 2026-09-15 §4): the composer's outer inset card and
// open input row and keyboard hints. editor.ts is the one real caller; these tests assert
// the structural contract other files depend on — root/field/editor/submit
// identity, slot order, and the typed focus projection — without pulling in
// CM6 (editor.test.ts and ask-entry.test.ts each cover their own half of
// that integration).
import { describe, expect, it } from 'vitest'
import { createComposerFrame } from './composer-frame'

describe('createComposerFrame', () => {
  it('places the given chrome inside the card, above the field', () => {
    const chrome = document.createElement('div')
    chrome.className = 'nocx-editor-chrome'
    const frame = createComposerFrame(chrome)

    expect(frame.root.classList.contains('ui-composer-frame')).toBe(true)
    expect([...frame.root.children].slice(0, 2)).toEqual([chrome, frame.field])
    expect(frame.root.textContent).toContain('↵run')
    frame.setSubmitHint('ask')
    expect(frame.root.textContent).toContain('↵ask')
    frame.setSwitchHint(['⌘', '↵'], 'run')
    expect(frame.root.querySelector('[data-hint="switch"]')?.textContent).toBe('⌘↵run')
  })

  it('never repaints the caller’s chrome — it does not touch its class, attributes or children', () => {
    const chrome = document.createElement('div')
    chrome.className = 'nocx-editor-chrome'
    const marker = document.createElement('span')
    chrome.appendChild(marker)
    const frame = createComposerFrame(chrome)

    expect(chrome.className).toBe('nocx-editor-chrome')
    expect(chrome.contains(marker)).toBe(true)
    expect(chrome.parentElement).toBe(frame.root)
  })

  it('the field holds exactly the editor and submit slots, in that order — the target switch is inserted ahead of them by a caller, not built here', () => {
    const frame = createComposerFrame(document.createElement('div'))
    expect([...frame.field.children]).toEqual([frame.editor, frame.submit])
    expect(frame.editor.classList.contains('ui-composer-frame__editor')).toBe(true)
    expect(frame.submit.classList.contains('ui-composer-frame__submit')).toBe(true)
  })

  it('starts unfocused, and setFocused projects a typed attribute on the field — never a class', () => {
    const frame = createComposerFrame(document.createElement('div'))
    expect(frame.field.dataset.focused).toBe('false')
    frame.setFocused(true)
    expect(frame.field.dataset.focused).toBe('true')
    expect(frame.field.className).toBe('ui-composer-frame__field') // untouched by focus
    frame.setFocused(false)
    expect(frame.field.dataset.focused).toBe('false')
  })

  it('dispose removes the card from its parent, taking the caller’s chrome with it', () => {
    const chrome = document.createElement('div')
    const frame = createComposerFrame(chrome)
    const parent = document.createElement('div')
    parent.appendChild(frame.root)
    expect(parent.contains(frame.root)).toBe(true)

    frame.dispose()
    expect(parent.contains(frame.root)).toBe(false)
    expect(parent.contains(chrome)).toBe(false)
  })
})
