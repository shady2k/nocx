// Test fixtures for the upload surface — a fake of the whole backend seam
// and a clock a test moves by hand. Shared by the store's tests, the flow's
// and the drop's, so all three drive the SAME fake: a second fake is a
// second opinion about what the wire does, and the two agree until one of
// them is wrong.
//
// Beside src/panes-fixtures.ts, and for the same reason: a helper a test
// imports belongs in a module, not copied into each spec.
import type { SendBodyOutcome, UploadRequest, UploadServices } from './upload-client'
import type { UploadStore } from './upload-store'
import type { DialogOpenFileForUpload } from '../generated/dialog.openFileForUpload'
import type { FilesDropped } from '../generated/files.dropped'
import type { FilesUploadDone } from '../generated/files.uploadDone'
import type { FilesUploadProgress } from '../generated/files.uploadProgress'
import type { FilesUploadResult } from '../generated/files.upload'

/** A fake of the whole backend surface, plus the two emitters a test needs
 *  to play the wire's notifications by hand. */
export function fakeUploadServices(): UploadServices & {
  emitProgress(p: FilesUploadProgress): void
  emitDone(p: FilesUploadDone): void
  emitDropped(p: FilesDropped): void
  uploads: UploadRequest[]
  cancels: string[]
  bodies: Array<{ url: string; size: number }>
  nextResult: FilesUploadResult[]
  nextSendBody: SendBodyOutcome[]
  nextPick: DialogOpenFileForUpload | null
  pickError: Error | null
  subscriberCount(): number
} {
  const progress = new Set<(p: FilesUploadProgress) => void>()
  const done = new Set<(p: FilesUploadDone) => void>()
  const dropped = new Set<(p: FilesDropped) => void>()
  const api = {
    uploads: [] as UploadRequest[],
    cancels: [] as string[],
    bodies: [] as Array<{ url: string; size: number }>,
    nextResult: [] as FilesUploadResult[],
    nextSendBody: [] as SendBodyOutcome[],
    nextPick: null as DialogOpenFileForUpload | null,
    pickError: null as Error | null,
    upload(req: UploadRequest): Promise<FilesUploadResult> {
      api.uploads.push(req)
      const r = api.nextResult.shift()
      if (r === undefined) return Promise.reject(new Error('no result queued'))
      return Promise.resolve(r)
    },
    cancel(transferId: string) {
      api.cancels.push(transferId)
      return Promise.resolve({})
    },
    sendBody(url: string, body: Blob, size: number): Promise<SendBodyOutcome> {
      api.bodies.push({ url, size })
      return Promise.resolve(api.nextSendBody.shift() ?? { ok: true })
    },
    subscribeProgress(h: (p: FilesUploadProgress) => void) {
      progress.add(h)
      return () => progress.delete(h)
    },
    subscribeDone(h: (p: FilesUploadDone) => void) {
      done.add(h)
      return () => done.delete(h)
    },
    subscribeDropped(h: (p: FilesDropped) => void) {
      dropped.add(h)
      return () => dropped.delete(h)
    },
    pickSource(): Promise<DialogOpenFileForUpload> {
      if (api.pickError !== null) return Promise.reject(api.pickError)
      return Promise.resolve(api.nextPick ?? { sourceTicket: '', name: '', size: 0 })
    },
    emitProgress(p: FilesUploadProgress) {
      for (const h of [...progress]) h(p)
    },
    emitDone(p: FilesUploadDone) {
      for (const h of [...done]) h(p)
    },
    emitDropped(p: FilesDropped) {
      for (const h of [...dropped]) h(p)
    },
    subscriberCount: () => progress.size + done.size + dropped.size,
  }
  return api
}

/** A clock the test moves by hand — a speed derived from a real duration is
 *  a test that depends on timing, which AGENTS.md forbids outright. */
export function fakeClock(): { now: () => number; advance(ms: number): void } {
  let t = 1_000
  return {
    now: () => t,
    advance(ms: number) {
      t += ms
    },
  }
}

/**
 * Seed one RUNNING row the way the flow does it: the file joins the batch,
 * and then files.upload's result gives it the address the wire will use.
 *
 * A helper rather than a call, because those are two steps now and a test
 * about what happens to a transfer in flight should not have to restate
 * them. It answers the ROW's id — which is what every store call takes,
 * since a queued file has a row before it has a transferId.
 */
export function beginTransfer(
  store: UploadStore,
  t: { transferId: string; name: string; destDir: string; machine: string; size: number },
): string {
  const [id] = store.enqueue([
    { name: t.name, destDir: t.destDir, machine: t.machine, size: t.size },
  ])
  store.start(id, t.transferId)
  return id
}
