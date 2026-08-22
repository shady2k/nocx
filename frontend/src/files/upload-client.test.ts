// UploadClient over the REAL dispatcher over a mock socket — the
// composition-seam test the Files panel's client already has (fm-w13): the
// assertion is what lands on the wire, not what a faked services object
// remembers being asked. A fake seam cannot see the defect this exists for:
// a method the composition root forgets to forward, or a call that never
// leaves the renderer.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Dispatcher } from '../dispatcher'
import { createUploadServices, type FetchLike, type UploadDecision } from './upload-client'
import type { FilesUploadDone } from '../generated/files.uploadDone'
import type { FilesUploadProgress } from '../generated/files.uploadProgress'
import type { FilesDropped } from '../generated/files.dropped'

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
    const event = { data: JSON.stringify(msg) }
    this.onmessage?.(event)
    this._fire('message', event)
  }
  private _fire(type: string, event?: unknown): void {
    for (const fn of this._listeners.get(type) ?? []) fn(event)
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

async function connect(d: Dispatcher): Promise<void> {
  const p = d.connect(9876)
  lastSocket().accept()
  await p
}

/** The frames the renderer put on the wire, parsed. */
function frames(): Array<{ method: string; params: Record<string, unknown>; id?: number }> {
  return lastSocket().sent.map((s) => JSON.parse(s) as never)
}

function frameFor(method: string): {
  method: string
  params: Record<string, unknown>
  id?: number
} {
  const f = frames().find((f) => f.method === method)
  if (f === undefined) throw new Error(`no ${method} frame was sent (${lastSocket().sent.length})`)
  return f
}

/** A fetch that records what it was asked and answers what the test says. */
function fakeFetch(answer: () => Promise<Response>): {
  fetch: FetchLike
  calls: Array<{ url: string; init: RequestInit }>
} {
  const calls: Array<{ url: string; init: RequestInit }> = []
  return {
    calls,
    fetch: (url, init) => {
      calls.push({ url, init })
      return answer()
    },
  }
}

const OK = () => Promise.resolve({ ok: true, status: 200 } as Response)

describe('the upload control plane on the wire', () => {
  it('names the destination and never a source path', async () => {
    const d = new Dispatcher()
    await connect(d)
    const services = createUploadServices(d, OK)
    void services.upload({ bindingId: 'b1', destDir: '/srv/data', name: 'notes.txt', size: 12 })
    const f = frameFor('files.upload')
    expect(f.params).toEqual({
      bindingId: 'b1',
      destDir: '/srv/data',
      name: 'notes.txt',
      size: 12,
    })
    // The KEY, not the value: "a request with a sourceTicket is a path
    // upload" keys on presence, and a `sourceTicket: undefined` would
    // marshal to a key the backend's strict decoder refuses.
    expect('sourceTicket' in f.params).toBe(false)
    expect('onExists' in f.params).toBe(false)
  })

  it('echoes a source ticket and the collision decision when it has them', async () => {
    const d = new Dispatcher()
    await connect(d)
    const services = createUploadServices(d, OK)
    void services.upload({
      bindingId: 'b1',
      destDir: '/srv/data',
      name: 'notes.txt',
      size: 12,
      sourceTicket: 'a'.repeat(32),
      onExists: 'keepBoth',
    })
    expect(frameFor('files.upload').params).toEqual({
      bindingId: 'b1',
      destDir: '/srv/data',
      name: 'notes.txt',
      size: 12,
      sourceTicket: 'a'.repeat(32),
      onExists: 'keepBoth',
    })
  })

  it('carries every decision the sink accepts, in the wire vocabulary', async () => {
    // The closed set, echoed rather than mapped: the kit's collision dialog
    // answers in these three words and so does the backend, so a mapping
    // table between two spellings of one vocabulary would be a second owner
    // of it — and the two would agree until somebody added a fourth.
    const decisions: UploadDecision[] = ['overwrite', 'keepBoth', 'skip']
    for (const onExists of decisions) {
      const d = new Dispatcher()
      await connect(d)
      void createUploadServices(d, OK).upload({
        bindingId: 'b1',
        destDir: '/srv',
        name: 'n',
        size: 1,
        onExists,
      })
      expect(frameFor('files.upload').params.onExists).toBe(onExists)
    }
  })

  it('cancels by transfer id', async () => {
    const d = new Dispatcher()
    await connect(d)
    void createUploadServices(d, OK).cancel('t1')
    expect(frameFor('files.uploadCancel').params).toEqual({ transferId: 't1' })
  })

  it('asks for a source ticket, never a path', async () => {
    const d = new Dispatcher()
    await connect(d)
    void createUploadServices(d, OK).pickSource()
    expect(frameFor('dialog.openFileForUpload').params).toEqual({})
  })

  it('delivers progress, done and dropped notifications off the socket', async () => {
    const d = new Dispatcher()
    await connect(d)
    const services = createUploadServices(d, OK)
    const progress: FilesUploadProgress[] = []
    const done: FilesUploadDone[] = []
    const dropped: FilesDropped[] = []
    services.subscribeProgress((p) => progress.push(p))
    services.subscribeDone((p) => done.push(p))
    services.subscribeDropped((p) => dropped.push(p))

    lastSocket().deliver({
      jsonrpc: '2.0',
      method: 'files.uploadProgress',
      params: { transferId: 't1', bytes: 5, total: 10 },
    })
    lastSocket().deliver({
      jsonrpc: '2.0',
      method: 'files.uploadDone',
      params: { transferId: 't1', outcome: 'written', finalName: 'notes.txt', stranded: [] },
    })
    lastSocket().deliver({
      jsonrpc: '2.0',
      method: 'files.dropped',
      params: { sessionId: 's1', sources: [{ sourceTicket: 'x', name: 'a.txt', size: 3 }] },
    })

    expect(progress).toEqual([{ transferId: 't1', bytes: 5, total: 10 }])
    expect(done[0].outcome).toBe('written')
    expect(dropped[0].sources[0].name).toBe('a.txt')
  })

  it('drops a notification that is not the shape the contract declares', async () => {
    const d = new Dispatcher()
    await connect(d)
    const services = createUploadServices(d, OK)
    const done: FilesUploadDone[] = []
    services.subscribeDone((p) => done.push(p))
    // `stranded` is always an array and never null; a frame without it is
    // not a terminal account and must not be read as one.
    lastSocket().deliver({
      jsonrpc: '2.0',
      method: 'files.uploadDone',
      params: { transferId: 't1', outcome: 'written', finalName: 'notes.txt' },
    })
    expect(done).toEqual([])
  })

  it('unsubscribes', async () => {
    const d = new Dispatcher()
    await connect(d)
    const services = createUploadServices(d, OK)
    const seen: FilesUploadProgress[] = []
    const off = services.subscribeProgress((p) => seen.push(p))
    off()
    lastSocket().deliver({
      jsonrpc: '2.0',
      method: 'files.uploadProgress',
      params: { transferId: 't1', bytes: 5, total: 10 },
    })
    expect(seen).toEqual([])
  })
})

describe('sendBody — the one request that is not JSON-RPC', () => {
  // The previous version of this asserted the client SETS Content-Length.
  // It was modelling behaviour that exists in no browser: Content-Length is
  // a forbidden header, so the attempt was silently dropped and the length
  // that reached the server was always the browser's own. The requirement
  // the sink enforces is met by the size check further down — "refuses a
  // body that is not the declared size, and sends nothing", which is what
  // makes the length the browser computes the declared one — and never by
  // a header this side can write.
  it('sends the blob and sets no header a browser would refuse to send', async () => {
    const d = new Dispatcher()
    await connect(d)
    const f = fakeFetch(OK)
    const body = new Blob([new Uint8Array(12)])
    const out = await createUploadServices(d, f.fetch).sendBody('/upload/tk', body, 12)
    expect(out).toEqual({ ok: true })
    expect(f.calls).toHaveLength(1)
    expect(f.calls[0].init.method).toBe('POST')
    expect(f.calls[0].init.body).toBe(body)
    const headers = (f.calls[0].init.headers ?? {}) as Record<string, string>
    expect(Object.keys(headers)).toEqual([])
  })

  it('posts to the backend the socket is on, not to the page origin', async () => {
    const d = new Dispatcher()
    await connect(d)
    const f = fakeFetch(OK)
    await createUploadServices(d, f.fetch).sendBody('/upload/tk', new Blob([new Uint8Array(3)]), 3)
    expect(f.calls[0].url).toBe('http://127.0.0.1:9876/upload/tk')
  })

  it('surfaces the STATUS, so 409 and 410 are two different answers', async () => {
    const d = new Dispatcher()
    await connect(d)
    const conflict = createUploadServices(d, () =>
      Promise.resolve({ ok: false, status: 409 } as Response),
    )
    const gone = createUploadServices(d, () =>
      Promise.resolve({ ok: false, status: 410 } as Response),
    )
    const body = new Blob([new Uint8Array(3)])
    await expect(conflict.sendBody('/upload/tk', body, 3)).resolves.toEqual({
      ok: false,
      kind: 'status',
      status: 409,
    })
    await expect(gone.sendBody('/upload/tk', body, 3)).resolves.toEqual({
      ok: false,
      kind: 'status',
      status: 410,
    })
  })

  it('reports a network failure as its own kind — the request got no answer at all', async () => {
    const d = new Dispatcher()
    await connect(d)
    const services = createUploadServices(d, () => Promise.reject(new Error('connection reset')))
    const out = await services.sendBody('/upload/tk', new Blob([new Uint8Array(3)]), 3)
    expect(out).toEqual({ ok: false, kind: 'network', message: 'connection reset' })
  })

  it('refuses a body that is not the declared size, and sends nothing', async () => {
    const d = new Dispatcher()
    await connect(d)
    const f = fakeFetch(OK)
    const out = await createUploadServices(d, f.fetch).sendBody(
      '/upload/tk',
      new Blob([new Uint8Array(7)]),
      12,
    )
    expect(out).toEqual({ ok: false, kind: 'size', declared: 12, actual: 7 })
    expect(f.calls).toEqual([])
  })

  it('reports no connection rather than guessing where the backend is', async () => {
    const d = new Dispatcher()
    const f = fakeFetch(OK)
    const out = await createUploadServices(d, f.fetch).sendBody(
      '/upload/tk',
      new Blob([new Uint8Array(3)]),
      3,
    )
    expect(out).toEqual({ ok: false, kind: 'network', message: 'no connection to the backend' })
    expect(f.calls).toEqual([])
  })

  it('passes the abort signal through, so a cancel stops the body', async () => {
    const d = new Dispatcher()
    await connect(d)
    const f = fakeFetch(OK)
    const ctrl = new AbortController()
    await createUploadServices(d, f.fetch).sendBody(
      '/upload/tk',
      new Blob([new Uint8Array(3)]),
      3,
      ctrl.signal,
    )
    expect(f.calls[0].init.signal).toBe(ctrl.signal)
  })
})

describe('the fetch it uses by default', () => {
  it('is the platform one — the seam exists for tests, not for a second transport', async () => {
    const d = new Dispatcher()
    await connect(d)
    const spy = vi.fn(() => Promise.resolve({ ok: true, status: 200 } as Response))
    const original = globalThis.fetch
    globalThis.fetch = spy
    try {
      await createUploadServices(d).sendBody('/upload/tk', new Blob([new Uint8Array(3)]), 3)
      expect(spy).toHaveBeenCalledTimes(1)
    } finally {
      globalThis.fetch = original
    }
  })
})
