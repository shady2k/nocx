// FilesClient over the REAL dispatcher over a mock socket — the
// composition-seam test (fm-w13): the panel's store is driven through
// createFilesPanelServices(dispatcher), NOT a faked services object, and
// the assertion is what lands on the wire. A fake seam cannot see the
// defect this exists for: a method added to the client that the
// composition root forgets to forward, or a call that never leaves the
// renderer — units green, production dead.
//
// The composition root's tracked wrapper (main.tsx) forwards by
// construction (spread + open/close overrides), so the frames below are
// exactly what the panel's production seam must emit.
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { Dispatcher } from '../dispatcher'
import { createFilesPanelServices } from './files-client'
import { createFilesTreeStore } from './files-store'
import type { ActiveOrigin } from '../pane-content'
import type { FilesListResult } from '../generated/files.list'

class MockSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3

  readyState: number = MockSocket.CONNECTING
  readonly url = 'ws://127.0.0.1:9876/session'
  readonly sent: string[] = []
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onmessage: ((ev: { data: unknown }) => void) | null = null
  onerror: (() => void) | null = null

  private _listeners = new Map<string, Set<(...args: unknown[]) => void>>()

  addEventListener(type: string, fn: (...args: unknown[]) => void): void {
    let s = this._listeners.get(type)
    if (!s) {
      s = new Set()
      this._listeners.set(type, s)
    }
    s.add(fn)
  }
  removeEventListener(type: string, fn: (...args: unknown[]) => void): void {
    this._listeners.get(type)?.delete(fn)
  }
  send(data: string): void {
    this.sent.push(data)
  }
  close(): void {
    this.readyState = MockSocket.CLOSED
    this.onclose?.()
    this._fire('close')
  }
  accept(): void {
    this.readyState = MockSocket.OPEN
    this.onopen?.()
  }
  deliver(msg: Record<string, unknown>): void {
    const data = JSON.stringify(msg)
    const event = { data }
    this.onmessage?.(event)
    this._fire('message', event)
  }
  private _fire(type: string, event?: unknown): void {
    for (const fn of this._listeners.get(type) ?? []) {
      fn(event)
    }
  }
}

const OriginalWebSocket = globalThis.WebSocket
let nextSocket: MockSocket | null = null

beforeEach(() => {
  nextSocket = null
  const fn = function (url: string) {
    const s = new MockSocket()
    Object.defineProperty(s, 'url', { value: url, writable: false })
    nextSocket = s
    return s as unknown as WebSocket
  } as unknown as { new (url: string): WebSocket; OPEN: number }
  fn.OPEN = OriginalWebSocket.OPEN
  globalThis.WebSocket = fn as unknown as typeof WebSocket
})

afterEach(() => {
  globalThis.WebSocket = OriginalWebSocket
})

function lastSocket(): MockSocket {
  if (!nextSocket) throw new Error('no WebSocket was constructed')
  return nextSocket
}

async function connect(d: Dispatcher, port = 9876): Promise<void> {
  const p = d.connect(port)
  lastSocket().accept()
  await p
}

/** Drain the microtask queue until the store's promise chains settle. */
async function settle(): Promise<void> {
  for (let i = 0; i < 8; i++) await Promise.resolve()
}

const LOCAL_A: ActiveOrigin = {
  paneId: 1,
  sessionId: 'session-a',
  kind: 'local',
  cwd: '/',
  cwdVerified: true,
  cwdFollow: true,
  host: null,
}

const OPEN_RESULT = {
  bindingId: 'b1',
  endpointId: null,
  revealAvailable: true,
  root: { path: '/', display: '/', inferred: false, inferredReason: '' },
}

const LIST_OK: FilesListResult = {
  state: 'ok',
  path: '/',
  canonical: 'C:/',
  entries: [],
  offset: 0,
  total: 0,
  hasMore: false,
  rev: 'r1',
}

/** Answer the most recent pending request frame for a method. The store
 *  sends files.list and files.watch back-to-back after files.open, so the
 *  latest frame is not necessarily the request being answered — the method
 *  is the address. */
function answer(socket: MockSocket, method: string, result: unknown): void {
  const sent = socket.sent.map((s) => JSON.parse(s) as { method?: string; id?: number })
  let id: number | undefined
  for (const f of sent) {
    if (f.method === method) id = f.id
  }
  socket.deliver({ jsonrpc: '2.0', id, result })
}

function frames(socket: MockSocket): Array<{ method?: string; params?: unknown }> {
  return socket.sent.map((s) => JSON.parse(s) as { method?: string; params?: unknown })
}

describe('files client over the real composition seam', () => {
  it('sends files.open, files.list and files.watch over the wire for a rescope', async () => {
    const dispatcher = new Dispatcher()
    await connect(dispatcher)
    const services = createFilesPanelServices(dispatcher)
    const store = createFilesTreeStore(services)
    const socket = lastSocket()

    store.rescope(LOCAL_A)
    await settle()
    answer(socket, 'files.open', OPEN_RESULT)
    await settle()
    answer(socket, 'files.list', LIST_OK)
    await settle()
    answer(socket, 'files.watch', { mode: 'watching' })
    await settle()

    const wire = frames(socket)
    expect(wire[0]?.method).toBe('files.open')
    expect(wire[0]?.params).toEqual({ sessionId: 'session-a', rootPath: '/' })
    expect(wire.some((f) => f.method === 'files.list')).toBe(true)
    // The watching half rides the same seam: the initial set is the root.
    const watch = wire.find((f) => f.method === 'files.watch')
    expect(watch?.params).toEqual({ bindingId: 'b1', paths: ['/'] })
    expect(store.watchMode()).toBe('watching')
    store.dispose()
  })

  it('delivers files.reveal with the binding and lexical path', async () => {
    const dispatcher = new Dispatcher()
    await connect(dispatcher)
    const services = createFilesPanelServices(dispatcher)
    const socket = lastSocket()

    const p = services.reveal('b1', '/home/alice/link')
    const reveal = frames(socket).find((f) => f.method === 'files.reveal')
    expect(reveal?.params).toEqual({ bindingId: 'b1', path: '/home/alice/link' })
    answer(socket, 'files.reveal', {}) // the reveal result
    await p
  })

  it('routes a files.changed notification from the wire into a re-list', async () => {
    const dispatcher = new Dispatcher()
    await connect(dispatcher)
    const services = createFilesPanelServices(dispatcher)
    const store = createFilesTreeStore(services)
    const socket = lastSocket()

    store.rescope(LOCAL_A)
    await settle()
    answer(socket, 'files.open', OPEN_RESULT)
    await settle()
    answer(socket, 'files.list', LIST_OK)
    await settle()
    answer(socket, 'files.watch', { mode: 'watching' })
    await settle()

    const listsBefore = frames(socket).filter((f) => f.method === 'files.list').length

    // The backend announces a dirty root; the panel re-lists it.
    socket.deliver({
      jsonrpc: '2.0',
      method: 'files.changed',
      params: { bindingId: 'b1', path: '/' },
    })
    await settle()
    answer(socket, 'files.list', LIST_OK)
    await settle()

    const listsAfter = frames(socket).filter((f) => f.method === 'files.list').length
    expect(listsAfter).toBe(listsBefore + 1)
    store.dispose()
  })
})
