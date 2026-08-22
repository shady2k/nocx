// The flow both gestures run through: one file at a time, one question at
// most, and the source routed rather than reimplemented.
import { describe, expect, it } from 'vitest'
import type { SendBodyOutcome } from './upload-client'
import { createUploadFlow, type UploadSource } from './upload-flow'
import { createUploadStore } from './upload-store'
import { isTerminalPhase, type OperationPhase } from '../ui/operation'
import { fakeClock, fakeUploadServices } from './upload-fixtures'
import type { CollisionRequest, CollisionResult } from '../ui/collision-dialog'
import type { ToastLevel } from '../ui/toast'

const DEST = { bindingId: 'b1', destDir: '/srv/data', machine: 'deploy@srv-01' }

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

  it('records a terminal body failure on the transfer as well as saying it', async () => {
    const h = harness()
    h.services.nextResult = [waiting('t1')]
    h.services.nextSendBody = [{ ok: false, kind: 'size', declared: 4, actual: 5 }]
    await h.flow.send(DEST, [streamSource('a.txt')])
    expect(h.store.transfer('t1')?.phase).toBe('failed')
    expect(h.store.transfer('t1')?.error).toContain('changed size')
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

describe('what the renderer does not know, it does not claim', () => {
  // files.uploadDone is the only thing that settles a transfer, and it is
  // RETAINED per session and flushed on reattach precisely so a dropped
  // connection cannot lose a terminal outcome. Two body results leave the
  // renderer without an answer, and inventing `failed` for them does not
  // merely pre-empt that mechanism — it overrides the one thing built to
  // answer this exact question.

  it('leaves a 409 unsettled: another claimant is sending it and the transfer is alive', async () => {
    const h = harness()
    h.services.nextResult = [waiting('t1')]
    h.services.nextSendBody = [{ ok: false, kind: 'status', status: 409 }]
    await h.flow.send(DEST, [streamSource('a.txt')])

    const t = h.store.transfer('t1')
    expect(t?.phase).toBe('unsettled')
    expect(isTerminalPhase(t!.phase)).toBe(false)
    // Cancelling still means something: the body running on the backend is
    // another request's, and files.uploadCancel reaches the transfer.
    h.store.cancel('t1')
    expect(h.services.cancels).toEqual(['t1'])

    h.services.emitDone({
      transferId: 't1',
      outcome: 'written',
      finalName: 'a.txt',
      stranded: [],
    })
    expect(h.store.transfer('t1')?.phase).toBe('written')
    expect(h.store.transfer('t1')?.error).toBeNull()
  })

  it('leaves a dropped connection unsettled, and settles it in EITHER direction', async () => {
    // Both outcomes, because the whole point is that the renderer did not
    // know which one it was going to be. A test that only asserted the
    // happy one would pass for a store that guessed.
    for (const outcome of ['written', 'failed'] as const) {
      const h = harness()
      h.services.nextResult = [waiting('t1')]
      h.services.nextSendBody = [{ ok: false, kind: 'network', message: 'connection reset' }]
      await h.flow.send(DEST, [streamSource('a.txt')])

      const t = h.store.transfer('t1')
      expect(t?.phase).toBe('unsettled')
      expect(isTerminalPhase(t!.phase)).toBe(false)
      expect(t?.error).toContain('connection reset')

      h.services.emitDone({
        transferId: 't1',
        outcome,
        finalName: outcome === 'written' ? 'a.txt' : '',
        ...(outcome === 'failed' ? { error: 'no space left on device' } : {}),
        stranded: [],
      })
      const settled = h.store.transfer('t1')
      expect(settled?.phase).toBe(outcome)
      expect(settled?.error).toBe(outcome === 'failed' ? 'no space left on device' : null)
    }
  })

  it('keeps a genuinely local failure terminal, and unsettles only the two that are unknown', async () => {
    // Flattening everything into `unsettled` trades one lie for another. A
    // body the renderer refused to send is over: no bytes left this
    // machine and none are going to.
    const cases: Array<{ what: string; outcome: SendBodyOutcome; phase: OperationPhase }> = [
      {
        what: 'the file changed size before a byte was sent',
        outcome: { ok: false, kind: 'size', declared: 4, actual: 5 },
        phase: 'failed',
      },
      {
        what: 'the ticket names nothing at all',
        outcome: { ok: false, kind: 'status', status: 410 },
        phase: 'failed',
      },
      {
        what: 'the server read the body and refused it',
        outcome: { ok: false, kind: 'status', status: 500 },
        phase: 'failed',
      },
      {
        what: 'another claimant holds the ticket',
        outcome: { ok: false, kind: 'status', status: 409 },
        phase: 'unsettled',
      },
      {
        what: 'the request never got an answer',
        outcome: { ok: false, kind: 'network', message: 'connection reset' },
        phase: 'unsettled',
      },
    ]
    for (const c of cases) {
      const h = harness()
      h.services.nextResult = [waiting('t1')]
      h.services.nextSendBody = [c.outcome]
      await h.flow.send(DEST, [streamSource('a.txt')])
      expect(`${c.what}: ${h.store.transfer('t1')?.phase}`).toBe(`${c.what}: ${c.phase}`)
    }
  })

  it('tells the person an unsettled transfer is unsettled, not that it failed', async () => {
    const h = harness()
    h.services.nextResult = [waiting('t1')]
    h.services.nextSendBody = [{ ok: false, kind: 'status', status: 409 }]
    await h.flow.send(DEST, [streamSource('a.txt')])
    expect(h.said.said[0]).toContain('already claimed')
    expect(h.said.said[0]).toContain('waiting for the server')
    expect(h.said.said[0].toLowerCase()).not.toContain('failed')
  })
})

describe('the machine the transfer is going to', () => {
  it('is recorded on the row from the destination the gesture resolved', async () => {
    // The list is global and the store knows neither binding nor session,
    // so this is the last place that can know it (amendment to nocx-hbdw4.4).
    const h = harness()
    h.services.nextResult = [started('t1')]
    await h.flow.send(DEST, [streamSource('a.txt')])
    expect(h.store.transfer('t1')?.machine).toBe('deploy@srv-01')
  })
})
