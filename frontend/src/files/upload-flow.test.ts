// The flow both gestures run through: one file at a time, one question at
// most, and the source routed rather than reimplemented.
import { describe, expect, it } from 'vitest'
import type { SendBodyOutcome } from './upload-client'
import { createUploadFlow, type UploadSource } from './upload-flow'
import { createUploadStore, type UploadStore } from './upload-store'
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

/** The ROW id for a transfer the wire has named, which is what every store
 *  call takes: a file has a row from the moment it is dropped and a
 *  transferId only once files.upload has answered (nocx-hbdw4.6). */
function rowOf(store: UploadStore, transferId: string): string {
  const t = store.transfer(transferId)
  if (t === undefined) throw new Error(`no row for ${transferId}`)
  return t.id
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
    h.store.cancel(rowOf(h.store, 't1'))
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

// ── Cancelling is intent succeeding, not a failure (nocx-hbdw4.3) ────────
//
// Press Cancel and the row correctly said `Cancelled`, while a red toast
// beside it said "calmhub.zip: the server refused the body (500)": two
// messages about one event, contradicting each other, and the toast blaming
// the server for what the person did on purpose (owner, running the
// product). The backend answering 500 to a deliberate cancellation is its
// own wrong and its own bead; this half is the renderer, which ALREADY
// KNOWS the transfer was cancelled because it is the thing that asked.
describe('a body send that fails after the person cancelled', () => {
  /** The real sequence: the body is in flight, the person presses Cancel,
   *  and the POST dies under it. Nothing else can produce this. */
  function cancelDuringBody(
    h: ReturnType<typeof harness>,
    outcome: SendBodyOutcome,
    transferId = 't1',
  ): void {
    h.services.sendBody = (url: string, body: Blob, size: number) => {
      void url
      void body
      void size
      h.store.cancel(rowOf(h.store, transferId))
      return Promise.resolve(outcome)
    }
  }

  it('raises no toast at all', async () => {
    const h = harness()
    h.services.nextResult = [waiting('t1')]
    cancelDuringBody(h, { ok: false, kind: 'status', status: 500 })

    await h.flow.send(DEST, [streamSource('calmhub.zip')])

    expect(h.said.said).toEqual([])
  })

  it('does not call it failed either — the backend still says how it ended', async () => {
    const h = harness()
    h.services.nextResult = [waiting('t1')]
    cancelDuringBody(h, { ok: false, kind: 'status', status: 500 })
    await h.flow.send(DEST, [streamSource('calmhub.zip')])

    // The renderer's half is over and the outcome is not the renderer's to
    // state: the cancel races the transfer's completion every time.
    expect(h.store.transfer('t1')?.phase).toBe('unsettled')
    expect(isTerminalPhase(h.store.transfer('t1')!.phase)).toBe(false)

    h.services.emitDone({
      transferId: 't1',
      outcome: 'cancelled',
      finalName: '',
      stranded: [],
    })
    expect(h.store.transfer('t1')?.phase).toBe('cancelled')
    // And no reason under the row: a cancelled transfer's underlying error
    // is a context cancellation, which is not a fault.
    expect(h.store.transfer('t1')?.error).toBeNull()
    expect(h.said.said).toEqual([])
  })

  it('stays quiet for the cancel LOSING the race too', async () => {
    // The person pressed Cancel, the body still failed, and the backend
    // finished writing anyway. Still not a refusal, still not news.
    const h = harness()
    h.services.nextResult = [waiting('t1')]
    cancelDuringBody(h, { ok: false, kind: 'network', message: 'connection reset' })
    await h.flow.send(DEST, [streamSource('calmhub.zip')])

    h.services.emitDone({
      transferId: 't1',
      outcome: 'written',
      finalName: 'calmhub.zip',
      stranded: [],
    })
    expect(h.store.transfer('t1')?.phase).toBe('written')
    expect(h.said.said).toEqual([])
  })
})

// AND THE OTHER HALF, which is what stops this fix trading one silence for
// another: a refusal nobody asked for must still be reported. This is the
// exact message the defect above was about, arriving for the exact reason
// it is meant to.
describe('a body send that fails and nobody asked for it', () => {
  it('says so, in danger, and marks the row failed', async () => {
    const h = harness()
    h.services.nextResult = [waiting('t1')]
    h.services.nextSendBody = [{ ok: false, kind: 'status', status: 500 }]

    await h.flow.send(DEST, [streamSource('calmhub.zip')])

    expect(h.said.said).toEqual(['calmhub.zip: the server refused the body (500)'])
    expect(h.store.transfer('t1')?.phase).toBe('failed')
  })

  it('is silenced only for the transfer that was cancelled, never for its neighbour', async () => {
    // The intent is recorded per transfer id. A set keyed on nothing would
    // silence the whole session after one cancel.
    const h = harness()
    h.services.nextResult = [waiting('t1', 'k1'), waiting('t2', 'k2')]
    let call = 0
    h.services.sendBody = () => {
      call++
      if (call === 1) h.store.cancel(rowOf(h.store, 't1'))
      return Promise.resolve({ ok: false, kind: 'status', status: 500 })
    }

    await h.flow.send(DEST, [streamSource('cancelled.zip'), streamSource('refused.zip')])

    expect(h.said.said).toEqual(['refused.zip: the server refused the body (500)'])
    expect(h.store.transfer('t1')?.phase).toBe('unsettled')
    expect(h.store.transfer('t2')?.phase).toBe('failed')
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

// ── The batch exists before the first byte does (nocx-hbdw4.6) ───────────
//
// The owner dropped two files. One uploaded; the second was nowhere — not
// in the list, not counted, not cancellable — until the first had finished.
// The send is sequential by design (§4) and stays that way; what was
// missing is that the waiting half was not represented anywhere.
describe('a multi-file drop, from the moment it is dropped', () => {
  it('registers every file BEFORE the first one is sent', async () => {
    const h = harness()
    // Counted at the moment files.upload is entered, which is the only
    // place the question can be asked: after the call it is trivially true
    // for the file being sent and says nothing about the others.
    const rowsAtFirstCall: number[] = []
    const services = h.services
    services.upload = (req) => {
      rowsAtFirstCall.push(h.store.transfers().length)
      services.uploads.push(req)
      return Promise.resolve(services.nextResult.shift() ?? started('t1'))
    }
    h.services.nextResult = [started('t1'), started('t2'), started('t3')]

    await h.flow.send(DEST, [streamSource('a.txt'), streamSource('b.txt'), streamSource('c.txt')])

    expect(rowsAtFirstCall[0]).toBe(3)
  })

  it('shows three rows for a three-file drop, every one of them waiting', async () => {
    const h = harness()
    // The first call never answers, so the batch is caught before anything
    // has started — including the file whose own call is in flight, which
    // is queued precisely because nothing has happened to it yet. No timer
    // and no duration: the state is observed, never waited for.
    let release: (r: { transferId: string }) => void = () => {}
    h.services.upload = () => new Promise((resolve) => (release = resolve))

    const sending = h.flow.send(DEST, [
      streamSource('a.txt'),
      streamSource('b.txt'),
      streamSource('c.txt'),
    ])

    expect(h.store.transfers().map((t) => [t.name, t.phase])).toEqual([
      ['a.txt', 'queued'],
      ['b.txt', 'queued'],
      ['c.txt', 'queued'],
    ])
    // And the sizes are known, which is what lets the aggregate bar be
    // about the batch rather than about whichever file is moving.
    expect(h.store.transfers().every((t) => t.size === 4)).toBe(true)

    // Distinct ids for the rest: one transfer, one row, and a fixture that
    // reused an id would be asking the store to fold two files into one.
    let next = 1
    h.services.upload = () => Promise.resolve(started(`t${++next}`))
    release(started('t1'))
    await sending
    expect(h.store.transfers()).toHaveLength(3)
  })
})

describe('taking one file out of a batch that is already running', () => {
  /** A batch caught mid-flight: `a.txt` is sending its body, `b.txt` and
   *  `c.txt` are queued behind it, and `act` runs at that exact moment. */
  async function midBatch(
    h: ReturnType<typeof harness>,
    act: (rows: string[]) => void,
  ): Promise<void> {
    h.services.nextResult = [waiting('t1', 'k1'), waiting('t2', 'k2'), waiting('t3', 'k3')]
    let first = true
    h.services.sendBody = () => {
      if (first) {
        first = false
        act(h.store.transfers().map((t) => t.id))
      }
      return Promise.resolve({ ok: true })
    }
    await h.flow.send(DEST, [streamSource('a.txt'), streamSource('b.txt'), streamSource('c.txt')])
  }

  it('sends nothing on the wire for the file that had not started', async () => {
    // There is no transfer yet, so there is nothing to cancel. Asserted on
    // the service double rather than by eye: a cancel for a transfer that
    // does not exist is the call this must never make.
    const h = harness()
    await midBatch(h, (rows) => h.store.cancel(rows[2]))
    expect(h.services.cancels).toEqual([])
    // And the call that would have created it never happened either.
    expect(h.services.uploads.map((u) => u.name)).toEqual(['a.txt', 'b.txt'])
  })

  it('leaves the rest of the batch alone — one press stops one file', async () => {
    const h = harness()
    await midBatch(h, (rows) => h.store.cancel(rows[2]))
    expect(h.store.transfers().map((t) => t.name)).toEqual(['a.txt', 'b.txt'])
    expect(h.store.transfer('t1')?.phase).toBe('running')
    expect(h.store.transfer('t2')?.phase).toBe('running')
  })

  it('does not silently drop the rest when the RUNNING file is the one cancelled', async () => {
    // The other half of the same rule, and the reason it is written both
    // ways: what is not defensible is that the batch's fate depends on
    // which row somebody happened to press.
    const h = harness()
    await midBatch(h, (rows) => h.store.cancel(rows[0]))
    expect(h.services.cancels).toEqual(['t1'])
    expect(h.services.uploads.map((u) => u.name)).toEqual(['a.txt', 'b.txt', 'c.txt'])
    expect(h.store.transfers().map((t) => t.name)).toEqual(['a.txt', 'b.txt', 'c.txt'])
  })

  it('stops the transfer that comes back for a file cancelled mid-call', async () => {
    // The window the sequential send opens: the row is queued for as long
    // as its own files.upload call takes. A transfer created after the
    // person left would otherwise run on the server with no row.
    const h = harness()
    let release: (r: { transferId: string }) => void = () => {}
    h.services.upload = () => new Promise((resolve) => (release = resolve))
    const sending = h.flow.send(DEST, [streamSource('a.txt')])
    h.store.cancel(h.store.transfers()[0].id)
    h.services.upload = () => Promise.reject(new Error('no more files'))
    release(started('t1'))
    await sending
    expect(h.services.cancels).toEqual(['t1'])
    // No body either: the transfer is not ours to feed any more.
    expect(h.services.bodies).toEqual([])
  })
})

describe('the files a refusal never got to', () => {
  it('are accounted for rather than left waiting for a turn that is not coming', async () => {
    // The batch still stops — a refusal is about the DESTINATION and one
    // message per remaining file would be noise — but the remaining files
    // are ROWS now, and a row that says "queued" for ever is the product
    // claiming work is coming that nobody is going to do.
    const h = harness()
    h.services.nextResult = []
    await h.flow.send(DEST, [streamSource('a.txt'), streamSource('b.txt'), streamSource('c.txt')])

    expect(h.services.uploads).toHaveLength(1)
    // One message, about the destination, once.
    expect(h.said.said).toHaveLength(1)
    expect(h.said.said[0]).toContain('a.txt')

    const rows = h.store.transfers()
    expect(rows.map((t) => [t.name, t.phase])).toEqual([
      // The one that was asked for and refused is a failure, and reads as
      // one, with the reason under it.
      ['a.txt', 'failed'],
      // The ones behind it were never asked. `skipped` is neutral, so
      // three of them do not read as three failures beside the one real
      // one.
      ['b.txt', 'skipped'],
      ['c.txt', 'skipped'],
    ])
    expect(rows[0].error).toContain('no result queued')
    expect(rows[1].error).toContain('a.txt was refused')
    // Nothing is still outstanding: every row in the batch has an account.
    expect(rows.every((t) => isTerminalPhase(t.phase))).toBe(true)
  })

  it('are accounted for when the refusal comes after the collision question too', async () => {
    // The second call is the one that carries the person's decision, and
    // it can be refused just as the first can.
    const h = harness({ answer: 'overwrite', applyToAll: false })
    h.services.nextResult = [COLLISION]
    await h.flow.send(DEST, [streamSource('a.txt'), streamSource('b.txt')])
    expect(h.store.transfers().map((t) => t.phase)).toEqual(['failed', 'skipped'])
  })
})

describe('what the collision question calls "the remaining files"', () => {
  it('counts what is still in the batch, not what is left in the array', async () => {
    // The apply-to-all offer names a number, and a person who took two
    // files out of a five-file drop would otherwise be offered an answer
    // "for the 4 remaining files" when three exist.
    const h = harness({ answer: 'overwrite', applyToAll: false })
    h.services.nextResult = [
      waiting('t1', 'k1'),
      COLLISION,
      started('t2'),
      started('t3'),
      started('t4'),
    ]
    let first = true
    h.services.sendBody = () => {
      if (first) {
        first = false
        // c.txt and d.txt leave while a.txt is sending; b.txt is next and
        // is the one that collides.
        const rows = h.store.transfers()
        h.store.cancel(rows[2].id)
        h.store.cancel(rows[3].id)
      }
      return Promise.resolve({ ok: true })
    }

    await h.flow.send(DEST, [
      streamSource('a.txt'),
      streamSource('b.txt'),
      streamSource('c.txt'),
      streamSource('d.txt'),
      streamSource('e.txt'),
    ])

    // b.txt and e.txt: the two that are still going to be sent.
    expect(h.person.asked.map((r) => r.remaining)).toEqual([2])
    expect(h.services.uploads.map((u) => u.name)).toEqual(['a.txt', 'b.txt', 'b.txt', 'e.txt'])
  })
})
