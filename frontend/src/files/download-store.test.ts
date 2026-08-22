// The download store's one property, and the failure paths around it.
//
// IN-FLIGHT STATE COMES FROM files.download's RESULT AND files.downloadDone,
// NEVER FROM A PROGRESS FRAME. The frames are dropped when nobody is
// attached and the done frame is retained, so a person can start a 4 GB
// download, close the laptop, and reattach to exactly one done frame and no
// progress at all.
//
// Written against the same three rules as `upload-store.test.ts` because
// they are the same three rules, plus the two places this direction
// accounts differently: a byte count that is meaningful on a FAILURE, and
// `sent` as its own terminal phase rather than a synonym folded into
// `written`.
import { describe, expect, it } from 'vitest'
import { createRoot } from 'solid-js'

import { fakeClock, fakeDownloadServices } from './download-fixtures'
import { createDownloadStore, type DownloadStore } from './download-store'
import { FINISHED_TRANSFERS_RETAINED } from './upload-store'

function fixture() {
  const services = fakeDownloadServices()
  const clock = fakeClock()
  const store = createDownloadStore({ services, now: clock.now })
  return { services, store, clock }
}

function start(store: DownloadStore, id = 't1'): void {
  store.begin({
    transferId: id,
    name: 'big.iso',
    sourcePath: '/srv/big.iso',
    machine: 'alice@srv-01',
    size: 400,
  })
}

describe('what the renderer knows about a download', () => {
  it('the RESULT starts the row, and it starts running with nothing observed', () => {
    const f = fixture()
    start(f.store)
    const t = f.store.transfer('t1')
    expect(t).toMatchObject({
      transferId: 't1',
      name: 'big.iso',
      sourcePath: '/srv/big.iso',
      // Recorded at the START, because the operations list is global and by
      // the time the row is drawn there is no tab left to ask.
      machine: 'alice@srv-01',
      size: 400,
      phase: 'running',
      endedAt: null,
      adopted: false,
    })
    // The call IS the start, so the store stamps it from its own clock.
    expect(t?.startedAt).toBe(1_000)
    // NOT zero: zero is a measurement, and nothing has been measured.
    expect(t?.bytes).toBeNull()
    expect(t?.speedBytesPerSecond).toBeNull()
  })

  it('a progress frame for a transfer with no row is ignored', () => {
    // Rule 2. After a reconnect this is the ordinary case, not an error:
    // the frame names a transfer whose row lived in a page that reloaded.
    const f = fixture()
    f.services.emitProgress({ transferId: 'ghost', bytes: 10, total: 100 })
    expect(f.store.transfers()).toEqual([])
  })

  it('a progress frame does not reopen a row that has ended', () => {
    const f = fixture()
    start(f.store)
    f.services.emitDone({
      transferId: 't1',
      outcome: 'sent',
      name: 'big.iso',
      bytes: 400,
      total: 400,
    })
    f.services.emitProgress({ transferId: 't1', bytes: 4, total: 400 })
    expect(f.store.transfer('t1')?.phase).toBe('sent')
    expect(f.store.transfer('t1')?.bytes).toBe(400)
  })

  it('a done frame with no row MINTS one, marked adopted, with the size it names', () => {
    // Rule 3, and the case retention exists to serve. It knows its name and
    // its numbers and NOT where the bytes came from, and says so by
    // carrying nothing rather than a guess.
    const f = fixture()
    f.services.emitDone({
      transferId: 't9',
      outcome: 'sent',
      name: 'orphan.bin',
      bytes: 12,
      total: 12,
    })
    expect(f.store.transfer('t9')).toMatchObject({
      name: 'orphan.bin',
      sourcePath: '',
      // Unknown, and saying so rather than guessing: a machine invented
      // here would read exactly like a fact.
      machine: '',
      startedAt: null,
      adopted: true,
      phase: 'sent',
      // The size IS known, and this is the asymmetry with an adopted
      // upload: files.downloadDone carries `total` on every outcome.
      bytes: 12,
      size: 12,
    })
  })

  it('a begin after an adopting done frame makes one row, not two', () => {
    const f = fixture()
    f.services.emitDone({ transferId: 't1', outcome: 'sent', name: 'a', bytes: 1, total: 1 })
    start(f.store)
    expect(f.store.transfers()).toHaveLength(1)
  })

  it('carries `sent` as its own phase rather than folding it into `written`', () => {
    // The wire's word, untranslated. A mapping layer between two spellings
    // of one closed set is the thing `ui/operation.ts` exists to refuse.
    const f = fixture()
    start(f.store)
    f.services.emitDone({
      transferId: 't1',
      outcome: 'sent',
      name: 'big.iso',
      bytes: 400,
      total: 400,
    })
    expect(f.store.transfer('t1')?.phase).toBe('sent')
  })

  it('records the bytes on a FAILURE — the honest account of what the far end got', () => {
    // The difference from an upload, and the reason the field is read on
    // every outcome: a download cannot be undone, so "3 of 12 MB" is the
    // only true thing left to say about a transfer that broke.
    const f = fixture()
    start(f.store)
    f.services.emitDone({
      transferId: 't1',
      outcome: 'failed',
      name: 'big.iso',
      bytes: 137,
      total: 400,
      error: 'the connection dropped',
    })
    expect(f.store.transfer('t1')).toMatchObject({
      phase: 'failed',
      bytes: 137,
      size: 400,
      error: 'the connection dropped',
    })
  })

  it('carries no error on a cancel — a context cancellation is not a fault', () => {
    const f = fixture()
    start(f.store)
    f.store.unsettle('t1', 'lost sight of it')
    f.services.emitDone({
      transferId: 't1',
      outcome: 'cancelled',
      name: 'big.iso',
      bytes: 9,
      total: 400,
    })
    expect(f.store.transfer('t1')?.phase).toBe('cancelled')
    expect(f.store.transfer('t1')?.error).toBeNull()
  })

  it('a second done frame cannot rewrite when the transfer ended', () => {
    const f = fixture()
    start(f.store)
    f.services.emitDone({ transferId: 't1', outcome: 'sent', name: 'a', bytes: 1, total: 1 })
    const first = f.store.transfer('t1')?.endedAt
    f.clock.advance(5_000)
    f.services.emitDone({ transferId: 't1', outcome: 'sent', name: 'a', bytes: 1, total: 1 })
    expect(f.store.transfer('t1')?.endedAt).toBe(first)
  })

  it('derives a rate from successive samples, and stops reporting one when it ends', () => {
    const f = fixture()
    start(f.store)
    f.services.emitProgress({ transferId: 't1', bytes: 100, total: 400 })
    // One sample is not a rate: there is nothing to measure against.
    expect(f.store.transfer('t1')?.speedBytesPerSecond).toBeNull()
    f.clock.advance(1_000)
    f.services.emitProgress({ transferId: 't1', bytes: 300, total: 400 })
    expect(f.store.transfer('t1')?.speedBytesPerSecond).toBe(200)
    f.services.emitDone({
      transferId: 't1',
      outcome: 'sent',
      name: 'big.iso',
      bytes: 400,
      total: 400,
    })
    expect(f.store.transfer('t1')?.speedBytesPerSecond).toBeNull()
  })

  it('learns its size from a frame it was adopted by, and from progress', () => {
    // `total` is repeated on every frame on purpose: a transfer adopted on
    // reattach learns its size here and nowhere else.
    const f = fixture()
    start(f.store)
    f.services.emitProgress({ transferId: 't1', bytes: 1, total: 4096 })
    expect(f.store.transfer('t1')?.size).toBe(4096)
  })
})

describe('when the renderer only knows half the story', () => {
  it('failLocally ends the row with a reason', () => {
    const f = fixture()
    start(f.store)
    f.store.failLocally('t1', 'no connection to fetch over')
    expect(f.store.transfer('t1')).toMatchObject({
      phase: 'failed',
      error: 'no connection to fetch over',
    })
    expect(f.store.transfer('t1')?.endedAt).not.toBeNull()
  })

  it('unsettle is NOT an ending — the backend may still be sending', () => {
    const f = fixture()
    start(f.store)
    f.store.unsettle('t1', 'lost sight of it')
    expect(f.store.transfer('t1')?.phase).toBe('unsettled')
    expect(f.store.transfer('t1')?.endedAt).toBeNull()
  })

  it('neither overrules a terminal phase the backend already gave', () => {
    for (const act of ['fail', 'unsettle'] as const) {
      const f = fixture()
      start(f.store)
      f.services.emitDone({ transferId: 't1', outcome: 'sent', name: 'a', bytes: 1, total: 1 })
      if (act === 'fail') f.store.failLocally('t1', 'late')
      else f.store.unsettle('t1', 'late')
      expect(f.store.transfer('t1')?.phase).toBe('sent')
    }
  })

  it('a downloadDone overrules what the renderer recorded about its own half', () => {
    const f = fixture()
    start(f.store)
    f.store.unsettle('t1', 'lost sight of it')
    f.services.emitDone({ transferId: 't1', outcome: 'sent', name: 'a', bytes: 1, total: 1 })
    expect(f.store.transfer('t1')).toMatchObject({ phase: 'sent', error: null })
  })
})

describe('cancelling', () => {
  it('reaches the wire and decides no phase locally', () => {
    // The person's cancel races the transfer's own completion every time,
    // and downloadDone says which won.
    const f = fixture()
    start(f.store)
    f.store.cancel('t1')
    expect(f.services.cancels).toEqual(['t1'])
    expect(f.store.transfer('t1')?.phase).toBe('running')
  })

  it('swallows a rejected cancel — cancelling a finished transfer is not an error', async () => {
    const f = fixture()
    start(f.store)
    f.services.cancel = () => Promise.reject(new Error('gone'))
    expect(() => f.store.cancel('t1')).not.toThrow()
    await Promise.resolve()
  })
})

describe('what it remembers', () => {
  it('bounds the FINISHED transfers and never drops a live one', () => {
    const f = fixture()
    for (let i = 0; i < FINISHED_TRANSFERS_RETAINED + 5; i++) {
      start(f.store, `f${i}`)
      f.clock.advance(10)
      f.services.emitDone({ transferId: `f${i}`, outcome: 'sent', name: 'a', bytes: 1, total: 1 })
    }
    start(f.store, 'live')
    const ids = f.store.transfers().map((t) => t.transferId)
    expect(ids).toContain('live')
    expect(ids.filter((id) => id !== 'live')).toHaveLength(FINISHED_TRANSFERS_RETAINED)
    // Oldest by endedAt, never by position.
    expect(ids).not.toContain('f0')
  })

  it('drops its wire subscriptions and its rows on dispose', () => {
    const f = fixture()
    start(f.store)
    expect(f.services.subscriberCount()).toBe(2)
    f.store.dispose()
    expect(f.services.subscriberCount()).toBe(0)
    expect(f.store.transfers()).toEqual([])
    // A frame arriving after dispose finds nothing to write to.
    f.services.emitDone({ transferId: 't2', outcome: 'sent', name: 'a', bytes: 1, total: 1 })
    expect(f.store.transfers()).toEqual([])
  })

  it('is reactive: a surface reading one transfer re-runs when it moves', () => {
    createRoot((dispose) => {
      const f = fixture()
      start(f.store)
      const seen: (string | undefined)[] = []
      // A tracked read, the way a surface reads it inside its JSX.
      const track = () => seen.push(f.store.transfer('t1')?.phase)
      track()
      f.services.emitDone({ transferId: 't1', outcome: 'sent', name: 'a', bytes: 1, total: 1 })
      track()
      expect(seen).toEqual(['running', 'sent'])
      dispose()
    })
  })
})
