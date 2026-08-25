// @vitest-environment jsdom
/**
 * FactList — the kit's read-only named facts (nocx-n7xha).
 *
 * What it must do: render every fact it is given as a named row, in the
 * order it was given, and never drop one. That is the whole property the
 * approval prompt leans on — a list that silently omits a row would let an
 * argument disappear from the question a person is answering, which is the
 * defect the prompt was using a verbatim JSON blob to avoid.
 *
 * `note` is the honest half: a value that must not be read as more certain
 * than it is carries its qualification on the row rather than in a
 * paragraph somewhere else, where it can drift away from the value it
 * qualifies.
 */
import { cleanup, render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it } from 'vitest'
import { FactList } from './fact-list'

afterEach(cleanup)

describe('FactList — named facts, read-only', () => {
  it('renders one named row per fact, in order, with the kit identities', () => {
    const { container } = render(() => (
      <FactList
        facts={[
          { name: 'sessionId', value: 'home/dev on this machine' },
          { name: 'command', value: 'df -h' },
        ]}
      />
    ))
    const list = container.querySelector('.ui-fact-list')
    expect(list).toBeTruthy()
    const names = Array.from(container.querySelectorAll('.ui-fact-list__name')).map(
      (n) => n.textContent,
    )
    const values = Array.from(container.querySelectorAll('.ui-fact-list__value')).map((v) =>
      v.textContent?.trim(),
    )
    expect(names).toEqual(['sessionId', 'command'])
    expect(values).toEqual(['home/dev on this machine', 'df -h'])
  })

  it('drops nothing: every fact given is a row', () => {
    const facts = Array.from({ length: 7 }, (_, i) => ({ name: `k${i}`, value: `v${i}` }))
    const { container } = render(() => <FactList facts={facts} />)
    expect(container.querySelectorAll('.ui-fact-list__row')).toHaveLength(7)
  })

  it('carries a note beside the value it qualifies, never elsewhere', () => {
    const { container } = render(() => (
      <FactList
        facts={[
          { name: 'a', value: 'certain' },
          { name: 'working directory', value: '/repo', note: 'not confirmed' },
        ]}
      />
    ))
    const notes = Array.from(container.querySelectorAll('.ui-fact-list__note'))
    expect(notes).toHaveLength(1)
    expect(notes[0].textContent).toBe('not confirmed')
    // The note belongs to the row whose value it qualifies — a note that
    // could float to another row would be worse than no note at all.
    const rows = Array.from(container.querySelectorAll('.ui-fact-list__row'))
    expect(rows[0].querySelector('.ui-fact-list__note')).toBeNull()
    expect(rows[1].querySelector('.ui-fact-list__note')).toBeTruthy()
  })

  it('renders nothing at all when there are no facts — an empty box is a bug', () => {
    const { container } = render(() => <FactList facts={[]} />)
    expect(container.querySelector('.ui-fact-list')).toBeNull()
  })

  it('names itself for a screen reader when the caller says what the list is', () => {
    const { container } = render(() => (
      <FactList facts={[{ name: 'a', value: 'b' }]} ariaLabel="What run would do" />
    ))
    expect(container.querySelector('.ui-fact-list')?.getAttribute('aria-label')).toBe(
      'What run would do',
    )
  })

  it('is a definition list: each name is the term of its own value', () => {
    const { container } = render(() => <FactList facts={[{ name: 'path', value: '/repo/a' }]} />)
    expect(container.querySelector('dl.ui-fact-list')).toBeTruthy()
    expect(container.querySelector('dt.ui-fact-list__name')?.textContent).toBe('path')
    expect(container.querySelector('dd.ui-fact-list__value')?.textContent).toBe('/repo/a')
  })
})
