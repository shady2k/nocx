// window-geometry — the renderer's relay for the window half of the
// UI-state document (ADR-0098).
//
// Three behaviours and nothing else: the saved geometry reaches the
// restore binding, the samples the shell emits reach the coordinator as
// notifications, and a client with no native host performs neither leg
// while still mounting.
import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { mountWindowGeometry, WINDOW_SAMPLED_EVENT } from './window-geometry'
import { Dispatcher } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import type { UIStateWindow } from './generated/uistate.window'

/** A dispatcher double: records every call and notify, answers calls
 *  from a queue. It is CONNECTED, so a relay mounted over it reads at
 *  once — the disconnected order has its own test against the real
 *  Dispatcher below. */
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
  const dispatcher = {
    call,
    notify,
    connected: true,
    onConnect: () => () => undefined,
  } as unknown as Dispatcher
  return { dispatcher, calls, notifications }
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

// ── The startup seam ──────────────────────────────────────────────────
// main.tsx mounts this relay at the composition root, above
// `dispatcher.start()`. Dispatcher.call REJECTS any call made while the
// socket does not exist ("not connected"), so a relay that reads at mount
// spends its one read on a moment the wire cannot answer: the rejection is
// swallowed, the binding that starts the shell's sampler is never called,
// and neither a restore nor a sample ever happens. These tests drive the
// REAL Dispatcher through that order rather than a double that is already
// connected, because the order is the defect.

/** The smallest socket the dispatcher's handshake needs: it answers the
 *  credit advertisement, records frames, and lets a test deliver an answer. */
class StartupSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3
  static last: StartupSocket | null = null
  readyState = 0
  binaryType = ''
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  onmessage: ((event: { data: unknown }) => void) | null = null
  onclose: (() => void) | null = null
  private readonly sent: string[] = []
  private readonly listeners = new Map<string, Set<(event: unknown) => void>>()

  constructor(readonly url: string) {
    StartupSocket.last = this
  }

  addEventListener(type: string, listener: (event: unknown) => void): void {
    let set = this.listeners.get(type)
    if (set === undefined) {
      set = new Set()
      this.listeners.set(type, set)
    }
    set.add(listener)
  }

  removeEventListener(type: string, listener: (event: unknown) => void): void {
    this.listeners.get(type)?.delete(listener)
  }

  send(data: string | ArrayBuffer): void {
    if (typeof data !== 'string') return
    this.sent.push(data)
    const frame = JSON.parse(data) as { id?: number; method?: string }
    if (frame.method === 'transport.credit' && frame.id !== undefined) {
      const id = frame.id
      queueMicrotask(() => {
        this.deliver({
          jsonrpc: '2.0',
          id,
          result: { window: 8, exempt: [], reserved: [] },
        })
      })
    }
  }

  accept(): void {
    this.readyState = StartupSocket.OPEN
    this.onopen?.()
  }

  close(): void {
    this.readyState = StartupSocket.CLOSED
    this.onclose?.()
    for (const listener of this.listeners.get('close') ?? []) listener(undefined)
  }

  deliver(payload: unknown): void {
    const event = { data: JSON.stringify(payload) }
    this.onmessage?.(event)
    for (const listener of this.listeners.get('message') ?? []) listener(event)
  }

  frames(): { id?: number; method?: string }[] {
    return this.sent.map((frame) => JSON.parse(frame) as { id?: number; method?: string })
  }
}

describe('mountWindowGeometry at the composition root', () => {
  afterEach(() => {
    StartupSocket.last = null
    vi.unstubAllGlobals()
  })

  it('reads the saved geometry once the dispatcher connects, not at mount', async () => {
    vi.stubGlobal('WebSocket', StartupSocket)
    const dispatcher = new Dispatcher(fixedEndpoint(9876))
    const restoreWindow = vi.fn(() => Promise.resolve())
    const { events } = fakeEvents()

    const unmount = mountWindowGeometry(dispatcher, { restoreWindow }, events)
    // Mounted before start(): there is no socket, so there is nothing the
    // read could answer, and the relay must not spend it here.
    await Promise.resolve()
    expect(StartupSocket.last).toBeNull()
    expect(restoreWindow).not.toHaveBeenCalled()

    dispatcher.start()
    await vi.waitFor(() => expect(StartupSocket.last).not.toBeNull())
    const socket = StartupSocket.last as StartupSocket
    socket.accept()

    const asked = await vi.waitFor(() => {
      const frame = socket.frames().find((f) => f.method === 'uistate.window.get')
      if (frame === undefined) {
        throw new Error('uistate.window.get was never asked after the socket opened')
      }
      return frame
    })
    socket.deliver({ jsonrpc: '2.0', id: asked.id, result: SAVED })

    await vi.waitFor(() => expect(restoreWindow).toHaveBeenCalledWith(SAVED))
    unmount()
    dispatcher.close()
  })

  it('carries a resize through one window and back into the next start', async () => {
    vi.stubGlobal('WebSocket', StartupSocket)
    const NEVER: UIStateWindow = {
      width: 0,
      height: 0,
      x: 0,
      y: 0,
      maximised: false,
      fullScreen: false,
      displays: '',
    }
    const MOVED: UIStateWindow = {
      width: 1512,
      height: 944,
      x: 88,
      y: 40,
      maximised: false,
      fullScreen: false,
      displays: '1:2560x1440p',
    }

    // First start: nothing saved, so the shell applies nothing and starts
    // sampling — the first launch that will begin to record.
    const first = await connectWithDocument(NEVER)
    expect(first.restoreWindow).toHaveBeenCalledWith(NEVER)

    // The person resizes and moves; the shell emits the change and the
    // renderer relays it to the coordinator.
    first.emit(WINDOW_SAMPLED_EVENT, { data: MOVED })
    expect(first.notifications()).toEqual([{ method: 'uistate.window.observe', params: MOVED }])

    // Quit and start again: the coordinator now answers with what was
    // written to it, and the shell is handed that same size and place.
    first.unmount()
    first.dispatcher.close()
    StartupSocket.last = null

    const second = await connectWithDocument(MOVED)
    expect(second.restoreWindow).toHaveBeenCalledWith(MOVED)
    second.unmount()
    second.dispatcher.close()
  })
})

/** Mount the relay the way main.tsx does — before `dispatcher.start()` —
 *  then connect and answer `uistate.window.get` with `saved`. */
async function connectWithDocument(saved: UIStateWindow): Promise<{
  dispatcher: Dispatcher
  restoreWindow: ReturnType<typeof vi.fn>
  emit: (name: string, data: unknown) => void
  notifications: () => { method: string; params: unknown }[]
  unmount: () => void
}> {
  const dispatcher = new Dispatcher(fixedEndpoint(9876))
  const restoreWindow = vi.fn(() => Promise.resolve())
  const { events, handlers } = fakeEvents()
  const notifications: { method: string; params: unknown }[] = []
  const unmount = mountWindowGeometry(dispatcher, { restoreWindow }, events)
  // Every sample goes to the coordinator through the real dispatcher.
  // Record those calls at the dispatcher itself, so the test observes the
  // leg the app uses rather than a double's.
  const originalNotify = dispatcher.notify.bind(dispatcher)
  dispatcher.notify = (method: string, params: unknown): void => {
    notifications.push({ method, params })
    originalNotify(method, params)
  }

  dispatcher.start()
  await vi.waitFor(() => expect(StartupSocket.last).not.toBeNull())
  const socket = StartupSocket.last as StartupSocket
  socket.accept()
  const asked = await vi.waitFor(() => {
    const frame = socket.frames().find((f) => f.method === 'uistate.window.get')
    if (frame === undefined) {
      throw new Error('uistate.window.get was never asked after the socket opened')
    }
    return frame
  })
  socket.deliver({ jsonrpc: '2.0', id: asked.id, result: saved })
  await vi.waitFor(() => expect(restoreWindow).toHaveBeenCalled())

  return {
    dispatcher,
    restoreWindow,
    emit: (name, data) => handlers.get(name)?.(data),
    notifications: () => notifications,
    unmount,
  }
}
