// uistate-client — the renderer's end of the UI-state document (ADR-0048).
//
// Two things are worth testing here and nothing else is: that a caller who
// knows only one field does not wipe the others, and that the mirror learns
// what the backend actually stored rather than what it was sent.
import { describe, expect, it, vi } from 'vitest'
import { UIStateClient } from './uistate-client'
import type { Dispatcher } from './dispatcher'

/** A dispatcher double: records every call and answers from a queue. */
function fakeDispatcher(answers: unknown[]): {
  dispatcher: Dispatcher
  calls: { method: string; params: unknown }[]
} {
  const calls: { method: string; params: unknown }[] = []
  const call = vi.fn((method: string, params: unknown) => {
    calls.push({ method, params })
    const next = answers.shift()
    if (next instanceof Error) return Promise.reject(next)
    return Promise.resolve(next)
  })
  return { dispatcher: { call } as unknown as Dispatcher, calls }
}

const STORED = {
  sidebar: { collapsed: true, activeViewId: 'ports', width: 320 },
  activeTab: 'pane-7',
}

describe('UIStateClient', () => {
  it('loads the document and exposes it synchronously afterwards', async () => {
    const { dispatcher, calls } = fakeDispatcher([STORED])
    const client = new UIStateClient(dispatcher)

    const loaded = await client.load()

    expect(calls[0]?.method).toBe('uistate.get')
    expect(loaded).toEqual(STORED)
    // Synchronous, because the composition root hands it to components that
    // mount before any further round trip.
    expect(client.state.sidebar.width).toBe(320)
  })

  it('falls back to working defaults when the document cannot be read', async () => {
    // An unreachable backend costs the user their layout, never their launch:
    // load() resolves with defaults rather than rejecting into bootstrap.
    const { dispatcher } = fakeDispatcher([new Error('not connected')])
    const client = new UIStateClient(dispatcher)

    const loaded = await client.load()

    expect(loaded.sidebar.width).toBe(240)
    expect(loaded.sidebar.collapsed).toBe(false)
    expect(loaded.activeTab).toBe('')
  })

  it('a caller that knows one field does not wipe the others', async () => {
    // The drag knows the width and nothing else. uistate.set takes the whole
    // value, so the merge has to happen somewhere — here, against the last
    // state the backend confirmed.
    const { dispatcher, calls } = fakeDispatcher([STORED, STORED])
    const client = new UIStateClient(dispatcher)
    await client.load()

    await client.save({ sidebar: { width: 400 } })

    expect(calls[1]?.method).toBe('uistate.set')
    expect(calls[1]?.params).toEqual({
      sidebar: { collapsed: true, activeViewId: 'ports', width: 400 },
      activeTab: 'pane-7',
    })
  })

  it('the mirror takes the STORED value, not the sent one', async () => {
    // The backend clamps the width. A client that kept what it sent would
    // hold a number nobody will ever read back — the exact defect the
    // contracts directory exists to prevent.
    const clamped = { sidebar: { collapsed: false, activeViewId: '', width: 640 }, activeTab: '' }
    const { dispatcher } = fakeDispatcher([clamped])
    const client = new UIStateClient(dispatcher)

    const stored = await client.save({ sidebar: { width: 99999 } })

    expect(stored.sidebar.width).toBe(640)
    expect(client.state.sidebar.width).toBe(640)
  })

  it('a failed write rejects, so a caller that wants to warn can', async () => {
    const { dispatcher } = fakeDispatcher([new Error('refused')])
    const client = new UIStateClient(dispatcher)

    await expect(client.save({ activeTab: 'pane-1' })).rejects.toThrow('refused')
    // And the mirror is unchanged: nothing was stored, so nothing is claimed.
    expect(client.state.activeTab).toBe('')
  })
})
