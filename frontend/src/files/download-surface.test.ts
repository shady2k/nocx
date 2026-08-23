// @vitest-environment jsdom
//
// A download's failure is reported through the SAME seam an upload's is: a
// toast, raised from the surface's own subscription to the terminal
// notification, so it arrives wherever the person is looking rather than
// only in the operations panel they may not have open.
//
// The two absences are asserted too, because a toast for a cancel the
// person just asked for is noise, and a toast for a success would be twenty
// notifications on a twenty-file day.
import { afterEach, describe, expect, it } from 'vitest'

import { downloadSurfaceFor } from './download-surface'
import { clearToasts, toasts } from '../ui/toast'
import type { Dispatcher } from '../dispatcher'

/** A dispatcher fake: the surface only ever subscribes and calls, and the
 *  one thing these tests drive is the notification coming back. */
function fakeDispatcher(): {
  dispatcher: Dispatcher
  emit(method: string, params: unknown): void
} {
  const handlers = new Map<string, Set<(p: unknown) => void>>()
  const dispatcher = {
    socket: null,
    call: () => Promise.resolve({}),
    subscribe(method: string, h: (p: unknown) => void) {
      const set = handlers.get(method) ?? new Set()
      set.add(h)
      handlers.set(method, set)
      return () => set.delete(h)
    },
    onConnect: () => () => {},
  } as unknown as Dispatcher
  return {
    dispatcher,
    emit(method, params) {
      for (const h of [...(handlers.get(method) ?? [])]) h(params)
    },
  }
}

afterEach(() => clearToasts())

describe('the one report a person must get wherever they are', () => {
  it('a failed download is a danger toast naming the file and the reason', () => {
    const d = fakeDispatcher()
    downloadSurfaceFor(d.dispatcher)
    clearToasts()
    d.emit('files.downloadDone', {
      transferId: 'd1',
      outcome: 'failed',
      name: 'big.iso',
      bytes: 3,
      total: 400,
      error: 'the remote read failed',
    })
    expect(toasts()).toHaveLength(1)
    expect(toasts()[0].level).toBe('danger')
    // The name, because a person shown "the download failed" and no name
    // cannot tell which of two downloads it was.
    expect(toasts()[0].message).toContain('big.iso')
    expect(toasts()[0].message).toContain('the remote read failed')
  })

  it('says something useful when a failure carries no reason', () => {
    const d = fakeDispatcher()
    downloadSurfaceFor(d.dispatcher)
    clearToasts()
    d.emit('files.downloadDone', {
      transferId: 'd1',
      outcome: 'failed',
      name: 'big.iso',
      bytes: 0,
      total: 400,
    })
    expect(toasts()).toHaveLength(1)
    expect(toasts()[0].message).toContain('big.iso')
  })

  it('says nothing about a cancel — the person asked for it', () => {
    const d = fakeDispatcher()
    downloadSurfaceFor(d.dispatcher)
    clearToasts()
    d.emit('files.downloadDone', {
      transferId: 'd1',
      outcome: 'cancelled',
      name: 'big.iso',
      bytes: 3,
      total: 400,
    })
    expect(toasts()).toEqual([])
  })

  it('says nothing about a success — the file arriving IS the report', () => {
    const d = fakeDispatcher()
    downloadSurfaceFor(d.dispatcher)
    clearToasts()
    d.emit('files.downloadDone', {
      transferId: 'd1',
      outcome: 'sent',
      name: 'big.iso',
      bytes: 400,
      total: 400,
    })
    expect(toasts()).toEqual([])
  })
})

describe('one surface per dispatcher', () => {
  it('the same dispatcher answers the same surface', () => {
    // Two stores subscribed to one notification stream would each mint a
    // row for every transfer the other started.
    const d = fakeDispatcher()
    expect(downloadSurfaceFor(d.dispatcher)).toBe(downloadSurfaceFor(d.dispatcher))
  })

  it('a different dispatcher answers a different one, so nothing is global', () => {
    expect(downloadSurfaceFor(fakeDispatcher().dispatcher)).not.toBe(
      downloadSurfaceFor(fakeDispatcher().dispatcher),
    )
  })
})
