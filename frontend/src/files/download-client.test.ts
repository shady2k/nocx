// DownloadClient over the REAL dispatcher over a mock socket — the
// composition-seam test, the shape `files-client.test.ts` uses and for its
// reason: a fake seam cannot see a method the composition root forgets to
// forward, or a call that never leaves the renderer. Units green,
// production dead.
//
// What this asserts that the fake cannot: the exact frames on the wire, and
// the URL resolution, which is the one piece of arithmetic in the client
// and the one that is wrong in exactly the environment nobody tests in.
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { Dispatcher } from '../dispatcher'
import { createDownloadServices } from './download-client'
import type { FilesDownloadDone } from '../generated/files.downloadDone'
import type { FilesDownloadProgress } from '../generated/files.downloadProgress'

class MockSocket {
  static readonly OPEN = 1
  readyState = 0
  readonly url = 'ws://127.0.0.1:9876/session'
  readonly sent: string[] = []
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onmessage: ((ev: { data: unknown }) => void) | null = null
  onerror: (() => void) | null = null
  private listeners = new Map<string, Set<(...a: unknown[]) => void>>()
  addEventListener(type: string, fn: (...a: unknown[]) => void): void {
    const s = this.listeners.get(type) ?? new Set()
    s.add(fn)
    this.listeners.set(type, s)
  }
  removeEventListener(type: string, fn: (...a: unknown[]) => void): void {
    this.listeners.get(type)?.delete(fn)
  }
  send(data: string): void {
    this.sent.push(data)
  }
  close(): void {
    this.readyState = 3
    this.onclose?.()
  }
  accept(): void {
    this.readyState = MockSocket.OPEN
    this.onopen?.()
  }
  deliver(msg: Record<string, unknown>): void {
    const event = { data: JSON.stringify(msg) }
    this.onmessage?.(event)
    for (const fn of this.listeners.get('message') ?? []) fn(event)
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

function socket(): MockSocket {
  if (nextSocket === null) throw new Error('no WebSocket was constructed')
  return nextSocket
}

async function connected(): Promise<{ d: Dispatcher; s: MockSocket }> {
  const d = new Dispatcher()
  const p = d.connect(9876)
  socket().accept()
  await p
  return { d, s: socket() }
}

const frames = (s: MockSocket) =>
  s.sent.map((f) => JSON.parse(f) as { method?: string; params?: Record<string, unknown> })

describe('what reaches the wire', () => {
  it('files.download names the binding and the path, and nothing else', async () => {
    const { d, s } = await connected()
    void createDownloadServices(d).download({ bindingId: 'b1', path: '/srv/big.iso' })
    const frame = frames(s).find((f) => f.method === 'files.download')
    expect(frame?.params).toEqual({ bindingId: 'b1', path: '/srv/big.iso' })
  })

  it('files.downloadCancel takes the transfer id', async () => {
    const { d, s } = await connected()
    void createDownloadServices(d).cancel('0'.repeat(32))
    const frame = frames(s).find((f) => f.method === 'files.downloadCancel')
    expect(frame?.params).toEqual({ transferId: '0'.repeat(32) })
  })
})

describe('resolving the fetch URL', () => {
  it('resolves the result`s PATH against the SOCKET`s origin, not the document`s', async () => {
    // Under `dev-web` vite serves the page and the backend is on another
    // port. A URL resolved against the document would fetch the bytes from
    // the wrong server — which in that environment is every fetch.
    const { d } = await connected()
    expect(createDownloadServices(d).resolveUrl('/download/abc')).toBe(
      'http://127.0.0.1:9876/download/abc',
    )
  })

  it('answers null with no connection, rather than guessing an origin', async () => {
    const { d } = await connected()
    d.close()
    expect(createDownloadServices(d).resolveUrl('/download/abc')).toBeNull()
  })
})

describe('the notifications', () => {
  it('delivers a well-formed progress frame', async () => {
    const { d, s } = await connected()
    const seen: FilesDownloadProgress[] = []
    createDownloadServices(d).subscribeProgress((p) => seen.push(p))
    s.deliver({
      jsonrpc: '2.0',
      method: 'files.downloadProgress',
      params: { transferId: 't1', bytes: 10, total: 100 },
    })
    expect(seen).toEqual([{ transferId: 't1', bytes: 10, total: 100 }])
  })

  it('delivers a well-formed done frame', async () => {
    const { d, s } = await connected()
    const seen: FilesDownloadDone[] = []
    createDownloadServices(d).subscribeDone((p) => seen.push(p))
    s.deliver({
      jsonrpc: '2.0',
      method: 'files.downloadDone',
      params: { transferId: 't1', outcome: 'sent', name: 'a', bytes: 1, total: 1 },
    })
    expect(seen).toHaveLength(1)
    expect(seen[0].outcome).toBe('sent')
  })

  it('drops a malformed frame instead of handing the store a broken record', async () => {
    // The store's rules turn on these fields being what they say. A frame
    // missing `total` would set a size of undefined, and every progress bar
    // downstream would divide by it.
    const { d, s } = await connected()
    const progress: unknown[] = []
    const done: unknown[] = []
    const svc = createDownloadServices(d)
    svc.subscribeProgress((p) => progress.push(p))
    svc.subscribeDone((p) => done.push(p))
    s.deliver({
      jsonrpc: '2.0',
      method: 'files.downloadProgress',
      params: { transferId: 't1', bytes: 10 },
    })
    s.deliver({
      jsonrpc: '2.0',
      method: 'files.downloadDone',
      params: { transferId: 't1', outcome: 'sent' },
    })
    s.deliver({ jsonrpc: '2.0', method: 'files.downloadProgress', params: null })
    expect(progress).toEqual([])
    expect(done).toEqual([])
  })

  it('unsubscribing stops the delivery', async () => {
    const { d, s } = await connected()
    const seen: unknown[] = []
    const off = createDownloadServices(d).subscribeProgress((p) => seen.push(p))
    off()
    s.deliver({
      jsonrpc: '2.0',
      method: 'files.downloadProgress',
      params: { transferId: 't1', bytes: 10, total: 100 },
    })
    expect(seen).toEqual([])
  })
})
