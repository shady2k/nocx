import { describe, expect, it } from 'vitest'

import { isDriverState, paneIndicator } from './pane-observation'

describe('the boundary guard', () => {
  it('lets every member of the closed set through', () => {
    for (const s of [
      'free_text',
      'permission_choice',
      'modal_choice',
      'working',
      'unknown',
      'exited',
    ]) {
      expect(isDriverState(s)).toBe(true)
    }
  })

  // A value nobody wrote a branch for would land in whichever branch was
  // written last, and every consumer of this treats what it cannot read as
  // busy — which only works if what it cannot read never arrives.
  it('refuses anything else', () => {
    for (const s of ['busy', '', 'Free_Text', null, undefined, 3, {}]) {
      expect(isDriverState(s)).toBe(false)
    }
  })
})

describe('what the tab shows', () => {
  it('shows what the driver said, marked as the strong source', () => {
    expect(paneIndicator('free_text', null)).toEqual({ activity: 'idle', source: 'driver' })
    expect(paneIndicator('working', null)).toEqual({ activity: 'working', source: 'driver' })
    expect(paneIndicator('exited', null)).toEqual({ activity: 'exited', source: 'driver' })
  })

  // Both menus need a person identically. Which menu it is decides whether
  // answering it answers the agent, and that is the typing caller's question.
  it('collapses both kinds of menu to one thing a person can act on', () => {
    expect(paneIndicator('permission_choice', null)?.activity).toBe('waiting')
    expect(paneIndicator('modal_choice', null)?.activity).toBe('waiting')
  })

  // The title still lights the indicator for the panes that have no driver,
  // and says so.
  it('falls back to the title, on the weaker source', () => {
    expect(paneIndicator(null, 'working')).toEqual({ activity: 'working', source: 'title' })
    expect(paneIndicator(null, 'idle')).toEqual({ activity: 'idle', source: 'title' })
  })

  // THE POINT OF HAVING A DRIVER. A pane blocked on a permission dialog keeps
  // a spinner in its title, so the title says 'working' — a busy worker that
  // is in fact waiting for a person. The driver's answer displaces it.
  it('prefers the driver over a title that disagrees with it', () => {
    expect(paneIndicator('permission_choice', 'working')).toEqual({
      activity: 'waiting',
      source: 'driver',
    })
  })

  // And displaces it even when the driver cannot tell, because "this screen
  // could not be read" is a better answer than a confident wrong one.
  it("prefers the driver's unknown over the title's confidence", () => {
    expect(paneIndicator('unknown', 'idle')).toEqual({ activity: 'unknown', source: 'driver' })
  })

  // A title that never mentioned an agent is not an idle agent.
  it('says nothing at all when neither source has anything', () => {
    expect(paneIndicator(null, null)).toBeNull()
  })
})
