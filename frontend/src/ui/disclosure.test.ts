// @vitest-environment jsdom
//
// Disclosure (ui/disclosure.ts) — the kit contract, pinned: a native
// disclosure that starts CLOSED, a body that concatenates chunks without
// showing their boundaries, and nothing at all rendered for a model that
// thought nothing (which is the caller's job: the thinking note is built at
// the first chunk, so a run with no reasoning never constructs one).
import { describe, it, expect } from 'vitest'
import { createDisclosure } from './disclosure'

const thinking = (expanded?: boolean) =>
  createDisclosure({
    kind: 'reasoning',
    summary: 'Thinking',
    ...(expanded === undefined ? {} : { expanded }),
  })

describe('Disclosure', () => {
  it('is a closed disclosure under the kit identity class', () => {
    const note = thinking()
    expect(note.el.classList.contains('ui-disclosure')).toBe(true)
    expect(note.el.tagName).toBe('DETAILS')
    expect(note.el.open).toBe(false)
    expect(note.el.querySelector('.ui-disclosure__summary')?.textContent).toBe('Thinking')
  })

  it('wears its kind, which is what selects the two apart', () => {
    expect(thinking().el.dataset.kind).toBe('reasoning')
    expect(createDisclosure({ kind: 'tool-result', summary: 'Result' }).el.dataset.kind).toBe(
      'tool-result',
    )
    expect(
      createDisclosure({ kind: 'tool-result', summary: 'Result' }).el.querySelector(
        '.ui-disclosure__summary',
      )?.textContent,
    ).toBe('Result')
  })

  it('opens on request — the setting decides the default, the note obeys it (nocx-y9e88)', () => {
    expect(thinking(true).el.open).toBe(true)
    expect(thinking(false).el.open).toBe(false)
    // Still a native disclosure: opened by default is not opened for good,
    // and the summary is what closes it again.
    expect(thinking(true).el.tagName).toBe('DETAILS')
  })

  it('concatenates chunks, so a split mid-word leaves no seam', () => {
    const note = thinking()
    note.append('the user asks about ')
    note.append('the screen')
    expect(note.el.querySelector('.ui-disclosure__body')?.textContent).toBe(
      'the user asks about the screen',
    )
  })

  it('ignores an empty chunk rather than growing the body with nothing', () => {
    const note = thinking()
    note.append('')
    expect(note.el.querySelector('.ui-disclosure__body')?.textContent).toBe('')
  })

  it('replaces the body for content that arrives whole, so a second landing does not double it', () => {
    const result = createDisclosure({ kind: 'tool-result', summary: 'Result' })
    result.set('{"ok":true}')
    result.set('{"ok":true}')
    expect(result.el.querySelector('.ui-disclosure__body')?.textContent).toBe('{"ok":true}')
  })
})
