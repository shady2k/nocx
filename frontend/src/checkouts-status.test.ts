// @vitest-environment jsdom
//
// "The checkout sweep cannot run" — from the wire to the sentence on the
// Settings page (nocx-xn63t.1.6). The unit tests pin the wording keyed on
// the closed reason enum; the store tests pin the mirror behaviour the
// section depends on: an unanswered read is not a degrade, and a reconnect
// re-reads.
import { describe, it, expect, vi } from 'vitest'
import { CheckoutsStatusStore, checkoutsUnavailableSentence } from './checkouts-status'
import type { CheckoutsStatus } from './generated/checkouts.status'
import type { WSClient } from './ipc'

const AVAILABLE: CheckoutsStatus = { available: true, reason: null, detail: null }

const DEGRADED: CheckoutsStatus = {
  available: false,
  reason: 'noRecord',
  detail: 'the content store is unavailable',
}

describe('checkoutsUnavailableSentence', () => {
  it('says nothing while the sweep can run', () => {
    expect(checkoutsUnavailableSentence(AVAILABLE)).toBeNull()
  })

  it('says nothing before the first read answers', () => {
    expect(checkoutsUnavailableSentence(null)).toBeNull()
  })

  it('names the record as the reason the sweep is dead', () => {
    const sentence = checkoutsUnavailableSentence(DEGRADED)
    expect(sentence).not.toBeNull()
    expect(sentence!.title).toContain('not being cleaned up')
    expect(sentence!.description).toContain('checkouts')
    expect(sentence!.description).toContain('(the content store is unavailable)')
  })

  it('says the stamps are untrusted when the record refused a write', () => {
    const sentence = checkoutsUnavailableSentence({
      available: false,
      reason: 'recordWrites',
      detail: 'disk full',
    })
    expect(sentence).not.toBeNull()
    expect(sentence!.description).toContain('trusted')
    expect(sentence!.description).toContain('(disk full)')
  })

  it('does not invent a why for a reason this build does not know', () => {
    const unknown = {
      available: false,
      reason: 'somethingNew',
      detail: null,
    } as unknown as CheckoutsStatus
    const sentence = checkoutsUnavailableSentence(unknown)
    expect(sentence).not.toBeNull()
    expect(sentence!.description).not.toContain('undefined')
    expect(sentence!.description).not.toContain('null')
  })
})

// ── the mirror ────────────────────────────────────────────────────────────

function fakeClient(answer: () => Promise<unknown>): WSClient {
  return {
    call: vi.fn(() => answer()),
    dispatcher: {
      onConnect: vi.fn(() => () => {}),
      subscribe: vi.fn(() => () => {}),
    },
  } as unknown as WSClient
}

describe('CheckoutsStatusStore', () => {
  it('mirrors a degraded answer and notifies its listener once', async () => {
    const client = fakeClient(() => Promise.resolve(DEGRADED))
    const store = new CheckoutsStatusStore(client)
    const seen: Array<CheckoutsStatus | null> = []
    store.subscribe((s) => seen.push(s))
    store.start()
    await Promise.resolve()
    await Promise.resolve()
    expect(store.status()).toEqual(DEGRADED)
    expect(seen).toEqual([DEGRADED])
  })

  it('leaves the mirror alone when a read fails', async () => {
    const client = fakeClient(() => Promise.reject(new Error('socket closed')))
    const store = new CheckoutsStatusStore(client)
    store.start()
    await Promise.resolve()
    await Promise.resolve()
    expect(store.status()).toBeNull()
  })

  it('does not notify again for an unchanged answer', async () => {
    const client = fakeClient(() => Promise.resolve(DEGRADED))
    const store = new CheckoutsStatusStore(client)
    const seen: Array<CheckoutsStatus | null> = []
    store.subscribe((s) => seen.push(s))
    store.start()
    await Promise.resolve()
    await Promise.resolve()
    await store.refresh()
    expect(seen).toEqual([DEGRADED])
  })
})
