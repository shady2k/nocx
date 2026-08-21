// The flow both gestures run through: one file at a time, one question at
// most, and the source routed rather than reimplemented.
import { describe, expect, it } from 'vitest'
import { createUploadFlow, type UploadSource } from './upload-flow'
import { createUploadStore } from './upload-store'
import { fakeClock, fakeUploadServices } from './upload-fixtures'
import type { CollisionRequest, CollisionResult } from '../ui/collision-dialog'
import type { ToastLevel } from '../ui/toast'

const DEST = { bindingId: 'b1', destDir: '/srv/data' }

function blobOf(size: number): Blob {
  return new Blob([new Uint8Array(size)])
}

function streamSource(name: string, size = 4): UploadSource {
  return { name, size, blob: blobOf(size) }
}

/** The person, scripted: every question gets the same answer, and every
 *  question is recorded so a test can assert HOW MANY were asked. */
function scriptedPerson(result: CollisionResult): {
  ask: (r: CollisionRequest) => Promise<CollisionResult>
  asked: CollisionRequest[]
} {
  const asked: CollisionRequest[] = []
  return {
    asked,
    ask: (r) => {
      asked.push(r)
      return Promise.resolve(result)
    },
  }
}

function reporter(): { report: (m: string, l: ToastLevel) => void; said: string[] } {
  const said: string[] = []
  return { said, report: (m) => said.push(m) }
}

function harness(answer: CollisionResult = { answer: 'skip', applyToAll: false }) {
  const services = fakeUploadServices()
  const store = createUploadStore({ services, now: fakeClock().now })
  const person = scriptedPerson(answer)
  const said = reporter()
  const flow = createUploadFlow({ services, store, ask: person.ask, report: said.report })
  return { services, store, flow, person, said }
}

const COLLISION = { collision: 'exists' } as const
const started = (id: string) => ({ transferId: id })
const waiting = (id: string, ticket = 'tk') => ({
  transferId: id,
  ticket,
  url: `/upload/${ticket}`,
})

describe('the collision question, and its reach', () => {
  it('asks once for a three-file drop when the person applies the answer to all', async () => {
    const h = harness({ answer: 'overwrite', applyToAll: true })
    // The first call collides; the answer then rides every later call, so
    // the backend never answers `collision` again.
    h.services.nextResult = [COLLISION, started('t1'), started('t2'), started('t3')]

    await h.flow.send(DEST, [streamSource('a.txt'), streamSource('b.txt'), streamSource('c.txt')])

    expect(h.person.asked).toHaveLength(1)
    expect(h.services.uploads.map((u) => [u.name, u.onExists])).toEqual([
      ['a.txt', undefined],
      ['a.txt', 'overwrite'],
      ['b.txt', 'overwrite'],
      ['c.txt', 'overwrite'],
    ])
    expect(h.store.transfers().map((t) => t.transferId)).toEqual(['t1', 't2', 't3'])
  })

  it('asks again for each file when the answer was not applied to all', async () => {
    const h = harness({ answer: 'keepBoth', applyToAll: false })
    h.services.nextResult = [COLLISION, started('t1'), COLLISION, started('t2')]

    await h.flow.send(DEST, [streamSource('a.txt'), streamSource('b.txt')])

    expect(h.person.asked).toHaveLength(2)
    expect(h.services.uploads.map((u) => u.onExists)).toEqual([
      undefined,
      'keepBoth',
      undefined,
      'keepBoth',
    ])
  })

  it('counts the file on screen among the remaining ones', async () => {
    // "How many files are still to be sent, THIS ONE INCLUDED" — the
    // dialog draws no apply-to-all at 1, so an off-by-one here is a
    // checkbox that disappears on the second-to-last file.
    const h = harness({ answer: 'skip', applyToAll: false })
    h.services.nextResult = [COLLISION, started('t1'), COLLISION, started('t2')]
    await h.flow.send(DEST, [streamSource('a.txt'), streamSource('b.txt')])
    expect(h.person.asked.map((r) => r.remaining)).toEqual([2, 1])
  })

  it('names the file and where it is going, and nothing else', async () => {
    const h = harness()
    h.services.nextResult = [COLLISION, started('t1')]
    await h.flow.send(DEST, [streamSource('notes.txt')])
    expect(h.person.asked[0]).toEqual({
      name: 'notes.txt',
      destination: '/srv/data',
      remaining: 1,
    })
  })

  it('calls again even when the answer is skip — a skip is the backend deciding, not the renderer', async () => {
    // files.upload with collision:"exists" created NOTHING. Dropping the
    // file here would leave no transfer and no uploadDone, so nothing would
    // ever say what became of it.
    const h = harness({ answer: 'skip', applyToAll: false })
    h.services.nextResult = [COLLISION, started('t1')]
    await h.flow.send(DEST, [streamSource('a.txt')])
    expect(h.services.uploads).toHaveLength(2)
    expect(h.services.uploads[1].onExists).toBe('skip')
    expect(h.store.transfer('t1')?.phase).toBe('running')
  })

  it('never stats anything itself', async () => {
    const h = harness()
    h.services.nextResult = [started('t1')]
    await h.flow.send(DEST, [streamSource('a.txt')])
    // One call, no question: the destination was free and the backend said
    // so. The renderer asked nothing about the filesystem first.
    expect(h.services.uploads).toHaveLength(1)
    expect(h.person.asked).toEqual([])
  })
})

describe('the two sources are routed, not reimplemented', () => {
  it('sends a body for a source the renderer holds bytes for', async () => {
    const h = harness()
    h.services.nextResult = [waiting('t1', 'ab')]
    await h.flow.send(DEST, [streamSource('a.txt', 7)])
    expect('sourceTicket' in h.services.uploads[0]).toBe(false)
    expect(h.services.bodies).toEqual([{ url: '/upload/ab', size: 7 }])
  })

  it('sends no body at all for a source named by a ticket', async () => {
    const h = harness()
    h.services.nextResult = [started('t1')]
    await h.flow.send(DEST, [{ name: 'a.txt', size: 7, sourceTicket: 'c'.repeat(32) }])
    expect(h.services.uploads[0].sourceTicket).toBe('c'.repeat(32))
    expect(h.services.bodies).toEqual([])
  })

  it('starts the row from the RESULT, before any byte has moved', async () => {
    const h = harness()
    h.services.nextResult = [waiting('t1')]
    await h.flow.send(DEST, [streamSource('a.txt', 7)])
    const t = h.store.transfer('t1')
    expect(t?.name).toBe('a.txt')
    expect(t?.destDir).toBe('/srv/data')
    expect(t?.size).toBe(7)
    expect(t?.bytes).toBeNull()
  })
})

describe('what the person is told when it does not work', () => {
  it('tells a 409 and a 410 apart, because they are different facts', async () => {
    const conflict = harness()
    conflict.services.nextResult = [waiting('t1')]
    conflict.services.nextSendBody = [{ ok: false, kind: 'status', status: 409 }]
    await conflict.flow.send(DEST, [streamSource('a.txt')])
    expect(conflict.said.said[0]).toContain('already claimed')

    const gone = harness()
    gone.services.nextResult = [waiting('t2')]
    gone.services.nextSendBody = [{ ok: false, kind: 'status', status: 410 }]
    await gone.flow.send(DEST, [streamSource('a.txt')])
    expect(gone.said.said[0]).toContain('already ended')
  })

  it('records the body failure on the transfer as well as saying it', async () => {
    const h = harness()
    h.services.nextResult = [waiting('t1')]
    h.services.nextSendBody = [{ ok: false, kind: 'network', message: 'connection reset' }]
    await h.flow.send(DEST, [streamSource('a.txt')])
    expect(h.store.transfer('t1')?.phase).toBe('failed')
    expect(h.store.transfer('t1')?.error).toContain('connection reset')
  })

  it('stops the batch when the destination refuses, rather than saying it once per file', async () => {
    // A refusal from files.upload is about the DESTINATION — a local
    // binding has no uploader (R1), a dead binding has no handle — so the
    // remaining files would each produce the same message about a place
    // that is not going to start accepting them.
    const h = harness()
    h.services.nextResult = []
    await h.flow.send(DEST, [streamSource('a.txt'), streamSource('b.txt'), streamSource('c.txt')])
    expect(h.services.uploads).toHaveLength(1)
    expect(h.said.said).toHaveLength(1)
    expect(h.said.said[0]).toContain('a.txt')
  })

  it('never rejects — a failed send is reported, not thrown at the gesture', async () => {
    const h = harness()
    h.services.nextResult = []
    await expect(h.flow.send(DEST, [streamSource('a.txt')])).resolves.toBeUndefined()
  })
})
