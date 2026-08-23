// The upload store read as operations. A projection, so what is worth
// asserting is that nothing is invented on the way across — and the one
// judgement it does make, which is whether a cancel is offered.
import { describe, expect, it } from 'vitest'

import { beginTransfer, fakeClock, fakeUploadServices } from './upload-fixtures'
import { createUploadStore } from './upload-store'
import { uploadOperations } from './upload-operations'

function fixture() {
  const services = fakeUploadServices()
  const store = createUploadStore({ services, now: fakeClock().now })
  return { services, store, ops: uploadOperations(store) }
}

describe('an upload as an operation', () => {
  it('carries what the row draws, and nothing the store did not say', () => {
    const f = fixture()
    const id = beginTransfer(f.store, {
      transferId: 't1',
      name: 'big.iso',
      destDir: '/srv/data',
      machine: 'deploy@srv-01',
      size: 400,
    })
    const [op, ...rest] = f.ops()
    expect(rest).toEqual([])
    // The WHOLE record but for the closure, which is asserted below and in
    // its own tests: a partial assertion is how a field nobody set stays
    // undefined until a surface reads it.
    const { cancel, ...data } = op
    expect(data).toEqual({
      // The ROW's id and not the wire's: an operation must not change
      // identity when the backend finally names its transfer.
      id,
      kind: 'upload',
      title: 'big.iso',
      destination: '/srv/data',
      machine: 'deploy@srv-01',
      phase: 'running',
      done: null,
      total: 400,
      speedBytesPerSecond: null,
      error: null,
      startedAt: 1000,
      endedAt: null,
    })
    expect(typeof cancel).toBe('function')
  })

  it('says the name that was actually written, once there is one', () => {
    // keepBoth changes it, and the row must say what is on the far side.
    const f = fixture()
    beginTransfer(f.store, {
      transferId: 't1',
      name: 'notes.txt',
      destDir: '/srv',
      machine: 'deploy@srv-01',
      size: 4,
    })
    f.services.emitDone({
      transferId: 't1',
      outcome: 'written',
      finalName: 'notes-1.txt',
      stranded: [],
    })
    expect(f.ops()[0].title).toBe('notes-1.txt')
  })

  it('offers a cancel while the work is live, on both non-terminal phases', () => {
    const f = fixture()
    const live = beginTransfer(f.store, {
      transferId: 't1',
      name: 'a',
      destDir: '/srv',
      machine: 'deploy@srv-01',
      size: 1,
    })
    expect(f.ops()[0].cancel).not.toBeNull()

    // `unsettled` especially: the renderer lost sight of a transfer the
    // backend may still be writing, and files.uploadCancel is exactly what
    // reaches it. A row with no cancel here would take away the only
    // control that can stop it.
    f.store.unsettle(live, 'the connection dropped')
    expect(f.ops()[0].cancel).not.toBeNull()
  })

  it('offers none once it is over, on every terminal outcome', () => {
    for (const outcome of ['written', 'skipped', 'cancelled', 'failed'] as const) {
      const f = fixture()
      beginTransfer(f.store, {
        transferId: 't1',
        name: 'a',
        destDir: '/srv',
        machine: 'deploy@srv-01',
        size: 1,
      })
      f.services.emitDone({ transferId: 't1', outcome, finalName: 'a', stranded: [] })
      expect(f.ops()[0].cancel).toBeNull()
    }
  })

  it('cancels through the store, which cancels through the wire', () => {
    // Not by deciding a phase locally: the person's cancel races the
    // transfer's own completion every time, and uploadDone says which won.
    const f = fixture()
    beginTransfer(f.store, {
      transferId: 't1',
      name: 'a',
      destDir: '/srv',
      machine: 'deploy@srv-01',
      size: 1,
    })
    f.ops()[0].cancel?.()
    expect(f.services.cancels).toEqual(['t1'])
    expect(f.ops()[0].phase).toBe('running')
  })

  it('carries no destination for a transfer it never saw start', () => {
    // Adopted from a retained outcome after a reload: it knows its name and
    // not where it went, and says so by carrying nothing rather than a
    // guess.
    const f = fixture()
    f.services.emitDone({
      transferId: 't9',
      outcome: 'written',
      finalName: 'orphan.bin',
      stranded: [],
    })
    expect(f.ops()[0].destination).toBe('')
    expect(f.ops()[0].title).toBe('orphan.bin')
  })
})
