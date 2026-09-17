// @vitest-environment jsdom
/**
 * BlockNotice (nocx-2019q) — what a block says about itself after the fact.
 *
 * What a user can do here: read one sentence about something that just became
 * true, and act on it. The assertions are therefore about what is ON the line
 * and what activating an action reaches — never about the shape of the DOM
 * for its own sake.
 */
import { describe, it, expect, vi } from 'vitest'
import { BlockNotice } from './block-notice'

const actions = (root: HTMLElement) =>
  Array.from(root.querySelectorAll<HTMLButtonElement>('.ui-block-notice__actions .ui-button'))

describe('BlockNotice', () => {
  it('states the fact and offers its actions, in the kit’s own button identity', () => {
    const undo = vi.fn()
    const notice = new BlockNotice({
      text: 'Saved: df -h — in every session, from now on',
      actions: [
        { label: 'Undo', onActivate: undo },
        { label: 'Manage permissions', onActivate: vi.fn() },
      ],
    })

    expect(notice.root.textContent).toContain('Saved: df -h — in every session, from now on')
    expect(actions(notice.root).map((b) => b.textContent)).toEqual(['Undo', 'Manage permissions'])
    // The kit's identity, not a hand-rolled control: a surface that painted
    // its own button here would be a second vocabulary for one concept.
    expect(actions(notice.root)[0].classList.contains('ui-button')).toBe(true)

    actions(notice.root)[0].click()
    expect(undo).toHaveBeenCalledTimes(1)
  })

  it('is announced as a status rather than interrupting what is being read', () => {
    const notice = new BlockNotice({ text: 'Saved.' })
    expect(notice.root.getAttribute('role')).toBe('status')
    expect(notice.root.getAttribute('aria-live')).toBe('polite')
  })

  it('RESTATES in place, so the line never carries two truths at once', () => {
    const notice = new BlockNotice({
      text: 'Saved: df -h — in every session, from now on',
      actions: [{ label: 'Undo', onActivate: vi.fn() }],
    })

    notice.say({ text: 'Undone: that answer is no longer saved.' })

    expect(notice.root.textContent).toContain('Undone: that answer is no longer saved.')
    expect(notice.root.textContent).not.toContain('in every session')
    // The Undo went with the fact it undid — an action that can no longer do
    // anything must not stay on screen offering to.
    expect(actions(notice.root)).toHaveLength(0)
  })

  it('wears the tone of what it is reporting, so a degrade is visible as one', () => {
    const notice = new BlockNotice({ text: 'Saved.' })
    expect(notice.root.dataset.tone).toBe('saved')

    notice.say({ text: 'It could not be saved.', tone: 'warning' })
    expect(notice.root.dataset.tone).toBe('warning')
  })
})
