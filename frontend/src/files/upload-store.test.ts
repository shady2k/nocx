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
  FINISHED_TRANSFERS_RETAINED,
  createUploadStore,
  type UploadStore,
  type UploadTransfer,
} from './upload-store'
import { isTerminalPhase, type OperationPhase } from '../ui/operation'
import type { UploadServices } from './upload-client'
import { beginTransfer, fakeClock, fakeUploadServices } from './upload-fixtures'

/** One running transfer, and the ROW's id for it — which is what every
 *  store call takes, because a file has a row before it has a transferId. */
function seeded(store: UploadStore, size = 400): string {
  return beginTransfer(store, {
    transferId: 't1',
    name: 'big.iso',
    destDir: '/srv/data',
    machine: 'deploy@srv-01',
    size,
  })
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
    const id = seeded(store)
    store.cancel(id)
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
    const id = seeded(store, 400)
    // The WHOLE record, not the fields this test happens to care about: a
    // partial assertion is how a field nobody set stays undefined until a
    // surface reads it.
    const expected: UploadTransfer = {
      id,
      transferId: 't1',
      name: 'big.iso',
      destDir: '/srv/data',
      machine: 'deploy@srv-01',
      size: 400,
      bytes: null,
      speedBytesPerSecond: null,
      phase: 'running',
      startedAt: 1000,
      endedAt: null,
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
      const phase: OperationPhase | undefined = store.transfer('t1')?.phase
      expect(phase).toBe(outcome)
      // The two non-terminal values, named: a row the wire has settled is
      // neither still moving nor still unknown.
      expect(phase).not.toBe('running')
      expect(phase).not.toBe('unsettled')
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
    // The renderer's POST is only its half of the transfer, and uploadDone
    // — the account that may not be lost — is the authority over both.
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    const id = seeded(store)
    store.failLocally(id, 'the file changed size while it was being sent')
    expect(store.transfer('t1')?.phase).toBe('failed')
    services.emitDone({ transferId: 't1', outcome: 'written', finalName: 'big.iso', stranded: [] })
    expect(store.transfer('t1')?.phase).toBe('written')
    expect(store.transfer('t1')?.error).toBeNull()
  })
})

describe('the row the renderer cannot account for', () => {
  // A 409 means another claimant's body is running for this ticket, and a
  // dropped connection means the backend may be writing the file right
  // now. Neither is a failure and neither is over; only uploadDone says.

  it('is not terminal, so nothing about it is settled yet', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    const id = seeded(store)
    store.unsettle(id, 'the upload was already claimed by another request')
    const t = store.transfer('t1')
    expect(t?.phase).toBe('unsettled')
    expect(isTerminalPhase(t!.phase)).toBe(false)
    expect(t?.error).toContain('already claimed')
    // The rate is arithmetic over samples this renderer can no longer
    // vouch for, so it stops rather than freezing at its last value.
    expect(t?.speedBytesPerSecond).toBeNull()
  })

  it('still takes progress, because a 409 transfer is genuinely running', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    const id = seeded(store, 400)
    store.unsettle(id, 'the upload was already claimed by another request')
    services.emitProgress({ transferId: 't1', bytes: 200, total: 400 })
    const t = store.transfer('t1')
    expect(t?.bytes).toBe(200)
    // And a sample still does not decide the phase — that is the store's
    // one property, and it holds for this state as much as for `running`.
    expect(t?.phase).toBe('unsettled')
  })

  it('settles in either direction the moment uploadDone arrives', () => {
    for (const outcome of ['written', 'failed', 'cancelled', 'skipped'] as const) {
      const services = fakeUploadServices()
      const store = createUploadStore({ services, now: fakeClock().now })
      const id = seeded(store)
      store.unsettle(id, 'the connection dropped')
      services.emitDone({ transferId: 't1', outcome, finalName: '', stranded: [] })
      const t = store.transfer('t1')
      expect(t?.phase).toBe(outcome)
      // Whatever the renderer recorded about its own half is replaced, so
      // no settled row carries a reason the wire did not give it.
      expect(t?.error).toBeNull()
    }
  })

  it('cannot reopen a transfer the wire already settled', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    const id = seeded(store)
    services.emitDone({ transferId: 't1', outcome: 'written', finalName: 'big.iso', stranded: [] })
    store.unsettle(id, 'the connection dropped')
    expect(store.transfer('t1')?.phase).toBe('written')
  })
})

describe('the store as a surface', () => {
  it('lists transfers in the order they started', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    beginTransfer(store, {
      transferId: 'a',
      name: 'a.txt',
      destDir: '/d',
      machine: 'deploy@srv-01',
      size: 1,
    })
    beginTransfer(store, {
      transferId: 'b',
      name: 'b.txt',
      destDir: '/d',
      machine: 'deploy@srv-01',
      size: 2,
    })
    expect(store.transfers().map((t) => t.transferId)).toEqual(['a', 'b'])
  })

  it('stamps a finished transfer with when it ended, and leaves a live one unstamped', () => {
    // `endedAt` is what orders the finished half of the operations list and
    // decides which one falls off the end. It cannot be read off the array,
    // which is in START order: a transfer that started first does not
    // finish first, and the pair below is exactly that case.
    const clock = fakeClock()
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: clock.now })
    beginTransfer(store, {
      transferId: 'a',
      name: 'a.txt',
      destDir: '/d',
      machine: 'deploy@srv-01',
      size: 1,
    })
    beginTransfer(store, {
      transferId: 'b',
      name: 'b.txt',
      destDir: '/d',
      machine: 'deploy@srv-01',
      size: 2,
    })

    clock.advance(50)
    services.emitDone({ transferId: 'b', outcome: 'written', finalName: 'b.txt', stranded: [] })
    expect(store.transfer('a')?.endedAt).toBeNull()
    const bEnded = store.transfer('b')?.endedAt
    expect(bEnded).toBe(1_050)

    clock.advance(50)
    services.emitDone({ transferId: 'a', outcome: 'written', finalName: 'a.txt', stranded: [] })
    expect(store.transfer('a')?.endedAt).toBe(1_100)
    // A second terminal frame for the same transfer does not rewrite when
    // it ended — the retention order would move under the reader.
    clock.advance(50)
    services.emitDone({ transferId: 'b', outcome: 'written', finalName: 'b.txt', stranded: [] })
    expect(store.transfer('b')?.endedAt).toBe(bEnded)
  })

  it('stamps a locally-failed transfer too, so it is retained like any other outcome', () => {
    const clock = fakeClock()
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: clock.now })
    const id = seeded(store)
    clock.advance(7)
    store.failLocally(id, 'the file could not be read')
    expect(store.transfer('t1')?.endedAt).toBe(1_007)
  })

  it('leaves an unsettled transfer unstamped — it has not ended', () => {
    // `unsettled` is the renderer not knowing, not the transfer being over.
    // A stamp here would put it in the finished half of the list and expose
    // it to eviction while files.uploadCancel still reaches it.
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    const id = seeded(store)
    store.unsettle(id, 'the connection dropped')
    expect(store.transfer('t1')?.endedAt).toBeNull()
  })

  it('remembers a bounded number of FINISHED transfers, dropping the oldest by when it ended', () => {
    // A finished transfer does not vanish and does not accumulate. It used
    // to do the second: a row stayed until somebody clicked its ×, which
    // is a chore the product invented for itself.
    const clock = fakeClock()
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: clock.now })
    const total = FINISHED_TRANSFERS_RETAINED + 3
    for (let i = 0; i < total; i++) {
      beginTransfer(store, {
        transferId: `t${i}`,
        name: `f${i}`,
        destDir: '/d',
        machine: 'deploy@srv-01',
        size: 1,
      })
    }
    // Finished in REVERSE start order, so "oldest" cannot be read off the
    // array — only `endedAt` answers it.
    for (let i = total - 1; i >= 0; i--) {
      clock.advance(1)
      services.emitDone({
        transferId: `t${i}`,
        outcome: 'written',
        finalName: `f${i}`,
        stranded: [],
      })
    }
    const ids = store.transfers().map((t) => t.transferId)
    expect(ids).toHaveLength(FINISHED_TRANSFERS_RETAINED)
    // t2, t1, t0 ended last, so they survive; the highest-numbered ones
    // ended first and are the ones dropped.
    expect(ids).toContain('t0')
    expect(ids).toContain('t2')
    expect(ids).not.toContain(`t${total - 1}`)
  })

  it('never evicts a transfer that is still live, however many have finished', () => {
    // The bound is about how long an OUTCOME is worth reading. A running
    // transfer has no outcome yet, and dropping it would take away the only
    // control that can stop it.
    const clock = fakeClock()
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: clock.now })
    beginTransfer(store, {
      transferId: 'live',
      name: 'big.iso',
      destDir: '/d',
      machine: 'deploy@srv-01',
      size: 400,
    })
    for (let i = 0; i < FINISHED_TRANSFERS_RETAINED + 5; i++) {
      beginTransfer(store, {
        transferId: `t${i}`,
        name: `f${i}`,
        destDir: '/d',
        machine: 'deploy@srv-01',
        size: 1,
      })
      clock.advance(1)
      services.emitDone({
        transferId: `t${i}`,
        outcome: 'written',
        finalName: `f${i}`,
        stranded: [],
      })
    }
    expect(store.transfer('live')?.phase).toBe('running')
    expect(store.transfers()).toHaveLength(FINISHED_TRANSFERS_RETAINED + 1)
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
    const id = seeded(store)
    expect(() => store.cancel(id)).not.toThrow()
    await Promise.resolve()
    // The transfer is still running as far as anybody knows: nothing was
    // told to it, so nothing about it changed.
    expect(store.transfer('t1')?.phase).toBe('running')
  })
})

// The renderer's own intent, recorded so the flow can tell a cancel apart
// from a refusal when the body send dies under it (nocx-hbdw4.3).
describe('the store remembers that somebody asked to stop', () => {
  it('says no about a transfer nobody has cancelled', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    const id = seeded(store, 400)
    expect(store.cancelRequested(id)).toBe(false)
  })

  it('says yes the instant cancel is called, not when the wire answers', () => {
    // Synchronously: the body POST this cancel is about to break may fail
    // before services.cancel resolves, and that is the moment the flow asks.
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    const id = seeded(store, 400)
    store.cancel(id)
    expect(store.cancelRequested(id)).toBe(true)
  })

  it('answers per transfer, never for the session', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    const a = beginTransfer(store, {
      transferId: 'a',
      name: 'a',
      destDir: '/d',
      machine: 'srv-01',
      size: 1,
    })
    const b = beginTransfer(store, {
      transferId: 'b',
      name: 'b',
      destDir: '/d',
      machine: 'srv-01',
      size: 1,
    })
    store.cancel(a)
    expect(store.cancelRequested(a)).toBe(true)
    expect(store.cancelRequested(b)).toBe(false)
  })

  it('stays yes after the outcome lands — the question is "did we ask"', () => {
    // Not "did it work". A cancel that lost the race was still asked for,
    // and a later body failure for the same id is still not news.
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    const id = seeded(store, 400)
    store.cancel(id)
    services.emitDone({ transferId: 't1', outcome: 'written', finalName: 'big.iso', stranded: [] })
    expect(store.cancelRequested(id)).toBe(true)
  })

  it('names a row that is not there and does nothing at all', () => {
    // Since nocx-hbdw4.6 a cancel names the ROW, and a row is what a
    // person presses — so there is no such thing as cancelling something
    // that is not on screen, and the previous "cancel an id with no row"
    // case cannot be written any more. What is left is that naming a row
    // that has gone is quiet: nothing on the wire, nothing recorded.
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    store.cancel('gone')
    expect(services.cancels).toEqual([])
    expect(store.cancelRequested('gone')).toBe(false)
  })
})

// What a finished row needs and the store is the only place that can stamp
// (nocx-hbdw4.4).
describe('the store stamps both ends of the work', () => {
  it('records when it started, on its own clock', () => {
    const clock = fakeClock()
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: clock.now })
    seeded(store, 400)
    const startedAt = store.transfer('t1')?.startedAt
    expect(startedAt).not.toBeNull()

    clock.advance(14_000)
    services.emitDone({ transferId: 't1', outcome: 'written', finalName: 'big.iso', stranded: [] })
    const t = store.transfer('t1')
    if (t === undefined || t.startedAt === null || t.endedAt === null) {
      throw new Error('a finished transfer must carry both ends')
    }
    // Both ends, which is what makes a duration a span rather than a moment.
    expect(t.endedAt - t.startedAt).toBe(14_000)
  })

  it('leaves an adopted transfer with no start and no size, rather than a zero of each', () => {
    // The row lived in a page that was reloaded: this renderer never saw
    // the call that would have carried them. "0 B, took 0 ms" would be an
    // answer to a question nobody could answer.
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    services.emitDone({ transferId: 'x', outcome: 'written', finalName: 'a.txt', stranded: [] })
    const t = store.transfer('x')
    expect(t?.adopted).toBe(true)
    expect(t?.startedAt).toBeNull()
    expect(t?.size).toBeNull()
    expect(t?.machine).toBe('')
  })
})

// ── The half of a batch the backend has never heard of (nocx-hbdw4.6) ────
//
// A batch sends one file at a time per binding (design §4), so every file
// after the first is waiting its turn. Before this it was waiting in a loop
// variable: drop two files and the second was not in the list, not in the
// count and not cancellable until the first had finished (owner,
// 2026-08-22).
describe('a file that has joined the batch and not been sent', () => {
  function batch(store: UploadStore, names: string[]): string[] {
    return store.enqueue(
      names.map((name) => ({ name, destDir: '/srv/data', machine: 'deploy@srv-01', size: 400 })),
    )
  }

  it('is a row from the moment it is enqueued, one per file, in order', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    const ids = batch(store, ['a.txt', 'b.txt', 'c.txt'])
    expect(ids).toHaveLength(3)
    expect(new Set(ids).size).toBe(3)
    expect(store.transfers().map((t) => t.name)).toEqual(['a.txt', 'b.txt', 'c.txt'])
    expect(store.transfers().map((t) => t.id)).toEqual(ids)
  })

  it('claims nothing about itself that has not happened', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    const [id] = batch(store, ['a.txt'])
    // The WHOLE record: a partial assertion is how a field nobody set
    // stays undefined until a surface reads it.
    const expected: UploadTransfer = {
      id,
      // No call has been made, so the wire has no name for it.
      transferId: null,
      name: 'a.txt',
      destDir: '/srv/data',
      machine: 'deploy@srv-01',
      // The one measurement a waiting file has, and it is what lets the
      // aggregate bar be about the batch rather than about one file.
      size: 400,
      bytes: null,
      speedBytesPerSecond: null,
      phase: 'queued',
      // Not started. A duration needs both ends and this end has not
      // happened yet.
      startedAt: null,
      endedAt: null,
      finalName: '',
      error: null,
      stranded: [],
      adopted: false,
    }
    expect(store.row(id)).toEqual(expected)
    // And it is outstanding work, not finished work: something is still
    // going to happen to it.
    expect(isTerminalPhase(store.row(id)!.phase)).toBe(false)
  })

  it('is not reachable by the wire, because the wire has not named it', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    batch(store, ['a.txt'])
    // A progress frame quoting an id nothing has been given cannot find a
    // queued row and must not mint one — rule 2 holds for this phase as
    // much as for `running`.
    services.emitProgress({ transferId: 't1', bytes: 5, total: 400 })
    expect(store.transfers()).toHaveLength(1)
    expect(store.transfers()[0].phase).toBe('queued')
    expect(store.transfers()[0].bytes).toBeNull()
  })

  it('starts in place when the result finally names it, keeping its identity', () => {
    const clock = fakeClock()
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: clock.now })
    const [id] = batch(store, ['a.txt'])
    clock.advance(500)
    expect(store.start(id, 't1')).toBe(true)
    const t = store.row(id)
    // The SAME row: a surface keys on the id, and a promotion that minted a
    // new one would dispose the row under whoever was pressing its × —
    // which is nocx-hbdw4.1's defect, arriving a second way.
    expect(t?.id).toBe(id)
    expect(store.transfers()).toHaveLength(1)
    expect(t?.transferId).toBe('t1')
    expect(t?.phase).toBe('running')
    // Stamped when it STARTED, not when it was dropped: the duration a
    // finished row reports is how long the transfer took, not how long the
    // person waited for their turn in the queue.
    expect(t?.startedAt).toBe(1_500)
    expect(store.transfer('t1')?.id).toBe(id)
  })

  it('takes progress only after it has started', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    const [id] = batch(store, ['a.txt'])
    store.start(id, 't1')
    services.emitProgress({ transferId: 't1', bytes: 100, total: 400 })
    expect(store.row(id)?.bytes).toBe(100)
  })
})

describe('taking one file out of the batch', () => {
  function batch(store: UploadStore, names: string[]): string[] {
    return store.enqueue(
      names.map((name) => ({ name, destDir: '/srv/data', machine: 'deploy@srv-01', size: 400 })),
    )
  }

  it('says nothing on the wire, because there is no transfer to say it about', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    const [a, b] = batch(store, ['a.txt', 'b.txt'])
    store.cancel(b)
    // files.upload was never called for it, so files.uploadCancel has
    // nothing to name.
    expect(services.cancels).toEqual([])
    // It leaves the batch. It is not an OUTCOME — nothing was attempted,
    // nothing was written — so it is not a row in the finished list either.
    expect(store.row(b)).toBeUndefined()
    expect(store.transfers().map((t) => t.id)).toEqual([a])
  })

  it('leaves every other file in the batch exactly where it was', () => {
    // One press stops one file. A cancel that took the batch with it would
    // make the outcome depend on which row somebody happened to press.
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    const [a, b, c] = batch(store, ['a.txt', 'b.txt', 'c.txt'])
    store.start(a, 't1')
    store.cancel(b)
    expect(store.row(a)?.phase).toBe('running')
    expect(store.row(c)?.phase).toBe('queued')
  })

  it('still cancels on the wire once the file has started', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    const [a] = batch(store, ['a.txt'])
    store.start(a, 't1')
    store.cancel(a)
    expect(services.cancels).toEqual(['t1'])
    // And the row stays: the cancel races the transfer's own completion,
    // and uploadDone is what says which won.
    expect(store.row(a)?.phase).toBe('running')
  })
})

describe('a file cancelled while its own files.upload call was in flight', () => {
  // The narrow window the sequential send opens: the row is queued for as
  // long as the call takes, and longer if the backend answers `collision`
  // and a person is looking at the dialog. Press × in that window and an
  // id comes back for a row that is not there any more.

  function inFlight(): {
    services: ReturnType<typeof fakeUploadServices>
    store: UploadStore
    id: string
  } {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    const [id] = store.enqueue([
      { name: 'a.txt', destDir: '/srv/data', machine: 'deploy@srv-01', size: 400 },
    ])
    store.cancel(id)
    return { services, store, id }
  }

  it('stops the transfer that came back, rather than orphaning it', () => {
    // It exists on the backend and has no row and no owner. Leaving it
    // would be an upload running on somebody's server that nothing in the
    // product can reach.
    const f = inFlight()
    expect(f.store.start(f.id, 't1')).toBe(false)
    expect(f.services.cancels).toEqual(['t1'])
  })

  it('does not bring the row back when the outcome arrives', () => {
    // Adoption is for a transfer this renderer never saw start (rule 3).
    // This is the opposite: it started it, stopped it on purpose, and the
    // person watched the row go — reappearing as an adopted row that knows
    // neither its destination nor its size would contradict them.
    const f = inFlight()
    f.store.start(f.id, 't1')
    f.services.emitDone({ transferId: 't1', outcome: 'cancelled', finalName: '', stranded: [] })
    expect(f.store.transfers()).toEqual([])
  })
})

describe('the files a refusal means nobody will ever attempt', () => {
  it('are closed with the reason rather than left waiting for ever', () => {
    const clock = fakeClock()
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: clock.now })
    const [id] = store.enqueue([
      { name: 'b.txt', destDir: '/srv/data', machine: 'deploy@srv-01', size: 400 },
    ])
    clock.advance(9)
    store.abandon(id, 'not attempted: a.txt was refused')
    const t = store.row(id)
    // `skipped` is the wire's own word for "not written, and that is
    // fine", which is exactly what happened to it — and it is NEUTRAL, so
    // four of them do not read as four failures beside the one real one.
    expect(t?.phase).toBe('skipped')
    expect(t?.error).toContain('not attempted')
    expect(isTerminalPhase(t!.phase)).toBe(true)
    // Stamped, so it is ordered and retained like any other outcome.
    expect(t?.endedAt).toBe(1_009)
    // And never given a start it did not have: a duration needs both ends.
    expect(t?.startedAt).toBeNull()
  })

  it('cannot be used on a file that did start — that one has an account coming', () => {
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    const id = seeded(store)
    store.abandon(id, 'not attempted')
    expect(store.row(id)?.phase).toBe('running')
  })
})

describe('a transfer whose outcome arrived before its own result did', () => {
  it('is one row and not two, and keeps what each half knew', () => {
    // A retained `uploadDone` adopts a transfer this renderer never saw
    // start (rule 3) — and it can name the very transfer a queued row is
    // about to be given. They are one file: the adopted half has the
    // outcome, the queued half has where it was going.
    const services = fakeUploadServices()
    const store = createUploadStore({ services, now: fakeClock().now })
    const [id] = store.enqueue([
      { name: 'big.iso', destDir: '/srv/data', machine: 'deploy@srv-01', size: 400 },
    ])
    services.emitDone({
      transferId: 't1',
      outcome: 'written',
      finalName: 'big.iso',
      stranded: [],
    })

    // Nothing left to send: it is already over.
    expect(store.start(id, 't1')).toBe(false)

    const rows = store.transfers()
    expect(rows).toHaveLength(1)
    expect(rows[0].phase).toBe('written')
    // And it no longer says it knows nothing about where the file went.
    expect(rows[0].destDir).toBe('/srv/data')
    expect(rows[0].machine).toBe('deploy@srv-01')
    expect(rows[0].size).toBe(400)
    expect(rows[0].adopted).toBe(false)
  })
})
