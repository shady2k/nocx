// window-geometry — the renderer's relay for the window half of the
// UI-state document (ADR-0098).
//
// Three behaviours and nothing else: the saved geometry reaches the
// restore binding, the samples the shell emits reach the coordinator as
// notifications, and a client with no native host performs neither leg
// while still mounting.
import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { mountWindowGeometry, WINDOW_SAMPLED_EVENT } from './window-geometry'
import type { Dispatcher } from './dispatcher'
import type { UIStateWindow } from './generated/uistate.window'

/** A dispatcher double: records every call and notify, answers calls
 *  from a queue. */
function fakeDispatcher(answers: unknown[]): {
  dispatcher: Dispatcher
  calls: { method: string; params: unknown }[]
  notifications: { method: string; params: unknown }[]
} {
  const calls: { method: string; params: unknown }[] = []
  const notifications: { method: string; params: unknown }[] = []
  const call = vi.fn((method: string, params: unknown) => {
    calls.push({ method, params })
    const next = answers.shift()
    if (next instanceof Error) return Promise.reject(next)
    return Promise.resolve(next)
  })
  const notify = vi.fn((method: string, params: unknown) => {
    notifications.push({ method, params })
  })
  return { dispatcher: { call, notify } as unknown as Dispatcher, calls, notifications }
}

/** An event seam double: captures the handler so a test can emit. */
function fakeEvents(): {
  events: { on: (name: string, handler: (data: unknown) => void) => () => void }
  handlers: Map<string, (data: unknown) => void>
} {
  const handlers = new Map<string, (data: unknown) => void>()
  return {
    events: {
      on: (name, handler) => {
        handlers.set(name, handler)
        return () => handlers.delete(name)
      },
    },
    handlers,
  }
}

const SAVED: UIStateWindow = {
  width: 1440,
  height: 900,
  x: 120,
  y: 64,
  maximised: false,
  fullScreen: false,
  displays: '1:2560x1440p',
}

// The reachability probe answers true inside a Wails webview; a test
// cannot have one, so the module's own bindingReachable seam is mocked
// per test. jsdom has no webview, so the default there is false.
vi.mock('./wails-runtime', () => ({
  bindingReachable: vi.fn(() => true),
}))
import { bindingReachable } from './wails-runtime'
const reachable = vi.mocked(bindingReachable)

describe('mountWindowGeometry', () => {
  beforeEach(() => {
    reachable.mockReturnValue(true)
  })
  afterEach(() => {
    vi.clearAllMocks()
  })

  it('reads the saved geometry and hands it to the shell binding', async () => {
    const { dispatcher, calls } = fakeDispatcher([SAVED])
    const restoreWindow = vi.fn(() => Promise.resolve())
    const { events } = fakeEvents()

    mountWindowGeometry(dispatcher, { restoreWindow }, events)
    // The restore call is awaited inside the mount; let the microtasks
    // settle before asserting on it.
    await vi.waitFor(() => expect(restoreWindow).toHaveBeenCalled())

    expect(calls[0]?.method).toBe('uistate.window.get')
    expect(restoreWindow).toHaveBeenCalledWith(SAVED)
  })

  it('still calls the binding when nothing was ever saved', async () => {
    // The binding is what starts the shell's sampler, so a first launch
    // must reach it too — with the zero geometry, which the shell reads
    // as "nothing to apply, everything to record".
    const never: UIStateWindow = {
      width: 0,
      height: 0,
      x: 0,
      y: 0,
      maximised: false,
      fullScreen: false,
      displays: '',
    }
    const { dispatcher } = fakeDispatcher([never])
    const restoreWindow = vi.fn(() => Promise.resolve())
    const { events } = fakeEvents()

    mountWindowGeometry(dispatcher, { restoreWindow }, events)
    await vi.waitFor(() => expect(restoreWindow).toHaveBeenCalled())

    expect(restoreWindow).toHaveBeenCalledWith(never)
  })

  it('relays each sampled change to the coordinator as a notification', () => {
    const { dispatcher, notifications } = fakeDispatcher([])
    const restoreWindow = vi.fn(() => Promise.resolve())
    const { events, handlers } = fakeEvents()

    mountWindowGeometry(dispatcher, { restoreWindow }, events)

    handlers.get(WINDOW_SAMPLED_EVENT)?.({
      data: { ...SAVED, width: 1920, height: 1200, maximised: true },
    })
    expect(notifications).toEqual([
      {
        method: 'uistate.window.observe',
        params: { ...SAVED, width: 1920, height: 1200, maximised: true },
      },
    ])
  })

  it('unwraps a one-element array payload, as v3 may deliver it', () => {
    const { dispatcher, notifications } = fakeDispatcher([])
    const { events, handlers } = fakeEvents()

    mountWindowGeometry(dispatcher, { restoreWindow: () => Promise.resolve() }, events)

    handlers.get(WINDOW_SAMPLED_EVENT)?.({ data: [SAVED] })
    expect(notifications).toHaveLength(1)
    expect(notifications[0]?.params).toEqual(SAVED)
  })

  it('ignores a payload that does not name a whole sample', () => {
    const { dispatcher, notifications } = fakeDispatcher([])
    const { events, handlers } = fakeEvents()

    mountWindowGeometry(dispatcher, { restoreWindow: () => Promise.resolve() }, events)

    handlers.get(WINDOW_SAMPLED_EVENT)?.({ data: 'resizing' })
    handlers.get(WINDOW_SAMPLED_EVENT)?.({ data: { width: 0, height: 0 } })
    handlers.get(WINDOW_SAMPLED_EVENT)?.(null)
    expect(notifications).toHaveLength(0)
  })

  it('a client with no native host restores nothing', async () => {
    reachable.mockReturnValue(false)
    const { dispatcher, calls } = fakeDispatcher([SAVED])
    const restoreWindow = vi.fn(() => Promise.resolve())
    const { events } = fakeEvents()

    mountWindowGeometry(dispatcher, { restoreWindow }, events)
    await vi.waitFor(() => expect(calls).toHaveLength(1))
    // The read happened (the coordinator answered); the binding was never
    // called. The shell's sampler starts only from that binding, so no
    // sample can follow — there is no relay leg to exercise in a client
    // whose shell never speaks.
    expect(restoreWindow).not.toHaveBeenCalled()
  })

  it('an unreachable backend costs the geometry, never the mount', async () => {
    const { dispatcher, calls } = fakeDispatcher([new Error('not connected')])
    const restoreWindow = vi.fn(() => Promise.resolve())
    const { events } = fakeEvents()

    const unmount = mountWindowGeometry(dispatcher, { restoreWindow }, events)
    // Waited on the state change (the read happened) rather than a duration.
    await vi.waitFor(() => expect(calls).toHaveLength(1))
    expect(restoreWindow).not.toHaveBeenCalled()
    expect(() => unmount()).not.toThrow()
  })
})
