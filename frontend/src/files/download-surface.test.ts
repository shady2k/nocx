// @vitest-environment jsdom
//
// THE FAILURE-TOAST TESTS THAT USED TO BE HERE ARE GONE WITH THE TOAST
// (nocx-zlxmm). This surface subscribed to files.downloadDone and called
// showToast itself on a `failed` outcome; the backend now raises that
// outcome into the notification pipeline at `settleDownload`, and the
// pipeline's toast sink lands in the same showToast — so the subscription
// was a second mechanism for one fact and two toasts for one download.
//
// What replaces those four tests is not nothing, and it is deliberately not
// here: the raise and its level per outcome are pinned in
// internal/transport/ws_transfer_notify_test.go, over the real socket, and
// the delivery from `notify.toast` into the kit's toast is pinned in
// src/notify/toast-bridge.test.ts. The behaviour moved, so the test moved
// with it.
//
// The ASSERTION LEFT BEHIND is the one this file can still make alone: this
// surface raises no toast of its own for any terminal outcome. It is what
// stops the removed subscription being reintroduced by somebody who reads
// the operations panel and concludes a failure goes unreported.
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

describe("a terminal outcome is the pipeline's to report, not this surface's", () => {
  // One case per member of files.downloadDone's outcome enum, because the
  // defect this guards against is a direct toast reappearing for exactly
  // one of them — which is how it looked before nocx-zlxmm, where `failed`
  // had one and the other two did not.
  for (const outcome of ['failed', 'cancelled', 'sent'] as const) {
    it(`raises no toast of its own for a ${outcome} download`, () => {
      const d = fakeDispatcher()
      downloadSurfaceFor(d.dispatcher)
      clearToasts()
      d.emit('files.downloadDone', {
        transferId: 'd1',
        outcome,
        name: 'big.iso',
        bytes: 3,
        total: 400,
        error: 'the remote read failed',
      })
      expect(toasts()).toEqual([])
    })
  }
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
