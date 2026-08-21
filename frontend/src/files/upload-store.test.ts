// The upload store's one property, and the failures it exists to stop.
//
// IN-FLIGHT STATE COMES FROM files.upload's RESULT AND files.uploadDone,
// AND NEVER FROM HAVING SEEN A PROGRESS NOTIFICATION (design §5.5).
// Progress is explicitly lossy: it is emitted to whatever is attached at
// that instant and dropped when nothing is. A person can start a 400 MB
// upload, close the laptop, and reattach after it finished — receiving the
// retained uploadDone and not one progress frame. A store that inferred
// "running" from the first sample would show that transfer as never having
// started; one that inferred "still running" from the absence of samples
// would show it uploading forever.
import { describe, expect, it } from 'vitest'
import {
  createUploadStore,
  type TransferPhase,
  type UploadStore,
  type UploadTransfer,
} from './upload-store'
import type { UploadServices } from './upload-client'
import { fakeClock, fakeUploadServices } from './upload-fixtures'

function seeded(store: UploadStore, size = 400): void {
  store.begin({ transferId: 't1', name: 'big.iso', destDir: '/srv/data', size })
}

describe('in-flight state never comes from a progress notification', () => {
  it('resolves a transfer that produced no progress frame at all', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    // The RPC result is what says the transfer exists.
    seeded(store, 400_000_000)
    expect(store.transfer('t1')?.phase).toBe('running')

    // Zero progress notifications: the laptop was closed for the whole
    // transfer, and the live indicator went to nobody.

    // The retained terminal account, flushed on reattach.
    services.emitDone({
      transferId: 't1',
      outcome: 'written',
      finalName: 'big.iso',
      stranded: [],
    })

    const t = store.transfer('t1')
    expect(t?.phase).toBe('written')
    expect(t?.finalName).toBe('big.iso')
  })

  it('does not mint a transfer from a progress frame', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    services.emitProgress({ transferId: 'unknown', bytes: 5, total: 10 })
    // A renderer with no row for it has nothing to draw and ignores the
    // frame; that is the ordinary case after a reconnect, not an error.
    expect(store.transfers()).toEqual([])
  })

  it('says an upload finished for a transfer it never saw start', () => {
    // The row lived in a page that was reloaded. Retention exists to serve
    // exactly this, and the correct handling is to say the upload finished
    // — not to discard the frame.
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    services.emitDone({
      transferId: 'gone',
      outcome: 'written',
      finalName: 'report.pdf',
      stranded: [],
    })
    const t = store.transfer('gone')
    expect(t?.phase).toBe('written')
    expect(t?.finalName).toBe('report.pdf')
    expect(t?.adopted).toBe(true)
  })

  it('does not let a late progress frame resurrect a finished transfer', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    seeded(store)
    services.emitDone({ transferId: 't1', outcome: 'cancelled', finalName: '', stranded: [] })
    services.emitProgress({ transferId: 't1', bytes: 200, total: 400 })
    expect(store.transfer('t1')?.phase).toBe('cancelled')
  })

  it('leaves the phase alone when the person cancels — the outcome is the wire to say', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    seeded(store)
    store.cancel('t1')
    expect(services.cancels).toEqual(['t1'])
    // The cancel races the transfer's own completion every time, and losing
    // that race is not a failure to show anybody: 'written' is a legitimate
    // answer to a cancel that arrived too late.
    expect(store.transfer('t1')?.phase).toBe('running')
    services.emitDone({ transferId: 't1', outcome: 'written', finalName: 'big.iso', stranded: [] })
    expect(store.transfer('t1')?.phase).toBe('written')
  })
})

describe('the row a result seeds', () => {
  it('is running, with nothing observed about it yet', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    seeded(store, 400)
    // The WHOLE record, not the fields this test happens to care about: a
    // partial assertion is how a field nobody set stays undefined until a
    // surface reads it.
    const expected: UploadTransfer = {
      transferId: 't1',
      name: 'big.iso',
      destDir: '/srv/data',
      size: 400,
      bytes: null,
      speedBytesPerSecond: null,
      phase: 'running',
      finalName: '',
      error: null,
      stranded: [],
      adopted: false,
    }
    expect(store.transfer('t1')).toEqual(expected)
  })

  it('ends on every outcome the wire can send, and on no other', () => {
    // The four are told to a person in four different ways, so they are
    // four values. If the wire grows a fifth, this is where the renderer
    // finds out rather than rendering it as 'running' forever.
    const outcomes = ['written', 'skipped', 'cancelled', 'failed'] as const
    for (const outcome of outcomes) {
      const services = fakeUploadServices()
      const store = createUploadStore({ services, now: fakeClock().now })
      seeded(store)
      services.emitDone({ transferId: 't1', outcome, finalName: '', stranded: [] })
      const phase: TransferPhase | undefined = store.transfer('t1')?.phase
      expect(phase).toBe(outcome)
      expect(phase).not.toBe('running')
    }
  })
})

describe('speed is derived here, from successive samples', () => {
  it('shows nothing rather than zero while no sample has arrived', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    seeded(store)
    const t = store.transfer('t1')
    expect(t?.speedBytesPerSecond).toBeNull()
    // And the byte count is unknown, not zero: nothing has been observed.
    expect(t?.bytes).toBeNull()
  })

  it('still has no speed after one sample — a rate needs two points', () => {
    const services = fakeUploadServices()
    const clock = fakeClock()
    const store = createUploadStore({ services, now: clock.now })
    seeded(store)
    services.emitProgress({ transferId: 't1', bytes: 100, total: 400 })
    expect(store.transfer('t1')?.bytes).toBe(100)
    expect(store.transfer('t1')?.speedBytesPerSecond).toBeNull()
  })

  it('derives bytes per second from the gap between two samples', () => {
    const services = fakeUploadServices()
    const clock = fakeClock()
    const store = createUploadStore({ services, now: clock.now })
    seeded(store, 4_000_000)
    services.emitProgress({ transferId: 't1', bytes: 1_000_000, total: 4_000_000 })
    clock.advance(2_000)
    services.emitProgress({ transferId: 't1', bytes: 3_000_000, total: 4_000_000 })
    // 2 MB in 2 s. The first rate a transfer has is the measurement itself;
    // smoothing has nothing to smooth against yet.
    expect(store.transfer('t1')?.speedBytesPerSecond).toBe(1_000_000)
  })

  it('smooths a later sample rather than jumping to it', () => {
    const services = fakeUploadServices()
    const clock = fakeClock()
    const store = createUploadStore({ services, now: clock.now })
    seeded(store, 10_000)
    services.emitProgress({ transferId: 't1', bytes: 0, total: 10_000 })
    clock.advance(1_000)
    services.emitProgress({ transferId: 't1', bytes: 1_000, total: 10_000 })
    expect(store.transfer('t1')?.speedBytesPerSecond).toBe(1_000)
    clock.advance(1_000)
    services.emitProgress({ transferId: 't1', bytes: 3_000, total: 10_000 })
    // The instant rate is 2000/s; the reported rate moves part of the way.
    const speed = store.transfer('t1')?.speedBytesPerSecond ?? 0
    expect(speed).toBeGreaterThan(1_000)
    expect(speed).toBeLessThan(2_000)
  })

  it('ignores a sample that arrives in the same millisecond as the last', () => {
    const services = fakeUploadServices()
    const clock = fakeClock()
    const store = createUploadStore({ services, now: clock.now })
    seeded(store)
    services.emitProgress({ transferId: 't1', bytes: 100, total: 400 })
    services.emitProgress({ transferId: 't1', bytes: 200, total: 400 })
    // Dividing by a zero interval is an infinite rate, which is not a
    // measurement. The byte count still moves.
    expect(store.transfer('t1')?.bytes).toBe(200)
    expect(store.transfer('t1')?.speedBytesPerSecond).toBeNull()
  })

  it('takes the total from the frame — a transfer adopted on reattach has no declared size', () => {
    const services = fakeUploadServices()
    const clock = fakeClock()
    const store = createUploadStore({ services, now: clock.now })
    seeded(store, 0)
    services.emitProgress({ transferId: 't1', bytes: 10, total: 400 })
    expect(store.transfer('t1')?.size).toBe(400)
  })
})

describe('the terminal account', () => {
  it('carries the failure reason on a failed outcome', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    seeded(store)
    services.emitDone({
      transferId: 't1',
      outcome: 'failed',
      finalName: '',
      error: 'permission denied',
      stranded: [],
    })
    const t = store.transfer('t1')
    expect(t?.phase).toBe('failed')
    expect(t?.error).toBe('permission denied')
  })

  it('reports a cancelled transfer as cancelled and never as a failure', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    seeded(store)
    services.emitDone({ transferId: 't1', outcome: 'cancelled', finalName: '', stranded: [] })
    const t = store.transfer('t1')
    expect(t?.phase).toBe('cancelled')
    expect(t?.error).toBeNull()
  })

  it('reports a skipped transfer, which moved nothing and is not a failure', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    seeded(store)
    services.emitDone({ transferId: 't1', outcome: 'skipped', finalName: '', stranded: [] })
    expect(store.transfer('t1')?.phase).toBe('skipped')
  })

  it('carries what was left behind, orthogonally to the outcome', () => {
    // A 'written' transfer whose backup unlink failed succeeded AND
    // stranded a path; naming one of them would leave a person with an
    // unmentioned file on their disk.
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    seeded(store)
    services.emitDone({
      transferId: 't1',
      outcome: 'written',
      finalName: 'big.iso',
      stranded: ['/srv/data/big.iso.nocx-bak-9f'],
    })
    const t = store.transfer('t1')
    expect(t?.phase).toBe('written')
    expect(t?.stranded).toEqual(['/srv/data/big.iso.nocx-bak-9f'])
  })

  it('lets the wire overrule a failure the renderer recorded', () => {
    // The renderer's POST is only its half of the transfer. A 409 means
    // somebody else claimed the ticket and the transfer is running — so
    // uploadDone, the account that may not be lost, is the authority.
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    seeded(store)
    store.failLocally('t1', 'the body was refused: 409')
    expect(store.transfer('t1')?.phase).toBe('failed')
    services.emitDone({ transferId: 't1', outcome: 'written', finalName: 'big.iso', stranded: [] })
    expect(store.transfer('t1')?.phase).toBe('written')
    expect(store.transfer('t1')?.error).toBeNull()
  })
})

describe('the store as a surface', () => {
  it('lists transfers in the order they started', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    store.begin({ transferId: 'a', name: 'a.txt', destDir: '/d', size: 1 })
    store.begin({ transferId: 'b', name: 'b.txt', destDir: '/d', size: 2 })
    expect(store.transfers().map((t) => t.transferId)).toEqual(['a', 'b'])
  })

  it('dismisses a row the person is done reading', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    seeded(store)
    services.emitDone({ transferId: 't1', outcome: 'written', finalName: 'big.iso', stranded: [] })
    store.dismiss('t1')
    expect(store.transfers()).toEqual([])
  })

  it('unsubscribes from the wire when it is disposed', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    expect(services.subscriberCount()).toBe(2)
    store.dispose()
    expect(services.subscriberCount()).toBe(0)
    // And a frame arriving afterwards touches nothing.
    services.emitDone({ transferId: 't1', outcome: 'written', finalName: 'x', stranded: [] })
    expect(store.transfers()).toEqual([])
  })

  it('survives a cancel the wire rejected — a refused cancel is not an unhandled rejection', async () => {
    const services = fakeUploadServices()
    const failing: UploadServices = {
      ...services,
      cancel: () => Promise.reject(new Error('socket gone')),
    }
    const store = createUploadStore({ services: failing, now: fakeClock().now })
    seeded(store)
    expect(() => store.cancel('t1')).not.toThrow()
    await Promise.resolve()
    // The transfer is still running as far as anybody knows: nothing was
    // told to it, so nothing about it changed.
    expect(store.transfer('t1')?.phase).toBe('running')
  })
})
