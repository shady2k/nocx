// @vitest-environment jsdom
// The chip as the owner will see it: a {{secret:NAME}} reference in the
// prompt renders as ONE atomic chip — the name visible, the braces never —
// and deletion leaves a document that still parses. The reference text stays
// in the document (that is what is stored, sent and resolved); only the
// rendering is a chip.
import { describe, it, expect, vi } from 'vitest'
import { EditorView } from '@codemirror/view'
import { CommandEditor } from './editor'
import { secretChipExtension } from './secret-chip'
import { createSecretChip, createSecretChipDamaged } from './ui/secret-chip'

const viewOf = (ed: CommandEditor): EditorView => {
  const withView = ed as unknown as { view: EditorView }
  return withView.view
}

const setup = () => {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const ed = new CommandEditor({ submit: vi.fn(), cancel: vi.fn() }, [secretChipExtension()])
  ed.mount(container)
  const view = viewOf(ed)
  return { ed, view, container }
}

const key = (view: EditorView, init: KeyboardEventInit) =>
  view.contentDOM.dispatchEvent(
    new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }),
  )

describe('createSecretChip (the emitter)', () => {
  it('renders the badge identity, the info tone and the name — never the value', () => {
    const chip = createSecretChip('openai-key')
    expect(chip.className).toContain('ui-badge')
    expect(chip.className).toContain('ui-secret-chip')
    expect(chip.dataset.tone).toBe('info')
    expect(chip.textContent).toContain('openai-key')
    expect(chip.textContent).not.toContain('{{secret:')
    expect(chip.querySelector('.ui-secret-chip__lock')).not.toBeNull()
  })
})

// §11.1's three states, never two. The middle one is what makes the badge
// safe rather than decorative: without it, "show the text when it does not
// match" would print the beginning of a live credential in the clear.
describe('createSecretChipDamaged (the emitter)', () => {
  it('names the secret and the SHAPE of the damage, and never a byte of it', () => {
    const chip = createSecretChipDamaged('API_TOKEN', 'truncated, 24 of 214 bytes')
    expect(chip.className).toContain('ui-badge')
    expect(chip.className).toContain('ui-secret-chip')
    expect(chip.dataset.variant).toBe('damaged')
    expect(chip.textContent).toContain('API_TOKEN')
    expect(chip.textContent).toContain('truncated, 24 of 214 bytes')
  })

  it('is visibly different from an intact chip by more than its colour', () => {
    const intact = createSecretChip('API_TOKEN')
    const damaged = createSecretChipDamaged('API_TOKEN', 'truncated, 24 of 214 bytes')
    // The tone differs…
    expect(damaged.dataset.tone).not.toBe(intact.dataset.tone)
    // …and so does the glyph and the text, because colour alone is not a
    // difference a person who cannot see it can read (WCAG 1.4.1).
    const glyph = (el: HTMLElement) => el.querySelector('.ui-secret-chip__lock')?.textContent
    expect(glyph(damaged)).not.toBe(glyph(intact))
    expect(damaged.textContent).not.toBe(intact.textContent)
    expect(damaged.querySelector('.ui-secret-chip__damage')).not.toBeNull()
    expect(intact.querySelector('.ui-secret-chip__damage')).toBeNull()
  })
})

describe('the chip in the editor (secretChipExtension)', () => {
  it('a reference renders as a chip with the name; the document keeps the reference', () => {
    const { ed, container } = setup()
    ed.show()
    ed.insertText('curl -H "Authorization: Bearer {{secret:openai-key}}" https://x')
    const chip = container.querySelector<HTMLElement>('.ui-secret-chip')
    expect(chip).not.toBeNull()
    expect(chip!.textContent).toContain('openai-key')
    // The document is untouched — what gets stored, sent and resolved.
    expect(ed.getDoc()).toBe('curl -H "Authorization: Bearer {{secret:openai-key}}" https://x')
  })

  it('two references render as two chips', () => {
    const { ed, container } = setup()
    ed.show()
    ed.insertText('echo {{secret:a}} {{secret:b}}')
    expect(container.querySelectorAll('.ui-secret-chip').length).toBe(2)
  })

  it('the caret steps over the chip as one unit (ArrowLeft and ArrowRight)', () => {
    const { ed, view } = setup()
    ed.show()
    ed.insertText('x{{secret:openai-key}}y')
    const refStart = 1
    const refEnd = 1 + '{{secret:openai-key}}'.length
    // Just after the chip: one ArrowLeft lands before it, never inside.
    view.dispatch({ selection: { anchor: refEnd } })
    key(view, { key: 'ArrowLeft' })
    expect(view.state.selection.main.head).toBe(refStart)
    // Just before the chip: one ArrowRight lands after it.
    view.dispatch({ selection: { anchor: refStart } })
    key(view, { key: 'ArrowRight' })
    expect(view.state.selection.main.head).toBe(refEnd)
  })

  it('Backspace removes the WHOLE reference, leaving a document that still parses', () => {
    const { ed, view } = setup()
    ed.show()
    ed.insertText('curl {{secret:openai-key}} https://x')
    view.dispatch({ selection: { anchor: ed.getDoc().length } })
    // Walk back over the trailing text to land just after the reference.
    view.dispatch({ selection: { anchor: 'curl {{secret:openai-key}}'.length } })
    key(view, { key: 'Backspace' })
    const doc = ed.getDoc()
    expect(doc).toBe('curl  https://x')
    expect(doc).not.toContain('{{secret:')
    expect(doc).not.toContain('NAM')
  })

  it('Backspace BEFORE the chip deletes the preceding character only — never a chip fragment', () => {
    const { ed, view } = setup()
    ed.show()
    ed.insertText('x{{secret:openai-key}}')
    view.dispatch({ selection: { anchor: 1 } })
    key(view, { key: 'Backspace' })
    const doc = ed.getDoc()
    expect(doc).toBe('{{secret:openai-key}}')
  })

  it('typing over a selection that includes a chip replaces the whole reference', () => {
    const { ed, view } = setup()
    ed.show()
    ed.insertText('{{secret:a}} tail')
    view.dispatch({ selection: { anchor: 0, head: '{{secret:a}}'.length } })
    ed.insertText('rm -rf /tmp/x')
    expect(ed.getDoc()).toBe('rm -rf /tmp/x tail')
    // No chip residue.
    expect(ed.getDoc()).not.toContain('{{secret:')
  })
})
