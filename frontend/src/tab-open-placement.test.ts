// @vitest-environment jsdom
import { describe, expect, it, vi, beforeEach } from 'vitest'
import {
  makeLayoutStore,
  mountPaneManager,
  resetSessionCounter,
} from './test-support/panes-fixtures'

// THE SEAT OF A CREATE ASKED FOR BEFORE THE PREVIOUS ONE WAS ANSWERED
// (nocx-ouru5.1.3).
//
// A tab's row reaches the renderer one round trip after the tab is minted, and
// the seat of the next create is "immediately after the tab in front". Resolved
// through the chain's cache, that seat does not exist yet inside the window —
// so a second Cmd+T before the first answer landed named no anchor and the tab
// went to the head of the strip. The renderer knows what it asked for, so the
// anchor is answered from what it minted.
//
// What is asserted is what the renderer ASKED WITH (`afterTabId`), not where
// the strip then drew the row: the positions are content.createTab's, which
// renumbers the strip server-side and which the renderer never computes. The
// e2e (e2e/tab-open-placement.spec.ts) watches the drawing itself.

describe('the seat of a create whose predecessor is still unanswered (nocx-ouru5.1.3)', () => {
  beforeEach(() => {
    resetSessionCounter()
    vi.clearAllMocks()
    document.body.innerHTML = ''
  })

  it('names the tab it minted a moment ago, and the chain from then on', async () => {
    const layout = makeLayoutStore()
    const ask = vi.spyOn(layout.backend, 'createTab')
    const { manager, bar } = await mountPaneManager(
      undefined,
      undefined,
      undefined,
      undefined,
      layout,
    )
    await vi.waitFor(() => expect(bar.querySelectorAll('.nocx-tab')).toHaveLength(1))
    const first = ask.mock.calls[0][0]
    expect(ask).toHaveBeenCalledTimes(1)

    // Two creates inside one round trip: the second is asked for while the
    // first one's row is nowhere in the cache yet.
    const holding = layout.backend.holdCreates()
    manager.newPane()
    await vi.waitFor(() => expect(ask).toHaveBeenCalledTimes(2))
    manager.newPane()
    await vi.waitFor(() => expect(ask).toHaveBeenCalledTimes(3))

    const [second, third] = [ask.mock.calls[1][0], ask.mock.calls[2][0]]
    expect(second.afterTabId, 'the first create is seated beside the tab in front').toBe(first.id)
    expect(
      third.afterTabId,
      'the second create is seated beside the tab the renderer just asked for',
    ).toBe(second.id)
    expect(third.workspaceId).toBe(second.workspaceId)

    // …and once the chain has answered, the chain is what is asked.
    holding.release()
    await vi.waitFor(() => expect(bar.querySelectorAll('.nocx-tab')).toHaveLength(3))
    manager.newPane()
    await vi.waitFor(() => expect(ask).toHaveBeenCalledTimes(4))
    expect(ask.mock.calls[3][0].afterTabId, "the chain's answer outranks the note").toBe(third.id)
  })
})
