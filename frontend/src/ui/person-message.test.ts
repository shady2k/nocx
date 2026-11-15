// @vitest-environment jsdom

import { describe, it, expect } from 'vitest'
import { PersonMessage } from './person-message'

describe('PersonMessage', () => {
  it('states who spoke and what they said, as the person wrote it', () => {
    const drawn = new PersonMessage('also check the second log\nit scrolled past', {
      label: 'You',
      aria: 'your message while the answer was written',
    })
    expect(drawn.root.classList.contains('ui-person-message')).toBe(true)
    expect(drawn.root.querySelector('.ui-person-message__label')?.textContent).toBe('You')
    // Verbatim: the same words on their own lines. A message a person typed is
    // not prose to be reflowed or paraphrased.
    expect(drawn.root.querySelector('.ui-person-message__text')?.textContent).toBe(
      'also check the second log\nit scrolled past',
    )
  })

  it('carries the words in the accessible name, not only the label', () => {
    const drawn = new PersonMessage('use the second log', {
      label: 'You',
      aria: 'your message while the answer was written',
    })
    expect(drawn.root.getAttribute('role')).toBe('note')
    expect(drawn.root.getAttribute('aria-label')).toBe('your message while the answer was written')
    expect(drawn.root.textContent).toContain('use the second log')
  })

  it('never reads as the agent\u2019s own words', () => {
    // The line is drawn as text, never as markup: a message that looks like
    // the answer is a message the person cannot tell from it afterwards.
    const drawn = new PersonMessage('<b>not bold</b>', { label: 'You', aria: 'your message' })
    expect(drawn.root.querySelector('b')).toBeNull()
    expect(drawn.root.textContent).toContain('<b>not bold</b>')
  })
})
