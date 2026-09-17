import { describe, expect, it } from 'vitest'

import { isDriverState, paneIndicator, readPaneChildren, sameChildren } from './pane-observation'

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

describe('an unreachable host', () => {
  // The dot must not go OUT: no dot means "there is no agent in this pane",
  // which is a different fact and one the tab already draws. An unobservable
  // agent is not an absent one — and that distinction is worth most exactly
  // when it is lost, scanning tabs after a suspend to see which to rescue.
  it('leaves the dot showing, as unknown', () => {
    expect(paneIndicator('working', null, false)).toEqual({
      activity: 'unknown',
      source: 'driver',
    })
  })

  // "Waiting for you" over a dead pipe calls a person to answer something
  // that cannot be delivered.
  it('stops asserting that the agent is waiting for a person', () => {
    expect(paneIndicator('permission_choice', null, false)?.activity).toBe('unknown')
    expect(paneIndicator(null, 'idle', false)?.activity).toBe('unknown')
  })

  // A pane with no agent still has no agent.
  it('invents nothing for a pane that had nothing to say', () => {
    expect(paneIndicator(null, null, false)).toBeNull()
  })

  // A slow host is still an observed one: bytes are arriving, just late.
  it('does not touch the indicator while the host is merely reachable', () => {
    expect(paneIndicator('working', null, true)?.activity).toBe('working')
    expect(paneIndicator('working', null)?.activity).toBe('working')
  })
})

describe("the TUI's own error", () => {
  it('is its own activity and collapses into neither neighbour', () => {
    const err = paneIndicator('error', null)
    expect(err).toEqual({ activity: 'error', source: 'driver' })

    // The two it would otherwise be mistaken for. 'working' says leave it
    // alone and 'waiting' says answer something that is not there; the whole
    // point of the value is that neither is true.
    expect(paneIndicator('working', null)?.activity).toBe('working')
    expect(paneIndicator('permission_choice', null)?.activity).toBe('waiting')
    expect(err?.activity).not.toBe('working')
    expect(err?.activity).not.toBe('waiting')
  })

  it('is a state the boundary guard lets through', () => {
    expect(isDriverState('error')).toBe(true)
  })
})

// ── The child rows at the boundary (nocx-o1v0h) ───────────────────────────

describe('readPaneChildren', () => {
  it('reads the rows the notification carried, name and task', () => {
    expect(
      readPaneChildren([{ name: 'Explore', task: 'List files in directory' }, { name: 'Plan' }]),
    ).toEqual([{ name: 'Explore', task: 'List files in directory' }, { name: 'Plan' }])
  })

  // Absent and empty are different claims and only one of them is ever true:
  // the backend cannot say "the panel is on screen and names nobody". A
  // renderer that accepted an empty list would draw an expanded parent with
  // nothing under it, which reads as children that vanished.
  it('answers null for absent, for a non-array, and for an empty list', () => {
    expect(readPaneChildren(undefined)).toBeNull()
    expect(readPaneChildren(null)).toBeNull()
    expect(readPaneChildren('Explore')).toBeNull()
    expect(readPaneChildren([])).toBeNull()
  })

  // The guard is the point: this is an unsolicited notification, nothing
  // correlates it, and what survives it reaches a surface that draws it.
  it('drops a row with no usable name rather than drawing a blank one', () => {
    expect(
      readPaneChildren([{ name: '' }, { task: 'orphan' }, 7, null, { name: 'Explore' }]),
    ).toEqual([{ name: 'Explore' }])
    // And if that empties the list, the whole list is absent — the same claim
    // an empty array would have made wrongly.
    expect(readPaneChildren([{ name: '' }, { task: 'orphan' }])).toBeNull()
  })

  // A task the screen did not carry is ABSENT, never an empty string: the two
  // are different claims and only the first is true, and a row rendering `''`
  // draws a separator with nothing after it.
  it('omits a task the notification did not carry', () => {
    const rows = readPaneChildren([{ name: 'Explore', task: '' }])
    expect(rows).toEqual([{ name: 'Explore' }])
    expect(rows?.[0]).not.toHaveProperty('task')
  })
})

describe('sameChildren', () => {
  it('is true only for the same rows in the same order', () => {
    const a = [{ name: 'Explore', task: 'List files' }, { name: 'Plan' }]
    expect(sameChildren(a, [{ name: 'Explore', task: 'List files' }, { name: 'Plan' }])).toBe(true)
    // The panel's order is the lineage's order, so a reordering is a
    // different screen and has to repaint.
    expect(sameChildren(a, [{ name: 'Plan' }, { name: 'Explore', task: 'List files' }])).toBe(false)
    expect(sameChildren(a, [{ name: 'Explore' }, { name: 'Plan' }])).toBe(false)
    expect(sameChildren(a, [{ name: 'Explore', task: 'List files' }])).toBe(false)
    expect(sameChildren([], [])).toBe(true)
  })
})
