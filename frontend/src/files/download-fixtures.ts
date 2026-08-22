// Test fixtures for the download surface — a fake of the whole backend seam,
// beside `upload-fixtures.ts` and for its reason: the store's tests, the
// flow's and the operations projection's all drive the SAME fake, because a
// second fake is a second opinion about what the wire does and the two agree
// until one of them is wrong.
//
// The clock is `upload-fixtures.ts`'s, re-exported rather than restated: a
// hand-moved clock is not a per-direction idea, and two of them would drift.
import type { DownloadRequest, DownloadServices } from './download-client'
import type { FilesDownloadDone } from '../generated/files.downloadDone'
import type { FilesDownloadProgress } from '../generated/files.downloadProgress'
import type { FilesDownloadResult } from '../generated/files.download'
import type { DownloadSaver } from './download-save'

export { fakeClock } from './upload-fixtures'

/** A well-formed result, with the wire's own widths — 32 hex for the id and
 *  64 for the ticket — so nothing in a test is testing against a shape the
 *  backend could not send. */
export function downloadResultFixture(
  over: Partial<FilesDownloadResult> = {},
): FilesDownloadResult {
  const ticket = 'a'.repeat(64)
  return {
    transferId: '0'.repeat(32),
    ticket,
    url: `/download/${ticket}`,
    name: 'big.iso',
    size: 400,
    ...over,
  }
}

/** A fake of the whole backend surface, plus the two emitters a test needs
 *  to play the wire's notifications by hand. */
export function fakeDownloadServices(): DownloadServices & {
  emitProgress(p: FilesDownloadProgress): void
  emitDone(p: FilesDownloadDone): void
  downloads: DownloadRequest[]
  cancels: string[]
  nextResult: FilesDownloadResult[]
  /** What `resolveUrl` answers. Null is the real "there is no socket" —
   *  the case the flow has to report rather than guess an origin for. */
  origin: string | null
  subscriberCount(): number
} {
  const progress = new Set<(p: FilesDownloadProgress) => void>()
  const done = new Set<(p: FilesDownloadDone) => void>()
  const api = {
    downloads: [] as DownloadRequest[],
    cancels: [] as string[],
    nextResult: [] as FilesDownloadResult[],
    origin: 'http://127.0.0.1:7331' as string | null,
    download(req: DownloadRequest): Promise<FilesDownloadResult> {
      api.downloads.push(req)
      const r = api.nextResult.shift()
      if (r === undefined) return Promise.reject(new Error('no result queued'))
      return Promise.resolve(r)
    },
    cancel(transferId: string) {
      api.cancels.push(transferId)
      return Promise.resolve({})
    },
    resolveUrl(url: string): string | null {
      if (api.origin === null) return null
      return new URL(url, api.origin).toString()
    },
    subscribeProgress(h: (p: FilesDownloadProgress) => void) {
      progress.add(h)
      return () => progress.delete(h)
    },
    subscribeDone(h: (p: FilesDownloadDone) => void) {
      done.add(h)
      return () => done.delete(h)
    },
    emitProgress(p: FilesDownloadProgress) {
      for (const h of [...progress]) h(p)
    },
    emitDone(p: FilesDownloadDone) {
      for (const h of [...done]) h(p)
    },
    subscriberCount: () => progress.size + done.size,
  }
  return api
}

/** A saver that records the URLs it was handed instead of navigating. The
 *  real one clicks an anchor, which jsdom cannot follow and a test must not
 *  want it to: what the flow owes is "the platform was asked, exactly once,
 *  with the resolved URL". */
export function fakeSaver(): DownloadSaver & { saved: string[] } {
  const saved: string[] = []
  return {
    saved,
    save(url: string) {
      saved.push(url)
    },
  }
}
